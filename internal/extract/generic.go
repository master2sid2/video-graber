package extract

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"

	"videograber/internal/netx"
)

// Generic extracts media from arbitrary pages using static HTML analysis,
// iframe recursion, Playerjs configs and (optionally) a headless browser.
type Generic struct {
	Client *netx.Client
	// Specific returns a site specific extractor for embedded player URLs.
	Specific func(u *url.URL) Extractor
	// Browser is used as a last resort when static analysis finds nothing.
	Browser *Browser
	Log     *slog.Logger
}

// Name implements Extractor.
func (g *Generic) Name() string { return "generic" }

// Match implements Extractor; the generic extractor accepts everything.
func (g *Generic) Match(*url.URL) bool { return true }

// found is what was located for one player/page.
type found struct {
	stream   *Stream
	tree     *pjsPlaylist
	treeBase *url.URL
	headers  Headers
	subs     []Track
	delegate *Result
	modules  []string // analysis modules involved
}

const maxIframeDepth = 3

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func mediaHeaders(pageURL string) Headers {
	return Headers{"Referer": pageURL, "Origin": originOf(pageURL)}
}

func isDirectMedia(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".m3u8", ".mpd", ".mp4", ".webm", ".mkv", ".m4v", ".mov", ".mp3", ".m4a", ".ogg", ".opus", ".flac", ".wav", ".ts":
		return true
	}
	return false
}

func (g *Generic) fetchPage(ctx context.Context, rawURL, referer string) (*page, *netx.Response, error) {
	h := Headers{}
	if referer != "" {
		h["Referer"] = referer
	}
	resp, err := g.Client.Get(ctx, rawURL, h)
	if err != nil {
		return nil, nil, err
	}
	return analyzePage(resp.Body, resp.URL), resp, nil
}

// Extract implements Extractor.
func (g *Generic) Extract(ctx context.Context, pageURL string, opts Options) (*Result, error) {
	if isDirectMedia(pageURL) {
		return g.direct(ctx, pageURL, opts.Referer)
	}
	p, resp, err := g.fetchPage(ctx, pageURL, opts.Referer)
	if err != nil {
		return nil, err
	}
	if resp.Body == nil || strings.Contains(resp.ContentType, "mpegurl") {
		return g.direct(ctx, resp.URL, opts.Referer)
	}

	media := &Media{PageURL: pageURL, Title: p.Title, Thumbnail: p.Thumbnail, Extractor: g.Name(), Duration: p.DurationISO}

	// Choices found directly on the page: episode links and player tabs.
	epURL := map[string]string{}
	for _, e := range p.Episodes {
		id := fmt.Sprintf("e%d", e.Number)
		if e.Season > 0 {
			id = fmt.Sprintf("s%de%d", e.Season, e.Number)
		}
		epURL[id] = e.URL
		c := Choice{ID: id, Label: e.Label, Number: e.Number}
		if e.Season > 0 {
			c.Season = fmt.Sprintf("Season %d", e.Season)
		}
		media.Episodes = append(media.Episodes, c)
		if sameURL(e.URL, p.URL) || sameURL(e.URL, pageURL) {
			media.Selected.Episode = id
		}
	}
	media.Translations = playerChoices(p.Players)
	for _, pl := range p.Players {
		if pl.Selected && len(media.Translations) > 0 {
			media.Selected.Translation = slug(pl.Label)
		}
	}

	hasOwnPlayer := len(p.Candidates) > 0 || len(p.Iframes) > 0 || len(p.Players) > 0 || p.Playlist != nil

	// When the page links to episodes but is not one of them, while its own
	// player carries a complete episode list, the player's list wins.
	var pre *found
	if len(media.Episodes) > 0 && media.Selected.Episode == "" && hasOwnPlayer && len(media.Translations) == 0 {
		if f, err := g.findOnPage(ctx, p, 0); err == nil && playerHasEpisodes(f) {
			pre = f
			media.Episodes, epURL = nil, map[string]string{}
		}
	}
	if len(media.Episodes) > 0 && media.Selected.Episode == "" && !hasOwnPlayer {
		media.Selected.Episode = media.Episodes[0].ID
	}

	// No page level choices: inherit them from what the player provides.
	if len(media.Episodes) == 0 && len(media.Translations) == 0 {
		f := pre
		if f == nil {
			var err error
			if f, err = g.findOnPage(ctx, p, 0); err != nil {
				return nil, err
			}
		}
		media.Modules = mergeNames(p.Modules, f.modules)
		switch {
		case f.delegate != nil:
			d := f.delegate
			media.Translations, media.Episodes, media.Selected = d.Media.Translations, d.Media.Episodes, d.Media.Selected
			media.Extractor = g.Name() + "+" + d.Media.Extractor
			media.Title = cmp.Or(media.Title, d.Media.Title)
			media.Thumbnail = cmp.Or(media.Thumbnail, d.Media.Thumbnail)
			media.Notes = append(media.Notes, d.Media.Notes...)
			return &Result{Media: media, Resolve: d.Resolve, Stream: d.Stream}, nil
		case f.tree != nil:
			media.Translations, media.Episodes, _ = f.tree.choices()
			first := f.tree.Leaves[0]
			_, _, keyOf := f.tree.choices()
			media.Selected.Translation, media.Selected.Episode = keyOf(first)
			resolve := func(ctx context.Context, sel Selection) (*Stream, error) { return g.streamFromFound(ctx, f, sel) }
			st, err := resolve(ctx, media.Selected)
			if err != nil {
				return nil, err
			}
			return &Result{Media: media, Resolve: resolve, Stream: st}, nil
		default:
			st := f.stream
			st.via = mergeNames(st.via, f.modules)
			if st.Duration == 0 {
				st.Duration = media.Duration
			}
			return &Result{Media: media, Stream: st, Resolve: func(context.Context, Selection) (*Stream, error) { return st, nil }}, nil
		}
	}

	// Page level choices: every selection may need fetching another page.
	var mu sync.Mutex
	cache := map[string]*page{p.URL: p, pageURL: p}
	pageFor := func(ctx context.Context, u string) (*page, error) {
		mu.Lock()
		cp := cache[u]
		mu.Unlock()
		if cp != nil {
			return cp, nil
		}
		np, _, err := g.fetchPage(ctx, u, p.URL)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		cache[u] = np
		mu.Unlock()
		return np, nil
	}
	resolve := func(ctx context.Context, sel Selection) (*Stream, error) {
		pg := p
		if u, ok := epURL[sel.Episode]; ok {
			var err error
			if pg, err = pageFor(ctx, u); err != nil {
				return nil, err
			}
		}
		if len(pg.Players) > 0 && (len(pg.Players) > 1 || len(pg.Iframes) == 0) {
			pl := pickPlayer(pg.Players, sel.Translation)
			return g.resolvePlayer(ctx, pl.URL, pg.URL)
		}
		f, err := g.findOnPage(ctx, pg, 0)
		if err != nil {
			return nil, err
		}
		return g.streamFromFound(ctx, f, Selection{})
	}
	st, err := resolve(ctx, media.Selected)
	if err != nil {
		return nil, err
	}
	media.Modules = mergeNames(p.Modules, st.via)
	if st.Duration == 0 {
		st.Duration = media.Duration
	}
	// A show page usually lists episodes while voice-over tabs live on the
	// episode pages: borrow them from the selected episode.
	if len(media.Translations) == 0 {
		if u, ok := epURL[media.Selected.Episode]; ok {
			if ep, err := pageFor(ctx, u); err == nil {
				media.Translations = playerChoices(ep.Players)
				for _, pl := range ep.Players {
					if pl.Selected && len(media.Translations) > 0 {
						media.Selected.Translation = slug(pl.Label)
					}
				}
			}
		}
	}
	return &Result{Media: media, Resolve: resolve, Stream: st}, nil
}

func sameURL(a, b string) bool {
	norm := func(s string) string {
		s, _, _ = strings.Cut(s, "#")
		return strings.TrimSuffix(s, "/")
	}
	return norm(a) == norm(b)
}

func playerChoices(players []playerLink) []Choice {
	if len(players) < 2 {
		return nil
	}
	var out []Choice
	seen := map[string]bool{}
	for i, pl := range players {
		label := cmp.Or(pl.Label, fmt.Sprintf("Player %d", i+1))
		id := slug(label)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Choice{ID: id, Label: label})
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func pickPlayer(players []playerLink, translation string) playerLink {
	if translation != "" {
		for _, pl := range players {
			if slug(pl.Label) == translation {
				return pl
			}
		}
	}
	for _, pl := range players {
		if pl.Selected {
			return pl
		}
	}
	return players[0]
}

// resolvePlayer fetches an embedded player page and returns its default stream.
func (g *Generic) resolvePlayer(ctx context.Context, playerURL, referer string) (*Stream, error) {
	if u, err := url.Parse(playerURL); err == nil && g.Specific != nil {
		if ext := g.Specific(u); ext != nil {
			res, err := ext.Extract(ctx, playerURL, Options{Referer: referer})
			if err != nil {
				return nil, err
			}
			if res.Stream != nil {
				return res.Stream, nil
			}
			return res.Resolve(ctx, res.Media.Selected)
		}
	}
	if isDirectMedia(playerURL) {
		return g.streamFromCandidates(ctx, []candidate{{URL: playerURL, Proto: protoFor(playerURL)}}, mediaHeaders(referer), nil)
	}
	p, _, err := g.fetchPage(ctx, playerURL, referer)
	if err != nil {
		return nil, err
	}
	f, err := g.findOnPage(ctx, p, 1)
	if err != nil {
		return nil, err
	}
	return g.streamFromFound(ctx, f, Selection{})
}

// findOnPage looks for media on a page, following iframes when needed.
func (g *Generic) findOnPage(ctx context.Context, p *page, depth int) (*found, error) {
	headers := mediaHeaders(p.URL)
	base, _ := url.Parse(p.URL)

	if p.Playlist != nil {
		f := &found{tree: p.Playlist, treeBase: base, headers: headers, subs: p.Subtitles, modules: mergeNames(p.Modules, []string{p.Playlist.Via})}
		if len(p.Playlist.Leaves) == 1 {
			st, err := g.streamFromFound(ctx, f, Selection{})
			if err == nil {
				return &found{stream: st, modules: f.modules}, nil
			}
			g.Log.Debug("playerjs source failed", "url", p.URL, "err", err)
		} else {
			return f, nil
		}
	}
	if len(p.Candidates) > 0 {
		st, err := g.streamFromCandidates(ctx, p.Candidates, headers, p.Subtitles)
		if err == nil {
			return &found{stream: st, modules: mergeNames(p.Modules, []string{p.Candidates[0].Via})}, nil
		}
		g.Log.Debug("page candidates failed", "url", p.URL, "err", err)
	}

	frames := slices.Clone(p.Iframes)
	if len(frames) == 0 && len(p.Players) > 0 {
		frames = append(frames, pickPlayer(p.Players, "").URL)
	}
	// Several embedded players: try them all (up to 4) and keep the one with
	// the longest episode list; the first one wins ties.
	var errs []error
	var best *found
	bestEps := -1
	consider := func(f *found) {
		n := 0
		switch {
		case f.tree != nil:
			_, eps, _ := f.tree.choices()
			n = len(eps)
		case f.delegate != nil:
			n = len(f.delegate.Media.Episodes)
		}
		if n > bestEps {
			best, bestEps = f, n
		}
	}
	for i, fr := range frames {
		if i >= 4 || ctx.Err() != nil {
			break
		}
		u, err := url.Parse(fr)
		if err != nil {
			continue
		}
		if g.Specific != nil {
			if ext := g.Specific(u); ext != nil {
				res, err := ext.Extract(ctx, fr, Options{Referer: p.URL})
				if err == nil {
					consider(&found{delegate: res, modules: mergeNames(p.Modules, []string{ext.Name()})})
					continue
				}
				errs = append(errs, fmt.Errorf("%s: %w", ext.Name(), err))
				continue
			}
		}
		if depth >= maxIframeDepth {
			continue
		}
		if isDirectMedia(fr) {
			if st, err := g.streamFromCandidates(ctx, []candidate{{URL: fr, Proto: protoFor(fr)}}, headers, nil); err == nil {
				consider(&found{stream: st, modules: mergeNames(p.Modules)})
			}
			continue
		}
		sub, _, err := g.fetchPage(ctx, fr, p.URL)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if f, err := g.findOnPage(ctx, sub, depth+1); err == nil {
			f.modules = mergeNames(p.Modules, f.modules)
			consider(f)
		} else {
			errs = append(errs, err)
		}
	}
	if best != nil {
		return best, nil
	}

	if g.Browser != nil && depth == 0 {
		g.Log.Info("static analysis found nothing, trying headless browser", "url", p.URL)
		cands, hdr, err := g.Browser.Capture(ctx, p.URL)
		if err == nil && len(cands) > 0 {
			st, err := g.streamFromCandidates(ctx, cands, hdr, p.Subtitles)
			if err == nil {
				return &found{stream: st, modules: mergeNames(p.Modules, []string{"browser"})}, nil
			}
			errs = append(errs, err)
		} else if err != nil {
			errs = append(errs, fmt.Errorf("browser: %w", err))
		}
	}

	if p.Encrypted {
		errs = append(errs, errors.New("the player configuration is obfuscated; the headless browser (-chrome) may handle it"))
	}
	return nil, errors.Join(append([]error{ErrNoMedia}, errs...)...)
}

func (g *Generic) streamFromFound(ctx context.Context, f *found, sel Selection) (*Stream, error) {
	st, err := g.streamFromFound1(ctx, f, sel)
	if err == nil && st != nil {
		st.via = mergeNames(st.via, f.modules)
	}
	return st, err
}

func (g *Generic) streamFromFound1(ctx context.Context, f *found, sel Selection) (*Stream, error) {
	switch {
	case f.stream != nil:
		return f.stream, nil
	case f.delegate != nil:
		if f.delegate.Stream != nil && sel == (Selection{}) {
			return f.delegate.Stream, nil
		}
		if sel == (Selection{}) {
			sel = f.delegate.Media.Selected
		}
		return f.delegate.Resolve(ctx, sel)
	case f.tree != nil:
		leaf := f.tree.pick(sel)
		items := parseLabeledList(leaf.File, f.treeBase)
		subs := slices.Clone(f.subs)
		for _, s := range parseLabeledList(leaf.Subtitle, f.treeBase) {
			subs = append(subs, Track{Label: cmp.Or(s.Label, "Subtitles"), URL: s.URL, Protocol: ProtoHTTP})
		}
		var cands []candidate
		for _, it := range items {
			cands = append(cands, candidate{URL: it.URL, Label: it.Label, Height: heightFrom(it.Label + " " + it.URL), Proto: protoFor(it.URL)})
		}
		return g.streamFromCandidates(ctx, cands, f.headers, subs)
	}
	return nil, ErrNoMedia
}

func (g *Generic) direct(ctx context.Context, rawURL, referer string) (*Result, error) {
	h := Headers{}
	if referer != "" {
		h = mediaHeaders(referer)
	}
	st, err := g.streamFromCandidates(ctx, []candidate{{URL: rawURL, Proto: protoFor(rawURL)}}, h, nil)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(rawURL)
	title := strings.TrimSuffix(path.Base(u.Path), path.Ext(u.Path))
	if title == "index" || title == "master" || title == "playlist" {
		title = path.Base(path.Dir(u.Path))
	}
	media := &Media{PageURL: rawURL, Title: title, Extractor: "direct", Duration: st.Duration, Modules: []string{"direct-link"}}
	return &Result{Media: media, Stream: st, Resolve: func(context.Context, Selection) (*Stream, error) { return st, nil }}, nil
}

// streamFromCandidates turns a set of media URLs into a Stream.
func (g *Generic) streamFromCandidates(ctx context.Context, cands []candidate, headers Headers, subs []Track) (*Stream, error) {
	if len(cands) > 1 && labeledSet(cands) {
		return g.streamFromQualityList(ctx, cands, headers, subs)
	}
	var hls, dash, files []candidate
	sniffed := 0
	for i := range cands {
		c := &cands[i]
		// Playlists served by scripts (playlist.php?…) carry no extension.
		if c.Proto == ProtoHTTP && !hasMediaExt(c.URL) && sniffed < 4 {
			sniffed++
			c.Proto = g.sniff(ctx, c.URL, headers)
		}
	}
	for _, c := range cands {
		switch c.Proto {
		case ProtoHLS:
			hls = append(hls, c)
		case ProtoDASH:
			dash = append(dash, c)
		default:
			files = append(files, c)
		}
	}
	var st *Stream
	var errs []error
	switch {
	case len(hls) == 1 || len(hls) > 0 && !labeledSet(hls):
		for _, c := range hls {
			s, err := hlsStream(ctx, g.Client, c.URL, headers, c.Label)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			st = s
			break
		}
	case len(hls) > 1:
		// A labelled quality list of HLS playlists ([720p]a.m3u8,[1080p]b.m3u8).
		st = &Stream{}
		for _, c := range hls {
			s, err := hlsStream(ctx, g.Client, c.URL, headers, c.Label)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if len(s.Formats) == 0 {
				continue
			}
			best := s.Formats[0]
			best.Label = cmp.Or(c.Label, best.Label)
			if best.Height == 0 {
				best.Height = c.Height
			}
			st.Formats = append(st.Formats, best)
			st.Duration = max(st.Duration, s.Duration)
			if len(st.Audio) == 0 {
				st.Audio = s.Audio
			}
		}
		sortFormats(st.Formats)
		assignFormatIDs(st.Formats, "hls")
	}
	if st == nil && len(dash) > 0 {
		st = &Stream{Formats: []Format{{ID: "dash", Label: "Auto (DASH)", Protocol: ProtoDASH, URL: dash[0].URL, Headers: headers, HasVideo: true, HasAudio: true}}}
	}
	if st == nil && len(files) > 0 {
		st = &Stream{}
		for _, c := range files {
			ext := strings.TrimPrefix(strings.ToLower(path.Ext(strings.SplitN(c.URL, "?", 2)[0])), ".")
			audioOnly := slices.Contains([]string{"mp3", "m4a", "ogg", "opus", "flac", "wav"}, ext)
			f := Format{Height: c.Height, Protocol: ProtoHTTP, URL: c.URL, Headers: headers, Ext: ext, HasVideo: !audioOnly, HasAudio: true}
			f.Label = cmp.Or(c.Label, qualityLabel(c.Height, 0))
			if f.Label == "Auto" && ext != "" {
				f.Label = strings.ToUpper(ext)
			}
			st.Formats = append(st.Formats, f)
		}
		sortFormats(st.Formats)
		assignFormatIDs(st.Formats, "file")
	}
	if st == nil || len(st.Formats) == 0 {
		return nil, errors.Join(append([]error{ErrNoMedia}, errs...)...)
	}
	seen := map[string]bool{}
	for _, s := range subs {
		if s.URL == "" || seen[s.URL] {
			continue
		}
		seen[s.URL] = true
		s.ID = fmt.Sprintf("x%d", len(st.Subtitles))
		s.Headers = headers
		st.Subtitles = append(st.Subtitles, s)
	}
	return st, nil
}

func labeledSet(c []candidate) bool {
	for _, x := range c {
		if x.Label == "" {
			return false
		}
	}
	return true
}

// sniff fetches a URL without a known media extension and decides by its
// content whether it is an HLS playlist, a DASH manifest or a plain file.
func (g *Generic) sniff(ctx context.Context, rawURL string, headers Headers) Protocol {
	resp, err := g.Client.Get(ctx, rawURL, headers)
	if err != nil || resp.Body == nil {
		g.Log.Debug("sniff failed", "url", rawURL, "err", err)
		return ProtoHTTP
	}
	head := strings.TrimSpace(string(resp.Body[:min(len(resp.Body), 512)]))
	head = strings.TrimPrefix(head, "\ufeff")
	switch {
	case strings.HasPrefix(head, "#EXTM3U") || strings.Contains(resp.ContentType, "mpegurl"):
		return ProtoHLS
	case strings.Contains(head, "<MPD") || strings.Contains(resp.ContentType, "dash+xml"):
		return ProtoDASH
	}
	return ProtoHTTP
}

// playerHasEpisodes reports whether a found player offers several episodes.
func playerHasEpisodes(f *found) bool {
	switch {
	case f.tree != nil:
		_, eps, _ := f.tree.choices()
		return len(eps) > 1
	case f.delegate != nil:
		return len(f.delegate.Media.Episodes) > 1
	}
	return false
}

// streamFromQualityList handles an explicit list of labelled qualities
// ([360p]a,[720p]b): every entry becomes one format. Entries may use
// different protocols, and "720.mp4" may well be an HLS playlist, so entries
// are sniffed when the list mixes protocols.
func (g *Generic) streamFromQualityList(ctx context.Context, cands []candidate, headers Headers, subs []Track) (*Stream, error) {
	mixed := slices.ContainsFunc(cands, func(c candidate) bool { return c.Proto != cands[0].Proto })
	st := &Stream{}
	var errs []error
	for _, c := range cands {
		if c.Proto == ProtoHTTP && (mixed || !hasMediaExt(c.URL)) {
			c.Proto = g.sniff(ctx, c.URL, headers)
		}
		height := cmp.Or(c.Height, heightFrom(c.Label))
		switch c.Proto {
		case ProtoHLS:
			s, err := hlsStream(ctx, g.Client, c.URL, headers, c.Label)
			if err != nil || len(s.Formats) == 0 {
				errs = append(errs, err)
				continue
			}
			f := s.Formats[0]
			f.Label = cmp.Or(c.Label, f.Label)
			f.Height = cmp.Or(f.Height, height)
			st.Formats = append(st.Formats, f)
			st.Duration = max(st.Duration, s.Duration)
			if len(st.Audio) == 0 {
				st.Audio = s.Audio
			}
		case ProtoDASH:
			st.Formats = append(st.Formats, Format{Label: c.Label, Height: height, Protocol: ProtoDASH, URL: c.URL, Headers: headers, HasVideo: true, HasAudio: true})
		default:
			st.Formats = append(st.Formats, Format{Label: c.Label, Height: height, Protocol: ProtoHTTP, URL: c.URL, Headers: headers, HasVideo: true, HasAudio: true,
				Ext: strings.TrimPrefix(strings.ToLower(path.Ext(strings.SplitN(c.URL, "?", 2)[0])), ".")})
		}
	}
	if len(st.Formats) == 0 {
		return nil, errors.Join(append([]error{ErrNoMedia}, errs...)...)
	}
	sortFormats(st.Formats)
	assignFormatIDs(st.Formats, "q")
	for _, s := range subs {
		if s.URL != "" {
			s.ID = fmt.Sprintf("x%d", len(st.Subtitles))
			s.Headers = headers
			st.Subtitles = append(st.Subtitles, s)
		}
	}
	return st, nil
}

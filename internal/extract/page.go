package extract

import (
	"bytes"
	"cmp"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// candidate is a media URL found on a page.
type candidate struct {
	URL    string
	Label  string
	Height int
	Proto  Protocol
	Score  int
}

// playerLink is an element pointing to an embedded player (e.g. a voice-over tab).
type playerLink struct {
	Label    string
	URL      string
	Selected bool
}

// episodeLink is a link to another page holding one episode.
type episodeLink struct {
	Label  string
	URL    string
	Number int
	Season int
	group  string
}

// page is the result of statically analysing one HTML document.
type page struct {
	URL         string
	Title       string
	Thumbnail   string
	Candidates  []candidate
	Iframes     []string
	Players     []playerLink
	Episodes    []episodeLink
	Subtitles   []Track
	Playlist    *pjsPlaylist
	Encrypted   bool // a Playerjs config is obfuscated
	DurationISO float64
}

var (
	reMediaURL = regexp.MustCompile(`(?i)(?:https?:)?//[^\s"'<>\\(){}\[\]]+?\.(m3u8|mpd|mp4|webm|mkv|m4v|mov)(?:\?[^\s"'<>\\(){}\[\]]*)?`)
	reEpisode  = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(\d{1,4})\s*(?:-?(?:я|й|ая))?\s*(?:серия|серии|эпизод|episode|ep\.?\b|series\b|bölüm|화|話)`),
		regexp.MustCompile(`(?i)(?:серия|эпизод|episode|ep\.?|e)\s*#?\s*(\d{1,4})\b`),
	}
	reSeason   = regexp.MustCompile(`(?i)(\d{1,3})\s*(?:-?й)?\s*(?:сезон|season)|(?:сезон|season|s)\s*(\d{1,3})\b`)
	reHeight   = regexp.MustCompile(`(?i)(?:^|[^\d])(2160|1440|1080|720|576|480|360|240|144)(?:p|[^\d]|$)`)
	reISODur   = regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?$`)
	playerAttr = []string{"data-url-player", "data-player-url", "data-player", "data-iframe", "data-iframe-src", "data-embed", "data-link", "data-url"}
)

// unescapeJS undoes the escaping commonly found in inline scripts and attributes.
func unescapeJS(s string) string {
	return strings.NewReplacer(`\/`, "/", `&`, "&", `/`, "/", `/`, "/", "&amp;", "&", `\x26`, "&").Replace(s)
}

func absURL(base *url.URL, ref string) string {
	ref = strings.TrimSpace(html.UnescapeString(ref))
	if ref == "" || strings.HasPrefix(ref, "javascript:") || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	u := base.ResolveReference(r)
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

func protoFor(u string) Protocol {
	p := strings.ToLower(u)
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	switch {
	case strings.HasSuffix(p, ".m3u8") || strings.Contains(p, ".m3u8:") || strings.Contains(p, "/hls/") && !strings.Contains(p, "."):
		return ProtoHLS
	case strings.HasSuffix(p, ".mpd"):
		return ProtoDASH
	}
	return ProtoHTTP
}

func heightFrom(s string) int {
	if m := reHeight.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		return h
	}
	if strings.Contains(strings.ToLower(s), "4k") {
		return 2160
	}
	return 0
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *xhtml.Node, key string) bool {
	return slices.ContainsFunc(n.Attr, func(a xhtml.Attribute) bool { return a.Key == key })
}

func textOf(n *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		if n.Type == xhtml.ElementNode && (n.DataAtom == atom.Script || n.DataAtom == atom.Style) {
			return
		}
		for c := range n.ChildNodes() {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func isSelected(n *xhtml.Node) bool {
	if attr(n, "aria-selected") == "true" || hasAttr(n, "selected") || attr(n, "aria-current") != "" {
		return true
	}
	for c := range strings.FieldsSeq(attr(n, "class")) {
		switch strings.ToLower(c) {
		case "active", "selected", "current", "is-active", "is-selected":
			return true
		}
		if strings.HasSuffix(c, "_active") || strings.HasSuffix(c, "--active") || strings.HasSuffix(c, "-active") {
			return true
		}
	}
	return false
}

func looksLikeURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "//")
}

func isImageURL(s string) bool {
	p := strings.ToLower(s)
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".avif", ".ico", ".css", ".js"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

// analyzePage parses an HTML document and collects everything of interest.
func analyzePage(body []byte, pageURL string) *page {
	base, _ := url.Parse(pageURL)
	p := &page{URL: pageURL}
	doc, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return p
	}

	var ogTitle, docTitle, h1 string
	seenCand := map[string]bool{}
	addCand := func(raw, label string, score int) {
		u := absURL(base, unescapeJS(raw))
		if u == "" || seenCand[u] || isImageURL(u) {
			return
		}
		seenCand[u] = true
		p.Candidates = append(p.Candidates, candidate{URL: u, Label: label, Height: heightFrom(label + " " + u), Proto: protoFor(u), Score: score})
	}
	seenIframe := map[string]bool{}
	addIframe := func(raw string) {
		u := absURL(base, unescapeJS(raw))
		if u == "" || seenIframe[u] || isImageURL(u) || isAdURL(u) {
			return
		}
		seenIframe[u] = true
		p.Iframes = append(p.Iframes, u)
	}
	seenPlayer := map[string]bool{}
	seenEp := map[string]bool{}
	var scripts []string

	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			switch n.DataAtom {
			case atom.Title:
				if docTitle == "" {
					docTitle = textOf(n)
				}
			case atom.H1:
				if h1 == "" {
					h1 = textOf(n)
				}
			case atom.Meta:
				prop := strings.ToLower(cmp.Or(attr(n, "property"), attr(n, "name"), attr(n, "itemprop")))
				content := attr(n, "content")
				switch prop {
				case "og:title":
					ogTitle = content
				case "og:image", "twitter:image":
					if p.Thumbnail == "" {
						p.Thumbnail = absURL(base, content)
					}
				case "og:video", "og:video:url", "og:video:secure_url", "twitter:player:stream", "contenturl":
					if !strings.Contains(content, ".swf") {
						if protoFor(content) != ProtoHTTP || reMediaURL.MatchString(content) {
							addCand(content, "", 30)
						}
					}
				case "duration":
					if p.DurationISO == 0 {
						p.DurationISO = parseISODuration(content)
					}
				}
			case atom.Video, atom.Audio, atom.Source:
				for _, k := range []string{"src", "data-src"} {
					if v := attr(n, k); v != "" && !strings.HasPrefix(v, "blob:") {
						addCand(v, cmp.Or(attr(n, "label"), attr(n, "title"), attr(n, "size"), attr(n, "res")), 50)
					}
				}
			case atom.Track:
				if src := absURL(base, attr(n, "src")); src != "" {
					kind := strings.ToLower(attr(n, "kind"))
					if kind == "" || kind == "subtitles" || kind == "captions" {
						p.Subtitles = append(p.Subtitles, Track{
							Label: cmp.Or(attr(n, "label"), attr(n, "srclang"), "Subtitles"), Lang: attr(n, "srclang"),
							URL: src, Protocol: ProtoHTTP,
						})
					}
				}
			case atom.Iframe, atom.Frame, atom.Embed:
				for _, k := range []string{"src", "data-src", "data-lazy-src", "data-original"} {
					if v := attr(n, k); v != "" && v != "about:blank" {
						addIframe(v)
					}
				}
			case atom.Script:
				if strings.Contains(attr(n, "type"), "ld+json") {
					p.parseJSONLD(textOf(n), addCand)
				}
				if n.FirstChild != nil {
					scripts = append(scripts, n.FirstChild.Data)
				}
			case atom.A, atom.Option:
				href := attr(n, "href")
				if n.DataAtom == atom.Option {
					href = attr(n, "value")
				}
				label := cmp.Or(attr(n, "title"), textOf(n), attr(n, "data-title"))
				if u := absURL(base, href); u != "" && !seenEp[u] {
					if num, ok := episodeNumber(label); ok {
						seenEp[u] = true
						ep := episodeLink{Label: label, URL: u, Number: num, Season: seasonNumber(label)}
						ep.group = episodeGroup(label)
						p.Episodes = append(p.Episodes, ep)
					}
				}
			}

			// Elements pointing to players: voice-over tabs, server buttons...
			for _, k := range playerAttr {
				v := attr(n, k)
				if v == "" || !looksLikeURL(v) || isImageURL(v) {
					continue
				}
				u := absURL(base, unescapeJS(v))
				if u == "" || seenPlayer[u] {
					continue
				}
				seenPlayer[u] = true
				label := cmp.Or(textOf(n), attr(n, "title"), attr(n, "data-title"), attr(n, "aria-label"))
				p.Players = append(p.Players, playerLink{Label: label, URL: u, Selected: isSelected(n)})
				break
			}
			// Subtitle URLs stored in attributes such as data-ru_subtitle.
			for _, a := range n.Attr {
				if strings.HasSuffix(a.Key, "subtitle") && looksLikeURL(a.Val) {
					lang := strings.TrimSuffix(strings.TrimPrefix(a.Key, "data-"), "_subtitle")
					p.Subtitles = append(p.Subtitles, Track{Label: strings.ToUpper(lang), Lang: lang, URL: absURL(base, a.Val), Protocol: ProtoHTTP})
				}
			}
		}
		for c := range n.ChildNodes() {
			walk(c)
		}
	}
	walk(doc)

	p.Title = strings.TrimSpace(cmp.Or(ogTitle, h1, docTitle))

	// Playerjs configuration (file: "..."), possibly with a playlist.
	raw := unescapeJS(string(body))
	if pl, enc := findPlayerjs(raw, base); pl != nil {
		p.Playlist = pl
	} else if enc {
		p.Encrypted = true
	}

	// Bare media URLs anywhere in the document (scripts, JSON blobs, attributes).
	for _, m := range reMediaURL.FindAllString(raw, -1) {
		score := 10
		if strings.Contains(strings.ToLower(m), ".m3u8") {
			score = 20
		}
		addCand(m, "", score)
	}
	_ = scripts

	p.Episodes = filterEpisodes(p.Episodes, pageURL)
	p.Candidates = slices.DeleteFunc(p.Candidates, func(c candidate) bool { return isAdURL(c.URL) })
	slices.SortStableFunc(p.Candidates, func(a, b candidate) int { return b.Score - a.Score })
	return p
}

func (p *page) parseJSONLD(s string, add func(string, string, int)) {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return
	}
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				switch strings.ToLower(k) {
				case "contenturl":
					if s, ok := val.(string); ok {
						add(s, "", 40)
					}
				case "thumbnailurl":
					if s, ok := val.(string); ok && p.Thumbnail == "" {
						p.Thumbnail = s
					}
				case "duration":
					if s, ok := val.(string); ok && p.DurationISO == 0 {
						p.DurationISO = parseISODuration(s)
					}
				}
				walk(val)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
}

func parseISODuration(s string) float64 {
	m := reISODur.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	se, _ := strconv.Atoi(m[3])
	return float64(h*3600 + mi*60 + se)
}

func episodeNumber(label string) (int, bool) {
	if len(label) > 200 {
		return 0, false
	}
	for _, re := range reEpisode {
		if m := re.FindStringSubmatch(label); m != nil {
			n, err := strconv.Atoi(m[1])
			if err == nil && n > 0 && n < 5000 {
				return n, true
			}
		}
	}
	return 0, false
}

func seasonNumber(label string) int {
	if m := reSeason.FindStringSubmatch(label); m != nil {
		n, _ := strconv.Atoi(cmp.Or(m[1], m[2]))
		return n
	}
	return 0
}

// EpisodeName combines a show title with an episode label for file names:
// "Серия 1" becomes "<title> - Серия 1", while a label that already names the
// show ("Show 2 серия") is kept as is.
func EpisodeName(title, episode string) string {
	title, episode = strings.TrimSpace(title), strings.TrimSpace(episode)
	if episode == "" {
		return title
	}
	if title == "" {
		return episode
	}
	core := episodeGroup(episode)
	letters := 0
	for _, r := range core {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters >= 3 && strings.Contains(strings.ToLower(title), core) {
		return episode
	}
	return title + " - " + episode
}

// episodeGroup strips numbers and episode words so that links belonging to
// the same show share a group key.
func episodeGroup(label string) string {
	s := strings.ToLower(label)
	for _, re := range reEpisode {
		s = re.ReplaceAllString(s, " ")
	}
	s = reSeason.ReplaceAllString(s, " ")
	s = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// filterEpisodes keeps the largest group of same-show episode links.
func filterEpisodes(eps []episodeLink, pageURL string) []episodeLink {
	if len(eps) < 2 {
		return nil
	}
	counts := map[string]int{}
	for _, e := range eps {
		counts[e.group]++
	}
	best, bestN := "", 0
	for g, n := range counts {
		if n > bestN || n == bestN && len(g) > len(best) {
			best, bestN = g, n
		}
	}
	if bestN < 2 {
		return nil
	}
	var out []episodeLink
	seenNum := map[[2]int]bool{}
	for _, e := range eps {
		key := [2]int{e.Season, e.Number}
		if e.group != best || seenNum[key] {
			continue
		}
		seenNum[key] = true
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b episodeLink) int {
		if a.Season != b.Season {
			return a.Season - b.Season
		}
		return a.Number - b.Number
	})
	_ = pageURL
	return out
}

var adHosts = []string{
	"doubleclick.net", "googlesyndication.com", "googleadservices.com", "google-analytics.com", "googletagmanager.com",
	"yandex.ru/ads", "an.yandex.ru", "mc.yandex.ru", "adfox.ru", "adriver.ru", "vk.com/rtrg", "facebook.com/tr",
	"youtube.com/subscribe", "disqus.com", "mail.ru/counter", "top-fwz1.mail.ru", "betweendigital.com", "hit.gemius",
}

func isAdURL(u string) bool {
	l := strings.ToLower(u)
	for _, h := range adHosts {
		if strings.Contains(l, h) {
			return true
		}
	}
	for _, w := range []string{"/ads/", "/ad/", "vast", "preroll", "/banner", "adserver", "/promo"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

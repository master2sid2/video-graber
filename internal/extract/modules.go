package extract

import (
	"encoding/json"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Module recognises one way in which web pages expose video. The generic
// extractor runs every module on each analysed document (the page itself and
// every embedded player it follows). Modules only contribute findings to the
// page: media candidates, playlists, episode links, voice-over tabs, iframes.
//
// New page layouts are supported by adding a module here; site specific
// logic (APIs, signatures) belongs into an Extractor instead.
type Module interface {
	Name() string
	Analyze(d *doc, p *page)
}

type moduleFunc struct {
	name string
	fn   func(d *doc, p *page)
}

func (m moduleFunc) Name() string            { return m.name }
func (m moduleFunc) Analyze(d *doc, p *page) { m.fn(d, p) }

// Modules is the ordered list of analysis modules.
var Modules = []Module{
	moduleFunc{"metadata", modMetadata},
	moduleFunc{"html5-video", modHTML5},
	moduleFunc{"json-ld", modJSONLD},
	moduleFunc{"iframes", modIframes},
	moduleFunc{"player-tabs", modPlayerTabs},
	moduleFunc{"episode-links", modEpisodeLinks},
	moduleFunc{"subtitle-attributes", modSubtitleAttrs},
	moduleFunc{"playerjs", modPlayerjs},
	moduleFunc{"js-playlist", modJSPlaylist},
	moduleFunc{"media-urls", modMediaURLs},
}

// ModuleNames lists the names of the analysis modules that find media.
func ModuleNames() []string {
	var out []string
	for _, m := range Modules {
		if m.Name() != "metadata" && m.Name() != "subtitle-attributes" {
			out = append(out, m.Name())
		}
	}
	return out
}

// ── metadata: title, poster, duration, OpenGraph video ─────────────────────

func modMetadata(d *doc, p *page) {
	var og, h1, title string
	d.walk(func(n *xhtml.Node) {
		switch n.DataAtom {
		case atom.Title:
			if title == "" {
				title = textOf(n)
			}
		case atom.H1:
			if h1 == "" {
				h1 = textOf(n)
			}
		case atom.Meta:
			prop := strings.ToLower(firstNonEmpty(attr(n, "property"), attr(n, "name"), attr(n, "itemprop")))
			content := attr(n, "content")
			switch prop {
			case "og:title":
				og = content
			case "og:image", "twitter:image":
				if p.Thumbnail == "" {
					p.Thumbnail = absURL(d.Base, content)
				}
			case "og:video", "og:video:url", "og:video:secure_url", "twitter:player:stream", "contenturl":
				if !strings.Contains(content, ".swf") && (protoFor(content) != ProtoHTTP || reMediaURL.MatchString(content)) {
					p.addCandidate(d, content, "", 30, "metadata")
				}
			case "duration":
				if p.DurationISO == 0 {
					p.DurationISO = parseISODuration(content)
				}
			}
		}
	})
	p.Title = strings.TrimSpace(firstNonEmpty(og, h1, title))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ── html5-video: <video>, <audio>, <source>, <track> ───────────────────────

func modHTML5(d *doc, p *page) {
	d.walk(func(n *xhtml.Node) {
		switch n.DataAtom {
		case atom.Video, atom.Audio, atom.Source:
			for _, k := range []string{"src", "data-src"} {
				if v := attr(n, k); v != "" && !strings.HasPrefix(v, "blob:") {
					p.addCandidate(d, v, firstNonEmpty(attr(n, "label"), attr(n, "title"), attr(n, "size"), attr(n, "res")), 50, "html5-video")
				}
			}
		case atom.Track:
			kind := strings.ToLower(attr(n, "kind"))
			if src := absURL(d.Base, attr(n, "src")); src != "" && (kind == "" || kind == "subtitles" || kind == "captions") {
				p.Subtitles = append(p.Subtitles, Track{
					Label: firstNonEmpty(attr(n, "label"), attr(n, "srclang"), "Subtitles"), Lang: attr(n, "srclang"), URL: src, Protocol: ProtoHTTP,
				})
			}
		}
	})
}

// ── json-ld: schema.org VideoObject ────────────────────────────────────────

func modJSONLD(d *doc, p *page) {
	d.walk(func(n *xhtml.Node) {
		if n.DataAtom != atom.Script || !strings.Contains(attr(n, "type"), "ld+json") {
			return
		}
		parseJSONLD(textOf(n),
			func(u string) { p.addCandidate(d, u, "", 40, "json-ld") },
			func(u string) {
				if p.Thumbnail == "" {
					p.Thumbnail = absURL(d.Base, u)
				}
			},
			func(s string) {
				if p.DurationISO == 0 {
					p.DurationISO = parseISODuration(s)
				}
			})
	})
}

// ── iframes: embedded players (followed recursively by the extractor) ─────

var reIframeInScript = regexp.MustCompile(`(?i)<iframe[^>]*?\s(?:data-)?src\s*=\s*\\?["']([^"'\\]+)`)

func modIframes(d *doc, p *page) {
	d.walk(func(n *xhtml.Node) {
		switch n.DataAtom {
		case atom.Iframe, atom.Frame, atom.Embed:
			for _, k := range []string{"src", "data-src", "data-lazy-src", "data-original"} {
				if v := attr(n, k); v != "" && v != "about:blank" {
					p.addIframe(d, v)
				}
			}
		}
	})
	// Players injected by scripts: $(…).html('<iframe src="…">').
	for _, m := range reIframeInScript.FindAllStringSubmatch(d.Raw, 20) {
		p.addIframe(d, m[1])
	}
}

// ── player-tabs: voice-over / server buttons pointing to players ──────────

var playerAttrs = []string{"data-url-player", "data-player-url", "data-player", "data-iframe", "data-iframe-src", "data-embed", "data-link", "data-url"}

func modPlayerTabs(d *doc, p *page) {
	d.walk(func(n *xhtml.Node) {
		for _, k := range playerAttrs {
			v := attr(n, k)
			if v == "" || !looksLikeURL(v) || isImageURL(v) {
				continue
			}
			u := absURL(d.Base, unescapeJS(v))
			if u == "" || p.seenPlayer[u] {
				continue
			}
			p.seenPlayer[u] = true
			label := firstNonEmpty(textOf(n), attr(n, "title"), attr(n, "data-title"), attr(n, "aria-label"))
			p.Players = append(p.Players, playerLink{Label: label, URL: u, Selected: isSelected(n)})
			p.hit("player-tabs")
			break
		}
	})
}

// ── episode-links: links/options to other episode pages ───────────────────

func modEpisodeLinks(d *doc, p *page) {
	d.walk(func(n *xhtml.Node) {
		if n.DataAtom != atom.A && n.DataAtom != atom.Option {
			return
		}
		href := attr(n, "href")
		if n.DataAtom == atom.Option {
			href = attr(n, "value")
		}
		u := absURL(d.Base, href)
		if u == "" || p.seenEp[u] || isDownloadLink(u) {
			return
		}
		label := firstNonEmpty(attr(n, "title"), textOf(n), attr(n, "data-title"))
		if num, ok := episodeNumber(label); ok {
			p.seenEp[u] = true
			p.Episodes = append(p.Episodes, episodeLink{Label: label, URL: u, Number: num, Season: seasonNumber(label), group: episodeGroup(label)})
		}
	})
	if len(filterEpisodes(p.Episodes)) > 0 {
		p.hit("episode-links")
	}
}

// ── subtitle-attributes: data-ru_subtitle="https://…vtt" ──────────────────

func modSubtitleAttrs(d *doc, p *page) {
	d.walk(func(n *xhtml.Node) {
		for _, a := range n.Attr {
			if strings.HasSuffix(a.Key, "subtitle") && looksLikeURL(a.Val) {
				lang := strings.TrimSuffix(strings.TrimPrefix(a.Key, "data-"), "_subtitle")
				p.Subtitles = append(p.Subtitles, Track{Label: strings.ToUpper(lang), Lang: lang, URL: absURL(d.Base, a.Val), Protocol: ProtoHTTP})
			}
		}
	})
}

// ── playerjs: new Playerjs({file: …}) configs ─────────────────────────────

func modPlayerjs(d *doc, p *page) {
	pl, enc := findPlayerjs(d.Raw, d.Base)
	if pl != nil {
		pl.Via = "playerjs"
		p.Playlist = pl
		p.hit("playerjs")
	} else if enc {
		p.Encrypted = true
	}
}

// ── js-playlist: JavaScript arrays of {title, file} objects ───────────────
//
// Many self-made and JW Player based players keep their episode list in a
// plain JS literal: var playlist = [{title: 'Episode 1', file: '…'}, …].

var reArrayStart = regexp.MustCompile(`[=:(,]\s*\[\s*\{`)

func modJSPlaylist(d *doc, p *page) {
	var best *pjsPlaylist
	for _, loc := range reArrayStart.FindAllStringIndex(d.Raw, 40) {
		start := strings.IndexByte(d.Raw[loc[0]:loc[1]], '[') + loc[0]
		lit, ok := readJSValue(d.Raw[start:])
		if !ok || len(lit) < 10 || !strings.Contains(lit, "file") && !strings.Contains(lit, "src") && !strings.Contains(lit, "hls") {
			continue
		}
		pl := parsePjsJSON(lit, d.Base)
		if pl == nil {
			continue
		}
		if best == nil || len(pl.Leaves) > len(best.Leaves) {
			best = pl
		}
	}
	if best == nil {
		return
	}
	// A single unlabeled leaf adds nothing over the other modules.
	if len(best.Leaves) == 1 && len(best.Leaves[0].Path) == 0 && p.Playlist != nil {
		return
	}
	if p.Playlist == nil || len(best.Leaves) > len(p.Playlist.Leaves) {
		best.Via = "js-playlist"
		p.Playlist = best
		p.hit("js-playlist")
	}
}

// ── media-urls: bare HLS/DASH/MP4 URLs anywhere in the document ───────────

func modMediaURLs(d *doc, p *page) {
	for _, m := range reMediaURL.FindAllString(d.Raw, -1) {
		score := 10
		if strings.Contains(strings.ToLower(m), ".m3u8") {
			score = 20
		}
		p.addCandidate(d, m, "", score, "media-urls")
	}
}

// ── lenient JavaScript literal → JSON ─────────────────────────────────────

// jsToJSON converts a JavaScript object/array literal into JSON: it removes
// comments and trailing commas, quotes bare keys and converts single quoted
// strings. It does not evaluate anything.
func jsToJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 32)
	var last byte // last significant byte written
	emit := func(str string) {
		b.WriteString(str)
		if t := strings.TrimRight(str, " \n\t\r"); t != "" {
			last = t[len(t)-1]
		}
	}
	isIdent := func(c byte) bool {
		return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	isSpace := func(c byte) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' }
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 3
		case c == '"' || c == '\'' || c == '`':
			q := c
			var sb strings.Builder
			sb.WriteByte('"')
			for i++; i < len(s) && s[i] != q; i++ {
				switch {
				case s[i] == '\\' && i+1 < len(s):
					if s[i+1] == '\'' {
						sb.WriteByte('\'')
					} else {
						sb.WriteByte('\\')
						sb.WriteByte(s[i+1])
					}
					i++
				case s[i] == '"':
					sb.WriteString(`\"`)
				case s[i] == '\n':
					sb.WriteString(`\n`)
				default:
					sb.WriteByte(s[i])
				}
			}
			sb.WriteByte('"')
			emit(sb.String())
		case c == ',':
			// Drop trailing commas: look ahead to the next significant byte.
			j := i + 1
			for j < len(s) && isSpace(s[j]) {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
			emit(",")
		case isIdent(c):
			j := i
			for j < len(s) && (isIdent(s[j]) || s[j] == '.' && (last != '{' && last != ',')) {
				j++
			}
			k := j
			for k < len(s) && isSpace(s[k]) {
				k++
			}
			word := s[i:j]
			if (last == '{' || last == ',') && k < len(s) && s[k] == ':' {
				emit(`"` + word + `"`)
			} else {
				emit(jsWord(word))
			}
			i = j - 1
		case c == '!' && i+1 < len(s) && (s[i+1] == '0' || s[i+1] == '1'):
			emit(map[byte]string{'0': "true", '1': "false"}[s[i+1]])
			i++
		case isSpace(c):
			b.WriteByte(c)
		default:
			emit(string(c))
		}
	}
	return b.String()
}

// jsWord maps a bare JS token to a JSON value.
func jsWord(w string) string {
	switch w {
	case "true", "false", "null":
		return w
	case "undefined", "NaN":
		return "null"
	}
	if json.Valid([]byte(w)) {
		return w // number
	}
	return "null" // variable reference or expression we cannot evaluate
}

// isDownloadLink reports links to torrents, archives and download scripts:
// they are labelled like episodes but are not pages with a player.
func isDownloadLink(u string) bool {
	l := strings.ToLower(u)
	for _, w := range []string{"torrent", "/download", "download.php", "getfile", ".zip", ".rar", ".7z"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

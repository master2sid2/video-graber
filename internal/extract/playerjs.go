package extract

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Playerjs (playerjs.com) is a very common embeddable player. Its config has a
// "file" field that is either a URL, a quality list such as
// "[360p]url1,[720p]url2", or a JSON playlist with nested "folder" arrays for
// translations / seasons / episodes.

type pjsLeaf struct {
	Path     []string // titles from the root to the leaf
	File     string
	Subtitle string
}

type pjsPlaylist struct {
	Leaves []pjsLeaf
}

var reFileKey = regexp.MustCompile(`["']?\bfile["']?\s*:\s*`)

// readJSValue reads a string literal or a bracketed array/object at s[0].
func readJSValue(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	switch q := s[0]; q {
	case '"', '\'', '`':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(s[i])
				}
				continue
			}
			if c == q {
				return b.String(), true
			}
			b.WriteByte(c)
		}
	case '[', '{':
		depth := 0
		var inStr byte
		for i := 0; i < len(s); i++ {
			c := s[i]
			if inStr != 0 {
				if c == '\\' {
					i++
				} else if c == inStr {
					inStr = 0
				}
				continue
			}
			switch c {
			case '"', '\'':
				inStr = c
			case '[', '{':
				depth++
			case ']', '}':
				depth--
				if depth == 0 {
					return s[:i+1], true
				}
			}
		}
	}
	return "", false
}

// findPlayerjs locates a Playerjs-like file definition in raw page text.
func findPlayerjs(raw string, base *url.URL) (pl *pjsPlaylist, encrypted bool) {
	for _, loc := range reFileKey.FindAllStringIndex(raw, 20) {
		val, ok := readJSValue(raw[loc[1]:])
		if !ok || len(val) < 4 {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) > 2 && val[0] == '#' && val[1] >= '0' && val[1] <= '9' {
			encrypted = true
			continue
		}
		if strings.HasPrefix(val, "[{") || strings.HasPrefix(val, "{") {
			if p := parsePjsJSON(val, base); p != nil {
				return p, false
			}
			continue
		}
		if strings.Contains(val, "//") || reMediaURL.MatchString(val) {
			sub := ""
			if i := strings.Index(raw[loc[1]:], "subtitle"); i >= 0 && i < 2000 {
				rest := raw[loc[1]+i+len("subtitle"):]
				rest = strings.TrimLeft(rest, `"' :`)
				sub, _ = readJSValue(rest)
			}
			if items := parseLabeledList(val, base); len(items) > 0 {
				return &pjsPlaylist{Leaves: []pjsLeaf{{File: val, Subtitle: sub}}}, false
			}
		}
	}
	return nil, encrypted
}

func parsePjsJSON(val string, base *url.URL) *pjsPlaylist {
	var v any
	if err := json.Unmarshal([]byte(val), &v); err != nil {
		// Some sites use single quotes; try a naive conversion.
		if err := json.Unmarshal([]byte(strings.ReplaceAll(val, "'", `"`)), &v); err != nil {
			return nil
		}
	}
	p := &pjsPlaylist{}
	var walk func(v any, path []string)
	walk = func(v any, path []string) {
		switch t := v.(type) {
		case []any:
			for _, x := range t {
				walk(x, path)
			}
		case map[string]any:
			title, _ := t["title"].(string)
			title = strings.TrimSpace(stripTags(title))
			next := path
			if title != "" {
				next = append(slices.Clone(path), title)
			}
			if f, ok := t["folder"]; ok {
				walk(f, next)
				return
			}
			if f, ok := t["playlist"]; ok {
				walk(f, next)
				return
			}
			if f, ok := t["file"].(string); ok && f != "" {
				sub, _ := t["subtitle"].(string)
				if len(parseLabeledList(f, base)) > 0 {
					p.Leaves = append(p.Leaves, pjsLeaf{Path: next, File: f, Subtitle: sub})
				}
			}
		}
	}
	walk(v, nil)
	if len(p.Leaves) == 0 {
		return nil
	}
	return p
}

var reTags = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string { return reTags.ReplaceAllString(s, "") }

type labeled struct {
	Label string
	URL   string
}

var reLabeled = regexp.MustCompile(`\[([^\]]*)\]\s*([^,\[\]\s]+)`)

// parseLabeledList parses "[360p]url1,[720p]url2" or "url1 or url2" or "url".
func parseLabeledList(s string, base *url.URL) []labeled {
	var out []labeled
	add := func(label, raw string) {
		raw, _, _ = strings.Cut(strings.TrimSpace(raw), " or ")
		if u := absURL(base, raw); u != "" {
			out = append(out, labeled{Label: strings.TrimSpace(label), URL: u})
		}
	}
	if ms := reLabeled.FindAllStringSubmatch(s, -1); len(ms) > 0 {
		for _, m := range ms {
			add(m[1], m[2])
		}
		return out
	}
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" && (strings.Contains(p, "//") || strings.HasPrefix(p, "/")) {
			add("", p)
		}
	}
	return out
}

// pjsChoices derives translations and episodes from playlist leaf paths.
//
// Depth 1 paths are episodes. Depth 2 is (season | translation, episode).
// Depth 3+ is (translation, season, episode...).
func (p *pjsPlaylist) choices() (translations, episodes []Choice, keyOf func(pjsLeaf) (tr, ep string)) {
	keyOf = func(l pjsLeaf) (string, string) {
		switch len(l.Path) {
		case 0:
			return "", ""
		case 1:
			return "", slug(l.Path[0])
		case 2:
			if seasonNumber(l.Path[0]) > 0 {
				return "", slug(l.Path[0] + " " + l.Path[1])
			}
			return slug(l.Path[0]), slug(l.Path[1])
		default:
			return slug(l.Path[0]), slug(strings.Join(l.Path[1:], " "))
		}
	}
	seenT, seenE := map[string]bool{}, map[string]bool{}
	for _, l := range p.Leaves {
		tr, ep := keyOf(l)
		if tr != "" && !seenT[tr] {
			seenT[tr] = true
			translations = append(translations, Choice{ID: tr, Label: l.Path[0]})
		}
		if ep != "" && !seenE[ep] {
			seenE[ep] = true
			c := Choice{ID: ep}
			switch len(l.Path) {
			case 1:
				c.Label = l.Path[0]
			case 2:
				c.Label = l.Path[1]
				if tr == "" {
					c.Season = l.Path[0]
				}
			default:
				c.Label = l.Path[len(l.Path)-1]
				c.Season = strings.Join(l.Path[1:len(l.Path)-1], " ")
			}
			c.Number, _ = episodeNumber(c.Label)
			episodes = append(episodes, c)
		}
	}
	if len(episodes) < 2 {
		episodes = nil
	}
	if len(translations) < 2 {
		translations = nil
	}
	return translations, episodes, keyOf
}

// pick returns the leaf matching the selection (falling back sensibly).
func (p *pjsPlaylist) pick(sel Selection) pjsLeaf {
	_, _, keyOf := p.choices()
	var epOnly *pjsLeaf
	for i, l := range p.Leaves {
		tr, ep := keyOf(l)
		trOK := sel.Translation == "" || tr == sel.Translation || tr == ""
		epOK := sel.Episode == "" || ep == sel.Episode || ep == ""
		if trOK && epOK {
			return l
		}
		if epOK && epOnly == nil {
			epOnly = &p.Leaves[i]
		}
	}
	if epOnly != nil {
		return *epOnly
	}
	return p.Leaves[0]
}

// slug normalises a label into a stable identifier.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127 {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

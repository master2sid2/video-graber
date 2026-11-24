package extract

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/master2sid2/video-graber/internal/netx"
)

type hlsVariant struct {
	URL       string
	Bandwidth int
	Width     int
	Height    int
	FPS       float64
	Codecs    string
	Audio     string
	Subs      string
}

type hlsMedia struct {
	Type     string // AUDIO, SUBTITLES, VIDEO, CLOSED-CAPTIONS
	GroupID  string
	Name     string
	Lang     string
	URI      string
	Default  bool
	Channels string
}

type hlsPlaylist struct {
	Master   bool
	Variants []hlsVariant
	Media    []hlsMedia
	Duration float64
}

// parseHLSAttrs parses an attribute list: KEY=VALUE,KEY="quoted, value".
func parseHLSAttrs(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(s[:eq])
		s = s[eq+1:]
		var val string
		if strings.HasPrefix(s, `"`) {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				val, s = s[1:], ""
			} else {
				val, s = s[1:end+1], s[end+2:]
			}
		} else {
			val, s, _ = strings.Cut(s, ",")
		}
		out[strings.ToUpper(key)] = val
		s = strings.TrimPrefix(strings.TrimLeft(s, " "), ",")
	}
	return out
}

func resolveRef(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// parseHLS parses a master or media playlist. base resolves relative URIs.
func parseHLS(data []byte, base string) (*hlsPlaylist, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("#EXTM3U")) {
		return nil, fmt.Errorf("not an HLS playlist")
	}
	p := &hlsPlaylist{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var pending *hlsVariant
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			p.Master = true
			a := parseHLSAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			v := hlsVariant{Codecs: a["CODECS"], Audio: a["AUDIO"], Subs: a["SUBTITLES"]}
			v.Bandwidth, _ = strconv.Atoi(cmp.Or(a["AVERAGE-BANDWIDTH"], a["BANDWIDTH"]))
			if w, h, ok := strings.Cut(a["RESOLUTION"], "x"); ok {
				v.Width, _ = strconv.Atoi(w)
				v.Height, _ = strconv.Atoi(h)
			}
			v.FPS, _ = strconv.ParseFloat(a["FRAME-RATE"], 64)
			pending = &v
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			p.Master = true
			a := parseHLSAttrs(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			m := hlsMedia{
				Type: strings.ToUpper(a["TYPE"]), GroupID: a["GROUP-ID"], Name: a["NAME"],
				Lang: a["LANGUAGE"], Default: strings.EqualFold(a["DEFAULT"], "YES"), Channels: a["CHANNELS"],
			}
			if a["URI"] != "" {
				m.URI = resolveRef(base, a["URI"])
			}
			p.Media = append(p.Media, m)
		case strings.HasPrefix(line, "#EXTINF:"):
			d, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			f, _ := strconv.ParseFloat(strings.TrimSpace(d), 64)
			p.Duration += f
		case strings.HasPrefix(line, "#"):
		default:
			if pending != nil {
				pending.URL = resolveRef(base, line)
				p.Variants = append(p.Variants, *pending)
				pending = nil
			}
		}
	}
	return p, sc.Err()
}

// hlsStream fetches an m3u8 URL and converts it into a Stream.
func hlsStream(ctx context.Context, c *netx.Client, rawURL string, headers Headers, label string) (*Stream, error) {
	resp, err := c.Get(ctx, rawURL, headers)
	if err != nil {
		return nil, err
	}
	pl, err := parseHLS(resp.Body, resp.URL)
	if err != nil {
		return nil, err
	}
	st := &Stream{}
	if !pl.Master {
		st.Duration = pl.Duration
		st.Formats = []Format{{
			ID: "hls", Label: cmp.Or(label, "Auto"), Protocol: ProtoHLS, URL: resp.URL, Headers: headers,
			HasVideo: true, HasAudio: true, Ext: "mp4",
		}}
		return st, nil
	}

	// Audio and subtitle renditions.
	audioGroups := map[string]bool{}
	for _, m := range pl.Media {
		switch m.Type {
		case "AUDIO":
			if m.URI == "" {
				continue // muxed into the variant
			}
			audioGroups[m.GroupID] = true
			st.Audio = append(st.Audio, Track{
				ID: fmt.Sprintf("a%d", len(st.Audio)), Label: cmp.Or(m.Name, m.Lang, "Audio"), Lang: m.Lang,
				Default: m.Default, Protocol: ProtoHLS, URL: m.URI, Headers: headers, Group: m.GroupID,
			})
		case "SUBTITLES":
			if m.URI == "" {
				continue
			}
			st.Subtitles = append(st.Subtitles, Track{
				ID: fmt.Sprintf("s%d", len(st.Subtitles)), Label: cmp.Or(m.Name, m.Lang, "Subtitles"), Lang: m.Lang,
				Default: m.Default, Protocol: ProtoHLS, URL: m.URI, Headers: headers, Group: m.GroupID,
			})
		}
	}

	seen := map[string]bool{}
	for _, v := range pl.Variants {
		if seen[v.URL] {
			continue
		}
		seen[v.URL] = true
		f := Format{
			Width: v.Width, Height: v.Height, FPS: v.FPS, Bitrate: v.Bandwidth, Protocol: ProtoHLS,
			URL: v.URL, Headers: headers, HasVideo: true, Ext: "mp4", AudioGroup: v.Audio, SubsGroup: v.Subs,
		}
		f.VCodec, f.ACodec = splitCodecs(v.Codecs)
		if f.VCodec == "" && f.ACodec != "" && v.Height == 0 && !strings.Contains(v.Codecs, ",") {
			f.HasVideo = false
		}
		f.HasAudio = v.Audio == "" || !audioGroups[v.Audio]
		if v.Codecs != "" && f.ACodec == "" && f.VCodec != "" {
			f.HasAudio = false
		}
		f.Label = qualityLabel(v.Height, v.Bandwidth)
		st.Formats = append(st.Formats, f)
	}
	sortFormats(st.Formats)
	assignFormatIDs(st.Formats, "hls")

	// Duration from the first variant (needed for progress reporting).
	if len(pl.Variants) > 0 {
		if r, err := c.Get(ctx, pl.Variants[0].URL, headers); err == nil {
			if mp, err := parseHLS(r.Body, r.URL); err == nil {
				st.Duration = mp.Duration
			}
		}
	}
	return st, nil
}

func splitCodecs(codecs string) (v, a string) {
	for c := range strings.SplitSeq(codecs, ",") {
		c = strings.TrimSpace(c)
		switch {
		case c == "":
		case strings.HasPrefix(c, "avc"), strings.HasPrefix(c, "hvc"), strings.HasPrefix(c, "hev"),
			strings.HasPrefix(c, "vp0"), strings.HasPrefix(c, "vp9"), strings.HasPrefix(c, "av01"):
			v = c
		case strings.HasPrefix(c, "mp4a"), strings.HasPrefix(c, "ac-3"), strings.HasPrefix(c, "ec-3"),
			strings.HasPrefix(c, "opus"), strings.HasPrefix(c, "flac"):
			a = c
		}
	}
	return
}

func qualityLabel(height, bitrate int) string {
	switch {
	case height > 0 && bitrate > 0:
		return fmt.Sprintf("%dp · %s", height, humanBitrate(bitrate))
	case height > 0:
		return fmt.Sprintf("%dp", height)
	case bitrate > 0:
		return humanBitrate(bitrate)
	}
	return "Auto"
}

func humanBitrate(bps int) string {
	if bps >= 1_000_000 {
		return strconv.FormatFloat(float64(bps)/1e6, 'f', 1, 64) + " Mbps"
	}
	return strconv.Itoa(bps/1000) + " kbps"
}

// sortFormats orders formats best first.
func sortFormats(f []Format) {
	slices.SortStableFunc(f, func(a, b Format) int {
		if a.Height != b.Height {
			return b.Height - a.Height
		}
		return b.Bitrate - a.Bitrate
	})
}

// assignFormatIDs gives formats stable, unique ids such as hls-1080.
func assignFormatIDs(f []Format, prefix string) {
	used := map[string]int{}
	for i := range f {
		id := prefix
		if f[i].Height > 0 {
			id = fmt.Sprintf("%s-%d", prefix, f[i].Height)
		}
		used[id]++
		if n := used[id]; n > 1 {
			id = fmt.Sprintf("%s-%d", id, n)
		}
		f[i].ID = id
	}
}

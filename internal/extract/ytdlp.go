package extract

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// YtDlp integrates the external yt-dlp binary (https://github.com/yt-dlp/yt-dlp).
// yt-dlp is NOT bundled with this project: install it separately and pass its
// path with -ytdlp. It is used for YouTube and many other well known sites,
// and as a fallback when the generic extractor finds nothing.
type YtDlp struct {
	Path      string
	ExtraArgs []string
	Proxy     string
	Hosts     []string
	TempDir   string
	Log       *slog.Logger
}

// DefaultYtDlpHosts are sites that are always delegated to yt-dlp.
var DefaultYtDlpHosts = []string{
	"youtube.com", "youtu.be", "youtube-nocookie.com", "music.youtube.com",
	"vimeo.com", "dailymotion.com", "twitch.tv", "tiktok.com", "instagram.com", "facebook.com", "fb.watch",
	"twitter.com", "x.com", "reddit.com", "soundcloud.com", "bandcamp.com", "bilibili.com", "nicovideo.jp",
	"vk.com", "vkvideo.ru", "vk.ru", "rutube.ru", "ok.ru", "dzen.ru", "coub.com", "streamable.com", "archive.org",
}

// Name implements Extractor.
func (y *YtDlp) Name() string { return "yt-dlp" }

// Match implements Extractor.
func (y *YtDlp) Match(u *url.URL) bool {
	return hostMatches(u.Hostname(), DefaultYtDlpHosts...) || hostMatches(u.Hostname(), y.Hosts...)
}

// cookiesTemp writes the user's cookies to a private temp file. yt-dlp
// rewrites the cookie file it is given, so every call works on its own copy.
// Cookies are strictly personal: there is no global fallback.
func (y *YtDlp) cookiesTemp(cookies string) (string, func(), error) {
	src := []byte(cookies)
	if len(bytes.TrimSpace(src)) == 0 {
		return "", func() {}, nil
	}
	if err := os.MkdirAll(y.TempDir, 0o700); err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp(y.TempDir, "cookies-*.txt")
	if err != nil {
		return "", nil, err
	}
	if !bytes.HasPrefix(bytes.TrimSpace(src), []byte("# ")) {
		// yt-dlp requires the Netscape header line.
		f.WriteString("# Netscape HTTP Cookie File\n")
	}
	f.Write(src)
	f.Close()
	name := f.Name()
	return name, func() { os.Remove(name) }, nil
}

// BaseArgs returns arguments shared by all yt-dlp invocations.
func (y *YtDlp) BaseArgs(cookiesPath string) []string {
	args := []string{"--no-warnings", "--no-progress", "--no-color", "--ignore-config"}
	if y.Proxy != "" {
		args = append(args, "--proxy", y.Proxy)
	}
	if cookiesPath != "" {
		args = append(args, "--cookies", cookiesPath)
	}
	return append(args, y.ExtraArgs...)
}

// Command builds a yt-dlp command that writes format formatID of pageURL to
// stdout. The returned cleanup removes temporary files and must be called
// after the process exits.
func (y *YtDlp) Command(ctx context.Context, pageURL, formatID, cookies string) (*exec.Cmd, func(), error) {
	cpath, cleanup, err := y.cookiesTemp(cookies)
	if err != nil {
		return nil, nil, err
	}
	args := append(y.BaseArgs(cpath), "--quiet", "--no-part", "--no-playlist", "-f", formatID, "-o", "-", "--", pageURL)
	cmd := exec.CommandContext(ctx, y.Path, args...)
	return cmd, cleanup, nil
}

type ytFormat struct {
	FormatID   string            `json:"format_id"`
	FormatNote string            `json:"format_note"`
	Ext        string            `json:"ext"`
	Width      *int              `json:"width"`
	Height     *int              `json:"height"`
	FPS        *float64          `json:"fps"`
	VCodec     string            `json:"vcodec"`
	ACodec     string            `json:"acodec"`
	TBR        *float64          `json:"tbr"`
	ABR        *float64          `json:"abr"`
	Filesize   *int64            `json:"filesize"`
	FilesizeA  *int64            `json:"filesize_approx"`
	Protocol   string            `json:"protocol"`
	Language   string            `json:"language"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"http_headers"`
	Resolution string            `json:"resolution"`
	DRM        bool              `json:"has_drm"`
}

type ytSub struct {
	Ext  string `json:"ext"`
	URL  string `json:"url"`
	Name string `json:"name"`
}

type ytInfo struct {
	Type       string             `json:"_type"`
	ID         string             `json:"id"`
	Title      string             `json:"title"`
	Thumbnail  string             `json:"thumbnail"`
	Duration   float64            `json:"duration"`
	WebpageURL string             `json:"webpage_url"`
	URL        string             `json:"url"`
	Extractor  string             `json:"extractor_key"`
	Formats    []ytFormat         `json:"formats"`
	Subtitles  map[string][]ytSub `json:"subtitles"`
	Entries    []ytInfo           `json:"entries"`
	Thumbs     []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

func (y *YtDlp) dump(ctx context.Context, pageURL, cookies string, playlist bool) (*ytInfo, error) {
	cpath, cleanup, err := y.cookiesTemp(cookies)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	args := append(y.BaseArgs(cpath), "-J", "--flat-playlist")
	if !playlist {
		args = append(args, "--no-playlist")
	}
	args = append(args, "--", pageURL)
	cmd := exec.CommandContext(ctx, y.Path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "ERROR:"); i >= 0 {
			msg = msg[i:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("yt-dlp: %s", msg)
	}
	var info ytInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("yt-dlp: invalid JSON: %w", err)
	}
	return &info, nil
}

// Extract implements Extractor.
func (y *YtDlp) Extract(ctx context.Context, pageURL string, opts Options) (*Result, error) {
	u, _ := url.Parse(pageURL)
	// A watch URL that also carries a list= parameter means "this video".
	playlist := u == nil || u.Query().Get("v") == ""
	info, err := y.dump(ctx, pageURL, opts.Cookies, playlist)
	if err != nil {
		return nil, err
	}
	media := &Media{PageURL: pageURL, Title: info.Title, Thumbnail: ytThumb(info), Extractor: "yt-dlp", Duration: info.Duration, Modules: []string{"yt-dlp"}}
	if info.Extractor != "" {
		media.Extractor = "yt-dlp:" + strings.ToLower(info.Extractor)
	}

	if info.Type == "playlist" || len(info.Entries) > 0 {
		entryURL := map[string]string{}
		for i, e := range info.Entries {
			id := cmp.Or(e.ID, fmt.Sprint(i+1))
			if _, dup := entryURL[id]; dup {
				continue
			}
			entryURL[id] = cmp.Or(e.WebpageURL, e.URL)
			media.Episodes = append(media.Episodes, Choice{ID: id, Label: cmp.Or(e.Title, fmt.Sprintf("#%d", i+1)), Number: i + 1})
		}
		if len(media.Episodes) == 0 {
			return nil, ErrNoMedia
		}
		media.Selected.Episode = media.Episodes[0].ID
		resolve := func(ctx context.Context, sel Selection) (*Stream, error) {
			eu, ok := entryURL[sel.Episode]
			if !ok {
				eu = entryURL[media.Episodes[0].ID]
			}
			ei, err := y.dump(ctx, eu, opts.Cookies, false)
			if err != nil {
				return nil, err
			}
			return ytStream(ei, eu), nil
		}
		return &Result{Media: media, Resolve: resolve}, nil
	}

	st := ytStream(info, cmp.Or(info.WebpageURL, pageURL))
	if len(st.Formats) == 0 {
		return nil, ErrNoMedia
	}
	return &Result{Media: media, Stream: st, Resolve: func(context.Context, Selection) (*Stream, error) { return st, nil }}, nil
}

func ytThumb(info *ytInfo) string {
	if info.Thumbnail != "" {
		return info.Thumbnail
	}
	if n := len(info.Thumbs); n > 0 {
		return info.Thumbs[n-1].URL
	}
	return ""
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// ytStream converts yt-dlp formats into a Stream. Every format is downloaded
// through yt-dlp itself (protocol ytdlp), so signatures, throttling and
// cookies are handled by yt-dlp.
func ytStream(info *ytInfo, pageURL string) *Stream {
	st := &Stream{Duration: info.Duration, Title: info.Title}
	for _, f := range info.Formats {
		if f.DRM || f.Ext == "mhtml" || strings.Contains(f.FormatNote, "storyboard") {
			continue
		}
		hasV := f.VCodec != "none" && f.VCodec != "" || f.VCodec == "" && deref(f.Height) > 0
		hasA := f.ACodec != "none" && f.ACodec != "" || f.ACodec == "" && f.VCodec == ""
		size := cmp.Or(deref(f.Filesize), deref(f.FilesizeA))
		switch {
		case hasV:
			h := deref(f.Height)
			label := f.FormatNote
			if h > 0 {
				label = fmt.Sprintf("%dp", h)
				if fps := deref(f.FPS); fps > 30 {
					label += fmt.Sprintf("%.0f", fps)
				}
			}
			st.Formats = append(st.Formats, Format{
				ID: f.FormatID, Label: cmp.Or(label, f.Resolution, f.FormatID), Width: deref(f.Width), Height: h,
				FPS: deref(f.FPS), Bitrate: int(deref(f.TBR) * 1000), VCodec: shortCodec(f.VCodec),
				ACodec: shortCodec(f.ACodec), Ext: f.Ext, Size: size, HasVideo: true, HasAudio: hasA,
				Protocol: ProtoYtDlp, URL: pageURL,
			})
		case hasA:
			label := fmt.Sprintf("%s %.0fk", f.Ext, cmp.Or(deref(f.ABR), deref(f.TBR)))
			if f.Language != "" {
				label += " · " + f.Language
			}
			if note := f.FormatNote; note != "" && !strings.Contains(strings.ToLower(note), "low") &&
				!strings.Contains(strings.ToLower(note), "medium") && !strings.Contains(strings.ToLower(note), "high") {
				label += " · " + note
			}
			st.Audio = append(st.Audio, Track{
				ID: f.FormatID, Label: label, Lang: f.Language, Codec: shortCodec(f.ACodec),
				Bitrate: int(cmp.Or(deref(f.ABR), deref(f.TBR)) * 1000), Protocol: ProtoYtDlp, URL: pageURL,
				Default: strings.Contains(f.FormatNote, "original") || strings.Contains(f.FormatNote, "default"),
			})
		}
	}
	slices.SortStableFunc(st.Formats, func(a, b Format) int {
		if a.Height != b.Height {
			return b.Height - a.Height
		}
		// Prefer formats that already carry audio, then higher bitrate.
		if a.HasAudio != b.HasAudio {
			if a.HasAudio {
				return -1
			}
			return 1
		}
		return b.Bitrate - a.Bitrate
	})
	slices.SortStableFunc(st.Audio, func(a, b Track) int {
		if a.Default != b.Default {
			if a.Default {
				return -1
			}
			return 1
		}
		return b.Bitrate - a.Bitrate
	})
	langs := slices.Sorted(func(yield func(string) bool) {
		for l := range info.Subtitles {
			if !yield(l) {
				return
			}
		}
	})
	for _, lang := range langs {
		subs := info.Subtitles[lang]
		if lang == "live_chat" {
			continue
		}
		idx := slices.IndexFunc(subs, func(s ytSub) bool { return s.Ext == "vtt" })
		if idx < 0 {
			continue
		}
		s := subs[idx]
		st.Subtitles = append(st.Subtitles, Track{
			ID: "y" + lang, Label: cmp.Or(s.Name, lang), Lang: lang, Protocol: ProtoHTTP, URL: s.URL, Headers: Headers{},
		})
	}
	return st
}

func shortCodec(c string) string {
	if c == "none" {
		return ""
	}
	c, _, _ = strings.Cut(c, ".")
	return c
}

// ErrYtDlpMissing is returned when yt-dlp is required but not configured.
var ErrYtDlpMissing = errors.New("yt-dlp is not configured (install it and pass -ytdlp)")

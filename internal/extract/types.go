// Package extract finds downloadable media on web pages.
//
// Extraction is a two step process: Extract analyses a page and returns a
// Media description (title, voice-over translations, episodes) together with
// a Resolver. The resolver turns a concrete Selection (translation + episode)
// into a Stream: the list of available video formats, audio and subtitle
// tracks with the URLs needed to download them.
package extract

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// ErrNoMedia is returned when nothing downloadable was found.
var ErrNoMedia = errors.New("no media found on the page")

// Headers are HTTP headers required to fetch a media URL (Referer, Origin...).
type Headers map[string]string

// Clone returns a copy of h (never nil).
func (h Headers) Clone() Headers {
	out := Headers{}
	maps.Copy(out, h)
	return out
}

// Protocol describes how a media URL is downloaded.
type Protocol string

const (
	ProtoHTTP  Protocol = "http"  // progressive file (mp4, webm, ...)
	ProtoHLS   Protocol = "hls"   // m3u8 playlist
	ProtoDASH  Protocol = "dash"  // mpd manifest
	ProtoYtDlp Protocol = "ytdlp" // piped through yt-dlp -f <id>
)

// Format is one downloadable video (or muxed audio+video) variant.
type Format struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Width    int      `json:"width,omitzero"`
	Height   int      `json:"height,omitzero"`
	FPS      float64  `json:"fps,omitzero"`
	Bitrate  int      `json:"bitrate,omitzero"` // bits per second
	VCodec   string   `json:"vcodec,omitempty"`
	ACodec   string   `json:"acodec,omitempty"`
	Ext      string   `json:"ext,omitempty"`
	Size     int64    `json:"size,omitzero"`
	HasVideo bool     `json:"has_video"`
	HasAudio bool     `json:"has_audio"`
	Protocol Protocol `json:"protocol"`

	URL        string  `json:"-"`
	Headers    Headers `json:"-"`
	AudioGroup string  `json:"-"` // HLS EXT-X-MEDIA group id
	SubsGroup  string  `json:"-"`
}

// Track is a separate audio or subtitle track.
type Track struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Lang     string   `json:"lang,omitempty"`
	Codec    string   `json:"codec,omitempty"`
	Bitrate  int      `json:"bitrate,omitzero"`
	Default  bool     `json:"default,omitzero"`
	Embedded bool     `json:"embedded,omitzero"` // stream inside the video file (selected with -map)
	Protocol Protocol `json:"protocol,omitempty"`

	URL         string  `json:"-"`
	Headers     Headers `json:"-"`
	Group       string  `json:"-"`
	StreamIndex int     `json:"-"` // index among the file's audio/subtitle streams when Embedded
}

// Stream is everything downloadable for one selection.
type Stream struct {
	Formats   []Format `json:"formats"`
	Audio     []Track  `json:"audio"`
	Subtitles []Track  `json:"subtitles"`
	Duration  float64  `json:"duration,omitzero"`
	Title     string   `json:"title,omitempty"`

	probed bool
	via    []string // analysis modules that produced the stream
}

// FindFormat returns the format with the given id.
func (s *Stream) FindFormat(id string) (*Format, bool) {
	for i := range s.Formats {
		if s.Formats[i].ID == id {
			return &s.Formats[i], true
		}
	}
	return nil, false
}

// FindAudio returns the audio track with the given id.
func (s *Stream) FindAudio(id string) (*Track, bool) {
	for i := range s.Audio {
		if s.Audio[i].ID == id {
			return &s.Audio[i], true
		}
	}
	return nil, false
}

// FindSubtitle returns the subtitle track with the given id.
func (s *Stream) FindSubtitle(id string) (*Track, bool) {
	for i := range s.Subtitles {
		if s.Subtitles[i].ID == id {
			return &s.Subtitles[i], true
		}
	}
	return nil, false
}

// Choice is a selectable item: a translation or an episode.
type Choice struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Season string `json:"season,omitempty"`
	Number int    `json:"number,omitzero"`
}

// Selection picks a translation and an episode ("" = default).
type Selection struct {
	Translation string `json:"translation"`
	Episode     string `json:"episode"`
}

// Media describes what was found on a page.
type Media struct {
	PageURL      string    `json:"page_url"`
	Title        string    `json:"title"`
	Thumbnail    string    `json:"thumbnail,omitempty"`
	Extractor    string    `json:"extractor"`
	Duration     float64   `json:"duration,omitzero"`
	Translations []Choice  `json:"translations"`
	Episodes     []Choice  `json:"episodes"`
	Selected     Selection `json:"selected"`
	Notes        []string  `json:"notes,omitempty"`
	// Modules lists the analysis modules that recognised the page.
	Modules []string `json:"modules,omitempty"`
}

// Resolver resolves a selection into a stream.
type Resolver func(ctx context.Context, sel Selection) (*Stream, error)

// Result is returned by extractors.
type Result struct {
	Media   *Media
	Resolve Resolver
	// Stream for Media.Selected, if already known.
	Stream *Stream
}

// Options carries per-request extraction settings.
type Options struct {
	// Cookies is a Netscape cookies.txt passed to yt-dlp (may be empty).
	Cookies string
	// Force selects an extractor by name ("" or "auto" = automatic).
	Force string
	// Referer of the embedding page (for embedded players).
	Referer string
}

// Extractor is implemented by site specific and generic extractors.
type Extractor interface {
	Name() string
	// Match reports whether the extractor is responsible for u.
	Match(u *url.URL) bool
	Extract(ctx context.Context, pageURL string, opts Options) (*Result, error)
}

// hostMatches reports whether host equals domain or is a subdomain of it.
func hostMatches(host string, domains ...string) bool {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// UnsupportedError means that no extractor or analysis module could find
// media on the page: the site needs a new module or a site-specific extractor.
type UnsupportedError struct {
	Tried []string
	Cause error
}

func (e *UnsupportedError) Error() string {
	return "no analysis module could find a video on this page"
}

func (e *UnsupportedError) Unwrap() error { return e.Cause }

func mergeNames(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, n := range l {
			if n != "" && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
}

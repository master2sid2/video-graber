package extract

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/master2sid2/video-graber/internal/netx"
)

// Engine chooses and runs extractors.
type Engine struct {
	Client   *netx.Client
	Generic  *Generic
	YtDlp    *YtDlp // nil when yt-dlp is not configured
	Specific []Extractor
	Prober   *Prober
	Log      *slog.Logger
}

// NewEngine wires the extractors together.
func NewEngine(c *netx.Client, yt *YtDlp, browser *Browser, prober *Prober, log *slog.Logger) *Engine {
	e := &Engine{Client: c, YtDlp: yt, Prober: prober, Log: log}
	// Site specific extractors for embedded players can be registered here.
	e.Specific = nil
	e.Generic = &Generic{Client: c, Browser: browser, Log: log, Specific: e.specificFor}
	return e
}

func (e *Engine) specificFor(u *url.URL) Extractor {
	for _, x := range e.Specific {
		if x.Match(u) {
			return x
		}
	}
	if e.YtDlp != nil && e.YtDlp.Match(u) {
		return e.YtDlp
	}
	return nil
}

// Extractors lists the names usable as Options.Force.
func (e *Engine) Extractors() []string {
	names := []string{"auto", "generic"}
	if e.YtDlp != nil {
		names = append(names, "yt-dlp")
	}
	if e.Generic.Browser != nil {
		names = append(names, "browser")
	}
	return names
}

// Extract analyses pageURL.
func (e *Engine) Extract(ctx context.Context, pageURL string, opts Options) (*Result, error) {
	pageURL = strings.TrimSpace(pageURL)
	if strings.HasPrefix(pageURL, "//") {
		pageURL = "https:" + pageURL
	}
	if !strings.Contains(pageURL, "://") {
		pageURL = "https://" + pageURL
	}
	u, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if err := e.Client.CheckURL(ctx, pageURL); err != nil {
		return nil, err
	}

	var res *Result
	switch strings.ToLower(opts.Force) {
	case "yt-dlp", "ytdlp":
		if e.YtDlp == nil {
			return nil, ErrYtDlpMissing
		}
		res, err = e.YtDlp.Extract(ctx, pageURL, opts)
	case "generic":
		res, err = e.Generic.Extract(ctx, pageURL, opts)
	case "browser":
		res, err = e.browserOnly(ctx, pageURL)
	default:
		if x := e.specificFor(u); x != nil {
			res, err = x.Extract(ctx, pageURL, opts)
			break
		}
		res, err = e.Generic.Extract(ctx, pageURL, opts)
		if err != nil && e.YtDlp != nil && ctx.Err() == nil {
			e.Log.Info("generic extractor failed, falling back to yt-dlp", "url", pageURL, "err", err)
			if r2, err2 := e.YtDlp.Extract(ctx, pageURL, opts); err2 == nil {
				res, err = r2, nil
			} else {
				err = errors.Join(err, err2)
			}
		}
	}
	if err != nil {
		if errors.Is(err, ErrNoMedia) {
			return nil, &UnsupportedError{Tried: e.Tried(), Cause: err}
		}
		return nil, err
	}
	e.wrap(res)
	return res, nil
}

// Tried lists everything the automatic mode attempts, in order.
func (e *Engine) Tried() []string {
	names := ModuleNames()
	if e.Generic.Browser != nil {
		names = append(names, "browser")
	}
	if e.YtDlp != nil {
		names = append(names, "yt-dlp")
	}
	return names
}

// wrap post-processes every resolved stream (validation, probing).
func (e *Engine) wrap(res *Result) {
	inner := res.Resolve
	res.Resolve = func(ctx context.Context, sel Selection) (*Stream, error) {
		st, err := inner(ctx, sel)
		if err != nil {
			return nil, err
		}
		if len(st.Formats) == 0 {
			return nil, ErrNoMedia
		}
		if !st.probed {
			st.probed = true
			e.Prober.Probe(ctx, st)
		}
		return st, nil
	}
}

func (e *Engine) browserOnly(ctx context.Context, pageURL string) (*Result, error) {
	if e.Generic.Browser == nil {
		return nil, errors.New("headless browser is not configured (pass -chrome)")
	}
	cands, hdr, err := e.Generic.Browser.Capture(ctx, pageURL)
	if err != nil {
		return nil, err
	}
	st, err := e.Generic.streamFromCandidates(ctx, cands, hdr, nil)
	if err != nil {
		return nil, err
	}
	title := pageURL
	if p, _, err := e.Generic.fetchPage(ctx, pageURL, ""); err == nil && p.Title != "" {
		title = p.Title
	}
	media := &Media{PageURL: pageURL, Title: title, Extractor: "browser", Duration: st.Duration, Modules: []string{"browser"}}
	return &Result{Media: media, Stream: st, Resolve: func(context.Context, Selection) (*Stream, error) { return st, nil }}, nil
}

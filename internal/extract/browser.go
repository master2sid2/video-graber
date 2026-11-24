package extract

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Browser drives an external headless Chrome/Chromium to capture media
// requests made by JavaScript players. The browser binary is not bundled; its
// path is configured with -chrome.
type Browser struct {
	ExecPath  string
	UserAgent string
	Proxy     string
	Timeout   time.Duration
	Log       *slog.Logger

	sem chan struct{}
}

// NewBrowser creates a browser runner allowing one session at a time.
func NewBrowser(execPath, userAgent, proxy string, timeout time.Duration, log *slog.Logger) *Browser {
	return &Browser{ExecPath: execPath, UserAgent: userAgent, Proxy: proxy, Timeout: timeout, Log: log, sem: make(chan struct{}, 1)}
}

type captured struct {
	url     string
	referer string
	proto   Protocol
	score   int
}

func classifyRequest(u, mime string) (Protocol, int, bool) {
	l := strings.ToLower(u)
	if strings.HasPrefix(l, "blob:") || strings.HasPrefix(l, "data:") || isAdURL(l) {
		return "", 0, false
	}
	mime = strings.ToLower(mime)
	switch {
	case strings.Contains(mime, "mpegurl") || strings.Contains(strings.SplitN(l, "?", 2)[0], ".m3u8"):
		return ProtoHLS, 30, true
	case strings.Contains(mime, "dash+xml") || strings.Contains(strings.SplitN(l, "?", 2)[0], ".mpd"):
		return ProtoDASH, 20, true
	case strings.HasPrefix(mime, "video/") && !strings.Contains(mime, "mp2t"):
		return ProtoHTTP, 10, true
	}
	p := strings.SplitN(l, "?", 2)[0]
	for _, ext := range []string{".mp4", ".webm", ".mkv", ".m4v"} {
		if strings.HasSuffix(p, ext) {
			return ProtoHTTP, 10, true
		}
	}
	return "", 0, false
}

// Capture loads pageURL, clicks the player and records media requests.
func (b *Browser) Capture(ctx context.Context, pageURL string) ([]candidate, Headers, error) {
	select {
	case b.sem <- struct{}{}:
		defer func() { <-b.sem }()
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(b.ExecPath),
		chromedp.UserAgent(b.UserAgent),
		chromedp.WindowSize(1280, 800),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
		// Keep cross-origin iframes in-process so their requests are visible.
		chromedp.Flag("disable-features", "IsolateOrigins,site-per-process"),
		chromedp.Flag("disable-site-isolation-trials", true),
	)
	if b.Proxy != "" {
		opts = append(opts, chromedp.ProxyServer(b.Proxy))
	}
	tctx, cancelTimeout := context.WithTimeout(ctx, b.Timeout)
	defer cancelTimeout()
	actx, cancelAlloc := chromedp.NewExecAllocator(tctx, opts...)
	defer cancelAlloc()
	bctx, cancelBrowser := chromedp.NewContext(actx)
	defer cancelBrowser()

	var mu sync.Mutex
	var found []captured
	seen := map[string]bool{}
	gotMedia := make(chan struct{}, 1)
	record := func(u, mime, referer string) {
		proto, score, ok := classifyRequest(u, mime)
		if !ok {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if seen[u] {
			return
		}
		seen[u] = true
		found = append(found, captured{url: u, referer: referer, proto: proto, score: score})
		select {
		case gotMedia <- struct{}{}:
		default:
		}
	}
	err := chromedp.Listen(bctx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			ref, _ := e.Request.Headers["Referer"].(string)
			record(e.Request.URL, "", cmp.Or(ref, e.DocumentURL))
		case *network.EventResponseReceived:
			record(e.Response.URL, e.Response.MimeType, "")
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start browser: %w", err)
	}

	// Navigate in the background: ads and trackers may delay the load event
	// long after the player already requested its media.
	navDone := make(chan error, 1)
	go func() { navDone <- chromedp.Do(bctx, chromedp.Navigate(pageURL)) }()

	clickPlayer := func() {
		pt, err := chromedp.Run(bctx, chromedp.Evaluate[[]float64](`(() => {
			let best = null, area = 0;
			for (const el of document.querySelectorAll('video, iframe, [class*="player"], [id*="player"]')) {
				const r = el.getBoundingClientRect();
				const a = r.width * r.height;
				if (a > area && r.width > 100 && r.height > 60) { best = el; area = a; }
			}
			if (!best) return [640, 400];
			best.scrollIntoView({block: 'center'});
			const r = best.getBoundingClientRect();
			return [r.left + r.width / 2, r.top + r.height / 2];
		})()`))
		if err != nil || len(pt) != 2 {
			return
		}
		_ = chromedp.Do(bctx, chromedp.MouseClickXY(pt[0], pt[1]))
	}

	clicks := 0
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
wait:
	for {
		select {
		case <-gotMedia:
			// Give the player a moment to request alternatives (audio, qualities).
			select {
			case <-time.After(2 * time.Second):
			case <-tctx.Done():
			}
			break wait
		case err := <-navDone:
			if err != nil && tctx.Err() == nil {
				b.Log.Debug("browser navigation error", "url", pageURL, "err", err)
			}
		case <-ticker.C:
			if clicks < 3 {
				clicks++
				clickPlayer()
			}
		case <-tctx.Done():
			break wait
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(found) == 0 {
		return nil, nil, ErrNoMedia
	}
	var cands []candidate
	hdr := Headers{}
	for _, f := range found {
		cands = append(cands, candidate{URL: f.url, Proto: f.proto, Score: f.score, Height: heightFrom(f.url)})
		if f.referer != "" && hdr["Referer"] == "" {
			hdr["Referer"] = f.referer
			hdr["Origin"] = originOf(f.referer)
		}
	}
	if hdr["Referer"] == "" {
		hdr = mediaHeaders(pageURL)
	}
	b.Log.Debug("browser captured media", "url", pageURL, "count", len(cands))
	return cands, hdr, nil
}

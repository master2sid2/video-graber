// Package netx provides the outgoing HTTP client used by extractors.
//
// Unless private networks are explicitly allowed, connections to loopback,
// link-local and private addresses are refused at dial time, which protects a
// publicly reachable instance from being used to probe internal services.
package netx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"
)

// MaxBody is the maximum size of a fetched page/playlist.
const MaxBody = 16 << 20

// ErrPrivateAddress is returned for blocked destinations.
var ErrPrivateAddress = errors.New("destination address is not allowed (private network)")

// Client performs outgoing requests.
type Client struct {
	HTTP         *http.Client
	UserAgent    string
	Proxy        string
	AllowPrivate bool
}

// New creates a client. proxy may be empty.
func New(proxy, userAgent string, allowPrivate bool) (*Client, error) {
	c := &Client{UserAgent: userAgent, Proxy: proxy, AllowPrivate: allowPrivate}
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip, err := netip.ParseAddr(host); err == nil && IsPrivate(ip) {
				return ErrPrivateAddress
			}
			return nil
		}
	}
	tr := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if proxy != "" {
		pu, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy: %w", err)
		}
		tr.Proxy = http.ProxyURL(pu)
		// The proxy itself may live on a private address.
		tr.DialContext = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	}
	jar, _ := cookiejar.New(nil)
	c.HTTP = &http.Client{
		Transport: tr,
		Jar:       jar,
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	return c, nil
}

// IsPrivate reports whether ip belongs to a non-public range.
func IsPrivate(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(ip) // carrier-grade NAT
}

// CheckURL validates that u is an http(s) URL pointing to a public host.
// It is used before handing URLs to external tools (ffmpeg, yt-dlp).
func (c *Client) CheckURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("URL has no host")
	}
	if c.AllowPrivate || c.Proxy != "" {
		return nil
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if IsPrivate(ip) {
			return ErrPrivateAddress
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return fmt.Errorf("resolve %s: %w", u.Hostname(), err)
	}
	if slices.ContainsFunc(addrs, IsPrivate) {
		return ErrPrivateAddress
	}
	return nil
}

// Response is a fetched document.
type Response struct {
	URL         string // final URL after redirects
	Status      int
	ContentType string
	Body        []byte
}

// Do performs a request and reads the whole body (up to MaxBody).
func (c *Client) Do(ctx context.Context, method, rawURL string, headers map[string]string, body io.Reader) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme %q", req.URL.Scheme)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,ru;q=0.8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/") && !strings.Contains(ct, "mpegurl") {
		// Do not download media files when we only wanted a page.
		return &Response{URL: resp.Request.URL.String(), Status: resp.StatusCode, ContentType: ct}, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return nil, err
	}
	r := &Response{URL: resp.Request.URL.String(), Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: data}
	if resp.StatusCode >= 400 {
		return r, fmt.Errorf("GET %s: HTTP %d", shortURL(rawURL), resp.StatusCode)
	}
	return r, nil
}

// Get fetches a URL.
func (c *Client) Get(ctx context.Context, rawURL string, headers map[string]string) (*Response, error) {
	return c.Do(ctx, http.MethodGet, rawURL, headers, nil)
}

// PostForm sends an application/x-www-form-urlencoded request.
func (c *Client) PostForm(ctx context.Context, rawURL string, headers map[string]string, form url.Values) (*Response, error) {
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=UTF-8"}
	maps.Copy(h, headers)
	return c.Do(ctx, http.MethodPost, rawURL, h, strings.NewReader(form.Encode()))
}

func shortURL(s string) string {
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

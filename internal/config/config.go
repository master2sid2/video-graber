// Package config parses command line flags and VG_* environment variables.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Mode is the access mode of the application.
type Mode string

const (
	// ModeShared exposes one interface for everybody without authentication.
	ModeShared Mode = "shared"
	// ModeIndividual requires authentication and keeps per-user history.
	ModeIndividual Mode = "individual"
)

// DefaultUserAgent is used for outgoing HTTP requests unless overridden.
const DefaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Config holds the whole runtime configuration.
type Config struct {
	Listen  string
	TLSCert string
	TLSKey  string
	Mode    Mode
	DataDir string
	TempDir string
	WebDir  string

	FFmpeg  string
	FFprobe string

	YtDlp      string
	YtDlpArgs  []string
	YtDlpHosts []string

	Chrome         string
	BrowserTimeout time.Duration

	CookiesFile string
	Proxy       string
	UserAgent   string
	ReportURL   string

	MaxJobs        int
	TempTTL        time.Duration
	JobRetention   time.Duration
	SessionTTL     time.Duration
	SecureCookies  bool
	TrustProxy     bool
	AllowPrivate   bool
	ExtractTimeout time.Duration

	LogLevel        string
	LogFormat       string
	LogOutput       string
	AccessLogOutput string
	AccessLogFormat string

	ShowVersion bool
}

// envOr returns the value of the VG_<NAME> environment variable or def.
func envOr(name, def string) string {
	key := "VG_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(name string, def bool) bool {
	switch strings.ToLower(envOr(name, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(envOr(name, "")); err == nil {
		return d
	}
	return def
}

func envInt(name string, def int) int {
	var v int
	if _, err := fmt.Sscanf(envOr(name, ""), "%d", &v); err == nil {
		return v
	}
	return def
}

// Parse builds the configuration from args (without the program name).
func Parse(args []string) (*Config, error) {
	c := &Config{}
	fs := flag.NewFlagSet("videograber", flag.ContinueOnError)
	var mode, ytArgs, ytHosts string

	fs.StringVar(&c.Listen, "listen", envOr("listen", ":8080"), "address to listen on")
	fs.StringVar(&c.TLSCert, "tls-cert", envOr("tls-cert", ""), "TLS certificate file (enables HTTPS)")
	fs.StringVar(&c.TLSKey, "tls-key", envOr("tls-key", ""), "TLS private key file")
	fs.StringVar(&mode, "mode", envOr("mode", string(ModeShared)), "access mode: shared | individual")
	fs.StringVar(&c.DataDir, "data-dir", envOr("data-dir", "./data"), "directory for the database")
	fs.StringVar(&c.TempDir, "temp-dir", envOr("temp-dir", ""), "directory for temporary files (default: system temp)")
	fs.StringVar(&c.WebDir, "web-dir", envOr("web-dir", ""), "serve the web UI from this directory instead of the embedded copy")

	fs.StringVar(&c.FFmpeg, "ffmpeg", envOr("ffmpeg", "ffmpeg"), "path to the ffmpeg binary")
	fs.StringVar(&c.FFprobe, "ffprobe", envOr("ffprobe", ""), "path to the ffprobe binary (default: next to ffmpeg)")

	fs.StringVar(&c.YtDlp, "ytdlp", envOr("ytdlp", ""), "path to the yt-dlp binary (empty: look up in PATH, \"off\": disable)")
	fs.StringVar(&ytArgs, "ytdlp-args", envOr("ytdlp-args", ""), "extra arguments passed to every yt-dlp call (space separated)")
	fs.StringVar(&ytHosts, "ytdlp-hosts", envOr("ytdlp-hosts", ""), "extra comma separated hosts always handled by yt-dlp")

	fs.StringVar(&c.Chrome, "chrome", envOr("chrome", ""), "path to Chrome/Chromium for headless extraction (\"auto\": look up in PATH, empty: disabled)")
	fs.DurationVar(&c.BrowserTimeout, "browser-timeout", envDuration("browser-timeout", 30*time.Second), "max time to wait for media in the headless browser")

	fs.StringVar(&c.CookiesFile, "cookies-file", envOr("cookies-file", ""), "global Netscape cookies.txt passed to yt-dlp")
	fs.StringVar(&c.Proxy, "proxy", envOr("proxy", ""), "HTTP(S)/SOCKS proxy for outgoing requests, yt-dlp and ffmpeg")
	fs.StringVar(&c.ReportURL, "report-url", envOr("report-url", ""), "where users report unsupported sites (URL or mailto:, {url} is replaced with the page URL)")
	fs.StringVar(&c.UserAgent, "user-agent", envOr("user-agent", DefaultUserAgent), "User-Agent for outgoing requests")

	fs.IntVar(&c.MaxJobs, "max-jobs", envInt("max-jobs", 2), "max concurrent ffmpeg jobs")
	fs.DurationVar(&c.TempTTL, "temp-ttl", envDuration("temp-ttl", time.Hour), "delete prepared temp files not downloaded within this time")
	fs.DurationVar(&c.JobRetention, "job-retention", envDuration("job-retention", 24*time.Hour), "keep finished jobs in the queue for this long")
	fs.DurationVar(&c.SessionTTL, "session-ttl", envDuration("session-ttl", 30*24*time.Hour), "login session lifetime")
	fs.DurationVar(&c.ExtractTimeout, "extract-timeout", envDuration("extract-timeout", 90*time.Second), "max time for a single extraction")
	fs.BoolVar(&c.SecureCookies, "secure-cookies", envBool("secure-cookies", false), "always mark cookies Secure (auto when served over TLS)")
	fs.BoolVar(&c.TrustProxy, "trust-proxy", envBool("trust-proxy", false), "trust X-Forwarded-For / X-Forwarded-Proto headers")
	fs.BoolVar(&c.AllowPrivate, "allow-private", envBool("allow-private", false), "allow extraction from private/loopback network addresses")

	fs.StringVar(&c.LogLevel, "log-level", envOr("log-level", "info"), "app log level: debug | info | warn | error")
	fs.StringVar(&c.LogFormat, "log-format", envOr("log-format", "text"), "app log format: text | json")
	fs.StringVar(&c.LogOutput, "log-output", envOr("log-output", "stdout"), "app log outputs, comma separated: stdout, stderr, <file path>, none")
	fs.StringVar(&c.AccessLogOutput, "access-log-output", envOr("access-log-output", "stdout"), "access log outputs, comma separated: stdout, stderr, <file path>, none")
	fs.StringVar(&c.AccessLogFormat, "access-log-format", envOr("access-log-format", "combined"), "access log format: combined | json")

	fs.BoolVar(&c.ShowVersion, "version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if c.ShowVersion {
		return c, nil
	}

	c.Mode = Mode(strings.ToLower(mode))
	if c.Mode != ModeShared && c.Mode != ModeIndividual {
		return nil, fmt.Errorf("invalid -mode %q: want shared or individual", mode)
	}
	c.YtDlpArgs = strings.Fields(ytArgs)
	for h := range strings.SplitSeq(ytHosts, ",") {
		if h = strings.TrimSpace(strings.ToLower(h)); h != "" {
			c.YtDlpHosts = append(c.YtDlpHosts, h)
		}
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return nil, errors.New("-tls-cert and -tls-key must be set together")
	}
	if c.MaxJobs < 1 {
		c.MaxJobs = 1
	}
	if c.TempDir == "" {
		c.TempDir = filepath.Join(os.TempDir(), "videograber")
	}
	if err := c.resolveBinaries(); err != nil {
		return nil, err
	}
	return c, nil
}

// resolveBinaries turns tool names into absolute paths where possible.
func (c *Config) resolveBinaries() error {
	p, err := exec.LookPath(c.FFmpeg)
	if err != nil {
		return fmt.Errorf("ffmpeg not found (%q): install it and pass -ffmpeg: %w", c.FFmpeg, err)
	}
	c.FFmpeg = p
	if c.FFprobe == "" {
		sibling := filepath.Join(filepath.Dir(p), "ffprobe")
		if _, err := os.Stat(sibling); err == nil {
			c.FFprobe = sibling
		} else if lp, err := exec.LookPath("ffprobe"); err == nil {
			c.FFprobe = lp
		}
	}

	switch strings.ToLower(c.YtDlp) {
	case "off", "none", "false":
		c.YtDlp = ""
	case "":
		if lp, err := exec.LookPath("yt-dlp"); err == nil {
			c.YtDlp = lp
		}
	default:
		lp, err := exec.LookPath(c.YtDlp)
		if err != nil {
			return fmt.Errorf("yt-dlp not found (%q): %w", c.YtDlp, err)
		}
		c.YtDlp = lp
	}

	switch strings.ToLower(c.Chrome) {
	case "", "off", "none", "false":
		c.Chrome = ""
	case "auto":
		c.Chrome = ""
		for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome", "headless-shell"} {
			if lp, err := exec.LookPath(name); err == nil {
				c.Chrome = lp
				break
			}
		}
	default:
		lp, err := exec.LookPath(c.Chrome)
		if err != nil {
			return fmt.Errorf("chrome not found (%q): %w", c.Chrome, err)
		}
		c.Chrome = lp
	}
	return nil
}

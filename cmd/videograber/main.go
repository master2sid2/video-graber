// Command videograber is a self-hosted web application that finds and
// downloads videos from web pages, optionally converting them with ffmpeg.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"videograber/internal/config"
	"videograber/internal/extract"
	"videograber/internal/jobs"
	"videograber/internal/logging"
	"videograber/internal/media"
	"videograber/internal/netx"
	"videograber/internal/server"
	"videograber/internal/store"
	"videograber/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "videograber:", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	debugExtract := len(args) > 0 && args[0] == "extract"
	if debugExtract {
		args = args[1:]
	}
	cfg, err := config.Parse(args)
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		fmt.Println("videograber", version)
		return nil
	}

	appSink, err := logging.OpenSink(cfg.LogOutput)
	if err != nil {
		return err
	}
	defer appSink.Close()
	accessSink, err := logging.OpenSink(cfg.AccessLogOutput)
	if err != nil {
		return err
	}
	defer accessSink.Close()
	log, err := logging.NewAppLogger(appSink, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}
	slog.SetDefault(log)

	client, err := netx.New(cfg.Proxy, cfg.UserAgent, cfg.AllowPrivate)
	if err != nil {
		return err
	}
	var yt *extract.YtDlp
	if cfg.YtDlp != "" {
		yt = &extract.YtDlp{
			Path: cfg.YtDlp, ExtraArgs: cfg.YtDlpArgs, Proxy: cfg.Proxy, CookiesFile: cfg.CookiesFile,
			Hosts: cfg.YtDlpHosts, TempDir: cfg.TempDir, Log: log,
		}
	}
	var browser *extract.Browser
	if cfg.Chrome != "" {
		browser = extract.NewBrowser(cfg.Chrome, cfg.UserAgent, cfg.Proxy, cfg.BrowserTimeout, log)
	}
	prober := &extract.Prober{Path: cfg.FFprobe, Proxy: cfg.Proxy, UA: cfg.UserAgent}
	engine := extract.NewEngine(client, yt, browser, prober, log)

	if debugExtract {
		return extractCommand(engine, cfg, args)
	}

	log.Info("starting videograber", "version", version, "mode", cfg.Mode, "listen", cfg.Listen)
	log.Info("external tools", "ffmpeg", cfg.FFmpeg, "ffprobe", orNone(cfg.FFprobe), "yt-dlp", orNone(cfg.YtDlp), "chrome", orNone(cfg.Chrome))

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	caps := media.DetectCapabilities(ctx, cfg.FFmpeg)
	log.Info("ffmpeg encoders", "video", caps.VideoCodecs, "audio", caps.AudioCodecs)
	runner := &media.Runner{
		FFmpeg: cfg.FFmpeg, YtDlp: yt, Proxy: cfg.Proxy, UserAgent: cfg.UserAgent, Log: log,
		HLSExtensionPicky: media.DetectHLSExtensionPicky(ctx, cfg.FFmpeg),
	}
	jm, err := jobs.NewManager(runner, cfg.MaxJobs, cfg.TempDir, cfg.TempTTL, cfg.JobRetention, log)
	if err != nil {
		return err
	}
	defer jm.Shutdown()

	var webFS fs.FS = web.FS
	if cfg.WebDir != "" {
		webFS = os.DirFS(cfg.WebDir)
		log.Info("serving web UI from disk", "dir", cfg.WebDir)
	}

	srv := server.New(server.Deps{
		Config: cfg, Store: st, Engine: engine, Jobs: jm, Client: client, Caps: caps,
		Log: log, AccessSink: accessSink, Web: webFS, Version: version,
	})
	jm.OnFinish = srv.OnJobFinished

	if tok, err := srv.SetupToken(ctx); err != nil {
		return err
	} else if tok != "" {
		log.Warn("=================================================================")
		log.Warn("INITIAL SETUP REQUIRED: open the web UI and enter this setup token")
		log.Warn("setup token", "token", tok)
		log.Warn("=================================================================")
	}

	go jm.Janitor(ctx)
	go srv.Janitor(ctx)

	// Reopen log files on SIGHUP (logrotate).
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := errors.Join(appSink.Reopen(), accessSink.Reopen()); err != nil {
				log.Error("reopen logs", "err", err)
			} else {
				log.Info("log files reopened")
			}
		}
	}()

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() {
		var err error
		if cfg.TLSCert != "" {
			err = httpSrv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			err = httpSrv.ListenAndServe()
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		jm.Shutdown()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(sctx); err != nil {
			httpSrv.Close()
		}
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(not configured)"
	}
	return s
}

// extractCommand implements `videograber extract [flags] URL` for debugging.
func extractCommand(engine *extract.Engine, cfg *config.Config, args []string) error {
	var target string
	for _, a := range args {
		if len(a) > 0 && a[0] != '-' {
			target = a
		}
	}
	if target == "" {
		return errors.New("usage: videograber extract [flags] URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ExtractTimeout)
	defer cancel()
	res, err := engine.Extract(ctx, target, extract.Options{})
	if err != nil {
		return err
	}
	st, err := res.Resolve(ctx, res.Media.Selected)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"media": res.Media, "stream": st})
}

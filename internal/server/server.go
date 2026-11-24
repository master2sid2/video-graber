// Package server exposes the HTTP API and the web UI.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"videograber/internal/auth"
	"videograber/internal/config"
	"videograber/internal/extract"
	"videograber/internal/jobs"
	"videograber/internal/logging"
	"videograber/internal/media"
	"videograber/internal/netx"
	"videograber/internal/store"
)

const sessionCookie = "vg_session"

// Deps are the collaborators of the server.
type Deps struct {
	Config     *config.Config
	Store      *store.Store
	Engine     *extract.Engine
	Jobs       *jobs.Manager
	Client     *netx.Client
	Caps       media.Capabilities
	Log        *slog.Logger
	AccessSink *logging.Sink
	Web        fs.FS
	Version    string
}

// Server is the HTTP application.
type Server struct {
	cfg        *config.Config
	store      *store.Store
	engine     *extract.Engine
	jobs       *jobs.Manager
	client     *netx.Client
	caps       media.Capabilities
	log        *slog.Logger
	accessSink *logging.Sink
	web        fs.FS
	version    string

	loginLimiter *auth.Limiter
	extractSem   chan struct{}

	setupMu    sync.Mutex
	setupToken string

	sessMu   sync.Mutex
	sessions map[string]*mediaSession
}

// New creates the server.
func New(d Deps) *Server {
	s := &Server{
		cfg: d.Config, store: d.Store, engine: d.Engine, jobs: d.Jobs, client: d.Client, caps: d.Caps,
		log: d.Log, accessSink: d.AccessSink, web: d.Web, version: d.Version,
		loginLimiter: auth.NewLimiter(10, time.Minute),
		extractSem:   make(chan struct{}, 8),
		sessions:     map[string]*mediaSession{},
	}
	return s
}

func (s *Server) individual() bool { return s.cfg.Mode == config.ModeIndividual }

// SetupToken generates (or returns) the one-time setup token. It returns ""
// when setup is not required.
func (s *Server) SetupToken(ctx context.Context) (string, error) {
	if !s.individual() {
		return "", nil
	}
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if n > 0 {
		s.setupToken = ""
		return "", nil
	}
	if s.setupToken == "" {
		s.setupToken = auth.RandomToken(24)
	}
	return s.setupToken, nil
}

func hashToken(t string) string { return auth.HashToken(t) }

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()

	// Public
	api.HandleFunc("GET /api/status", s.handleStatus)
	api.HandleFunc("POST /api/setup", s.handleSetup)
	api.HandleFunc("POST /api/login", s.handleLogin)
	api.HandleFunc("POST /api/logout", s.handleLogout)
	api.HandleFunc("GET /api/invites/{code}", s.handleInviteCheck)
	api.HandleFunc("POST /api/register", s.handleRegister)

	// Account
	api.HandleFunc("PUT /api/me/password", s.requireUser(s.handleChangePassword))
	api.HandleFunc("GET /api/me/prefs", s.requireUser(s.handleGetPrefs))
	api.HandleFunc("PUT /api/me/prefs", s.requireUser(s.handlePutPrefs))
	api.HandleFunc("GET /api/cookies", s.requireUser(s.handleGetCookies))
	api.HandleFunc("PUT /api/cookies", s.requireUser(s.handlePutCookies))
	api.HandleFunc("DELETE /api/cookies", s.requireUser(s.handleDeleteCookies))

	// Extraction
	api.HandleFunc("POST /api/extract", s.requireUser(s.handleExtract))
	api.HandleFunc("POST /api/extract/{id}/resolve", s.requireUser(s.handleResolve))

	// Jobs
	api.HandleFunc("GET /api/jobs", s.requireUser(s.handleListJobs))
	api.HandleFunc("POST /api/jobs", s.requireUser(s.handleCreateJobs))
	api.HandleFunc("GET /api/jobs/{id}", s.requireUser(s.handleGetJob))
	api.HandleFunc("POST /api/jobs/{id}/cancel", s.requireUser(s.handleCancelJob))
	api.HandleFunc("DELETE /api/jobs/{id}", s.requireUser(s.handleDeleteJob))
	api.HandleFunc("GET /api/jobs/{id}/download", s.requireUser(s.handleDownload))
	api.HandleFunc("GET /api/events", s.requireUser(s.handleEvents))

	// Library (individual mode)
	api.HandleFunc("GET /api/categories", s.requireUser(s.handleListCategories))
	api.HandleFunc("POST /api/categories", s.requireUser(s.handleCreateCategory))
	api.HandleFunc("PATCH /api/categories/{id}", s.requireUser(s.handleUpdateCategory))
	api.HandleFunc("DELETE /api/categories/{id}", s.requireUser(s.handleDeleteCategory))
	api.HandleFunc("GET /api/history", s.requireUser(s.handleListHistory))
	api.HandleFunc("PATCH /api/history/{id}", s.requireUser(s.handleUpdateHistory))
	api.HandleFunc("DELETE /api/history/{id}", s.requireUser(s.handleDeleteHistory))
	api.HandleFunc("DELETE /api/history", s.requireUser(s.handleClearHistory))

	// Administration
	api.HandleFunc("GET /api/admin/users", s.requireAdmin(s.handleListUsers))
	api.HandleFunc("POST /api/admin/users", s.requireAdmin(s.handleCreateUser))
	api.HandleFunc("PATCH /api/admin/users/{id}", s.requireAdmin(s.handleUpdateUser))
	api.HandleFunc("DELETE /api/admin/users/{id}", s.requireAdmin(s.handleDeleteUser))
	api.HandleFunc("GET /api/admin/invites", s.requireAdmin(s.handleListInvites))
	api.HandleFunc("POST /api/admin/invites", s.requireAdmin(s.handleCreateInvite))
	api.HandleFunc("DELETE /api/admin/invites/{id}", s.requireAdmin(s.handleDeleteInvite))
	api.HandleFunc("GET /api/admin/cookies", s.requireAdmin(s.handleGetGlobalCookies))
	api.HandleFunc("PUT /api/admin/cookies", s.requireAdmin(s.handlePutGlobalCookies))
	api.HandleFunc("DELETE /api/admin/cookies", s.requireAdmin(s.handleDeleteGlobalCookies))

	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unknown endpoint")
	})

	root := http.NewServeMux()
	root.Handle("/api/", http.NewCrossOriginProtection().Handler(s.authenticate(api)))
	root.Handle("/", s.staticHandler())

	var h http.Handler = root
	h = securityHeaders(h)
	h = s.recoverer(h)
	h = s.accessLog(h)
	return h
}

// staticHandler serves the UI. Embedded files never change while running,
// so their ETags are computed once.
func (s *Server) staticHandler() http.Handler {
	var etags sync.Map
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		f, err := s.web.Open(name)
		if err != nil {
			// Unknown paths fall back to the SPA shell.
			name = "index.html"
			if f, err = s.web.Open(name); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if s.cfg.WebDir == "" {
			tag, ok := etags.Load(name)
			if !ok {
				h := sha256.New()
				io.Copy(h, rs)
				rs.Seek(0, io.SeekStart)
				tag = `"` + hex.EncodeToString(h.Sum(nil))[:20] + `"`
				etags.Store(name, tag)
			}
			w.Header().Set("ETag", tag.(string))
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, st.ModTime(), rs)
	})
}

// Janitor cleans expired media sessions and login sessions.
func (s *Server) Janitor(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.sessMu.Lock()
		for id, ms := range s.sessions {
			if time.Since(ms.lastUsed()) > 3*time.Hour {
				delete(s.sessions, id)
			}
		}
		s.sessMu.Unlock()
		if s.individual() {
			_ = s.store.PurgeSessions(ctx)
		}
	}
}

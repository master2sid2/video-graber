package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"videograber/internal/auth"
	"videograber/internal/store"
)

type statusResponse struct {
	Mode          string   `json:"mode"`
	SetupRequired bool     `json:"setup_required"`
	User          any      `json:"user"`
	Version       string   `json:"version"`
	Features      features `json:"features"`
	Extractors    []string `json:"extractors"`
	Capabilities  any      `json:"capabilities"`
	MaxJobs       int      `json:"max_jobs"`
	Modules       []string `json:"modules,omitempty"`
	ReportURL     string   `json:"report_url,omitempty"`
}

type features struct {
	YtDlp   bool `json:"ytdlp"`
	Browser bool `json:"browser"`
	FFprobe bool `json:"ffprobe"`
	History bool `json:"history"`
	Cookies bool `json:"cookies"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := statusResponse{
		Mode: string(s.cfg.Mode), Version: s.version, MaxJobs: s.cfg.MaxJobs,
		Features: features{
			YtDlp: s.cfg.YtDlp != "", Browser: s.cfg.Chrome != "", FFprobe: s.cfg.FFprobe != "",
			History: s.individual(), Cookies: s.cfg.YtDlp != "" && s.individual(),
		},
	}
	if tok, err := s.SetupToken(r.Context()); err == nil && tok != "" {
		resp.SetupRequired = true
	}
	if u := userFrom(r); u != nil {
		resp.User = u
	}
	if !s.individual() || resp.User != nil {
		resp.Extractors = s.engine.Extractors()
		resp.Capabilities = s.caps
		resp.Modules = s.engine.Tried()
		resp.ReportURL = s.cfg.ReportURL
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) error {
	token := auth.RandomToken(32)
	if err := s.store.CreateSession(r.Context(), hashToken(token), u.ID, s.cfg.SessionTTL, s.clientIP(r), r.UserAgent()); err != nil {
		return err
	}
	_ = s.store.TouchLogin(r.Context(), u.ID)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.cfg.SecureCookies || s.isHTTPS(r), Expires: time.Now().Add(s.cfg.SessionTTL),
	})
	return nil
}

type credentials struct {
	Token    string `json:"token,omitempty"`
	Invite   string `json:"invite,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) limited(w http.ResponseWriter, r *http.Request) bool {
	if !s.loginLimiter.Allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again in a minute")
		return true
	}
	return false
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.individual() {
		writeError(w, http.StatusNotFound, "not available in shared mode")
		return
	}
	if s.limited(w, r) {
		return
	}
	var c credentials
	if err := readJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tok, err := s.SetupToken(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tok == "" {
		writeError(w, http.StatusConflict, "setup is already completed")
		return
	}
	if !auth.EqualTokens(strings.TrimSpace(c.Token), tok) {
		writeError(w, http.StatusForbidden, "invalid setup token (see the application log)")
		return
	}
	if err := validateCredentials(c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), c.Username, hash, true)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.setupMu.Lock()
	s.setupToken = ""
	s.setupMu.Unlock()
	s.log.Info("initial setup completed", "admin", u.Username)
	if err := s.startSession(w, r, u); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func validateCredentials(c credentials) error {
	if err := auth.ValidateUsername(c.Username); err != nil {
		return err
	}
	return auth.ValidatePassword(c.Password)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.individual() {
		writeError(w, http.StatusNotFound, "not available in shared mode")
		return
	}
	if s.limited(w, r) {
		return
	}
	var c credentials
	if err := readJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.store.UserByName(r.Context(), strings.TrimSpace(c.Username))
	if errors.Is(err, store.ErrNotFound) {
		auth.CheckDummy(c.Password)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ok, err := auth.CheckPassword(c.Password, u.PasswordHash)
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if u.Disabled {
		writeError(w, http.StatusForbidden, "this account is disabled")
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("user logged in", "user", u.Username, "ip", s.clientIP(r))
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if h := sessionHashFrom(r); h != "" {
		_ = s.store.DeleteSession(r.Context(), h)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleInviteCheck(w http.ResponseWriter, r *http.Request) {
	if !s.individual() {
		writeError(w, http.StatusNotFound, "not available in shared mode")
		return
	}
	inv, err := s.store.InviteByCode(r.Context(), r.PathValue("code"))
	if err != nil || !inv.Usable() {
		writeError(w, http.StatusNotFound, "this invite link is invalid or expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"valid": true})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.individual() {
		writeError(w, http.StatusNotFound, "not available in shared mode")
		return
	}
	if s.limited(w, r) {
		return
	}
	var c credentials
	if err := readJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateCredentials(c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, err := s.store.RegisterWithInvite(r.Context(), strings.TrimSpace(c.Invite), c.Username, hash)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusForbidden, "this invite link is invalid or expired")
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "this username is already taken")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	s.log.Info("user registered with invite", "user", u.Username)
	if err := s.startSession(w, r, u); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if u == nil {
		writeError(w, http.StatusNotFound, "not available in shared mode")
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if ok, _ := auth.CheckPassword(req.Current, u.PasswordHash); !ok {
		writeError(w, http.StatusForbidden, "the current password is wrong")
		return
	}
	if err := auth.ValidatePassword(req.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.store.SetPassword(r.Context(), u.ID, hash); err != nil {
		s.internalError(w, r, err)
		return
	}
	// Log out every other device.
	_ = s.store.DeleteUserSessions(r.Context(), u.ID, sessionHashFrom(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- preferences ----------

func (s *Server) handleGetPrefs(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	prefs := "{}"
	if u != nil {
		var err error
		if prefs, err = s.store.UserPrefs(r.Context(), u.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(prefs))
}

func (s *Server) handlePutPrefs(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if u == nil {
		writeError(w, http.StatusNotFound, "preferences are stored in the browser in shared mode")
		return
	}
	var v map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid preferences")
		return
	}
	b, _ := json.Marshal(v)
	if err := s.store.SetUserPrefs(r.Context(), u.ID, string(b)); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---------- cookies ----------

// legacyGlobalCookiesKey held instance-wide cookies in earlier versions.
const legacyGlobalCookiesKey = "ytdlp_cookies"

// cookiesInfo describes the caller's cookies without revealing them.
type cookiesInfo struct {
	Present bool `json:"present"`
	Count   int  `json:"count"`
	Domains int  `json:"domains"`
}

// cookieStats counts cookies without revealing them.
func cookieStats(content string) (count, domains int) {
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) >= 7 {
			count++
			seen[strings.TrimPrefix(f[0], ".")] = true
		}
	}
	return count, len(seen)
}

func validateCookies(content string) error {
	if len(content) > 1<<20 {
		return errors.New("cookies file is too large (1 MB max)")
	}
	if n, _ := cookieStats(content); n == 0 {
		return errors.New("no cookies found: export them in the Netscape cookies.txt format")
	}
	return nil
}

// cookiesFor returns the caller's own cookies. Cookies are strictly
// personal: nobody downloads with somebody else's account, so there is no
// global fallback and none at all in shared mode (no accounts there).
func (s *Server) cookiesFor(r *http.Request) string {
	u := userFrom(r)
	if u == nil {
		return ""
	}
	c, _ := s.store.UserCookies(r.Context(), u.ID)
	return c
}

// cookieUser returns the user for cookie endpoints (individual mode only).
func (s *Server) cookieUser(w http.ResponseWriter, r *http.Request) *store.User {
	u := userFrom(r)
	if u == nil {
		writeError(w, http.StatusNotFound, "cookies are personal and only available in individual mode")
	}
	return u
}

func (s *Server) writeCookiesInfo(w http.ResponseWriter, r *http.Request, u *store.User) {
	content, err := s.store.UserCookies(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var info cookiesInfo
	info.Count, info.Domains = cookieStats(content)
	info.Present = info.Count > 0
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleGetCookies(w http.ResponseWriter, r *http.Request) {
	if u := s.cookieUser(w, r); u != nil {
		s.writeCookiesInfo(w, r, u)
	}
}

func (s *Server) handlePutCookies(w http.ResponseWriter, r *http.Request) {
	u := s.cookieUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateCookies(req.Content); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetUserCookies(r.Context(), u.ID, req.Content); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeCookiesInfo(w, r, u)
}

func (s *Server) handleDeleteCookies(w http.ResponseWriter, r *http.Request) {
	u := s.cookieUser(w, r)
	if u == nil {
		return
	}
	if err := s.store.SetUserCookies(r.Context(), u.ID, ""); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeCookiesInfo(w, r, u)
}

// PurgeGlobalCookies deletes instance-wide cookies stored by earlier
// versions (admin "global cookies" or uploads in shared mode): cookies are
// personal now and must not be used for everybody's downloads.
func (s *Server) PurgeGlobalCookies(ctx context.Context) {
	if c, err := s.store.GetSetting(ctx, legacyGlobalCookiesKey); err == nil && c != "" {
		if err := s.store.SetSetting(ctx, legacyGlobalCookiesKey, ""); err == nil {
			s.log.Warn("removed instance-wide YouTube cookies from the database: cookies are personal now (Settings → YouTube cookies)")
		}
	}
}

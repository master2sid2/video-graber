package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/master2sid2/video-graber/internal/store"
)

type ctxKey int

const (
	keyUser ctxKey = iota
	keySessionHash
	keyLogUser
)

// userFrom returns the authenticated user (nil in shared mode).
func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(keyUser).(*store.User)
	return u
}

func sessionHashFrom(r *http.Request) string {
	s, _ := r.Context().Value(keySessionHash).(string)
	return s
}

// logUser lets handlers tell the access log who the user is.
type logUser struct{ name string }

// statusWriter records status and size for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

// clientIP returns the client address, honouring proxies when trusted.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ip, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(ip)
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return strings.TrimSpace(xr)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) isHTTPS(r *http.Request) bool {
	return r.TLS != nil || s.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// accessLog writes one line per request in combined or JSON format.
func (s *Server) accessLog(next http.Handler) http.Handler {
	if !s.accessSink.Enabled() {
		return next
	}
	jsonFmt := strings.EqualFold(s.cfg.AccessLogFormat, "json")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lu := &logUser{name: "-"}
		r = r.WithContext(context.WithValue(r.Context(), keyLogUser, lu))
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
					status = http.StatusInternalServerError
				}
				defer panic(rec)
			}
			dur := time.Since(start)
			ip := s.clientIP(r)
			if jsonFmt {
				line, _ := json.Marshal(map[string]any{
					"time": start.Format(time.RFC3339), "ip": ip, "user": lu.name, "method": r.Method,
					"path": r.URL.RequestURI(), "proto": r.Proto, "status": status, "bytes": sw.bytes,
					"duration_ms": dur.Milliseconds(), "referer": r.Referer(), "user_agent": r.UserAgent(),
				})
				_, _ = s.accessSink.Write(append(line, '\n'))
				return
			}
			fmt.Fprintf(s.accessSink, "%s - %s [%s] \"%s %s %s\" %d %d \"%s\" \"%s\" %dms\n",
				ip, lu.name, start.Format("02/Jan/2006:15:04:05 -0700"), r.Method, r.URL.RequestURI(), r.Proto,
				status, sw.bytes, r.Referer(), r.UserAgent(), dur.Milliseconds())
		}()
		next.ServeHTTP(sw, r)
	})
}

// recoverer turns panics into 500 responses (except deliberate aborts).
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				s.log.Error("panic", "err", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets a strict CSP and friends.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: http: data:; style-src 'self'; "+
			"script-src 'self'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

// authenticate loads the session user (individual mode).
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.individual() {
			if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
				hash := hashToken(c.Value)
				if u, err := s.store.SessionUser(r.Context(), hash); err == nil && !u.Disabled {
					ctx := context.WithValue(r.Context(), keyUser, u)
					ctx = context.WithValue(ctx, keySessionHash, hash)
					if lu, ok := ctx.Value(keyLogUser).(*logUser); ok {
						lu.name = u.Username
					}
					r = r.WithContext(ctx)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireUser rejects anonymous requests in individual mode.
func (s *Server) requireUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.individual() && userFrom(r) == nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		h(w, r)
	}
}

// requireAdmin allows only administrators (individual mode).
func (s *Server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r)
		if !s.individual() {
			writeError(w, http.StatusNotFound, "not available in shared mode")
			return
		}
		if u == nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if !u.IsAdmin {
			writeError(w, http.StatusForbidden, "administrator access required")
			return
		}
		h(w, r)
	}
}

// ---------- JSON helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

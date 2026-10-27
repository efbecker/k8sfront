// Package api exposes the REST and WebSocket interface consumed by apps/web.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/remote"
	"github.com/efbecker/k8sfront/apps/api/internal/secrets"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// Version is set at build time.
var Version = "dev"

// Server wires dependencies into HTTP handlers.
type Server struct {
	Store          store.Store
	Secrets        *secrets.Manager
	Engine         *jobs.Engine
	Connector      *remote.Connector
	Auth           *Auth
	Log            *slog.Logger
	WebDir         string
	AllowedOrigins []string
}

// Handler builds the HTTP handler tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	pub := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, h) }
	priv := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAuth(h)) }

	pub("GET /api/v1/health", s.health)
	pub("POST /api/v1/auth/login", s.login)
	pub("POST /api/v1/auth/logout", s.logout)
	priv("GET /api/v1/auth/me", s.me)

	priv("GET /api/v1/servers", s.listServers)
	priv("POST /api/v1/servers", s.createServer)
	priv("GET /api/v1/servers/{id}", s.getServer)
	priv("PATCH /api/v1/servers/{id}", s.updateServer)
	priv("DELETE /api/v1/servers/{id}", s.deleteServer)
	priv("GET /api/v1/servers/{id}/host-key", s.getHostKey)
	priv("DELETE /api/v1/servers/{id}/host-key", s.resetHostKey)
	priv("POST /api/v1/servers/{id}/preflight", s.preflightOne)
	priv("POST /api/v1/preflight", s.preflightMany)

	priv("GET /api/v1/jobs", s.listJobs)
	priv("GET /api/v1/jobs/{id}", s.getJob)
	priv("GET /api/v1/jobs/{id}/logs", s.getJobLogs)
	priv("POST /api/v1/jobs/{id}/cancel", s.cancelJob)
	priv("POST /api/v1/jobs/{id}/retry", s.retryJob)
	priv("GET /api/v1/jobs/{id}/stream", s.streamJob)

	priv("GET /api/v1/audit", s.listAudit)

	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	}))
	if s.WebDir != "" {
		mux.Handle("/", spaHandler(s.WebDir))
	}
	return s.withMiddleware(mux)
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
			// CSRF defence in depth (cookies are already SameSite=Strict):
			// state-changing requests must be JSON, which a cross-site form cannot send.
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.ContentLength > 0 &&
				!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "requests must use application/json")
				return
			}
		}
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.Log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController and websocket.Accept reach the hijacker.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Auth.Verify(r); err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- JSON helpers ----------------------------------------------------------------

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: code, Message: msg})
}

func (s *Server) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, context.Canceled):
		writeError(w, 499, "cancelled", "request cancelled")
	default:
		s.Log.Error("internal error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) audit(r *http.Request, action, targetType, targetID string, details any) {
	var raw json.RawMessage
	if details != nil {
		raw, _ = json.Marshal(details)
	}
	if err := s.Store.AppendAudit(r.Context(), &store.AuditEntry{Actor: "admin", Action: action, TargetType: targetType,
		TargetID: targetID, Details: raw, RemoteAddr: clientIP(r)}); err != nil {
		s.Log.Error("audit write failed", "err", err)
	}
}

// --- misc handlers ----------------------------------------------------------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": Version})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if !s.Auth.CheckToken(req.Token) {
		time.Sleep(500 * time.Millisecond) // slow down guessing
		s.audit(r, "auth.login_failed", "", "", nil)
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid admin token")
		return
	}
	s.Auth.Issue(w)
	s.audit(r, "auth.login", "", "", nil)
	writeJSON(w, http.StatusOK, map[string]string{"user": "admin"})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"user": "admin", "version": Version})
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Store.ListAudit(r.Context(), 200)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// spaHandler serves the built frontend, falling back to index.html.
func spaHandler(dir string) http.Handler {
	fsrv := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fsrv.ServeHTTP(w, r)
	})
}

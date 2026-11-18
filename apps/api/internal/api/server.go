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
	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
	"github.com/efbecker/k8sfront/apps/api/internal/kube"
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
	Versions       *k8sver.Fetcher
	Kube           *kube.Manager
	Log            *slog.Logger
	WebDir         string
	AllowedOrigins []string
}

// Handler builds the HTTP handler tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	pub := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, h) }
	// priv: reads need at least the viewer role, anything that changes state
	// needs operator. opr/adm raise the minimum for sensitive routes.
	priv := func(pattern string, h http.HandlerFunc) {
		min := store.RoleOperator
		if strings.HasPrefix(pattern, "GET ") {
			min = store.RoleViewer
		}
		mux.Handle(pattern, s.require(min, h))
	}
	opr := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.require(store.RoleOperator, h)) }
	adm := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.require(store.RoleAdmin, h)) }
	self := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.require(store.RoleViewer, h)) }

	pub("GET /api/v1/health", s.health)
	pub("GET /api/v1/auth/providers", s.authProviders)
	pub("POST /api/v1/auth/login", s.login)
	pub("POST /api/v1/auth/logout", s.logout)
	self("GET /api/v1/auth/me", s.me)
	self("POST /api/v1/auth/password", s.changeOwnPassword)

	// Users and authentication (admin only).
	adm("GET /api/v1/users", s.listUsers)
	adm("POST /api/v1/users", s.createUser)
	adm("PATCH /api/v1/users/{id}", s.updateUser)
	adm("DELETE /api/v1/users/{id}", s.deleteUser)
	adm("GET /api/v1/auth/ldap", s.getLDAP)
	adm("PUT /api/v1/auth/ldap", s.putLDAP)
	adm("POST /api/v1/auth/ldap/test", s.testLDAP)

	priv("GET /api/v1/servers", s.listServers)
	priv("POST /api/v1/servers", s.createServer)
	priv("GET /api/v1/servers/{id}", s.getServer)
	priv("PATCH /api/v1/servers/{id}", s.updateServer)
	priv("DELETE /api/v1/servers/{id}", s.deleteServer)
	priv("GET /api/v1/servers/{id}/removal", s.serverRemovalPlan)
	priv("POST /api/v1/servers/{id}/remove", s.removeServer)
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

	priv("GET /api/v1/kubernetes/versions", s.kubernetesVersions)
	priv("GET /api/v1/clusters", s.listClusters)
	priv("POST /api/v1/clusters", s.createCluster)
	priv("GET /api/v1/clusters/{id}", s.getCluster)
	priv("DELETE /api/v1/clusters/{id}", s.deleteCluster)
	priv("POST /api/v1/clusters/{id}/bootstrap", s.bootstrapCluster)
	opr("GET /api/v1/clusters/{id}/kubeconfig", s.downloadKubeconfig) // cluster-admin credentials
	priv("GET /api/v1/clusters/{id}/k8s/overview", s.kubeOverview)
	priv("GET /api/v1/clusters/{id}/k8s/namespaces", s.kubeNamespaces)
	priv("GET /api/v1/clusters/{id}/k8s/namespaces-usage", s.namespaceUsage)
	priv("GET /api/v1/clusters/{id}/k8s/namespaces-usage/{ns}", s.namespaceUsage)
	priv("GET /api/v1/clusters/{id}/k8s/workloads", s.kubeWorkloads)
	priv("GET /api/v1/clusters/{id}/k8s/workloads/{ns}/{kind}/{name}/related", s.workloadRelated)
	priv("GET /api/v1/clusters/{id}/k8s/network", s.kubeNetwork)
	priv("GET /api/v1/clusters/{id}/k8s/access", s.kubeAccess)
	priv("GET /api/v1/clusters/{id}/k8s/access/probe", s.accessProbe)
	priv("POST /api/v1/clusters/{id}/loadbalancer", s.setLoadBalancer)
	priv("GET /api/v1/clusters/{id}/k8s/topology/stream", s.topologyStream)
	priv("POST /api/v1/clusters/{id}/k8s/nodes/{name}/cordon", s.cordonNode)
	priv("POST /api/v1/clusters/{id}/k8s/nodes/{name}/uncordon", s.uncordonNode)
	priv("POST /api/v1/clusters/{id}/k8s/nodes/{name}/drain", s.drainNode)

	// Phase 4: deploy center.
	priv("POST /api/v1/deploy/render", s.renderApp)
	priv("POST /api/v1/clusters/{id}/deploy/app", s.deployApp)
	priv("POST /api/v1/clusters/{id}/apply", s.applyManifest)
	priv("POST /api/v1/clusters/{id}/delete", s.deleteObjects)
	priv("GET /api/v1/clusters/{id}/helm/releases", s.helmReleases)
	priv("POST /api/v1/clusters/{id}/helm", s.helmJob)

	// Phase 5: updates & lifecycle.
	priv("GET /api/v1/clusters/{id}/upgrades/plan", s.upgradePlan)
	priv("POST /api/v1/clusters/{id}/upgrades", s.startUpgrade)
	priv("POST /api/v1/clusters/{id}/nodes", s.addNode)
	priv("DELETE /api/v1/clusters/{id}/nodes/{serverId}", s.removeNode)
	priv("POST /api/v1/clusters/{id}/os-updates/check", s.osCheck)
	priv("POST /api/v1/clusters/{id}/os-updates", s.osUpdate)
	priv("POST /api/v1/clusters/{id}/rolling-reboot", s.rollingReboot)
	priv("POST /api/v1/clusters/{id}/certs", s.certsJob)

	// Registries & images.
	priv("GET /api/v1/registries", s.listRegistries)
	priv("POST /api/v1/registries", s.createRegistry)
	priv("GET /api/v1/registries/{id}", s.getRegistry)
	priv("PATCH /api/v1/registries/{id}", s.updateRegistry)
	priv("DELETE /api/v1/registries/{id}", s.deleteRegistry)
	priv("POST /api/v1/registries/{id}/check", s.checkRegistry)
	priv("POST /api/v1/clusters/{id}/registries/sync", s.syncRegistries)
	priv("GET /api/v1/clusters/{id}/k8s/images", s.imageCatalog)
	priv("POST /api/v1/clusters/{id}/images/prepull", s.prepullImages)
	priv("POST /api/v1/clusters/{id}/images/prune", s.pruneImages)

	opr("GET /api/v1/audit", s.listAudit)

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
		writeError(w, http.StatusConflict, "conflict", strings.TrimPrefix(err.Error(), store.ErrConflict.Error()+": "))
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
	s.auditAs(r, actor(r), action, targetType, targetID, details)
}

// auditAs records an entry for an explicit actor (login, before a session exists).
func (s *Server) auditAs(r *http.Request, who, action, targetType, targetID string, details any) {
	var raw json.RawMessage
	if details != nil {
		raw, _ = json.Marshal(details)
	}
	if err := s.Store.AppendAudit(r.Context(), &store.AuditEntry{Actor: who, Action: action, TargetType: targetType,
		TargetID: targetID, Details: raw, RemoteAddr: clientIP(r)}); err != nil {
		s.Log.Error("audit write failed", "err", err)
	}
}

// --- misc handlers ----------------------------------------------------------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": Version})
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

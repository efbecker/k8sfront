package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/efbecker/k8sfront/apps/api/internal/kube"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var namespaceRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// kubeCluster resolves the cluster client or writes the error response.
func (s *Server) kubeCluster(w http.ResponseWriter, r *http.Request) (*kube.Cluster, bool) {
	cl, err := s.Kube.Get(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		return cl, true
	case errors.Is(err, kube.ErrNoKubeconfig):
		writeError(w, http.StatusConflict, "not_ready", err.Error())
	case errors.Is(err, store.ErrNotFound):
		s.storeError(w, err)
	default:
		s.kubeError(w, err)
	}
	return nil, false
}

// kubeError maps failures talking to the cluster to 502 (or 403 for RBAC).
func (s *Server) kubeError(w http.ResponseWriter, err error) {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		writeError(w, http.StatusForbidden, "forbidden", "a API do Kubernetes recusou a credencial: "+err.Error())
		return
	}
	s.Log.Warn("kubernetes api", "err", err)
	writeError(w, http.StatusBadGateway, "upstream", "não foi possível falar com a API do Kubernetes: "+err.Error())
}

func namespaceParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	ns := r.URL.Query().Get("namespace")
	if ns != "" && !namespaceRe.MatchString(ns) {
		writeError(w, http.StatusBadRequest, "validation", "namespace inválido")
		return "", false
	}
	return ns, true
}

func (s *Server) kubeOverview(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kube.RequestTimeout)
	defer cancel()
	ov, err := kube.GetOverview(ctx, cl.Client)
	if err != nil {
		s.kubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (s *Server) kubeNamespaces(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kube.RequestTimeout)
	defer cancel()
	ns, err := kube.Namespaces(ctx, cl.Client)
	if err != nil {
		s.kubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

func (s *Server) kubeWorkloads(w http.ResponseWriter, r *http.Request) {
	ns, ok := namespaceParam(w, r)
	if !ok {
		return
	}
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kube.RequestTimeout)
	defer cancel()
	out, err := kube.ListWorkloads(ctx, cl.Client, ns)
	if err != nil {
		s.kubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) kubeNetwork(w http.ResponseWriter, r *http.Request) {
	ns, ok := namespaceParam(w, r)
	if !ok {
		return
	}
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kube.RequestTimeout)
	defer cancel()
	out, err := kube.ListNetwork(ctx, cl.Client, ns)
	if err != nil {
		s.kubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// topologyStream sends a snapshot of nodes, pods and services followed by
// live upsert/delete events. When the client falls behind the socket is closed
// with 1013 and the client reconnects for a fresh snapshot.
func (s *Server) topologyStream(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.AllowedOrigins})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())

	topo := cl.Topology()
	sub, initial := topo.Subscribe()
	defer topo.Unsubscribe(sub)

	send := func(ev kube.TopoEvent) error {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return wsjson.Write(wctx, conn, ev)
	}
	for _, ev := range initial {
		if err := send(ev); err != nil {
			return
		}
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case ev, ok := <-sub.C:
			if !ok {
				conn.Close(websocket.StatusTryAgainLater, "resync")
				return
			}
			if err := send(ev); err != nil {
				return
			}
		}
	}
}

package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/kube"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"time"
)

func (s *Server) listAccess(ctx context.Context, w http.ResponseWriter, r *http.Request, ns string) (*kube.Access, bool) {
	c, err := s.Store.GetCluster(ctx, r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return nil, false
	}
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return nil, false
	}
	var lb kube.LoadBalancerInfo
	if opts, err := bootstrap.ParseOptions(c.Options); err == nil && opts.LoadBalancer != nil {
		lb.Provider, lb.Addresses = string(opts.LoadBalancer.Provider), opts.LoadBalancer.Addresses
	}
	out, err := kube.ListAccess(ctx, cl.Client, ns, lb)
	if err != nil {
		s.kubeError(w, err)
		return nil, false
	}
	return out, true
}

// kubeAccess lists how each Service is reached from outside the cluster.
func (s *Server) kubeAccess(w http.ResponseWriter, r *http.Request) {
	ns, ok := namespaceParam(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kube.RequestTimeout)
	defer cancel()
	if out, ok := s.listAccess(ctx, w, r, ns); ok {
		writeJSON(w, http.StatusOK, out)
	}
}

// accessProbe GETs one of the links of a Service from the k8sfront server.
// Only URLs that the access listing produced for that Service are accepted,
// so the endpoint cannot be used to reach arbitrary hosts.
func (s *Server) accessProbe(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, svc, u := q.Get("namespace"), q.Get("service"), q.Get("url")
	if !namespaceRe.MatchString(ns) || svc == "" || u == "" {
		writeError(w, http.StatusBadRequest, "validation", "namespace, service e url são obrigatórios")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kube.RequestTimeout)
	defer cancel()
	acc, ok := s.listAccess(ctx, w, r, ns)
	if !ok {
		return
	}
	if !acc.HasLink(ns, svc, u) {
		writeError(w, http.StatusBadRequest, "validation", "essa URL não é um acesso conhecido do Service")
		return
	}
	pctx, pcancel := context.WithTimeout(r.Context(), kube.ProbeTimeout+time.Second)
	defer pcancel()
	writeJSON(w, http.StatusOK, kube.Probe(pctx, u))
}

// setLoadBalancer installs MetalLB (or updates its address pool) on a ready cluster.
func (s *Server) setLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var in bootstrap.LoadBalancer
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if in.Provider == "" {
		in.Provider = bootstrap.LBMetalLB
	}
	addrs := in.Addresses[:0]
	for _, a := range in.Addresses {
		if a = strings.TrimSpace(a); a != "" {
			addrs = append(addrs, a)
		}
	}
	in.Addresses = addrs
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	members, err := s.Store.ListClusterServers(r.Context(), c.ID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	spec, err := ops.SpecFor(c, members)
	if err == nil {
		spec.Options.LoadBalancer = &in
		err = spec.Validate()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", strings.TrimPrefix(err.Error(), bootstrap.ErrInvalidSpec.Error()+": "))
		return
	}
	s.submitMaintenance(w, r, c, ops.KindLoadBalancer, ops.LoadBalancerParams{ClusterID: c.ID, LoadBalancer: in},
		map[string]any{"provider": in.Provider, "addresses": in.Addresses})
}

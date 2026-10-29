package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/google/uuid"
)

func (s *Server) kubernetesVersions(w http.ResponseWriter, r *http.Request) {
	rel, err := s.Versions.Stable(r.Context())
	if err != nil {
		s.Log.Warn("kubernetes versions", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", "não foi possível consultar dl.k8s.io: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rel)
}

type clusterNodeInput struct {
	ServerID string         `json:"serverId"`
	Role     store.NodeRole `json:"role"`
}

type clusterInput struct {
	Name              string             `json:"name"`
	KubernetesVersion string             `json:"kubernetesVersion"`
	PodCIDR           string             `json:"podCidr"`
	ServiceCIDR       string             `json:"serviceCidr"`
	CNI               string             `json:"cni"`
	Topology          string             `json:"topology"`
	Options           bootstrap.Options  `json:"options"`
	Nodes             []clusterNodeInput `json:"nodes"`
	// Start submits the bootstrap job right after creating the cluster.
	Start bool `json:"start"`
}

type clusterView struct {
	store.Cluster
	HasKubeconfig bool         `json:"hasKubeconfig"`
	Nodes         []serverView `json:"nodes"`
	BootstrapJob  *store.Job   `json:"bootstrapJob,omitempty"`
}

func (s *Server) clusterView(ctx context.Context, c *store.Cluster) (*clusterView, error) {
	members, err := s.Store.ListClusterServers(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	v := &clusterView{Cluster: *c, HasKubeconfig: c.KubeconfigSecretID != nil, Nodes: make([]serverView, len(members))}
	for i := range members {
		v.Nodes[i] = view(&members[i])
	}
	if j, err := s.latestBootstrapJob(ctx, c.ID); err == nil && j != nil {
		v.BootstrapJob = j
	} else if err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Server) latestBootstrapJob(ctx context.Context, clusterID string) (*store.Job, error) {
	list, err := s.Store.ListJobs(ctx, store.JobFilter{TargetID: clusterID, Limit: 50})
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Kind == ops.KindBootstrap {
			return &list[i], nil
		}
	}
	return nil, nil
}

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListClusters(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	out := make([]*clusterView, 0, len(list))
	for i := range list {
		v, err := s.clusterView(r.Context(), &list[i])
		if err != nil {
			s.storeError(w, err)
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getCluster(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetCluster(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	v, err := s.clusterView(r.Context(), c)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) createCluster(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in clusterInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	opts, _ := json.Marshal(in.Options)
	c := &store.Cluster{ID: uuid.NewString(), Name: strings.TrimSpace(in.Name), KubernetesVersion: in.KubernetesVersion,
		PodCIDR: in.PodCIDR, ServiceCIDR: in.ServiceCIDR, CNI: in.CNI, Topology: in.Topology, Options: opts, Status: store.ClusterDraft}

	members := make([]store.Server, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		srv, err := s.Store.GetServer(ctx, n.ServerID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "validation", "servidor "+n.ServerID+" não existe")
			return
		} else if err != nil {
			s.storeError(w, err)
			return
		}
		if srv.ClusterID != nil {
			writeError(w, http.StatusConflict, "conflict", fmt.Sprintf("servidor %s já pertence a outro cluster", srv.Name))
			return
		}
		if n.Role != store.RoleControlPlane && n.Role != store.RoleWorker {
			writeError(w, http.StatusBadRequest, "validation", fmt.Sprintf("papel inválido para %s", srv.Name))
			return
		}
		srv.Role = n.Role
		members = append(members, *srv)
	}
	// Control planes first, in the order given: the first one runs kubeadm init.
	ordered := make([]store.Server, 0, len(members))
	for _, role := range []store.NodeRole{store.RoleControlPlane, store.RoleWorker} {
		for _, m := range members {
			if m.Role == role {
				ordered = append(ordered, m)
			}
		}
	}
	if _, err := ops.SpecFor(c, ordered); err != nil {
		writeError(w, http.StatusBadRequest, "validation", strings.TrimPrefix(err.Error(), bootstrap.ErrInvalidSpec.Error()+": "))
		return
	}
	if err := s.Store.CreateCluster(ctx, c); err != nil {
		s.storeError(w, err)
		return
	}
	for i := range ordered {
		ordered[i].ClusterID = &c.ID
		if err := s.Store.UpdateServer(ctx, &ordered[i]); err != nil {
			s.storeError(w, err)
			return
		}
	}
	s.audit(r, "cluster.create", "cluster", c.ID, map[string]any{"name": c.Name, "version": c.KubernetesVersion, "cni": c.CNI, "nodes": len(ordered)})
	if in.Start {
		if _, err := s.startBootstrap(r, c.ID); err != nil {
			s.storeError(w, err)
			return
		}
	}
	v, err := s.clusterView(ctx, c)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// startBootstrap resumes the cluster's last failed/paused bootstrap job (so
// completed steps are skipped) or submits a new one.
func (s *Server) startBootstrap(r *http.Request, clusterID string) (*store.Job, error) {
	ctx := r.Context()
	last, err := s.latestBootstrapJob(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if last != nil {
		switch last.Status {
		case store.JobPending, store.JobRunning:
			return nil, fmt.Errorf("%w: o bootstrap deste cluster já está em execução", store.ErrConflict)
		case store.JobFailed, store.JobPaused, store.JobCancelled:
			if err := s.Engine.Retry(ctx, last.ID); err != nil {
				return nil, err
			}
			s.audit(r, "cluster.bootstrap_retry", "cluster", clusterID, map[string]string{"jobId": last.ID})
			return s.Store.GetJob(ctx, last.ID)
		}
	}
	job, err := s.Engine.Submit(ctx, ops.KindBootstrap, "cluster", clusterID, ops.BootstrapParams{ClusterID: clusterID}, "admin")
	if err != nil {
		return nil, err
	}
	s.audit(r, "cluster.bootstrap", "cluster", clusterID, map[string]string{"jobId": job.ID})
	return job, nil
}

func (s *Server) bootstrapCluster(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Store.GetCluster(r.Context(), id); err != nil {
		s.storeError(w, err)
		return
	}
	job, err := s.startBootstrap(r, id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) deleteCluster(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := s.Store.GetCluster(ctx, r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	if c.Status == store.ClusterReady || c.Status == store.ClusterProvisioning {
		writeError(w, http.StatusConflict, "conflict", "apenas clusters em rascunho ou com falha podem ser removidos do inventário")
		return
	}
	if j, err := s.latestBootstrapJob(ctx, c.ID); err == nil && j != nil && (j.Status == store.JobRunning || j.Status == store.JobPending) {
		writeError(w, http.StatusConflict, "conflict", "cancele o bootstrap em execução antes de remover o cluster")
		return
	}
	if err := s.Store.DeleteCluster(ctx, c.ID); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "cluster.delete", "cluster", c.ID, map[string]string{"name": c.Name})
	w.WriteHeader(http.StatusNoContent)
}

// downloadKubeconfig returns admin.conf. Every download is audited.
func (s *Server) downloadKubeconfig(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetCluster(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	if c.KubeconfigSecretID == nil {
		writeError(w, http.StatusNotFound, "not_found", "o cluster ainda não tem kubeconfig")
		return
	}
	kc, err := s.Secrets.Get(r.Context(), *c.KubeconfigSecretID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	defer func() {
		for i := range kc {
			kc[i] = 0
		}
	}()
	s.audit(r, "cluster.kubeconfig_download", "cluster", c.ID, nil)
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-admin.conf"`, c.Name))
	_, _ = w.Write(kc)
}

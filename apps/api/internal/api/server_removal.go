package api

import (
	"context"
	"net/http"

	"github.com/efbecker/k8sfront/apps/api/internal/kube"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// serverRemoval previews "Remover servidor": inventory only, or a graceful
// cluster removal (cordon, drain respecting PDBs, delete node, kubeadm reset).
type serverRemoval struct {
	InCluster   bool              `json:"inCluster"`
	ClusterID   string            `json:"clusterId,omitempty"`
	ClusterName string            `json:"clusterName,omitempty"`
	Role        string            `json:"role"`
	Blocked     string            `json:"blocked,omitempty"`
	Warnings    []string          `json:"warnings"`
	Node        *kube.NodeRemoval `json:"node,omitempty"`
}

func (s *Server) planServerRemoval(ctx context.Context, srv *store.Server) (*serverRemoval, *store.Cluster, error) {
	out := &serverRemoval{Role: string(srv.Role), Warnings: []string{}}
	if srv.ClusterID == nil {
		return out, nil, nil
	}
	out.InCluster = true
	c, err := s.Store.GetCluster(ctx, *srv.ClusterID)
	if err != nil {
		return nil, nil, err
	}
	out.ClusterID, out.ClusterName = c.ID, c.Name
	if srv.Role != store.RoleWorker {
		out.Blocked = "este servidor é control plane do cluster " + c.Name + ". Remover control planes não é suportado: tirar um membro do etcd pode derrubar o quórum e, se for o único, o cluster inteiro. Remova os workers ou exclua o cluster."
		return out, c, nil
	}
	if c.KubeconfigSecretID == nil {
		out.Blocked = "o cluster " + c.Name + " ainda não terminou o bootstrap; conclua o bootstrap ou exclua o cluster para liberar o servidor."
		return out, c, nil
	}
	cl, err := s.Kube.Get(ctx, c.ID)
	if err != nil {
		out.Warnings = append(out.Warnings, "não foi possível consultar o Kubernetes agora ("+err.Error()+"); o job tenta de novo e, se o nó não existir mais, só faz o kubeadm reset.")
		return out, c, nil
	}
	kctx, cancel := context.WithTimeout(ctx, kube.RequestTimeout)
	defer cancel()
	plan, err := kube.PlanNodeRemoval(kctx, cl.Client, srv.Host)
	if err != nil {
		out.Warnings = append(out.Warnings, "não foi possível listar os pods do nó: "+err.Error())
		return out, c, nil
	}
	if plan == nil {
		out.Warnings = append(out.Warnings, "nenhum nó do Kubernetes tem o IP "+srv.Host+"; o job pula o drain e só faz o kubeadm reset no servidor.")
		return out, c, nil
	}
	out.Node = plan
	moves, blocks, emptyDir := 0, 0, 0
	for _, p := range plan.Pods {
		switch p.Fate {
		case "moves":
			moves++
			if p.EmptyDir {
				emptyDir++
			}
		case "blocks":
			blocks++
		}
	}
	if moves > 0 && plan.OtherSchedulable == 0 {
		out.Warnings = append(out.Warnings, "não há outro nó pronto e agendável: os pods movidos ficarão Pending até existir capacidade.")
	}
	if blocks > 0 {
		out.Warnings = append(out.Warnings, "há pods sem controlador (criados soltos). O drain se recusa a removê-los e o job pausa antes de excluir o nó; apague-os ou recrie-os via Deployment e use Retomar.")
	}
	if emptyDir > 0 {
		out.Warnings = append(out.Warnings, "alguns pods usam emptyDir: esses dados temporários são descartados quando o pod muda de nó.")
	}
	return out, c, nil
}

func (s *Server) serverRemovalPlan(w http.ResponseWriter, r *http.Request) {
	srv, err := s.Store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	plan, _, err := s.planServerRemoval(r.Context(), srv)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// removeServer deletes a free server from the inventory right away (the host
// is not touched) or, for a cluster worker, starts the graceful removal job,
// which deletes it from the inventory at the end.
func (s *Server) removeServer(w http.ResponseWriter, r *http.Request) {
	srv, err := s.Store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	plan, c, err := s.planServerRemoval(r.Context(), srv)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if plan.Blocked != "" {
		writeError(w, http.StatusConflict, "conflict", plan.Blocked)
		return
	}
	if !plan.InCluster {
		if err := s.Store.DeleteServer(r.Context(), srv.ID); err != nil {
			s.storeError(w, err)
			return
		}
		s.audit(r, "server.delete", "server", srv.ID, map[string]string{"server": srv.Name})
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		return
	}
	s.submitMaintenance(w, r, c, ops.KindNodeRemove, ops.NodeParams{ClusterID: c.ID, ServerID: srv.ID, DeleteServer: true},
		map[string]any{"server": srv.Name, "deleteServer": true})
}

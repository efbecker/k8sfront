package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/kube"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/upgrade"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var nodeNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// exclusiveKinds may not run while another of them (or a bootstrap) is
// pending or running on the same cluster.
var exclusiveKinds = []string{ops.KindBootstrap, ops.KindUpgrade, ops.KindNodeAdd, ops.KindNodeRemove, ops.KindOSUpdate, ops.KindRollingReboot, ops.KindCerts, ops.KindRegistriesSync}

func (s *Server) clusterBusy(ctx context.Context, clusterID string) (*store.Job, error) {
	list, err := s.Store.ListJobs(ctx, store.JobFilter{TargetID: clusterID, Status: []store.JobStatus{store.JobPending, store.JobRunning}, Limit: 50})
	if err != nil {
		return nil, err
	}
	for i := range list {
		if slices.Contains(exclusiveKinds, list[i].Kind) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// submitMaintenance submits a lifecycle job on a ready cluster.
func (s *Server) submitMaintenance(w http.ResponseWriter, r *http.Request, c *store.Cluster, kind string, params any, details map[string]any) {
	if slices.Contains(exclusiveKinds, kind) {
		busy, err := s.clusterBusy(r.Context(), c.ID)
		if err != nil {
			s.storeError(w, err)
			return
		}
		if busy != nil {
			writeError(w, http.StatusConflict, "conflict", fmt.Sprintf("%s (%s, job %s)", errBusy.Error(), busy.Kind, busy.ID))
			return
		}
	}
	job, err := s.Engine.Submit(r.Context(), kind, "cluster", c.ID, params, actor(r))
	if err != nil {
		s.storeError(w, err)
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["jobId"] = job.ID
	s.audit(r, kind, "cluster", c.ID, details)
	writeJSON(w, http.StatusAccepted, job)
}

type upgradePlan struct {
	ClusterVersion string                `json:"clusterVersion"`
	ServerVersion  string                `json:"serverVersion"`
	Nodes          []upgrade.NodeVersion `json:"nodes"`
	Checks         []upgrade.Check       `json:"checks"`
	Targets        []upgrade.Target      `json:"targets"`
	Blocked        bool                  `json:"blocked"`
	PausedJob      *store.Job            `json:"pausedJob,omitempty"`
	// InProgress: an upgrade job is queued or running (blocks a new one).
	InProgress bool `json:"inProgress,omitempty"`
}

func (s *Server) buildPlan(ctx context.Context, w http.ResponseWriter, r *http.Request) (*upgradePlan, bool) {
	c, ok := s.readyCluster(w, r)
	if !ok {
		return nil, false
	}
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return nil, false
	}
	kctx, cancel := context.WithTimeout(ctx, kube.RequestTimeout)
	defer cancel()
	st, err := upgrade.Inspect(kctx, cl.Client)
	if err != nil {
		s.kubeError(w, err)
		return nil, false
	}
	plan := &upgradePlan{ClusterVersion: c.KubernetesVersion, ServerVersion: st.ServerVersion, Nodes: st.Nodes, Checks: st.Checks, Targets: []upgrade.Target{}}
	cur, err := upgrade.ParseLoose(st.ServerVersion)
	if err != nil {
		plan.Checks = append(plan.Checks, upgrade.Check{ID: "version", Name: "Versão da API", Status: "fail", Detail: err.Error()})
	} else if s.Versions == nil {
		plan.Checks = append(plan.Checks, upgrade.Check{ID: "releases", Name: "Releases do Kubernetes", Status: "warn", Detail: "consulta de versões desativada"})
	} else if _, err := s.Versions.Stable(kctx); err != nil {
		plan.Checks = append(plan.Checks, upgrade.Check{ID: "releases", Name: "Releases do Kubernetes", Status: "warn", Detail: "não foi possível consultar dl.k8s.io: " + err.Error()})
	} else if plan.Targets, err = upgrade.Targets(kctx, cur, s.Versions.LatestPatch); err != nil {
		plan.Checks = append(plan.Checks, upgrade.Check{ID: "releases", Name: "Releases do Kubernetes", Status: "warn", Detail: err.Error()})
	}
	list, err := s.Store.ListJobs(ctx, store.JobFilter{TargetID: c.ID, Status: []store.JobStatus{store.JobPaused, store.JobFailed, store.JobPending, store.JobRunning}, Limit: 20})
	if err != nil {
		s.storeError(w, err)
		return nil, false
	}
	for i := range list {
		if list[i].Kind == ops.KindUpgrade {
			plan.PausedJob = &list[i]
			if list[i].Status == store.JobPaused || list[i].Status == store.JobFailed {
				plan.Checks = append(plan.Checks, upgrade.Check{ID: "pending-upgrade", Name: "Upgrade anterior", Status: "fail",
					Detail: "Há um upgrade interrompido: retome-o (as etapas concluídas são puladas) ou cancele-o antes de planejar outro"})
			} else {
				// Not a failed pre-check: the UI shows it as "in progress".
				plan.InProgress = true
				plan.Blocked = true
			}
			break
		}
	}
	for _, ch := range plan.Checks {
		if ch.Status == "fail" {
			plan.Blocked = true
		}
	}
	return plan, true
}

func (s *Server) upgradePlan(w http.ResponseWriter, r *http.Request) {
	plan, ok := s.buildPlan(r.Context(), w, r)
	if ok {
		writeJSON(w, http.StatusOK, plan)
	}
}

func (s *Server) startUpgrade(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TargetVersion string `json:"targetVersion"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	plan, ok := s.buildPlan(r.Context(), w, r)
	if !ok {
		return
	}
	if plan.Blocked {
		writeError(w, http.StatusConflict, "conflict", "as pré-checagens do upgrade falharam; veja o plano")
		return
	}
	target, err := upgrade.Resolve(plan.Targets, in.TargetVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	cur, _ := upgrade.ParseLoose(plan.ServerVersion)
	if _, err := ops.ValidateHops(cur.String(), target.Hops); err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	c, _ := s.readyCluster(w, r)
	s.submitMaintenance(w, r, c, ops.KindUpgrade, ops.UpgradeParams{ClusterID: c.ID, From: cur.String(), Hops: target.Hops},
		map[string]any{"from": cur.String(), "to": target.Version, "hops": target.Hops})
}

// addNode attaches a free server as a worker (default) or control plane and joins it.
func (s *Server) addNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServerID string         `json:"serverId"`
		Role     store.NodeRole `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	switch in.Role {
	case "":
		in.Role = store.RoleWorker
	case store.RoleWorker, store.RoleControlPlane:
	default:
		writeError(w, http.StatusBadRequest, "validation", "papel inválido (control-plane ou worker)")
		return
	}
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	srv, err := s.Store.GetServer(r.Context(), in.ServerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "validation", "servidor não existe")
			return
		}
		s.storeError(w, err)
		return
	}
	if srv.ClusterID != nil && *srv.ClusterID != c.ID {
		writeError(w, http.StatusConflict, "conflict", fmt.Sprintf("servidor %s já pertence a outro cluster", srv.Name))
		return
	}
	if busy, err := s.clusterBusy(r.Context(), c.ID); err != nil || busy != nil {
		if err != nil {
			s.storeError(w, err)
		} else {
			writeError(w, http.StatusConflict, "conflict", errBusy.Error())
		}
		return
	}
	members, err := s.Store.ListClusterServers(r.Context(), c.ID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	srv.ClusterID, srv.Role = &c.ID, in.Role
	spec, err := ops.SpecFor(c, append(members, *srv))
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	if in.Role == store.RoleControlPlane {
		if spec.Topology == bootstrap.TopologySingle {
			writeError(w, http.StatusBadRequest, "validation", "clusters single-node não recebem outros nós")
			return
		}
		if spec.Mode() == bootstrap.EndpointDirect {
			writeError(w, http.StatusBadRequest, "validation", "este cluster usa o IP do primeiro control plane como endpoint da API; adicionar control planes exige o proxy local ou um VIP (recrie o cluster)")
			return
		}
	}
	if err := s.Store.UpdateServer(r.Context(), srv); err != nil {
		s.storeError(w, err)
		return
	}
	s.submitMaintenance(w, r, c, ops.KindNodeAdd, ops.NodeParams{ClusterID: c.ID, ServerID: srv.ID}, map[string]any{"server": srv.Name, "role": in.Role})
}

func (s *Server) removeNode(w http.ResponseWriter, r *http.Request) {
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	srv, err := s.Store.GetServer(r.Context(), r.PathValue("serverId"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	if srv.ClusterID == nil || *srv.ClusterID != c.ID {
		writeError(w, http.StatusBadRequest, "validation", "o servidor não pertence a este cluster")
		return
	}
	if srv.Role == store.RoleControlPlane {
		left, err := s.controlPlanesLeft(r.Context(), c.ID, srv.ID)
		if err != nil {
			s.storeError(w, err)
			return
		}
		if left == 0 {
			writeError(w, http.StatusBadRequest, "validation", "não é possível remover o único control plane do cluster")
			return
		}
	} else if srv.Role != store.RoleWorker {
		writeError(w, http.StatusBadRequest, "validation", "o servidor não tem papel no cluster")
		return
	}
	s.submitMaintenance(w, r, c, ops.KindNodeRemove, ops.NodeParams{ClusterID: c.ID, ServerID: srv.ID}, map[string]any{"server": srv.Name, "role": srv.Role})
}

func nodeParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if len(name) > 253 || !nodeNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "validation", "nome de nó inválido")
		return "", false
	}
	return name, true
}

func (s *Server) setSchedulable(w http.ResponseWriter, r *http.Request, unschedulable bool) {
	name, ok := nodeParam(w, r)
	if !ok {
		return
	}
	cl, ok := s.kubeCluster(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kube.RequestTimeout)
	defer cancel()
	patch := fmt.Appendf(nil, `{"spec":{"unschedulable":%t}}`, unschedulable)
	if _, err := cl.Client.CoreV1().Nodes().Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{FieldManager: "k8sfront"}); err != nil {
		s.kubeError(w, err)
		return
	}
	action := "node.uncordon"
	if unschedulable {
		action = "node.cordon"
	}
	s.audit(r, action, "cluster", r.PathValue("id"), map[string]string{"node": name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cordonNode(w http.ResponseWriter, r *http.Request)   { s.setSchedulable(w, r, true) }
func (s *Server) uncordonNode(w http.ResponseWriter, r *http.Request) { s.setSchedulable(w, r, false) }

func (s *Server) drainNode(w http.ResponseWriter, r *http.Request) {
	name, ok := nodeParam(w, r)
	if !ok {
		return
	}
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	s.submitMaintenance(w, r, c, ops.KindNodeDrain, ops.DrainParams{ClusterID: c.ID, Node: name}, map[string]any{"node": name})
}

func (s *Server) osCheck(w http.ResponseWriter, r *http.Request) {
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	s.submitMaintenance(w, r, c, ops.KindOSCheck, ops.ClusterParams{ClusterID: c.ID}, nil)
}

func (s *Server) memberIDs(w http.ResponseWriter, r *http.Request, c *store.Cluster, ids []string) bool {
	if len(ids) == 0 {
		return true
	}
	members, err := s.Store.ListClusterServers(r.Context(), c.ID)
	if err != nil {
		s.storeError(w, err)
		return false
	}
	for _, id := range ids {
		if !slices.ContainsFunc(members, func(m store.Server) bool { return m.ID == id }) {
			writeError(w, http.StatusBadRequest, "validation", "servidor "+id+" não pertence ao cluster")
			return false
		}
	}
	return true
}

func (s *Server) osUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServerIDs []string `json:"serverIds"`
		Reboot    string   `json:"reboot"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if in.Reboot == "" {
		in.Reboot = ops.RebootIfRequired
	}
	if !slices.Contains([]string{ops.RebootNever, ops.RebootIfRequired, ops.RebootAlways}, in.Reboot) {
		writeError(w, http.StatusBadRequest, "validation", "reboot deve ser never, if-required ou always")
		return
	}
	c, ok := s.readyCluster(w, r)
	if !ok || !s.memberIDs(w, r, c, in.ServerIDs) {
		return
	}
	s.submitMaintenance(w, r, c, ops.KindOSUpdate, ops.OSUpdateParams{ClusterID: c.ID, ServerIDs: in.ServerIDs, Reboot: in.Reboot},
		map[string]any{"servers": len(in.ServerIDs), "reboot": in.Reboot})
}

func (s *Server) rollingReboot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServerIDs []string `json:"serverIds"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	c, ok := s.readyCluster(w, r)
	if !ok || !s.memberIDs(w, r, c, in.ServerIDs) {
		return
	}
	s.submitMaintenance(w, r, c, ops.KindRollingReboot, ops.RollingRebootParams{ClusterID: c.ID, ServerIDs: in.ServerIDs},
		map[string]any{"servers": len(in.ServerIDs)})
}

func (s *Server) certsJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	if in.Action != "check" && in.Action != "renew" {
		writeError(w, http.StatusBadRequest, "validation", "action deve ser check ou renew")
		return
	}
	c, ok := s.readyCluster(w, r)
	if !ok {
		return
	}
	kind := ops.KindCerts
	if in.Action == "check" {
		// Read-only: does not need to wait for other maintenance.
		job, err := s.Engine.Submit(r.Context(), kind, "cluster", c.ID, ops.CertsParams{ClusterID: c.ID, Action: in.Action}, actor(r))
		if err != nil {
			s.storeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
		return
	}
	s.submitMaintenance(w, r, c, kind, ops.CertsParams{ClusterID: c.ID, Action: in.Action}, map[string]any{"action": in.Action})
}

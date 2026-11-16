package upgrade

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Check is one pre-upgrade verification.
type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail
	Detail string `json:"detail"`
}

// NodeVersion is a node's role and versions.
type NodeVersion struct {
	Name           string `json:"name"`
	ControlPlane   bool   `json:"controlPlane"`
	Ready          bool   `json:"ready"`
	Unschedulable  bool   `json:"unschedulable"`
	KubeletVersion string `json:"kubeletVersion"`
	InternalIP     string `json:"internalIp"`
	OSImage        string `json:"osImage"`
	KernelVersion  string `json:"kernelVersion"`
	Runtime        string `json:"containerRuntime"`
}

// State is what the API reports about the cluster before an upgrade.
type State struct {
	ServerVersion string        `json:"serverVersion"`
	Nodes         []NodeVersion `json:"nodes"`
	Checks        []Check       `json:"checks"`
}

// Blocked reports whether any check failed.
func (s *State) Blocked() bool {
	for _, c := range s.Checks {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

// Inspect reads versions, node health and PodDisruptionBudgets.
func Inspect(ctx context.Context, cs kubernetes.Interface) (*State, error) {
	sv, err := cs.Discovery().ServerVersion()
	if err != nil {
		return nil, err
	}
	st := &State{ServerVersion: sv.GitVersion}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var notReady, cordoned []string
	for _, n := range nodes.Items {
		nv := NodeVersion{Name: n.Name, Unschedulable: n.Spec.Unschedulable, KubeletVersion: n.Status.NodeInfo.KubeletVersion,
			OSImage: n.Status.NodeInfo.OSImage, KernelVersion: n.Status.NodeInfo.KernelVersion, Runtime: n.Status.NodeInfo.ContainerRuntimeVersion}
		_, nv.ControlPlane = n.Labels["node-role.kubernetes.io/control-plane"]
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady {
				nv.Ready = c.Status == corev1.ConditionTrue
			}
		}
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				nv.InternalIP = a.Address
			}
		}
		if !nv.Ready {
			notReady = append(notReady, n.Name)
		}
		if nv.Unschedulable {
			cordoned = append(cordoned, n.Name)
		}
		st.Nodes = append(st.Nodes, nv)
	}
	sort.Slice(st.Nodes, func(i, j int) bool {
		if st.Nodes[i].ControlPlane != st.Nodes[j].ControlPlane {
			return st.Nodes[i].ControlPlane
		}
		return st.Nodes[i].Name < st.Nodes[j].Name
	})

	if len(notReady) > 0 {
		st.Checks = append(st.Checks, Check{"nodes-ready", "Todos os nós Ready", "fail", "Fora do ar: " + strings.Join(notReady, ", ")})
	} else {
		st.Checks = append(st.Checks, Check{"nodes-ready", "Todos os nós Ready", "pass", fmt.Sprintf("%d nó(s) Ready", len(nodes.Items))})
	}
	if len(cordoned) > 0 {
		st.Checks = append(st.Checks, Check{"cordoned", "Nós com agendamento bloqueado", "warn",
			strings.Join(cordoned, ", ") + " (o upgrade faz uncordon ao final de cada nó)"})
	}

	var versions []string
	for _, n := range st.Nodes {
		versions = append(versions, n.KubeletVersion)
	}
	if oldest, ok := MinVersion(versions); ok {
		if api, err := parseLoose(sv.GitVersion); err == nil && api.Minor-oldest.Minor >= 1 {
			st.Checks = append(st.Checks, Check{"skew", "Version skew do kubelet", "warn",
				fmt.Sprintf("kubelet mais antigo em %s com API em %s: o upgrade alinha todos os nós", oldest, api)})
		} else {
			st.Checks = append(st.Checks, Check{"skew", "Version skew do kubelet", "pass", "kubelets alinhados com a API"})
		}
	}

	pdbs, err := cs.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var blocking []string
	for _, p := range pdbs.Items {
		if p.Status.ExpectedPods > 0 && p.Status.DisruptionsAllowed == 0 {
			blocking = append(blocking, p.Namespace+"/"+p.Name)
		}
	}
	if len(blocking) > 0 {
		st.Checks = append(st.Checks, Check{"pdb", "PodDisruptionBudgets", "warn",
			"Sem disrupção permitida agora (o drain vai esperar e pode pausar o job): " + strings.Join(blocking, ", ")})
	} else {
		st.Checks = append(st.Checks, Check{"pdb", "PodDisruptionBudgets", "pass", fmt.Sprintf("%d PDB(s), nenhum bloqueando drain", len(pdbs.Items))})
	}
	return st, nil
}

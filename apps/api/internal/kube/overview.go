package kube

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
)

// NodeUsage is a node with its scheduled load and live usage.
type NodeUsage struct {
	Node
	Pods                 int    `json:"pods"`
	CPURequestedMillis   int64  `json:"cpuRequestedMillis"`
	MemoryRequestedBytes int64  `json:"memoryRequestedBytes"`
	CPUUsedMillis        *int64 `json:"cpuUsedMillis"`   // nil without metrics-server
	MemoryUsedBytes      *int64 `json:"memoryUsedBytes"` // nil without metrics-server
}

// Totals aggregates allocatable/requested/used for one resource.
type Totals struct {
	Allocatable int64  `json:"allocatable"`
	Requested   int64  `json:"requested"`
	Used        *int64 `json:"used"`
}

// PodCounts breaks pods down by phase plus a "problem" bucket.
type PodCounts struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Pending   int `json:"pending"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Problem   int `json:"problem"` // CrashLoopBackOff, ImagePull errors, not ready while running...
}

// Component is a control-plane static pod (etcd, apiserver, ...).
type Component struct {
	Name     string `json:"name"`
	Node     string `json:"node"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
}

// Event is a slim Kubernetes event.
type Event struct {
	Type      string    `json:"type"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Count     int32     `json:"count"`
	Last      time.Time `json:"last"`
}

// Overview is the cluster dashboard payload.
type Overview struct {
	Version          string      `json:"version"`
	Platform         string      `json:"platform"`
	Nodes            []NodeUsage `json:"nodes"`
	NodesReady       int         `json:"nodesReady"`
	Pods             PodCounts   `json:"pods"`
	Namespaces       int         `json:"namespaces"`
	Services         int         `json:"services"`
	Deployments      int         `json:"deployments"`
	DeploymentsReady int         `json:"deploymentsReady"`
	CPU              Totals      `json:"cpu"`    // millicores
	Memory           Totals      `json:"memory"` // bytes
	MetricsAvailable bool        `json:"metricsAvailable"`
	ControlPlane     []Component `json:"controlPlane"`
	System           []Workload  `json:"system"`
	Events           []Event     `json:"events"`
	CollectedAt      time.Time   `json:"collectedAt"`
}

// systemNamespaces hold the add-ons installed by the bootstrap.
var systemNamespaces = []string{"kube-system", "calico-system", "tigera-operator"}

// IsSystemNamespace reports whether ns hosts platform components.
func IsSystemNamespace(ns string) bool {
	for _, s := range systemNamespaces {
		if s == ns {
			return true
		}
	}
	return false
}

// GetOverview collects the dashboard. Missing optional pieces (metrics,
// events) degrade gracefully instead of failing the whole call.
func GetOverview(ctx context.Context, cs kubernetes.Interface) (*Overview, error) {
	ov := &Overview{CollectedAt: time.Now().UTC(), Nodes: []NodeUsage{}, ControlPlane: []Component{}, System: []Workload{}, Events: []Event{}}
	if v, err := discovery.ToServerVersionInterfaceWithContext(cs.Discovery()).ServerVersionWithContext(ctx); err == nil {
		ov.Version, ov.Platform = v.GitVersion, v.Platform
	} else {
		return nil, err
	}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i := range nodes.Items {
		nv := NodeUsage{Node: nodeView(&nodes.Items[i])}
		if nv.Ready {
			ov.NodesReady++
		}
		ov.CPU.Allocatable += nv.CPUMillis
		ov.Memory.Allocatable += nv.MemoryBytes
		idx[nv.Name] = len(ov.Nodes)
		ov.Nodes = append(ov.Nodes, nv)
	}

	for i := range pods.Items {
		p := &pods.Items[i]
		pv := podView(p)
		ov.Pods.Total++
		switch p.Status.Phase {
		case corev1.PodRunning:
			ov.Pods.Running++
		case corev1.PodPending:
			ov.Pods.Pending++
		case corev1.PodSucceeded:
			ov.Pods.Succeeded++
		case corev1.PodFailed:
			ov.Pods.Failed++
		}
		if PodHasProblem(&pv) {
			ov.Pods.Problem++
		}
		terminal := p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
		if j, ok := idx[p.Spec.NodeName]; ok && !terminal {
			n := &ov.Nodes[j]
			n.Pods++
			cpu, mem := podRequests(p)
			n.CPURequestedMillis += cpu
			n.MemoryRequestedBytes += mem
			ov.CPU.Requested += cpu
			ov.Memory.Requested += mem
		}
		if p.Namespace == "kube-system" && p.Labels["tier"] == "control-plane" {
			comp := p.Labels["component"]
			if comp == "" {
				comp = p.Name
			}
			ov.ControlPlane = append(ov.ControlPlane, Component{Name: comp, Node: p.Spec.NodeName,
				Ready: pv.Containers > 0 && pv.Ready == pv.Containers && p.Status.Phase == corev1.PodRunning, Restarts: pv.Restarts})
		}
	}
	sort.Slice(ov.ControlPlane, func(i, j int) bool {
		if ov.ControlPlane[i].Name != ov.ControlPlane[j].Name {
			return ov.ControlPlane[i].Name < ov.ControlPlane[j].Name
		}
		return ov.ControlPlane[i].Node < ov.ControlPlane[j].Node
	})

	if usage, err := nodeMetrics(ctx, cs); err == nil {
		ov.MetricsAvailable = true
		var cpu, mem int64
		for name, u := range usage {
			if j, ok := idx[name]; ok {
				c, m := u[0], u[1]
				ov.Nodes[j].CPUUsedMillis, ov.Nodes[j].MemoryUsedBytes = &c, &m
				cpu += c
				mem += m
			}
		}
		ov.CPU.Used, ov.Memory.Used = &cpu, &mem
	}

	if ns, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{}); err == nil {
		ov.Namespaces = len(ns.Items)
	}
	if svcs, err := cs.CoreV1().Services("").List(ctx, metav1.ListOptions{}); err == nil {
		ov.Services = len(svcs.Items)
	}
	if deps, err := cs.AppsV1().Deployments("").List(ctx, metav1.ListOptions{}); err == nil {
		for i := range deps.Items {
			d := deploymentView(&deps.Items[i])
			ov.Deployments++
			if d.Ready >= d.Desired {
				ov.DeploymentsReady++
			}
			if IsSystemNamespace(d.Namespace) {
				ov.System = append(ov.System, d)
			}
		}
	}
	if dss, err := cs.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{}); err == nil {
		for i := range dss.Items {
			if IsSystemNamespace(dss.Items[i].Namespace) {
				ov.System = append(ov.System, daemonSetView(&dss.Items[i]))
			}
		}
	}
	sort.Slice(ov.System, func(i, j int) bool { return ov.System[i].Name < ov.System[j].Name })

	if evs, err := cs.CoreV1().Events("").List(ctx, metav1.ListOptions{FieldSelector: "type=Warning", Limit: 500}); err == nil {
		ov.Events = slimEvents(evs.Items, 25)
	}
	return ov, nil
}

// PodHasProblem flags pods an operator should look at.
func PodHasProblem(p *Pod) bool {
	if p.Phase == string(corev1.PodFailed) {
		return true
	}
	if p.Reason != "" && p.Reason != "Terminating" && p.Reason != "Completed" {
		return true
	}
	return p.Phase == string(corev1.PodRunning) && p.Ready < p.Containers
}

func podRequests(p *corev1.Pod) (cpu, mem int64) {
	for _, c := range p.Spec.Containers {
		cpu += c.Resources.Requests.Cpu().MilliValue()
		mem += c.Resources.Requests.Memory().Value()
	}
	// Init containers run sequentially: the effective request is the max.
	for _, c := range p.Spec.InitContainers {
		cpu = max(cpu, c.Resources.Requests.Cpu().MilliValue())
		mem = max(mem, c.Resources.Requests.Memory().Value())
	}
	return cpu, mem
}

// nodeMetrics reads metrics.k8s.io without pulling the metrics client module.
func nodeMetrics(ctx context.Context, cs kubernetes.Interface) (map[string][2]int64, error) {
	rc := cs.Discovery().RESTClient()
	if rc == nil {
		return nil, errors.New("discovery client unavailable")
	}
	raw, err := rc.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Usage map[string]string `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make(map[string][2]int64, len(list.Items))
	for _, it := range list.Items {
		cpu, err1 := resource.ParseQuantity(it.Usage["cpu"])
		mem, err2 := resource.ParseQuantity(it.Usage["memory"])
		if err1 != nil || err2 != nil {
			continue
		}
		out[it.Metadata.Name] = [2]int64{cpu.MilliValue(), mem.Value()}
	}
	return out, nil
}

func slimEvents(items []corev1.Event, limit int) []Event {
	out := make([]Event, 0, len(items))
	for _, e := range items {
		last := e.LastTimestamp.Time
		if last.IsZero() {
			last = e.EventTime.Time
		}
		if last.IsZero() {
			last = e.CreationTimestamp.Time
		}
		count := e.Count
		if e.Series != nil && e.Series.Count > count {
			count = e.Series.Count
		}
		out = append(out, Event{Type: e.Type, Reason: e.Reason, Message: strings.TrimSpace(e.Message), Kind: e.InvolvedObject.Kind,
			Name: e.InvolvedObject.Name, Namespace: e.Namespace, Count: max(count, 1), Last: last})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

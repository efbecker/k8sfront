package kube

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
)

// RemovalPod is a pod on a node about to be removed and what drain does to it.
type RemovalPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	OwnerKind string `json:"ownerKind,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	// Fate: moves (recreated elsewhere by its controller) | daemon (DaemonSet
	// or static pod, goes away with the node) | blocks (no controller: drain
	// refuses to evict it) | done (already finished).
	Fate     string `json:"fate"`
	EmptyDir bool   `json:"emptyDir,omitempty"`
}

// NodeRemoval previews a graceful node removal.
type NodeRemoval struct {
	NodeName string       `json:"nodeName"`
	Pods     []RemovalPod `json:"pods"`
	// OtherSchedulable counts Ready, schedulable nodes left to receive pods.
	OtherSchedulable int `json:"otherSchedulable"`
}

// PlanNodeRemoval finds the node by InternalIP (kubeadm pins node-ip to the
// registered address) and classifies its pods. nil means the host is not a
// node of the cluster.
func PlanNodeRemoval(ctx context.Context, cs kubernetes.Interface, host string) (*NodeRemoval, error) {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out *NodeRemoval
	others := 0
	for i := range nodes.Items {
		v := nodeView(&nodes.Items[i])
		if v.InternalIP == host && out == nil {
			out = &NodeRemoval{NodeName: v.Name, Pods: []RemovalPod{}}
			continue
		}
		if v.Ready && !v.Unschedulable {
			others++
		}
	}
	if out == nil {
		return nil, nil
	}
	out.OtherSchedulable = others
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("spec.nodeName", out.NodeName).String()})
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName != out.NodeName {
			continue
		}
		v := podView(p)
		rp := RemovalPod{Namespace: p.Namespace, Name: p.Name, OwnerKind: v.OwnerKind, OwnerName: v.OwnerName, Fate: "moves"}
		switch {
		case p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed:
			rp.Fate = "done"
		case v.OwnerKind == "DaemonSet" || p.Annotations["kubernetes.io/config.mirror"] != "" || v.OwnerKind == "Node":
			rp.Fate = "daemon"
		case len(p.OwnerReferences) == 0:
			rp.Fate = "blocks"
		}
		for _, vol := range p.Spec.Volumes {
			if vol.EmptyDir != nil {
				rp.EmptyDir = true
			}
		}
		out.Pods = append(out.Pods, rp)
	}
	rank := map[string]int{"blocks": 0, "moves": 1, "daemon": 2, "done": 3}
	sort.Slice(out.Pods, func(i, j int) bool {
		a, b := out.Pods[i], out.Pods[j]
		if rank[a.Fate] != rank[b.Fate] {
			return rank[a.Fate] < rank[b.Fate]
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out, nil
}

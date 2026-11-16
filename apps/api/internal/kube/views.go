package kube

import (
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// Node is the slim node view used by the overview and the topology.
type Node struct {
	UID              string            `json:"uid"`
	Name             string            `json:"name"`
	Roles            []string          `json:"roles"`
	Ready            bool              `json:"ready"`
	Unschedulable    bool              `json:"unschedulable"`
	Conditions       []string          `json:"conditions,omitempty"` // abnormal conditions (MemoryPressure, ...)
	KubeletVersion   string            `json:"kubeletVersion"`
	InternalIP       string            `json:"internalIP"`
	OSImage          string            `json:"osImage"`
	KernelVersion    string            `json:"kernelVersion"`
	ContainerRuntime string            `json:"containerRuntime"`
	Architecture     string            `json:"architecture"`
	PodCIDR          string            `json:"podCIDR,omitempty"`
	CPUMillis        int64             `json:"cpuMillis"`   // allocatable
	MemoryBytes      int64             `json:"memoryBytes"` // allocatable
	PodsCapacity     int64             `json:"podsCapacity"`
	Taints           []string          `json:"taints,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	Created          time.Time         `json:"created"`
}

// Pod is the slim pod view. It deliberately leaves out env, volumes and
// annotations.
type Pod struct {
	UID        string            `json:"uid"`
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Node       string            `json:"node"`
	Phase      string            `json:"phase"`
	Reason     string            `json:"reason,omitempty"` // CrashLoopBackOff, ImagePullBackOff, Terminating...
	IP         string            `json:"ip,omitempty"`
	Ready      int               `json:"ready"`
	Containers int               `json:"containers"`
	Restarts   int32             `json:"restarts"`
	Images     []string          `json:"images"`
	OwnerKind  string            `json:"ownerKind,omitempty"`
	OwnerName  string            `json:"ownerName,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	QOSClass   string            `json:"qosClass,omitempty"`
	Created    time.Time         `json:"created"`
}

// Service is the slim service view.
type Service struct {
	UID        string            `json:"uid"`
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Type       string            `json:"type"`
	ClusterIP  string            `json:"clusterIP"`
	ExternalIP []string          `json:"externalIPs,omitempty"`
	Ports      []string          `json:"ports"`
	Selector   map[string]string `json:"selector,omitempty"`
	Created    time.Time         `json:"created"`
}

func nodeView(n *corev1.Node) Node {
	v := Node{
		UID: string(n.UID), Name: n.Name, Unschedulable: n.Spec.Unschedulable, PodCIDR: n.Spec.PodCIDR,
		KubeletVersion: n.Status.NodeInfo.KubeletVersion, OSImage: n.Status.NodeInfo.OSImage,
		KernelVersion: n.Status.NodeInfo.KernelVersion, ContainerRuntime: n.Status.NodeInfo.ContainerRuntimeVersion,
		Architecture: n.Status.NodeInfo.Architecture, Labels: n.Labels, Created: n.CreationTimestamp.Time,
	}
	for k := range n.Labels {
		if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && r != "" {
			v.Roles = append(v.Roles, r)
		}
	}
	sort.Strings(v.Roles)
	for _, c := range n.Status.Conditions {
		switch {
		case c.Type == corev1.NodeReady:
			v.Ready = c.Status == corev1.ConditionTrue
		case c.Status == corev1.ConditionTrue:
			v.Conditions = append(v.Conditions, string(c.Type))
		}
	}
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP && v.InternalIP == "" {
			v.InternalIP = a.Address
		}
	}
	v.CPUMillis = n.Status.Allocatable.Cpu().MilliValue()
	v.MemoryBytes = n.Status.Allocatable.Memory().Value()
	v.PodsCapacity = n.Status.Allocatable.Pods().Value()
	for _, t := range n.Spec.Taints {
		s := t.Key
		if t.Value != "" {
			s += "=" + t.Value
		}
		v.Taints = append(v.Taints, s+":"+string(t.Effect))
	}
	return v
}

func podView(p *corev1.Pod) Pod {
	v := Pod{
		UID: string(p.UID), Name: p.Name, Namespace: p.Namespace, Node: p.Spec.NodeName, Phase: string(p.Status.Phase),
		Reason: p.Status.Reason, IP: p.Status.PodIP, Containers: len(p.Spec.Containers), Labels: p.Labels,
		QOSClass: string(p.Status.QOSClass), Created: p.CreationTimestamp.Time,
	}
	for _, c := range p.Spec.Containers {
		v.Images = append(v.Images, c.Image)
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			v.Ready++
		}
		v.Restarts += cs.RestartCount
		if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "ContainerCreating" {
			v.Reason = w.Reason
		} else if t := cs.State.Terminated; t != nil && t.Reason != "" && t.Reason != "Completed" && v.Reason == "" {
			v.Reason = t.Reason
		}
	}
	for _, cs := range p.Status.InitContainerStatuses {
		if w := cs.State.Waiting; w != nil && (w.Reason == "CrashLoopBackOff" || strings.Contains(w.Reason, "Err")) {
			v.Reason = "Init:" + w.Reason
		}
	}
	if p.DeletionTimestamp != nil {
		v.Reason = "Terminating"
	}
	if len(p.OwnerReferences) > 0 {
		o := p.OwnerReferences[0]
		v.OwnerKind, v.OwnerName = o.Kind, o.Name
		// Collapse ReplicaSet → Deployment using the pod-template-hash suffix.
		if o.Kind == "ReplicaSet" {
			if h := p.Labels["pod-template-hash"]; h != "" && strings.HasSuffix(o.Name, "-"+h) {
				v.OwnerKind, v.OwnerName = "Deployment", strings.TrimSuffix(o.Name, "-"+h)
			}
		}
	}
	return v
}

func serviceView(s *corev1.Service) Service {
	v := Service{
		UID: string(s.UID), Name: s.Name, Namespace: s.Namespace, Type: string(s.Spec.Type), ClusterIP: s.Spec.ClusterIP,
		ExternalIP: s.Spec.ExternalIPs, Selector: s.Spec.Selector, Created: s.CreationTimestamp.Time, Ports: []string{},
	}
	for _, ing := range s.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			v.ExternalIP = append(v.ExternalIP, ing.IP)
		} else if ing.Hostname != "" {
			v.ExternalIP = append(v.ExternalIP, ing.Hostname)
		}
	}
	for _, p := range s.Spec.Ports {
		str := portString(p.Port, string(p.Protocol))
		if p.NodePort != 0 {
			str = portString(p.Port, "") + ":" + portString(p.NodePort, string(p.Protocol))
		}
		v.Ports = append(v.Ports, str)
	}
	return v
}

func portString(p int32, proto string) string {
	s := strconv.Itoa(int(p))
	if proto != "" {
		s += "/" + proto
	}
	return s
}

// Workload is a Deployment, StatefulSet or DaemonSet.
type Workload struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Desired   int32     `json:"desired"`
	Ready     int32     `json:"ready"`
	Updated   int32     `json:"updated"`
	Available int32     `json:"available"`
	Images    []string  `json:"images"`
	Created   time.Time `json:"created"`
}

func images(spec corev1.PodSpec) []string {
	out := make([]string, 0, len(spec.Containers))
	for _, c := range spec.Containers {
		out = append(out, c.Image)
	}
	return out
}

func deploymentView(d *appsv1.Deployment) Workload {
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	return Workload{Kind: "Deployment", Name: d.Name, Namespace: d.Namespace, Desired: desired, Ready: d.Status.ReadyReplicas,
		Updated: d.Status.UpdatedReplicas, Available: d.Status.AvailableReplicas, Images: images(d.Spec.Template.Spec), Created: d.CreationTimestamp.Time}
}

func statefulSetView(s *appsv1.StatefulSet) Workload {
	desired := int32(1)
	if s.Spec.Replicas != nil {
		desired = *s.Spec.Replicas
	}
	return Workload{Kind: "StatefulSet", Name: s.Name, Namespace: s.Namespace, Desired: desired, Ready: s.Status.ReadyReplicas,
		Updated: s.Status.UpdatedReplicas, Available: s.Status.AvailableReplicas, Images: images(s.Spec.Template.Spec), Created: s.CreationTimestamp.Time}
}

func daemonSetView(d *appsv1.DaemonSet) Workload {
	return Workload{Kind: "DaemonSet", Name: d.Name, Namespace: d.Namespace, Desired: d.Status.DesiredNumberScheduled, Ready: d.Status.NumberReady,
		Updated: d.Status.UpdatedNumberScheduled, Available: d.Status.NumberAvailable, Images: images(d.Spec.Template.Spec), Created: d.CreationTimestamp.Time}
}

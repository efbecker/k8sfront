package kube

import (
	"context"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Namespaces lists namespace names.
func Namespaces(ctx context.Context, cs kubernetes.Interface) ([]string, error) {
	list, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, n := range list.Items {
		out = append(out, n.Name)
	}
	sort.Strings(out)
	return out, nil
}

// Workloads is the payload of the Workloads page.
type Workloads struct {
	Workloads []Workload `json:"workloads"`
	Pods      []Pod      `json:"pods"`
}

// ListWorkloads returns Deployments, StatefulSets, DaemonSets and pods of ns
// ("" = all namespaces).
func ListWorkloads(ctx context.Context, cs kubernetes.Interface, ns string) (*Workloads, error) {
	out := &Workloads{Workloads: []Workload{}, Pods: []Pod{}}
	deps, err := cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range deps.Items {
		out.Workloads = append(out.Workloads, deploymentView(&deps.Items[i]))
	}
	sts, err := cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range sts.Items {
		out.Workloads = append(out.Workloads, statefulSetView(&sts.Items[i]))
	}
	dss, err := cs.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range dss.Items {
		out.Workloads = append(out.Workloads, daemonSetView(&dss.Items[i]))
	}
	sort.Slice(out.Workloads, func(i, j int) bool {
		a, b := out.Workloads[i], out.Workloads[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		out.Pods = append(out.Pods, podView(&pods.Items[i]))
	}
	sort.Slice(out.Pods, func(i, j int) bool {
		a, b := out.Pods[i], out.Pods[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out, nil
}

// Ingress is a slim networking.k8s.io/v1 Ingress.
type Ingress struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Class     string    `json:"class"`
	Hosts     []string  `json:"hosts"`
	Address   []string  `json:"address"`
	TLS       bool      `json:"tls"`
	Created   time.Time `json:"created"`
}

// PVC is a slim PersistentVolumeClaim.
type PVC struct {
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	Status       string    `json:"status"`
	Volume       string    `json:"volume"`
	Capacity     string    `json:"capacity"`
	StorageClass string    `json:"storageClass"`
	AccessModes  []string  `json:"accessModes"`
	Created      time.Time `json:"created"`
}

// PV is a slim PersistentVolume.
type PV struct {
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	Capacity      string    `json:"capacity"`
	Claim         string    `json:"claim"`
	StorageClass  string    `json:"storageClass"`
	ReclaimPolicy string    `json:"reclaimPolicy"`
	Created       time.Time `json:"created"`
}

// StorageClass is a slim StorageClass.
type StorageClass struct {
	Name          string `json:"name"`
	Provisioner   string `json:"provisioner"`
	Default       bool   `json:"default"`
	ReclaimPolicy string `json:"reclaimPolicy"`
	BindingMode   string `json:"bindingMode"`
}

// Network is the payload of the "Rede & Storage" page.
type Network struct {
	Services        []Service      `json:"services"`
	Ingresses       []Ingress      `json:"ingresses"`
	NetworkPolicies int            `json:"networkPolicies"`
	PVCs            []PVC          `json:"pvcs"`
	PVs             []PV           `json:"pvs"`
	StorageClasses  []StorageClass `json:"storageClasses"`
}

// ListNetwork returns services, ingresses and storage objects of ns ("" = all).
func ListNetwork(ctx context.Context, cs kubernetes.Interface, ns string) (*Network, error) {
	out := &Network{Services: []Service{}, Ingresses: []Ingress{}, PVCs: []PVC{}, PVs: []PV{}, StorageClasses: []StorageClass{}}
	svcs, err := cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range svcs.Items {
		out.Services = append(out.Services, serviceView(&svcs.Items[i]))
	}
	sort.Slice(out.Services, func(i, j int) bool {
		a, b := out.Services[i], out.Services[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	if ings, err := cs.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{}); err == nil {
		for _, in := range ings.Items {
			v := Ingress{Name: in.Name, Namespace: in.Namespace, TLS: len(in.Spec.TLS) > 0, Hosts: []string{}, Address: []string{}, Created: in.CreationTimestamp.Time}
			if in.Spec.IngressClassName != nil {
				v.Class = *in.Spec.IngressClassName
			}
			for _, r := range in.Spec.Rules {
				if r.Host != "" {
					v.Hosts = append(v.Hosts, r.Host)
				}
			}
			for _, a := range in.Status.LoadBalancer.Ingress {
				if a.IP != "" {
					v.Address = append(v.Address, a.IP)
				} else if a.Hostname != "" {
					v.Address = append(v.Address, a.Hostname)
				}
			}
			out.Ingresses = append(out.Ingresses, v)
		}
	}
	if nps, err := cs.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{}); err == nil {
		out.NetworkPolicies = len(nps.Items)
	}
	if pvcs, err := cs.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{}); err == nil {
		for _, c := range pvcs.Items {
			v := PVC{Name: c.Name, Namespace: c.Namespace, Status: string(c.Status.Phase), Volume: c.Spec.VolumeName, AccessModes: []string{}, Created: c.CreationTimestamp.Time}
			if q, ok := c.Status.Capacity[corev1.ResourceStorage]; ok {
				v.Capacity = q.String()
			} else if q, ok := c.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
				v.Capacity = q.String()
			}
			if c.Spec.StorageClassName != nil {
				v.StorageClass = *c.Spec.StorageClassName
			}
			for _, m := range c.Spec.AccessModes {
				v.AccessModes = append(v.AccessModes, string(m))
			}
			out.PVCs = append(out.PVCs, v)
		}
	}
	// Cluster-scoped storage objects are listed regardless of the namespace filter.
	if pvs, err := cs.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{}); err == nil {
		for _, p := range pvs.Items {
			v := PV{Name: p.Name, Status: string(p.Status.Phase), StorageClass: p.Spec.StorageClassName,
				ReclaimPolicy: string(p.Spec.PersistentVolumeReclaimPolicy), Created: p.CreationTimestamp.Time}
			if q, ok := p.Spec.Capacity[corev1.ResourceStorage]; ok {
				v.Capacity = q.String()
			}
			if r := p.Spec.ClaimRef; r != nil {
				v.Claim = r.Namespace + "/" + r.Name
			}
			out.PVs = append(out.PVs, v)
		}
	}
	if scs, err := cs.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{}); err == nil {
		for _, s := range scs.Items {
			v := StorageClass{Name: s.Name, Provisioner: s.Provisioner,
				Default: s.Annotations["storageclass.kubernetes.io/is-default-class"] == "true"}
			if s.ReclaimPolicy != nil {
				v.ReclaimPolicy = string(*s.ReclaimPolicy)
			}
			if s.VolumeBindingMode != nil {
				v.BindingMode = string(*s.VolumeBindingMode)
			}
			out.StorageClasses = append(out.StorageClasses, v)
		}
	}
	return out, nil
}

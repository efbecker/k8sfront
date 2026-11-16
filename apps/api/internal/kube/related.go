package kube

import (
	"context"
	"sort"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Ref names one object (same JSON shape as deploy.ObjectRef).
type Ref struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

// templateLabels returns the pod template labels of every workload in ns,
// keyed by "Kind/name".
func templateLabels(ctx context.Context, cs kubernetes.Interface, ns string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	deps, err := cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range deps.Items {
		out["Deployment/"+d.Name] = d.Spec.Template.Labels
	}
	sts, err := cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range sts.Items {
		out["StatefulSet/"+s.Name] = s.Spec.Template.Labels
	}
	dss, err := cs.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range dss.Items {
		out["DaemonSet/"+d.Name] = d.Spec.Template.Labels
	}
	return out, nil
}

func selects(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// RelatedObjects lists what only exists to serve one workload: Services
// whose selector matches its pods and no other workload's, and Ingresses
// whose backends are all such Services. Shared objects are left out.
func RelatedObjects(ctx context.Context, cs kubernetes.Interface, ns, kind, name string) ([]Ref, error) {
	all, err := templateLabels(ctx, cs, ns)
	if err != nil {
		return nil, err
	}
	key := kind + "/" + name
	mine, ok := all[key]
	if !ok {
		return []Ref{}, nil // already gone: nothing to offer
	}
	out := []Ref{}
	svcs, err := cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, s := range svcs.Items {
		if !selects(s.Spec.Selector, mine) {
			continue
		}
		shared := false
		for k, l := range all {
			if k != key && selects(s.Spec.Selector, l) {
				shared = true
				break
			}
		}
		if !shared {
			own[s.Name] = true
			out = append(out, Ref{APIVersion: "v1", Kind: "Service", Namespace: ns, Name: s.Name})
		}
	}
	if len(own) > 0 {
		ings, err := cs.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, in := range ings.Items {
			if backends := ingressServices(&in); len(backends) > 0 && allIn(backends, own) {
				out = append(out, Ref{APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Namespace: ns, Name: in.Name})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Kind > out[j].Kind || (out[i].Kind == out[j].Kind && out[i].Name < out[j].Name)
	})
	return out, nil
}

func ingressServices(in *networkingv1.Ingress) []string {
	var out []string
	add := func(b *networkingv1.IngressBackend) {
		if b != nil && b.Service != nil {
			out = append(out, b.Service.Name)
		}
	}
	add(in.Spec.DefaultBackend)
	for _, r := range in.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for i := range r.HTTP.Paths {
			add(&r.HTTP.Paths[i].Backend)
		}
	}
	return out
}

func allIn(names []string, set map[string]bool) bool {
	for _, n := range names {
		if !set[n] {
			return false
		}
	}
	return true
}

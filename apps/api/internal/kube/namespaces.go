package kube

import (
	"context"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
)

// NSItem is something a person put in a namespace (generated objects such as
// ReplicaSets, controller-owned Pods, the kube-root-ca.crt ConfigMap, the
// default ServiceAccount or Helm's storage Secrets are left out).
type NSItem struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"` // HelmRelease is virtual (Helm's storage Secrets)
	Name       string `json:"name"`
}

// NamespaceUsage summarizes one namespace for the Namespaces screen.
type NamespaceUsage struct {
	Name    string         `json:"name"`
	Phase   string         `json:"phase"`
	Created time.Time      `json:"created"`
	System  bool           `json:"system"`
	Pods    int            `json:"pods"`
	Counts  map[string]int `json:"counts"`
	Items   []NSItem       `json:"items"`
}

// SystemNamespaces cannot be deleted from the UI.
var SystemNamespaces = map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true, "default": true}

var (
	secretsRes    = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	configMapsRes = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
)

// metaItem is the part of an object the inventory needs.
type metaItem struct {
	ns, name    string
	labels      map[string]string
	annotations map[string]string
	owned       bool
}

// listMeta lists secrets/configmaps by metadata only (never their data) when
// a metadata client exists; tests fall back to the typed client.
func listMeta(ctx context.Context, cs kubernetes.Interface, md metadata.Interface, gvr schema.GroupVersionResource, ns string) ([]metaItem, error) {
	var out []metaItem
	if md != nil {
		list, err := md.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, it := range list.Items {
			out = append(out, metaItem{it.Namespace, it.Name, it.Labels, it.Annotations, len(it.OwnerReferences) > 0})
		}
		return out, nil
	}
	if gvr.Resource == "secrets" {
		list, err := cs.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, it := range list.Items {
			out = append(out, metaItem{it.Namespace, it.Name, it.Labels, it.Annotations, len(it.OwnerReferences) > 0})
		}
		return out, nil
	}
	list, err := cs.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, it := range list.Items {
		out = append(out, metaItem{it.Namespace, it.Name, it.Labels, it.Annotations, len(it.OwnerReferences) > 0})
	}
	return out, nil
}

// NamespaceInventory returns the usage of every namespace (ns == "") or one.
func NamespaceInventory(ctx context.Context, cs kubernetes.Interface, md metadata.Interface, ns string) ([]NamespaceUsage, error) {
	byNS := map[string]*NamespaceUsage{}
	if ns == "" {
		list, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, n := range list.Items {
			byNS[n.Name] = &NamespaceUsage{Name: n.Name, Phase: string(n.Status.Phase), Created: n.CreationTimestamp.UTC()}
		}
	} else {
		n, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		byNS[n.Name] = &NamespaceUsage{Name: n.Name, Phase: string(n.Status.Phase), Created: n.CreationTimestamp.UTC()}
	}
	add := func(namespace, apiVersion, kind, name string) {
		if u := byNS[namespace]; u != nil {
			u.Items = append(u.Items, NSItem{APIVersion: apiVersion, Kind: kind, Name: name})
		}
	}
	deps, err := cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range deps.Items {
		add(d.Namespace, "apps/v1", "Deployment", d.Name)
	}
	sts, err := cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range sts.Items {
		add(s.Namespace, "apps/v1", "StatefulSet", s.Name)
	}
	dss, err := cs.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range dss.Items {
		add(d.Namespace, "apps/v1", "DaemonSet", d.Name)
	}
	cjs, err := cs.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, c := range cjs.Items {
		add(c.Namespace, "batch/v1", "CronJob", c.Name)
	}
	jobs, err := cs.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, j := range jobs.Items {
		if len(j.OwnerReferences) == 0 {
			add(j.Namespace, "batch/v1", "Job", j.Name)
		}
	}
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, p := range pods.Items {
		if u := byNS[p.Namespace]; u != nil {
			u.Pods++
		}
		if len(p.OwnerReferences) == 0 && p.Annotations["kubernetes.io/config.mirror"] == "" {
			add(p.Namespace, "v1", "Pod", p.Name)
		}
	}
	svcs, err := cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range svcs.Items {
		if len(s.OwnerReferences) == 0 {
			add(s.Namespace, "v1", "Service", s.Name)
		}
	}
	ings, err := cs.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, i := range ings.Items {
		add(i.Namespace, "networking.k8s.io/v1", "Ingress", i.Name)
	}
	pvcs, err := cs.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, c := range pvcs.Items {
		add(c.Namespace, "v1", "PersistentVolumeClaim", c.Name)
	}
	sas, err := cs.CoreV1().ServiceAccounts(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range sas.Items {
		if s.Name != "default" && len(s.OwnerReferences) == 0 {
			add(s.Namespace, "v1", "ServiceAccount", s.Name)
		}
	}
	cms, err := listMeta(ctx, cs, md, configMapsRes, ns)
	if err != nil {
		return nil, err
	}
	for _, c := range cms {
		if c.name != "kube-root-ca.crt" && !c.owned {
			add(c.ns, "v1", "ConfigMap", c.name)
		}
	}
	secs, err := listMeta(ctx, cs, md, secretsRes, ns)
	if err != nil {
		return nil, err
	}
	releases := map[string]bool{}
	for _, s := range secs {
		switch {
		case s.labels["owner"] == "helm":
			if name := s.labels["name"]; name != "" && !releases[s.ns+"/"+name] {
				releases[s.ns+"/"+name] = true
				add(s.ns, "helm", "HelmRelease", name)
			}
		case s.annotations["kubernetes.io/service-account.name"] != "", s.owned:
		default:
			add(s.ns, "v1", "Secret", s.name)
		}
	}
	out := make([]NamespaceUsage, 0, len(byNS))
	for _, u := range byNS {
		u.System = SystemNamespaces[u.Name]
		u.Counts = map[string]int{}
		if u.Items == nil {
			u.Items = []NSItem{}
		}
		for _, it := range u.Items {
			u.Counts[it.Kind]++
		}
		sort.Slice(u.Items, func(i, j int) bool {
			if u.Items[i].Kind != u.Items[j].Kind {
				return u.Items[i].Kind < u.Items[j].Kind
			}
			return u.Items[i].Name < u.Items[j].Name
		})
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

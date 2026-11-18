package kube

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/registry"
	"github.com/efbecker/k8sfront/apps/api/internal/secrets"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/util/retry"
)

// Labels of the imagePullSecrets k8sfront manages.
const (
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelRegistry  = "k8sfront.io/registry"
)

// PullSecretSpec is the desired imagePullSecret of one registry.
type PullSecretSpec struct {
	RegistryID   string
	Name         string   // Secret name (k8sfront-registry-<name>)
	Namespaces   []string // empty: every non-system namespace
	DockerConfig []byte   // .dockerconfigjson (never logged or returned)
}

// PullSecretChange is one action of a sync (no secret data).
type PullSecretChange struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Action    string `json:"action"` // applied | removed | error
	Error     string `json:"error,omitempty"`
}

var systemNS = registry.SystemNamespaces

func (p *PullSecretSpec) targets(ns string) bool {
	if len(p.Namespaces) == 0 {
		return !registry.IsSystemNamespace(ns)
	}
	return slices.Contains(p.Namespaces, ns)
}

// SyncPullSecrets makes every targeted namespace hold the registry Secrets
// and lists them in its default ServiceAccount, so pods (including the ones
// created by the deploy center) pull with them. Secrets that k8sfront created
// for registries no longer desired there are removed. With only != "", just
// that namespace is reconciled (used right after a deploy creates it) and
// nothing is removed elsewhere.
func SyncPullSecrets(ctx context.Context, cs kubernetes.Interface, md metadata.Interface, desired []PullSecretSpec, only string) ([]PullSecretChange, error) {
	var nss []string
	if only != "" {
		nss = []string{only}
	} else {
		list, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, n := range list.Items {
			if n.Status.Phase != corev1.NamespaceTerminating {
				nss = append(nss, n.Name)
			}
		}
		sort.Strings(nss)
	}
	var out []PullSecretChange
	want := map[string]bool{} // ns/name
	for _, ns := range nss {
		var names []string
		for i := range desired {
			p := &desired[i]
			if !p.targets(ns) {
				continue
			}
			want[ns+"/"+p.Name] = true
			ch := PullSecretChange{Namespace: ns, Name: p.Name, Action: "applied"}
			if err := applySecret(ctx, cs, ns, p); err != nil {
				ch.Action, ch.Error = "error", err.Error()
			} else {
				names = append(names, p.Name)
			}
			out = append(out, ch)
		}
		if len(names) > 0 {
			if err := patchDefaultSA(ctx, cs, ns, names, nil); err != nil {
				out = append(out, PullSecretChange{Namespace: ns, Name: "serviceaccount/default", Action: "error", Error: err.Error()})
			}
		}
	}
	if only != "" {
		return out, nil
	}
	existing, err := managedPullSecrets(ctx, cs, md)
	if err != nil {
		return out, err
	}
	for _, it := range existing {
		if want[it.ns+"/"+it.name] {
			continue
		}
		ch := PullSecretChange{Namespace: it.ns, Name: it.name, Action: "removed"}
		if err := patchDefaultSA(ctx, cs, it.ns, nil, []string{it.name}); err != nil && !apierrors.IsNotFound(err) {
			ch.Action, ch.Error = "error", err.Error()
		} else if err := cs.CoreV1().Secrets(it.ns).Delete(ctx, it.name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			ch.Action, ch.Error = "error", err.Error()
		}
		out = append(out, ch)
	}
	return out, nil
}

func applySecret(ctx context.Context, cs kubernetes.Interface, ns string, p *PullSecretSpec) error {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: ns, Labels: map[string]string{LabelManagedBy: "k8sfront", LabelRegistry: p.RegistryID},
			Annotations: map[string]string{"k8sfront.io/description": "imagePullSecret gerenciado pelo k8sfront (Registries & Imagens)"}},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: p.DockerConfig},
	}
	_, err := cs.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{FieldManager: "k8sfront"})
	if apierrors.IsAlreadyExists(err) {
		// Unconditional update: the stored Secret is never read back.
		_, err = cs.CoreV1().Secrets(ns).Update(ctx, sec, metav1.UpdateOptions{FieldManager: "k8sfront"})
	}
	return err
}

// patchDefaultSA adds and removes imagePullSecrets of the namespace's default
// ServiceAccount. A brand-new namespace gets its ServiceAccount a moment
// after creation, so NotFound is retried briefly when adding.
func patchDefaultSA(ctx context.Context, cs kubernetes.Interface, ns string, add, remove []string) error {
	for attempt := 0; ; attempt++ {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			sa, err := cs.CoreV1().ServiceAccounts(ns).Get(ctx, "default", metav1.GetOptions{})
			if err != nil {
				return err
			}
			changed := false
			kept := sa.ImagePullSecrets[:0:0]
			for _, r := range sa.ImagePullSecrets {
				if slices.Contains(remove, r.Name) {
					changed = true
					continue
				}
				kept = append(kept, r)
			}
			for _, n := range add {
				if !slices.ContainsFunc(kept, func(r corev1.LocalObjectReference) bool { return r.Name == n }) {
					kept = append(kept, corev1.LocalObjectReference{Name: n})
					changed = true
				}
			}
			if !changed {
				return nil
			}
			sa.ImagePullSecrets = kept
			_, err = cs.CoreV1().ServiceAccounts(ns).Update(ctx, sa, metav1.UpdateOptions{FieldManager: "k8sfront"})
			return err
		})
		if err == nil || !apierrors.IsNotFound(err) || len(add) == 0 || attempt >= 10 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// managedPullSecrets lists (metadata only) the registry Secrets k8sfront created.
func managedPullSecrets(ctx context.Context, cs kubernetes.Interface, md metadata.Interface) ([]metaItem, error) {
	sel := metav1.ListOptions{LabelSelector: LabelManagedBy + "=k8sfront," + LabelRegistry}
	var out []metaItem
	if md != nil {
		list, err := md.Resource(secretsGVR).Namespace("").List(ctx, sel)
		if err != nil {
			return nil, err
		}
		for _, it := range list.Items {
			out = append(out, metaItem{ns: it.Namespace, name: it.Name, labels: it.Labels})
		}
		return out, nil
	}
	list, err := cs.CoreV1().Secrets("").List(ctx, sel)
	if err != nil {
		return nil, err
	}
	for _, it := range list.Items {
		out = append(out, metaItem{ns: it.Namespace, name: it.Name, labels: it.Labels})
	}
	return out, nil
}

// --- image catalog -------------------------------------------------------------

// ImageUse is a workload (or bare pod) running an image.
type ImageUse struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

// CatalogImage is one image of the cluster: what runs it and where it is cached.
type CatalogImage struct {
	Ref       string     `json:"ref"`  // fully qualified (docker.io/library/nginx:1.29)
	Name      string     `json:"name"` // without tag/digest
	Host      string     `json:"host"`
	Tag       string     `json:"tag,omitempty"`
	Digest    string     `json:"digest,omitempty"`
	SizeBytes int64      `json:"sizeBytes"`
	Nodes     []string   `json:"nodes"` // nodes that have it cached
	Pods      int        `json:"pods"`
	Uses      []ImageUse `json:"uses"`
	InUse     bool       `json:"inUse"`
	System    bool       `json:"system"` // used only by kube-system pods
}

// Catalog is every image in use or cached on a node.
type Catalog struct {
	Images      []CatalogImage `json:"images"`
	Nodes       []string       `json:"nodes"`
	CollectedAt time.Time      `json:"collectedAt"`
	// Truncated nodes report only their 50 largest images (kubelet nodeStatusMaxImages).
	Truncated []string `json:"truncated"`
}

// ListImages builds the catalog from pods (what runs) and node status (what
// is cached, with sizes).
func ListImages(ctx context.Context, cs kubernetes.Interface) (*Catalog, error) {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	cat := &Catalog{Images: []CatalogImage{}, Nodes: []string{}, Truncated: []string{}, CollectedAt: time.Now().UTC()}
	byRef := map[string]*CatalogImage{}
	get := func(ref string) *CatalogImage {
		img, err := registry.ParseImage(ref)
		if err != nil {
			return nil
		}
		name, tag, digest, host := img.Name(), img.Tag, img.Digest, img.Host
		key := name
		if tag != "" {
			key += ":" + tag
		}
		if digest != "" && tag == "" {
			key += "@" + digest
		}
		if c, ok := byRef[key]; ok {
			return c
		}
		c := &CatalogImage{Ref: key, Name: name, Host: host, Tag: tag, Digest: digest, Nodes: []string{}, Uses: []ImageUse{}, System: true}
		byRef[key] = c
		return c
	}
	for _, n := range nodes.Items {
		cat.Nodes = append(cat.Nodes, n.Name)
		if len(n.Status.Images) >= 50 {
			cat.Truncated = append(cat.Truncated, n.Name)
		}
		for _, im := range n.Status.Images {
			seen := map[*CatalogImage]bool{}
			for _, ref := range im.Names {
				if strings.Contains(ref, "@sha256:") && len(im.Names) > 1 {
					continue // the tagged name identifies the image
				}
				c := get(ref)
				if c == nil || seen[c] {
					continue
				}
				seen[c] = true
				if im.SizeBytes > c.SizeBytes {
					c.SizeBytes = im.SizeBytes
				}
				if !slices.Contains(c.Nodes, n.Name) {
					c.Nodes = append(c.Nodes, n.Name)
				}
			}
		}
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		use := ImageUse{Namespace: p.Namespace, Kind: "Pod", Name: p.Name}
		if o := metav1.GetControllerOf(p); o != nil {
			use.Kind, use.Name = o.Kind, o.Name
			if o.Kind == "ReplicaSet" {
				if i := strings.LastIndex(o.Name, "-"); i > 0 {
					use.Kind, use.Name = "Deployment", o.Name[:i]
				}
			}
		}
		refs := map[*CatalogImage]bool{}
		for _, c := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
			if ci := get(c.Image); ci != nil {
				refs[ci] = true
			}
		}
		for ci := range refs {
			ci.InUse = true
			ci.Pods++
			if !slices.Contains(systemNS, p.Namespace) {
				ci.System = false
			}
			if !slices.Contains(ci.Uses, use) {
				ci.Uses = append(ci.Uses, use)
			}
			if p.Spec.NodeName != "" && !slices.Contains(ci.Nodes, p.Spec.NodeName) {
				ci.Nodes = append(ci.Nodes, p.Spec.NodeName) // running there, so it is cached even if not listed
			}
		}
	}
	for _, c := range byRef {
		if !c.InUse {
			c.System = false
		}
		sort.Strings(c.Nodes)
		cat.Images = append(cat.Images, *c)
	}
	sort.Slice(cat.Images, func(i, j int) bool { return cat.Images[i].Ref < cat.Images[j].Ref })
	sort.Strings(cat.Nodes)
	return cat, nil
}

// --- pre-pull ------------------------------------------------------------------

// PrepullNamespace holds the temporary pre-pull DaemonSets.
const PrepullNamespace = "k8sfront-system"

// PrepullCommand is never meant to exist in the image: the container fails
// right after the image is pulled, so no entrypoint of the image ever runs.
const PrepullCommand = "/k8sfront-prepull-noop"

// Pull states of an image on a node.
const (
	PullPending = "pending"
	PullDone    = "pulled"
	PullFailed  = "failed"
)

// PullState is the pull result of one image on one node.
type PullState struct {
	Node    string `json:"node"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// ContainerName is the pre-pull container of the i-th image.
func ContainerName(i int) string { return fmt.Sprintf("img-%d", i) }

// PullStates evaluates the pre-pull pods: a container that got past
// ContainerCreating has its image on the node (the bogus command makes it
// fail afterwards, which is expected).
func PullStates(pods []corev1.Pod, images []string) []PullState {
	var out []PullState
	for _, p := range pods {
		node := p.Spec.NodeName
		if node == "" {
			continue
		}
		st := map[string]corev1.ContainerStatus{}
		for _, c := range p.Status.ContainerStatuses {
			st[c.Name] = c
		}
		for i, img := range images {
			ps := PullState{Node: node, Image: img, State: PullPending}
			c, ok := st[ContainerName(i)]
			switch {
			case !ok:
			case c.State.Running != nil || c.State.Terminated != nil || c.LastTerminationState.Terminated != nil || c.ImageID != "":
				ps.State = PullDone
			case c.State.Waiting != nil:
				switch c.State.Waiting.Reason {
				case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "ErrImageNeverPull", "RegistryUnavailable", "SignatureValidationFailed":
					ps.State, ps.Message = PullFailed, strings.TrimSpace(c.State.Waiting.Reason+": "+c.State.Waiting.Message)
				case "CrashLoopBackOff", "RunContainerError", "CreateContainerError", "CreateContainerConfigError", "StartError":
					ps.State = PullDone
				}
			}
			out = append(out, ps)
		}
	}
	return out
}

// PrepullDaemonSet runs one container per image on every node (all taints
// tolerated). Containers request almost nothing and never run the image's
// entrypoint (see PrepullCommand).
func PrepullDaemonSet(name string, images, pullSecrets []string) *appsv1.DaemonSet {
	labels := map[string]string{LabelManagedBy: "k8sfront", "k8sfront.io/prepull": name}
	var containers []corev1.Container
	tiny := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1m"), corev1.ResourceMemory: resource.MustParse("8Mi")}
	for i, img := range images {
		containers = append(containers, corev1.Container{
			Name: ContainerName(i), Image: img, Command: []string{PrepullCommand}, ImagePullPolicy: corev1.PullIfNotPresent,
			Resources: corev1.ResourceRequirements{Requests: tiny, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")}},
		})
	}
	var refs []corev1.LocalObjectReference
	for _, s := range pullSecrets {
		refs = append(refs, corev1.LocalObjectReference{Name: s})
	}
	no := false
	grace := int64(0)
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: PrepullNamespace, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: containers, ImagePullSecrets: refs, AutomountServiceAccountToken: &no,
					TerminationGracePeriodSeconds: &grace, EnableServiceLinks: &no,
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
				},
			},
		},
	}
}

// DesiredPullSecrets builds the pull secrets of every registry with
// credentials and pull secrets enabled that targets the cluster. Passwords
// are decrypted only into the returned dockerconfigjson payloads.
func DesiredPullSecrets(ctx context.Context, st store.Store, sec *secrets.Manager, clusterID string) ([]PullSecretSpec, error) {
	regs, err := st.ListRegistries(ctx)
	if err != nil {
		return nil, err
	}
	var out []PullSecretSpec
	for i := range regs {
		r := &regs[i]
		if !r.PullSecret || r.Username == "" || r.PasswordSecretID == nil || !r.AppliesTo(clusterID) {
			continue
		}
		pw, err := sec.Get(ctx, *r.PasswordSecretID)
		if err != nil {
			return nil, fmt.Errorf("registry %s: %w", r.Name, err)
		}
		cfg, err := registry.DockerConfigJSON(r, string(pw))
		for j := range pw {
			pw[j] = 0
		}
		if err != nil {
			return nil, err
		}
		out = append(out, PullSecretSpec{RegistryID: r.ID, Name: registry.SecretName(r), Namespaces: r.Namespaces, DockerConfig: cfg})
	}
	return out, nil
}

// NamespaceExists reports whether ns exists.
func NamespaceExists(ctx context.Context, cs kubernetes.Interface, ns string) (bool, error) {
	_, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// EnsurePrepullNamespace creates k8sfront-system when missing.
func EnsurePrepullNamespace(ctx context.Context, cs kubernetes.Interface) error {
	_, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: PrepullNamespace,
		Labels: map[string]string{LabelManagedBy: "k8sfront"}}}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// StartPrepull (re)creates the pre-pull DaemonSet.
func StartPrepull(ctx context.Context, cs kubernetes.Interface, name string, images, pullSecrets []string) error {
	StopPrepull(ctx, cs, name)
	_, err := cs.AppsV1().DaemonSets(PrepullNamespace).Create(ctx, PrepullDaemonSet(name, images, pullSecrets), metav1.CreateOptions{FieldManager: "k8sfront"})
	return err
}

// PrepullProgress returns how many nodes the DaemonSet targets and the pull
// state of every image on the nodes that already have a pod.
func PrepullProgress(ctx context.Context, cs kubernetes.Interface, name string, images []string) (int, []PullState, error) {
	ds, err := cs.AppsV1().DaemonSets(PrepullNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, nil, err
	}
	pods, err := cs.CoreV1().Pods(PrepullNamespace).List(ctx, metav1.ListOptions{LabelSelector: "k8sfront.io/prepull=" + name})
	if err != nil {
		return 0, nil, err
	}
	return int(ds.Status.DesiredNumberScheduled), PullStates(pods.Items, images), nil
}

// StopPrepull deletes the DaemonSet (and its pods) without waiting.
func StopPrepull(ctx context.Context, cs kubernetes.Interface, name string) {
	bg := metav1.DeletePropagationBackground
	_ = cs.AppsV1().DaemonSets(PrepullNamespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg})
}

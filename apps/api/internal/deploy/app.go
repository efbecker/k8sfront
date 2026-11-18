// Package deploy is the deploy center (phase 4): a visual form rendered into
// Kubernetes manifests on the backend, YAML parsing and server-side apply
// (with dry-run=server), and a metadata-only view of Helm releases.
package deploy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

// ErrInvalid wraps validation failures (HTTP 400).
var ErrInvalid = errors.New("invalid application")

// FieldManager is the server-side apply manager name.
const FieldManager = "k8sfront"

// AppSpec is the visual form of the deploy center.
type AppSpec struct {
	Name          string            `json:"name"`
	Namespace     string            `json:"namespace"`
	Image         string            `json:"image"`
	Replicas      int32             `json:"replicas"`
	ContainerPort int32             `json:"containerPort"`
	Env           []EnvVar          `json:"env"`
	HealthPath    string            `json:"healthPath"`
	Resources     Resources         `json:"resources"`
	Service       *ServiceSpec      `json:"service,omitempty"`
	Ingress       *IngressSpec      `json:"ingress,omitempty"`
	Volume        *VolumeSpec       `json:"volume,omitempty"`
	PullSecret    *PullSecret       `json:"pullSecret,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Resources struct {
	CPURequest    string `json:"cpuRequest"`
	MemoryRequest string `json:"memoryRequest"`
	CPULimit      string `json:"cpuLimit"`
	MemoryLimit   string `json:"memoryLimit"`
}

type ServiceSpec struct {
	Type     string `json:"type"` // ClusterIP | NodePort | LoadBalancer
	Port     int32  `json:"port"`
	NodePort int32  `json:"nodePort"`
}

type IngressSpec struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	ClassName string `json:"className"`
	TLSSecret string `json:"tlsSecret"`
}

type VolumeSpec struct {
	Size         string `json:"size"`
	StorageClass string `json:"storageClass"`
	MountPath    string `json:"mountPath"`
	AccessMode   string `json:"accessMode"` // ReadWriteOnce | ReadWriteMany | ReadOnlyMany | ReadWriteOncePod
}

// PullSecret references an existing imagePullSecret (Name), carries
// registry credentials that become a dockerconfigjson Secret at apply time,
// or points at a registered registry (RegistryID, resolved by the API into
// the other fields). The password is never rendered into the preview.
type PullSecret struct {
	Name       string `json:"name"`
	Server     string `json:"server"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	RegistryID string `json:"registryId,omitempty"`
}

// HasCredentials reports whether a Secret must be created from credentials.
func (p *PullSecret) HasCredentials() bool { return p != nil && p.Password != "" }

var (
	dns1035   = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)
	dns1123   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	subdomain = regexp.MustCompile(`^(\*\.)?[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	envName   = regexp.MustCompile(`^[-._a-zA-Z][-._a-zA-Z0-9]*$`)
	imageRef  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$`)
	labelKey  = regexp.MustCompile(`^([a-z0-9]([-a-z0-9.]*[a-z0-9])?/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`)
	labelVal  = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)
	server    = regexp.MustCompile(`^[a-zA-Z0-9.-]+(:\d+)?(/[a-zA-Z0-9._/-]*)?$`)
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func quantity(field, v string) error {
	if v == "" {
		return nil
	}
	if _, err := resource.ParseQuantity(v); err != nil {
		return invalid("%s: quantidade inválida %q (ex.: 250m, 512Mi, 1Gi)", field, v)
	}
	return nil
}

func (s *PullSecret) secretName(app string) string {
	if s.Name != "" {
		return s.Name
	}
	return app + "-registry"
}

// Normalize fills defaults and validates the form.
func (a *AppSpec) Normalize() error {
	a.Name = strings.TrimSpace(a.Name)
	a.Namespace = strings.TrimSpace(a.Namespace)
	a.Image = strings.TrimSpace(a.Image)
	if a.Namespace == "" {
		a.Namespace = "default"
	}
	if !dns1035.MatchString(a.Name) {
		return invalid("nome %q inválido: use letras minúsculas, números e hífen, começando com letra (até 63)", a.Name)
	}
	if !dns1123.MatchString(a.Namespace) {
		return invalid("namespace %q inválido", a.Namespace)
	}
	if a.Image == "" || !imageRef.MatchString(a.Image) {
		return invalid("imagem %q inválida", a.Image)
	}
	if a.Replicas < 0 || a.Replicas > 100 {
		return invalid("réplicas devem estar entre 0 e 100")
	}
	if a.ContainerPort < 0 || a.ContainerPort > 65535 {
		return invalid("porta do container inválida")
	}
	seen := map[string]bool{}
	for _, e := range a.Env {
		if !envName.MatchString(e.Name) {
			return invalid("variável de ambiente %q inválida", e.Name)
		}
		if seen[e.Name] {
			return invalid("variável de ambiente %q repetida", e.Name)
		}
		seen[e.Name] = true
	}
	for k, v := range a.Labels {
		if !labelKey.MatchString(k) || !labelVal.MatchString(v) {
			return invalid("label %s=%s inválido", k, v)
		}
	}
	if a.HealthPath != "" && (!strings.HasPrefix(a.HealthPath, "/") || a.ContainerPort == 0) {
		return invalid("health check exige caminho começando com / e a porta do container")
	}
	for _, q := range [][2]string{{"CPU request", a.Resources.CPURequest}, {"memória request", a.Resources.MemoryRequest},
		{"CPU limit", a.Resources.CPULimit}, {"memória limit", a.Resources.MemoryLimit}} {
		if err := quantity(q[0], q[1]); err != nil {
			return err
		}
	}
	if s := a.Service; s != nil {
		if a.ContainerPort == 0 {
			return invalid("o Service precisa da porta do container")
		}
		if s.Type == "" {
			s.Type = "ClusterIP"
		}
		if s.Type != "ClusterIP" && s.Type != "NodePort" && s.Type != "LoadBalancer" {
			return invalid("tipo de Service %q inválido", s.Type)
		}
		if s.Port == 0 {
			s.Port = a.ContainerPort
		}
		if s.Port < 1 || s.Port > 65535 {
			return invalid("porta do Service inválida")
		}
		if s.NodePort != 0 && (s.Type == "ClusterIP" || s.NodePort < 30000 || s.NodePort > 32767) {
			return invalid("nodePort deve estar entre 30000 e 32767 (Service NodePort ou LoadBalancer)")
		}
	}
	if in := a.Ingress; in != nil {
		if a.Service == nil {
			return invalid("o Ingress precisa de um Service")
		}
		in.Host = strings.ToLower(strings.TrimSpace(in.Host))
		if in.Host != "" && !subdomain.MatchString(in.Host) {
			return invalid("host do Ingress %q inválido", in.Host)
		}
		if in.Path == "" {
			in.Path = "/"
		}
		if !strings.HasPrefix(in.Path, "/") {
			return invalid("caminho do Ingress deve começar com /")
		}
		if in.ClassName != "" && !dns1123.MatchString(in.ClassName) {
			return invalid("ingressClassName inválido")
		}
		if in.TLSSecret != "" && (!dns1123.MatchString(in.TLSSecret) || in.Host == "") {
			return invalid("TLS exige host e um nome de Secret válido")
		}
	}
	if v := a.Volume; v != nil {
		if v.Size == "" {
			v.Size = "1Gi"
		}
		if err := quantity("tamanho do volume", v.Size); err != nil {
			return err
		}
		if !strings.HasPrefix(v.MountPath, "/") {
			return invalid("ponto de montagem do volume deve ser absoluto")
		}
		if v.AccessMode == "" {
			v.AccessMode = "ReadWriteOnce"
		}
		switch v.AccessMode {
		case "ReadWriteOnce", "ReadWriteMany", "ReadOnlyMany", "ReadWriteOncePod":
		default:
			return invalid("modo de acesso %q inválido", v.AccessMode)
		}
		if v.StorageClass != "" && !subdomain.MatchString(v.StorageClass) {
			return invalid("StorageClass inválida")
		}
	}
	if p := a.PullSecret; p != nil {
		if p.Name != "" && !dns1123.MatchString(p.Name) {
			return invalid("nome do imagePullSecret inválido")
		}
		if p.Password != "" {
			if p.Server == "" || !server.MatchString(p.Server) || p.Username == "" {
				return invalid("credenciais do registry exigem servidor e usuário")
			}
		} else if p.Name == "" {
			a.PullSecret = nil
		}
	}
	return nil
}

func (a *AppSpec) labels() map[string]string {
	l := map[string]string{"app.kubernetes.io/name": a.Name, "app.kubernetes.io/managed-by": "k8sfront"}
	for k, v := range a.Labels {
		l[k] = v
	}
	return l
}

func (a *AppSpec) selector() map[string]string {
	return map[string]string{"app.kubernetes.io/name": a.Name}
}

// Objects returns the typed manifests of the form (without the registry
// credentials Secret, see PullSecretObject).
func (a *AppSpec) Objects() ([]runtime.Object, error) {
	var out []runtime.Object
	if a.Namespace != "default" {
		out = append(out, &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: a.Namespace}})
	}
	meta := metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace, Labels: a.labels()}
	c := corev1.Container{Name: a.Name, Image: a.Image}
	if a.ContainerPort > 0 {
		c.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: a.ContainerPort, Protocol: corev1.ProtocolTCP}}
	}
	for _, e := range a.Env {
		c.Env = append(c.Env, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	req, lim := corev1.ResourceList{}, corev1.ResourceList{}
	set := func(rl corev1.ResourceList, name corev1.ResourceName, v string) {
		if v != "" {
			rl[name] = resource.MustParse(v)
		}
	}
	set(req, corev1.ResourceCPU, a.Resources.CPURequest)
	set(req, corev1.ResourceMemory, a.Resources.MemoryRequest)
	set(lim, corev1.ResourceCPU, a.Resources.CPULimit)
	set(lim, corev1.ResourceMemory, a.Resources.MemoryLimit)
	if len(req) > 0 {
		c.Resources.Requests = req
	}
	if len(lim) > 0 {
		c.Resources.Limits = lim
	}
	if a.HealthPath != "" {
		probe := func(delay int32) *corev1.Probe {
			return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: a.HealthPath, Port: intstr.FromString("http")}},
				InitialDelaySeconds: delay, PeriodSeconds: 10}
		}
		c.ReadinessProbe, c.LivenessProbe = probe(5), probe(15)
	}
	pod := corev1.PodSpec{Containers: []corev1.Container{c}}
	if a.PullSecret != nil {
		pod.ImagePullSecrets = []corev1.LocalObjectReference{{Name: a.PullSecret.secretName(a.Name)}}
	}
	strategy := appsv1.DeploymentStrategy{}
	if v := a.Volume; v != nil {
		claim := a.Name + "-data"
		out = append(out, &corev1.PersistentVolumeClaim{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
			ObjectMeta: metav1.ObjectMeta{Name: claim, Namespace: a.Namespace, Labels: a.labels()},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.PersistentVolumeAccessMode(v.AccessMode)},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(v.Size)}},
			},
		})
		if v.StorageClass != "" {
			sc := v.StorageClass
			out[len(out)-1].(*corev1.PersistentVolumeClaim).Spec.StorageClassName = &sc
		}
		pod.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}}
		pod.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "data", MountPath: v.MountPath}}
		if v.AccessMode != "ReadWriteMany" {
			// A RWO volume cannot be attached to the old and new pod at once.
			strategy.Type = appsv1.RecreateDeploymentStrategyType
		}
	}
	replicas := a.Replicas
	out = append(out, &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: meta,
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: a.selector()},
			Strategy: strategy,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: a.labels()}, Spec: pod},
		},
	})
	if s := a.Service; s != nil {
		port := corev1.ServicePort{Name: "http", Port: s.Port, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP, NodePort: s.NodePort}
		out = append(out, &corev1.Service{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: meta,
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceType(s.Type), Selector: a.selector(), Ports: []corev1.ServicePort{port}},
		})
	}
	if in := a.Ingress; in != nil {
		pt := networkingv1.PathTypePrefix
		rule := networkingv1.IngressRule{Host: in.Host, IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{Path: in.Path, PathType: &pt, Backend: networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{Name: a.Name, Port: networkingv1.ServiceBackendPort{Name: "http"}}}}},
		}}}
		ing := &networkingv1.Ingress{
			TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"},
			ObjectMeta: meta,
			Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{rule}},
		}
		if in.ClassName != "" {
			cn := in.ClassName
			ing.Spec.IngressClassName = &cn
		}
		if in.TLSSecret != "" {
			ing.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{in.Host}, SecretName: in.TLSSecret}}
		}
		out = append(out, ing)
	}
	return out, nil
}

// PullSecretObject builds the dockerconfigjson Secret from the registry
// credentials (only at apply time; never rendered or returned).
func (a *AppSpec) PullSecretObject() (*unstructured.Unstructured, error) {
	p := a.PullSecret
	if !p.HasCredentials() {
		return nil, nil
	}
	auth := base64.StdEncoding.EncodeToString([]byte(p.Username + ":" + p.Password))
	cfg, err := json.Marshal(map[string]any{"auths": map[string]any{p.Server: map[string]string{"username": p.Username, "password": p.Password, "auth": auth}}})
	if err != nil {
		return nil, err
	}
	sec := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: p.secretName(a.Name), Namespace: a.Namespace, Labels: a.labels()},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}
	return toUnstructured(sec)
}

// CreatedPullSecretRef is the registry Secret the form creates from
// credentials (server + user), for deletion. A Secret referenced only by
// name was not created by k8sfront and is left alone.
func (a *AppSpec) CreatedPullSecretRef() *ObjectRef {
	p := a.PullSecret
	if p == nil || p.Server == "" || p.Username == "" || p.RegistryID != "" {
		return nil // a registered registry's Secret is shared: never deleted with the app
	}
	return &ObjectRef{APIVersion: "v1", Kind: "Secret", Namespace: a.Namespace, Name: p.secretName(a.Name)}
}

func toUnstructured(o runtime.Object) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		return nil, err
	}
	delete(m, "status")
	if md, ok := m["metadata"].(map[string]any); ok {
		delete(md, "creationTimestamp")
	}
	if spec, ok := m["spec"].(map[string]any); ok {
		if tpl, ok := spec["template"].(map[string]any); ok {
			if md, ok := tpl["metadata"].(map[string]any); ok {
				delete(md, "creationTimestamp")
			}
		}
		if st, ok := spec["strategy"].(map[string]any); ok && len(st) == 0 {
			delete(spec, "strategy")
		}
		if c, ok := spec["template"].(map[string]any); ok {
			if ps, ok := c["spec"].(map[string]any); ok {
				if cs, ok := ps["containers"].([]any); ok {
					for _, ci := range cs {
						if cm, ok := ci.(map[string]any); ok {
							if r, ok := cm["resources"].(map[string]any); ok && len(r) == 0 {
								delete(cm, "resources")
							}
						}
					}
				}
			}
		}
	}
	return &unstructured.Unstructured{Object: m}, nil
}

// Render returns the manifests as unstructured objects and multi-document YAML.
func (a *AppSpec) Render() ([]*unstructured.Unstructured, string, error) {
	objs, err := a.Objects()
	if err != nil {
		return nil, "", err
	}
	var us []*unstructured.Unstructured
	var docs []string
	if a.PullSecret.HasCredentials() {
		docs = append(docs, fmt.Sprintf("# O Secret %q (credenciais do registry %s) é criado no apply e não aparece aqui.\n",
			a.PullSecret.secretName(a.Name), a.PullSecret.Server))
	}
	for _, o := range objs {
		u, err := toUnstructured(o)
		if err != nil {
			return nil, "", err
		}
		us = append(us, u)
		b, err := yaml.Marshal(u.Object)
		if err != nil {
			return nil, "", err
		}
		docs = append(docs, string(b))
	}
	text := ""
	for i, d := range docs {
		if i > 0 && !strings.HasPrefix(docs[i-1], "#") {
			text += "---\n"
		}
		text += d
	}
	return us, text, nil
}

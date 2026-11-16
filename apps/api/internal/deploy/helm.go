package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
	"sigs.k8s.io/yaml"
)

// HelmRelease is a release as seen from the labels of Helm's storage
// Secrets. Only metadata is listed: the release payload (which embeds the
// values, possibly secrets) is never read.
type HelmRelease struct {
	Name      string     `json:"name"`
	Namespace string     `json:"namespace"`
	Revision  int        `json:"revision"`
	Status    string     `json:"status"`
	Updated   *time.Time `json:"updated,omitempty"`
}

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// ListHelmReleases returns the latest revision of each release.
func ListHelmReleases(ctx context.Context, md metadata.Interface, ns string) ([]HelmRelease, error) {
	list, err := md.Resource(secretsGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: "owner=helm"})
	if err != nil {
		return nil, err
	}
	latest := map[string]HelmRelease{}
	for _, it := range list.Items {
		l := it.Labels
		rev, _ := strconv.Atoi(l["version"])
		r := HelmRelease{Name: l["name"], Namespace: it.Namespace, Revision: rev, Status: l["status"]}
		if r.Name == "" {
			continue
		}
		if ts, err := strconv.ParseInt(l["modifiedAt"], 10, 64); err == nil {
			t := time.Unix(ts, 0).UTC()
			r.Updated = &t
		} else if !it.CreationTimestamp.IsZero() {
			t := it.CreationTimestamp.UTC()
			r.Updated = &t
		}
		key := r.Namespace + "/" + r.Name
		if cur, ok := latest[key]; !ok || r.Revision > cur.Revision {
			latest[key] = r
		}
	}
	out := make([]HelmRelease, 0, len(latest))
	for _, r := range latest {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// HelmInput is the request to install/upgrade or uninstall a chart.
type HelmInput struct {
	Action    string `json:"action"` // install | uninstall
	Release   string `json:"release"`
	Namespace string `json:"namespace"`
	Chart     string `json:"chart"`
	Repo      string `json:"repo"`
	Version   string `json:"version"`
	Values    string `json:"values"`
}

var (
	releaseRe  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,51}[a-z0-9])?$`)
	chartRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*(/[a-zA-Z0-9][a-zA-Z0-9._-]*)?$`)
	ociRe      = regexp.MustCompile(`^oci://[a-zA-Z0-9.-]+(:\d+)?(/[a-zA-Z0-9._-]+)+$`)
	chartVerRe = regexp.MustCompile(`^v?[0-9A-Za-z.+~^<>=*, -]{1,64}$`)
)

// MaxValuesBytes bounds the values file.
const MaxValuesBytes = 256 << 10

// Normalize validates the request.
func (h *HelmInput) Normalize() error {
	h.Release = strings.TrimSpace(h.Release)
	h.Namespace = strings.TrimSpace(h.Namespace)
	h.Chart = strings.TrimSpace(h.Chart)
	h.Repo = strings.TrimSpace(h.Repo)
	h.Version = strings.TrimSpace(h.Version)
	if h.Action == "" {
		h.Action = "install"
	}
	if h.Namespace == "" {
		h.Namespace = "default"
	}
	if !releaseRe.MatchString(h.Release) {
		return invalid("nome de release %q inválido (minúsculas, números e hífen, até 53)", h.Release)
	}
	if !dns1123.MatchString(h.Namespace) {
		return invalid("namespace %q inválido", h.Namespace)
	}
	switch h.Action {
	case "uninstall":
		h.Chart, h.Repo, h.Version, h.Values = "", "", "", ""
		return nil
	case "install":
	default:
		return invalid("ação %q inválida", h.Action)
	}
	if strings.HasPrefix(h.Chart, "oci://") {
		if !ociRe.MatchString(h.Chart) {
			return invalid("referência OCI %q inválida", h.Chart)
		}
		if h.Repo != "" {
			return invalid("charts oci:// não usam URL de repositório")
		}
	} else {
		if !chartRe.MatchString(h.Chart) {
			return invalid("chart %q inválido", h.Chart)
		}
		if h.Repo == "" {
			return invalid("informe a URL do repositório do chart (ou use uma referência oci://)")
		}
		u, err := url.Parse(h.Repo)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.ContainsAny(h.Repo, " '\"`$\\") {
			return invalid("URL de repositório %q inválida (use https://)", h.Repo)
		}
		if strings.Contains(h.Chart, "/") {
			return invalid("com --repo informe só o nome do chart, sem prefixo de repositório")
		}
	}
	if h.Version != "" && !chartVerRe.MatchString(h.Version) {
		return invalid("versão do chart %q inválida", h.Version)
	}
	if len(h.Values) > MaxValuesBytes {
		return invalid("values.yaml acima de %d KiB", MaxValuesBytes>>10)
	}
	for _, line := range strings.Split(h.Values, "\n") {
		if strings.TrimSpace(line) == "K8SFRONT_EOF" {
			return errors.New("values.yaml contém uma linha reservada (K8SFRONT_EOF)")
		}
	}
	if strings.TrimSpace(h.Values) != "" {
		var v map[string]any
		if err := yaml.Unmarshal([]byte(h.Values), &v); err != nil {
			return invalid("values.yaml inválido: %v", err)
		}
	}
	return nil
}

// String describes the request for logs and audit (never the values).
func (h *HelmInput) String() string {
	if h.Action == "uninstall" {
		return fmt.Sprintf("helm uninstall %s -n %s", h.Release, h.Namespace)
	}
	src := h.Chart
	if h.Repo != "" {
		src = h.Repo + " " + h.Chart
	}
	if h.Version != "" {
		src += "@" + h.Version
	}
	return fmt.Sprintf("helm upgrade --install %s %s -n %s", h.Release, src, h.Namespace)
}

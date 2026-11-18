// Package registry holds the container registry logic that does not depend on
// a cluster: presets (Docker Hub, GHCR, Quay, Harbor, generic), validation,
// image reference parsing, containerd hosts.toml rendering, dockerconfigjson
// pull secrets and a credentials check against the registry API.
package registry

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// ErrInvalid wraps validation failures (HTTP 400).
var ErrInvalid = errors.New("invalid registry")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Kinds of registry.
const (
	KindDockerHub = "dockerhub"
	KindGHCR      = "ghcr"
	KindQuay      = "quay"
	KindHarbor    = "harbor"
	KindGeneric   = "generic"
)

// Preset is the fixed host/endpoint of a public registry.
type Preset struct {
	Host     string
	Endpoint string
}

// Presets of the public registries (their host and endpoint are fixed).
var Presets = map[string]Preset{
	KindDockerHub: {"docker.io", "https://registry-1.docker.io"},
	KindGHCR:      {"ghcr.io", "https://ghcr.io"},
	KindQuay:      {"quay.io", "https://quay.io"},
}

// DockerHubHost is the implicit registry of short image names (nginx, bitnami/redis).
const DockerHubHost = "docker.io"

var (
	nameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,40}[a-z0-9])?$`)
	hostRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?(:[0-9]{1,5})?$`)
	nsRe   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	userRe = regexp.MustCompile(`^[^\s:]{1,256}$`)
)

// SystemNamespaces never receive pull secrets automatically.
var SystemNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}

// IsSystemNamespace reports whether ns is cluster plumbing.
func IsSystemNamespace(ns string) bool { return slices.Contains(SystemNamespaces, ns) }

func cleanURL(field, raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", invalid("%s %q: use uma URL http:// ou https://", field, raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", invalid("%s %q: informe só esquema, host e porta (sem usuário, caminho ou query)", field, raw)
	}
	h := strings.ToLower(u.Host)
	if !hostRe.MatchString(h) && net.ParseIP(strings.Trim(u.Hostname(), "[]")) == nil {
		return "", invalid("%s %q: host inválido", field, raw)
	}
	return u.Scheme + "://" + h, nil
}

// Normalize fills the preset fields, trims and validates a registry.
func Normalize(r *store.Registry) error {
	r.Name = strings.TrimSpace(r.Name)
	r.Host = strings.ToLower(strings.TrimSpace(r.Host))
	r.Username = strings.TrimSpace(r.Username)
	r.CACert = strings.TrimSpace(r.CACert)
	if r.Kind == "" {
		r.Kind = KindGeneric
	}
	if !nameRe.MatchString(r.Name) {
		return invalid("nome %q inválido: letras minúsculas, números e hífen (até 42)", r.Name)
	}
	switch r.Kind {
	case KindDockerHub, KindGHCR, KindQuay:
		p := Presets[r.Kind]
		r.Host, r.Endpoint = p.Host, p.Endpoint
	case KindHarbor, KindGeneric:
		if r.Host == "" && r.Endpoint != "" {
			if u, err := url.Parse(strings.TrimSpace(r.Endpoint)); err == nil {
				r.Host = strings.ToLower(u.Host)
			}
		}
		if !hostRe.MatchString(r.Host) || !strings.ContainsAny(r.Host, ".:") && r.Host != "localhost" {
			return invalid("host %q inválido: é o nome usado nas imagens, como registry.exemplo.com ou 10.0.0.5:5000", r.Host)
		}
		if r.Host == DockerHubHost {
			return invalid("para o Docker Hub use o tipo Docker Hub")
		}
		if strings.TrimSpace(r.Endpoint) == "" {
			r.Endpoint = "https://" + r.Host
		}
	default:
		return invalid("tipo %q inválido", r.Kind)
	}
	ep, err := cleanURL("endpoint", r.Endpoint)
	if err != nil {
		return err
	}
	r.Endpoint = ep
	mirrors := []string{}
	for _, m := range r.Mirrors {
		if strings.TrimSpace(m) == "" {
			continue
		}
		u, err := cleanURL("mirror", m)
		if err != nil {
			return err
		}
		if u != r.Endpoint && !slices.Contains(mirrors, u) {
			mirrors = append(mirrors, u)
		}
	}
	if len(mirrors) > 10 {
		return invalid("no máximo 10 mirrors")
	}
	r.Mirrors = mirrors
	if r.Username != "" && !userRe.MatchString(r.Username) {
		return invalid("usuário inválido")
	}
	if r.CACert != "" {
		if err := checkCA(r.CACert); err != nil {
			return err
		}
		r.CACert += "\n"
	}
	nss := []string{}
	for _, ns := range r.Namespaces {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		if !nsRe.MatchString(ns) {
			return invalid("namespace %q inválido", ns)
		}
		if !slices.Contains(nss, ns) {
			nss = append(nss, ns)
		}
	}
	slices.Sort(nss)
	r.Namespaces = nss
	if r.ClusterIDs == nil {
		r.ClusterIDs = []string{}
	}
	return nil
}

func checkCA(pemText string) error {
	rest := []byte(pemText)
	n := 0
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			return invalid("o CA deve conter apenas blocos CERTIFICATE (nunca uma chave privada)")
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			return invalid("certificado CA inválido: %v", err)
		}
		n++
	}
	if n == 0 || strings.TrimSpace(string(rest)) != "" {
		return invalid("CA deve estar em PEM (-----BEGIN CERTIFICATE-----)")
	}
	return nil
}

// SecretName is the imagePullSecret created for the registry in each namespace.
func SecretName(r *store.Registry) string { return "k8sfront-registry-" + r.Name }

// AuthKey is the dockerconfigjson key kubelet matches against image hosts.
func AuthKey(r *store.Registry) string {
	if r.Host == DockerHubHost {
		return "https://index.docker.io/v1/"
	}
	return r.Host
}

// DockerConfigJSON builds the .dockerconfigjson payload of a pull secret.
func DockerConfigJSON(r *store.Registry, password string) ([]byte, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(r.Username + ":" + password))
	return json.Marshal(map[string]any{"auths": map[string]any{
		AuthKey(r): map[string]string{"username": r.Username, "password": password, "auth": auth},
	}})
}

// Image is a parsed image reference.
type Image struct {
	Host       string `json:"host"`
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"`
}

// String is the fully qualified reference (docker.io/library/nginx:latest).
func (i Image) String() string {
	s := i.Host + "/" + i.Repository
	if i.Tag != "" {
		s += ":" + i.Tag
	}
	if i.Digest != "" {
		s += "@" + i.Digest
	}
	return s
}

// Name is host/repository without tag or digest.
func (i Image) Name() string { return i.Host + "/" + i.Repository }

var refRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$`)

// ParseImage normalizes an image reference the way containerd does:
// nginx → docker.io/library/nginx:latest.
func ParseImage(ref string) (Image, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 512 || !refRe.MatchString(ref) {
		return Image{}, invalid("imagem %q inválida", ref)
	}
	var img Image
	if i := strings.Index(ref, "@"); i >= 0 {
		img.Digest = ref[i+1:]
		ref = ref[:i]
		if !strings.Contains(img.Digest, ":") {
			return Image{}, invalid("digest inválido em %q", ref)
		}
	}
	first, rest, hasSlash := strings.Cut(ref, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		img.Host = strings.ToLower(first)
		ref = rest
	} else {
		img.Host = DockerHubHost
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		img.Tag = ref[i+1:]
		ref = ref[:i]
		if img.Tag == "" {
			return Image{}, invalid("tag vazia")
		}
	}
	if ref == "" || strings.Contains(ref, ":") || strings.HasSuffix(ref, "/") {
		return Image{}, invalid("imagem inválida")
	}
	if img.Host == DockerHubHost && !strings.Contains(ref, "/") {
		ref = "library/" + ref
	}
	if img.Host == "index.docker.io" || img.Host == "registry-1.docker.io" {
		img.Host = DockerHubHost
	}
	img.Repository = ref
	if img.Tag == "" && img.Digest == "" {
		img.Tag = "latest"
	}
	return img, nil
}

// Match returns the registry whose host serves the image (nil if none).
func Match(image string, regs []store.Registry) *store.Registry {
	img, err := ParseImage(image)
	if err != nil {
		return nil
	}
	for i := range regs {
		if regs[i].Host == img.Host {
			return &regs[i]
		}
	}
	return nil
}

func tomlString(s string) string {
	b, _ := json.Marshal(s) // JSON strings are valid TOML basic strings
	return string(b)
}

// HostDir is the directory of the registry under /etc/containerd/certs.d.
func HostDir(r *store.Registry) string { return r.Host }

// HostsTOML renders /etc/containerd/certs.d/<host>/hosts.toml: mirrors first
// (pull + resolve), then the upstream server. caPath is where the CA bundle
// is written on the node ("" when the registry has none).
func HostsTOML(r *store.Registry, caPath string) string {
	var b strings.Builder
	b.WriteString("# Gerado pelo k8sfront (registry " + r.Name + "). Alterações manuais são sobrescritas.\n")
	fmt.Fprintf(&b, "server = %s\n", tomlString(r.Endpoint))
	tls := func(indent string) {
		if caPath != "" {
			fmt.Fprintf(&b, "%sca = %s\n", indent, tomlString(caPath))
		}
		if r.SkipVerify {
			fmt.Fprintf(&b, "%sskip_verify = true\n", indent)
		}
	}
	tls("")
	for _, m := range r.Mirrors {
		fmt.Fprintf(&b, "\n[host.%s]\n  capabilities = [\"pull\", \"resolve\"]\n", tomlString(m))
		tls("  ")
	}
	return b.String()
}

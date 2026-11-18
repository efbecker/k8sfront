package registry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

func TestParseImage(t *testing.T) {
	cases := map[string]string{
		"nginx":                              "docker.io/library/nginx:latest",
		"nginx:1.29":                         "docker.io/library/nginx:1.29",
		"bitnami/redis:7":                    "docker.io/bitnami/redis:7",
		"docker.io/library/nginx:1.29":       "docker.io/library/nginx:1.29",
		"index.docker.io/library/nginx":      "docker.io/library/nginx:latest",
		"ghcr.io/org/app:v1":                 "ghcr.io/org/app:v1",
		"registry.local:5000/team/api:2.0.1": "registry.local:5000/team/api:2.0.1",
		"localhost/app":                      "localhost/app:latest",
		"quay.io/x/y@sha256:abcd":            "quay.io/x/y@sha256:abcd",
		"registry.k8s.io/pause:3.10":         "registry.k8s.io/pause:3.10",
	}
	for in, want := range cases {
		img, err := ParseImage(in)
		if err != nil || img.String() != want {
			t.Errorf("ParseImage(%q) = %q, %v; want %q", in, img.String(), err, want)
		}
	}
	for _, bad := range []string{"", "nginx:", "a b", "x:y:z", "repo/", "$(id)", "nginx@nodigest"} {
		if _, err := ParseImage(bad); err == nil {
			t.Errorf("ParseImage(%q) should fail", bad)
		}
	}
}

func testCA(t *testing.T) string {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "registry-ca"}, NotBefore: time.Now(),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestNormalize(t *testing.T) {
	hub := &store.Registry{Name: "hub", Kind: KindDockerHub, Host: "ignored", Mirrors: []string{" https://cache.local:5000/ ", "", "https://cache.local:5000"}}
	if err := Normalize(hub); err != nil {
		t.Fatal(err)
	}
	if hub.Host != "docker.io" || hub.Endpoint != "https://registry-1.docker.io" || len(hub.Mirrors) != 1 || hub.Mirrors[0] != "https://cache.local:5000" {
		t.Fatalf("preset not applied: %+v", hub)
	}
	priv := &store.Registry{Name: "harbor", Kind: KindHarbor, Host: "Harbor.Example.com", CACert: testCA(t), Namespaces: []string{"b", "a", "a", " "}}
	if err := Normalize(priv); err != nil {
		t.Fatal(err)
	}
	if priv.Host != "harbor.example.com" || priv.Endpoint != "https://harbor.example.com" || strings.Join(priv.Namespaces, ",") != "a,b" {
		t.Fatalf("generic defaults: %+v", priv)
	}
	bad := map[string]func(r *store.Registry){
		"name":       func(r *store.Registry) { r.Name = "Bad Name" },
		"kind":       func(r *store.Registry) { r.Kind = "ecr" },
		"host":       func(r *store.Registry) { r.Host = "noport" },
		"hub-host":   func(r *store.Registry) { r.Host = "docker.io" },
		"endpoint":   func(r *store.Registry) { r.Endpoint = "ftp://x.example.com" },
		"userinfo":   func(r *store.Registry) { r.Endpoint = "https://u:p@x.example.com" },
		"path":       func(r *store.Registry) { r.Mirrors = []string{"https://m.example.com/v2/x"} },
		"ca":         func(r *store.Registry) { r.CACert = "not a pem" },
		"private":    func(r *store.Registry) { r.CACert = "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----" },
		"namespace":  func(r *store.Registry) { r.Namespaces = []string{"Bad_NS"} },
		"username":   func(r *store.Registry) { r.Username = "a b" },
		"heredoc":    func(r *store.Registry) { r.Mirrors = []string{"https://x.example.com\nK8SFRONT_EOF"} },
		"quote-host": func(r *store.Registry) { r.Host = `x.example.com"` },
	}
	for name, mut := range bad {
		r := &store.Registry{Name: "reg", Kind: KindGeneric, Host: "registry.example.com"}
		mut(r)
		if err := Normalize(r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestHostsTOML(t *testing.T) {
	r := &store.Registry{Name: "hub", Kind: KindDockerHub, Mirrors: []string{"https://cache.local:5000", "http://10.0.0.9:5000"}, SkipVerify: true}
	if err := Normalize(r); err != nil {
		t.Fatal(err)
	}
	got := HostsTOML(r, "/etc/containerd/certs.d/docker.io/ca.crt")
	for _, want := range []string{
		`server = "https://registry-1.docker.io"`,
		`ca = "/etc/containerd/certs.d/docker.io/ca.crt"`,
		`[host."https://cache.local:5000"]`,
		`[host."http://10.0.0.9:5000"]`,
		`capabilities = ["pull", "resolve"]`,
		`skip_verify = true`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hosts.toml missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "cache.local") > strings.Index(got, "10.0.0.9") {
		t.Error("mirrors must keep their order (containerd tries them in order)")
	}
}

func TestDockerConfigJSON(t *testing.T) {
	r := &store.Registry{Name: "hub", Kind: KindDockerHub, Username: "ci"}
	_ = Normalize(r)
	raw, err := DockerConfigJSON(r, "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Auths map[string]struct{ Username, Password, Auth string } `json:"auths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	a, ok := cfg.Auths["https://index.docker.io/v1/"]
	if !ok || a.Auth != base64.StdEncoding.EncodeToString([]byte("ci:s3cr3t")) {
		t.Fatalf("docker hub auth key/payload wrong: %s", raw)
	}
	g := &store.Registry{Name: "g", Kind: KindGHCR}
	_ = Normalize(g)
	if AuthKey(g) != "ghcr.io" || SecretName(g) != "k8sfront-registry-g" {
		t.Fatal("auth key / secret name")
	}
}

func TestMatch(t *testing.T) {
	regs := []store.Registry{{ID: "1", Host: "docker.io"}, {ID: "2", Host: "registry.local:5000"}}
	if m := Match("nginx:1", regs); m == nil || m.ID != "1" {
		t.Fatal("short names belong to Docker Hub")
	}
	if m := Match("registry.local:5000/a/b", regs); m == nil || m.ID != "2" {
		t.Fatal("host with port")
	}
	if Match("ghcr.io/a/b", regs) != nil {
		t.Fatal("unknown host")
	}
}

func TestCheckBearerAndBasic(t *testing.T) {
	var tokenSrv *httptest.Server
	tokenSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if r.URL.Query().Get("service") != "reg" {
			w.WriteHeader(400)
			return
		}
		if !ok {
			w.Write([]byte(`{"token":"anon"}`))
			return
		}
		if u != "ci" || p != "good" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"token":"t"}`))
	}))
	defer tokenSrv.Close()
	bearer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+tokenSrv.URL+`/token",service="reg"`)
		w.WriteHeader(401)
	}))
	defer bearer.Close()
	r := &store.Registry{Name: "b", Kind: KindGeneric, Host: "reg.example.com", Endpoint: bearer.URL, Username: "ci"}
	if res := Check(context.Background(), r, "good"); !res.OK || !res.Authenticated {
		t.Fatalf("bearer good creds: %+v", res)
	}
	if res := Check(context.Background(), r, "bad"); res.OK {
		t.Fatalf("bearer bad creds accepted: %+v", res)
	}
	r.Username = ""
	if res := Check(context.Background(), r, ""); !res.OK || res.Authenticated {
		t.Fatalf("anonymous: %+v", res)
	}

	basic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == "ci" && p == "good" {
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="Registry"`)
		w.WriteHeader(401)
	}))
	defer basic.Close()
	r = &store.Registry{Name: "c", Kind: KindGeneric, Host: "reg.example.com", Endpoint: basic.URL, Username: "ci"}
	if res := Check(context.Background(), r, "good"); !res.OK || !res.Authenticated {
		t.Fatalf("basic: %+v", res)
	}
	if res := Check(context.Background(), r, "nope"); res.OK {
		t.Fatalf("basic bad creds accepted: %+v", res)
	}
}

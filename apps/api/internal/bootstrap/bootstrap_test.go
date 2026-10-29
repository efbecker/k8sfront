package bootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
)

func haSpec() *Spec {
	v, _ := k8sver.Parse("v1.37.2")
	return &Spec{
		ClusterName: "prod", Version: v, PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", CNI: CNICilium, Topology: TopologyMulti,
		Options:       Options{MetricsServer: true, KubeVIP: &KubeVIP{Address: "192.168.10.50", Interface: "ens18"}},
		ControlPlanes: []Node{{"a", "cp1", "192.168.10.11"}, {"b", "cp2", "192.168.10.12"}, {"c", "cp3", "192.168.10.13"}},
		Workers:       []Node{{"d", "w1", "192.168.10.21"}},
	}
}

func TestValidate(t *testing.T) {
	if err := haSpec().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(s *Spec){
		"overlap":         func(s *Spec) { s.ServiceCIDR = "10.244.128.0/20" },
		"no vip for HA":   func(s *Spec) { s.Options.KubeVIP = nil },
		"even CPs":        func(s *Spec) { s.ControlPlanes = s.ControlPlanes[:2] },
		"old k8s":         func(s *Spec) { s.Version.Minor = 28 },
		"bad name":        func(s *Spec) { s.ClusterName = "Prod_1" },
		"node in podcidr": func(s *Spec) { s.Workers[0].Host = "10.244.1.5" },
		"vip is node":     func(s *Spec) { s.Options.KubeVIP.Address = "192.168.10.11" },
		"single w/ worker": func(s *Spec) {
			s.Topology = TopologySingle
			s.ControlPlanes = s.ControlPlanes[:1]
			s.Options.KubeVIP = nil
		},
		"duplicate node": func(s *Spec) { s.Workers[0] = s.ControlPlanes[1] },
		"bad iface":      func(s *Spec) { s.Options.KubeVIP.Interface = "eth0; reboot" },
	}
	for name, mutate := range cases {
		s := haSpec()
		mutate(s)
		if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: expected ErrInvalidSpec, got %v", name, err)
		}
	}
}

func TestJoinSecretsFormat(t *testing.T) {
	sec, err := NewJoinSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`).MatchString(sec.Token) || len(sec.CertificateKey) != 64 {
		t.Fatalf("bad secrets: %q %q", sec.Token, sec.CertificateKey)
	}
}

// Every rendered script must be valid bash once wrapped by sshx.BuildScript.
func TestScriptsAreValidBash(t *testing.T) {
	s := haSpec()
	sec, _ := NewJoinSecrets()
	calico := haSpec()
	calico.CNI = CNICalico
	scripts := map[string]string{
		"swap": SwapScript(), "kernel": KernelScript(), "containerd": ContainerdScript(),
		"repo": s.RepoScript(), "packages": s.PackagesScript(), "images": s.ImagesScript(),
		"init": s.InitScript(sec), "finalize": KubeVIPFinalizeScript(), "creds": s.JoinCredentialsScript(sec),
		"join-cp": s.JoinScript(s.ControlPlanes[1], true, sec, "sha256:abc"), "join-w": s.JoinScript(s.Workers[0], false, sec, "sha256:abc"),
		"cilium": s.CNIScript(), "calico": calico.CNIScript(), "metrics": MetricsServerScript(), "untaint": UntaintScript(),
	}
	for name, body := range scripts {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(sshx.BuildScript(body, nil))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: bash -n failed: %v\n%s", name, err, out)
		}
		if regexp.MustCompile(`\bapt-key\b`).MatchString(body) {
			t.Errorf("%s uses apt-key", name)
		}
		if strings.Contains(body, "apt-get") && !strings.Contains(body, "--force-confold") {
			t.Errorf("%s runs apt-get without the non-interactive dpkg options", name)
		}
	}
	if !strings.Contains(scripts["repo"], "/etc/apt/keyrings/kubernetes-apt-keyring.gpg") || !strings.Contains(scripts["repo"], "v1.37") {
		t.Error("repo script must use the keyrings directory and the minor channel")
	}
	if !strings.Contains(scripts["init"], "super-admin.conf") || !strings.Contains(scripts["join-cp"], "kube-vip") ||
		strings.Contains(scripts["join-w"], "kube-vip") {
		t.Error("kube-vip must run only on control planes (super-admin.conf during init)")
	}
	// Secrets travel inside the script (stdin), never on a kubeadm command line.
	for _, l := range strings.Split(scripts["init"]+scripts["creds"]+scripts["join-cp"], "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "kubeadm") && (strings.Contains(l, sec.Token) || strings.Contains(l, sec.CertificateKey)) {
			t.Errorf("secret on a kubeadm command line: %q", l)
		}
	}
}

func selfSignedCA(t *testing.T) []byte {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kubernetes"}, NotBefore: time.Now(),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCACertHash(t *testing.T) {
	h, err := CACertHash(selfSignedCA(t))
	if err != nil || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(h) {
		t.Fatalf("hash %q %v", h, err)
	}
	if _, err := CACertHash([]byte("nope")); err == nil {
		t.Fatal("expected error")
	}
}

// With K8SFRONT_KUBEADM pointing at a kubeadm binary, the rendered
// configurations are checked by `kubeadm config validate`.
func TestConfigsValidateWithKubeadm(t *testing.T) {
	bin := os.Getenv("K8SFRONT_KUBEADM")
	if bin == "" {
		t.Skip("set K8SFRONT_KUBEADM=/path/to/kubeadm to validate configs")
	}
	s := haSpec()
	sec, _ := NewJoinSecrets()
	dir := t.TempDir()
	docs := map[string]string{
		"init.yaml": s.InitConfig(sec), "token.yaml": s.TokenConfig(sec),
		"join-cp.yaml": s.JoinConfig(s.ControlPlanes[1], true, sec, "sha256:"+strings.Repeat("a", 64)),
		"join-w.yaml":  s.JoinConfig(s.Workers[0], false, sec, "sha256:"+strings.Repeat("a", 64)),
	}
	for name, doc := range docs {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(doc), 0o600)
		if out, err := exec.Command(bin, "config", "validate", "--config", p).CombinedOutput(); err != nil {
			t.Errorf("%s rejected by kubeadm: %v\n%s\n---\n%s", name, err, out, doc)
		}
	}
}

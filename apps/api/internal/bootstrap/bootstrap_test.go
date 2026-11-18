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
		"proxy and vip":  func(s *Spec) { s.Options.EndpointMode = EndpointLocalProxy },
		"vip mode no vip": func(s *Spec) {
			s.Options.EndpointMode = EndpointKubeVIP
			s.Options.KubeVIP = nil
		},
		"bad mode": func(s *Spec) { s.Options.EndpointMode = "dns" },
	}
	for name, mutate := range cases {
		s := haSpec()
		mutate(s)
		if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: expected ErrInvalidSpec, got %v", name, err)
		}
	}
}

func proxySpec() *Spec {
	s := haSpec()
	s.Options.KubeVIP = nil
	s.Options.EndpointMode = EndpointLocalProxy
	return s
}

func TestValidateNewRequiresOddControlPlanes(t *testing.T) {
	s := proxySpec()
	s.ControlPlanes = s.ControlPlanes[:2]
	// Lifecycle jobs see even counts while control planes are added one by one.
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateNew(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("new cluster with 2 control planes: %v", err)
	}
	if err := proxySpec().ValidateNew(); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointModes(t *testing.T) {
	legacy := haSpec()
	legacy.Options.KubeVIP = nil
	legacy.ControlPlanes = legacy.ControlPlanes[:1]
	if legacy.Mode() != EndpointDirect || legacy.Endpoint() != "192.168.10.11:6443" {
		t.Fatalf("legacy clusters keep the first control plane: %s %s", legacy.Mode(), legacy.Endpoint())
	}
	legacy.ControlPlanes = haSpec().ControlPlanes
	if err := legacy.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatal("direct mode must refuse more than one control plane")
	}
	if v := haSpec(); v.Mode() != EndpointKubeVIP || v.Endpoint() != "192.168.10.50:6443" || len(v.ExternalEndpoints()) != 1 {
		t.Fatalf("kube-vip endpoint: %s %v", v.Endpoint(), v.ExternalEndpoints())
	}
	p := proxySpec()
	if p.Endpoint() != "k8s-api.k8sfront.internal:7445" {
		t.Fatalf("local proxy endpoint: %s", p.Endpoint())
	}
	if got := p.ExternalEndpoints(); len(got) != 3 || got[2] != "192.168.10.13:6443" {
		t.Fatalf("external endpoints: %v", got)
	}
	cc := p.ClusterConfig()
	if !strings.Contains(cc, `controlPlaneEndpoint: "k8s-api.k8sfront.internal:7445"`) || !strings.Contains(cc, `- "k8s-api.k8sfront.internal"`) {
		t.Fatalf("cluster config must use and sign the proxy name:\n%s", cc)
	}
	sec, _ := NewJoinSecrets()
	if j := p.JoinConfig(p.Workers[0], false, sec, "sha256:abc"); !strings.Contains(j, `apiServerEndpoint: "k8s-api.k8sfront.internal:7445"`) {
		t.Fatalf("joins go through the local proxy:\n%s", j)
	}
	if strings.Contains(p.InitScript(sec), "kube-vip") || strings.Contains(p.JoinScript(p.ControlPlanes[1], true, sec, "sha256:abc"), "kube-vip") {
		t.Fatal("no kube-vip in local proxy mode")
	}
}

func TestAPIProxyConfig(t *testing.T) {
	cfg := APIProxyConfig(proxySpec().ControlPlanes)
	for _, want := range []string{"bind 127.0.0.1:7445", "uri /readyz", "check-ssl", "on-marked-down shutdown-sessions",
		"server cp1 192.168.10.11:6443", "server cp3 192.168.10.13:6443"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "0.0.0.0") || strings.Contains(cfg, "*:7445") {
		t.Error("the proxy must listen on loopback only")
	}
	if bin, err := exec.LookPath("haproxy"); err == nil {
		p := filepath.Join(t.TempDir(), "apiproxy.cfg")
		os.WriteFile(p, []byte(cfg), 0o600)
		if out, err := exec.Command(bin, "-c", "-f", p).CombinedOutput(); err != nil {
			t.Errorf("haproxy rejected the config: %v\n%s", err, out)
		}
	}
	script := APIProxyScript(proxySpec().ControlPlanes)
	if !strings.Contains(script, "k8s-api.k8sfront.internal") || !strings.Contains(script, "Before=kubelet.service") || strings.Contains(script, "apt-key") {
		t.Error("proxy script must map the name in /etc/hosts and start before the kubelet")
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
		"cilium": s.CNIScript(), "calico": calico.CNIScript(), "metrics": MetricsServerScript(), "untaint": UntaintScript(), "netcheck": s.NetCheckScript(),
		"precheck": UpgradePrecheckScript(), "etcd-snapshot": EtcdSnapshotScript("cp1", "v1.38.1"),
		"upgrade-apply": UpgradeKubeadmScript(s.Version, true), "upgrade-node": UpgradeKubeadmScript(s.Version, false),
		"upgrade-kubelet": UpgradeKubeletScript(s.Version), "repo-for": RepoScriptFor(s.Version),
		"drain": DrainScript("w1", true), "cordon": DrainScript("w1", false), "uncordon": UncordonScript("w1"), "restore-node": RestoreNodeScript("w1"),
		"wait-node": WaitNodeScript("w1", "v1.37.2"), "wait-kubelet": WaitKubeletScript(), "reboot": RebootScript(),
		"os-check": OSCheckScript(), "os-upgrade": OSUpgradeScript(), "certs-check": CertsCheckScript(), "certs-renew": CertsRenewScript(),
		"delete-node": DeleteNodeScript("w1"), "reset-node": ResetNodeScript(),
		"apiproxy": APIProxyScript(s.ControlPlanes), "etcd-member-remove": EtcdMemberRemoveScript("cp3", "192.168.10.13"),
		"init-proxy": proxySpec().InitScript(sec), "join-proxy": proxySpec().JoinScript(s.ControlPlanes[1], true, sec, "sha256:abc"),
		"helm-install":   HelmScript(HelmRequest{Action: HelmInstall, Release: "web", Namespace: "apps", Chart: "nginx", Repo: "https://charts.example.com", Version: "1.2.3", Values: "replicaCount: 2\n"}),
		"helm-oci":       HelmScript(HelmRequest{Action: HelmInstall, Release: "web", Namespace: "apps", Chart: "oci://registry.example.com/charts/web"}),
		"helm-uninstall": HelmScript(HelmRequest{Action: HelmUninstall, Release: "web", Namespace: "apps"}),
		"registries": RegistriesScript([]RegistryHost{{Dir: "docker.io", TOML: "server = \"https://registry-1.docker.io\"\n"},
			{Dir: "r.example.com:5000", TOML: "server = \"https://r.example.com:5000\"\n", CA: "-----BEGIN CERTIFICATE-----\nAA\n-----END CERTIFICATE-----\n"}}),
		"registries-empty": RegistriesScript(nil),
		"image-prune":      ImagePruneScript(),
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
	if !strings.Contains(scripts["containerd"], "/opt/cni/bin") || !strings.Contains(scripts["containerd"], "containernetworking-plugins") {
		t.Error("containerd must look for CNI plugins in /opt/cni/bin (where Cilium/Calico install them), not Debian's /usr/lib/cni")
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
	p := proxySpec()
	sec, _ := NewJoinSecrets()
	dir := t.TempDir()
	docs := map[string]string{
		"init.yaml": s.InitConfig(sec), "token.yaml": s.TokenConfig(sec),
		"init-proxy.yaml": p.InitConfig(sec), "join-proxy.yaml": p.JoinConfig(p.Workers[0], false, sec, "sha256:"+strings.Repeat("a", 64)),
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

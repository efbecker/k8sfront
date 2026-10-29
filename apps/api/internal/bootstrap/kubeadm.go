package bootstrap

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// q renders a YAML-safe double-quoted scalar (JSON strings are valid YAML).
func q(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// JoinSecrets are the short-lived credentials used to join nodes. Both values
// must be registered in the session redactors before use.
type JoinSecrets struct {
	Token          string // [a-z0-9]{6}.[a-z0-9]{16}
	CertificateKey string // 64 hex chars, decrypts the uploaded control-plane certs
}

const tokenAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// NewJoinSecrets generates a bootstrap token and certificate key.
func NewJoinSecrets() (JoinSecrets, error) {
	tok := make([]byte, 0, 22)
	buf := make([]byte, 64)
	for len(tok) < 22 {
		if _, err := rand.Read(buf); err != nil {
			return JoinSecrets{}, err
		}
		for _, c := range buf {
			// Rejection sampling: 252 = 7*36 keeps the distribution uniform.
			if c < 252 && len(tok) < 22 {
				tok = append(tok, tokenAlphabet[int(c)%len(tokenAlphabet)])
			}
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return JoinSecrets{}, err
	}
	return JoinSecrets{Token: string(tok[:6]) + "." + string(tok[6:]), CertificateKey: hex.EncodeToString(key)}, nil
}

func advertise(host string) string {
	if ip, err := netip.ParseAddr(host); err == nil {
		return fmt.Sprintf("localAPIEndpoint:\n  advertiseAddress: %s\n  bindPort: %d\n", q(ip.String()), APIServerPort)
	}
	return ""
}

func nodeRegistration(host string) string {
	var b strings.Builder
	b.WriteString("nodeRegistration:\n  criSocket: " + q(CRISocket) + "\n  imagePullPolicy: IfNotPresent\n")
	if ip, err := netip.ParseAddr(host); err == nil {
		// Pin the kubelet to the address the operator registered (multi-NIC hosts).
		b.WriteString("  kubeletExtraArgs:\n  - name: node-ip\n    value: " + q(ip.String()) + "\n")
	}
	return b.String()
}

func bootstrapTokens(sec JoinSecrets) string {
	return "bootstrapTokens:\n- token: " + q(sec.Token) + "\n  ttl: 2h0m0s\n  usages: [signing, authentication]\n  groups: [system:bootstrappers:kubeadm:default-node-token]\n"
}

// InitConfig renders the kubeadm v1beta4 configuration for the first control plane.
func (s *Spec) InitConfig(sec JoinSecrets) string {
	cp := s.ControlPlanes[0]
	var b strings.Builder
	b.WriteString("apiVersion: kubeadm.k8s.io/v1beta4\nkind: InitConfiguration\n")
	b.WriteString(bootstrapTokens(sec))
	b.WriteString("certificateKey: " + q(sec.CertificateKey) + "\n")
	b.WriteString(advertise(cp.Host))
	b.WriteString(nodeRegistration(cp.Host))
	b.WriteString("---\n")
	b.WriteString(s.ClusterConfig())
	b.WriteString("---\napiVersion: kubelet.config.k8s.io/v1beta1\nkind: KubeletConfiguration\ncgroupDriver: systemd\n")
	return b.String()
}

// ClusterConfig renders the ClusterConfiguration document.
func (s *Spec) ClusterConfig() string {
	var b strings.Builder
	b.WriteString("apiVersion: kubeadm.k8s.io/v1beta4\nkind: ClusterConfiguration\n")
	b.WriteString("clusterName: " + q(s.ClusterName) + "\n")
	b.WriteString("kubernetesVersion: " + q(s.Version.String()) + "\n")
	b.WriteString("controlPlaneEndpoint: " + q(s.Endpoint()) + "\n")
	b.WriteString("networking:\n  podSubnet: " + q(s.PodCIDR) + "\n  serviceSubnet: " + q(s.ServiceCIDR) + "\n")
	sans := []string{}
	if kv := s.Options.KubeVIP; kv != nil && kv.Address != "" {
		sans = append(sans, kv.Address)
	}
	for _, n := range s.ControlPlanes {
		sans = append(sans, n.Host)
	}
	sans = append(sans, s.Options.ExtraSANs...)
	b.WriteString("apiServer:\n  certSANs:\n")
	for _, san := range dedupe(sans) {
		b.WriteString("  - " + q(san) + "\n")
	}
	return b.String()
}

// TokenConfig is the minimal InitConfiguration used by `kubeadm token create`
// and `kubeadm init phase upload-certs` so the secrets never appear in argv.
func (s *Spec) TokenConfig(sec JoinSecrets) string {
	return "apiVersion: kubeadm.k8s.io/v1beta4\nkind: InitConfiguration\n" + bootstrapTokens(sec) +
		"certificateKey: " + q(sec.CertificateKey) + "\n" + nodeRegistration(s.ControlPlanes[0].Host) + "---\n" + s.ClusterConfig()
}

// JoinConfig renders the JoinConfiguration for a node. caHash is "sha256:<hex>".
func (s *Spec) JoinConfig(n Node, controlPlane bool, sec JoinSecrets, caHash string) string {
	var b strings.Builder
	b.WriteString("apiVersion: kubeadm.k8s.io/v1beta4\nkind: JoinConfiguration\n")
	b.WriteString("discovery:\n  bootstrapToken:\n    apiServerEndpoint: " + q(s.Endpoint()) + "\n    token: " + q(sec.Token) +
		"\n    caCertHashes:\n    - " + q(caHash) + "\n")
	b.WriteString(nodeRegistration(n.Host))
	if controlPlane {
		b.WriteString("controlPlane:\n  certificateKey: " + q(sec.CertificateKey) + "\n")
		if ip, err := netip.ParseAddr(n.Host); err == nil {
			b.WriteString(fmt.Sprintf("  localAPIEndpoint:\n    advertiseAddress: %s\n    bindPort: %d\n", q(ip.String()), APIServerPort))
		}
	}
	return b.String()
}

// CACertHash computes kubeadm's discovery hash (SHA-256 of the CA's
// SubjectPublicKeyInfo) from /etc/kubernetes/pki/ca.crt.
func CACertHash(caPEM []byte) (string, error) {
	block, _ := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("ca.crt is not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// KubeVIPManifest renders the kube-vip static pod (ARP mode, control-plane
// load balancing with leader election). kubeconfig is the host path mounted
// as the pod's credentials: super-admin.conf on the first control plane
// during `kubeadm init` (Kubernetes >= 1.29), admin.conf everywhere else.
func (s *Spec) KubeVIPManifest(kubeconfig string) string {
	kv := s.Options.KubeVIP
	env := [][2]string{
		{"vip_arp", "true"},
		{"port", fmt.Sprint(APIServerPort)},
		{"vip_interface", "__K8SFRONT_VIP_IFACE__"},
		{"vip_cidr", "32"},
		{"dns_mode", "first"},
		{"cp_enable", "true"},
		{"cp_namespace", "kube-system"},
		{"svc_enable", "false"},
		{"vip_leaderelection", "true"},
		{"vip_leasename", "plndr-cp-lock"},
		{"vip_leaseduration", "5"},
		{"vip_renewdeadline", "3"},
		{"vip_retryperiod", "1"},
		{"address", kv.Address},
	}
	var b strings.Builder
	b.WriteString(`apiVersion: v1
kind: Pod
metadata:
  name: kube-vip
  namespace: kube-system
  labels:
    app.kubernetes.io/managed-by: k8sfront
spec:
  containers:
  - name: kube-vip
    image: ` + q("ghcr.io/kube-vip/kube-vip:"+DefaultKubeVIPVersion) + `
    imagePullPolicy: IfNotPresent
    args: ["manager"]
    env:
`)
	for _, e := range env {
		b.WriteString("    - name: " + e[0] + "\n      value: " + q(e[1]) + "\n")
	}
	b.WriteString(`    securityContext:
      capabilities:
        add: ["NET_ADMIN", "NET_RAW"]
        drop: ["ALL"]
    volumeMounts:
    - mountPath: /etc/kubernetes/admin.conf
      name: kubeconfig
  hostAliases:
  - hostnames: ["kubernetes"]
    ip: 127.0.0.1
  hostNetwork: true
  volumes:
  - name: kubeconfig
    hostPath:
      path: ` + q(kubeconfig) + `
`)
	return b.String()
}

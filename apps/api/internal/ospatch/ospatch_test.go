package ospatch

import "testing"

func TestParse(t *testing.T) {
	out := `Atualizando índices do APT
K8SFRONT_PKG libssl3t64/stable-security 3.5.1-1+deb13u2 amd64 [upgradable from: 3.5.1-1+deb13u1]
K8SFRONT_PKG kubeadm/unknown 1.37.3-1.1 amd64 [upgradable from: 1.37.2-1.1]
K8SFRONT_PKG tzdata/stable-updates,stable 2026b-0+deb13u1 all [upgradable from: 2026a-0+deb13u1]
K8SFRONT_HOLD kubeadm
K8SFRONT_HOLD kubectl
K8SFRONT_HOLD kubelet
K8SFRONT_REBOOT=yes
K8SFRONT_KERNEL=6.12.48+deb13-amd64
`
	r := Parse(out)
	if len(r.Packages) != 3 || r.Security != 1 || !r.RebootRequired || len(r.Held) != 3 || r.Kernel != "6.12.48+deb13-amd64" {
		t.Fatalf("report %+v", r)
	}
	if p := r.Packages[0]; p.Name != "libssl3t64" || !p.Security || p.From != "3.5.1-1+deb13u1" || p.To != "3.5.1-1+deb13u2" {
		t.Fatalf("pkg %+v", p)
	}
	if !r.Packages[1].Held || r.Packages[2].Held || r.Packages[2].Suite != "stable-updates,stable" {
		t.Fatalf("held flags %+v", r.Packages)
	}
}

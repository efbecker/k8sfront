package bootstrap

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
)

//go:embed scripts/*.sh
var scriptFS embed.FS

var templates = template.Must(template.New("").Delims("<%", "%>").Funcs(template.FuncMap{
	"q": sshx.ShellQuote,
}).ParseFS(scriptFS, "scripts/*.sh"))

// render executes scripts/<name>.sh with data, prefixed by the common helpers.
func render(name string, data any) string {
	var b bytes.Buffer
	if err := templates.ExecuteTemplate(&b, "common.sh", nil); err != nil {
		panic(err)
	}
	if err := templates.ExecuteTemplate(&b, name+".sh", data); err != nil {
		panic(fmt.Sprintf("render %s: %v", name, err)) // templates are static: a failure is a programming error
	}
	return b.String()
}

// Node preparation (all nodes, all idempotent).

func SwapScript() string       { return render("swap", nil) }
func KernelScript() string     { return render("kernel", nil) }
func ContainerdScript() string { return render("containerd", nil) }

func (s *Spec) RepoScript() string {
	return render("k8s-repo", map[string]string{"Minor": s.Version.MinorString()})
}

func (s *Spec) PackagesScript() string {
	return render("k8s-packages", map[string]string{"Plain": s.Version.Plain(), "Version": s.Version.String()})
}

func (s *Spec) ImagesScript() string {
	return render("images", map[string]string{"Version": s.Version.String()})
}

func (s *Spec) vipInterface() string {
	if s.Options.KubeVIP != nil {
		return s.Options.KubeVIP.Interface
	}
	return ""
}

func (s *Spec) kubeVIPEnabled() bool {
	return s.Options.KubeVIP != nil && s.Options.KubeVIP.Address != ""
}

// InitScript runs kubeadm init on the first control plane (skipped when done).
func (s *Spec) InitScript(sec JoinSecrets) string {
	data := map[string]any{"Config": s.InitConfig(sec), "KubeVIP": s.kubeVIPEnabled(), "VIPInterface": s.vipInterface()}
	if s.kubeVIPEnabled() {
		// During init only super-admin.conf has the rights kube-vip needs.
		data["KubeVIPManifest"] = s.KubeVIPManifest("/etc/kubernetes/super-admin.conf")
	}
	return render("init", data)
}

func KubeVIPFinalizeScript() string { return render("kubevip-finalize", nil) }

// JoinCredentialsScript (re)creates the bootstrap token and, for HA, uploads
// the control-plane certificates encrypted with the certificate key.
func (s *Spec) JoinCredentialsScript(sec JoinSecrets) string {
	return render("join-credentials", map[string]any{
		"Config": s.TokenConfig(sec), "TokenID": strings.SplitN(sec.Token, ".", 2)[0], "UploadCerts": len(s.ControlPlanes) > 1,
	})
}

// JoinScript joins a control plane or worker (skipped when already joined).
func (s *Spec) JoinScript(n Node, controlPlane bool, sec JoinSecrets, caHash string) string {
	data := map[string]any{
		"Config": s.JoinConfig(n, controlPlane, sec, caHash), "ControlPlane": controlPlane,
		"KubeVIP": controlPlane && s.kubeVIPEnabled(), "VIPInterface": s.vipInterface(),
	}
	if controlPlane && s.kubeVIPEnabled() {
		data["KubeVIPManifest"] = s.KubeVIPManifest("/etc/kubernetes/admin.conf")
	}
	return render("join", data)
}

func (s *Spec) CNIScript() string {
	if s.CNI == CNICalico {
		return render("cni-calico", map[string]string{"Version": s.CNIVersion(), "PodCIDR": s.PodCIDR})
	}
	return render("cni-cilium", map[string]string{"Version": strings.TrimPrefix(s.CNIVersion(), "v"), "CLIVersion": DefaultCiliumCLIVersion})
}

func MetricsServerScript() string {
	return render("metrics-server", map[string]string{"Version": DefaultMetricsServer})
}

func UntaintScript() string { return render("untaint", nil) }

// Read-only commands whose output the orchestrator parses (run with Quiet).
const (
	ReadAdminConf = "cat /etc/kubernetes/admin.conf"
	ReadCACert    = "cat /etc/kubernetes/pki/ca.crt"
	GetNodesJSON  = "kubectl --kubeconfig /etc/kubernetes/admin.conf get nodes -o json"
)

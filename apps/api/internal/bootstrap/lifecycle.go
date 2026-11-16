package bootstrap

import (
	"net"
	"strings"

	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
)

// Lifecycle scripts (phase 5): upgrades, node maintenance, OS patching,
// certificates and Helm. Scripts marked "primary" run on the first control
// plane and drive the cluster through kubectl with admin.conf.

// DefaultHelmVersion is the Helm CLI installed on the primary control plane
// (checksum verified against get.helm.sh).
const DefaultHelmVersion = "v4.0.0"

// DrainTimeout bounds kubectl drain (PDBs that never allow an eviction make
// the job pause instead of hanging).
const DrainTimeout = "10m"

// UpgradePrecheckScript (primary): all nodes Ready, etcd healthy, deprecated
// API usage and blocking PDBs (warnings).
func UpgradePrecheckScript() string { return render("upgrade-precheck", nil) }

// EtcdSnapshotScript (primary): etcdctl snapshot through the local etcd pod,
// kept under /var/backups/k8sfront (last 5).
func EtcdSnapshotScript(node, label string) string {
	return render("etcd-snapshot", map[string]string{"Node": node, "Label": label})
}

// UpgradeKubeadmScript installs the exact kubeadm and runs `kubeadm upgrade
// apply` (first control plane) or `kubeadm upgrade node` (every other node).
func UpgradeKubeadmScript(v k8sver.Version, apply bool) string {
	return render("upgrade-kubeadm", map[string]any{"Plain": v.Plain(), "Version": v.String(), "Apply": apply})
}

// UpgradeKubeletScript installs the exact kubelet/kubectl (unhold → install →
// hold) and restarts the kubelet.
func UpgradeKubeletScript(v k8sver.Version) string {
	return render("upgrade-kubelet", map[string]string{"Plain": v.Plain(), "Version": v.String()})
}

// RepoScriptFor points the node at the pkgs.k8s.io channel of v's minor.
func RepoScriptFor(v k8sver.Version) string {
	return render("k8s-repo", map[string]string{"Minor": v.MinorString()})
}

// DrainScript (primary) cordons the node and, unless drain is false (single
// node clusters), evicts its pods respecting PodDisruptionBudgets.
func DrainScript(node string, drain bool) string {
	return render("drain", map[string]any{"Node": node, "Drain": drain, "Timeout": DrainTimeout})
}

// UncordonScript (primary).
func UncordonScript(node string) string { return render("uncordon", map[string]string{"Node": node}) }

// WaitNodeScript (primary) waits for Ready and, when version is set, for the
// kubelet to report it.
func WaitNodeScript(node, version string) string {
	return render("wait-node", map[string]string{"Node": node, "Version": version})
}

// WaitKubeletScript runs on a node after it rebooted.
func WaitKubeletScript() string { return render("wait-kubelet", nil) }

// RebootScript schedules a reboot in 3s so the SSH command returns cleanly.
func RebootScript() string { return render("reboot", nil) }

// OSCheckScript refreshes APT and prints K8SFRONT_* lines parsed by ParseOSCheck.
func OSCheckScript() string { return render("os-check", nil) }

// OSUpgradeScript applies pending updates preserving apt-mark holds.
func OSUpgradeScript() string { return render("os-upgrade", nil) }

// CertsCheckScript prints `kubeadm certs check-expiration`.
func CertsCheckScript() string { return render("certs-check", nil) }

// CertsRenewScript renews every kubeadm certificate and restarts the control
// plane static pods one at a time.
func CertsRenewScript() string {
	return render("certs-renew", map[string]string{"CRISocket": CRISocket})
}

// DeleteNodeScript (primary).
func DeleteNodeScript(node string) string {
	return render("delete-node", map[string]string{"Node": node})
}

// ResetNodeScript runs kubeadm reset on a node leaving the cluster.
func ResetNodeScript() string { return render("reset-node", map[string]string{"CRISocket": CRISocket}) }

// HelmAction is what a Helm job does.
type HelmAction string

const (
	HelmInstall   HelmAction = "install" // upgrade --install
	HelmUninstall HelmAction = "uninstall"
)

// HelmRequest describes a Helm operation. Values may hold secrets: they reach
// helm through process substitution inside the script sent over stdin.
type HelmRequest struct {
	Action    HelmAction
	Release   string
	Namespace string
	Chart     string // repo chart name, or oci:// reference
	Repo      string // https:// repository URL (empty for oci://)
	Version   string
	Values    string
}

// HelmScript (primary) installs the pinned Helm CLI if needed and runs the request.
func HelmScript(r HelmRequest) string {
	return render("helm", map[string]any{
		"HelmVersion": DefaultHelmVersion, "Action": string(r.Action), "Release": r.Release, "Namespace": r.Namespace,
		"Chart": r.Chart, "Repo": r.Repo, "Version": r.Version, "Values": strings.TrimRight(r.Values, "\n"),
	})
}

// Read-only commands (run with Quiet; output parsed by the orchestrator).
const (
	ReadBootID   = "cat /proc/sys/kernel/random/boot_id"
	ReadHostname = "hostname"
)

// EtcdMemberRemoveScript (run on a remaining control plane) removes the etcd
// member of a control plane leaving the cluster, matched by name or peer URL.
// Idempotent: kubeadm reset on the node usually removed it already.
func EtcdMemberRemoveScript(node, host string) string {
	return render("etcd-member-remove", map[string]string{"Node": node, "PeerURL": "https://" + net.JoinHostPort(host, "2380")})
}

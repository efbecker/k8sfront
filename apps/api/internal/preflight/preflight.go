// Package preflight validates that a Debian 13+ server can join a Kubernetes
// cluster: SSH access, sudo, OS version, resources, clock and free ports.
package preflight

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// Status of a check or of the whole report.
type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Check is one validated requirement.
type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Facts are raw host facts gathered in a single unprivileged SSH call.
type Facts struct {
	Hostname        string  `json:"hostname"`
	Kernel          string  `json:"kernel"`
	Arch            string  `json:"arch"`
	CPUs            int     `json:"cpus"`
	MemTotalBytes   uint64  `json:"memTotalBytes"`
	DiskFreeBytes   uint64  `json:"diskFreeBytes"` // free space on /var/lib
	ClockSkewSec    float64 `json:"clockSkewSeconds"`
	NTPSynchronized string  `json:"ntpSynchronized"` // yes | no | unknown
	SwapBytes       uint64  `json:"swapBytes"`
	ListeningPorts  []int   `json:"listeningPorts"`
	CgroupV2        bool    `json:"cgroupV2"`
	Init            string  `json:"init"`
}

// Report is the outcome of a preflight run.
type Report struct {
	Status    Status           `json:"status"`
	Role      store.NodeRole   `json:"role"`
	OS        *sshx.DebianInfo `json:"os,omitempty"`
	Sudo      *sshx.SudoReport `json:"sudo,omitempty"`
	Facts     *Facts           `json:"facts,omitempty"`
	Checks    []Check          `json:"checks"`
	CheckedAt time.Time        `json:"checkedAt"`
}

// Add appends a check and folds its status into the report status.
func (r *Report) Add(c Check) {
	r.Checks = append(r.Checks, c)
	r.Status = worst(r.Status, c.Status)
}

func worst(a, b Status) Status {
	rank := map[Status]int{"": 0, Pass: 1, Warn: 2, Fail: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Requirements are minimums per role (kubeadm's own preflight thresholds).
type Requirements struct {
	MinCPUs      int
	MinMemBytes  uint64
	MinDiskBytes uint64
	WarnDisk     uint64
	Ports        []int
}

const (
	gib = 1 << 30
	mib = 1 << 20
)

// RequirementsFor returns the minimums for a node role.
func RequirementsFor(role store.NodeRole) Requirements {
	switch role {
	case store.RoleControlPlane:
		return Requirements{MinCPUs: 2, MinMemBytes: 1700 * mib, MinDiskBytes: 10 * gib, WarnDisk: 20 * gib,
			Ports: []int{6443, 2379, 2380, 10250, 10257, 10259}}
	case store.RoleWorker:
		return Requirements{MinCPUs: 1, MinMemBytes: 1 * gib, MinDiskBytes: 10 * gib, WarnDisk: 20 * gib,
			Ports: []int{10250, 10256}}
	default:
		return Requirements{MinCPUs: 1, MinMemBytes: 1 * gib, MinDiskBytes: 10 * gib, WarnDisk: 20 * gib,
			Ports: []int{6443, 2379, 2380, 10250, 10256, 10257, 10259}}
	}
}

// FactsScript collects facts without root. Listening ports come from
// /proc/net so the check works even without iproute2.
const FactsScript = `echo "hostname=$(hostname 2>/dev/null)"
echo "kernel=$(uname -r)"
echo "arch=$(uname -m)"
echo "cpus=$(nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo)"
echo "mem_kb=$(awk '/^MemTotal:/{print $2}' /proc/meminfo)"
d=/var/lib; [ -d "$d" ] || d=/
echo "disk_free=$(df -PB1 "$d" | awk 'NR==2{print $4}')"
echo "swap_kb=$(awk 'NR>1{s+=$3} END{print s+0}' /proc/swaps)"
echo "ntp_sync=$(timedatectl show -p NTPSynchronized --value 2>/dev/null || echo unknown)"
echo "cgroup2=$([ -f /sys/fs/cgroup/cgroup.controllers ] && echo yes || echo no)"
echo "init=$(cat /proc/1/comm 2>/dev/null)"
echo "ports_hex=$(cat /proc/net/tcp /proc/net/tcp6 2>/dev/null | awk '$4=="0A"{split($2,a,":"); print a[2]}' | sort -u | tr '\n' ',')"
echo "epoch=$(date +%s.%N)"
`

// ParseFacts parses FactsScript output. localEpoch is the controller's clock
// when the output was received, used to estimate skew.
func ParseFacts(out string, localEpoch float64) (*Facts, error) {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}
	if _, ok := kv["kernel"]; !ok {
		return nil, fmt.Errorf("facts output is incomplete")
	}
	f := &Facts{Hostname: kv["hostname"], Kernel: kv["kernel"], Arch: kv["arch"], NTPSynchronized: kv["ntp_sync"],
		CgroupV2: kv["cgroup2"] == "yes", Init: kv["init"]}
	f.CPUs, _ = strconv.Atoi(kv["cpus"])
	if v, err := strconv.ParseUint(kv["mem_kb"], 10, 64); err == nil {
		f.MemTotalBytes = v * 1024
	}
	f.DiskFreeBytes, _ = strconv.ParseUint(kv["disk_free"], 10, 64)
	if v, err := strconv.ParseUint(kv["swap_kb"], 10, 64); err == nil {
		f.SwapBytes = v * 1024
	}
	if f.NTPSynchronized == "" {
		f.NTPSynchronized = "unknown"
	}
	if remote, err := strconv.ParseFloat(kv["epoch"], 64); err == nil && localEpoch > 0 {
		f.ClockSkewSec = math.Round((remote-localEpoch)*1000) / 1000
	}
	seen := map[int]bool{}
	for _, h := range strings.Split(kv["ports_hex"], ",") {
		if h == "" {
			continue
		}
		if p, err := strconv.ParseUint(h, 16, 16); err == nil && !seen[int(p)] {
			seen[int(p)] = true
			f.ListeningPorts = append(f.ListeningPorts, int(p))
		}
	}
	sort.Ints(f.ListeningPorts)
	return f, nil
}

// Evaluate turns facts into checks for the given role.
func Evaluate(f *Facts, role store.NodeRole) []Check {
	req := RequirementsFor(role)
	var checks []Check

	arch := Check{ID: "arch", Name: "Arquitetura da CPU", Status: Pass, Detail: f.Arch}
	if f.Arch != "x86_64" && f.Arch != "aarch64" {
		arch.Status, arch.Detail = Fail, fmt.Sprintf("%s não é suportada (amd64 ou arm64)", f.Arch)
	}
	checks = append(checks, arch)

	cpu := Check{ID: "cpu", Name: "CPU", Status: Pass, Detail: fmt.Sprintf("%d vCPU (mínimo %d)", f.CPUs, req.MinCPUs)}
	if f.CPUs < req.MinCPUs {
		cpu.Status = Fail
	}
	checks = append(checks, cpu)

	mem := Check{ID: "memory", Name: "Memória RAM", Status: Pass,
		Detail: fmt.Sprintf("%s (mínimo %s)", humanBytes(f.MemTotalBytes), humanBytes(req.MinMemBytes))}
	if f.MemTotalBytes < req.MinMemBytes {
		mem.Status = Fail
	}
	checks = append(checks, mem)

	disk := Check{ID: "disk", Name: "Disco livre em /var/lib", Status: Pass,
		Detail: fmt.Sprintf("%s livres (mínimo %s, recomendado %s)", humanBytes(f.DiskFreeBytes), humanBytes(req.MinDiskBytes), humanBytes(req.WarnDisk))}
	switch {
	case f.DiskFreeBytes < req.MinDiskBytes:
		disk.Status = Fail
	case f.DiskFreeBytes < req.WarnDisk:
		disk.Status = Warn
	}
	checks = append(checks, disk)

	clock := Check{ID: "clock", Name: "Sincronia de relógio", Status: Pass,
		Detail: fmt.Sprintf("desvio de %.3fs em relação ao control-plane da plataforma; NTP sincronizado: %s", f.ClockSkewSec, f.NTPSynchronized)}
	skew := math.Abs(f.ClockSkewSec)
	switch {
	case skew > 30:
		clock.Status = Fail
	case skew > 2 || f.NTPSynchronized == "no":
		clock.Status = Warn
	}
	checks = append(checks, clock)

	listening := map[int]bool{}
	for _, p := range f.ListeningPorts {
		listening[p] = true
	}
	var busy []string
	for _, p := range req.Ports {
		if listening[p] {
			busy = append(busy, strconv.Itoa(p))
		}
	}
	ports := Check{ID: "ports", Name: "Portas obrigatórias livres", Status: Pass, Detail: "livres: " + joinInts(req.Ports)}
	if len(busy) > 0 {
		ports.Status, ports.Detail = Fail, "em uso: "+strings.Join(busy, ", ")+" (cluster já instalado? use kubeadm reset)"
	}
	checks = append(checks, ports)

	var nodePorts []string
	for _, p := range f.ListeningPorts {
		if p >= 30000 && p <= 32767 {
			nodePorts = append(nodePorts, strconv.Itoa(p))
		}
	}
	if len(nodePorts) > 0 {
		checks = append(checks, Check{ID: "nodeports", Name: "Faixa NodePort 30000-32767", Status: Warn, Detail: "em uso: " + strings.Join(nodePorts, ", ")})
	}

	swap := Check{ID: "swap", Name: "Swap", Status: Pass, Detail: "desativado"}
	if f.SwapBytes > 0 {
		swap.Status, swap.Detail = Warn, fmt.Sprintf("%s ativos; será desativado de forma persistente no bootstrap", humanBytes(f.SwapBytes))
	}
	checks = append(checks, swap)

	cg := Check{ID: "cgroup", Name: "cgroup v2 + systemd", Status: Pass, Detail: "cgroup v2 unificado, init " + f.Init}
	if !f.CgroupV2 || f.Init != "systemd" {
		cg.Status, cg.Detail = Fail, fmt.Sprintf("cgroup v2: %v, init: %s (containerd com SystemdCgroup exige ambos)", f.CgroupV2, f.Init)
	}
	checks = append(checks, cg)
	return checks
}

// Gather runs the facts script over an open client.
func Gather(ctx context.Context, cl *sshx.Client) (*Facts, error) {
	res, err := cl.Run(ctx, FactsScript, sshx.RunOptions{}, nil)
	if err != nil {
		return nil, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	return ParseFacts(res.Stdout, now)
}

func humanBytes(b uint64) string {
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	default:
		return fmt.Sprintf("%d MiB", b/mib)
	}
}

func joinInts(xs []int) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ", ")
}

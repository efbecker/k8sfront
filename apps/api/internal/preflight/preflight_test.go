package preflight

import (
	"testing"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

const sample = `hostname=cp1
kernel=6.12.48+deb13-amd64
arch=x86_64
cpus=4
mem_kb=8048000
disk_free=53687091200
swap_kb=1048576
ntp_sync=yes
cgroup2=yes
init=systemd
ports_hex=0016,1A0B,7D3A,
epoch=1761600000.500
`

func TestParseAndEvaluate(t *testing.T) {
	f, err := ParseFacts(sample, 1761600000.0)
	if err != nil {
		t.Fatal(err)
	}
	if f.CPUs != 4 || f.MemTotalBytes != 8048000*1024 || f.ClockSkewSec != 0.5 {
		t.Fatalf("bad facts: %+v", f)
	}
	// 0x0016=22, 0x1A0B=6667, 0x7D3A=32058
	if len(f.ListeningPorts) != 3 || f.ListeningPorts[0] != 22 || f.ListeningPorts[2] != 32058 {
		t.Fatalf("bad ports: %v", f.ListeningPorts)
	}
	r := &Report{}
	for _, c := range Evaluate(f, store.RoleControlPlane) {
		r.Add(c)
	}
	if r.Status != Warn { // swap active + NodePort busy
		t.Fatalf("status = %s, checks = %+v", r.Status, r.Checks)
	}
}

func TestBusyAPIServerPortFails(t *testing.T) {
	f := &Facts{Arch: "aarch64", CPUs: 2, MemTotalBytes: 4 * gib, DiskFreeBytes: 40 * gib, CgroupV2: true, Init: "systemd",
		NTPSynchronized: "yes", ListeningPorts: []int{6443}}
	r := &Report{}
	for _, c := range Evaluate(f, store.RoleControlPlane) {
		r.Add(c)
	}
	if r.Status != Fail {
		t.Fatalf("expected fail, got %s", r.Status)
	}
	// The same host is fine as a worker.
	r = &Report{}
	for _, c := range Evaluate(f, store.RoleWorker) {
		r.Add(c)
	}
	if r.Status != Pass {
		t.Fatalf("expected pass for worker, got %s: %+v", r.Status, r.Checks)
	}
}

func TestControlPlaneNeedsTwoCPUs(t *testing.T) {
	f := &Facts{Arch: "x86_64", CPUs: 1, MemTotalBytes: 4 * gib, DiskFreeBytes: 40 * gib, CgroupV2: true, Init: "systemd", NTPSynchronized: "yes"}
	for _, c := range Evaluate(f, store.RoleControlPlane) {
		if c.ID == "cpu" && c.Status != Fail {
			t.Fatal("1 vCPU must fail for control-plane")
		}
	}
}

package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const containerd17 = `version = 2
[plugins]
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = ""
    [plugins."io.containerd.grpc.v1.cri".registry.mirrors]
`

const containerd2 = `version = 3
[plugins]
  [plugins.'io.containerd.cri.v1.images'.registry]
    config_path = '/CERTS:/etc/docker/certs.d'
`

// runRegistries executes the rendered script against a temporary certs.d and
// config.toml with systemctl/crictl stubbed, returning its output and the
// stub call log.
func runRegistries(t *testing.T, dir, config string, hosts []RegistryHost) (string, string) {
	t.Helper()
	stubs := filepath.Join(dir, "bin")
	calls := filepath.Join(dir, "calls")
	os.MkdirAll(stubs, 0o755)
	for _, name := range []string{"systemctl", "crictl"} {
		os.WriteFile(filepath.Join(stubs, name), []byte("#!/bin/sh\necho \""+name+" $*\" >> "+calls+"\n"), 0o755)
	}
	body := registriesScript(hosts, filepath.Join(dir, "certs.d"), config)
	cmd := exec.Command("bash", "-c", body)
	cmd.Env = append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	c, _ := os.ReadFile(calls)
	os.Remove(calls)
	return string(out), string(c)
}

func TestRegistriesScript(t *testing.T) {
	dir := t.TempDir()
	certs := filepath.Join(dir, "certs.d")
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte(containerd17), 0o644)
	// A directory created by hand (no marker) must survive the sync.
	os.MkdirAll(filepath.Join(certs, "manual.example.com"), 0o755)

	hosts := []RegistryHost{
		{Dir: "docker.io", TOML: "server = \"https://registry-1.docker.io\"\n\n[host.\"https://cache.local:5000\"]\n  capabilities = [\"pull\", \"resolve\"]\n"},
		{Dir: "registry.local:5000", TOML: "server = \"https://registry.local:5000\"\nca = \"x\"\n", CA: "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"},
	}
	out, calls := runRegistries(t, dir, cfg, hosts)
	b, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b), "config_path = '"+certs+"'") {
		t.Fatalf("containerd 1.7 config_path not set:\n%s", b)
	}
	if !strings.Contains(calls, "systemctl restart containerd") {
		t.Fatalf("containerd must restart when config_path changes (calls: %q)\n%s", calls, out)
	}
	toml, _ := os.ReadFile(filepath.Join(certs, "docker.io", "hosts.toml"))
	if string(toml) != hosts[0].TOML {
		t.Fatalf("hosts.toml content: %q", toml)
	}
	if ca, _ := os.ReadFile(filepath.Join(certs, "registry.local:5000", "ca.crt")); string(ca) != hosts[1].CA {
		t.Fatalf("ca.crt content: %q", ca)
	}

	// Second run: nothing changes, no restart; dropping a registry removes
	// only the directory k8sfront created.
	out, calls = runRegistries(t, dir, cfg, hosts[:1])
	if strings.Contains(calls, "restart") {
		t.Fatalf("unexpected restart on an unchanged config: %q", calls)
	}
	if !strings.Contains(out, "sem mudança") {
		t.Fatalf("unchanged hosts.toml should be reported:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(certs, "registry.local:5000")); !os.IsNotExist(err) {
		t.Fatal("stale managed registry directory must be removed")
	}
	if _, err := os.Stat(filepath.Join(certs, "manual.example.com")); err != nil {
		t.Fatal("directories not created by k8sfront must be kept")
	}

	// containerd 2.x already reads certs.d: no edit, no restart.
	cfg2 := filepath.Join(dir, "config2.toml")
	os.WriteFile(cfg2, []byte(strings.ReplaceAll(containerd2, "/CERTS", certs)), 0o644)
	_, calls = runRegistries(t, dir, cfg2, nil)
	if strings.Contains(calls, "restart") {
		t.Fatalf("containerd 2.x config must be left alone: %q", calls)
	}
	if _, err := os.Stat(filepath.Join(certs, "docker.io")); !os.IsNotExist(err) {
		t.Fatal("an empty registry list removes every managed directory")
	}
}

func TestContainerdScriptEnablesCertsDir(t *testing.T) {
	s := ContainerdScript()
	if !strings.Contains(s, "config_path = '/etc/containerd/certs.d'") || !strings.Contains(s, "install -d -m 0755 /etc/containerd/certs.d") {
		t.Fatal("bootstrap containerd must read registry hosts from /etc/containerd/certs.d")
	}
}

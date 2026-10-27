//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/efbecker/k8sfront/apps/api/internal/api"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/remote"
	"github.com/efbecker/k8sfront/apps/api/internal/secrets"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/store/sqlite"
	"github.com/efbecker/k8sfront/apps/api/internal/vault"
)

const debianImage = "mirror.gcr.io/library/debian:trixie"

type platform struct {
	URL    string
	client *http.Client
	db     *sqlite.DB
}

func startPlatform(t *testing.T) *platform {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	key, err := vault.LoadKey("", filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := vault.New(key)
	db, err := sqlite.Open(ctx, filepath.Join(dir, "k8sfront.db"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sec := secrets.NewManager(db, v)
	conn := &remote.Connector{Store: db, Secrets: sec, Dialer: sshx.NewDialer(db)}
	engine := jobs.New(db, jobs.NewHub(db, logger), logger, 2)
	(&ops.Ops{Store: db, Connector: conn}).Register(engine)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := &api.Server{Store: db, Secrets: sec, Engine: engine, Connector: conn, Log: logger,
		Auth: api.NewAuth("integration-admin-token", key, time.Hour, false)}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		engine.Shutdown(sctx)
		db.Close()
	})
	jar, _ := cookiejar.New(nil)
	p := &platform{URL: ts.URL, client: &http.Client{Jar: jar}, db: db}
	p.do(t, "POST", "/api/v1/auth/login", map[string]string{"token": "integration-admin-token"}, nil, 200)
	return p
}

func (p *platform) do(t *testing.T, method, path string, body, out any, want int) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, p.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
}

type streamMsg struct {
	Type string         `json:"type"`
	Job  *store.Job     `json:"job"`
	Log  *store.LogLine `json:"log"`
}

// follow streams a job until it reaches a final state and returns its lines.
func (p *platform) follow(t *testing.T, jobID string, after int64) (*store.Job, []store.LogLine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	u := strings.Replace(p.URL, "http", "ws", 1) + "/api/v1/jobs/" + jobID + "/stream?after=" + itoa(after)
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: p.client})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	var logs []store.LogLine
	var last *store.Job
	ready := false
	done := func() bool {
		return ready && last != nil && (last.Status == store.JobSucceeded || last.Status == store.JobFailed || last.Status == store.JobPaused)
	}
	for {
		var m streamMsg
		if err := wsjson.Read(ctx, c, &m); err != nil {
			t.Fatalf("stream read: %v (lines so far: %d)", err, len(logs))
		}
		switch m.Type {
		case "log":
			logs = append(logs, *m.Log)
		case "job":
			last = m.Job
		case "ready":
			ready = true
		}
		// The engine flushes logs before publishing a final snapshot, so once
		// the backlog is replayed and the job is final, nothing is missing.
		if done() {
			return last, logs
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func startDebian(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if err := exec.Command("docker", "image", "inspect", debianImage).Run(); err != nil {
		t.Skipf("image %s not present (docker pull %s)", debianImage, debianImage)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", debianImage, "sleep", "infinity").Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	return id
}

func TestEndToEndPreflightOnDebian13(t *testing.T) {
	container := startDebian(t)
	sshSrv := &testSSHServer{Users: map[string]string{"root": "Debian-R00t"}, Backend: dockerBackend(container), HostKey: newHostKey(t)}
	sshSrv.Start(t)
	host, port := splitAddr(t, sshSrv.Addr)

	p := startPlatform(t)
	var srv struct {
		ID          string `json:"id"`
		HasPassword bool   `json:"hasPassword"`
		Password    string `json:"password"`
	}
	p.do(t, "POST", "/api/v1/servers", map[string]any{"name": "trixie-cp1", "host": host, "port": port, "sshUser": "root",
		"authMethod": "password", "password": "Debian-R00t", "role": "control-plane"}, &srv, 201)
	if !srv.HasPassword || srv.Password != "" {
		t.Fatalf("API must expose only presence flags: %+v", srv)
	}
	var job store.Job
	p.do(t, "POST", "/api/v1/servers/"+srv.ID+"/preflight", nil, &job, 202)
	final, logs := p.follow(t, job.ID, 0)

	var full store.Job
	p.do(t, "GET", "/api/v1/jobs/"+job.ID, nil, &full, 200)
	var res ops.PreflightResult
	if err := json.Unmarshal(full.Result, &res); err != nil {
		t.Fatalf("result: %v (%s)", err, full.Result)
	}
	rep := res.Reports[srv.ID]
	if rep == nil || rep.OS == nil || rep.OS.Major < 13 {
		t.Fatalf("Debian 13 not detected: %+v", rep)
	}
	if rep.Sudo == nil || rep.Sudo.Mode != store.SudoRoot {
		t.Fatalf("sudo: %+v", rep.Sudo)
	}
	t.Logf("job %s finished as %s with report status %s", job.ID, final.Status, rep.Status)
	for _, c := range rep.Checks {
		t.Logf("  [%s] %s: %s", c.Status, c.Name, c.Detail)
	}
	joined := ""
	for i, l := range logs {
		if l.Seq != int64(i+1) {
			t.Fatalf("stream seq gap: line %d has seq %d", i, l.Seq)
		}
		joined += l.Line + "\n"
	}
	if strings.Contains(joined, "Debian-R00t") {
		t.Fatal("password leaked into the job log")
	}

	// A reconnecting client resumes after its last seq without duplicates.
	_, resumed := p.follow(t, job.ID, int64(len(logs)-3))
	if len(resumed) != 3 || resumed[0].Seq != int64(len(logs)-2) {
		t.Fatalf("resume returned %d lines starting at %v", len(resumed), resumed)
	}

	var stored store.Server
	p.do(t, "GET", "/api/v1/servers/"+srv.ID, nil, &stored, 200)
	if stored.OSVersionID != "13" || stored.SudoMode != store.SudoRoot || stored.LastCheckStatus == "" {
		t.Fatalf("server not updated: %+v", stored)
	}
}

func TestEndToEndRejectsNonDebian(t *testing.T) {
	requireLocalSudo(t) // this machine is the "remote" host
	out, _ := exec.Command("sh", "-c", ". /etc/os-release; echo $ID").Output()
	if strings.TrimSpace(string(out)) == "debian" {
		t.Skip("local host is Debian")
	}
	sshSrv, _ := localServer(t)
	host, port := splitAddr(t, sshSrv.Addr)
	p := startPlatform(t)
	var srv struct{ ID string }
	p.do(t, "POST", "/api/v1/servers", map[string]any{"host": host, "port": port, "sshUser": "withpw",
		"authMethod": "password", "password": sudoPW}, &srv, 201)
	var job store.Job
	p.do(t, "POST", "/api/v1/servers/"+srv.ID+"/preflight", nil, &job, 202)
	final, logs := p.follow(t, job.ID, 0)
	if final.Status != store.JobFailed {
		t.Fatalf("status = %s", final.Status)
	}
	var sawOS bool
	for _, l := range logs {
		if strings.Contains(l.Line, "unsupported operating system") {
			sawOS = true
		}
		if strings.Contains(l.Line, "sudo") && strings.Contains(l.Line, "▶") {
			t.Fatalf("sudo must not be attempted on a non-Debian host: %q", l.Line)
		}
	}
	if !sawOS {
		t.Fatal("expected a clear unsupported OS error in the log")
	}
}

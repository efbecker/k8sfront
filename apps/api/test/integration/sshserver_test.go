//go:build integration

package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os/exec"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testSSHServer is a minimal SSH server for integration tests. Commands run
// for real through Backend (runuser on this machine, or docker exec into a
// Debian 13 container), so sudo, os-release and shell behaviour are genuine;
// only the sshd daemon is replaced.
type testSSHServer struct {
	Addr     string
	Users    map[string]string // user -> password ("" disables password auth)
	AuthKeys map[string]ssh.PublicKey
	Backend  func(user, cmd string) *exec.Cmd
	HostKey  ssh.Signer
	ln       net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	Commands []string // argv-level command lines received (what `ps` would show)
}

func newHostKey(t *testing.T) ssh.Signer {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *testSSHServer) Start(t *testing.T) {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if want, ok := s.Users[c.User()]; ok && want != "" && want == string(pw) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if k, ok := s.AuthKeys[c.User()]; ok && string(k.Marshal()) == string(key.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(s.HostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	s.Addr = ln.Addr().String()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(nc, cfg)
		}
	}()
	t.Cleanup(func() { ln.Close(); s.wg.Wait() })
}

func (s *testSSHServer) handle(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go s.session(conn.User(), ch, creqs)
	}
}

func (s *testSSHServer) session(user string, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			req.Reply(req.Type == "env", nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			req.Reply(false, nil)
			return
		}
		req.Reply(true, nil)
		s.mu.Lock()
		s.Commands = append(s.Commands, payload.Command)
		s.mu.Unlock()

		cmd := s.Backend(user, payload.Command)
		stdin, _ := cmd.StdinPipe()
		cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
		code := 0
		if err := cmd.Start(); err != nil {
			code = 127
		} else {
			go func() { io.Copy(stdin, ch); stdin.Close() }()
			if err := cmd.Wait(); err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					code = ee.ExitCode()
				} else {
					code = 1
				}
			}
		}
		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, uint32(code))
		ch.SendRequest("exit-status", false, status)
		return
	}
}

// runuserBackend runs commands as a local user, like sshd would.
func runuserBackend(user, cmd string) *exec.Cmd {
	if user == "root" {
		return exec.Command("/bin/bash", "-c", cmd)
	}
	return exec.Command("runuser", "-u", user, "--", "/bin/bash", "-c", cmd)
}

// dockerBackend runs commands inside a container.
func dockerBackend(container string) func(user, cmd string) *exec.Cmd {
	return func(user, cmd string) *exec.Cmd {
		return exec.Command("docker", "exec", "-i", "-u", user, container, "/bin/bash", "-c", cmd)
	}
}

package sshx

import (
	"context"
	"fmt"
	"strings"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// SudoReport describes the privilege escalation detection.
type SudoReport struct {
	Mode    store.SudoMode `json:"mode"`
	UID     string         `json:"uid"`
	Message string         `json:"message"`
}

// DetectSudo determines how to become root and stores it on the client:
//
//  1. uid 0                    -> root (no sudo)
//  2. `sudo -n true` succeeds  -> NOPASSWD
//  3. password configured and `sudo -k -S -p ” -v` accepts it via stdin -> password
//
// The sudo password is only ever written to stdin; it is never part of argv.
func (c *Client) DetectSudo(ctx context.Context) (*SudoReport, error) {
	uid, _, code, err := c.runRaw(ctx, "id -u", nil)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("`id -u` failed with status %d", code)
	}
	uid = strings.TrimSpace(uid)
	if uid == "0" {
		c.SetSudoMode(store.SudoRoot)
		return &SudoReport{Mode: store.SudoRoot, UID: uid, Message: "connected as root"}, nil
	}
	if _, _, code, err := c.runRaw(ctx, "command -v sudo", nil); err != nil {
		return nil, err
	} else if code != 0 {
		return nil, fmt.Errorf("%w: user %q is not root and sudo is not installed (apt-get install sudo)", ErrSudoAuth, c.target.Creds.User)
	}
	if _, _, code, err := c.runRaw(ctx, "sudo -n true", []byte{}); err != nil {
		return nil, err
	} else if code == 0 {
		c.SetSudoMode(store.SudoNoPasswd)
		return &SudoReport{Mode: store.SudoNoPasswd, UID: uid, Message: "sudo NOPASSWD available"}, nil
	}
	if len(c.target.Creds.SudoPassword) == 0 {
		return nil, fmt.Errorf("%w: sudo for %q requires a password and none is configured", ErrSudoAuth, c.target.Creds.User)
	}
	in := append(append([]byte{}, c.target.Creds.SudoPassword...), '\n')
	_, stderr, code, err := c.runRaw(ctx, "sudo -k -S -p '' -v", in)
	for i := range in {
		in[i] = 0
	}
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%w: sudo rejected the configured password for %q: %s", ErrSudoAuth, c.target.Creds.User, firstLine(strings.TrimSpace(stderr)))
	}
	c.SetSudoMode(store.SudoPassword)
	return &SudoReport{Mode: store.SudoPassword, UID: uid, Message: "sudo with password (injected via stdin)"}, nil
}

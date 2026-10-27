package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/vault"
	"github.com/google/uuid"
)

// serverView is the API representation; secrets are reduced to presence flags.
type serverView struct {
	store.Server
	HasPrivateKey   bool `json:"hasPrivateKey"`
	HasPassphrase   bool `json:"hasPassphrase"`
	HasPassword     bool `json:"hasPassword"`
	HasSudoPassword bool `json:"hasSudoPassword"`
}

func view(s *store.Server) serverView {
	return serverView{
		Server:          *s,
		HasPrivateKey:   s.AuthMethod == store.AuthKey && s.CredentialSecretID != nil,
		HasPassphrase:   s.PassphraseSecretID != nil,
		HasPassword:     s.AuthMethod == store.AuthPassword && s.CredentialSecretID != nil,
		HasSudoPassword: s.SudoSecretID != nil,
	}
}

// serverInput is used for create (all required fields) and update (pointers =
// optional). Secret fields are write-only.
type serverInput struct {
	Name              *string `json:"name"`
	Host              *string `json:"host"`
	Port              *int    `json:"port"`
	SSHUser           *string `json:"sshUser"`
	AuthMethod        *string `json:"authMethod"`
	Role              *string `json:"role"`
	PrivateKey        *string `json:"privateKey"`
	Passphrase        *string `json:"passphrase"`
	Password          *string `json:"password"`
	SudoPassword      *string `json:"sudoPassword"`
	ClearSudoPassword bool    `json:"clearSudoPassword"`
}

var (
	userRe     = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}\$?$`)
	hostnameRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
)

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func invalid(format string, args ...any) error { return &validationError{fmt.Sprintf(format, args...)} }

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// apply validates in and applies it to srv. Secrets are encrypted and stored;
// replaced secret IDs are returned for deletion after the row is saved.
func (s *Server) apply(ctx context.Context, srv *store.Server, in *serverInput, creating bool) (obsolete []string, err error) {
	if in.Name != nil {
		srv.Name = strings.TrimSpace(*in.Name)
	}
	if in.Host != nil {
		srv.Host = strings.TrimSpace(*in.Host)
	}
	if in.Port != nil {
		srv.Port = *in.Port
	}
	if in.SSHUser != nil {
		srv.SSHUser = strings.TrimSpace(*in.SSHUser)
	}
	if in.Role != nil {
		srv.Role = store.NodeRole(*in.Role)
	}
	authChanged := false
	if in.AuthMethod != nil && store.AuthMethod(*in.AuthMethod) != srv.AuthMethod {
		srv.AuthMethod = store.AuthMethod(*in.AuthMethod)
		authChanged = true
	}
	if srv.Port == 0 {
		srv.Port = 22
	}
	if srv.Name == "" {
		srv.Name = srv.Host
	}

	switch {
	case srv.Host == "":
		return nil, invalid("host is required")
	case net.ParseIP(srv.Host) == nil && !hostnameRe.MatchString(srv.Host):
		return nil, invalid("host %q is not a valid IP address or hostname", srv.Host)
	case srv.Port < 1 || srv.Port > 65535:
		return nil, invalid("port must be between 1 and 65535")
	case !userRe.MatchString(srv.SSHUser):
		return nil, invalid("sshUser %q is not a valid Linux user name", srv.SSHUser)
	case len(srv.Name) > 128:
		return nil, invalid("name is too long")
	}
	switch srv.Role {
	case store.RoleNone, store.RoleControlPlane, store.RoleWorker:
	default:
		return nil, invalid("role must be control-plane, worker or empty")
	}

	replaceSecret := func(cur **string, kind store.SecretKind, value []byte) error {
		id, err := s.Secrets.Put(ctx, kind, value)
		vault.Wipe(value)
		if err != nil {
			return err
		}
		if *cur != nil {
			obsolete = append(obsolete, **cur)
		}
		*cur = &id
		return nil
	}

	switch srv.AuthMethod {
	case store.AuthKey:
		key := []byte(str(in.PrivateKey))
		pass := []byte(str(in.Passphrase))
		if len(key) == 0 && (creating || authChanged) {
			return nil, invalid("privateKey is required for key authentication")
		}
		if len(key) > 0 {
			if !strings.HasSuffix(string(key), "\n") {
				key = append(key, '\n')
			}
			if _, err := sshx.ValidatePrivateKey(key, pass); err != nil {
				return nil, invalid("%v", err)
			}
			if err := replaceSecret(&srv.CredentialSecretID, store.SecretSSHPrivateKey, key); err != nil {
				return nil, err
			}
			if srv.PassphraseSecretID != nil {
				obsolete = append(obsolete, *srv.PassphraseSecretID)
				srv.PassphraseSecretID = nil
			}
			if len(pass) > 0 {
				if err := replaceSecret(&srv.PassphraseSecretID, store.SecretSSHPassphrase, pass); err != nil {
					return nil, err
				}
			}
		}
	case store.AuthPassword:
		pw := []byte(str(in.Password))
		if len(pw) == 0 && (creating || authChanged) {
			return nil, invalid("password is required for password authentication")
		}
		if len(pw) > 0 {
			if err := replaceSecret(&srv.CredentialSecretID, store.SecretSSHPassword, pw); err != nil {
				return nil, err
			}
		}
		if srv.PassphraseSecretID != nil {
			obsolete = append(obsolete, *srv.PassphraseSecretID)
			srv.PassphraseSecretID = nil
		}
	default:
		return nil, invalid("authMethod must be \"key\" or \"password\"")
	}

	if in.ClearSudoPassword && srv.SudoSecretID != nil {
		obsolete = append(obsolete, *srv.SudoSecretID)
		srv.SudoSecretID = nil
	}
	if sp := str(in.SudoPassword); sp != "" {
		if err := replaceSecret(&srv.SudoSecretID, store.SecretSudoPassword, []byte(sp)); err != nil {
			return nil, err
		}
	}
	// Any change to how we connect invalidates the detected sudo mode.
	if in.SSHUser != nil || in.SudoPassword != nil || in.ClearSudoPassword || authChanged {
		srv.SudoMode = store.SudoUnknown
	}
	return obsolete, nil
}

func (s *Server) respondApplyError(w http.ResponseWriter, err error) {
	var ve *validationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, "validation", ve.msg)
		return
	}
	s.storeError(w, err)
}

func (s *Server) listServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListServers(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	out := make([]serverView, len(list))
	for i := range list {
		out[i] = view(&list[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getServer(w http.ResponseWriter, r *http.Request) {
	srv, err := s.Store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view(srv))
}

func (s *Server) createServer(w http.ResponseWriter, r *http.Request) {
	var in serverInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	srv := &store.Server{ID: uuid.NewString(), SudoMode: store.SudoUnknown}
	if in.AuthMethod == nil {
		writeError(w, http.StatusBadRequest, "validation", "authMethod is required")
		return
	}
	if _, err := s.apply(r.Context(), srv, &in, true); err != nil {
		s.cleanupSecrets(srv)
		s.respondApplyError(w, err)
		return
	}
	if err := s.Store.CreateServer(r.Context(), srv); err != nil {
		s.cleanupSecrets(srv)
		s.storeError(w, err)
		return
	}
	s.audit(r, "server.create", "server", srv.ID, map[string]any{"host": srv.Host, "port": srv.Port, "user": srv.SSHUser, "role": srv.Role})
	writeJSON(w, http.StatusCreated, view(srv))
}

func (s *Server) cleanupSecrets(srv *store.Server) {
	for _, id := range []*string{srv.CredentialSecretID, srv.PassphraseSecretID, srv.SudoSecretID} {
		if id != nil {
			_ = s.Secrets.Delete(context.Background(), *id)
		}
	}
}

func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	var in serverInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	srv, err := s.Store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	obsolete, err := s.apply(r.Context(), srv, &in, false)
	if err != nil {
		s.respondApplyError(w, err)
		return
	}
	if err := s.Store.UpdateServer(r.Context(), srv); err != nil {
		s.storeError(w, err)
		return
	}
	for _, id := range obsolete {
		_ = s.Secrets.Delete(r.Context(), id)
	}
	s.audit(r, "server.update", "server", srv.ID, map[string]any{"credentialsChanged": len(obsolete) > 0 || in.PrivateKey != nil || in.Password != nil || in.SudoPassword != nil})
	writeJSON(w, http.StatusOK, view(srv))
}

func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.DeleteServer(r.Context(), id); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "server.delete", "server", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func hostPort(srv *store.Server) string { return sshx.Target{Host: srv.Host, Port: srv.Port}.HostPort() }

func (s *Server) getHostKey(w http.ResponseWriter, r *http.Request) {
	srv, err := s.Store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	kh, err := s.Store.GetKnownHost(r.Context(), hostPort(srv))
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, kh)
}

// resetHostKey forgets the trusted key, e.g. after a host was reinstalled. The
// next connection registers the new key (TOFU).
func (s *Server) resetHostKey(w http.ResponseWriter, r *http.Request) {
	srv, err := s.Store.GetServer(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	if err := s.Store.DeleteKnownHost(r.Context(), hostPort(srv)); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "server.host_key_reset", "server", srv.ID, map[string]string{"hostPort": hostPort(srv)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) preflightOne(w http.ResponseWriter, r *http.Request) {
	s.submitPreflight(w, r, []string{r.PathValue("id")})
}

func (s *Server) preflightMany(w http.ResponseWriter, r *http.Request) {
	var in ops.PreflightParams
	if err := decodeJSON(r, &in); err != nil || len(in.ServerIDs) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "body must be {\"serverIds\": [...]}")
		return
	}
	s.submitPreflight(w, r, in.ServerIDs)
}

func (s *Server) submitPreflight(w http.ResponseWriter, r *http.Request, ids []string) {
	seen := map[string]bool{}
	uniq := ids[:0]
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := s.Store.GetServer(r.Context(), id); err != nil {
			s.storeError(w, err)
			return
		}
		uniq = append(uniq, id)
	}
	targetType, targetID := "servers", ""
	if len(uniq) == 1 {
		targetType, targetID = "server", uniq[0]
	}
	job, err := s.Engine.Submit(r.Context(), ops.KindPreflight, targetType, targetID, ops.PreflightParams{ServerIDs: uniq}, "admin")
	if err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "server.preflight", targetType, targetID, map[string]any{"jobId": job.ID, "serverIds": uniq})
	writeJSON(w, http.StatusAccepted, job)
}

package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/ldap"
	"github.com/efbecker/k8sfront/apps/api/internal/passwd"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/google/uuid"
)

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._@-]{0,63}$`)

func normalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// meView is what the SPA needs about the caller.
type meView struct {
	Principal
	User    string `json:"user"` // kept for older clients
	Version string `json:"version"`
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, meView{Principal: *p, User: p.Username, Version: Version})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

// authProviders tells the login page which forms to show (public).
func (s *Server) authProviders(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.CountUsers(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	cfg, err := s.loadLDAP(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": n > 0, "ldap": cfg.Enabled, "ldapKind": cfg.Kind})
}

// loginError is a failed authentication: status and message for the client,
// reason for the audit log only.
type loginError struct {
	status  int
	message string
	reason  string
}

func (e *loginError) Error() string { return e.reason }

var errBadLogin = &loginError{status: http.StatusUnauthorized, message: "usuário ou senha inválidos", reason: "bad_credentials"}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Token != "" || req.Username == "" {
		if !s.Auth.CheckToken(req.Token) {
			time.Sleep(500 * time.Millisecond) // slow down guessing
			s.auditAs(r, "admin (token)", "auth.login_failed", "", "", map[string]string{"method": "token"})
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid admin token")
			return
		}
		s.Auth.Issue(w, "", 0)
		s.auditAs(r, "admin (token)", "auth.login", "", "", map[string]string{"method": "token"})
		p := tokenPrincipal
		writeJSON(w, http.StatusOK, meView{Principal: p, User: p.Username, Version: Version})
		return
	}
	username := normalizeUsername(req.Username)
	u, err := s.authenticate(r.Context(), username, req.Password)
	if err != nil {
		var le *loginError
		if !errors.As(err, &le) {
			s.storeError(w, err)
			return
		}
		time.Sleep(500 * time.Millisecond)
		s.auditAs(r, username, "auth.login_failed", "", "", map[string]string{"reason": le.reason})
		writeError(w, le.status, "unauthorized", le.message)
		return
	}
	s.Auth.Issue(w, u.ID, u.SessionEpoch)
	s.auditAs(r, u.Username, "auth.login", "user", u.ID, map[string]any{"method": u.Source, "role": u.Role})
	p := Principal{UserID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role, Source: u.Source}
	writeJSON(w, http.StatusOK, meView{Principal: p, User: p.Username, Version: Version})
}

// authenticate tries the local account first, then LDAP/AD. A local account
// always wins over a directory account with the same name.
func (s *Server) authenticate(ctx context.Context, username, password string) (*store.User, error) {
	if username == "" || password == "" || len(password) > 1024 {
		return nil, errBadLogin
	}
	u, err := s.Store.GetUserByUsername(ctx, username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if u != nil && u.Source == store.UserLocal {
		if !passwd.Verify(u.PasswordHash, password) {
			return nil, errBadLogin
		}
		if u.Disabled {
			return nil, &loginError{status: http.StatusUnauthorized, message: errBadLogin.message, reason: "disabled"}
		}
		now := time.Now()
		u.LastLoginAt = &now
		if err := s.Store.UpdateUser(ctx, u); err != nil {
			return nil, err
		}
		return u, nil
	}
	cfg, err := s.loadLDAP(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		passwd.Burn(password)
		return nil, errBadLogin
	}
	bindPw, err := s.ldapBindPassword(ctx, cfg)
	if err != nil {
		return nil, err
	}
	lctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*2*time.Second)
	defer cancel()
	id, err := ldap.Authenticate(lctx, cfg, bindPw, username, password)
	if errors.Is(err, ldap.ErrBadCredentials) {
		return nil, errBadLogin
	}
	if err != nil {
		s.Log.Warn("ldap login failed", "err", err)
		return nil, &loginError{status: http.StatusBadGateway, reason: "directory_error",
			message: "não foi possível validar no LDAP/AD agora; tente de novo ou entre com uma conta local"}
	}
	if id.Role == "" {
		return nil, &loginError{status: http.StatusForbidden, reason: "no_mapped_group",
			message: "seu usuário não pertence a nenhum grupo com acesso ao k8sfront; fale com um administrador"}
	}
	// The directory may canonicalize the name (UPN → sAMAccountName).
	if id.Username != username || u == nil {
		u, err = s.Store.GetUserByUsername(ctx, id.Username)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	now := time.Now()
	if u == nil {
		u = &store.User{ID: uuid.NewString(), Username: id.Username, Source: store.UserLDAP}
	} else if u.Source != store.UserLDAP {
		return nil, &loginError{status: http.StatusUnauthorized, message: errBadLogin.message, reason: "local_account_conflict"}
	} else if u.Disabled {
		return nil, &loginError{status: http.StatusUnauthorized, message: errBadLogin.message, reason: "disabled"}
	}
	u.DisplayName, u.Email, u.ExternalDN, u.Groups, u.Role, u.LastLoginAt = id.DisplayName, id.Email, id.DN, id.Groups, id.Role, &now
	if u.CreatedAt.IsZero() {
		err = s.Store.CreateUser(ctx, u)
	} else {
		err = s.Store.UpdateUser(ctx, u)
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// --- user management ----------------------------------------------------------

type userView struct {
	store.User
	HasPassword bool `json:"hasPassword"`
}

func viewUser(u *store.User) userView {
	return userView{User: *u, HasPassword: u.PasswordHash != ""}
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	out := make([]userView, 0, len(list))
	for i := range list {
		out = append(out, viewUser(&list[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

type userInput struct {
	Username    *string     `json:"username"`
	DisplayName *string     `json:"displayName"`
	Email       *string     `json:"email"`
	Role        *store.Role `json:"role"`
	Password    *string     `json:"password"`
	Disabled    *bool       `json:"disabled"`
}

func validation(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, "validation", msg)
}

func cleanText(v *string, max int) (string, bool) {
	t := strings.TrimSpace(*v)
	return t, len(t) <= max && !strings.ContainsAny(t, "\r\n\x00")
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in userInput
	if err := decodeJSON(r, &in); err != nil {
		validation(w, "JSON inválido")
		return
	}
	u := &store.User{ID: uuid.NewString(), Source: store.UserLocal, Role: store.RoleViewer}
	if in.Username == nil || !usernameRe.MatchString(normalizeUsername(*in.Username)) {
		validation(w, "usuário deve ter de 1 a 64 caracteres: letras minúsculas, números, ponto, hífen, _ ou @")
		return
	}
	u.Username = normalizeUsername(*in.Username)
	if !s.applyUserInput(w, u, &in, true) {
		return
	}
	if in.Password == nil {
		validation(w, "defina uma senha inicial")
		return
	}
	if err := s.Store.CreateUser(r.Context(), u); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "user.create", "user", u.ID, map[string]any{"username": u.Username, "role": u.Role, "disabled": u.Disabled})
	writeJSON(w, http.StatusCreated, viewUser(u))
}

// applyUserInput validates and copies the editable fields.
func (s *Server) applyUserInput(w http.ResponseWriter, u *store.User, in *userInput, creating bool) bool {
	if in.Username != nil && !creating && normalizeUsername(*in.Username) != u.Username {
		validation(w, "o nome de usuário não pode ser alterado; crie outro usuário")
		return false
	}
	if in.DisplayName != nil {
		v, ok := cleanText(in.DisplayName, 128)
		if !ok {
			validation(w, "nome inválido")
			return false
		}
		u.DisplayName = v
	}
	if in.Email != nil {
		v, ok := cleanText(in.Email, 254)
		if !ok || (v != "" && !strings.Contains(v, "@")) {
			validation(w, "e-mail inválido")
			return false
		}
		u.Email = v
	}
	if in.Role != nil {
		if !in.Role.Valid() {
			validation(w, "papel deve ser admin, operator ou viewer")
			return false
		}
		if u.Source == store.UserLDAP && *in.Role != u.Role {
			validation(w, "o papel de usuários LDAP/AD vem do mapeamento de grupos e é atualizado a cada login")
			return false
		}
		u.Role = *in.Role
	}
	if in.Password != nil {
		if u.Source != store.UserLocal {
			validation(w, "a senha de usuários LDAP/AD é gerenciada no diretório")
			return false
		}
		if err := passwd.Validate(*in.Password); err != nil {
			validation(w, err.Error())
			return false
		}
		h, err := passwd.Hash(*in.Password)
		if err != nil {
			s.storeError(w, err)
			return false
		}
		u.PasswordHash = h
		u.SessionEpoch++ // sign out every session of the user
	}
	if in.Disabled != nil {
		if *in.Disabled && !u.Disabled {
			u.SessionEpoch++
		}
		u.Disabled = *in.Disabled
	}
	return true
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	var in userInput
	if err := decodeJSON(r, &in); err != nil {
		validation(w, "JSON inválido")
		return
	}
	u, err := s.Store.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	before := *u
	if p := principalFrom(r.Context()); p.UserID == u.ID {
		if (in.Role != nil && *in.Role != u.Role) || (in.Disabled != nil && *in.Disabled) {
			validation(w, "você não pode mudar o próprio papel nem desativar a própria conta; peça a outro administrador")
			return
		}
	}
	if !s.applyUserInput(w, u, &in, false) {
		return
	}
	if err := s.Store.UpdateUser(r.Context(), u); err != nil {
		s.storeError(w, err)
		return
	}
	details := map[string]any{"username": u.Username, "passwordChanged": in.Password != nil}
	if before.Role != u.Role {
		details["role"] = map[string]store.Role{"from": before.Role, "to": u.Role}
	}
	if before.Disabled != u.Disabled {
		details["disabled"] = u.Disabled
	}
	if before.DisplayName != u.DisplayName {
		details["displayName"] = u.DisplayName
	}
	if before.Email != u.Email {
		details["email"] = u.Email
	}
	s.audit(r, "user.update", "user", u.ID, details)
	writeJSON(w, http.StatusOK, viewUser(u))
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if principalFrom(r.Context()).UserID == id {
		validation(w, "você não pode excluir a própria conta")
		return
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "user.delete", "user", id, map[string]any{"username": u.Username, "source": u.Source, "role": u.Role})
	w.WriteHeader(http.StatusNoContent)
}

// changeOwnPassword lets a local user change their own password.
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &in); err != nil {
		validation(w, "JSON inválido")
		return
	}
	p := principalFrom(r.Context())
	if p.Source != store.UserLocal {
		validation(w, "só contas locais trocam a senha aqui")
		return
	}
	u, err := s.Store.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !passwd.Verify(u.PasswordHash, in.CurrentPassword) {
		time.Sleep(500 * time.Millisecond)
		s.audit(r, "auth.password_change_failed", "user", u.ID, nil)
		validation(w, "senha atual incorreta")
		return
	}
	if !s.applyUserInput(w, u, &userInput{Password: &in.NewPassword}, false) {
		return
	}
	if err := s.Store.UpdateUser(r.Context(), u); err != nil {
		s.storeError(w, err)
		return
	}
	s.Auth.Issue(w, u.ID, u.SessionEpoch) // keep this session, other ones end
	s.audit(r, "auth.password_change", "user", u.ID, map[string]string{"username": u.Username})
	w.WriteHeader(http.StatusNoContent)
}

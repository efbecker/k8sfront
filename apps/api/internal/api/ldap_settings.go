package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/ldap"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/vault"
)

const ldapSettingKey = "ldap"

// loadLDAP returns the saved configuration (disabled AD defaults when none).
func (s *Server) loadLDAP(ctx context.Context) (*ldap.Config, error) {
	raw, err := s.Store.GetSetting(ctx, ldapSettingKey)
	if errors.Is(err, store.ErrNotFound) {
		c := ldap.Defaults(ldap.KindAD)
		c.Security, c.Port, c.TimeoutSeconds, c.GroupMappings = ldap.SecurityLDAPS, 636, 10, []ldap.GroupMapping{}
		return &c, nil
	}
	if err != nil {
		return nil, err
	}
	var c ldap.Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.GroupMappings == nil {
		c.GroupMappings = []ldap.GroupMapping{}
	}
	return &c, nil
}

func (s *Server) ldapBindPassword(ctx context.Context, c *ldap.Config) (string, error) {
	if c.BindPasswordSecretID == "" {
		return "", nil
	}
	b, err := s.Secrets.Get(ctx, c.BindPasswordSecretID)
	if err != nil {
		return "", err
	}
	defer vault.Wipe(b)
	return string(b), nil
}

// ldapView never carries the bind password, only HasBindPassword.
type ldapView struct {
	ldap.Config
	BindPasswordSecretID string                 `json:"bindPasswordSecretId,omitempty"` // shadowed: never sent
	HasBindPassword      bool                   `json:"hasBindPassword"`
	Defaults             map[string]ldap.Config `json:"defaults"`
}

func viewLDAP(c *ldap.Config) ldapView {
	defs := map[string]ldap.Config{}
	for _, k := range []string{ldap.KindAD, ldap.KindLDAP} {
		defs[k] = ldap.Defaults(k)
	}
	return ldapView{Config: *c, HasBindPassword: c.BindPasswordSecretID != "", Defaults: defs}
}

type ldapInput struct {
	ldap.Config
	BindPassword      *string `json:"bindPassword"`
	ClearBindPassword bool    `json:"clearBindPassword"`
}

func (s *Server) getLDAP(w http.ResponseWriter, r *http.Request) {
	c, err := s.loadLDAP(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewLDAP(c))
}

func ldapAudit(c *ldap.Config, passwordChanged bool) map[string]any {
	return map[string]any{"enabled": c.Enabled, "kind": c.Kind, "host": c.Host, "port": c.Port, "security": c.Security,
		"skipVerify": c.SkipVerify, "caCert": c.CACert != "", "bindDn": c.BindDN, "bindPasswordChanged": passwordChanged,
		"baseDn": c.BaseDN, "userFilter": c.UserFilter, "groupBaseDn": c.GroupBaseDN, "groupFilter": c.GroupFilter,
		"groupMappings": c.GroupMappings, "defaultRole": c.DefaultRole}
}

func checkBindPassword(pw *string) error {
	if pw != nil && (strings.ContainsAny(*pw, "\r\n\x00") || len(*pw) > 1024) {
		return errors.New("senha da conta de serviço inválida")
	}
	return nil
}

func (s *Server) putLDAP(w http.ResponseWriter, r *http.Request) {
	var in ldapInput
	if err := decodeJSON(r, &in); err != nil {
		validation(w, "JSON inválido")
		return
	}
	cur, err := s.loadLDAP(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	c := in.Config
	c.BindPasswordSecretID = cur.BindPasswordSecretID // never taken from the client
	if err := ldap.Normalize(&c); err != nil {
		validation(w, ldap.ValidationMessage(err))
		return
	}
	if err := checkBindPassword(in.BindPassword); err != nil {
		validation(w, err.Error())
		return
	}
	changed := false
	old := c.BindPasswordSecretID
	if in.BindPassword != nil && *in.BindPassword != "" {
		id, err := s.Secrets.Put(r.Context(), store.SecretLDAPBind, []byte(*in.BindPassword))
		if err != nil {
			s.storeError(w, err)
			return
		}
		c.BindPasswordSecretID, changed = id, true
	} else if in.ClearBindPassword {
		c.BindPasswordSecretID, changed = "", true
	}
	if c.BindDN != "" && c.BindPasswordSecretID == "" {
		validation(w, "informe a senha da conta de serviço (bind DN)")
		return
	}
	raw, _ := json.Marshal(c)
	if err := s.Store.PutSetting(r.Context(), ldapSettingKey, raw); err != nil {
		s.storeError(w, err)
		return
	}
	if changed && old != "" {
		if err := s.Secrets.Delete(r.Context(), old); err != nil {
			s.Log.Warn("delete old ldap bind secret", "err", err)
		}
	}
	s.audit(r, "auth.ldap_update", "settings", ldapSettingKey, ldapAudit(&c, changed))
	writeJSON(w, http.StatusOK, viewLDAP(&c))
}

// testLDAP runs the connection flow with the form as it is (not saved yet),
// using the saved bind password when the form leaves it blank.
func (s *Server) testLDAP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ldapInput
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		validation(w, "JSON inválido")
		return
	}
	cur, err := s.loadLDAP(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	c := in.Config
	c.Enabled = false // the mapping requirement only applies when saving it enabled
	if err := ldap.Normalize(&c); err != nil {
		validation(w, ldap.ValidationMessage(err))
		return
	}
	if err := checkBindPassword(in.BindPassword); err != nil {
		validation(w, err.Error())
		return
	}
	bindPw := ""
	if in.BindPassword != nil && *in.BindPassword != "" {
		bindPw = *in.BindPassword
	} else if !in.ClearBindPassword {
		if bindPw, err = s.ldapBindPassword(r.Context(), cur); err != nil {
			s.storeError(w, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(c.TimeoutSeconds)*3*time.Second)
	defer cancel()
	username := normalizeUsername(in.Username)
	rep := ldap.Test(ctx, &c, bindPw, username, in.Password)
	details := map[string]any{"host": c.Host, "port": c.Port, "security": c.Security, "ok": rep.OK}
	if username != "" {
		details["username"] = username
	}
	s.audit(r, "auth.ldap_test", "settings", ldapSettingKey, details)
	writeJSON(w, http.StatusOK, rep)
}

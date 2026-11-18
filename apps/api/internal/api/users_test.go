package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/efbecker/k8sfront/apps/api/internal/ldap"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// userClient logs in with username/password and keeps the session cookie.
type userClient struct {
	t  *testing.T
	ts *httptest.Server
	c  *http.Client
}

func (u *userClient) do(method, path string, body any, out any) int {
	u.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, u.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := u.c.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func login(t *testing.T, ts *httptest.Server, username, password string) (*userClient, int) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	u := &userClient{t: t, ts: ts, c: &http.Client{Jar: jar}}
	code := u.do("POST", "/api/v1/auth/login", map[string]string{"username": username, "password": password}, nil)
	return u, code
}

func createUser(t *testing.T, ts *httptest.Server, username string, role store.Role) string {
	t.Helper()
	var out userView
	if code := call(t, ts, "POST", "/api/v1/users", map[string]any{"username": username, "role": role, "password": "s3cret-password!"}, &out); code != http.StatusCreated {
		t.Fatalf("create %s: %d", username, code)
	}
	return out.ID
}

func TestRolesAreEnforced(t *testing.T) {
	ts, _ := newTestServer(t)
	createUser(t, ts, "Viewer.One", store.RoleViewer) // normalized to lower case
	createUser(t, ts, "op", store.RoleOperator)

	if _, code := login(t, ts, "viewer.one", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("bad password: %d", code)
	}
	v, code := login(t, ts, "VIEWER.ONE", "s3cret-password!")
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	var me meView
	if v.do("GET", "/api/v1/auth/me", nil, &me) != 200 || me.Role != store.RoleViewer || me.Username != "viewer.one" || me.Source != store.UserLocal {
		t.Fatalf("me %+v", me)
	}
	if code := v.do("GET", "/api/v1/servers", nil, nil); code != 200 {
		t.Fatalf("viewer read: %d", code)
	}
	var e apiError
	if code := v.do("POST", "/api/v1/servers", map[string]any{"name": "x"}, &e); code != http.StatusForbidden || !strings.Contains(e.Message, "Leitura") {
		t.Fatalf("viewer write: %d %+v", code, e)
	}
	for _, p := range []string{"/api/v1/users", "/api/v1/auth/ldap", "/api/v1/audit", "/api/v1/clusters/x/kubeconfig"} {
		if code := v.do("GET", p, nil, nil); code != http.StatusForbidden {
			t.Errorf("viewer GET %s: %d", p, code)
		}
	}

	op, _ := login(t, ts, "op", "s3cret-password!")
	if code := op.do("GET", "/api/v1/audit", nil, nil); code != 200 {
		t.Fatalf("operator audit: %d", code)
	}
	if code := op.do("POST", "/api/v1/users", map[string]any{"username": "x", "password": "s3cret-password!"}, nil); code != http.StatusForbidden {
		t.Fatalf("operator manages users: %d", code)
	}
	if code := op.do("POST", "/api/v1/servers", map[string]any{"name": "srv", "host": "10.0.0.1", "sshUser": "debian", "authMethod": "password", "password": "pw"}, nil); code != http.StatusCreated {
		t.Fatalf("operator write: %d", code)
	}

	// The audit log names who acted.
	var entries []store.AuditEntry
	call(t, ts, "GET", "/api/v1/audit", nil, &entries)
	found := false
	for _, a := range entries {
		if a.Action == "server.create" && a.Actor == "op" {
			found = true
		}
		if strings.Contains(string(a.Details), "s3cret") {
			t.Fatalf("password in audit: %s", a.Details)
		}
	}
	if !found {
		t.Fatalf("server.create by op not audited: %+v", entries)
	}
}

func TestLastLocalAdminIsKept(t *testing.T) {
	ts, _ := newTestServer(t)
	alice := createUser(t, ts, "alice", store.RoleAdmin)
	a, _ := login(t, ts, "alice", "s3cret-password!")

	if code := a.do("DELETE", "/api/v1/users/"+alice, nil, nil); code != http.StatusBadRequest {
		t.Fatalf("self delete: %d", code)
	}
	if code := a.do("PATCH", "/api/v1/users/"+alice, map[string]any{"role": "viewer"}, nil); code != http.StatusBadRequest {
		t.Fatalf("self demote: %d", code)
	}
	// Even the token cannot remove the last enabled local admin.
	var e apiError
	for _, body := range []map[string]any{{"role": "operator"}, {"disabled": true}} {
		if code := call(t, ts, "PATCH", "/api/v1/users/"+alice, body, &e); code != http.StatusConflict || !strings.Contains(e.Message, "administrador local") {
			t.Fatalf("%v: %d %+v", body, code, e)
		}
	}
	if code := call(t, ts, "DELETE", "/api/v1/users/"+alice, nil, nil); code != http.StatusConflict {
		t.Fatalf("delete last admin: %d", code)
	}
	createUser(t, ts, "bob", store.RoleAdmin)
	if code := call(t, ts, "PATCH", "/api/v1/users/"+alice, map[string]any{"disabled": true}, nil); code != 200 {
		t.Fatalf("disable with another admin: %d", code)
	}
	// Disabling revokes the session right away.
	if code := a.do("GET", "/api/v1/auth/me", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("disabled session: %d", code)
	}
	if _, code := login(t, ts, "alice", "s3cret-password!"); code != http.StatusUnauthorized {
		t.Fatalf("disabled login: %d", code)
	}
}

func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	ts, _ := newTestServer(t)
	createUser(t, ts, "carol", store.RoleOperator)
	s1, _ := login(t, ts, "carol", "s3cret-password!")
	s2, _ := login(t, ts, "carol", "s3cret-password!")
	if code := s1.do("POST", "/api/v1/auth/password", map[string]string{"currentPassword": "nope", "newPassword": "another-password"}, nil); code != 400 {
		t.Fatalf("wrong current: %d", code)
	}
	if code := s1.do("POST", "/api/v1/auth/password", map[string]string{"currentPassword": "s3cret-password!", "newPassword": "short"}, nil); code != 400 {
		t.Fatalf("policy: %d", code)
	}
	if code := s1.do("POST", "/api/v1/auth/password", map[string]string{"currentPassword": "s3cret-password!", "newPassword": "another-password"}, nil); code != 204 {
		t.Fatalf("change: %d", code)
	}
	if s1.do("GET", "/api/v1/auth/me", nil, nil) != 200 || s2.do("GET", "/api/v1/auth/me", nil, nil) != 401 {
		t.Fatal("the session that changed the password stays, the other one ends")
	}
	if _, code := login(t, ts, "carol", "another-password"); code != 200 {
		t.Fatalf("new password: %d", code)
	}
}

func TestUserValidationAndProviders(t *testing.T) {
	ts, _ := newTestServer(t)
	var prov map[string]any
	call(t, ts, "GET", "/api/v1/auth/providers", nil, &prov)
	if prov["users"] != false || prov["ldap"] != false {
		t.Fatalf("providers %v", prov)
	}
	for _, body := range []map[string]any{
		{"username": "", "password": "s3cret-password!"},
		{"username": "has space", "password": "s3cret-password!"},
		{"username": "ok", "password": "short"},
		{"username": "ok"},
		{"username": "ok", "password": "s3cret-password!", "role": "root"},
	} {
		if code := call(t, ts, "POST", "/api/v1/users", body, nil); code != 400 {
			t.Errorf("%v: %d", body, code)
		}
	}
	createUser(t, ts, "dave", store.RoleViewer)
	if code := call(t, ts, "POST", "/api/v1/users", map[string]any{"username": "DAVE", "password": "s3cret-password!"}, nil); code != 409 {
		t.Fatalf("duplicate: %d", code)
	}
	var list []map[string]any
	call(t, ts, "GET", "/api/v1/users", nil, &list)
	if len(list) != 1 || list[0]["hasPassword"] != true {
		t.Fatalf("list %v", list)
	}
	if _, ok := list[0]["passwordHash"]; ok {
		t.Fatal("hash leaked")
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "argon2") {
		t.Fatal("hash leaked")
	}
}

func TestLDAPSettingsNeverReturnTheBindPassword(t *testing.T) {
	ts, _ := newTestServer(t)
	cfg := map[string]any{"enabled": true, "kind": "ad", "host": "127.0.0.1", "port": 1, "security": "none",
		"bindDn": "CN=svc,DC=corp,DC=local", "baseDn": "DC=corp,DC=local",
		"groupMappings": []map[string]string{{"group": "k8s-admins", "role": "admin"}}}
	var e apiError
	if code := call(t, ts, "PUT", "/api/v1/auth/ldap", cfg, &e); code != 400 || !strings.Contains(e.Message, "senha da conta de serviço") {
		t.Fatalf("missing bind password: %d %+v", code, e)
	}
	cfg["bindPassword"] = "bind-secret-value"
	var view map[string]any
	if code := call(t, ts, "PUT", "/api/v1/auth/ldap", cfg, &view); code != 200 {
		t.Fatalf("put: %d", code)
	}
	call(t, ts, "GET", "/api/v1/auth/ldap", nil, &view)
	raw, _ := json.Marshal(view)
	if view["hasBindPassword"] != true || strings.Contains(string(raw), "bind-secret-value") || strings.Contains(string(raw), "bindPasswordSecretId") {
		t.Fatalf("view %s", raw)
	}
	if view["userFilter"] == "" || view["port"].(float64) != 1 {
		t.Fatalf("defaults/port %s", raw)
	}
	var prov map[string]any
	call(t, ts, "GET", "/api/v1/auth/providers", nil, &prov)
	if prov["ldap"] != true || prov["ldapKind"] != "ad" {
		t.Fatalf("providers %v", prov)
	}

	// Port 1 refuses connections: the test reports the failing step, and a
	// login falls back to a clear directory error instead of hanging.
	var rep struct {
		OK    bool `json:"ok"`
		Steps []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"steps"`
	}
	delete(cfg, "bindPassword")
	if code := call(t, ts, "POST", "/api/v1/auth/ldap/test", cfg, &rep); code != 200 || rep.OK || len(rep.Steps) != 1 || rep.Steps[0].Name != "Conexão" {
		t.Fatalf("test: %d %+v", code, rep)
	}
	if _, code := login(t, ts, "someone", "password-x"); code != http.StatusBadGateway {
		t.Fatalf("ldap down: %d", code)
	}

	var audit []store.AuditEntry
	call(t, ts, "GET", "/api/v1/audit", nil, &audit)
	raw, _ = json.Marshal(audit)
	if !strings.Contains(string(raw), "auth.ldap_update") || !strings.Contains(string(raw), "auth.ldap_test") || strings.Contains(string(raw), "bind-secret-value") {
		t.Fatalf("audit %s", raw)
	}
}

func TestLDAPLoginSyncsRoleFromGroups(t *testing.T) {
	ts, db := newTestServer(t)
	dir := ldap.NewFakeDirectory(t)
	cfg := &ldap.Config{Enabled: true, Kind: ldap.KindAD, Host: "127.0.0.1", Port: dir.Port(), Security: ldap.SecurityNone,
		BindDN: "CN=svc,DC=corp,DC=local", BaseDN: "DC=corp,DC=local",
		GroupMappings: []ldap.GroupMapping{{Group: "k8s-ops", Role: store.RoleOperator}, {Group: "k8s-admins", Role: store.RoleAdmin}}}
	if err := ldap.Normalize(cfg); err != nil {
		t.Fatal(err)
	}
	userDN := "CN=Eve,OU=Users,DC=corp,DC=local"
	dir.Passwords[cfg.BindDN] = "svc-pass"
	dir.Passwords[userDN] = "eve-pass"
	dir.Answer(strings.ReplaceAll(cfg.UserFilter, "{username}", "eve"), ldap.Entry{DN: userDN, Attributes: map[string][]string{
		"samaccountname": {"eve"}, "displayname": {"Eve Ops"}, "memberof": {"CN=k8s-ops,OU=Groups,DC=corp,DC=local"}}})
	dir.Answer(strings.ReplaceAll(cfg.UserFilter, "{username}", "mallory"), ldap.Entry{DN: "CN=Mallory,DC=corp,DC=local",
		Attributes: map[string][]string{"samaccountname": {"mallory"}}})
	dir.Passwords["CN=Mallory,DC=corp,DC=local"] = "mallory-pass"
	dir.Start()

	body := map[string]any{"enabled": true, "kind": "ad", "host": "127.0.0.1", "port": dir.Port(), "security": "none",
		"bindDn": cfg.BindDN, "bindPassword": "svc-pass", "baseDn": cfg.BaseDN, "groupMappings": cfg.GroupMappings}
	if code := call(t, ts, "PUT", "/api/v1/auth/ldap", body, nil); code != 200 {
		t.Fatalf("put: %d", code)
	}
	var rep ldap.Report
	call(t, ts, "POST", "/api/v1/auth/ldap/test", map[string]any{"kind": "ad", "host": "127.0.0.1", "port": dir.Port(), "security": "none",
		"bindDn": cfg.BindDN, "baseDn": cfg.BaseDN, "groupMappings": cfg.GroupMappings, "username": "eve", "password": "eve-pass"}, &rep)
	if !rep.OK || rep.Identity == nil || rep.Identity.Role != store.RoleOperator {
		t.Fatalf("test with the saved bind password: %+v", rep)
	}

	eve, code := login(t, ts, "Eve", "eve-pass")
	if code != 200 {
		t.Fatalf("ldap login: %d", code)
	}
	var me meView
	eve.do("GET", "/api/v1/auth/me", nil, &me)
	if me.Role != store.RoleOperator || me.Source != store.UserLDAP || me.DisplayName != "Eve Ops" {
		t.Fatalf("me %+v", me)
	}
	if _, code := login(t, ts, "eve", "wrong"); code != 401 {
		t.Fatalf("wrong ldap password: %d", code)
	}
	if _, code := login(t, ts, "mallory", "mallory-pass"); code != http.StatusForbidden {
		t.Fatalf("user outside the mapped groups: %d", code)
	}

	// The role of a directory user cannot be edited by hand, but the account can be disabled.
	u, _ := db.GetUserByUsername(context.Background(), "eve")
	if code := call(t, ts, "PATCH", "/api/v1/users/"+u.ID, map[string]any{"role": "admin"}, nil); code != 400 {
		t.Fatalf("edit ldap role: %d", code)
	}
	if code := call(t, ts, "PATCH", "/api/v1/users/"+u.ID, map[string]any{"disabled": true}, nil); code != 200 {
		t.Fatalf("disable ldap user: %d", code)
	}
	if _, code := login(t, ts, "eve", "eve-pass"); code != 401 {
		t.Fatalf("disabled ldap user logged in: %d", code)
	}

	// A local account with the same name always wins over the directory.
	createUser(t, ts, "mallory", store.RoleViewer)
	if _, code := login(t, ts, "mallory", "mallory-pass"); code != 401 {
		t.Fatalf("directory password accepted for a local account: %d", code)
	}
}

package ldap

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

func selfSigned(t *testing.T) (*tls.Config, string) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestBERIntRoundTrip(t *testing.T) {
	for _, v := range []int64{0, 1, 127, 128, 255, 256, 65535, -1, -128, -129, 1 << 40} {
		e, rest, err := next(berInt(tagInteger, v))
		if err != nil || len(rest) != 0 {
			t.Fatal(err)
		}
		got, _ := e.int()
		if got != v {
			t.Errorf("int %d → %d", v, got)
		}
	}
	long := strings.Repeat("x", 70000)
	e, _, err := next(berStr(tagOctetString, long))
	if err != nil || e.str() != long {
		t.Fatal("long octet string round trip failed")
	}
}

func TestCompileFilter(t *testing.T) {
	ok := []string{
		"(uid=alice)",
		"uid=alice",
		"(&(objectClass=user)(|(sAMAccountName=a)(userPrincipalName=a)))",
		"(!(cn=x))",
		"(cn=*)",
		"(cn=ab*cd*ef)",
		"(cn=*end)",
		"(member:1.2.840.113556.1.4.1941:=CN=a\\2cOU=b)",
		"(cn:dn:2.5.13.5:=x)",
		"(uidNumber>=1000)",
	}
	for _, f := range ok {
		if _, err := CompileFilter(f); err != nil {
			t.Errorf("%q: %v", f, err)
		}
	}
	bad := []string{"", "(uid=a", "(&)", "(=a)", "(u id=a)", "(cn=\\zz)", "(uid=a))"}
	for _, f := range bad {
		if _, err := CompileFilter(f); err == nil {
			t.Errorf("%q: expected an error", f)
		}
	}
	// Equality is [3]{attr, value}.
	b, _ := CompileFilter("(uid=a)")
	if !bytes.Equal(b, []byte{0xa3, 0x08, 0x04, 0x03, 'u', 'i', 'd', 0x04, 0x01, 'a'}) {
		t.Errorf("equality encoding = % x", b)
	}
}

func TestEscapeFilterBlocksInjection(t *testing.T) {
	f := expand("(uid={username})", "*)(uid=*", "")
	if f != `(uid=\2a\29\28uid=\2a)` {
		t.Fatalf("got %s", f)
	}
	b, err := CompileFilter(f)
	if err != nil {
		t.Fatal(err)
	}
	// Still a single equality match whose value is the literal input.
	e, _, _ := next(b)
	if e.tag != 0xa3 {
		t.Fatalf("tag %x", e.tag)
	}
	kids, _ := e.children()
	if kids[1].str() != "*)(uid=*" {
		t.Fatalf("value %q", kids[1].str())
	}
}

func adConfig(f *FakeDirectory) *Config {
	c := &Config{Enabled: true, Kind: KindAD, Host: "127.0.0.1", Port: f.Port(), Security: SecurityNone,
		BindDN: "CN=svc,DC=corp,DC=local", BaseDN: "DC=corp,DC=local",
		GroupMappings: []GroupMapping{{Group: "k8s-admins", Role: store.RoleAdmin}, {Group: "CN=k8s-ops,OU=Groups,DC=corp,DC=local", Role: store.RoleOperator}}}
	if err := Normalize(c); err != nil {
		f.t.Fatalf("%v", err)
	}
	c.Port = f.Port()
	return c
}

func seedAD(f *FakeDirectory, c *Config) {
	f.Passwords[c.BindDN] = "svc-pass"
	userDN := "CN=Alice Silva,OU=Users,DC=corp,DC=local"
	f.Passwords[userDN] = "alice-pass"
	f.Answer(expand(c.UserFilter, "alice", ""), Entry{DN: userDN, Attributes: map[string][]string{
		"samaccountname": {"Alice"}, "displayname": {"Alice Silva"}, "mail": {"alice@corp.local"},
		"memberof": {"CN=k8s-ops,OU=Groups,DC=corp,DC=local"},
	}})
	f.Answer(expand(c.GroupFilter, "alice", userDN),
		Entry{DN: "CN=k8s-ops,OU=Groups,DC=corp,DC=local"}, Entry{DN: "CN=k8s-admins,OU=Nested,DC=corp,DC=local"})
}

func TestAuthenticateAD(t *testing.T) {
	f := NewFakeDirectory(t)
	c := adConfig(f)
	seedAD(f, c)
	f.Start()
	id, err := Authenticate(context.Background(), c, "svc-pass", "alice", "alice-pass")
	if err != nil {
		t.Fatal(err)
	}
	if id.Username != "alice" || id.DisplayName != "Alice Silva" || id.Email != "alice@corp.local" {
		t.Fatalf("identity %+v", id)
	}
	if len(id.Groups) != 2 {
		t.Fatalf("groups %v (memberOf and the nested search must be merged without duplicates)", id.Groups)
	}
	if id.Role != store.RoleAdmin {
		t.Fatalf("role %q, want admin via the nested group name", id.Role)
	}

	if _, err := Authenticate(context.Background(), c, "svc-pass", "alice", "wrong"); err != ErrBadCredentials {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := Authenticate(context.Background(), c, "svc-pass", "bob", "x"); err != ErrBadCredentials {
		t.Fatalf("unknown user: %v", err)
	}
	if _, err := Authenticate(context.Background(), c, "svc-pass", "alice", ""); err != ErrBadCredentials {
		t.Fatalf("empty password must never bind: %v", err)
	}
	if _, err := Authenticate(context.Background(), c, "bad", "alice", "alice-pass"); err == nil || !strings.Contains(err.Error(), "conta de serviço") {
		t.Fatalf("bad service password: %v", err)
	}
}

func TestMapRoleDefaultAndDeny(t *testing.T) {
	c := &Config{GroupMappings: []GroupMapping{{Group: "viewers", Role: store.RoleViewer}, {Group: "ops", Role: store.RoleOperator}}}
	if r := MapRole(c, []string{"CN=Viewers,DC=x", "cn=OPS,dc=x"}); r != store.RoleOperator {
		t.Fatalf("highest role wins, got %q", r)
	}
	if r := MapRole(c, []string{"CN=other,DC=x"}); r != "" {
		t.Fatalf("no mapping and no default must deny, got %q", r)
	}
	c.DefaultRole = store.RoleViewer
	if r := MapRole(c, nil); r != store.RoleViewer {
		t.Fatalf("default role, got %q", r)
	}
}

func TestStartTLSAndLDAPS(t *testing.T) {
	tcfg, caPEM := selfSigned(t)
	for _, sec := range []Security{SecurityStartTLS, SecurityLDAPS} {
		t.Run(string(sec), func(t *testing.T) {
			f := NewFakeDirectory(t)
			f.TLS, f.LDAPS = tcfg, sec == SecurityLDAPS
			c := adConfig(f)
			c.Security, c.CACert = sec, caPEM
			seedAD(f, c)
			f.Start()
			rep := Test(context.Background(), c, "svc-pass", "alice", "alice-pass")
			if !rep.OK {
				t.Fatalf("report %+v", rep)
			}
			if !strings.Contains(rep.Steps[0].Detail, "com TLS") {
				t.Fatalf("connection step %+v", rep.Steps[0])
			}

			// Without the CA the certificate must be rejected.
			c.CACert = ""
			rep = Test(context.Background(), c, "svc-pass", "", "")
			if rep.OK || rep.Steps[0].OK {
				t.Fatalf("untrusted certificate accepted: %+v", rep)
			}
		})
	}
}

func TestTestReportWithoutUser(t *testing.T) {
	f := NewFakeDirectory(t)
	c := adConfig(f)
	seedAD(f, c)
	f.Start()
	rep := Test(context.Background(), c, "svc-pass", "", "")
	if !rep.OK || len(rep.Steps) != 3 {
		t.Fatalf("report %+v", rep)
	}
	rep = Test(context.Background(), c, "svc-pass", "alice", "nope")
	if rep.OK || rep.Steps[len(rep.Steps)-1].Name != "Senha do usuário" {
		t.Fatalf("report %+v", rep)
	}
}

func TestNormalize(t *testing.T) {
	c := &Config{Kind: "AD", Host: "dc01.corp.local", BaseDN: "DC=corp,DC=local", Enabled: true,
		GroupMappings: []GroupMapping{{Group: " Domain Admins ", Role: store.RoleAdmin}, {Group: ""}}}
	if err := Normalize(c); err != nil {
		t.Fatal(err)
	}
	if c.Port != 636 || c.Security != SecurityLDAPS || c.UsernameAttr != "sAMAccountName" || len(c.GroupMappings) != 1 || c.GroupMappings[0].Group != "Domain Admins" {
		t.Fatalf("defaults: %+v", c)
	}
	for name, mut := range map[string]func(*Config){
		"url in host":        func(c *Config) { c.Host = "ldap://dc01" },
		"no base":            func(c *Config) { c.BaseDN = "" },
		"filter w/o user":    func(c *Config) { c.UserFilter = "(uid=x)" },
		"bad filter":         func(c *Config) { c.UserFilter = "(uid={username}" },
		"bad role":           func(c *Config) { c.GroupMappings = []GroupMapping{{Group: "g", Role: "root"}} },
		"enabled w/o access": func(c *Config) { c.GroupMappings = nil },
		"bad port":           func(c *Config) { c.Port = 70000 },
	} {
		cc := *c
		mut(&cc)
		if err := Normalize(&cc); err == nil || !IsValidation(err) {
			t.Errorf("%s: expected validation error, got %v", name, err)
		}
	}
}

package ldap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// Kinds of directory. They only change the defaults of the filters and attributes.
const (
	KindAD   = "ad"   // Microsoft Active Directory
	KindLDAP = "ldap" // OpenLDAP, FreeIPA, 389-ds…
)

// GroupMapping grants Role to members of Group (a full DN or just the group name/CN).
type GroupMapping struct {
	Group string     `json:"group"`
	Role  store.Role `json:"role"`
}

// Config is the LDAP/AD integration as persisted in the "ldap" setting.
// The bind password lives in the vault (BindPasswordSecretID).
type Config struct {
	Enabled              bool           `json:"enabled"`
	Kind                 string         `json:"kind"`
	Host                 string         `json:"host"`
	Port                 int            `json:"port"`
	Security             Security       `json:"security"`
	SkipVerify           bool           `json:"skipVerify"`
	CACert               string         `json:"caCert"`
	BindDN               string         `json:"bindDn"`
	BindPasswordSecretID string         `json:"bindPasswordSecretId,omitempty"`
	BaseDN               string         `json:"baseDn"`
	UserFilter           string         `json:"userFilter"`
	UsernameAttr         string         `json:"usernameAttr"`
	DisplayNameAttr      string         `json:"displayNameAttr"`
	EmailAttr            string         `json:"emailAttr"`
	GroupBaseDN          string         `json:"groupBaseDn"`
	GroupFilter          string         `json:"groupFilter"`
	GroupMappings        []GroupMapping `json:"groupMappings"`
	DefaultRole          store.Role     `json:"defaultRole"` // "" = deny users outside the mapped groups
	TimeoutSeconds       int            `json:"timeoutSeconds"`
}

// Defaults returns the filters and attributes recommended for a kind.
func Defaults(kind string) Config {
	if kind == KindAD {
		return Config{
			Kind:            KindAD,
			UserFilter:      "(&(objectCategory=person)(objectClass=user)(|(sAMAccountName={username})(userPrincipalName={username})))",
			UsernameAttr:    "sAMAccountName",
			DisplayNameAttr: "displayName",
			EmailAttr:       "mail",
			// LDAP_MATCHING_RULE_IN_CHAIN: also finds nested groups.
			GroupFilter: "(&(objectClass=group)(member:1.2.840.113556.1.4.1941:={dn}))",
		}
	}
	return Config{
		Kind:            KindLDAP,
		UserFilter:      "(&(objectClass=inetOrgPerson)(uid={username}))",
		UsernameAttr:    "uid",
		DisplayNameAttr: "cn",
		EmailAttr:       "mail",
		GroupFilter:     "(|(&(objectClass=groupOfNames)(member={dn}))(&(objectClass=groupOfUniqueNames)(uniqueMember={dn}))(&(objectClass=posixGroup)(memberUid={username})))",
	}
}

var errInvalid = errors.New("validation")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errInvalid, fmt.Sprintf(format, a...))
}

// IsValidation reports whether err came from Normalize.
func IsValidation(err error) bool { return errors.Is(err, errInvalid) }

// ValidationMessage strips the internal prefix.
func ValidationMessage(err error) string {
	return strings.TrimPrefix(err.Error(), errInvalid.Error()+": ")
}

// Normalize fills defaults and validates the configuration.
func Normalize(c *Config) error {
	c.Kind = strings.ToLower(strings.TrimSpace(c.Kind))
	if c.Kind != KindAD && c.Kind != KindLDAP {
		return invalid("tipo deve ser ad ou ldap")
	}
	def := Defaults(c.Kind)
	trim := func(p *string, d string) {
		*p = strings.TrimSpace(*p)
		if *p == "" {
			*p = d
		}
	}
	trim(&c.UserFilter, def.UserFilter)
	trim(&c.UsernameAttr, def.UsernameAttr)
	trim(&c.DisplayNameAttr, def.DisplayNameAttr)
	trim(&c.EmailAttr, def.EmailAttr)
	trim(&c.GroupFilter, def.GroupFilter)
	c.Host = strings.TrimSpace(c.Host)
	c.BindDN = strings.TrimSpace(c.BindDN)
	c.BaseDN = strings.TrimSpace(c.BaseDN)
	c.GroupBaseDN = strings.TrimSpace(c.GroupBaseDN)
	if c.Security == "" {
		c.Security = SecurityLDAPS
	}
	switch c.Security {
	case SecurityNone, SecurityStartTLS, SecurityLDAPS:
	default:
		return invalid("segurança deve ser ldaps, starttls ou none")
	}
	if c.Host == "" {
		return invalid("informe o servidor (host ou IP)")
	}
	if strings.ContainsAny(c.Host, "/ ") || strings.Contains(c.Host, "://") {
		return invalid("servidor deve ser só o host ou IP, sem ldap:// nem porta")
	}
	if c.Port == 0 {
		c.Port = 389
		if c.Security == SecurityLDAPS {
			c.Port = 636
		}
	}
	if c.Port < 1 || c.Port > 65535 {
		return invalid("porta inválida")
	}
	if c.BaseDN == "" {
		return invalid("informe a base de busca (ex.: DC=empresa,DC=local)")
	}
	if !strings.Contains(c.UserFilter, "{username}") {
		return invalid("o filtro de usuário precisa conter {username}")
	}
	if _, err := CompileFilter(expand(c.UserFilter, "x", "x")); err != nil {
		return invalid("filtro de usuário: %v", err)
	}
	if _, err := CompileFilter(expand(c.GroupFilter, "x", "x")); err != nil {
		return invalid("filtro de grupos: %v", err)
	}
	for _, a := range []string{c.UsernameAttr, c.DisplayNameAttr, c.EmailAttr} {
		if !validAttr(a) {
			return invalid("atributo inválido %q", a)
		}
	}
	if c.DefaultRole != "" && !c.DefaultRole.Valid() {
		return invalid("papel padrão inválido")
	}
	maps := []GroupMapping{}
	for _, m := range c.GroupMappings {
		m.Group = strings.TrimSpace(m.Group)
		if m.Group == "" {
			continue
		}
		if !m.Role.Valid() {
			return invalid("papel inválido para o grupo %q", m.Group)
		}
		maps = append(maps, m)
	}
	c.GroupMappings = maps
	if c.Enabled && len(maps) == 0 && c.DefaultRole == "" {
		return invalid("mapeie ao menos um grupo para um papel (ou defina um papel padrão)")
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 10
	}
	if c.TimeoutSeconds > 60 {
		c.TimeoutSeconds = 60
	}
	return nil
}

// expand substitutes the (escaped) placeholders of a filter.
func expand(filter, username, dn string) string {
	return strings.NewReplacer("{username}", EscapeFilter(username), "{dn}", EscapeFilter(dn)).Replace(filter)
}

func (c *Config) dialOptions() DialOptions {
	return DialOptions{Host: c.Host, Port: c.Port, Security: c.Security, CACert: c.CACert, SkipVerify: c.SkipVerify,
		Timeout: time.Duration(c.TimeoutSeconds) * time.Second}
}

// Identity is an authenticated directory user.
type Identity struct {
	Username    string     `json:"username"`
	DN          string     `json:"dn"`
	DisplayName string     `json:"displayName"`
	Email       string     `json:"email"`
	Groups      []string   `json:"groups"`
	Role        store.Role `json:"role"` // "" when no mapping matched and there is no default role
}

// ErrBadCredentials hides whether the user exists or the password is wrong.
var ErrBadCredentials = errors.New("usuário ou senha inválidos")

// groupName returns the value of the first RDN of a DN (CN=Ops,OU=x → Ops).
func groupName(dn string) string {
	first, _, _ := strings.Cut(dn, ",")
	if _, v, ok := strings.Cut(first, "="); ok {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(dn)
}

// MapRole picks the highest role whose group matches (DN or name, case-insensitive).
func MapRole(c *Config, groups []string) store.Role {
	best := store.Role("")
	for _, m := range c.GroupMappings {
		for _, g := range groups {
			if strings.EqualFold(m.Group, g) || strings.EqualFold(m.Group, groupName(g)) {
				if m.Role.Rank() > best.Rank() {
					best = m.Role
				}
			}
		}
	}
	if best == "" {
		best = c.DefaultRole
	}
	return best
}

// Step is one line of a connection test report.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Report is the result of Test.
type Report struct {
	OK       bool      `json:"ok"`
	Steps    []Step    `json:"steps"`
	Identity *Identity `json:"identity,omitempty"`
}

// session opens a connection and binds with the service account (or
// anonymously when no bind DN is set).
func session(ctx context.Context, c *Config, bindPassword string, step func(name string, err error, okDetail string)) (*Conn, error) {
	conn, err := Dial(ctx, c.dialOptions())
	addr := net.JoinHostPort(c.Host, fmt.Sprint(c.Port))
	if err != nil {
		step("Conexão", err, "")
		return nil, err
	}
	detail := addr + " sem TLS"
	if conn.TLS {
		detail = addr + " com TLS (" + string(c.Security) + ")"
	}
	step("Conexão", nil, detail)
	if c.BindDN != "" {
		if err := conn.Bind(c.BindDN, bindPassword); err != nil {
			conn.Close()
			if IsInvalidCredentials(err) {
				err = errors.New("bind DN ou senha da conta de serviço inválidos")
			}
			step("Bind da conta de serviço", err, "")
			return nil, err
		}
		step("Bind da conta de serviço", nil, c.BindDN)
	} else {
		step("Bind da conta de serviço", nil, "anônimo (sem bind DN)")
	}
	return conn, nil
}

func findUser(conn *Conn, c *Config, username string) (*Entry, error) {
	entries, err := conn.Search(SearchRequest{
		BaseDN:     c.BaseDN,
		Filter:     expand(c.UserFilter, username, ""),
		Attributes: []string{c.UsernameAttr, c.DisplayNameAttr, c.EmailAttr, "memberOf"},
		SizeLimit:  2,
	})
	var le *Error
	if errors.As(err, &le) && le.Code == ResultSizeLimitExceeded {
		return nil, fmt.Errorf("o filtro encontrou mais de um usuário para %q", username)
	}
	if err != nil {
		return nil, err
	}
	switch len(entries) {
	case 0:
		return nil, ErrBadCredentials
	case 1:
		return &entries[0], nil
	}
	return nil, fmt.Errorf("o filtro encontrou mais de um usuário para %q", username)
}

func userGroups(conn *Conn, c *Config, e *Entry, username string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(g string) {
		if k := strings.ToLower(g); g != "" && !seen[k] {
			seen[k] = true
			out = append(out, g)
		}
	}
	for _, g := range e.Values("memberOf") {
		add(g)
	}
	base := c.GroupBaseDN
	if base == "" {
		base = c.BaseDN
	}
	entries, err := conn.Search(SearchRequest{BaseDN: base, Filter: expand(c.GroupFilter, username, e.DN), Attributes: []string{"cn"}, SizeLimit: 1000})
	var le *Error
	if err != nil && !(errors.As(err, &le) && le.Code == ResultSizeLimitExceeded) {
		return out, err
	}
	for _, g := range entries {
		add(g.DN)
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out, nil
}

// Authenticate checks username/password against the directory and returns the
// identity with the mapped role (empty when no group grants access).
func Authenticate(ctx context.Context, c *Config, bindPassword, username, password string) (*Identity, error) {
	if username == "" || password == "" {
		return nil, ErrBadCredentials
	}
	conn, err := session(ctx, c, bindPassword, func(string, error, string) {})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return login(conn, c, username, password, func(string, error, string) {})
}

func login(conn *Conn, c *Config, username, password string, step func(string, error, string)) (*Identity, error) {
	e, err := findUser(conn, c, username)
	if err != nil {
		if errors.Is(err, ErrBadCredentials) {
			step("Busca do usuário", errors.New("nenhum usuário encontrado com o filtro"), "")
		} else {
			step("Busca do usuário", err, "")
		}
		return nil, err
	}
	step("Busca do usuário", nil, e.DN)
	// Groups are read with the service account, before the user bind changes
	// the identity of the connection.
	groups, gerr := userGroups(conn, c, e, username)
	if err := conn.Bind(e.DN, password); err != nil {
		if IsInvalidCredentials(err) {
			step("Senha do usuário", errors.New("senha inválida (ou conta bloqueada/expirada)"), "")
			return nil, ErrBadCredentials
		}
		step("Senha do usuário", err, "")
		return nil, err
	}
	step("Senha do usuário", nil, "bind como o usuário aceito")
	if gerr != nil {
		step("Grupos", gerr, "")
		return nil, gerr
	}
	id := &Identity{
		Username:    strings.ToLower(firstNonEmpty(e.Get(c.UsernameAttr), username)),
		DN:          e.DN,
		DisplayName: e.Get(c.DisplayNameAttr),
		Email:       e.Get(c.EmailAttr),
		Groups:      groups,
	}
	if id.Groups == nil {
		id.Groups = []string{}
	}
	step("Grupos", nil, fmt.Sprintf("%d grupo(s)", len(groups)))
	id.Role = MapRole(c, groups)
	return id, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Test runs the whole flow step by step for the settings screen. With a
// username and password it also authenticates that user and shows the role
// it would get.
func Test(ctx context.Context, c *Config, bindPassword, username, password string) Report {
	rep := Report{}
	step := func(name string, err error, ok string) {
		if err != nil {
			rep.Steps = append(rep.Steps, Step{Name: name, Detail: err.Error()})
			return
		}
		rep.Steps = append(rep.Steps, Step{Name: name, OK: true, Detail: ok})
	}
	conn, err := session(ctx, c, bindPassword, step)
	if err != nil {
		return rep
	}
	defer conn.Close()
	if username == "" {
		// Only check that the base DN is searchable.
		_, err := conn.Search(SearchRequest{BaseDN: c.BaseDN, Filter: expand(c.UserFilter, "k8sfront-connection-test", ""), Attributes: []string{"cn"}, SizeLimit: 1})
		step("Busca na base", err, c.BaseDN)
		rep.OK = err == nil
		return rep
	}
	id, err := login(conn, c, username, password, step)
	if err != nil {
		return rep
	}
	rep.Identity = id
	if id.Role == "" {
		rep.Steps = append(rep.Steps, Step{Name: "Papel", Detail: "nenhum grupo do usuário está mapeado: o login seria recusado"})
		return rep
	}
	rep.Steps = append(rep.Steps, Step{Name: "Papel", OK: true, Detail: string(id.Role)})
	rep.OK = true
	return rep
}

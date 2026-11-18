package ldap

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Result codes (RFC 4511 §4.1.9) the client looks at.
const (
	ResultSuccess            = 0
	ResultSizeLimitExceeded  = 4
	ResultInvalidCredentials = 49
)

// Error is a non-success LDAPResult.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("ldap: código %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("ldap: código %d", e.Code)
}

// IsInvalidCredentials reports whether the server rejected a bind (code 49).
func IsInvalidCredentials(err error) bool {
	var le *Error
	return errors.As(err, &le) && le.Code == ResultInvalidCredentials
}

// Security selects how the connection is protected.
type Security string

const (
	SecurityNone     Security = "none"
	SecurityStartTLS Security = "starttls"
	SecurityLDAPS    Security = "ldaps"
)

// DialOptions describe how to reach the directory.
type DialOptions struct {
	Host       string
	Port       int
	Security   Security
	CACert     string // PEM bundle added to the system roots
	SkipVerify bool
	Timeout    time.Duration
}

// Conn is one LDAP session. It is not safe for concurrent use.
type Conn struct {
	nc      net.Conn
	r       *bufio.Reader
	msgID   int64
	timeout time.Duration
	TLS     bool
}

func tlsConfig(o DialOptions) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: o.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: o.SkipVerify} //nolint:gosec // opt-in, shown as a warning in the UI
	if strings.TrimSpace(o.CACert) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(o.CACert)) {
			return nil, errors.New("CA inválida: nenhum certificado PEM encontrado")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// Dial connects and, for LDAPS/StartTLS, completes the TLS handshake.
func Dial(ctx context.Context, o DialOptions) (*Conn, error) {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Port == 0 {
		o.Port = 389
		if o.Security == SecurityLDAPS {
			o.Port = 636
		}
	}
	tcfg, err := tlsConfig(o)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: o.Timeout}
	nc, err := d.DialContext(ctx, "tcp", net.JoinHostPort(o.Host, strconv.Itoa(o.Port)))
	if err != nil {
		return nil, err
	}
	c := &Conn{nc: nc, r: bufio.NewReader(nc), timeout: o.Timeout}
	switch o.Security {
	case SecurityLDAPS:
		if err := c.upgradeTLS(ctx, tcfg); err != nil {
			nc.Close()
			return nil, err
		}
	case SecurityStartTLS:
		if err := c.startTLS(ctx, tcfg); err != nil {
			nc.Close()
			return nil, err
		}
	}
	return c, nil
}

func (c *Conn) upgradeTLS(ctx context.Context, cfg *tls.Config) error {
	tc := tls.Client(c.nc, cfg)
	hctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		return fmt.Errorf("TLS: %w", err)
	}
	c.nc, c.r, c.TLS = tc, bufio.NewReader(tc), true
	return nil
}

const oidStartTLS = "1.3.6.1.4.1.1466.20037"

func (c *Conn) startTLS(ctx context.Context, cfg *tls.Config) error {
	id, err := c.send(tlv(appExtendedRequest, berStr(0x80, oidStartTLS)))
	if err != nil {
		return err
	}
	op, err := c.readResponse(id)
	if err != nil {
		return err
	}
	if op.tag != appExtendedResp {
		return errMalformed
	}
	if err := ldapResult(op); err != nil {
		return fmt.Errorf("StartTLS recusado: %w", err)
	}
	return c.upgradeTLS(ctx, cfg)
}

// Close sends an unbind and closes the socket.
func (c *Conn) Close() error {
	if c == nil || c.nc == nil {
		return nil
	}
	_ = c.nc.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = c.send([]byte{appUnbindRequest, 0x00})
	return c.nc.Close()
}

func (c *Conn) send(op []byte) (int64, error) {
	c.msgID++
	msg := tlv(tagSequence, berInt(tagInteger, c.msgID), op)
	_ = c.nc.SetWriteDeadline(time.Now().Add(c.timeout))
	_, err := c.nc.Write(msg)
	return c.msgID, err
}

// readMessage returns the protocolOp of the next message with the given id.
func (c *Conn) readResponse(id int64) (elem, error) {
	for {
		_ = c.nc.SetReadDeadline(time.Now().Add(c.timeout))
		m, err := readElem(c.r)
		if err != nil {
			return elem{}, err
		}
		if m.tag != tagSequence {
			return elem{}, errMalformed
		}
		parts, err := m.children()
		if err != nil || len(parts) < 2 {
			return elem{}, errMalformed
		}
		got, err := parts[0].int()
		if err != nil {
			return elem{}, err
		}
		if got == 0 && parts[1].tag == appExtendedResp {
			// Notice of disconnection (RFC 4511 §4.4.1).
			if err := ldapResult(parts[1]); err != nil {
				return elem{}, fmt.Errorf("servidor encerrou a conexão: %w", err)
			}
			return elem{}, errors.New("ldap: servidor encerrou a conexão")
		}
		if got != id {
			continue
		}
		return parts[1], nil
	}
}

// ldapResult checks the LDAPResult at the start of a response op.
func ldapResult(op elem) error {
	parts, err := op.children()
	if err != nil || len(parts) < 3 {
		return errMalformed
	}
	code, err := parts[0].int()
	if err != nil {
		return err
	}
	if code == ResultSuccess {
		return nil
	}
	return &Error{Code: int(code), Message: strings.TrimRight(parts[2].str(), "\x00 \n")}
}

// Bind performs a simple bind. An empty password is refused locally: LDAP
// treats it as an unauthenticated bind that "succeeds" without checking.
func (c *Conn) Bind(dn, password string) error {
	if password == "" {
		return &Error{Code: ResultInvalidCredentials, Message: "senha vazia"}
	}
	id, err := c.send(tlv(appBindRequest, berInt(tagInteger, 3), berStr(tagOctetString, dn), berStr(0x80, password)))
	if err != nil {
		return err
	}
	op, err := c.readResponse(id)
	if err != nil {
		return err
	}
	if op.tag != appBindResponse {
		return errMalformed
	}
	return ldapResult(op)
}

// Entry is one search result.
type Entry struct {
	DN         string
	Attributes map[string][]string // lower-case attribute names
}

// Get returns the first value of an attribute (case-insensitive name).
func (e *Entry) Get(attr string) string {
	if v := e.Attributes[strings.ToLower(attr)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Values returns every value of an attribute.
func (e *Entry) Values(attr string) []string { return e.Attributes[strings.ToLower(attr)] }

// SearchRequest is a subtree search.
type SearchRequest struct {
	BaseDN     string
	Filter     string
	Attributes []string
	SizeLimit  int
}

// Search runs a subtree search and returns the entries (references are ignored).
func (c *Conn) Search(req SearchRequest) ([]Entry, error) {
	filter, err := CompileFilter(req.Filter)
	if err != nil {
		return nil, err
	}
	attrs := make([][]byte, 0, len(req.Attributes))
	for _, a := range req.Attributes {
		attrs = append(attrs, berStr(tagOctetString, a))
	}
	op := tlv(appSearchRequest,
		berStr(tagOctetString, req.BaseDN),
		berInt(tagEnumerated, 2), // wholeSubtree
		berInt(tagEnumerated, 0), // neverDerefAliases
		berInt(tagInteger, int64(req.SizeLimit)),
		berInt(tagInteger, int64(c.timeout/time.Second)),
		berBool(tagBoolean, false),
		filter,
		tlv(tagSequence, attrs...),
	)
	id, err := c.send(op)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for {
		op, err := c.readResponse(id)
		if err != nil {
			return nil, err
		}
		switch op.tag {
		case appSearchEntry:
			e, err := parseEntry(op)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		case appSearchReference:
		case appSearchDone:
			if err := ldapResult(op); err != nil {
				var le *Error
				if errors.As(err, &le) && le.Code == ResultSizeLimitExceeded {
					return out, err
				}
				return nil, err
			}
			return out, nil
		default:
			return nil, errMalformed
		}
	}
}

func parseEntry(op elem) (Entry, error) {
	parts, err := op.children()
	if err != nil || len(parts) < 2 {
		return Entry{}, errMalformed
	}
	e := Entry{DN: parts[0].str(), Attributes: map[string][]string{}}
	list, err := parts[1].children()
	if err != nil {
		return Entry{}, err
	}
	for _, a := range list {
		kv, err := a.children()
		if err != nil || len(kv) < 2 {
			return Entry{}, errMalformed
		}
		vals, err := kv[1].children()
		if err != nil {
			return Entry{}, err
		}
		key := strings.ToLower(kv[0].str())
		for _, v := range vals {
			e.Attributes[key] = append(e.Attributes[key], v.str())
		}
	}
	return e, nil
}

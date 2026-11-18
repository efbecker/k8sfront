package ldap

import (
	"bufio"
	"crypto/tls"
	"net"
)

// TB is the part of testing.TB the fake directory needs.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// FakeDirectory is a tiny in-process LDAP server for tests of this and other
// packages: binds are checked against Passwords and searches are answered by
// matching the compiled filter bytes. It never listens unless a test creates it.
type FakeDirectory struct {
	t         TB
	ln        net.Listener
	Passwords map[string]string  // dn → password
	answers   map[string][]Entry // compiled filter (string of bytes) → entries
	TLS       *tls.Config        // for LDAPS / StartTLS
	LDAPS     bool               // TLS from the first byte
}

// Answer registers the entries returned for a search with this filter.
func (f *FakeDirectory) Answer(filter string, entries ...Entry) {
	b, err := CompileFilter(filter)
	if err != nil {
		f.t.Fatalf("compile %q: %v", filter, err)
	}
	f.answers[string(b)] = entries
}

// NewFakeDirectory listens on 127.0.0.1; configure it, then call Start.
func NewFakeDirectory(t TB) *FakeDirectory {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &FakeDirectory{t: t, ln: ln, Passwords: map[string]string{}, answers: map[string][]Entry{}}
	t.Cleanup(func() { ln.Close() })
	return f
}

// Start serves connections; configure the directory before calling it.
func (f *FakeDirectory) Start() {
	ln := f.ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
}

// Port is the TCP port on 127.0.0.1.
func (f *FakeDirectory) Port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func result(tag byte, code int) []byte {
	return tlv(tag, berInt(tagEnumerated, int64(code)), berStr(tagOctetString, ""), berStr(tagOctetString, ""))
}

func (f *FakeDirectory) serve(c net.Conn) {
	defer c.Close()
	if f.LDAPS {
		tc := tls.Server(c, f.TLS)
		if err := tc.Handshake(); err != nil {
			return
		}
		c = tc
	}
	r := bufio.NewReader(c)
	for {
		m, err := readElem(r)
		if err != nil {
			return
		}
		parts, _ := m.children()
		id, _ := parts[0].int()
		reply := func(op []byte) { c.Write(tlv(tagSequence, berInt(tagInteger, id), op)) }
		op := parts[1]
		switch op.tag {
		case appBindRequest:
			p, _ := op.children()
			dn, pw := p[1].str(), p[2].str()
			if want, ok := f.Passwords[dn]; ok && want == pw {
				reply(result(appBindResponse, 0))
			} else {
				reply(result(appBindResponse, ResultInvalidCredentials))
			}
		case appSearchRequest:
			p, _ := op.children()
			filter := p[6]
			raw := tlv(filter.tag, filter.data)
			for _, e := range f.answers[string(raw)] {
				var attrs [][]byte
				for k, vs := range e.Attributes {
					var vals [][]byte
					for _, v := range vs {
						vals = append(vals, berStr(tagOctetString, v))
					}
					attrs = append(attrs, tlv(tagSequence, berStr(tagOctetString, k), tlv(tagSet, vals...)))
				}
				reply(tlv(appSearchEntry, berStr(tagOctetString, e.DN), tlv(tagSequence, attrs...)))
			}
			reply(result(appSearchDone, 0))
		case appExtendedRequest:
			reply(result(appExtendedResp, 0))
			tc := tls.Server(c, f.TLS)
			if err := tc.Handshake(); err != nil {
				return
			}
			c = tc
			r = bufio.NewReader(tc)
		case appUnbindRequest:
			return
		}
	}
}

// Package ldap is a minimal LDAPv3 client (RFC 4511) with just what the
// platform needs to authenticate users against OpenLDAP/FreeIPA and Microsoft
// Active Directory: simple bind, subtree search, StartTLS and LDAPS. It is
// written on the standard library so the API keeps its dependency set small.
package ldap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// BER identifiers used by LDAP (single-octet tags only).
const (
	tagBoolean     = 0x01
	tagInteger     = 0x02
	tagOctetString = 0x04
	tagEnumerated  = 0x0a
	tagSequence    = 0x30
	tagSet         = 0x31

	appBindRequest     = 0x60
	appBindResponse    = 0x61
	appUnbindRequest   = 0x42
	appSearchRequest   = 0x63
	appSearchEntry     = 0x64
	appSearchDone      = 0x65
	appSearchReference = 0x73
	appExtendedRequest = 0x77
	appExtendedResp    = 0x78
)

// maxMessage bounds a single LDAP message read from the server.
const maxMessage = 8 << 20

var errMalformed = errors.New("ldap: malformed BER data")

func encLen(n int) []byte {
	switch {
	case n < 0x80:
		return []byte{byte(n)}
	case n <= 0xff:
		return []byte{0x81, byte(n)}
	case n <= 0xffff:
		return []byte{0x82, byte(n >> 8), byte(n)}
	case n <= 0xffffff:
		return []byte{0x83, byte(n >> 16), byte(n >> 8), byte(n)}
	default:
		return []byte{0x84, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

// tlv builds a tag-length-value with the concatenated contents.
func tlv(tag byte, contents ...[]byte) []byte {
	n := 0
	for _, c := range contents {
		n += len(c)
	}
	out := make([]byte, 0, n+6)
	out = append(out, tag)
	out = append(out, encLen(n)...)
	for _, c := range contents {
		out = append(out, c...)
	}
	return out
}

func berInt(tag byte, v int64) []byte {
	var b []byte
	for {
		b = append([]byte{byte(v)}, b...)
		v >>= 8
		if (v == 0 && b[0]&0x80 == 0) || (v == -1 && b[0]&0x80 != 0) {
			break
		}
	}
	return tlv(tag, b)
}

func berStr(tag byte, s string) []byte { return tlv(tag, []byte(s)) }

func berBool(tag byte, v bool) []byte {
	if v {
		return tlv(tag, []byte{0xff})
	}
	return tlv(tag, []byte{0x00})
}

// elem is a decoded TLV: the identifier octet and the raw content.
type elem struct {
	tag  byte
	data []byte
}

func parseLen(b []byte) (n, used int, err error) {
	if len(b) == 0 {
		return 0, 0, errMalformed
	}
	if b[0] < 0x80 {
		return int(b[0]), 1, nil
	}
	k := int(b[0] & 0x7f)
	if k == 0 || k > 4 || len(b) < 1+k {
		return 0, 0, errMalformed // indefinite lengths are not allowed in LDAP
	}
	for i := 1; i <= k; i++ {
		n = n<<8 | int(b[i])
	}
	return n, 1 + k, nil
}

// next decodes one element from b and returns the remainder.
func next(b []byte) (elem, []byte, error) {
	if len(b) < 2 || b[0]&0x1f == 0x1f {
		return elem{}, nil, errMalformed
	}
	n, used, err := parseLen(b[1:])
	if err != nil || n < 0 || len(b) < 1+used+n {
		return elem{}, nil, errMalformed
	}
	start := 1 + used
	return elem{tag: b[0], data: b[start : start+n]}, b[start+n:], nil
}

// children decodes every element inside a constructed value.
func (e elem) children() ([]elem, error) {
	var out []elem
	b := e.data
	for len(b) > 0 {
		c, rest, err := next(b)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
		b = rest
	}
	return out, nil
}

func (e elem) int() (int64, error) {
	if len(e.data) == 0 || len(e.data) > 8 {
		return 0, errMalformed
	}
	v := int64(int8(e.data[0]))
	for _, c := range e.data[1:] {
		v = v<<8 | int64(c)
	}
	return v, nil
}

func (e elem) str() string { return string(e.data) }

// readElem reads one complete top-level element from r.
func readElem(r *bufio.Reader) (elem, error) {
	tag, err := r.ReadByte()
	if err != nil {
		return elem{}, err
	}
	first, err := r.ReadByte()
	if err != nil {
		return elem{}, err
	}
	n := int(first)
	if first >= 0x80 {
		k := int(first & 0x7f)
		if k == 0 || k > 4 {
			return elem{}, errMalformed
		}
		n = 0
		for i := 0; i < k; i++ {
			c, err := r.ReadByte()
			if err != nil {
				return elem{}, err
			}
			n = n<<8 | int(c)
		}
	}
	if n > maxMessage {
		return elem{}, fmt.Errorf("ldap: message of %d bytes exceeds the limit", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return elem{}, err
	}
	return elem{tag: tag, data: data}, nil
}

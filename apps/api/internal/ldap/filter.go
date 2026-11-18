package ldap

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// EscapeFilter escapes a value for use inside a search filter (RFC 4515), so a
// username such as "*)(uid=*" can never change the meaning of the filter.
func EscapeFilter(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', '*', '(', ')', 0:
			fmt.Fprintf(&b, "\\%02x", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// CompileFilter turns an RFC 4515 string filter into its BER encoding.
func CompileFilter(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("filtro vazio")
	}
	if s[0] != '(' {
		s = "(" + s + ")"
	}
	out, rest, err := compile(s)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(rest) != "" {
		return nil, fmt.Errorf("filtro inválido: sobra %q", rest)
	}
	return out, nil
}

func compile(s string) ([]byte, string, error) {
	if len(s) < 3 || s[0] != '(' {
		return nil, "", fmt.Errorf("filtro inválido perto de %q", trunc(s))
	}
	switch s[1] {
	case '&', '|':
		tag := byte(0xa0)
		if s[1] == '|' {
			tag = 0xa1
		}
		rest := s[2:]
		var parts [][]byte
		for {
			rest = strings.TrimLeft(rest, " ")
			if rest == "" {
				return nil, "", fmt.Errorf("filtro sem ')' final")
			}
			if rest[0] == ')' {
				break
			}
			p, r, err := compile(rest)
			if err != nil {
				return nil, "", err
			}
			parts = append(parts, p)
			rest = r
		}
		if len(parts) == 0 {
			return nil, "", fmt.Errorf("'%c' sem filtros", s[1])
		}
		return tlv(tag, parts...), rest[1:], nil
	case '!':
		p, r, err := compile(strings.TrimLeft(s[2:], " "))
		if err != nil {
			return nil, "", err
		}
		if r == "" || r[0] != ')' {
			return nil, "", fmt.Errorf("filtro '!' sem ')' final")
		}
		return tlv(0xa2, p), r[1:], nil
	}
	end := strings.IndexByte(s, ')')
	if end < 0 {
		return nil, "", fmt.Errorf("filtro sem ')' final")
	}
	item, err := compileItem(s[1:end])
	if err != nil {
		return nil, "", err
	}
	return item, s[end+1:], nil
}

func trunc(s string) string {
	if len(s) > 30 {
		return s[:30] + "…"
	}
	return s
}

func compileItem(it string) ([]byte, error) {
	eq := strings.IndexByte(it, '=')
	if eq <= 0 {
		return nil, fmt.Errorf("item de filtro inválido %q", it)
	}
	left, value := it[:eq], it[eq+1:]
	op := byte(0)
	if l := len(left); l > 0 {
		switch left[l-1] {
		case '~', '>', '<', ':':
			op = left[l-1]
			left = left[:l-1]
		}
	}
	if op != ':' && !validAttr(left) {
		return nil, fmt.Errorf("atributo inválido %q no filtro", left)
	}
	switch op {
	case '~':
		return ava(0xa8, left, value)
	case '>':
		return ava(0xa5, left, value)
	case '<':
		return ava(0xa6, left, value)
	case ':':
		return extensible(left, value)
	}
	if value == "*" {
		return berStr(0x87, left), nil
	}
	if strings.Contains(value, "*") {
		return substrings(left, value)
	}
	return ava(0xa3, left, value)
}

func validAttr(a string) bool {
	if a == "" {
		return false
	}
	for _, c := range a {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == ';') {
			return false
		}
	}
	return true
}

func unescape(v string) ([]byte, error) {
	out := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' {
			out = append(out, v[i])
			continue
		}
		if i+3 > len(v) {
			return nil, fmt.Errorf("escape inválido em %q", v)
		}
		b, err := hex.DecodeString(v[i+1 : i+3])
		if err != nil {
			return nil, fmt.Errorf("escape inválido em %q", v)
		}
		out = append(out, b[0])
		i += 2
	}
	return out, nil
}

func ava(tag byte, attr, value string) ([]byte, error) {
	v, err := unescape(value)
	if err != nil {
		return nil, err
	}
	return tlv(tag, berStr(tagOctetString, attr), tlv(tagOctetString, v)), nil
}

func substrings(attr, value string) ([]byte, error) {
	parts := strings.Split(value, "*")
	var subs [][]byte
	for i, p := range parts {
		if p == "" {
			continue
		}
		v, err := unescape(p)
		if err != nil {
			return nil, err
		}
		tag := byte(0x81) // any
		if i == 0 {
			tag = 0x80 // initial
		} else if i == len(parts)-1 {
			tag = 0x82 // final
		}
		subs = append(subs, tlv(tag, v))
	}
	return tlv(0xa4, berStr(tagOctetString, attr), tlv(tagSequence, subs...)), nil
}

// extensible encodes "attr[:dn][:rule]:=value" (e.g. AD's nested group rule
// member:1.2.840.113556.1.4.1941:=<dn>).
func extensible(left, value string) ([]byte, error) {
	fields := strings.Split(left, ":")
	attr, rule, dn := fields[0], "", false
	for _, f := range fields[1:] {
		switch {
		case strings.EqualFold(f, "dn"):
			dn = true
		case f != "" && rule == "":
			rule = f
		default:
			return nil, fmt.Errorf("regra extensível inválida %q", left)
		}
	}
	if attr == "" && rule == "" {
		return nil, fmt.Errorf("regra extensível sem atributo nem regra")
	}
	if attr != "" && !validAttr(attr) {
		return nil, fmt.Errorf("atributo inválido %q no filtro", attr)
	}
	v, err := unescape(value)
	if err != nil {
		return nil, err
	}
	var parts [][]byte
	if rule != "" {
		parts = append(parts, berStr(0x81, rule))
	}
	if attr != "" {
		parts = append(parts, berStr(0x82, attr))
	}
	parts = append(parts, tlv(0x83, v))
	if dn {
		parts = append(parts, berBool(0x84, true))
	}
	return tlv(0xa9, parts...), nil
}

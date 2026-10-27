package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"time"
)

const sessionCookie = "k8sfront_session"

// Auth implements a single-admin token login with stateless, HMAC-signed
// session cookies (HttpOnly, SameSite=Strict).
type Auth struct {
	token   []byte
	signKey []byte
	ttl     time.Duration
	secure  bool
}

// NewAuth derives the cookie signing key from the master key material.
func NewAuth(adminToken string, keyMaterial []byte, ttl time.Duration, secure bool) *Auth {
	mac := hmac.New(sha256.New, keyMaterial)
	mac.Write([]byte("k8sfront/session-signing/v1"))
	return &Auth{token: []byte(adminToken), signKey: mac.Sum(nil), ttl: ttl, secure: secure}
}

// CheckToken compares in constant time.
func (a *Auth) CheckToken(tok string) bool {
	return len(tok) > 0 && subtle.ConstantTimeCompare([]byte(tok), a.token) == 1
}

func (a *Auth) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, a.signKey)
	mac.Write(payload)
	return mac.Sum(nil)
}

// Issue sets a fresh session cookie.
func (a *Auth) Issue(w http.ResponseWriter) {
	payload := make([]byte, 8+16)
	binary.BigEndian.PutUint64(payload, uint64(time.Now().Add(a.ttl).Unix()))
	_, _ = rand.Read(payload[8:])
	v := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(a.sign(payload))
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: v, Path: "/", HttpOnly: true, Secure: a.secure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(a.ttl.Seconds())})
}

// Clear removes the session cookie.
func (a *Auth) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: a.secure,
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

var errUnauthorized = errors.New("unauthorized")

// Verify accepts a valid session cookie or an "Authorization: Bearer <admin token>".
func (a *Auth) Verify(r *http.Request) error {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if a.CheckToken(strings.TrimPrefix(h, "Bearer ")) {
			return nil
		}
		return errUnauthorized
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return errUnauthorized
	}
	p, s, ok := strings.Cut(c.Value, ".")
	if !ok {
		return errUnauthorized
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(p)
	sig, err2 := base64.RawURLEncoding.DecodeString(s)
	if err1 != nil || err2 != nil || len(payload) != 24 || !hmac.Equal(sig, a.sign(payload)) {
		return errUnauthorized
	}
	if time.Now().Unix() > int64(binary.BigEndian.Uint64(payload)) {
		return errUnauthorized
	}
	return nil
}

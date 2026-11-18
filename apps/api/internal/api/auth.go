package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

const sessionCookie = "k8sfront_session"

// Auth issues and verifies stateless, HMAC-signed session cookies (HttpOnly,
// SameSite=Strict). A session names a user and the user's session epoch, so
// disabling the user or changing the password revokes it. The admin token
// stays as break-glass access (login and "Authorization: Bearer").
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

// session is what a cookie proves. UserID "" means the admin token.
type session struct {
	UserID string
	Epoch  int64
}

// Issue sets a fresh session cookie: exp(8) | nonce(16) | epoch(8) | user id.
func (a *Auth) Issue(w http.ResponseWriter, userID string, epoch int64) {
	payload := make([]byte, 32, 32+len(userID))
	binary.BigEndian.PutUint64(payload, uint64(time.Now().Add(a.ttl).Unix()))
	_, _ = rand.Read(payload[8:24])
	binary.BigEndian.PutUint64(payload[24:], uint64(epoch))
	payload = append(payload, userID...)
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
func (a *Auth) Verify(r *http.Request) (*session, error) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if a.CheckToken(strings.TrimPrefix(h, "Bearer ")) {
			return &session{}, nil
		}
		return nil, errUnauthorized
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, errUnauthorized
	}
	p, s, ok := strings.Cut(c.Value, ".")
	if !ok {
		return nil, errUnauthorized
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(p)
	sig, err2 := base64.RawURLEncoding.DecodeString(s)
	// 24 bytes: admin-token cookie issued before user accounts existed.
	if err1 != nil || err2 != nil || (len(payload) != 24 && len(payload) < 32) || len(payload) > 32+64 || !hmac.Equal(sig, a.sign(payload)) {
		return nil, errUnauthorized
	}
	if time.Now().Unix() > int64(binary.BigEndian.Uint64(payload)) {
		return nil, errUnauthorized
	}
	if len(payload) == 24 {
		return &session{}, nil
	}
	return &session{Epoch: int64(binary.BigEndian.Uint64(payload[24:32])), UserID: string(payload[32:])}, nil
}

// SourceToken marks the break-glass admin token principal.
const SourceToken = "token"

// Principal is who is calling, resolved on every request (so a role change
// or a disabled account takes effect immediately).
type Principal struct {
	UserID      string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"displayName"`
	Role        store.Role `json:"role"`
	Source      string     `json:"source"`
}

var tokenPrincipal = Principal{Username: "admin", DisplayName: "Token de administrador", Role: store.RoleAdmin, Source: SourceToken}

type principalKey struct{}

func principalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// actor is the audit/job name of the caller.
func actor(r *http.Request) string {
	if p := principalFrom(r.Context()); p != nil {
		if p.Source == SourceToken {
			return "admin (token)"
		}
		return p.Username
	}
	return "anônimo"
}

func (s *Server) resolve(r *http.Request) (*Principal, error) {
	sess, err := s.Auth.Verify(r)
	if err != nil {
		return nil, err
	}
	if sess.UserID == "" {
		p := tokenPrincipal
		return &p, nil
	}
	u, err := s.Store.GetUser(r.Context(), sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if u.Disabled || u.SessionEpoch != sess.Epoch || !u.Role.Valid() {
		return nil, errUnauthorized
	}
	return &Principal{UserID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role, Source: u.Source}, nil
}

var roleLabel = map[store.Role]string{store.RoleViewer: "Leitura", store.RoleOperator: "Operador", store.RoleAdmin: "Administrador"}

// require authenticates the caller and checks the minimum role.
func (s *Server) require(min store.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.resolve(r)
		if err != nil {
			if !errors.Is(err, errUnauthorized) {
				s.storeError(w, err)
				return
			}
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if p.Role.Rank() < min.Rank() {
			writeError(w, http.StatusForbidden, "forbidden",
				fmt.Sprintf("seu papel (%s) não permite esta ação; é preciso %s", roleLabel[p.Role], roleLabel[min]))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

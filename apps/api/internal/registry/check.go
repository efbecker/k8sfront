package registry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// CheckResult is the outcome of a credentials check (no secret inside).
type CheckResult struct {
	OK            bool   `json:"ok"`
	Authenticated bool   `json:"authenticated"`
	Message       string `json:"message"`
	Endpoint      string `json:"endpoint"`
}

// Check calls GET /v2/ on the upstream endpoint (mirrors are not probed) and,
// when the registry asks for a token (Bearer) or Basic auth, authenticates
// with the stored credentials. Only the status is returned.
func Check(ctx context.Context, r *store.Registry, password string) CheckResult {
	res := CheckResult{Endpoint: r.Endpoint}
	client, err := httpClient(r)
	if err != nil {
		res.Message = err.Error()
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := get(ctx, client, r.Endpoint+"/v2/", "")
	if err != nil {
		res.Message = "sem resposta do registry: " + err.Error()
		return res
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		res.OK = true
		res.Message = "registry respondeu sem exigir login"
		if r.Username != "" {
			res.Message += " (as credenciais não foram testadas)"
		}
		return res
	}
	if resp.StatusCode != http.StatusUnauthorized {
		res.Message = fmt.Sprintf("resposta inesperada de /v2/: HTTP %d", resp.StatusCode)
		return res
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	scheme, params := parseChallenge(challenge)
	basic := ""
	if r.Username != "" {
		basic = "Basic " + basicAuth(r.Username, password)
	}
	switch scheme {
	case "basic":
		if basic == "" {
			res.Message = "o registry exige login: informe usuário e senha/token"
			return res
		}
		resp, err := get(ctx, client, r.Endpoint+"/v2/", basic)
		if err != nil {
			res.Message = err.Error()
			return res
		}
		resp.Body.Close()
		return authResult(res, resp.StatusCode)
	case "bearer":
		realm := params["realm"]
		u, err := url.Parse(realm)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			res.Message = "o registry indicou um servidor de token inválido"
			return res
		}
		q := u.Query()
		if s := params["service"]; s != "" {
			q.Set("service", s)
		}
		u.RawQuery = q.Encode()
		if basic == "" {
			// Anonymous token: public pulls work, private repositories need credentials.
			resp, err := get(ctx, client, u.String(), "")
			if err != nil {
				res.Message = err.Error()
				return res
			}
			resp.Body.Close()
			res.OK = resp.StatusCode == http.StatusOK
			res.Message = "acesso anônimo (somente imagens públicas)"
			if !res.OK {
				res.Message = fmt.Sprintf("o servidor de token recusou acesso anônimo (HTTP %d)", resp.StatusCode)
			}
			return res
		}
		resp, err := get(ctx, client, u.String(), basic)
		if err != nil {
			res.Message = err.Error()
			return res
		}
		resp.Body.Close()
		return authResult(res, resp.StatusCode)
	}
	res.Message = "desafio de autenticação não suportado: " + challenge
	return res
}

func authResult(res CheckResult, code int) CheckResult {
	switch code {
	case http.StatusOK:
		res.OK, res.Authenticated = true, true
		res.Message = "login aceito pelo registry"
	case http.StatusUnauthorized, http.StatusForbidden:
		res.Message = "o registry recusou usuário/senha"
	default:
		res.Message = fmt.Sprintf("resposta inesperada no login: HTTP %d", code)
	}
	return res
}

func httpClient(r *store.Registry) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if r.CACert != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(r.CACert)) {
			return nil, fmt.Errorf("CA inválido")
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	if r.SkipVerify {
		tr.TLSClientConfig.InsecureSkipVerify = true // explicit operator choice for this registry
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("redirecionamentos demais")
		}
		// Never forward credentials to another host.
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	}}, nil
}

func get(ctx context.Context, c *http.Client, u, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "k8sfront-registry-check")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp, nil
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// parseChallenge parses `Bearer realm="...",service="..."`.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				params[k] = v[1:]
				break
			}
			params[k] = v[1 : end+1]
			rest = v[end+2:]
		} else {
			val, r, _ := strings.Cut(v, ",")
			params[k] = strings.TrimSpace(val)
			rest = r
		}
	}
	return strings.ToLower(scheme), params
}

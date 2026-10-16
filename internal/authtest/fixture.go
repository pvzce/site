// Package authtest provides a local OIDC issuer and session store for authentication tests.
package authtest

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"pvzce-site/internal/auth"
)

const Secret = "test-client-secret-that-is-not-a-real-credential"

type Provider struct {
	Server *httptest.Server
	Issuer string
	Fault  string
	mu     sync.Mutex
	codes  map[string]url.Values
	key    *rsa.PrivateKey
}

func NewProvider(t testing.TB) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{codes: map[string]url.Values{}, key: key}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	p.Issuer = p.Server.URL
	t.Cleanup(p.Server.Close)
	return p
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": p.Issuer, "authorization_endpoint": p.Issuer + "/authorize", "token_endpoint": p.Issuer + "/token", "userinfo_endpoint": p.Issuer + "/userinfo", "jwks_uri": p.Issuer + "/global-keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	case "/.well-known/pvzce-site/jwks":
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "application", Algorithm: "RS256", Use: "sig"}}})
	case "/global-keys":
		http.Error(w, "global keys must not be used", http.StatusInternalServerError)
	case "/authorize":
		query := r.URL.Query()
		if query.Get("code_challenge_method") != "S256" || query.Get("nonce") == "" || query.Get("state") == "" {
			http.Error(w, "invalid authorization request", 400)
			return
		}
		code := rand.Text()
		p.mu.Lock()
		p.codes[code] = query
		p.mu.Unlock()
		callback, err := url.Parse(query.Get("redirect_uri"))
		if err != nil {
			http.Error(w, "invalid callback", 400)
			return
		}
		values := callback.Query()
		values.Set("state", query.Get("state"))
		values.Set("code", code)
		callback.RawQuery = values.Encode()
		http.Redirect(w, r, callback.String(), 302)
	case "/token":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", 400)
			return
		}
		p.mu.Lock()
		query, ok := p.codes[r.PostForm.Get("code")]
		delete(p.codes, r.PostForm.Get("code"))
		p.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || r.PostForm.Get("client_secret") != Secret || r.PostForm.Get("client_id") != "pvzce-site" || r.PostForm.Get("redirect_uri") != query.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != query.Get("code_challenge") {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		claims := map[string]any{"iss": p.Issuer, "sub": "test-subject", "aud": "pvzce-site", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": query.Get("nonce")}
		switch p.Fault {
		case "nonce":
			claims["nonce"] = "incorrect"
		case "audience":
			claims["aud"] = "another-client"
		case "issuer":
			claims["iss"] = "http://another-issuer"
		case "expired":
			claims["exp"] = time.Now().Add(-time.Hour).Unix()
		}
		payload, _ := json.Marshal(claims)
		key := p.key
		if p.Fault == "signature" {
			key, _ = rsa.GenerateKey(rand.Reader, 2048)
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "application"))
		if err != nil {
			http.Error(w, "signer failed", 500)
			return
		}
		signed, err := signer.Sign(payload)
		if err != nil {
			http.Error(w, "sign failed", 500)
			return
		}
		token, err := signed.CompactSerialize()
		if err != nil {
			http.Error(w, "serialize failed", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-access-token", "token_type": "Bearer", "expires_in": 3600, "id_token": token})
	case "/userinfo":
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		subject := "test-subject"
		if p.Fault == "userinfo" {
			subject = "another-subject"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"sub": subject, "preferred_username": "demo", "name": "<script>alert(1)</script>", "email": "demo@example.invalid"})
	default:
		http.NotFound(w, r)
	}
}

// Authorize simulates the browser redirect without following the application's callback.
func (p *Provider) Authorize(t testing.TB, location string) url.Values {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	internal, _ := url.Parse(p.Server.URL)
	u.Scheme = internal.Scheme
	u.Host = internal.Host
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	callback, err := url.Parse(response.Header.Get("Location"))
	if err != nil || response.StatusCode != 302 {
		t.Fatalf("authorization failed: %d", response.StatusCode)
	}
	return callback.Query()
}

type Memory struct {
	mu     sync.Mutex
	logins map[string]struct {
		Binding string
		Login   auth.Login
	}
	sessions map[string]auth.Session
}

func NewMemory() *Memory {
	return &Memory{logins: map[string]struct {
		Binding string
		Login   auth.Login
	}{}, sessions: map[string]auth.Session{}}
}
func (m *Memory) SaveLogin(_ context.Context, state, binding string, login auth.Login) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logins[state] = struct {
		Binding string
		Login   auth.Login
	}{binding, login}
	return nil
}
func (m *Memory) ConsumeLogin(_ context.Context, state, binding string) (auth.Login, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	login, ok := m.logins[state]
	if !ok || login.Binding != binding {
		return auth.Login{}, auth.ErrInvalidLogin
	}
	delete(m.logins, state)
	return login.Login, nil
}
func (m *Memory) CreateSession(_ context.Context, _ string, user auth.User, ttl time.Duration) (string, auth.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	token := rand.Text() + rand.Text()
	user.ID = 1
	session := auth.Session{User: user, CSRFToken: rand.Text() + rand.Text(), ExpiresAt: time.Now().Add(ttl)}
	m.sessions[token] = session
	return token, session, nil
}
func (m *Memory) GetSession(_ context.Context, token string) (auth.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[token]
	if !ok || !session.ExpiresAt.After(time.Now()) {
		return auth.Session{}, auth.ErrUnauthorized
	}
	return session, nil
}
func (m *Memory) DeleteSession(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
	return nil
}

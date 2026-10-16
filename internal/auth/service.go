package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"pvzce-site/internal/config"
)

type Service struct {
	cfg      config.Auth
	store    SessionStore
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	client   *http.Client
}

func New(ctx context.Context, cfg config.Auth, store SessionStore) (*Service, error) {
	if store == nil {
		return nil, errors.New("authentication session store is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	var roundTripper http.RoundTripper = transport
	if cfg.InternalURL != "" {
		public, err := url.Parse(cfg.IssuerURL)
		if err != nil {
			return nil, err
		}
		internal, err := url.Parse(cfg.InternalURL)
		if err != nil {
			return nil, err
		}
		roundTripper = &issuerTransport{base: transport, public: public, internal: internal}
	}
	client := &http.Client{Transport: roundTripper, Timeout: 5 * time.Second}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), cfg.IssuerURL)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("discover Casdoor: %w", err)
	}
	// Pin verification to this application's JWKS while retaining strict issuer and audience checks.
	keyContext := oidc.ClientContext(context.Background(), client)
	keys := oidc.NewRemoteKeySet(keyContext, cfg.IssuerURL+"/.well-known/"+url.PathEscape(cfg.Application)+"/jwks")
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	return &Service{
		cfg: cfg, store: store, provider: provider, client: client,
		verifier: oidc.NewVerifier(cfg.IssuerURL, keys, &oidc.Config{ClientID: cfg.ClientID, SupportedSigningAlgs: []string{oidc.RS256}}),
		oauth:    oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: endpoint, RedirectURL: cfg.PublicURL + "/auth/callback", Scopes: []string{oidc.ScopeOpenID, "profile", "email"}},
	}, nil
}

func (s *Service) Close()              { s.client.CloseIdleConnections() }
func (s *Service) Config() config.Auth { return s.cfg }

func (s *Service) LoginURL(ctx context.Context, binding string) (string, error) {
	state := rand.Text() + rand.Text()
	login := Login{Nonce: rand.Text() + rand.Text(), Verifier: oauth2.GenerateVerifier()}
	if err := s.store.SaveLogin(ctx, state, binding, login); err != nil {
		return "", err
	}
	return s.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(login.Verifier), oidc.Nonce(login.Nonce)), nil
}

func (s *Service) CompleteLogin(ctx context.Context, state, binding, code string) (string, Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if len(state) != 52 || len(binding) != 52 || code == "" {
		return "", Session{}, ErrInvalidLogin
	}
	login, err := s.store.ConsumeLogin(ctx, state, binding)
	if err != nil {
		return "", Session{}, err
	}
	ctx = oidc.ClientContext(ctx, s.client)
	token, err := s.oauth.Exchange(ctx, code, oauth2.VerifierOption(login.Verifier))
	if err != nil {
		return "", Session{}, ErrInvalidLogin
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return "", Session{}, ErrInvalidLogin
	}
	idToken, err := s.verifier.Verify(ctx, raw)
	if err != nil || idToken.Subject == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(login.Nonce)) != 1 {
		return "", Session{}, ErrInvalidLogin
	}
	info, err := s.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil || info.Subject != idToken.Subject {
		return "", Session{}, ErrInvalidLogin
	}
	var claims struct {
		Username string `json:"preferred_username"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		Picture  string `json:"picture"`
	}
	if err := info.Claims(&claims); err != nil {
		return "", Session{}, ErrInvalidLogin
	}
	user := User{Subject: idToken.Subject, Username: claims.Username, Name: claims.Name, Email: claims.Email, Picture: claims.Picture}
	return s.store.CreateSession(ctx, s.cfg.IssuerURL, user, s.cfg.SessionTTL)
}

func (s *Service) Session(ctx context.Context, token string) (Session, error) {
	return s.store.GetSession(ctx, token)
}
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, token)
}

// Internal network routing changes transport destinations without relaxing JWT issuer checks.
type issuerTransport struct {
	base             http.RoundTripper
	public, internal *url.URL
}

func (t *issuerTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
func (t *issuerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.EqualFold(request.URL.Host, t.public.Host) && request.URL.Scheme == t.public.Scheme {
		clone := request.Clone(request.Context())
		clone.URL.Host = t.internal.Host
		clone.URL.Scheme = t.internal.Scheme
		clone.Host = t.public.Host
		return t.base.RoundTrip(clone)
	}
	return t.base.RoundTrip(request)
}

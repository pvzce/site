package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/authtest"
	"pvzce-site/internal/config"
)

func TestAuthorizationCodeFlow(t *testing.T) {
	provider := authtest.NewProvider(t)
	cfg := config.Default().Auth
	cfg.IssuerURL = provider.Issuer
	cfg.ClientSecret = authtest.Secret
	ctx, cancel := context.WithCancel(context.Background())
	service, err := auth.New(ctx, cfg, authtest.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	cancel() // Discovery completion must not cancel future JWKS fetches.
	t.Cleanup(service.Close)
	binding := strings.Repeat("b", 52)
	location, err := service.LoginURL(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	callback := provider.Authorize(t, location)
	if _, _, err := service.CompleteLogin(context.Background(), callback.Get("state"), strings.Repeat("x", 52), callback.Get("code")); !errors.Is(err, auth.ErrInvalidLogin) {
		t.Fatalf("browser binding was not verified: %v", err)
	}
	token, session, err := service.CompleteLogin(context.Background(), callback.Get("state"), binding, callback.Get("code"))
	if err != nil || len(token) != 52 || session.User.Subject != "test-subject" || session.User.Username != "demo" {
		t.Fatalf("login failed: %v", err)
	}
	if _, _, err := service.CompleteLogin(context.Background(), callback.Get("state"), binding, callback.Get("code")); !errors.Is(err, auth.ErrInvalidLogin) {
		t.Fatalf("login replay accepted: %v", err)
	}
	if err := service.Logout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Session(context.Background(), token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("revoked session accepted: %v", err)
	}
}

func TestUntrustedTokensAreRejected(t *testing.T) {
	for _, fault := range []string{"nonce", "audience", "issuer", "expired", "signature", "userinfo"} {
		t.Run(fault, func(t *testing.T) {
			provider := authtest.NewProvider(t)
			provider.Fault = fault
			cfg := config.Default().Auth
			cfg.IssuerURL = provider.Issuer
			cfg.ClientSecret = authtest.Secret
			service, err := auth.New(context.Background(), cfg, authtest.NewMemory())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(service.Close)
			binding := strings.Repeat("b", 52)
			location, err := service.LoginURL(context.Background(), binding)
			if err != nil {
				t.Fatal(err)
			}
			callback := provider.Authorize(t, location)
			if _, _, err := service.CompleteLogin(context.Background(), callback.Get("state"), binding, callback.Get("code")); !errors.Is(err, auth.ErrInvalidLogin) {
				t.Fatalf("untrusted token accepted: %v", err)
			}
		})
	}
}

func TestInternalRoutingRetainsPublicIssuer(t *testing.T) {
	provider := authtest.NewProvider(t)
	provider.Issuer = "http://localhost:39999"
	cfg := config.Default().Auth
	cfg.IssuerURL = provider.Issuer
	cfg.InternalURL = provider.Server.URL
	cfg.ClientSecret = authtest.Secret
	service, err := auth.New(context.Background(), cfg, authtest.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	binding := strings.Repeat("b", 52)
	location, err := service.LoginURL(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(location, provider.Issuer+"/authorize") {
		t.Fatal("internal address leaked into browser login URL")
	}
	callback := provider.Authorize(t, location)
	if _, _, err := service.CompleteLogin(context.Background(), callback.Get("state"), binding, callback.Get("code")); err != nil {
		t.Fatalf("internal routing failed: %v", err)
	}
}

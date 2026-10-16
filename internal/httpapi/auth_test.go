package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/authtest"
	"pvzce-site/internal/config"
)

func TestBrowserLoginAndLogout(t *testing.T) {
	provider := authtest.NewProvider(t)
	app := httptest.NewUnstartedServer(nil)
	cfg := config.Default()
	cfg.Auth.IssuerURL = provider.Issuer
	cfg.Auth.PublicURL = "http://" + app.Listener.Addr().String()
	cfg.Auth.ClientSecret = authtest.Secret
	service, err := auth.New(context.Background(), cfg.Auth, authtest.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	app.Config.Handler = NewHandler(cfg, testLogger(), Dependencies{Auth: service, Ready: func(context.Context) error { return nil }})
	app.Start()
	t.Cleanup(app.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	response, err := client.Get(app.URL + "/api/v1/account/me")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("account endpoint allows anonymous access")
	}
	response, err = client.Get(app.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.Contains(string(body), "&lt;script&gt;") || strings.Contains(string(body), "<script>alert") {
		t.Fatal("login failed or profile was not escaped")
	}
	if response.Request.URL.Path != "/account" {
		t.Fatalf("callback did not redirect to account page: %s", response.Request.URL.Path)
	}
	response, err = client.Get(app.URL + "/api/v1/account/me")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Data auth.Session `json:"data"`
	}
	err = json.NewDecoder(response.Body).Decode(&payload)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if payload.Data.User.Username != "demo" || payload.Data.CSRFToken == "" {
		t.Fatal("invalid account response")
	}
	request, _ := http.NewRequest(http.MethodPost, app.URL+"/auth/logout", nil)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("logout without CSRF token was accepted")
	}
	request, _ = http.NewRequest(http.MethodPost, app.URL+"/auth/logout", nil)
	request.Header.Set("X-CSRF-Token", payload.Data.CSRFToken)
	request.Header.Set("Origin", "http://evil.example")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin logout was accepted")
	}
	request, _ = http.NewRequest(http.MethodPost, app.URL+"/auth/logout", nil)
	request.Header.Set("X-CSRF-Token", payload.Data.CSRFToken)
	request.Header.Set("Origin", app.URL)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("logout failed")
	}
	response, err = client.Get(app.URL + "/api/v1/account/me")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("logout did not revoke authentication")
	}
}

func TestInvalidCallbackAndDisabledAuthentication(t *testing.T) {
	handler := NewHandler(config.Default(), testLogger(), Dependencies{})
	for _, path := range []string{"/auth/login", "/auth/callback?code=fake&state=fake", "/api/v1/account/me"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 503 {
			t.Fatalf("%s: status=%d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/logout", nil))
	if w.Code != 405 {
		t.Fatal("GET logout must not change account state")
	}
}

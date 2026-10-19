package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/authtest"
	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
	"pvzce-site/internal/gamelogin"
	"pvzce-site/internal/workshop"
)

type gameLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (l *gameLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.Write(b)
}
func (l *gameLog) text() string { l.mu.Lock(); defer l.mu.Unlock(); return l.Buffer.String() }

type gameHTTPFixture struct {
	h       http.Handler
	db      *database.DB
	origin  string
	cookie  *http.Cookie
	account auth.Session
	logs    *gameLog
	ip      string
}

func gameHTTP(t *testing.T) *gameHTTPFixture {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL required for HTTP game handoff integration")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.URL = raw
	cfg.Database.QueryTimeout = 10 * time.Second
	db, e := database.Open(ctx, cfg.Database)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(db.Close)
	store, e := auth.NewStore(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	provider := authtest.NewProvider(t)
	app := httptest.NewUnstartedServer(nil)
	cfg.Auth.Enabled = true
	cfg.Auth.IssuerURL = provider.Issuer
	cfg.Auth.PublicURL = "http://" + app.Listener.Addr().String()
	cfg.Auth.ClientSecret = authtest.Secret
	accounts, e := auth.New(ctx, cfg.Auth, store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(accounts.Close)
	g := cfg.GameLogin
	g.Enabled = true
	g.Origin = cfg.Auth.PublicURL
	g.Versions = []string{"1.0.0", "1.1.0"}
	service, e := gamelogin.New(ctx, db, g)
	if e != nil {
		t.Fatal(e)
	}
	logs := &gameLog{}
	h := NewHandler(cfg, slog.New(slog.NewJSONHandler(logs, nil)), Dependencies{Auth: accounts, GameLogin: service, Workshop: &workshop.Service{DB: db}})
	app.Config.Handler = h
	app.Start()
	t.Cleanup(app.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	// Exercise authorization code + PKCE + signed ID token + UserInfo and the
	// production PostgreSQL website session store, not an injected account context.
	response, e := client.Get(app.URL + "/auth/login")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("OIDC login failed", response.StatusCode)
	}
	response, e = client.Get(app.URL + "/api/v1/account/me")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	var payload struct {
		Data auth.Session `json:"data"`
	}
	if e = json.NewDecoder(response.Body).Decode(&payload); e != nil || response.StatusCode != 200 || payload.Data.User.ID == 0 {
		t.Fatal("verified website account missing", e)
	}
	var cookie *http.Cookie
	u, _ := url.Parse(app.URL)
	for _, c := range jar.Cookies(u) {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("website cookie missing")
	}
	t.Cleanup(func() {
		if _, e := db.Exec(ctx, `DELETE FROM pvzce_auth.sessions WHERE user_id=$1`, payload.Data.User.ID); e != nil {
			t.Error(e)
		}
		if _, e := db.Exec(ctx, `DELETE FROM pvzce_auth.users WHERE id=$1`, payload.Data.User.ID); e != nil {
			t.Error(e)
		}
	})
	return &gameHTTPFixture{h, db, app.URL, cookie, payload.Data, logs, fmt.Sprintf("192.0.2.%d:1234", payload.Data.User.ID%250+1)}
}
func (f *gameHTTPFixture) request(method, path, body string, website bool, bearer string) *http.Request {
	r := httptest.NewRequest(method, f.origin+path, strings.NewReader(body))
	r.RemoteAddr = f.ip
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if website {
		r.AddCookie(f.cookie)
		r.Header.Set("X-CSRF-Token", f.account.CSRFToken)
		r.Header.Set("Origin", f.origin)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}
func (f *gameHTTPFixture) send(t *testing.T, r *http.Request, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: want %d got %d: %s", r.Method, r.URL.Path, want, w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("sensitive response is cacheable")
	}
	return w
}
func (f *gameHTTPFixture) issue(t *testing.T) gamelogin.Ticket {
	t.Helper()
	w := f.send(t, f.request("POST", "/api/v1/launcher/login-codes", `{"protocol":1,"game_id":"pvzce","game_version":"1.0.0"}`, true, ""), 200)
	var result struct {
		Data gamelogin.Ticket `json:"data"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	return result.Data
}
func (f *gameHTTPFixture) exchange(t *testing.T, ticket gamelogin.Ticket) gamelogin.Grant {
	t.Helper()
	b, _ := json.Marshal(gamelogin.ExchangeRequest{Request: gamelogin.Request{Protocol: 1, GameID: "pvzce", GameVersion: "1.0.0"}, LoginCode: ticket.LoginCode})
	w := f.send(t, f.request("POST", "/api/v1/game/login-code/exchange", string(b), false, ""), 200)
	var result struct {
		Data gamelogin.Grant `json:"data"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	return result.Data
}
func TestGameHTTPHandoffIntegration(t *testing.T) {
	f := gameHTTP(t)
	w := f.send(t, f.request("GET", "/api/v1/launcher/capabilities", "", false, ""), 200)
	if !strings.Contains(w.Body.String(), `"game_login_protocol":1`) {
		t.Fatal("capability missing")
	}
	ticket := f.issue(t)
	grant := f.exchange(t, ticket)
	if grant.User.ID != f.account.User.ID || grant.TokenType != "Bearer" || grant.User.DisplayName != f.account.User.Name {
		t.Fatal("verified identity changed")
	}
	w = f.send(t, f.request("GET", "/api/v1/game/session", "", false, grant.AccessToken), 200)
	if strings.Contains(w.Body.String(), grant.AccessToken) || strings.Contains(w.Body.String(), "email") || strings.Contains(w.Body.String(), "subject") || strings.Contains(w.Body.String(), "refresh") {
		t.Fatal("session exposes excess identity or token")
	}
	f.send(t, f.request("GET", "/api/v1/game/session", "", true, ""), 401)
	f.send(t, f.request("GET", "/api/v1/game/session", "", false, f.cookie.Value), 401)
	f.send(t, f.request("GET", "/api/v1/account/me", "", false, grant.AccessToken), 401)
	f.send(t, f.request("POST", "/auth/logout", "", false, grant.AccessToken), 401)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/projects"}, {"GET", "/api/v1/author/projects"}, {"PUT", "/api/v1/projects/1/uploads/package"},
		{"POST", "/api/v1/projects/1/transitions"}, {"GET", "/api/v1/review-queue"}, {"GET", "/api/v1/review/files/1"}, {"POST", "/api/v1/admin/accounts/1"}, {"GET", "/api/v1/admin/accounts"},
	} {
		f.send(t, f.request(tc.method, tc.path, "", false, grant.AccessToken), 401)
	}
	disguised := f.request("GET", "/api/v1/account/me", "", false, "")
	disguised.AddCookie(&http.Cookie{Name: sessionCookie, Value: grant.AccessToken})
	f.send(t, disguised, 401)

	f.send(t, f.request("POST", "/api/v1/launcher/login-codes", `{"protocol":1,"game_id":"pvzce","game_version":"1.0.0"}`, false, grant.AccessToken), 403)
	f.send(t, f.request("POST", "/api/v1/game/logout", "", false, grant.AccessToken), 204)
	f.send(t, f.request("GET", "/api/v1/game/session", "", false, grant.AccessToken), 401)
	second := f.issue(t)
	active := f.exchange(t, second)
	pending := f.issue(t)
	f.send(t, f.request("POST", "/auth/logout", "", true, ""), 204)
	f.send(t, f.request("GET", "/api/v1/game/session", "", false, active.AccessToken), 401)
	b, _ := json.Marshal(gamelogin.ExchangeRequest{Request: gamelogin.Request{Protocol: 1, GameID: "pvzce", GameVersion: "1.0.0"}, LoginCode: pending.LoginCode})
	f.send(t, f.request("POST", "/api/v1/game/login-code/exchange", string(b), false, ""), 401)
	for _, secret := range []string{ticket.LoginCode, grant.AccessToken, active.AccessToken, pending.LoginCode, f.cookie.Value, f.account.CSRFToken} {
		if strings.Contains(f.logs.text(), secret) {
			t.Fatal("credentials leaked to access logs")
		}
	}
}
func TestGameHTTPValidationIntegration(t *testing.T) {
	f := gameHTTP(t)
	valid := `{"protocol":1,"game_id":"pvzce","game_version":"1.0.0"}`
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"unknown-account", `{"protocol":1,"game_id":"pvzce","game_version":"1.0.0","user_id":1}`, 400},
		{"duplicate", `{"protocol":1,"protocol":1,"game_id":"pvzce","game_version":"1.0.0"}`, 400},
		{"case-alias", `{"Protocol":1,"game_id":"pvzce","game_version":"1.0.0"}`, 400},
		{"trailing", valid + `{}`, 400}, {"array", `[]`, 400}, {"null", `null`, 400}, {"missing", `{}`, 400},
		{"wrong-protocol", `{"protocol":2,"game_id":"pvzce","game_version":"1.0.0"}`, 400},
		{"wrong-game", `{"protocol":1,"game_id":"other","game_version":"1.0.0"}`, 400},
		{"unlisted-version", `{"protocol":1,"game_id":"pvzce","game_version":"9.0.0"}`, 403},
		{"oversize", strings.Repeat("x", 2049), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.send(t, f.request("POST", "/api/v1/launcher/login-codes", tc.body, true, ""), tc.want)
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*http.Request)
		want   int
	}{
		{"missing-origin", func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"wrong-origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403},
		{"wrong-csrf", func(r *http.Request) { r.Header.Set("X-CSRF-Token", "secret-not-for-errors") }, 403},
		{"missing-cookie", func(r *http.Request) { r.Header.Del("Cookie") }, 401},
		{"expired-cookie", func(r *http.Request) { r.Header.Set("Cookie", sessionCookie+"=invalid-cookie") }, 401},
		{"cross-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"wrong-host", func(r *http.Request) {
			r.Host = "evil.example"
			r.Header.Set("X-Forwarded-Host", strings.TrimPrefix(f.origin, "http://"))
		}, 403},
		{"query", func(r *http.Request) { r.URL.RawQuery = "login_code=secret-not-for-errors" }, 400},
		{"form", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, 400},
		{"duplicate-csrf", func(r *http.Request) { r.Header.Add("X-CSRF-Token", f.account.CSRFToken) }, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.request("POST", "/api/v1/launcher/login-codes", valid, true, "")
			tc.change(r)
			w := f.send(t, r, tc.want)
			if strings.Contains(w.Body.String(), "secret-not-for-errors") {
				t.Fatal("error echoed secret")
			}
		})
	}
	ticket := f.issue(t)
	b, _ := json.Marshal(gamelogin.ExchangeRequest{Request: gamelogin.Request{Protocol: 1, GameID: "pvzce", GameVersion: "1.1.0"}, LoginCode: ticket.LoginCode})
	f.send(t, f.request("POST", "/api/v1/game/login-code/exchange", string(b), false, ""), 401)
	grant := f.exchange(t, ticket)
	for _, path := range []string{"/api/v1/game/session?access_token=" + grant.AccessToken, "/api/v1/game/session?login_code=" + ticket.LoginCode} {
		f.send(t, f.request("GET", path, "", false, grant.AccessToken), 400)
	}
	r := f.request("GET", "/api/v1/game/session", "", false, grant.AccessToken)
	r.Header.Add("Authorization", "Bearer "+grant.AccessToken)
	f.send(t, r, 401)
	// Credentials mistakenly included in an unrecognized route or a panic must not be logged.
	f.send(t, f.request("GET", "/api/v1/game/"+ticket.LoginCode, "", false, ""), 404)
	panicHandler := middleware(slog.New(slog.NewJSONHandler(f.logs, nil)), 4096, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(grant.AccessToken) }))
	w := httptest.NewRecorder()
	panicHandler.ServeHTTP(w, f.request("GET", "/api/v1/game/session", "", false, grant.AccessToken))
	if w.Code != 500 {
		t.Fatal("panic not handled")
	}
	for _, secret := range []string{ticket.LoginCode, grant.AccessToken, "secret-not-for-errors"} {
		if strings.Contains(f.logs.text(), secret) {
			t.Fatal("credentials in logs")
		}
	}
}
func TestGameHTTPLimitAndAvailabilityIntegration(t *testing.T) {
	f := gameHTTP(t)
	// Saturate only this fixture's network bucket, shared across replicas.
	ip, _, _ := strings.Cut(f.ip, ":")
	sum := sha256.Sum256([]byte("exchange-ip:" + ip))
	if _, e := f.db.Exec(context.Background(), `INSERT INTO pvzce_game.rate_limits(key_hash,window_start,attempts) VALUES($1,date_trunc('minute',clock_timestamp()),60) ON CONFLICT(key_hash) DO UPDATE SET window_start=excluded.window_start,attempts=60`, sum[:]); e != nil {
		t.Fatal(e)
	}
	w := f.send(t, f.request("POST", "/api/v1/game/login-code/exchange", `{}`, false, ""), 429)
	if w.Header().Get("Retry-After") != "60" {
		t.Fatal("retry guidance missing")
	}
}
func TestGameDisabledAndStrictJSON(t *testing.T) {
	h := NewHandler(config.Default(), testLogger(), Dependencies{})
	for _, tc := range []struct{ method, path string }{{"GET", "/api/v1/launcher/capabilities"}, {"POST", "/api/v1/launcher/login-codes"}, {"POST", "/api/v1/game/login-code/exchange"}, {"GET", "/api/v1/game/session"}, {"POST", "/api/v1/game/logout"}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != 503 || strings.Contains(w.Body.String(), "game_login_protocol") {
			t.Fatal("disabled service advertised capability")
		}
	}
	for _, body := range []string{`{"login_code":"secret"}`, `{"protocol":1,"Protocol":1}`, `{"protocol":1}null`, `{"protocol":1,}`, `null`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var target gamelogin.Request
		if gameJSON(w, r, &target) {
			t.Fatal("accepted malformed JSON")
		}
	}
}

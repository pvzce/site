package httpapi

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/authtest"
	"pvzce-site/internal/config"
	"pvzce-site/internal/workshop"
)

func TestStreamingFormCSRFFirst(t *testing.T) {
	provider := authtest.NewProvider(t)
	cfg := config.Default().Auth
	cfg.IssuerURL = provider.Issuer
	cfg.ClientSecret = authtest.Secret
	cfg.PublicURL = "http://localhost:8080"
	store := authtest.NewMemory()
	svc, e := auth.New(context.Background(), cfg, store)
	if e != nil {
		t.Fatal(e)
	}
	defer svc.Close()
	token, session, e := store.CreateSession(context.Background(), provider.Issuer, auth.User{ID: 1}, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	called := false
	handler := RequireAccount(svc, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		parts, ok := r.Context().Value(uploadPartsKey{}).(*multipart.Reader)
		if !ok {
			t.Fatal("multipart stream lost")
		}
		part, e := parts.NextPart()
		if e != nil || part.FormName() != "file" {
			t.Fatal("file not left for streaming", e)
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		name, csrf string
		first      bool
		origin     string
		want       int
	}{{"valid", session.CSRFToken, true, cfg.PublicURL, 204}, {"wrong", "bad", true, cfg.PublicURL, 403}, {"file-first", session.CSRFToken, false, cfg.PublicURL, 403}, {"cross-site", session.CSRFToken, true, "http://evil.invalid", 403}} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			if tc.first {
				_ = writer.WriteField("csrf_token", tc.csrf)
			}
			file, _ := writer.CreateFormFile("file", "pack.zip")
			_, _ = file.Write([]byte("fixture"))
			if !tc.first {
				_ = writer.WriteField("csrf_token", tc.csrf)
			}
			_ = writer.Close()
			r := httptest.NewRequest("POST", "/projects/1/upload", &body)
			r.Header.Set("Content-Type", writer.FormDataContentType())
			r.Header.Set("Origin", tc.origin)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
			w := httptest.NewRecorder()
			called = false
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || called != (tc.want == 204) {
				t.Fatalf("HTTP %d called=%v", w.Code, called)
			}
		})
	}
}
func TestWorkshopRequiresLoginAndEscapes(t *testing.T) {
	cfg := config.Default()
	h := NewHandler(cfg, testLogger(), Dependencies{Workshop: &workshop.Service{}})
	r := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(`{"owner_id":1,"role":"admin"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("missing auth did not fail closed", w.Code)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/profile", nil)
	r = r.WithContext(withAccount(r.Context(), auth.Session{User: auth.User{ID: 1}}))
	renderWorkshop(w, r, "profile", map[string]any{"Profile": workshop.Actor{DisplayName: "<script>alert(1)</script>"}, "Usage": nil})
	if strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("template injected raw script")
	}
}

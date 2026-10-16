package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"pvzce-site/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestHTTPContract(t *testing.T) {
	s := httptest.NewServer(NewHandler(config.Default(), testLogger(), Dependencies{Ready: func(context.Context) error { return nil }}))
	defer s.Close()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodGet, "/api/v1/info", http.StatusOK},
		{http.MethodHead, "/api/v1/info", http.StatusOK},
		{http.MethodGet, "/health/live", http.StatusOK},
		{http.MethodGet, "/health/ready", http.StatusOK},
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodGet, "/api/v1/files/example.txt", http.StatusNotFound},
		{http.MethodGet, "/api/v1/info/extra", http.StatusNotFound},
		{http.MethodPost, "/api/v1/info", http.StatusMethodNotAllowed},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
		{http.MethodPost, "/missing", http.StatusNotFound},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, s.URL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Request-ID", "untrusted-client-value")
			resp, err := s.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d, want %d", resp.StatusCode, tc.status)
			}
			id := resp.Header.Get("X-Request-ID")
			if id == "" || id == "untrusted-client-value" {
				t.Fatalf("invalid request ID: %q", id)
			}
			if resp.Header.Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("unexpected content type: %s", resp.Header.Get("Content-Type"))
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if tc.method == http.MethodHead {
				if len(body) != 0 {
					t.Fatal("HEAD response has a body")
				}
				return
			}
			if !json.Valid(body) {
				t.Fatalf("invalid JSON: %s", body)
			}
			if tc.status >= 400 {
				var decoded errorResponse
				if err := json.Unmarshal(body, &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded.Error.RequestID != id || decoded.Error.Code == "" {
					t.Fatalf("invalid error response: %s", body)
				}
			}
			if tc.status == http.StatusMethodNotAllowed && resp.Header.Get("Allow") != "GET, HEAD" {
				t.Fatal("method error has no Allow header")
			}
		})
	}
}

func TestReadinessDoesNotAffectLiveness(t *testing.T) {
	handler := NewHandler(config.Default(), testLogger(), Dependencies{Ready: func(context.Context) error { return errors.New("unavailable") }})
	for path, status := range map[string]int{"/health/live": 200, "/health/ready": 503} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status {
			t.Fatalf("%s: status=%d, want %d", path, w.Code, status)
		}
	}
}

func BenchmarkInfo(b *testing.B) {
	handler := NewHandler(config.Default(), testLogger(), Dependencies{Ready: func(context.Context) error { return nil }})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
	b.ReportAllocs()
	for b.Loop() {
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
}

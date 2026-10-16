package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONInputLimits(t *testing.T) {
	handler := middleware(testLogger(), 64, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name string `json:"name"`
		}
		if decodeJSON(w, r, &input) {
			writeJSON(w, http.StatusOK, input)
		}
	}))
	for _, tc := range []struct {
		name, contentType, body string
		unknownLength           bool
		status                  int
	}{
		{"valid", "application/json; charset=utf-8", `{"name":"test"}`, false, 200},
		{"unsupported type", "text/plain", `{"name":"test"}`, false, 415},
		{"invalid JSON", "application/json", `{`, false, 400},
		{"unknown field", "application/json", `{"other":"test"}`, false, 400},
		{"multiple values", "application/json", `{"name":"test"} {}`, false, 400},
		{"known length", "application/json", `{"name":"` + strings.Repeat("a", 64) + `"}`, false, 413},
		{"unknown length", "application/json", `{"name":"` + strings.Repeat("a", 64) + `"}`, true, 413},
		{"trailing bytes", "application/json", `{"name":"test"}` + strings.Repeat(" ", 64), true, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			if tc.unknownLength {
				req.ContentLength = -1
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestPanicRecovery(t *testing.T) {
	handler := middleware(testLogger(), 1024, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("private implementation detail")
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test", nil))
	if w.Code != 500 || !strings.Contains(w.Body.String(), "internal_error") ||
		strings.Contains(w.Body.String(), "private implementation detail") {
		t.Fatalf("unexpected panic response: %d %s", w.Code, w.Body.String())
	}
}

func TestPanicAfterCommitAbortsResponse(t *testing.T) {
	for _, flush := range []bool{false, true} {
		handler := middleware(testLogger(), 1024, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if flush {
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Fatal(err)
				}
			} else {
				_, _ = io.WriteString(w, "partial response")
			}
			panic("after commit")
		}))
		func() {
			defer func() {
				if value := recover(); value != http.ErrAbortHandler {
					t.Errorf("expected connection abort, got %v", value)
				}
			}()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/test", nil))
		}()
	}
}

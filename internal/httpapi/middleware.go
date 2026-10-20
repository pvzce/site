package httpapi

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type requestIDKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func middleware(logger *slog.Logger, maxBodyBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := rand.Text()
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		tracked := &responseWriter{ResponseWriter: w}
		defer func() {
			status := tracked.status
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			// Routine probe traffic stays out of production INFO logs.
			if (r.URL.Path == "/health/live" || r.URL.Path == "/health/ready") && status < 400 {
				level = slog.LevelDebug
			}
			if status >= 500 {
				level = slog.LevelError
			}
			logger.Log(r.Context(), level, "http request", "request_id", id,
				"method", r.Method, "path", logPath(r.URL.Path), "status", status,
				"bytes", tracked.bytes, "duration_ms", float64(time.Since(started).Microseconds())/1000)
		}()
		defer func() {
			if value := recover(); value != nil {
				if value == http.ErrAbortHandler {
					panic(value)
				}
				if sensitiveGamePath(r.URL.Path) {
					logger.ErrorContext(r.Context(), "handler panic", "request_id", id, "stack", string(debug.Stack()))
				} else {
					logger.ErrorContext(r.Context(), "handler panic", "request_id", id,
						"panic", value, "stack", string(debug.Stack()))
				}
				if tracked.status != 0 {
					// A response already sent cannot be replaced with a JSON error.
					panic(http.ErrAbortHandler)
				}
				writeError(tracked, r, http.StatusInternalServerError, "internal_error", "Internal server error")
			}
		}()
		if r.ContentLength > maxBodyBytes {
			writeError(tracked, r, http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large")
			return
		}
		// Also limit bodies with unknown length, including chunked transfer encoding.
		r.Body = http.MaxBytesReader(tracked, r.Body, maxBodyBytes)
		next.ServeHTTP(tracked, r)
	})
}

func sensitiveGamePath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/game/") || strings.HasPrefix(path, "/api/v1/launcher/")
}
func logPath(path string) string {
	if sensitiveGamePath(path) {
		switch path {
		case "/api/v1/launcher/capabilities", "/api/v1/launcher/login-codes", "/api/v1/game/login-code/exchange", "/api/v1/game/session", "/api/v1/game/logout":
			return path
		default:
			return "/api/v1/game/[redacted]"
		}
	}
	return path
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

// Unwrap preserves http.ResponseController support for deadlines, flushing, and hijacking.
func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *responseWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

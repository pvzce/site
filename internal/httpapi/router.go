package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/buildinfo"
	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
	"pvzce-site/internal/fileio"
)

// Dependencies are initialized once at startup and shared by business handlers.
// Files is an internal reader and is not exposed through a download route.
type Dependencies struct {
	Database *database.DB
	Files    *fileio.Directories
	Ready    func(context.Context) error
	Auth     *auth.Service
}

// NewHandler is the composition point for HTTP routes and shared middleware.
func NewHandler(cfg config.Config, logger *slog.Logger, deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	registerAccountRoutes(mux, deps.Auth, logger)
	info := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]string{
				"name":        buildinfo.Name,
				"version":     buildinfo.Version,
				"environment": cfg.Environment,
			},
		})
	}
	handleGET(mux, "/{$}", info)
	handleGET(mux, "/api/v1/info", info)
	handleGET(mux, "/health/live", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	handleGET(mux, "/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if deps.Ready == nil || deps.Ready(r.Context()) != nil {
			writeError(w, r, http.StatusServiceUnavailable, "not_ready", "Service is not ready")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "Route not found")
	})
	return middleware(logger, cfg.MaxBodyBytes, mux)
}

// A path-only fallback keeps method errors in the same JSON format as other API errors.
func handleGET(mux *http.ServeMux, path string, handler http.HandlerFunc) {
	mux.HandleFunc("GET "+path, handler)
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	})
}

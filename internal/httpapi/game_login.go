package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"pvzce-site/internal/config"
	"pvzce-site/internal/gamelogin"
)

func registerGameLogin(mux *http.ServeMux, deps Dependencies) {
	s := deps.GameLogin
	guard := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			if s == nil || deps.Auth == nil {
				gameError(w, r, errors.New("not configured"))
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
			origin, err := config.GameLoginOrigin(strings.Split(s.Origin(), "://")[0]+"://"+r.Host, true)
			// Forwarded headers never determine the destination or the trusted origin.
			if err != nil || origin != s.Origin() || len(r.Header.Values("Origin")) > 1 || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.Origin()) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				gameError(w, r, gamelogin.ErrForbidden)
				return
			}
			if r.URL.RawQuery != "" || r.URL.ForceQuery {
				gameError(w, r, gamelogin.ErrInvalid)
				return
			}
			next(w, r)
		}
	}
	handleGET(mux, "/api/v1/launcher/capabilities", guard(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Ready(r.Context()); err != nil {
			gameError(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"data": map[string]int{"game_login_protocol": 1}})
	}))
	post := func(path string, h http.HandlerFunc) {
		mux.HandleFunc("POST "+path, guard(h))
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", "POST")
			writeError(w, r, 405, "method_not_allowed", "Method not allowed")
		})
	}
	limit := func(w http.ResponseWriter, r *http.Request, category string, max int) bool {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = "unknown"
		}
		if err = s.Limit(r.Context(), category, ip, max); err != nil {
			gameError(w, r, err)
			return false
		}
		return true
	}
	post("/api/v1/launcher/login-codes", func(w http.ResponseWriter, r *http.Request) {
		if !limit(w, r, "issue-ip", 60) {
			return
		}
		if r.Header.Get("Origin") != s.Origin() || len(r.Header.Values("X-CSRF-Token")) != 1 || len(r.Header.Values("Authorization")) != 0 {
			gameError(w, r, gamelogin.ErrForbidden)
			return
		}
		var request gamelogin.Request
		if !gameJSON(w, r, &request) {
			return
		}
		// The existing website gate verifies Cookie + CSRF; the transaction rechecks
		// its source session and account before creating the authorization binding.
		RequireAccount(deps.Auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, _ := r.Cookie(sessionCookie)
			ticket, err := s.Issue(r.Context(), cookie.Value, r.Header.Get("X-CSRF-Token"), request)
			if err != nil {
				gameError(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"data": ticket})
		})).ServeHTTP(w, r)
	})
	post("/api/v1/game/login-code/exchange", func(w http.ResponseWriter, r *http.Request) {
		if !limit(w, r, "exchange-ip", 60) {
			return
		}
		if len(r.Header.Values("Authorization")) != 0 {
			gameError(w, r, gamelogin.ErrInvalid)
			return
		}
		var request gamelogin.ExchangeRequest
		if !gameJSON(w, r, &request) {
			return
		}
		grant, err := s.Exchange(r.Context(), request)
		if err != nil {
			gameError(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"data": grant})
	})
	session := func(logout bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !limit(w, r, "session-ip", 120) {
				return
			}
			if len(r.Header.Values("Authorization")) != 1 {
				gameError(w, r, gamelogin.ErrUnauthorized)
				return
			}
			token := gamelogin.Bearer(r.Header.Get("Authorization"))
			if token == "" {
				gameError(w, r, gamelogin.ErrUnauthorized)
				return
			}
			if r.ContentLength != 0 {
				gameError(w, r, gamelogin.ErrInvalid)
				return
			}
			result, err := s.Session(r.Context(), token, logout)
			if err != nil {
				gameError(w, r, err)
				return
			}
			if logout {
				w.WriteHeader(204)
				return
			}
			writeJSON(w, 200, map[string]any{"data": result})
		}
	}
	handleGET(mux, "/api/v1/game/session", guard(session(false)))
	post("/api/v1/game/logout", session(true))
}

// Bounded JSON objects only: no unknown/duplicate fields, trailing values, or
// form/query credentials. Decoder errors are never returned to clients or logs.
func gameJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		gameError(w, r, gamelogin.ErrInvalid)
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2049))
	if err != nil || len(body) > 2048 {
		gameError(w, r, gamelogin.ErrInvalid)
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		gameError(w, r, gamelogin.ErrInvalid)
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			gameError(w, r, gamelogin.ErrInvalid)
			return false
		}
		name, ok := key.(string)
		_, exchange := target.(*gamelogin.ExchangeRequest)
		allowed := name == "protocol" || name == "game_id" || name == "game_version" || exchange && name == "login_code"
		if !ok || !allowed || seen[name] {
			gameError(w, r, gamelogin.ErrInvalid)
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			gameError(w, r, gamelogin.ErrInvalid)
			return false
		}
	}
	if _, err = d.Token(); err != nil {
		gameError(w, r, gamelogin.ErrInvalid)
		return false
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		gameError(w, r, gamelogin.ErrInvalid)
		return false
	}
	d = json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		gameError(w, r, gamelogin.ErrInvalid)
		return false
	}
	return true
}
func gameError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 503, "game_login_unavailable", "Game login is unavailable; continue offline"
	switch {
	case errors.Is(err, gamelogin.ErrInvalid):
		status, code, message = 400, "invalid_game_request", "Invalid game login request"
	case errors.Is(err, gamelogin.ErrUnauthorized):
		status, code, message = 401, "game_unauthorized", "Game login has expired or is invalid; start again from the launcher"
	case errors.Is(err, gamelogin.ErrForbidden):
		status, code, message = 403, "game_forbidden", "Game login is not permitted"
	case errors.Is(err, gamelogin.ErrRateLimited):
		status, code, message = 429, "game_rate_limited", "Too many requests; try again later"
		w.Header().Set("Retry-After", "60")
	}
	writeError(w, r, status, code, message)
}

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"pvzce-site/internal/auth"
)

const sessionCookie = "pvzce_session"
const loginCookie = "pvzce_login"

type accountKey struct{}

// Account returns the session installed by RequireAccount.
func Account(ctx context.Context) (auth.Session, bool) {
	session, ok := ctx.Value(accountKey{}).(auth.Session)
	return session, ok
}

// RequireAccount protects business routes, including CSRF validation for state-changing methods.
func RequireAccount(service *auth.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeError(w, r, 503, "auth_unavailable", "Account service is not configured")
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, r, 401, "unauthorized", "Login is required")
			return
		}
		session, err := service.Session(r.Context(), cookie.Value)
		if errors.Is(err, auth.ErrUnauthorized) {
			writeError(w, r, 401, "unauthorized", "Login is required")
			return
		}
		if err != nil {
			writeError(w, r, 503, "auth_unavailable", "Account service is unavailable")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			csrf := r.Header.Get("X-CSRF-Token")
			if csrf == "" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
				if err := r.ParseForm(); err != nil {
					writeError(w, r, 400, "invalid_form", "Invalid form")
					return
				}
				csrf = r.PostForm.Get("csrf_token")
			}
			origin := r.Header.Get("Origin")
			if (origin != "" && origin != service.Config().PublicURL) || r.Header.Get("Sec-Fetch-Site") == "cross-site" ||
				csrf == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(session.CSRFToken)) != 1 {
				writeError(w, r, 403, "invalid_csrf", "Request could not be verified")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountKey{}, session)))
	})
}

//go:embed account.html
var accountSource string
var accountTemplate = template.Must(template.New("account").Parse(accountSource))

func registerAccountRoutes(mux *http.ServeMux, service *auth.Service, logger *slog.Logger) {
	handleGET(mux, "/auth/login", func(w http.ResponseWriter, r *http.Request) {
		authHeaders(w)
		if service == nil {
			writeError(w, r, 503, "auth_unavailable", "Account service is not configured")
			return
		}
		binding := rand.Text() + rand.Text()
		location, err := service.LoginURL(r.Context(), binding)
		if err != nil {
			logger.ErrorContext(r.Context(), "start login failed", "request_id", RequestID(r.Context()))
			writeError(w, r, 503, "auth_unavailable", "Account service is unavailable")
			return
		}
		setLoginCookie(w, service, binding, 300)
		http.Redirect(w, r, location, http.StatusFound)
	})
	handleGET(mux, "/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		authHeaders(w)
		if service == nil {
			writeError(w, r, 503, "auth_unavailable", "Account service is not configured")
			return
		}
		cookie, err := r.Cookie(loginCookie)
		setLoginCookie(w, service, "", -1)
		if err != nil || r.URL.Query().Get("error") != "" {
			writeError(w, r, 401, "login_failed", "Login was not completed; please try again")
			return
		}
		token, session, err := service.CompleteLogin(r.Context(), r.URL.Query().Get("state"), cookie.Value, r.URL.Query().Get("code"))
		if err != nil {
			if errors.Is(err, auth.ErrInvalidLogin) {
				writeError(w, r, 401, "login_failed", "Login could not be verified; please try again")
				return
			}
			logger.ErrorContext(r.Context(), "complete login failed", "request_id", RequestID(r.Context()))
			writeError(w, r, 503, "auth_unavailable", "Account service is unavailable")
			return
		}
		// Replace and revoke any previous local session to avoid session fixation.
		if old, err := r.Cookie(sessionCookie); err == nil && old.Value != token {
			_ = service.Logout(r.Context(), old.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: service.Config().CookieSecure, SameSite: http.SameSiteLaxMode, Expires: session.ExpiresAt, MaxAge: int(service.Config().SessionTTL.Seconds())})
		http.Redirect(w, r, service.Config().PublicURL+"/account", http.StatusSeeOther)
	})
	handleGET(mux, "/api/v1/account/me", func(w http.ResponseWriter, r *http.Request) {
		RequireAccount(service, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, _ := Account(r.Context())
			writeJSON(w, 200, map[string]any{"data": session})
		})).ServeHTTP(w, r)
	})
	mux.Handle("POST /auth/logout", RequireAccount(service, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders(w)
		cookie, _ := r.Cookie(sessionCookie)
		if err := service.Logout(r.Context(), cookie.Value); err != nil {
			writeError(w, r, 503, "auth_unavailable", "Account service is unavailable")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", Value: "", HttpOnly: true, Secure: service.Config().CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			http.Redirect(w, r, "/account", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.HandleFunc("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		writeError(w, r, 405, "method_not_allowed", "Method not allowed")
	})
	handleGET(mux, "/account", func(w http.ResponseWriter, r *http.Request) {
		authHeaders(w)
		var session *auth.Session
		if service != nil {
			if cookie, err := r.Cookie(sessionCookie); err == nil {
				value, err := service.Session(r.Context(), cookie.Value)
				if err == nil {
					session = &value
				} else if !errors.Is(err, auth.ErrUnauthorized) {
					writeError(w, r, 503, "auth_unavailable", "Account service is unavailable")
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := accountTemplate.Execute(w, struct {
			Enabled bool
			Session *auth.Session
		}{service != nil, session}); err != nil {
			logger.ErrorContext(r.Context(), "render account page failed", "request_id", RequestID(r.Context()))
		}
	})
}

func setLoginCookie(w http.ResponseWriter, service *auth.Service, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: value, Path: "/auth", HttpOnly: true, Secure: service.Config().CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func authHeaders(w http.ResponseWriter) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
}

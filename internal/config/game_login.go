package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GameLogin is deliberately opt-in, independently of website authentication.
type GameLogin struct {
	Enabled    bool
	Origin     string
	Versions   []string
	CodeTTL    time.Duration
	SessionTTL time.Duration
}

func loadGameLogin(cfg *Config) error {
	g := &cfg.GameLogin
	var err error
	if v := env("GAME_LOGIN_ENABLED"); v != "" {
		g.Enabled, err = strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("GAME_LOGIN_ENABLED must be true or false")
		}
	}
	if !g.Enabled {
		return nil
	}
	if !cfg.Auth.Enabled || cfg.Database.URL == "" {
		return fmt.Errorf("game login requires website authentication and PostgreSQL")
	}
	allowHTTP := false
	if v := env("GAME_LOGIN_ALLOW_LOCAL_HTTP"); v != "" {
		allowHTTP, err = strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("GAME_LOGIN_ALLOW_LOCAL_HTTP must be true or false")
		}
	}
	g.Origin, err = GameLoginOrigin(cfg.Auth.PublicURL, allowHTTP && cfg.Environment != "production")
	if err != nil {
		return err
	}
	// Keep CSRF checks and the advertised origin identical after normalization.
	cfg.Auth.PublicURL = g.Origin
	for _, v := range strings.Split(env("GAME_LOGIN_VERSIONS"), ",") {
		v = strings.TrimSpace(v)
		if !ValidGameVersion(v) {
			return fmt.Errorf("GAME_LOGIN_VERSIONS must be a comma-separated allowlist of game versions")
		}
		g.Versions = append(g.Versions, v)
	}
	for _, setting := range []struct {
		name   string
		target *time.Duration
		max    time.Duration
	}{
		{"GAME_LOGIN_CODE_TTL", &g.CodeTTL, 90 * time.Second},
		{"GAME_LOGIN_SESSION_TTL", &g.SessionTTL, time.Hour},
	} {
		if v := env(setting.name); v != "" {
			*setting.target, err = time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("invalid %s", setting.name)
			}
		}
		if *setting.target < time.Second || *setting.target > setting.max {
			return fmt.Errorf("%s is outside its supported lifetime", setting.name)
		}
	}
	return nil
}

func ValidGameVersion(v string) bool {
	if len(v) == 0 || len(v) > 64 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._+-", c)) {
			return false
		}
	}
	return true
}

// GameLoginOrigin never uses request Host or forwarded headers to select an origin.
func GameLoginOrigin(raw string, allowLocalHTTP bool) (string, error) {
	u, err := url.Parse(raw)
	invalid := func() (string, error) {
		return "", fmt.Errorf("game login requires a valid HTTPS APP_PUBLIC_URL origin (HTTP only with explicit local development opt-in)")
	}
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.Opaque != "" {
		return invalid()
	}
	host := strings.ToLower(u.Hostname())
	if strings.ContainsAny(host, "%\\ \t\r\n") {
		return invalid()
	}
	// Match the launcher's URI host grammar. IDNs must use their ASCII form;
	// accepting an underscore or malformed IPv6 would advertise an unusable API.
	if net.ParseIP(host) == nil {
		if len(host) > 253 {
			return invalid()
		}
		for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return invalid()
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
					return invalid()
				}
			}
		}
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && allowLocalHTTP && (host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "10.0.2.2")) {
		return invalid()
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return invalid()
	}
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return invalid()
		}
		port = strconv.Itoa(n)
	}
	if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}

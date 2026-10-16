package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Auth struct {
	Enabled      bool
	IssuerURL    string
	InternalURL  string
	Application  string
	ClientID     string
	ClientSecret string
	PublicURL    string
	CookieSecure bool
	SessionTTL   time.Duration
}

func defaultAuth() Auth {
	return Auth{
		IssuerURL: "http://localhost:8000", Application: "pvzce-site", ClientID: "pvzce-site",
		PublicURL: "http://localhost:8080", SessionTTL: 24 * time.Hour,
	}
}

func loadAuth(cfg *Auth) error {
	if value := env("AUTH_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("AUTH_ENABLED must be true or false")
		}
		cfg.Enabled = enabled
	}
	for _, setting := range []struct {
		name   string
		target *string
	}{
		{"CASDOOR_ISSUER_URL", &cfg.IssuerURL}, {"CASDOOR_INTERNAL_URL", &cfg.InternalURL},
		{"CASDOOR_APPLICATION", &cfg.Application}, {"CASDOOR_CLIENT_ID", &cfg.ClientID},
		{"CASDOOR_CLIENT_SECRET", &cfg.ClientSecret}, {"APP_PUBLIC_URL", &cfg.PublicURL},
	} {
		if value := env(setting.name); value != "" {
			*setting.target = value
		}
	}
	if value := env("AUTH_SESSION_TTL"); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil || duration < time.Minute || duration > 30*24*time.Hour {
			return fmt.Errorf("AUTH_SESSION_TTL must be between 1m and 720h")
		}
		cfg.SessionTTL = duration
	}
	cfg.IssuerURL = strings.TrimRight(cfg.IssuerURL, "/")
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	cfg.InternalURL = strings.TrimRight(cfg.InternalURL, "/")
	cfg.CookieSecure = strings.HasPrefix(cfg.PublicURL, "https://")
	if !cfg.Enabled {
		return nil
	}
	for name, value := range map[string]string{"CASDOOR_ISSUER_URL": cfg.IssuerURL, "APP_PUBLIC_URL": cfg.PublicURL} {
		if err := validateOrigin(value, true); err != nil {
			return fmt.Errorf("invalid %s: %w", name, err)
		}
	}
	if cfg.InternalURL != "" {
		if err := validateOrigin(cfg.InternalURL, false); err != nil {
			return fmt.Errorf("invalid CASDOOR_INTERNAL_URL: %w", err)
		}
	}
	if cfg.ClientID == "" || len(cfg.ClientSecret) < 24 {
		return fmt.Errorf("CASDOOR_CLIENT_SECRET must contain at least 24 characters")
	}
	if cfg.Application == "" || strings.ContainsAny(cfg.Application, "/\\?# ") {
		return fmt.Errorf("invalid CASDOOR_APPLICATION")
	}
	return nil
}

func validateOrigin(value string, requirePublicTLS bool) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("expected an HTTP(S) origin without a path or credentials")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid port")
		}
	}
	if requirePublicTLS && u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("HTTP is supported only for localhost; use HTTPS for a public origin")
		}
	}
	return nil
}

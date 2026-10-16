package config

import (
	"strings"
	"testing"
)

func TestAuthConfiguration(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("CASDOOR_CLIENT_SECRET", strings.Repeat("s", 32))
	t.Setenv("APP_PUBLIC_URL", "https://site.example.com/")
	t.Setenv("CASDOOR_ISSUER_URL", "https://login.example.com/")
	t.Setenv("CASDOOR_INTERNAL_URL", "http://casdoor:8000")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auth.Enabled || !cfg.Auth.CookieSecure || cfg.Auth.PublicURL != "https://site.example.com" || cfg.Auth.IssuerURL != "https://login.example.com" {
		t.Fatal("invalid authentication configuration")
	}
}

func TestInvalidAuthConfiguration(t *testing.T) {
	for _, setting := range []struct{ Key, Value string }{
		{"AUTH_ENABLED", "maybe"}, {"CASDOOR_CLIENT_SECRET", "short"}, {"APP_PUBLIC_URL", "http://public.example"},
		{"CASDOOR_ISSUER_URL", "http://user:password@localhost:8000"}, {"CASDOOR_INTERNAL_URL", "file:///etc/passwd"},
		{"CASDOOR_APPLICATION", "../other"}, {"AUTH_SESSION_TTL", "0s"}, {"AUTH_SESSION_TTL", "721h"},
	} {
		t.Run(setting.Key, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("AUTH_ENABLED", "true")
			t.Setenv("DB_HOST", "localhost")
			t.Setenv("CASDOOR_CLIENT_SECRET", strings.Repeat("s", 32))
			t.Setenv(setting.Key, setting.Value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid authentication configuration accepted")
			}
		})
	}
	clearEnvironment(t)
	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("CASDOOR_CLIENT_SECRET", strings.Repeat("s", 32))
	if _, err := Load(); err == nil {
		t.Fatal("authentication without a database accepted")
	}
}

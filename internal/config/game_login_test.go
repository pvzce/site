package config

import (
	"strings"
	"testing"
	"time"
)

func TestGameOrigin(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		local     bool
	}{
		{"https://SITE.example:443", "https://site.example", false},
		{"https://site.example:8443", "https://site.example:8443", false},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"http://[::1]:80", "http://[::1]", true},
		{"http://10.0.2.2:8080", "http://10.0.2.2:8080", true},
	} {
		got, err := GameLoginOrigin(tc.raw, tc.local)
		if err != nil || got != tc.want {
			t.Errorf("origin %s: %s %v", tc.raw, got, err)
		}
	}
	for _, raw := range []string{"https://bad_host", "https://[not:ipv6]", "https://bad..host", "https://-bad.example", "http://site.example", "http://localhost", "https://user:secret@site.example", "https://site.example/path", "https://site.example/", "https://site.example?", "https://site.example#", "https://site.example:0", "https://site.example:65536", "https://site.example:", "https://site.example?token=secret", "https://site.example#secret", "file:///tmp/a"} {
		if _, err := GameLoginOrigin(raw, false); err == nil {
			t.Errorf("accepted invalid origin: %s", raw)
		}
	}
}
func TestGameConfigFailClosed(t *testing.T) {
	valid := func() Config {
		c := Default()
		c.Auth.Enabled = true
		c.Auth.PublicURL = "https://site.example"
		c.Database.URL = "postgres://unused"
		return c
	}
	t.Setenv("GAME_LOGIN_ENABLED", "true")
	t.Setenv("GAME_LOGIN_VERSIONS", "1.0.0,1.1.0-beta")
	c := valid()
	if err := loadGameLogin(&c); err != nil || c.GameLogin.CodeTTL != 60*time.Second || c.GameLogin.SessionTTL != time.Hour {
		t.Fatal("valid config", err)
	}
	for _, tc := range []struct{ key, value string }{
		{"GAME_LOGIN_CODE_TTL", "91s"}, {"GAME_LOGIN_CODE_TTL", "0s"}, {"GAME_LOGIN_SESSION_TTL", "61m"}, {"GAME_LOGIN_SESSION_TTL", "bad-secret"},
		{"GAME_LOGIN_VERSIONS", ""}, {"GAME_LOGIN_VERSIONS", "1.0.0,,2.0"}, {"GAME_LOGIN_VERSIONS", "../../secret"}, {"GAME_LOGIN_ENABLED", "perhaps"}, {"GAME_LOGIN_ALLOW_LOCAL_HTTP", "perhaps"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			c := valid()
			err := loadGameLogin(&c)
			if err == nil || strings.Contains(err.Error(), "bad-secret") {
				t.Fatal("invalid or unsafe config error", err)
			}
		})
	}
	for _, change := range []func(*Config){func(c *Config) { c.Auth.Enabled = false }, func(c *Config) { c.Database.URL = "" }, func(c *Config) { c.Auth.PublicURL = "http://localhost:8080" }} {
		c := valid()
		change(&c)
		if loadGameLogin(&c) == nil {
			t.Fatal("incomplete config accepted")
		}
	}
	t.Setenv("GAME_LOGIN_ALLOW_LOCAL_HTTP", "true")
	c = valid()
	c.Auth.PublicURL = "http://localhost:8080"
	if loadGameLogin(&c) != nil {
		t.Fatal("explicit local development denied")
	}
	c = valid()
	c.Environment = "production"
	c.Auth.PublicURL = "http://localhost:8080"
	if loadGameLogin(&c) == nil {
		t.Fatal("production allowed HTTP")
	}
}

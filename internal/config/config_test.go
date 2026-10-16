package config

import (
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"
)

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"APP_ENV", "HTTP_ADDR", "LOG_LEVEL", "HTTP_READ_HEADER_TIMEOUT",
		"HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT",
		"SHUTDOWN_TIMEOUT", "HTTP_MAX_BODY_BYTES",
		"DATABASE_URL", "DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD", "DB_SSLMODE",
		"DB_MAX_CONNS", "DB_MIN_CONNS", "DB_CONNECT_TIMEOUT", "DB_QUERY_TIMEOUT",
		"DB_MAX_CONN_LIFETIME", "DB_MAX_CONN_IDLE_TIME", "FILE_ROOT", "FILE_MAX_BYTES",
		"DATA_DIR", "CONFIG_DIR", "CACHE_DIR",
		"AUTH_ENABLED", "CASDOOR_ISSUER_URL", "CASDOOR_INTERNAL_URL", "CASDOOR_APPLICATION",
		"CASDOOR_CLIENT_ID", "CASDOOR_CLIENT_SECRET", "APP_PUBLIC_URL", "AUTH_SESSION_TTL",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadConfiguration(t *testing.T) {
	clearEnvironment(t)
	cfg, err := Load()
	if err != nil || cfg != Default() {
		t.Fatalf("defaults: cfg=%+v err=%v", cfg, err)
	}
	t.Setenv("APP_ENV", "production")
	t.Setenv("HTTP_ADDR", " [::1]:9090 ")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("HTTP_READ_HEADER_TIMEOUT", "2s")
	t.Setenv("HTTP_READ_TIMEOUT", "20s")
	t.Setenv("HTTP_WRITE_TIMEOUT", "30s")
	t.Setenv("HTTP_IDLE_TIMEOUT", "90s")
	t.Setenv("SHUTDOWN_TIMEOUT", "12s")
	t.Setenv("HTTP_MAX_BODY_BYTES", "2048")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "production" || cfg.HTTPAddr != "[::1]:9090" || cfg.LogLevel != slog.LevelDebug ||
		cfg.ReadHeaderTimeout != 2*time.Second || cfg.ReadTimeout != 20*time.Second ||
		cfg.WriteTimeout != 30*time.Second || cfg.IdleTimeout != 90*time.Second ||
		cfg.ShutdownTimeout != 12*time.Second || cfg.MaxBodyBytes != 2048 {
		t.Fatalf("configuration overrides were not applied: %+v", cfg)
	}
}

func TestInvalidConfigurationFailsAtStartup(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"APP_ENV", "unknown"},
		{"HTTP_ADDR", "8080"},
		{"HTTP_ADDR", ":0"},
		{"HTTP_ADDR", ":65536"},
		{"LOG_LEVEL", "verbose"},
		{"HTTP_READ_HEADER_TIMEOUT", "later"},
		{"HTTP_READ_TIMEOUT", "0s"},
		{"HTTP_WRITE_TIMEOUT", "-1s"},
		{"HTTP_IDLE_TIMEOUT", "0s"},
		{"SHUTDOWN_TIMEOUT", "0s"},
		{"HTTP_MAX_BODY_BYTES", "0"},
		{"HTTP_MAX_BODY_BYTES", "many"},
		{"HTTP_READ_TIMEOUT", "1s"},
		{"DATABASE_URL", "mysql://localhost/db"},
		{"DATABASE_URL", "postgres://user:private-password@host:bad/db"},
		{"DB_MAX_CONNS", "0"},
		{"DB_MIN_CONNS", "-1"},
		{"DB_MIN_CONNS", "11"},
		{"DB_CONNECT_TIMEOUT", "0s"},
		{"DB_QUERY_TIMEOUT", "-1s"},
		{"DB_MAX_CONN_LIFETIME", "bad"},
		{"DB_MAX_CONN_IDLE_TIME", "0s"},
		{"FILE_MAX_BYTES", "0"},
		{"FILE_MAX_BYTES", "1073741825"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv(tc.name, tc.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("expected an error mentioning %s, got %v", tc.name, err)
			}
		})
	}
}

func TestDatabaseAndFileConfiguration(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("DB_HOST", "::1")
	t.Setenv("DB_USER", "app-user")
	t.Setenv("DB_PASSWORD", " special@password:/?# ")
	t.Setenv("DB_SSLMODE", "verify-full")
	t.Setenv("DB_MAX_CONNS", "20")
	t.Setenv("DB_MIN_CONNS", "2")
	t.Setenv("DB_QUERY_TIMEOUT", "2s")
	t.Setenv("DATA_DIR", "/srv/data")
	t.Setenv("CONFIG_DIR", "/srv/configs")
	t.Setenv("CACHE_DIR", "/tmp/cache")
	t.Setenv("FILE_MAX_BYTES", "1024")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(cfg.Database.URL)
	if err != nil {
		t.Fatal("generated database URL is invalid")
	}
	password, _ := parsed.User.Password()
	if parsed.Host != "[::1]:5432" || parsed.User.Username() != "app-user" || password != " special@password:/?# " || parsed.Query().Get("sslmode") != "verify-full" {
		t.Fatal("database URL failed to preserve connection settings")
	}
	if cfg.Database.MaxConns != 20 || cfg.Database.MinConns != 2 || cfg.Database.QueryTimeout != 2*time.Second ||
		cfg.Files.DataDir != "/srv/data" || cfg.Files.ConfigDir != "/srv/configs" || cfg.Files.CacheDir != "/tmp/cache" || cfg.Files.MaxBytes != 1024 {
		t.Fatal("database or file configuration was not applied")
	}
	t.Setenv("DATABASE_URL", "postgres://other@remote/db?sslmode=verify-full")
	cfg, err = Load()
	if err != nil || cfg.Database.URL != "postgres://other@remote/db?sslmode=verify-full" {
		t.Fatal("DATABASE_URL must take precedence over individual settings")
	}
}

func TestLegacyFileRootOnlyConfiguresData(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("FILE_ROOT", "/legacy/data")
	cfg, err := Load()
	if err != nil || cfg.Files.DataDir != "/legacy/data" || cfg.Files.ConfigDir != "./configs" || cfg.Files.CacheDir != "./cache" {
		t.Fatal("FILE_ROOT compatibility must apply only to the data directory")
	}
	t.Setenv("DATA_DIR", "/new/data")
	cfg, err = Load()
	if err != nil || cfg.Files.DataDir != "/new/data" {
		t.Fatal("DATA_DIR must take precedence over FILE_ROOT")
	}
}

func TestConnectionConfigurationErrorsDoNotExposeCredentials(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("DATABASE_URL", "postgres://user:private-password@host:bad/db")
	if _, err := Load(); err == nil || strings.Contains(err.Error(), "private-password") {
		t.Fatal("invalid connection settings must fail without exposing credentials")
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_PORT", "bad")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DB_PORT") {
		t.Fatalf("expected invalid DB_PORT error, got %v", err)
	}
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_SSLMODE", "bad")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DB_SSLMODE") {
		t.Fatalf("expected invalid DB_SSLMODE error, got %v", err)
	}
}

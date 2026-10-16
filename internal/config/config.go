package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment       string
	HTTPAddr          string
	LogLevel          slog.Level
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxBodyBytes      int64
	Database          Database
	Files             Files
	Auth              Auth
}

type Database struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	ConnectTimeout  time.Duration
	QueryTimeout    time.Duration
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

type Files struct {
	DataDir   string
	ConfigDir string
	CacheDir  string
	MaxBytes  int64
}

func Default() Config {
	return Config{
		Environment:       "development",
		HTTPAddr:          ":8080",
		LogLevel:          slog.LevelInfo,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   10 * time.Second,
		MaxBodyBytes:      1 << 20,
		Database: Database{
			MaxConns: 10, MinConns: 1,
			ConnectTimeout: 5 * time.Second, QueryTimeout: 3 * time.Second,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: 5 * time.Minute,
		},
		Files: Files{DataDir: "./data", ConfigDir: "./configs", CacheDir: "./cache", MaxBytes: 16 << 20},
		Auth:  defaultAuth(),
	}
}

// Load reads only environment variables; the process does not implicitly read .env files.
func Load() (Config, error) {
	cfg := Default()
	if value := env("APP_ENV"); value != "" {
		cfg.Environment = value
	}
	switch cfg.Environment {
	case "development", "production", "test":
	default:
		return Config{}, fmt.Errorf("APP_ENV must be development, production, or test")
	}
	if value := env("HTTP_ADDR"); value != "" {
		cfg.HTTPAddr = value
	}
	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return Config{}, fmt.Errorf("HTTP_ADDR port must be between 1 and 65535")
	}
	if value := env("LOG_LEVEL"); value != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(value)); err != nil {
			return Config{}, fmt.Errorf("invalid LOG_LEVEL: %w", err)
		}
	}
	for _, setting := range []struct {
		name   string
		target *time.Duration
	}{
		{"HTTP_READ_HEADER_TIMEOUT", &cfg.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", &cfg.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", &cfg.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", &cfg.IdleTimeout},
		{"SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
		{"DB_CONNECT_TIMEOUT", &cfg.Database.ConnectTimeout},
		{"DB_QUERY_TIMEOUT", &cfg.Database.QueryTimeout},
		{"DB_MAX_CONN_LIFETIME", &cfg.Database.MaxConnLifetime},
		{"DB_MAX_CONN_IDLE_TIME", &cfg.Database.MaxConnIdleTime},
	} {
		if value := env(setting.name); value != "" {
			duration, err := time.ParseDuration(value)
			if err != nil || duration <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration, e.g. 15s", setting.name)
			}
			*setting.target = duration
		}
	}
	if cfg.ReadTimeout < cfg.ReadHeaderTimeout {
		return Config{}, fmt.Errorf("HTTP_READ_TIMEOUT must be at least HTTP_READ_HEADER_TIMEOUT")
	}
	if value := env("HTTP_MAX_BODY_BYTES"); value != "" {
		cfg.MaxBodyBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || cfg.MaxBodyBytes <= 0 {
			return Config{}, fmt.Errorf("HTTP_MAX_BODY_BYTES must be a positive integer")
		}
	}
	if err := loadDatabase(&cfg.Database); err != nil {
		return Config{}, err
	}
	if value := env("FILE_ROOT"); value != "" {
		cfg.Files.DataDir = value // Legacy name for the data directory.
	}
	for _, setting := range []struct {
		name   string
		target *string
	}{
		{"DATA_DIR", &cfg.Files.DataDir},
		{"CONFIG_DIR", &cfg.Files.ConfigDir},
		{"CACHE_DIR", &cfg.Files.CacheDir},
	} {
		if value := env(setting.name); value != "" {
			*setting.target = value
		}
	}
	if value := env("FILE_MAX_BYTES"); value != "" {
		cfg.Files.MaxBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || cfg.Files.MaxBytes < 1 || cfg.Files.MaxBytes > 1<<30 {
			return Config{}, fmt.Errorf("FILE_MAX_BYTES must be between 1 and 1073741824")
		}
	}
	if err := loadAuth(&cfg.Auth); err != nil {
		return Config{}, err
	}
	if cfg.Auth.Enabled && cfg.Database.URL == "" {
		return Config{}, fmt.Errorf("authentication requires DATABASE_URL or DB_HOST")
	}
	return cfg, nil
}

func loadDatabase(cfg *Database) error {
	for _, setting := range []struct {
		name   string
		target *int32
		min    int64
	}{
		{"DB_MAX_CONNS", &cfg.MaxConns, 1},
		{"DB_MIN_CONNS", &cfg.MinConns, 0},
	} {
		if value := env(setting.name); value != "" {
			number, err := strconv.ParseInt(value, 10, 32)
			if err != nil || number < setting.min {
				return fmt.Errorf("%s must be an integer >= %d", setting.name, setting.min)
			}
			*setting.target = int32(number)
		}
	}
	if cfg.MinConns > cfg.MaxConns {
		return fmt.Errorf("DB_MIN_CONNS must not exceed DB_MAX_CONNS")
	}
	if value := env("DATABASE_URL"); value != "" {
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" {
			// Do not return parsing errors that can contain the password or complete URL.
			return fmt.Errorf("DATABASE_URL must be a valid postgres:// or postgresql:// URL")
		}
		cfg.URL = value
		return nil
	}
	host := env("DB_HOST")
	if host == "" {
		return nil // Explicitly unconfigured: the server can run without a database.
	}
	port := env("DB_PORT")
	if port == "" {
		port = "5432"
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("DB_PORT must be between 1 and 65535")
	}
	name, user, sslMode := env("DB_NAME"), env("DB_USER"), env("DB_SSLMODE")
	if name == "" {
		name = "pvzce"
	}
	if user == "" {
		user = "pvzce"
	}
	if sslMode == "" {
		sslMode = "require"
	}
	switch sslMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return fmt.Errorf("invalid DB_SSLMODE")
	}
	connectionURL := url.URL{
		Scheme: "postgres", Host: net.JoinHostPort(host, port), Path: "/" + name,
		User: url.UserPassword(user, os.Getenv("DB_PASSWORD")),
	}
	query := url.Values{"sslmode": {sslMode}}
	connectionURL.RawQuery = query.Encode()
	cfg.URL = connectionURL.String()
	return nil
}

func env(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

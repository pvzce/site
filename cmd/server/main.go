package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/casdoorbootstrap"
	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
	"pvzce-site/internal/fileio"
	"pvzce-site/internal/healthcheck"
	"pvzce-site/internal/httpapi"
	"pvzce-site/internal/migrate"
	"pvzce-site/internal/server"
	"pvzce-site/internal/workshop"
)

func main() {
	if err := run(); err != nil {
		slog.Error("process failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) == 2 && os.Args[1] == "init-casdoor" {
		if err := casdoorbootstrap.Initialize(".env", "configs/casdoor"); err != nil {
			return err
		}
		fmt.Println("Casdoor configuration initialized; credentials: configs/casdoor/credentials.txt")
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		return healthcheck.Check(context.Background(), cfg.HTTPAddr)
	}
	if len(os.Args) > 1 && os.Args[1] != "migrate" && os.Args[1] != "bootstrap-admin" && os.Args[1] != "init-storage" {
		return fmt.Errorf("usage: %s [healthcheck|init-casdoor|migrate|bootstrap-admin USER_ID]", os.Args[0])
	}

	if len(os.Args) == 2 && os.Args[1] == "init-storage" {
		s, e := workshop.LoadSettings()
		if e != nil {
			return e
		}
		return workshop.BootstrapStorage(context.Background(), s)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	if db != nil {
		defer db.Close()
		logger.Info("database connected", "max_connections", cfg.Database.MaxConns)
	} else {
		logger.Info("database not configured")
	}
	if len(os.Args) > 1 {
		if db == nil {
			return fmt.Errorf("operator commands require PostgreSQL")
		}
		if e := migrate.Apply(ctx, db); e != nil {
			return e
		}
		if os.Args[1] == "migrate" && len(os.Args) == 2 {
			return nil
		}
		if os.Args[1] == "bootstrap-admin" && len(os.Args) == 3 {
			uid, e := strconv.ParseInt(os.Args[2], 10, 64)
			if e != nil || uid < 1 {
				return fmt.Errorf("valid existing site user ID required")
			}
			svc := &workshop.Service{DB: db}
			return svc.BootstrapAdmin(ctx, uid)
		}
		return fmt.Errorf("usage: migrate | bootstrap-admin USER_ID")
	}
	var works *workshop.Service
	if db != nil && cfg.Auth.Enabled {
		settings, e := workshop.LoadSettings()
		if e != nil {
			return e
		}
		works, e = workshop.New(ctx, db, settings)
		if e != nil {
			return e
		}
	}
	if works != nil {
		go works.Maintenance(ctx, logger)
	}
	files, err := fileio.NewDirectories(cfg.Files)
	if err != nil {
		return err
	}
	defer files.Close()
	var accounts *auth.Service
	if cfg.Auth.Enabled {
		store, err := auth.NewStore(ctx, db)
		if err != nil {
			return fmt.Errorf("initialize accounts: %w", err)
		}
		accounts, err = auth.New(ctx, cfg.Auth, store)
		if err != nil {
			return err
		}
		defer accounts.Close()
		logger.Info("Casdoor authentication initialized")
	}
	return server.New(cfg, logger, httpapi.Dependencies{Database: db, Files: files, Auth: accounts, Workshop: works}).Run(ctx)
}

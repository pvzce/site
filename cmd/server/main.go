package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/casdoorbootstrap"
	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
	"pvzce-site/internal/fileio"
	"pvzce-site/internal/healthcheck"
	"pvzce-site/internal/httpapi"
	"pvzce-site/internal/server"
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

	if len(os.Args) > 1 {
		if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
			return healthcheck.Check(context.Background(), cfg.HTTPAddr)
		}
		return fmt.Errorf("usage: %s [healthcheck|init-casdoor]", os.Args[0])
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
	return server.New(cfg, logger, httpapi.Dependencies{Database: db, Files: files, Auth: accounts}).Run(ctx)
}

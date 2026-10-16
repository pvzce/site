package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
	"pvzce-site/internal/httpapi"
)

type Server struct {
	http            *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
	ready           atomic.Bool
	database        *database.DB
}

func New(cfg config.Config, logger *slog.Logger, deps httpapi.Dependencies) *Server {
	s := &Server{logger: logger, shutdownTimeout: cfg.ShutdownTimeout, database: deps.Database}
	deps.Ready = s.checkReady
	s.http = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewHandler(cfg, logger, deps),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	return s
}

func (s *Server) checkReady(ctx context.Context) error {
	if !s.ready.Load() {
		return fmt.Errorf("HTTP server is not ready")
	}
	if s.database != nil {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := s.database.Ping(ctx); err != nil {
			return err
		}
	}
	if !s.ready.Load() {
		return fmt.Errorf("HTTP server is shutting down")
	}
	return nil
}

// Run serves until cancellation, then lets in-flight requests finish within the shutdown deadline.
func (s *Server) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	listener, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return s.serve(ctx, listener)
}

func (s *Server) serve(ctx context.Context, listener net.Listener) error {
	defer listener.Close()
	s.ready.Store(true)
	defer s.ready.Store(false)
	errCh := make(chan error, 1)
	go func() { errCh <- s.http.Serve(listener) }()
	s.logger.Info("server started", "address", listener.Addr().String())

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	s.ready.Store(false)
	s.logger.Info("server shutting down")
	// Use a fresh context: the triggering context has already been canceled.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		_ = s.http.Close()
		<-errCh
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	s.logger.Info("server stopped")
	return nil
}

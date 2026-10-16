package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
	"pvzce-site/internal/httpapi"
)

func newTestServer(t *testing.T, timeout time.Duration) (*Server, net.Listener) {
	t.Helper()
	cfg := config.Default()
	cfg.ShutdownTimeout = timeout
	s := New(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), httpapi.Dependencies{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.http.Close()
		_ = listener.Close()
	})
	return s, listener
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server")
		var zero T
		return zero
	}
}

func request(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	return err
}

func TestGracefulShutdownFinishesInflightRequest(t *testing.T) {
	s, listener := newTestServer(t, 2*time.Second)
	started := make(chan struct{})
	release := make(chan struct{})
	stopping := make(chan struct{})
	s.http.RegisterOnShutdown(func() { close(stopping) })
	s.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		if r.Context().Err() != nil {
			t.Error("graceful shutdown canceled an in-flight request")
		}
		_, _ = io.WriteString(w, "finished")
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.serve(ctx, listener) }()
	requestDone := make(chan error, 1)
	go func() { requestDone <- request("http://" + listener.Addr().String()) }()
	await(t, started)
	if !s.ready.Load() {
		t.Fatal("running server is not ready")
	}
	cancel()
	await(t, stopping)
	if s.ready.Load() {
		t.Fatal("shutting-down server is still ready")
	}
	select {
	case err := <-serverDone:
		t.Fatalf("shutdown returned before request finished: %v", err)
	default:
	}
	close(release)
	if err := await(t, requestDone); err != nil {
		t.Fatal(err)
	}
	if err := await(t, serverDone); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownDeadlineClosesStuckRequests(t *testing.T) {
	s, listener := newTestServer(t, 20*time.Millisecond)
	started := make(chan struct{})
	handlerDone := make(chan struct{})
	s.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(handlerDone)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.serve(ctx, listener) }()
	requestDone := make(chan error, 1)
	go func() { requestDone <- request("http://" + listener.Addr().String()) }()
	await(t, started)
	cancel()
	if err := await(t, serverDone); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected shutdown deadline, got %v", err)
	}
	await(t, handlerDone)
	if err := await(t, requestDone); err == nil {
		t.Fatal("stuck request was not aborted")
	}
}

func TestListenFailureIsReturned(t *testing.T) {
	s, listener := newTestServer(t, time.Second)
	s.http.Addr = listener.Addr().String()
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("expected an error for an occupied port")
	}
}

func TestReadinessTracksDatabaseAvailability(t *testing.T) {
	connectionURL := os.Getenv("TEST_DATABASE_URL")
	if connectionURL == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL readiness tests")
	}
	cfg := config.Default()
	cfg.Database.URL = connectionURL
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), httpapi.Dependencies{Database: db})
	s.ready.Store(true)
	check := func(path string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status {
			t.Fatalf("%s: status=%d, want %d", path, w.Code, status)
		}
	}
	check("/health/ready", 200)
	db.Close()
	check("/health/ready", 503)
	check("/health/live", 200)
}

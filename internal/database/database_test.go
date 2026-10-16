package database

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"pvzce-site/internal/config"
)

func TestOptionalDatabaseAndInvalidURL(t *testing.T) {
	cfg := config.Default().Database
	if db, err := Open(context.Background(), cfg); err != nil || db != nil {
		t.Fatalf("unconfigured database should be disabled: %v", err)
	}
	cfg.URL = "postgres://user:private-password@localhost:bad/db"
	if _, err := Open(context.Background(), cfg); err == nil || strings.Contains(err.Error(), "private-password") {
		t.Fatal("invalid URL should fail without exposing credentials")
	}
}

func TestInvalidPoolSettingsDoNotPanic(t *testing.T) {
	cfg := config.Default().Database
	cfg.URL = "postgres://test@localhost/test"
	cfg.MaxConns = 0
	if _, err := Open(context.Background(), cfg); err == nil {
		t.Fatal("invalid pool settings were accepted")
	}
}

func TestStartupConnectionHasDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := config.Default().Database
	cfg.URL = "postgres://test:test@" + listener.Addr().String() + "/test?sslmode=disable"
	cfg.MinConns = 0
	cfg.ConnectTimeout = 50 * time.Millisecond
	started := time.Now()
	if _, err := Open(context.Background(), cfg); err == nil {
		t.Fatal("a non-responsive database was accepted")
	}
	if time.Since(started) > time.Second {
		t.Fatal("database startup exceeded its deadline")
	}
}

func TestPostgreSQLIntegration(t *testing.T) {
	connectionURL := os.Getenv("TEST_DATABASE_URL")
	if connectionURL == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL integration tests")
	}
	cfg := config.Default().Database
	cfg.URL = connectionURL
	cfg.QueryTimeout = time.Second
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	ctx := context.Background()
	var value int
	if err := db.QueryRow(ctx, "SELECT $1::int + 1", 41).Scan(&value); err != nil || value != 42 {
		t.Fatalf("parameterized query: value=%d err=%v", value, err)
	}
	rows, err := db.Query(ctx, "SELECT generate_series(1, 3)")
	if err != nil {
		t.Fatal(err)
	}
	var sum int
	for rows.Next() {
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		sum += value
	}
	rows.Close()
	if rows.Err() != nil || sum != 6 {
		t.Fatalf("rows iteration was canceled prematurely: sum=%d err=%v", sum, rows.Err())
	}
	schema := pgx.Identifier{"integration_" + strings.ToLower(rand.Text())}.Sanitize()
	if _, err := db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("clean up integration schema: %v", err)
		}
	})
	table := schema + ".items"
	if _, err := db.Exec(ctx, "CREATE TABLE "+table+" (value text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO "+table+" VALUES ($1)", "committed")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback requested")
	if err := db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" VALUES ($1)", "rolled back"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("transaction did not return its callback error: %v", err)
	}
	func() {
		defer func() {
			if recover() != "rollback panic" {
				t.Error("transaction panic did not propagate")
			}
		}()
		_ = db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "INSERT INTO "+table+" VALUES ($1)", "panic"); err != nil {
				t.Fatal(err)
			}
			panic("rollback panic")
		})
	}()
	if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&value); err != nil || value != 1 {
		t.Fatalf("transaction rollback failed: count=%d err=%v", value, err)
	}
	if _, err := db.Exec(ctx, "SELECT pg_sleep(3)"); err == nil || (!errors.Is(err, context.DeadlineExceeded) && !pgconn.Timeout(err)) {
		t.Fatalf("query timeout was not enforced: %v", err)
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatalf("pool did not recover after a timed-out query: %v", err)
	}
	// Timed-out connections are destroyed asynchronously by pgxpool / puddle.
	deadline := time.Now().Add(2 * time.Second)
	for db.Stats().AcquiredConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stats := db.Stats()
	if stats.AcquiredConns() != 0 || stats.MaxConns() != cfg.MaxConns {
		t.Fatalf("pool: acquired=%d max=%d", stats.AcquiredConns(), stats.MaxConns())
	}
}

package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"pvzce-site/internal/config"
)

// DB owns a shared, concurrency-safe PostgreSQL connection pool.
type DB struct {
	pool         *pgxpool.Pool
	queryTimeout time.Duration
}

// Open returns nil when no database is configured. A configured database must pass a startup ping.
func Open(ctx context.Context, cfg config.Database) (*DB, error) {
	if cfg.URL == "" {
		return nil, nil
	}
	if cfg.MaxConns < 1 || cfg.MinConns < 0 || cfg.MinConns > cfg.MaxConns ||
		cfg.ConnectTimeout <= 0 || cfg.QueryTimeout <= 0 || cfg.MaxConnLifetime <= 0 || cfg.MaxConnIdleTime <= 0 {
		return nil, fmt.Errorf("invalid database pool settings")
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid DATABASE_URL")
	}
	poolConfig.MaxConns = cfg.MaxConns
	poolConfig.MinConns = cfg.MinConns
	poolConfig.MaxConnLifetime = cfg.MaxConnLifetime
	poolConfig.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolConfig.MaxConnLifetimeJitter = cfg.MaxConnLifetime / 10
	poolConfig.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "pvzce-site"
	connectCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(connectCtx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &DB{pool: pool, queryTimeout: cfg.QueryTimeout}, nil
}

func (db *DB) Close() {
	db.pool.Close()
}

func (db *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, db.queryTimeout)
	defer cancel()
	return db.pool.Ping(ctx)
}

func (db *DB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	ctx, cancel := context.WithTimeout(ctx, db.queryTimeout)
	defer cancel()
	return db.pool.Exec(ctx, sql, args...)
}

// Query keeps its timeout alive until rows are exhausted or closed. Always defer rows.Close().
func (db *DB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	ctx, cancel := context.WithTimeout(ctx, db.queryTimeout)
	rows, err := db.pool.Query(ctx, sql, args...)
	if err != nil {
		cancel()
		return nil, err
	}
	return &timedRows{Rows: rows, cancel: cancel}, nil
}

// QueryRow releases its timeout when Scan is called. Call Scan for every returned row.
func (db *DB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	ctx, cancel := context.WithTimeout(ctx, db.queryTimeout)
	return &timedRow{Row: db.pool.QueryRow(ctx, sql, args...), cancel: cancel}
}

// InTx commits on success and rolls back on errors or panics. Use the supplied context for queries.
func (db *DB) InTx(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, db.queryTimeout)
	defer cancel()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (db *DB) Stats() *pgxpool.Stat {
	return db.pool.Stat()
}

type timedRows struct {
	pgx.Rows
	cancel context.CancelFunc
}

func (rows *timedRows) Close() {
	rows.Rows.Close()
	rows.cancel()
}

func (rows *timedRows) Next() bool {
	more := rows.Rows.Next()
	if !more {
		rows.cancel()
	}
	return more
}

type timedRow struct {
	pgx.Row
	cancel context.CancelFunc
}

func (row *timedRow) Scan(destination ...any) error {
	defer row.cancel()
	return row.Row.Scan(destination...)
}

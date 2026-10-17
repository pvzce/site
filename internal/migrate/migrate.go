// Package migrate applies embedded, checksummed forward-only PostgreSQL migrations.
package migrate

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"pvzce-site/internal/database"
)

//go:embed sql/*.sql
var migrations embed.FS

func Apply(ctx context.Context, db *database.DB) error {
	if db == nil {
		return fmt.Errorf("migrations require PostgreSQL")
	}
	return db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(706879122531)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.site_migrations(name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		names, err := fs.Glob(migrations, "sql/*.sql")
		if err != nil {
			return err
		}
		sort.Strings(names)
		for _, name := range names {
			body, err := migrations.ReadFile(name)
			if err != nil {
				return err
			}
			sum := fmt.Sprintf("%x", sha256.Sum256(body))
			var previous string
			err = tx.QueryRow(ctx, `SELECT checksum FROM public.site_migrations WHERE name=$1`, name).Scan(&previous)
			if err == nil {
				if previous != sum {
					return fmt.Errorf("migration checksum changed: %s", name)
				}
				continue
			}
			if err != pgx.ErrNoRows {
				return err
			}
			if _, err = tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO public.site_migrations(name,checksum) VALUES($1,$2)`, name, sum); err != nil {
				return err
			}
		}
		return nil
	})
}

package migrate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
)

func TestUpgradeLegacyAndChecksumIntegration(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	cfg := config.Default().Database
	cfg.URL = raw
	control, e := database.Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer control.Close()
	name := "migration_test_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, e := control.Exec(ctx, "CREATE DATABASE "+quoted); e != nil {
		t.Fatal("migration test requires CREATE DATABASE on test server:", e)
	}
	defer func() {
		if _, e := control.Exec(ctx, "DROP DATABASE "+quoted+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
	}()
	parsed, e := url.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	parsed.Path = "/" + name
	cfg.URL = parsed.String()
	db, e := database.Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	legacy, e := migrations.ReadFile("sql/001_auth.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(ctx, string(legacy)); e != nil {
		t.Fatal(e)
	}
	var uid int64
	if e := db.QueryRow(ctx, `INSERT INTO pvzce_auth.users(issuer,subject,name) VALUES('legacy','existing-user','Preserved profile') RETURNING id`).Scan(&uid); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Apply(ctx, db) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var nameValue string
	if e := db.QueryRow(ctx, `SELECT name FROM pvzce_auth.users WHERE id=$1`, uid).Scan(&nameValue); e != nil || nameValue != "Preserved profile" {
		t.Fatal("legacy account lost", e)
	}
	var count int
	if e := db.QueryRow(ctx, `SELECT count(*) FROM public.site_migrations`).Scan(&count); e != nil || count != 4 {
		t.Fatal("migration count", count, e)
	}
	if _, e := db.Exec(ctx, `UPDATE public.site_migrations SET checksum='tampered' WHERE name='sql/001_auth.sql'`); e != nil {
		t.Fatal(e)
	}
	if e := Apply(ctx, db); e == nil || !strings.Contains(e.Error(), "checksum changed") {
		t.Fatal("checksum drift not rejected", e)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(legacy))
	if _, e := db.Exec(ctx, `UPDATE public.site_migrations SET checksum=$1 WHERE name='sql/001_auth.sql'`, sum); e != nil {
		t.Fatal(e)
	}
	if e := Apply(ctx, db); e != nil {
		t.Fatal(e)
	}
}

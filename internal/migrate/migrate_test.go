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

	// Reproduce a populated Part 1 installation, then apply only the new forward migrations.
	if _, e := db.Exec(ctx, `CREATE TABLE public.site_migrations(name text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"001_auth.sql", "002_workshop.sql", "003_history_policy.sql", "004_search_indexes.sql"} {
		name := "sql/" + n
		body, e := migrations.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		if n != "001_auth.sql" {
			if _, e := db.Exec(ctx, string(body)); e != nil {
				t.Fatal(e)
			}
		}
		if _, e := db.Exec(ctx, `INSERT INTO public.site_migrations(name,checksum) VALUES($1,$2)`, name, fmt.Sprintf("%x", sha256.Sum256(body))); e != nil {
			t.Fatal(e)
		}
	}
	var pid, vid, fid int64
	if _, e := db.Exec(ctx, `INSERT INTO pvzce_site.accounts(user_id) VALUES($1)`, uid); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(ctx, `INSERT INTO pvzce_site.projects(owner_id,slug,title,kind,description,license,state) VALUES($1,'preserved','Preserved','datapack','Existing package','MIT','published') RETURNING id`, uid).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(ctx, `INSERT INTO pvzce_site.versions(project_id,creator_id,number,changelog,channel,state) VALUES($1,$2,'1.0','history','stable','published') RETURNING id`, pid, uid).Scan(&vid); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(ctx, `INSERT INTO pvzce_site.files(project_id,version_id,uploader_id,purpose,object_key,filename,size,sha256,scan_state,validation_state) VALUES($1,$2,$3,'package','private/existing','existing.zip',123,'preserved-hash','clean','valid') RETURNING id`, pid, vid, uid).Scan(&fid); e != nil {
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
	var hash, state string
	var safety bool
	if e := db.QueryRow(ctx, `SELECT sha256,content_state,safety_quarantined FROM pvzce_site.files WHERE id=$1`, fid).Scan(&hash, &state, &safety); e != nil || hash != "preserved-hash" || state != "published" || safety {
		t.Fatal("existing published file lost during upgrade", e)
	}
	var nameValue string
	if e := db.QueryRow(ctx, `SELECT name FROM pvzce_auth.users WHERE id=$1`, uid).Scan(&nameValue); e != nil || nameValue != "Preserved profile" {
		t.Fatal("legacy account lost", e)
	}
	var count int
	if e := db.QueryRow(ctx, `SELECT count(*) FROM public.site_migrations`).Scan(&count); e != nil || count != len(migrationNames()) {
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

func migrationNames() []string {
	files, _ := migrations.ReadDir("sql")
	names := []string{}
	for _, f := range files {
		names = append(names, f.Name())
	}
	return names
}

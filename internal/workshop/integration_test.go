package workshop

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
	"pvzce-site/internal/migrate"
)

type testObjects struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (o *testObjects) Put(_ context.Context, key string, r io.Reader, n int64) error {
	b, e := io.ReadAll(r)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.data[key] = b
	return e
}
func (o *testObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b, ok := o.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (o *testObjects) Delete(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.data, key)
	return nil
}

type testScanner struct {
	verdict string
	err     error
}

func (s testScanner) Scan(context.Context, io.Reader) (string, error) { return s.verdict, s.err }
func testService(t *testing.T) (*Service, []int64) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	cfg := config.Default().Database
	cfg.URL = url
	cfg.QueryTimeout = 15 * time.Second
	db, e := database.Open(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(db.Close)
	store, e := auth.NewStore(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	s := &Service{DB: db, Settings: Settings{MaxFile: 209715200, DailyQuota: 524288000, Location: time.UTC, HashKey: strings.Repeat("test-only-key-", 4), ScannerAddress: "test-injected-scanner"}, Objects: &testObjects{data: map[string][]byte{}}, Scanner: testScanner{verdict: "clean"}}
	var ids []int64
	for i := 0; i < 6; i++ {
		_, session, e := store.CreateSession(context.Background(), "test://workshop/"+rand.Text(), auth.User{Subject: rand.Text()}, time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, session.User.ID)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		queries := []string{
			`DELETE FROM pvzce_site.downloads WHERE file_id IN (SELECT id FROM pvzce_site.files WHERE uploader_id=ANY($1))`,
			`DELETE FROM pvzce_site.appeals WHERE user_id=ANY($1)`,
			`DELETE FROM pvzce_site.reviews WHERE actor_id=ANY($1)`,
			`DELETE FROM pvzce_site.audit WHERE actor_id=ANY($1)`,
			`UPDATE pvzce_site.projects SET icon_file_id=NULL WHERE owner_id=ANY($1)`,
			`DELETE FROM pvzce_site.uploads WHERE user_id=ANY($1)`,
			`DELETE FROM pvzce_site.files WHERE uploader_id=ANY($1)`,
			`DELETE FROM pvzce_site.dependencies WHERE version_id IN (SELECT id FROM pvzce_site.versions WHERE creator_id=ANY($1))`,
			`DELETE FROM pvzce_site.version_compatibility WHERE version_id IN (SELECT id FROM pvzce_site.versions WHERE creator_id=ANY($1))`,
			`DELETE FROM pvzce_site.versions WHERE creator_id=ANY($1)`,
			`DELETE FROM pvzce_site.collaborators WHERE user_id=ANY($1)`,
			`DELETE FROM pvzce_site.projects WHERE owner_id=ANY($1)`,
			`DELETE FROM pvzce_site.quota_days WHERE scope=ANY(SELECT 'account:'||unnest($1::bigint[]))`,
			`DELETE FROM pvzce_auth.sessions WHERE user_id=ANY($1)`,
			`DELETE FROM pvzce_auth.users WHERE id=ANY($1)`,
		}
		for _, q := range queries {
			if _, e := db.Exec(ctx, q, ids); e != nil {
				t.Error("test cleanup:", e)
			}
		}
	})
	return s, ids
}
func projectInput() ProjectInput {
	return ProjectInput{Slug: "test-" + strings.ToLower(rand.Text()), Title: "Workshop searchable lifecycle", Kind: "datapack", Description: "PVZCE data pack", License: "MIT", Tags: []string{"test"}, ContentLabel: "general"}
}
func mustProject(t *testing.T, s *Service, uid int64) int64 {
	t.Helper()
	id, e := s.CreateProject(context.Background(), uid, projectInput())
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func mustVersion(t *testing.T, s *Service, uid, pid int64) int64 {
	t.Helper()
	id, e := s.CreateVersion(context.Background(), uid, pid, VersionInput{Number: "1.0." + fmt.Sprint(time.Now().UnixNano()), Channel: "stable", GameVersions: []string{"1.0.0"}})
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func uploadFixture(t *testing.T, s *Service, uid, pid, vid int64) File {
	t.Helper()
	f := archive(t, dataFiles())
	b, e := os.ReadFile(f.Name())
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	out, e := s.Upload(context.Background(), uid, UploadInput{ProjectID: pid, VersionID: vid, Purpose: "package", Filename: "test.zip", Size: int64(len(b)), ExpectedSHA256: hex.EncodeToString(sum[:]), IdempotencyKey: rand.Text(), IP: "192.0.2." + fmt.Sprint(uid%250+1)}, bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func transition(t *testing.T, s *Service, uid, pid, vid int64, action string) {
	t.Helper()
	if e := s.Transition(context.Background(), uid, pid, vid, ReviewInput{Action: action, Reason: "integration validation"}); e != nil {
		t.Fatalf("%s: %v", action, e)
	}
}
func TestWorkshopLifecycleIntegration(t *testing.T) {
	s, ids := testService(t)
	ctx := context.Background()
	owner, reviewer, admin, stranger := ids[0], ids[1], ids[2], ids[3]
	// Site roles are explicitly granted, never inferred from OIDC claims.
	for _, r := range []struct {
		id   int64
		role string
	}{{reviewer, "reviewer"}, {admin, "admin"}} {
		if _, e := s.Profile(ctx, r.id); e != nil {
			t.Fatal(e)
		}
		if _, e := s.DB.Exec(ctx, `UPDATE pvzce_site.accounts SET role=$2 WHERE user_id=$1`, r.id, r.role); e != nil {
			t.Fatal(e)
		}
	}
	pid := mustProject(t, s, owner)
	vid := mustVersion(t, s, owner, pid)
	if e := s.EditProject(ctx, stranger, pid, projectInput()); !errors.Is(e, ErrForbidden) {
		t.Fatalf("cross-owner edit: %v", e)
	}
	if _, e := s.CreateVersion(ctx, stranger, pid, VersionInput{Number: "1", Channel: "stable", GameVersions: []string{"1.0.0"}}); !errors.Is(e, ErrForbidden) {
		t.Fatalf("cross-owner version: %v", e)
	}
	if e := s.ManageAccount(ctx, reviewer, stranger, "admin", "active", "attempt"); !errors.Is(e, ErrForbidden) {
		t.Fatalf("reviewer role escalation: %v", e)
	}
	if e := s.ManageAccount(ctx, admin, admin, "user", "active", "self"); !errors.Is(e, ErrInvalid) {
		t.Fatalf("self-role mutation: %v", e)
	}
	file := uploadFixture(t, s, owner, pid, vid)
	if file.ScanState != "clean" || file.ValidationState != "valid" {
		t.Fatalf("file not ready: %+v", file)
	}
	if _, r, e := s.Download(ctx, owner, file.ID, "192.0.2.1", false); e == nil {
		r.Close()
		t.Fatal("quarantined package downloadable by author")
	}
	if _, r, e := s.ReviewFile(ctx, owner, file.ID); e == nil {
		r.Close()
		t.Fatal("self review inspection allowed")
	}
	if _, r, e := s.ReviewFile(ctx, reviewer, file.ID); e != nil {
		t.Fatal(e)
	} else {
		r.Close()
	}
	transition(t, s, owner, pid, 0, "submit")
	transition(t, s, owner, pid, vid, "submit")
	if e := s.Transition(ctx, owner, pid, 0, ReviewInput{Action: "approve", Reason: "self"}); !errors.Is(e, ErrForbidden) {
		t.Fatal("owner approved own project", e)
	}
	if e := s.Collaborator(ctx, owner, pid, reviewer, false); e != nil {
		t.Fatal(e)
	}
	if e := s.Transition(ctx, reviewer, pid, 0, ReviewInput{Action: "approve", Reason: "collaborator"}); !errors.Is(e, ErrForbidden) {
		t.Fatal("collaborator review allowed", e)
	}
	if e := s.Collaborator(ctx, owner, pid, reviewer, true); e != nil {
		t.Fatal(e)
	}
	transition(t, s, reviewer, pid, 0, "approve")
	transition(t, s, reviewer, pid, vid, "approve")
	for i := 0; i < 2; i++ {
		_, reader, e := s.Download(ctx, 0, file.ID, "192.0.2.1", false)
		if e != nil {
			t.Fatal(e)
		}
		content, e := io.ReadAll(reader)
		reader.Close()
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != file.SHA256 {
			t.Fatal("download hash mismatch")
		}
	}
	detail, e := s.Detail(ctx, 0, pid)
	if e != nil || detail.Project.Downloads != 1 || detail.Versions[0].Downloads != 1 {
		t.Fatalf("download dedup: %+v %v", detail, e)
	}
	page, e := s.List(ctx, 0, Filter{Search: "searchable", Kind: "datapack", Tag: "test", GameVersion: "1.0.0", Sort: "downloads", Size: 1, Page: 1})
	if e != nil || len(page.Items) != 1 || page.Total < 1 {
		t.Fatal("search/page", page, e)
	}
	if _, e = s.List(ctx, 0, Filter{Sort: "'; DROP TABLE foo; --"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("sort injection accepted", e)
	}
	transition(t, s, owner, pid, vid, "withdraw")
	if _, r, e := s.Download(ctx, 0, file.ID, "192.0.2.1", false); e == nil {
		r.Close()
		t.Fatal("withdrawn version still downloads")
	}
	transition(t, s, owner, pid, vid, "resume")
	transition(t, s, owner, pid, vid, "submit")
	transition(t, s, reviewer, pid, vid, "approve")
	// Uploads cannot replace a published file even using a fresh idempotency key.
	f := archive(t, dataFiles())
	b, _ := os.ReadFile(f.Name())
	sum := sha256.Sum256(b)
	if _, e := s.Upload(ctx, owner, UploadInput{ProjectID: pid, VersionID: vid, Purpose: "package", Filename: "replace.zip", Size: int64(len(b)), ExpectedSHA256: hex.EncodeToString(sum[:]), IdempotencyKey: rand.Text(), IP: "192.0.2.2"}, bytes.NewReader(b)); !errors.Is(e, ErrInvalid) {
		t.Fatal("published replacement allowed", e)
	}
	transition(t, s, reviewer, pid, 0, "remove")
	if _, r, e := s.Download(ctx, 0, file.ID, "192.0.2.1", false); e == nil {
		r.Close()
		t.Fatal("removed project downloadable")
	}
	var reviewID int64
	if e := s.DB.QueryRow(ctx, `SELECT max(id) FROM pvzce_site.reviews WHERE project_id=$1 AND action='remove'`, pid).Scan(&reviewID); e != nil {
		t.Fatal(e)
	}
	appeal, e := s.Appeal(ctx, owner, reviewID, "please reconsider")
	if e != nil {
		t.Fatal(e)
	}
	if e := s.ResolveAppeal(ctx, reviewer, appeal, "own decision"); !errors.Is(e, ErrForbidden) {
		t.Fatal("same reviewer decided appeal", e)
	}
	if e := s.ResolveAppeal(ctx, admin, appeal, "independent response"); e != nil {
		t.Fatal(e)
	}
	transition(t, s, reviewer, pid, 0, "restore")
	transition(t, s, reviewer, pid, 0, "approve")
	// Metadata edits hide the existing public files until review is repeated.
	edit := detail.Project.ProjectInput
	edit.Title = "Edited title"
	if e := s.EditProject(ctx, owner, pid, edit); e != nil {
		t.Fatal(e)
	}
	if _, r, e := s.Download(ctx, 0, file.ID, "192.0.2.1", false); e == nil {
		r.Close()
		t.Fatal("edited draft remained public")
	}
	if e := s.ManageAccount(ctx, admin, owner, "user", "restricted", "integration restriction"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CreateProject(ctx, owner, projectInput()); !errors.Is(e, ErrForbidden) {
		t.Fatal("restricted account created project", e)
	}
	if e := s.ManageAccount(ctx, admin, owner, "user", "active", "lift test restriction"); e != nil {
		t.Fatal(e)
	}
	if e := migrate.Apply(ctx, s.DB); e != nil {
		t.Fatal("repeat migration", e)
	}
	p, e := s.Detail(ctx, owner, pid)
	if e != nil || p.Project.Title != "Edited title" {
		t.Fatal("migration lost content", e)
	}
}
func TestQuotaConcurrentAccountingIntegration(t *testing.T) {
	s, ids := testService(t)
	ctx := context.Background()
	s.Settings.MaxFile = 100
	s.Settings.DailyQuota = 100
	ip := rand.Text()
	var mu sync.Mutex
	var reservations []Reservation
	var failures []error
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, e := s.Begin(ctx, ids[i%len(ids)], ip, rand.Text(), "same payload", 60)
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				reservations = append(reservations, r)
			} else {
				failures = append(failures, e)
			}
		}(i)
	}
	wg.Wait()
	if len(reservations) != 1 {
		t.Fatalf("shared-IP overadmission: %d successes, %v", len(reservations), failures)
	}
	for _, e := range failures {
		if !errors.Is(e, ErrQuota) {
			t.Fatal(e)
		}
	}
	r := reservations[0]
	if e := s.AuthorizeRead(ctx, r.ID, 20); e != nil {
		t.Fatal(e)
	}
	if e := s.Finish(ctx, r.ID, 13, "failed", nil); e != nil {
		t.Fatal(e)
	}
	var used, held int64
	if e := s.DB.QueryRow(ctx, `SELECT used,reserved FROM pvzce_site.quota_days WHERE day=$1 AND scope=$2`, r.Day, "ip:"+r.IPHash).Scan(&used, &held); e != nil || used != 13 || held != 0 {
		t.Fatalf("settlement used=%d held=%d: %v", used, held, e)
	}
	r, e := s.Begin(ctx, ids[0], rand.Text(), rand.Text(), "expires", 40)
	if e != nil {
		t.Fatal(e)
	}
	if e := s.AuthorizeRead(ctx, r.ID, 17); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB.Exec(ctx, `UPDATE pvzce_site.uploads SET expires_at=now()-interval '1 second' WHERE id=$1`, r.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.Reap(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.AuthorizeRead(ctx, r.ID, 1); !errors.Is(e, ErrConflict) {
		t.Fatal("expired upload continued", e)
	}
	if e := s.DB.QueryRow(ctx, `SELECT used,reserved FROM pvzce_site.quota_days WHERE day=$1 AND scope=$2`, r.Day, "ip:"+r.IPHash).Scan(&used, &held); e != nil || used != 17 || held != 0 {
		t.Fatalf("expiry accounting used=%d held=%d: %v", used, held, e)
	}
	// One account cannot evade its quota by changing IP.
	s.Settings.DailyQuota = 30
	if _, e := s.Begin(ctx, ids[0], rand.Text(), rand.Text(), "account quota", 30); !errors.Is(e, ErrQuota) {
		t.Fatalf("account quota bypass: %v", e)
	}
}
func TestScanFailureAndPrivateAttachmentsIntegration(t *testing.T) {
	s, ids := testService(t)
	ctx := context.Background()
	pid := mustProject(t, s, ids[0])
	vid := mustVersion(t, s, ids[0], pid)
	s.Scanner = testScanner{err: ErrUnavailable}
	file := uploadFixture(t, s, ids[0], pid, vid)
	if file.ScanState != "failed" {
		t.Fatal("scan failure marked safe", file)
	}
	if e := s.Transition(ctx, ids[0], pid, vid, ReviewInput{Action: "submit", Reason: "try"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("unscanned version submitted", e)
	}
	s.Scanner = testScanner{verdict: "clean"}
	if e := s.RetryScan(ctx, ids[0], file.ID); e != nil {
		t.Fatal(e)
	}
	d, e := s.Detail(ctx, ids[0], pid)
	if e != nil || d.Versions[0].State != "draft" {
		t.Fatal("rescan auto-published", e)
	}
	content := []byte("private log fixture\n")
	sum := sha256.Sum256(content)
	in := UploadInput{ProjectID: pid, Purpose: "attachment", Filename: "log.txt", Size: int64(len(content)), ExpectedSHA256: hex.EncodeToString(sum[:]), IdempotencyKey: rand.Text(), IP: rand.Text()}
	attachment, e := s.Upload(ctx, ids[0], in, bytes.NewReader(content))
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Upload(ctx, ids[0], in, strings.NewReader("must not be read"))
	if e != nil || again.ID != attachment.ID {
		t.Fatal("idempotent completed retry", again, e)
	}
	if _, r, e := s.Download(ctx, 0, attachment.ID, "anon", false); e == nil {
		r.Close()
		t.Fatal("private attachment leaked")
	}
	if _, r, e := s.Download(ctx, ids[1], attachment.ID, "other", false); e == nil {
		r.Close()
		t.Fatal("private attachment IDOR")
	}
	if _, r, e := s.Download(ctx, ids[0], attachment.ID, "self", false); e != nil {
		t.Fatal(e)
	} else {
		r.Close()
	}
	s.Scanner = testScanner{verdict: "infected"}
	bad := uploadFixture(t, s, ids[0], pid, mustVersion(t, s, ids[0], pid))
	if e := s.RetryScan(ctx, ids[0], bad.ID); !errors.Is(e, ErrInvalid) {
		t.Fatal("infected verdict cleared", e)
	}
}
func TestTrustedProxyBoundaries(t *testing.T) {
	s := &Service{Settings: Settings{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.9:123"
	r.Header.Set("X-Forwarded-For", "198.51.100.5")
	ip, e := s.ClientIP(r)
	if e != nil || ip != "192.0.2.9" {
		t.Fatal("untrusted proxy spoof", ip, e)
	}
	r.RemoteAddr = "10.0.0.1:123"
	r.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.5, 10.0.0.2")
	ip, e = s.ClientIP(r)
	if e != nil || ip != "198.51.100.5" {
		t.Fatal("trusted chain", ip, e)
	}
}

func TestSharedQuotaInterruptAndIdempotencyIntegration(t *testing.T) {
	s, ids := testService(t)
	ctx := context.Background()
	uid := ids[0]
	pid := mustProject(t, s, uid)
	vid := mustVersion(t, s, uid, pid)
	file := uploadFixture(t, s, uid, pid, vid)
	var iconBytes bytes.Buffer
	if e := png.Encode(&iconBytes, image.NewRGBA(image.Rect(0, 0, 8, 8))); e != nil {
		t.Fatal(e)
	}
	icon := iconBytes.Bytes()
	sum := sha256.Sum256(icon)
	ip := rand.Text()
	in := UploadInput{ProjectID: pid, Purpose: "icon", Filename: "icon.png", Size: int64(len(icon)), ExpectedSHA256: hex.EncodeToString(sum[:]), IdempotencyKey: rand.Text(), IP: ip}
	if _, e := s.Upload(ctx, uid, in, bytes.NewReader(icon)); e != nil {
		t.Fatal(e)
	}
	text := []byte("private attachment")
	sum = sha256.Sum256(text)
	in = UploadInput{ProjectID: pid, Purpose: "attachment", Filename: "log.txt", Size: int64(len(text)), ExpectedSHA256: hex.EncodeToString(sum[:]), IdempotencyKey: rand.Text(), IP: ip}
	if _, e := s.Upload(ctx, uid, in, bytes.NewReader(text)); e != nil {
		t.Fatal(e)
	}
	var used, reserved int64
	scope := fmt.Sprintf("account:%d", uid)
	day := time.Now().UTC().Format("2006-01-02")
	if e := s.DB.QueryRow(ctx, `SELECT used,reserved FROM pvzce_site.quota_days WHERE day=$1 AND scope=$2`, day, scope).Scan(&used, &reserved); e != nil {
		t.Fatal(e)
	}
	want := file.Size + int64(len(icon)) + int64(len(text))
	if used != want || reserved != 0 {
		t.Fatalf("shared quota used=%d want=%d held=%d", used, want, reserved)
	}
	in.Size = 100
	in.IdempotencyKey = rand.Text()
	if _, e := s.Upload(ctx, uid, in, strings.NewReader("short")); !errors.Is(e, ErrInvalid) {
		t.Fatal("interrupted upload accepted", e)
	}
	if e := s.DB.QueryRow(ctx, `SELECT used,reserved FROM pvzce_site.quota_days WHERE day=$1 AND scope=$2`, day, scope).Scan(&used, &reserved); e != nil || used != want+5 || reserved != 0 {
		t.Fatalf("interruption accounting %d %d %v", used, reserved, e)
	}
	in.Size = s.Settings.MaxFile + 1
	in.IdempotencyKey = rand.Text()
	if _, e := s.Upload(ctx, uid, in, strings.NewReader("")); !errors.Is(e, ErrTooLarge) {
		t.Fatal("file limit ignored", e)
	}
	r, e := s.Begin(ctx, uid, ip, "pending-key", "one fingerprint", 10)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.Begin(ctx, uid, ip, "pending-key", "one fingerprint", 10); !errors.Is(e, ErrConflict) {
		t.Fatal("parallel idempotent retry admitted", e)
	}
	if _, e := s.Begin(ctx, uid, ip, "pending-key", "different fingerprint", 10); !errors.Is(e, ErrConflict) {
		t.Fatal("key payload drift accepted", e)
	}
	if e := s.Finish(ctx, r.ID, 0, "failed", nil); e != nil {
		t.Fatal(e)
	}
}

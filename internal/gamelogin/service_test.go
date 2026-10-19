package gamelogin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
)

type fixture struct {
	s     *Service
	store *auth.Store
	token string
	site  auth.Session
	db    *database.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL required for game login PostgreSQL integration")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.URL = raw
	cfg.Database.QueryTimeout = 10 * time.Second
	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	store, err := auth.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	token, site, err := store.CreateSession(ctx, "game-test:"+rand.Text(), auth.User{Subject: rand.Text(), Name: "Verified player"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(ctx, `DELETE FROM pvzce_auth.sessions WHERE user_id=$1`, site.User.ID); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec(ctx, `DELETE FROM pvzce_auth.users WHERE id=$1`, site.User.ID); err != nil {
			t.Error(err)
		}
	})
	g := cfg.GameLogin
	g.Enabled = true
	g.Origin = "https://site.example"
	g.Versions = []string{"1.0.0", "1.1.0"}
	s, err := New(ctx, db, g)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{s, store, token, site, db}
}
func (f *fixture) issue(t *testing.T) Ticket {
	t.Helper()
	ticket, err := f.s.Issue(context.Background(), f.token, f.site.CSRFToken, Request{1, "pvzce", "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}
func (f *fixture) exchange(t *testing.T, ticket Ticket) Grant {
	t.Helper()
	g, err := f.s.Exchange(context.Background(), ExchangeRequest{Request{1, "pvzce", "1.0.0"}, ticket.LoginCode})
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func TestSecretEncodingAndFormatting(t *testing.T) {
	for i := 0; i < 30; i++ {
		s := secret()
		if !ValidSecret(s) || len(s) != 43 {
			t.Fatal("invalid generated secret")
		}
	}
	for _, s := range []string{"", strings.Repeat("a", 32), strings.Repeat("a", 128), strings.Repeat("a", 42) + "=", strings.Repeat("a", 43) + "\n"} {
		if ValidSecret(s) {
			t.Fatal("invalid secret accepted")
		}
	}
	raw := secret()
	if strings.Contains(fmt.Sprint(Ticket{LoginCode: raw}, Grant{AccessToken: raw}, ExchangeRequest{LoginCode: raw}), raw) {
		t.Fatal("formatting exposes credential")
	}
	if Bearer("Bearer "+raw) != raw || Bearer("Basic "+raw) != "" || Bearer("Bearer  "+raw) != "" {
		t.Fatal("ambiguous bearer parsing")
	}
}
func TestHandoffAtomicAndHashedIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ticket := f.issue(t)
	if !ValidSecret(ticket.LoginCode) || ticket.Protocol != 1 || ticket.APIURL != f.s.Origin() || !ticket.ExpiresAt.After(time.Now()) || ticket.ExpiresAt.After(time.Now().Add(90*time.Second)) {
		t.Fatal("invalid ticket")
	}
	var hash []byte
	var row string
	if err := f.db.QueryRow(ctx, `SELECT code_hash,row_to_json(c)::text FROM pvzce_game.login_codes c WHERE user_id=$1`, f.site.User.ID).Scan(&hash, &row); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(ticket.LoginCode))
	if string(hash) != string(sum[:]) || strings.Contains(row, ticket.LoginCode) {
		t.Fatal("code stored in cleartext")
	}
	var wg sync.WaitGroup
	results := make(chan Grant, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, e := f.s.Exchange(ctx, ExchangeRequest{Request{1, "pvzce", "1.0.0"}, ticket.LoginCode})
			results <- g
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	successes := 0
	for e := range errs {
		if e == nil {
			successes++
		} else if !errors.Is(e, ErrUnauthorized) {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatal("concurrent single-use violation", successes)
	}
	var grant Grant
	for g := range results {
		if g.AccessToken != "" {
			grant = g
		}
	}
	if !ValidSecret(grant.AccessToken) || grant.TokenType != "Bearer" || grant.User.ID != f.site.User.ID || grant.User.DisplayName != "Verified player" || grant.ExpiresAt.After(f.site.ExpiresAt) || grant.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Fatal("invalid game grant")
	}
	if err := f.db.QueryRow(ctx, `SELECT token_hash,row_to_json(s)::text FROM pvzce_game.sessions s WHERE user_id=$1`, f.site.User.ID).Scan(&hash, &row); err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256([]byte(grant.AccessToken))
	if string(hash) != string(sum[:]) || strings.Contains(row, grant.AccessToken) || strings.Contains(row, ticket.LoginCode) {
		t.Fatal("game credential stored in cleartext")
	}
	// Lost exchange response is indistinguishable from a replay and cannot mint a second session.
	if _, e := f.s.Exchange(ctx, ExchangeRequest{Request{1, "pvzce", "1.0.0"}, ticket.LoginCode}); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("replayed exchange", e)
	}
	if session, e := f.s.Session(ctx, grant.AccessToken, false); e != nil || session.User.ID != f.site.User.ID || len(session.Scope) != 1 || session.Scope[0] != "game:session" {
		t.Fatal("session verification", e)
	}
	if _, e := f.store.GetSession(ctx, grant.AccessToken); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatal("game token accepted by website", e)
	}
	if _, e := f.s.Session(ctx, f.token, false); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("website token accepted by game", e)
	}
	if _, e := f.s.Session(ctx, grant.AccessToken, true); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Session(ctx, grant.AccessToken, false); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("game logout did not revoke", e)
	}
}
func TestBindingsAndRevocationIntegration(t *testing.T) {
	for _, scenario := range []string{"code-expired", "site-expired", "logout-before", "logout-after", "restricted-before", "restricted-after", "csrf", "origin", "version", "protocol", "game", "session-expired", "session-origin", "rollback"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			ticket := f.issue(t)
			request := ExchangeRequest{Request{1, "pvzce", "1.0.0"}, ticket.LoginCode}
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, e := f.db.Exec(ctx, sql, args...); e != nil {
					t.Fatal(e)
				}
			}
			switch scenario {
			case "code-expired":
				exec(`UPDATE pvzce_game.login_codes SET created_at=now()-interval '120 seconds',expires_at=now()-interval '60 seconds' WHERE user_id=$1`, f.site.User.ID)
			case "site-expired":
				exec(`UPDATE pvzce_auth.sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1`, f.site.User.ID)
			case "logout-before":
				if e := f.store.DeleteSession(ctx, f.token); e != nil {
					t.Fatal(e)
				}
			case "restricted-before":
				exec(`UPDATE pvzce_site.accounts SET status='restricted' WHERE user_id=$1`, f.site.User.ID)
				exec(`UPDATE pvzce_site.accounts SET status='active' WHERE user_id=$1`, f.site.User.ID)
			case "csrf":
				if _, e := f.s.Issue(ctx, f.token, "wrong", request.Request); !errors.Is(e, ErrForbidden) {
					t.Fatal(e)
				}
				return
			case "origin":
				exec(`UPDATE pvzce_game.login_codes SET origin='https://other.example' WHERE user_id=$1`, f.site.User.ID)
			case "version":
				request.GameVersion = "1.1.0"
			case "protocol":
				request.Protocol = 2
			case "game":
				request.GameID = "other"
			case "rollback":
				// Force the session insert to fail after code deletion; both operations must roll back.
				exec(`UPDATE pvzce_auth.sessions SET expires_at=now()+interval '3 hours' WHERE user_id=$1`, f.site.User.ID)
				f.s.cfg.SessionTTL = 2 * time.Hour
				if _, e := f.s.Exchange(ctx, request); e == nil {
					t.Fatal("failed insert succeeded")
				}
				f.s.cfg.SessionTTL = time.Hour
				f.exchange(t, ticket)
				return
			default:
				grant := f.exchange(t, ticket)
				switch scenario {
				case "logout-after":
					if e := f.store.DeleteSession(ctx, f.token); e != nil {
						t.Fatal(e)
					}
				case "restricted-after":
					exec(`UPDATE pvzce_site.accounts SET status='restricted' WHERE user_id=$1`, f.site.User.ID)
					if _, e := f.s.Issue(ctx, f.token, f.site.CSRFToken, request.Request); !errors.Is(e, ErrForbidden) {
						t.Fatal("restricted issuance", e)
					}
					exec(`UPDATE pvzce_site.accounts SET status='active' WHERE user_id=$1`, f.site.User.ID)
				case "session-expired":
					exec(`UPDATE pvzce_game.sessions SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE user_id=$1`, f.site.User.ID)
				case "session-origin":
					exec(`UPDATE pvzce_game.sessions SET origin='https://other.example' WHERE user_id=$1`, f.site.User.ID)
				}
				if _, e := f.s.Session(ctx, grant.AccessToken, false); !errors.Is(e, ErrUnauthorized) {
					t.Fatal("revoked session accepted", e)
				}
				return
			}
			if _, e := f.s.Exchange(ctx, request); e == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}
func TestLimitsAndReadinessIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		f.issue(t)
	}
	if _, e := f.s.Issue(ctx, f.token, f.site.CSRFToken, Request{1, "pvzce", "1.0.0"}); !errors.Is(e, ErrRateLimited) {
		t.Fatal("account limit", e)
	}
	key := rand.Text()
	other := &Service{db: f.db, cfg: f.s.cfg}
	if e := f.s.Limit(ctx, "test", key, 1); e != nil {
		t.Fatal(e)
	}
	if e := other.Limit(ctx, "test", key, 1); !errors.Is(e, ErrRateLimited) {
		t.Fatal("limit not shared across service instances", e)
	}
	if e := f.s.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	// Read-only replicas must stop advertising support, even when reads succeed.
	cfg := config.Default().Database
	u, e := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	u.RawQuery = q.Encode()
	cfg.URL = u.String()
	readonly, e := database.Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer readonly.Close()
	if e := (&Service{db: readonly, cfg: f.s.cfg}).Ready(ctx); e == nil {
		t.Fatal("read-only database advertised capability")
	}
}

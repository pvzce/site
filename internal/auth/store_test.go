package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
)

func TestPersistentSessionsIntegration(t *testing.T) {
	connectionURL := os.Getenv("TEST_DATABASE_URL")
	if connectionURL == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL account integration tests")
	}
	cfg := config.Default().Database
	cfg.URL = connectionURL
	db, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	store, err := auth.NewStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state := rand.Text() + rand.Text()
	binding := rand.Text() + rand.Text()
	if err := store.SaveLogin(ctx, state, binding, auth.Login{Nonce: "nonce", Verifier: "verifier"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeLogin(ctx, state, "wrong browser"); !errors.Is(err, auth.ErrInvalidLogin) {
		t.Fatalf("wrong browser binding accepted: %v", err)
	}
	if login, err := store.ConsumeLogin(ctx, state, binding); err != nil || login.Nonce != "nonce" || login.Verifier != "verifier" {
		t.Fatalf("consume login: %v", err)
	}
	if _, err := store.ConsumeLogin(ctx, state, binding); !errors.Is(err, auth.ErrInvalidLogin) {
		t.Fatalf("state replay accepted: %v", err)
	}
	issuer := "test://" + rand.Text()
	user := auth.User{Subject: rand.Text(), Username: "integration", Name: "Test"}
	token, session, err := store.CreateSession(ctx, issuer, user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(context.Background(), "DELETE FROM pvzce_auth.sessions WHERE user_id=$1", session.User.ID); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec(context.Background(), "DELETE FROM pvzce_auth.users WHERE id=$1", session.User.ID); err != nil {
			t.Error(err)
		}
	})
	// A new repository instance must recover the existing session without in-memory state.
	reopened, err := auth.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.GetSession(ctx, token)
	if err != nil || loaded.User.ID != session.User.ID || loaded.CSRFToken != session.CSRFToken {
		t.Fatalf("session did not survive repository reinitialization: %v", err)
	}
	sum := sha256.Sum256([]byte(token))
	var stored []byte
	if err := db.QueryRow(ctx, "SELECT token_hash FROM pvzce_auth.sessions WHERE user_id=$1", session.User.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, sum[:]) || bytes.Contains(stored, []byte(token)) {
		t.Fatal("session bearer token was not hashed in storage")
	}
	second, another, err := store.CreateSession(ctx, issuer, user, time.Hour)
	if err != nil || another.User.ID != session.User.ID {
		t.Fatalf("the same OIDC identity was duplicated: %v", err)
	}
	if err := store.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession(ctx, token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("revoked token accepted: %v", err)
	}
	sum = sha256.Sum256([]byte(second))
	if _, err := db.Exec(ctx, "UPDATE pvzce_auth.sessions SET expires_at=now()-interval '1 minute' WHERE token_hash=$1", sum[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession(ctx, second); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expired token accepted: %v", err)
	}
}

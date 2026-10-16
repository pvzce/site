package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"pvzce-site/internal/database"
)

var (
	ErrUnauthorized = errors.New("authentication required")
	ErrInvalidLogin = errors.New("invalid or expired login")
)

//go:embed schema.sql
var schema string

type User struct {
	ID       int64  `json:"id"`
	Subject  string `json:"subject"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Picture  string `json:"picture"`
}

type Session struct {
	User      User      `json:"user"`
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Login struct{ Nonce, Verifier string }

// SessionStore allows deterministic protocol tests while production uses PostgreSQL.
type SessionStore interface {
	SaveLogin(context.Context, string, string, Login) error
	ConsumeLogin(context.Context, string, string) (Login, error)
	CreateSession(context.Context, string, User, time.Duration) (string, Session, error)
	GetSession(context.Context, string) (Session, error)
	DeleteSession(context.Context, string) error
}

type Store struct{ db *database.DB }

func NewStore(ctx context.Context, db *database.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("authentication requires a database")
	}
	if err := db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(706879122531)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	}); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func digest(token string) []byte { sum := sha256.Sum256([]byte(token)); return sum[:] }

func (s *Store) SaveLogin(ctx context.Context, state, binding string, login Login) error {
	return s.db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM pvzce_auth.login_states WHERE state_hash IN (SELECT state_hash FROM pvzce_auth.login_states WHERE expires_at <= now() LIMIT 1000)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO pvzce_auth.login_states (state_hash, binding_hash, nonce, verifier, expires_at) VALUES ($1,$2,$3,$4,now()+interval '5 minutes')`, digest(state), digest(binding), login.Nonce, login.Verifier)
		return err
	})
}

func (s *Store) ConsumeLogin(ctx context.Context, state, binding string) (Login, error) {
	var login Login
	err := s.db.QueryRow(ctx, `DELETE FROM pvzce_auth.login_states WHERE state_hash=$1 AND binding_hash=$2 AND expires_at>now() RETURNING nonce,verifier`, digest(state), digest(binding)).Scan(&login.Nonce, &login.Verifier)
	if errors.Is(err, pgx.ErrNoRows) {
		return Login{}, ErrInvalidLogin
	}
	return login, err
}

func (s *Store) CreateSession(ctx context.Context, issuer string, user User, ttl time.Duration) (string, Session, error) {
	token := rand.Text() + rand.Text()
	session := Session{User: user, CSRFToken: rand.Text() + rand.Text(), ExpiresAt: time.Now().Add(ttl)}
	err := s.db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO pvzce_auth.users (issuer,subject,username,name,email,picture) VALUES ($1,$2,$3,$4,$5,$6)
            ON CONFLICT (issuer,subject) DO UPDATE SET username=excluded.username,name=excluded.name,email=excluded.email,picture=excluded.picture,last_login_at=now() RETURNING id`, issuer, user.Subject, user.Username, user.Name, user.Email, user.Picture).Scan(&session.User.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM pvzce_auth.sessions WHERE token_hash IN (SELECT token_hash FROM pvzce_auth.sessions WHERE expires_at <= now() LIMIT 1000)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO pvzce_auth.sessions (token_hash,user_id,csrf_token,expires_at) VALUES ($1,$2,$3,$4)", digest(token), session.User.ID, session.CSRFToken, session.ExpiresAt)
		return err
	})
	return token, session, err
}

func (s *Store) GetSession(ctx context.Context, token string) (Session, error) {
	if len(token) != 52 {
		return Session{}, ErrUnauthorized
	}
	var session Session
	err := s.db.QueryRow(ctx, `SELECT u.id,u.subject,u.username,u.name,u.email,u.picture,s.csrf_token,s.expires_at FROM pvzce_auth.sessions s JOIN pvzce_auth.users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()`, digest(token)).Scan(&session.User.ID, &session.User.Subject, &session.User.Username, &session.User.Name, &session.User.Email, &session.User.Picture, &session.CSRFToken, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrUnauthorized
	}
	return session, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM pvzce_auth.sessions WHERE token_hash=$1", digest(token))
	return err
}

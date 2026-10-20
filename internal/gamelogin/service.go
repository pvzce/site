// Package gamelogin implements short-lived identity handoff, not multiplayer authorization.
package gamelogin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"pvzce-site/internal/config"
	"pvzce-site/internal/database"
	"pvzce-site/internal/migrate"
)

var (
	ErrInvalid      = errors.New("invalid game login request")
	ErrUnauthorized = errors.New("invalid or expired game credential")
	ErrForbidden    = errors.New("game login is not permitted")
	ErrRateLimited  = errors.New("too many game login requests")
)

type Request struct {
	Protocol    int    `json:"protocol"`
	GameID      string `json:"game_id"`
	GameVersion string `json:"game_version"`
}
type ExchangeRequest struct {
	Request
	LoginCode string `json:"login_code"`
}
type Ticket struct {
	Protocol  int       `json:"protocol"`
	APIURL    string    `json:"api_url"`
	LoginCode string    `json:"login_code"`
	ExpiresAt time.Time `json:"expires_at"`
}
type User struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
}
type Session struct {
	User      User      `json:"user"`
	ExpiresAt time.Time `json:"expires_at"`
	Scope     []string  `json:"scope"`
}
type Grant struct {
	Session
	TokenType   string `json:"token_type"`
	AccessToken string `json:"access_token"`
}

// Prevent accidental formatting of response objects from exposing credentials.
func (Ticket) String() string          { return "GameLoginTicket[redacted]" }
func (Grant) String() string           { return "GameSessionGrant[redacted]" }
func (ExchangeRequest) String() string { return "GameExchangeRequest[redacted]" }

type Service struct {
	db  *database.DB
	cfg config.GameLogin
}

func New(ctx context.Context, db *database.DB, cfg config.GameLogin) (*Service, error) {
	if db == nil || !cfg.Enabled || len(cfg.Versions) == 0 || cfg.CodeTTL < time.Second || cfg.CodeTTL > 90*time.Second || cfg.SessionTTL < time.Second || cfg.SessionTTL > time.Hour {
		return nil, errors.New("game login is not configured")
	}
	origin, err := config.GameLoginOrigin(cfg.Origin, true)
	if err != nil || origin != cfg.Origin {
		return nil, errors.New("invalid game login origin")
	}
	for _, v := range cfg.Versions {
		if !config.ValidGameVersion(v) {
			return nil, ErrInvalid
		}
	}
	cfg.Versions = append([]string(nil), cfg.Versions...)
	if err = migrate.Apply(ctx, db); err != nil {
		return nil, err
	}
	s := &Service{db: db, cfg: cfg}
	return s, s.Ready(ctx)
}
func (s *Service) Origin() string { return s.cfg.Origin }
func (s *Service) Ready(ctx context.Context) error {
	// Schema migrations happen at startup. Do not advertise if required storage or write
	// privileges have since disappeared, or this connection is on a read-only replica.
	var ready bool
	err := s.db.QueryRow(ctx, `SELECT current_setting('transaction_read_only')='off'

 AND (SELECT bool_and(has_table_privilege(t,p)) FROM (VALUES
 ('pvzce_game.login_codes','SELECT'),('pvzce_game.login_codes','INSERT'),('pvzce_game.login_codes','DELETE'),
 ('pvzce_game.sessions','SELECT'),('pvzce_game.sessions','INSERT'),('pvzce_game.sessions','DELETE'),('pvzce_game.sessions','UPDATE'),
 ('pvzce_game.rate_limits','SELECT'),('pvzce_game.rate_limits','INSERT'),('pvzce_game.rate_limits','UPDATE'),('pvzce_game.rate_limits','DELETE'),
 ('pvzce_auth.sessions','SELECT'),('pvzce_auth.sessions','UPDATE'),('pvzce_auth.users','SELECT'),('pvzce_auth.users','UPDATE'),
 ('pvzce_site.accounts','SELECT'),('pvzce_site.accounts','INSERT'),('pvzce_site.accounts','UPDATE')) AS permissions(t,p))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='revoke_game_on_restriction' AND tgrelid='pvzce_site.accounts'::regclass AND tgenabled='O')`).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("game login storage is unavailable")
	}
	return nil
}
func digest(raw string) []byte { h := sha256.Sum256([]byte(raw)); return h[:] }
func secret() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func ValidSecret(v string) bool {
	// This backend issues exactly 256 bits in the canonical unpadded encoding.
	b, e := base64.RawURLEncoding.DecodeString(v)
	return e == nil && len(v) == 43 && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == v
}
func (s *Service) validate(r Request) error {
	if r.Protocol != 1 || r.GameID != "pvzce" || !config.ValidGameVersion(r.GameVersion) {
		return ErrInvalid
	}
	for _, v := range s.cfg.Versions {
		if v == r.GameVersion {
			return nil
		}
	}
	return ErrForbidden
}

// Limit uses database-backed, fixed one-minute windows shared by all replicas.
// Network keys must come from RemoteAddr, never untrusted forwarded headers.
func (s *Service) Limit(ctx context.Context, category, key string, maximum int) error {
	var n int
	err := s.db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := cleanup(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM pvzce_game.rate_limits WHERE key_hash IN (SELECT key_hash FROM pvzce_game.rate_limits WHERE window_start<now()-interval '2 minutes' LIMIT 1000)`); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO pvzce_game.rate_limits(key_hash,window_start,attempts) VALUES($1,date_trunc('minute',clock_timestamp()),1)
  ON CONFLICT(key_hash) DO UPDATE SET attempts=CASE WHEN pvzce_game.rate_limits.window_start=excluded.window_start THEN LEAST(pvzce_game.rate_limits.attempts+1,1000000) ELSE 1 END,window_start=excluded.window_start RETURNING attempts`, digest(category+":"+key)).Scan(&n)
	})
	if err != nil {
		return err
	}
	if n > maximum {
		return ErrRateLimited
	}
	return nil
}

// identity locks account -> source session. Restriction and website logout must
// wait for these shared locks and then revoke every dependent grant atomically.
func identity(ctx context.Context, tx pgx.Tx, uid int64, source []byte) (User, string, time.Time, error) {
	var user User
	var status, csrf string
	var expiry time.Time
	if _, err := tx.Exec(ctx, `INSERT INTO pvzce_site.accounts(user_id) VALUES($1) ON CONFLICT DO NOTHING`, uid); err != nil {
		return user, csrf, expiry, err
	}
	err := tx.QueryRow(ctx, `SELECT u.id,COALESCE(NULLIF(a.display_name,''),NULLIF(u.name,''),u.username),a.status FROM pvzce_auth.users u JOIN pvzce_site.accounts a ON a.user_id=u.id WHERE u.id=$1 FOR SHARE OF a,u`, uid).Scan(&user.ID, &user.DisplayName, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return user, csrf, expiry, ErrUnauthorized
	}
	if err != nil {
		return user, csrf, expiry, err
	}
	if status != "active" {
		return user, csrf, expiry, ErrForbidden
	}
	err = tx.QueryRow(ctx, `SELECT csrf_token,expires_at FROM pvzce_auth.sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`, source, uid).Scan(&csrf, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrUnauthorized
	}
	return user, csrf, expiry, err
}
func (s *Service) Issue(ctx context.Context, websiteToken, csrf string, r Request) (Ticket, error) {
	if err := s.validate(r); err != nil {
		return Ticket{}, err
	}
	if len(websiteToken) != 52 || csrf == "" {
		return Ticket{}, ErrUnauthorized
	}
	source := digest(websiteToken)
	var uid int64
	err := s.db.QueryRow(ctx, `SELECT user_id FROM pvzce_auth.sessions WHERE token_hash=$1 AND expires_at>clock_timestamp()`, source).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, ErrUnauthorized
	}
	if err != nil {
		return Ticket{}, err
	}
	if err = s.Limit(ctx, "issue-user", fmt.Sprint(uid), 10); err != nil {
		return Ticket{}, err
	}
	ticket := Ticket{Protocol: 1, APIURL: s.cfg.Origin, LoginCode: secret()}
	err = s.db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, expected, expires, err := identity(ctx, tx, uid, source)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(csrf), []byte(expected)) != 1 {
			return ErrForbidden
		}
		return tx.QueryRow(ctx, `WITH t AS (SELECT clock_timestamp() AS now) INSERT INTO pvzce_game.login_codes(code_hash,source_hash,user_id,origin,protocol,game_id,game_version,created_at,expires_at)
   SELECT $1,$2,$3,$4,1,'pvzce',$5,t.now,LEAST($6::timestamptz,t.now+$7::double precision*interval '1 second') FROM t RETURNING expires_at`, digest(ticket.LoginCode), source, uid, s.cfg.Origin, r.GameVersion, expires, s.cfg.CodeTTL.Seconds()).Scan(&ticket.ExpiresAt)
	})
	if err != nil {
		return Ticket{}, err
	}
	return ticket, nil
}
func cleanup(ctx context.Context, tx pgx.Tx) error {
	for _, spec := range []struct{ table, key string }{{"login_codes", "code_hash"}, {"sessions", "token_hash"}} {
		if _, err := tx.Exec(ctx, `DELETE FROM pvzce_game.`+spec.table+` WHERE `+spec.key+` IN (SELECT `+spec.key+` FROM pvzce_game.`+spec.table+` WHERE expires_at<=clock_timestamp() LIMIT 1000)`); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) Exchange(ctx context.Context, r ExchangeRequest) (Grant, error) {
	if err := s.validate(r.Request); err != nil {
		return Grant{}, err
	}
	if !ValidSecret(r.LoginCode) {
		return Grant{}, ErrUnauthorized
	}
	hash := digest(r.LoginCode)
	var uid int64
	var source []byte
	err := s.db.QueryRow(ctx, `SELECT user_id,source_hash FROM pvzce_game.login_codes WHERE code_hash=$1`, hash).Scan(&uid, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrUnauthorized
	}
	if err != nil {
		return Grant{}, err
	}
	grant := Grant{TokenType: "Bearer", AccessToken: secret(), Session: Session{Scope: []string{"game:session"}}}
	err = s.db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		user, _, expires, err := identity(ctx, tx, uid, source)
		if err != nil {
			return err
		}
		grant.User = user
		tag, err := tx.Exec(ctx, `DELETE FROM pvzce_game.login_codes WHERE code_hash=$1 AND user_id=$2 AND source_hash=$3 AND origin=$4 AND protocol=$5 AND game_id=$6 AND game_version=$7 AND expires_at>clock_timestamp()`, hash, uid, source, s.cfg.Origin, r.Protocol, r.GameID, r.GameVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrUnauthorized
		}
		return tx.QueryRow(ctx, `WITH t AS (SELECT clock_timestamp() AS now) INSERT INTO pvzce_game.sessions(token_hash,source_hash,user_id,origin,protocol,game_id,game_version,scope,created_at,expires_at)
   SELECT $1,$2,$3,$4,1,'pvzce',$5,'game:session',t.now,LEAST($6::timestamptz,t.now+$7::double precision*interval '1 second') FROM t RETURNING expires_at`, digest(grant.AccessToken), source, uid, s.cfg.Origin, r.GameVersion, expires, s.cfg.SessionTTL.Seconds()).Scan(&grant.ExpiresAt)
	})
	if err != nil {
		return Grant{}, err
	}
	return grant, nil
}
func (s *Service) Session(ctx context.Context, token string, logout bool) (Session, error) {
	if !ValidSecret(token) {
		return Session{}, ErrUnauthorized
	}
	hash := digest(token)
	var uid int64
	var source []byte
	err := s.db.QueryRow(ctx, `SELECT user_id,source_hash FROM pvzce_game.sessions WHERE token_hash=$1`, hash).Scan(&uid, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, err
	}
	session := Session{Scope: []string{"game:session"}}
	err = s.db.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		user, _, _, err := identity(ctx, tx, uid, source)
		if err != nil {
			return err
		}
		session.User = user
		var version string
		lock := " FOR SHARE"
		if logout {
			lock = " FOR UPDATE"
		}
		err = tx.QueryRow(ctx, `SELECT expires_at,game_version FROM pvzce_game.sessions WHERE token_hash=$1 AND source_hash=$2 AND user_id=$3 AND origin=$4 AND protocol=1 AND game_id='pvzce' AND scope='game:session' AND expires_at>clock_timestamp()`+lock, hash, source, uid, s.cfg.Origin).Scan(&session.ExpiresAt, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if err = s.validate(Request{1, "pvzce", version}); err != nil {
			return err
		}
		if logout {
			_, err = tx.Exec(ctx, `DELETE FROM pvzce_game.sessions WHERE token_hash=$1`, hash)
		}
		return err
	})
	if err != nil {
		return Session{}, err
	}
	return session, nil
}

// Bearer accepts one canonical credential and never falls back to website cookies.
func Bearer(value string) string {
	fields := strings.Split(value, " ")
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || !ValidSecret(fields[1]) {
		return ""
	}
	return fields[1]
}

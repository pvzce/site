// Package workshop owns business authorization; Casdoor identities never grant site roles.
package workshop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"pvzce-site/internal/database"
	"pvzce-site/internal/migrate"
)

var (
	ErrForbidden   = errors.New("operation is not permitted")
	ErrNotFound    = errors.New("resource not found")
	ErrInvalid     = errors.New("invalid input or transition")
	ErrConflict    = errors.New("resource or upload already exists")
	ErrQuota       = errors.New("daily upload quota exceeded")
	ErrTooLarge    = errors.New("file exceeds configured size limit")
	ErrUnavailable = errors.New("required upload service unavailable")
)

type Service struct {
	DB       *database.DB
	Settings Settings
	Objects  ObjectStore
	Scanner  Scanner
}

func New(ctx context.Context, db *database.DB, s Settings) (*Service, error) {
	if err := migrate.Apply(ctx, db); err != nil {
		return nil, err
	}
	svc := &Service{DB: db, Settings: s, Scanner: Clamd{Address: s.ScannerAddress}}
	if s.Endpoint != "" {
		if len(s.HashKey) < 32 {
			return nil, fmt.Errorf("DOWNLOAD_HASH_KEY must contain at least 32 bytes")
		}
		obj, e := NewS3(ctx, s)
		if e != nil {
			return nil, e
		}
		svc.Objects = obj
	}
	return svc, nil
}
func Status(err error) int {
	switch {
	case errors.Is(err, ErrForbidden):
		return 403
	case errors.Is(err, ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		return 404
	case errors.Is(err, ErrInvalid):
		return 400
	case errors.Is(err, ErrConflict):
		return 409
	case errors.Is(err, ErrTooLarge):
		return 413
	case errors.Is(err, ErrQuota):
		return 429
	case errors.Is(err, ErrUnavailable):
		return 503
	}
	return http.StatusInternalServerError
}

type Actor struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
	Bio         string `json:"bio"`
	Status      string `json:"status"`
	Role        string `json:"role"`
}

func (s *Service) actor(ctx context.Context, tx pgx.Tx, uid int64) (Actor, error) {
	var a Actor
	if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.accounts(user_id) VALUES($1) ON CONFLICT DO NOTHING`, uid); e != nil {
		return a, e
	}
	e := tx.QueryRow(ctx, `SELECT user_id,display_name,bio,status,role FROM pvzce_site.accounts WHERE user_id=$1 FOR SHARE`, uid).Scan(&a.ID, &a.DisplayName, &a.Bio, &a.Status, &a.Role)
	if e == nil && a.Status != "active" {
		return a, ErrForbidden
	}
	return a, e
}
func (s *Service) Profile(ctx context.Context, uid int64) (Actor, error) {
	var a Actor
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error { var e error; a, e = s.actor(ctx, tx, uid); return e })
	return a, e
}
func (s *Service) EditProfile(ctx context.Context, uid int64, name, bio string) error {
	if len(name) > 100 || len(bio) > 4000 {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE pvzce_site.accounts SET display_name=$2,bio=$3,updated_at=now() WHERE user_id=$1`, uid, name, bio)
		if e != nil {
			return e
		}
		return audit(ctx, tx, uid, "profile.edit", fmt.Sprint(uid), "self-service")
	})
}
func audit(ctx context.Context, tx pgx.Tx, uid int64, action, resource, reason string) error {
	_, e := tx.Exec(ctx, `INSERT INTO pvzce_site.audit(actor_id,action,resource,reason) VALUES($1,$2,$3,$4)`, uid, action, resource, reason)
	return e
}
func (s *Service) ManageAccount(ctx context.Context, uid, target int64, role, status, reason string) error {
	if uid == target || !oneOf(role, "user", "reviewer", "admin") || !oneOf(status, "active", "restricted") || !reasonOK(reason) {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// Serialize role assignments with bootstrap and avoid last-admin removal.
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(706879122532)`); e != nil {
			return e
		}
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if a.Role != "admin" {
			return ErrForbidden
		}
		if _, e = tx.Exec(ctx, `INSERT INTO pvzce_site.accounts(user_id) VALUES($1) ON CONFLICT DO NOTHING`, target); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE pvzce_site.accounts SET role=$2,status=$3,updated_at=now() WHERE user_id=$1`, target, role, status)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return audit(ctx, tx, uid, "account.manage", fmt.Sprint(target), reason+"; role="+role+"; status="+status)
	})
}

// BootstrapAdmin is a local operator command, not an HTTP endpoint. It requires an existing verified OIDC account.
func (s *Service) BootstrapAdmin(ctx context.Context, uid int64) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(706879122532)`); e != nil {
			return e
		}
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM pvzce_site.accounts WHERE role='admin'`).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return ErrConflict
		}
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.accounts SET role='admin' WHERE user_id=$1`, uid); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "admin.bootstrap", fmt.Sprint(uid), "local operator bootstrap")
	})
}
func oneOf(v string, values ...string) bool {
	for _, x := range values {
		if v == x {
			return true
		}
	}
	return false
}
func reasonOK(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 4000 }

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,79}$`)

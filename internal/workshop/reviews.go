package workshop

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type ReviewInput struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

func (s *Service) Transition(ctx context.Context, uid, pid, vid int64, in ReviewInput) error {
	if !reasonOK(in.Reason) {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		var owner int64
		var projectState string
		if e := tx.QueryRow(ctx, `SELECT owner_id,state FROM pvzce_site.projects WHERE id=$1 FOR UPDATE`, pid).Scan(&owner, &projectState); e != nil {
			return e
		}
		m, e := member(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		table := "projects"
		id := pid
		state := projectState
		creator := owner
		if vid != 0 {
			table = "versions"
			id = vid
			if e := tx.QueryRow(ctx, `SELECT state,creator_id FROM pvzce_site.versions WHERE id=$1 AND project_id=$2 FOR UPDATE`, vid, pid).Scan(&state, &creator); e != nil {
				return e
			}
		}
		next := ""
		switch in.Action {
		case "submit":
			if !m {
				return ErrForbidden
			}
			if !oneOf(state, "draft", "rejected") {
				return ErrInvalid
			}
			if projectState == "removed" || projectState == "withdrawn" {
				return ErrInvalid
			}
			next = "pending"
		case "withdraw":
			if !m {
				return ErrForbidden
			}
			if oneOf(state, "removed", "withdrawn") {
				return ErrInvalid
			}
			next = "withdrawn"
		case "resume":
			if !m {
				return ErrForbidden
			}
			if state != "withdrawn" {
				return ErrInvalid
			}
			next = "draft"
		case "approve", "reject", "remove", "restore":
			own, e := reviewerConflict(ctx, tx, pid, uid)
			if e != nil {
				return e
			}
			if !oneOf(a.Role, "reviewer", "admin") || own || creator == uid {
				return ErrForbidden
			}
			switch in.Action {
			case "approve":
				if state != "pending" {
					return ErrInvalid
				}
				next = "published"
			case "reject":
				if state != "pending" {
					return ErrInvalid
				}
				next = "rejected"
			case "remove":
				if state == "removed" {
					return ErrInvalid
				}
				next = "removed"
			case "restore":
				if state != "removed" {
					return ErrInvalid
				}
				next = "pending"
			}
		default:
			return ErrInvalid
		}
		if oneOf(next, "pending", "published") {
			var bad int
			query := `SELECT count(*) FROM pvzce_site.files WHERE project_id=$1 AND (scan_state<>'clean' OR validation_state<>'valid') AND id=(SELECT icon_file_id FROM pvzce_site.projects WHERE id=$1)`
			args := []any{pid}
			if vid != 0 {
				query = `SELECT CASE WHEN count(*)=1 AND bool_and(scan_state='clean' AND validation_state='valid') THEN 0 ELSE 1 END FROM pvzce_site.files WHERE project_id=$1 AND version_id=$2 AND purpose='package'`
				args = append(args, vid)
			}
			if e := tx.QueryRow(ctx, query, args...).Scan(&bad); e != nil {
				return e
			}
			if bad != 0 {
				return ErrInvalid
			}
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.`+table+` SET previous_state=state,state=$2,updated_at=now() WHERE id=$1`, id, next); e != nil {
			return e
		}
		var v any
		if vid != 0 {
			v = vid
		}
		if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.reviews(project_id,version_id,actor_id,action,reason,from_state,to_state) VALUES($1,$2,$3,$4,$5,$6,$7)`, pid, v, uid, in.Action, in.Reason, state, next); e != nil {
			return e
		}
		return audit(ctx, tx, uid, table+"."+in.Action, fmt.Sprint(id), in.Reason)
	})
}
func (s *Service) Appeal(ctx context.Context, uid, review int64, reason string) (int64, error) {
	if !reasonOK(reason) {
		return 0, ErrInvalid
	}
	var id int64
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		var pid int64
		if e := tx.QueryRow(ctx, `SELECT project_id FROM pvzce_site.reviews WHERE id=$1 AND action IN ('reject','remove')`, review).Scan(&pid); e != nil {
			return e
		}
		ok, e := member(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		if !ok {
			return ErrForbidden
		}
		if e := tx.QueryRow(ctx, `INSERT INTO pvzce_site.appeals(review_id,user_id,reason) VALUES($1,$2,$3) RETURNING id`, review, uid, reason).Scan(&id); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "appeal.create", fmt.Sprint(id), reason)
	})
	return id, e
}
func (s *Service) ResolveAppeal(ctx context.Context, uid, id int64, reply string) error {
	if !reasonOK(reply) {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if !oneOf(a.Role, "reviewer", "admin") {
			return ErrForbidden
		}
		var pid, user, actor int64
		if e := tx.QueryRow(ctx, `SELECT r.project_id,a.user_id,r.actor_id FROM pvzce_site.appeals a JOIN pvzce_site.reviews r ON r.id=a.review_id WHERE a.id=$1 FOR UPDATE OF a`, id).Scan(&pid, &user, &actor); e != nil {
			return e
		}
		own, e := reviewerConflict(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		if own || user == uid || actor == uid {
			return ErrForbidden
		}
		tag, e := tx.Exec(ctx, `UPDATE pvzce_site.appeals SET state='resolved',reply=$2 WHERE id=$1 AND state='pending'`, id, reply)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalid
		}
		return audit(ctx, tx, uid, "appeal.resolve", fmt.Sprint(id), reply)
	})
}
func (s *Service) Appeals(ctx context.Context, uid int64) (json.RawMessage, error) {
	var raw json.RawMessage
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(json_agg(a ORDER BY a.id DESC),'[]'::json) FROM (SELECT id,review_id,user_id,reason,state,reply,created_at FROM pvzce_site.appeals WHERE user_id=$1 OR $2 ORDER BY id DESC LIMIT 100) a`, uid, oneOf(a.Role, "reviewer", "admin")).Scan(&raw)
	})
	return raw, e
}
func (s *Service) Queue(ctx context.Context, uid int64) (json.RawMessage, error) {
	var raw json.RawMessage
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if !oneOf(a.Role, "reviewer", "admin") {
			return ErrForbidden
		}
		return tx.QueryRow(ctx, `SELECT json_build_object('projects',COALESCE((SELECT json_agg(p) FROM (SELECT id,title,owner_id,state FROM pvzce_site.projects WHERE state='pending' ORDER BY updated_at,id LIMIT 100) p),'[]'::json),'versions',COALESCE((SELECT json_agg(v) FROM (SELECT id,project_id,number,creator_id,state FROM pvzce_site.versions WHERE state='pending' ORDER BY updated_at,id LIMIT 100) v),'[]'::json))`).Scan(&raw)
	})
	return raw, e
}
func (s *Service) Audit(ctx context.Context, uid int64) (json.RawMessage, error) {
	var raw json.RawMessage
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if a.Role != "admin" {
			return ErrForbidden
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(json_agg(a),'[]'::json) FROM (SELECT * FROM pvzce_site.audit ORDER BY id DESC LIMIT 100) a`).Scan(&raw)
	})
	return raw, e
}

// Historical contributors remain conflicted after collaborator removal; removing membership cannot enable self-review.
func reviewerConflict(ctx context.Context, tx pgx.Tx, pid, uid int64) (bool, error) {
	var conflict bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pvzce_site.projects WHERE id=$1 AND owner_id=$2) OR EXISTS(SELECT 1 FROM pvzce_site.collaborators WHERE project_id=$1 AND user_id=$2) OR EXISTS(SELECT 1 FROM pvzce_site.versions WHERE project_id=$1 AND creator_id=$2) OR EXISTS(SELECT 1 FROM pvzce_site.files WHERE project_id=$1 AND uploader_id=$2) OR EXISTS(SELECT 1 FROM pvzce_site.project_revisions WHERE project_id=$1 AND actor_id=$2)`, pid, uid).Scan(&conflict)
	return conflict, e
}

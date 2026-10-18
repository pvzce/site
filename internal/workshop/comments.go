package workshop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type PageInput struct {
	Before                  int64
	Size                    int
	IncludeSuggestive, Mine bool
}

func (p PageInput) limit() (int, error) {
	if p.Before < 0 {
		return 0, ErrInvalid
	}
	if p.Size == 0 {
		return 20, nil
	}
	if p.Size < 1 || p.Size > 100 || p.Before < 0 {
		return 0, ErrInvalid
	}
	return p.Size, nil
}

type Page[T any] struct {
	Items      []T   `json:"items"`
	NextBefore int64 `json:"next_before"`
}
type Comment struct {
	ID           int64     `json:"id"`
	ProjectID    int64     `json:"project_id"`
	UserID       int64     `json:"user_id"`
	Body         string    `json:"body"`
	Revision     int64     `json:"revision"`
	State        string    `json:"state"`
	ContentLabel string    `json:"content_label"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type CommentInput struct {
	Body         string `json:"body"`
	ContentLabel string `json:"content_label"`
	Revision     int64  `json:"revision"`
}
type ContentReview struct {
	ReviewInput
	Revision int64 `json:"revision"`
}

const commentCols = `c.id,c.project_id,c.user_id,c.body,c.revision,c.state,c.content_label,c.created_at,c.updated_at`

func scanComment(row pgx.Row) (Comment, error) {
	var c Comment
	e := row.Scan(&c.ID, &c.ProjectID, &c.UserID, &c.Body, &c.Revision, &c.State, &c.ContentLabel, &c.CreatedAt, &c.UpdatedAt)
	return c, e
}
func validComment(in CommentInput) bool {
	return strings.TrimSpace(in.Body) != "" && len(in.Body) <= 8000 && oneOf(in.ContentLabel, "general", "suggestive")
}
func (s *Service) projectVisible(ctx context.Context, tx pgx.Tx, uid, pid int64, include bool) error {
	var state, label string
	if e := tx.QueryRow(ctx, `SELECT state,content_label FROM pvzce_site.projects WHERE id=$1`, pid).Scan(&state, &label); e != nil {
		return e
	}
	if state == "published" && (label == "general" || include) {
		return nil
	}
	if uid == 0 {
		return ErrNotFound
	}
	a, e := s.identity(ctx, tx, uid)
	if e != nil {
		return e
	}
	m, e := member(ctx, tx, pid, uid)
	if e != nil {
		return e
	}
	if !m && !isStaff(a) {
		return ErrNotFound
	}
	return nil
}
func (s *Service) CreateComment(ctx context.Context, uid, pid int64, in CommentInput) (Comment, error) {
	var out Comment
	if in.ContentLabel == "" {
		in.ContentLabel = "general"
	}
	if !validComment(in) {
		return out, ErrInvalid
	}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM pvzce_site.projects WHERE id=$1 FOR SHARE`, pid).Scan(&state); e != nil {
			return e
		}
		if state != "published" {
			return ErrNotFound
		}
		if e := limitedActivity(ctx, tx, uid, "comment.write", 5, 50); e != nil {
			return e
		}
		var e error
		out, e = scanComment(tx.QueryRow(ctx, `INSERT INTO pvzce_site.comments AS c(project_id,user_id,body,content_label) VALUES($1,$2,$3,$4) RETURNING `+commentCols, pid, uid, in.Body, in.ContentLabel))
		if e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.comment_revisions(comment_id,revision,body,content_label) VALUES($1,1,$2,$3)`, out.ID, in.Body, in.ContentLabel); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "comment.write", fmt.Sprint(out.ID), "created; manual review required")
	})
	if e != nil {
		return out, e
	}
	return out, s.adviseComment(ctx, out)
}
func (s *Service) EditComment(ctx context.Context, uid, id int64, in CommentInput) (Comment, error) {
	var out Comment
	if !validComment(in) || in.Revision < 1 {
		return out, ErrInvalid
	}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		c, e := scanComment(tx.QueryRow(ctx, `SELECT `+commentCols+` FROM pvzce_site.comments c WHERE id=$1 FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if c.UserID != uid {
			return ErrForbidden
		}
		if c.Revision != in.Revision {
			return ErrConflict
		}
		if c.State == "removed" {
			return ErrForbidden
		}
		if e := limitedActivity(ctx, tx, uid, "comment.write", 5, 50); e != nil {
			return e
		}
		out, e = scanComment(tx.QueryRow(ctx, `UPDATE pvzce_site.comments c SET body=$2,content_label=$3,revision=revision+1,state='pending',updated_at=now() WHERE id=$1 RETURNING `+commentCols, id, in.Body, in.ContentLabel))
		if e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.comment_revisions VALUES($1,$2,$3,$4,now())`, id, out.Revision, out.Body, out.ContentLabel); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "comment.write", fmt.Sprint(id), "edited; previous approval invalidated")
	})
	if e != nil {
		return out, e
	}
	return out, s.adviseComment(ctx, out)
}
func (s *Service) adviseComment(ctx context.Context, c Comment) error {
	advice := Advice{ModelVersion: "unconfigured", Category: "uncertain", Recommendation: "manual", Reason: "No moderation provider configured; human review required", Confidence: 0}
	source := "fallback"
	if s.Moderator != nil {
		candidate, e := s.Moderator.Evaluate(ctx, ModerationInput{Text: c.Body, Kind: "comment", Policy: PolicyVersion})
		if e == nil && validAdvice(candidate) {
			advice = candidate
			source = "model"
		} else {
			advice.ModelVersion = "unavailable"
			advice.Reason = "Provider unavailable or invalid/uncertain contract; human review required"
		}
	}
	// Advisories are tied to immutable revisions and NEVER change published/role/account state.
	_, e := s.DB.Exec(ctx, `INSERT INTO pvzce_site.moderation_decisions(comment_id,revision,source,model_version,policy_version,category,recommendation,reason,confidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, c.ID, c.Revision, source, advice.ModelVersion, PolicyVersion, advice.Category, advice.Recommendation, advice.Reason, advice.Confidence)
	return e
}
func (s *Service) DeleteComment(ctx context.Context, uid, id int64) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		c, e := scanComment(tx.QueryRow(ctx, `SELECT `+commentCols+` FROM pvzce_site.comments c WHERE id=$1 FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if c.UserID != uid {
			return ErrForbidden
		}
		next, e := nextContentState(c.State, "withdraw")
		if e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.comments SET state=$2,updated_at=now() WHERE id=$1`, id, next); e != nil {
			return e
		}
		return recordAction(ctx, tx, uid, "comment", id, c.Revision, ReviewInput{Action: "withdraw", Reason: "author deletion"}, c.State, next, c.ContentLabel)
	})
}
func (s *Service) ReviewComment(ctx context.Context, uid, id int64, in ContentReview) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error { return s.reviewCommentTx(ctx, tx, uid, id, in) })
}
func (s *Service) reviewCommentTx(ctx context.Context, tx pgx.Tx, uid, id int64, in ContentReview) error {
	a, e := s.actor(ctx, tx, uid)
	if e != nil {
		return e
	}
	if !isStaff(a) || !reasonOK(in.Reason) || in.Revision < 1 {
		return ErrForbidden
	}
	c, e := scanComment(tx.QueryRow(ctx, `SELECT `+commentCols+` FROM pvzce_site.comments c WHERE id=$1 FOR UPDATE`, id))
	if e != nil {
		return e
	}
	own, e := reviewerConflict(ctx, tx, c.ProjectID, uid)
	if e != nil {
		return e
	}
	if own || c.UserID == uid {
		return ErrForbidden
	}
	if c.Revision != in.Revision {
		return ErrConflict
	}
	next, e := nextContentState(c.State, in.Action)
	if e != nil || !oneOf(in.Action, "approve", "reject", "remove", "restore") {
		return ErrInvalid
	}
	category := in.Category
	if category == "" {
		category = c.ContentLabel
	}
	if !validCategory(category) || (next == "published" && !allowedCategory(category)) {
		return ErrInvalid
	}
	label := c.ContentLabel
	if allowedCategory(category) {
		label = category
	}
	if _, e := tx.Exec(ctx, `UPDATE pvzce_site.comments SET state=$2,content_label=$3,updated_at=now() WHERE id=$1`, id, next, label); e != nil {
		return e
	}
	var previous *int64
	if e := tx.QueryRow(ctx, `SELECT max(id) FROM pvzce_site.moderation_decisions WHERE comment_id=$1 AND revision=$2 AND source IN ('model','fallback')`, id, c.Revision).Scan(&previous); e != nil {
		return e
	}
	recommendation := "manual"
	if in.Action == "approve" {
		recommendation = "approve"
	}
	if in.Action == "reject" || in.Action == "remove" {
		recommendation = "reject"
	}
	if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.moderation_decisions(comment_id,revision,source,model_version,policy_version,category,recommendation,reason,confidence,actor_id,overrides_id) VALUES($1,$2,'manual','human',$3,$4,$5,$6,1,$7,$8)`, id, c.Revision, PolicyVersion, category, recommendation, in.Reason, uid, previous); e != nil {
		return e
	}
	if e := notify(ctx, tx, c.UserID, "comment:"+fmt.Sprint(id), in.Action+": "+in.Reason); e != nil {
		return e
	}
	return recordAction(ctx, tx, uid, "comment", id, c.Revision, in.ReviewInput, c.State, next, category)
}
func (s *Service) Comments(ctx context.Context, uid, pid int64, p PageInput) (Page[Comment], error) {
	out := Page[Comment]{Items: []Comment{}}
	size, e := p.limit()
	if e != nil {
		return out, e
	}
	e = s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if e := s.projectVisible(ctx, tx, uid, pid, p.IncludeSuggestive); e != nil {
			return e
		}
		if p.Mine {
			if _, e := s.identity(ctx, tx, uid); e != nil {
				return e
			}
		}
		rows, e := tx.Query(ctx, `SELECT `+commentCols+` FROM pvzce_site.comments c JOIN pvzce_site.accounts a ON a.user_id=c.user_id WHERE c.project_id=$1 AND ($2::bigint=0 OR c.id<$2) AND (($3::boolean AND c.user_id=$4) OR (NOT $3 AND c.state='published' AND a.status='active' AND (c.content_label='general' OR $5))) ORDER BY c.id DESC LIMIT $6`, pid, p.Before, p.Mine, uid, p.IncludeSuggestive, size+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			c, e := scanComment(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, c)
		}
		return rows.Err()
	})
	if len(out.Items) > size {
		out.Items = out.Items[:size]
		out.NextBefore = out.Items[size-1].ID
	}
	return out, e
}
func (s *Service) CommentHistory(ctx context.Context, uid, id int64) (json.RawMessage, error) {
	return s.CommentHistoryBefore(ctx, uid, id, 0)
}
func (s *Service) CommentHistoryBefore(ctx context.Context, uid, id, before int64) (json.RawMessage, error) {
	if before < 0 {
		return nil, ErrInvalid
	}
	var raw json.RawMessage
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.identity(ctx, tx, uid)
		if e != nil {
			return e
		}
		var owner int64
		if e := tx.QueryRow(ctx, `SELECT user_id FROM pvzce_site.comments WHERE id=$1`, id).Scan(&owner); e != nil {
			return e
		}
		if owner != uid && !isStaff(a) {
			return ErrForbidden
		}
		return tx.QueryRow(ctx, `WITH revisions AS MATERIALIZED (SELECT * FROM pvzce_site.comment_revisions WHERE comment_id=$1 AND ($2::bigint=0 OR revision<$2) ORDER BY revision DESC LIMIT 20) SELECT json_build_object('revisions',COALESCE((SELECT json_agg(r ORDER BY r.revision DESC) FROM revisions r),'[]'::json),'decisions',COALESCE((SELECT json_agg(x ORDER BY x.id DESC) FROM (SELECT d.* FROM pvzce_site.moderation_decisions d WHERE comment_id=$1 AND revision IN(SELECT revision FROM revisions) ORDER BY id DESC LIMIT 100) x),'[]'::json),'actions',COALESCE((SELECT json_agg(x ORDER BY x.id DESC) FROM (SELECT a.* FROM pvzce_site.content_actions a WHERE target_kind='comment' AND target_id=$1 AND revision IN(SELECT revision FROM revisions) ORDER BY id DESC LIMIT 100) x),'[]'::json),'next_before_revision',(SELECT CASE WHEN count(*)=20 THEN min(revision) ELSE 0 END FROM revisions))`, id, before).Scan(&raw)
	})
	return raw, e
}

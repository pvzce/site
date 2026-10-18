package workshop

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
)

type FeedbackInput struct {
	ProjectID    int64  `json:"project_id"`
	VersionID    int64  `json:"version_id"`
	GameVersion  string `json:"game_version"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Steps        string `json:"steps"`
	Expected     string `json:"expected"`
	Actual       string `json:"actual"`
	Public       bool   `json:"public"`
	ContentLabel string `json:"content_label"`
}
type Feedback struct {
	Revision   int64 `json:"revision"`
	ID         int64 `json:"id"`
	ReporterID int64 `json:"reporter_id"`
	FeedbackInput
	State        string `json:"state"`
	ContentState string `json:"content_state"`
	AssigneeID   *int64 `json:"assignee_id"`
}

const feedbackCols = `f.id,f.reporter_id,COALESCE(f.project_id,0),COALESCE(f.version_id,0),f.game_version,f.kind,f.title,f.steps,f.expected,f.actual,f.is_public,f.content_label,f.state,f.content_state,f.assignee_id,f.revision`

func scanFeedback(row pgx.Row) (Feedback, error) {
	var f Feedback
	e := row.Scan(&f.ID, &f.ReporterID, &f.ProjectID, &f.VersionID, &f.GameVersion, &f.Kind, &f.Title, &f.Steps, &f.Expected, &f.Actual, &f.Public, &f.ContentLabel, &f.State, &f.ContentState, &f.AssigneeID, &f.Revision)
	return f, e
}
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
func (s *Service) CreateFeedback(ctx context.Context, uid int64, in FeedbackInput) (Feedback, error) {
	var out Feedback
	if in.ContentLabel == "" {
		in.ContentLabel = "general"
	}
	if !oneOf(in.Kind, "bug", "suggestion") || strings.TrimSpace(in.Title) == "" || len(in.Title) > 200 || len(in.Steps) > 16000 || len(in.Expected) > 8000 || len(in.Actual) > 16000 || !allowedCategory(in.ContentLabel) || in.ProjectID < 0 || in.VersionID < 0 || (in.VersionID > 0 && in.ProjectID == 0) {
		return out, ErrInvalid
	}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		if e := limitedActivity(ctx, tx, uid, "feedback.create", 3, 30); e != nil {
			return e
		}
		if in.ProjectID > 0 {
			if e := s.projectVisible(ctx, tx, uid, in.ProjectID, true); e != nil {
				return e
			}
		}
		if in.VersionID > 0 {
			var pid int64
			if e := tx.QueryRow(ctx, `SELECT project_id FROM pvzce_site.versions WHERE id=$1 AND project_id=$2 AND EXISTS(SELECT 1 FROM pvzce_site.version_compatibility WHERE version_id=$1 AND game_version=$4) AND (state='published' OR EXISTS(SELECT 1 FROM pvzce_site.projects WHERE id=$2 AND owner_id=$3) OR EXISTS(SELECT 1 FROM pvzce_site.collaborators WHERE project_id=$2 AND user_id=$3))`, in.VersionID, in.ProjectID, uid, in.GameVersion).Scan(&pid); e != nil {
				return e
			}
		}
		var e error
		out, e = scanFeedback(tx.QueryRow(ctx, `INSERT INTO pvzce_site.feedback AS f(reporter_id,project_id,version_id,game_version,kind,title,steps,expected,actual,is_public,content_label) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+feedbackCols, uid, nullableID(in.ProjectID), nullableID(in.VersionID), in.GameVersion, in.Kind, in.Title, in.Steps, in.Expected, in.Actual, in.Public, in.ContentLabel))
		if e != nil {
			return conflict(e)
		}
		if e := feedbackEvent(ctx, tx, uid, out.ID, "new", "new", "Submitted; redact tokens and personal information before attaching logs", false); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "feedback.create", fmt.Sprint(out.ID), "pending; private unless explicitly requested public")
	})
	return out, e
}
func feedbackEvent(ctx context.Context, tx pgx.Tx, uid, id int64, from, to, reply string, staff bool) error {
	_, e := tx.Exec(ctx, `INSERT INTO pvzce_site.feedback_events(feedback_id,actor_id,from_state,to_state,reply,staff) VALUES($1,$2,$3,$4,$5,$6)`, id, uid, from, to, reply, staff)
	return e
}
func (s *Service) feedbackAccess(ctx context.Context, tx pgx.Tx, uid int64, f Feedback, include bool) error {
	if uid > 0 {
		a, e := s.identity(ctx, tx, uid)
		if e != nil {
			return e
		}
		if uid == f.ReporterID || isStaff(a) {
			return nil
		}
	}
	if !f.Public || f.ContentState != "published" || (f.ContentLabel != "general" && !include) {
		return ErrNotFound
	}
	if f.ProjectID > 0 {
		var ok bool
		if e := tx.QueryRow(ctx, `SELECT state='published' AND (content_label='general' OR $2) FROM pvzce_site.projects WHERE id=$1`, f.ProjectID, include).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			return ErrNotFound
		}
	}
	return nil
}
func (s *Service) FeedbackDetail(ctx context.Context, uid, id int64, include bool) (any, error) {
	var out any
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		f, e := scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackCols+` FROM pvzce_site.feedback f WHERE id=$1`, id))
		if e != nil {
			return e
		}
		if e = s.feedbackAccess(ctx, tx, uid, f, include); e != nil {
			return e
		}
		privileged := uid == f.ReporterID
		if uid > 0 {
			a, e := s.identity(ctx, tx, uid)
			if e != nil {
				return e
			}
			privileged = privileged || isStaff(a)
		}
		var events, files, actions json.RawMessage
		// Unreviewed user supplements are visible only to reporter/staff. Public events appear only after a fresh content approval.
		if e := tx.QueryRow(ctx, `SELECT COALESCE(json_agg(e ORDER BY e.id),'[]'::json) FROM pvzce_site.feedback_events e WHERE feedback_id=$1`, id).Scan(&events); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x ORDER BY x.id),'[]'::json) FROM (SELECT id,filename,size,sha256,scan_state,validation_state,is_private,content_state,content_label,safety_quarantined FROM pvzce_site.files WHERE feedback_id=$1 AND ($2 OR (NOT is_private AND content_state='published' AND NOT safety_quarantined AND scan_state='clean' AND validation_state='valid' AND (content_label='general' OR $3)))) x`, id, privileged, include).Scan(&files); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT COALESCE(json_agg(a ORDER BY a.id DESC),'[]'::json) FROM pvzce_site.content_actions a WHERE ((target_kind='feedback' AND target_id=$1) OR (target_kind='file' AND target_id IN(SELECT id FROM pvzce_site.files WHERE feedback_id=$1))) AND $2`, id, privileged).Scan(&actions); e != nil {
			return e
		}
		out = map[string]any{"feedback": f, "events": events, "files": files, "actions": actions}
		return nil
	})
	return out, e
}
func (s *Service) FeedbackList(ctx context.Context, uid int64, p PageInput) (json.RawMessage, error) {
	var raw json.RawMessage
	size, e := p.limit()
	if e != nil {
		return raw, e
	}
	e = s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		staff := false
		if uid > 0 {
			a, e := s.identity(ctx, tx, uid)
			if e != nil {
				return e
			}
			staff = isStaff(a)
		}
		if p.Mine && uid == 0 {
			return ErrForbidden
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x ORDER BY x.id DESC),'[]'::json) FROM (SELECT f.* FROM pvzce_site.feedback f LEFT JOIN pvzce_site.projects p ON p.id=f.project_id WHERE ($1::bigint=0 OR f.id<$1) AND (($2 AND f.reporter_id=$3) OR (NOT $2 AND ($4 OR (f.is_public AND f.content_state='published' AND (f.content_label='general' OR $5) AND (f.project_id IS NULL OR (p.state='published' AND (p.content_label='general' OR $5))))))) ORDER BY f.id DESC LIMIT $6) x`, p.Before, p.Mine, uid, staff, p.IncludeSuggestive, size).Scan(&raw)
	})
	return raw, e
}
func (s *Service) SupplementFeedback(ctx context.Context, uid, id int64, reply string) error {
	if !reasonOK(reply) {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		f, e := scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackCols+` FROM pvzce_site.feedback f WHERE id=$1 FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if uid != f.ReporterID {
			return ErrForbidden
		}
		if f.State == "closed" || f.ContentState == "removed" {
			return ErrInvalid
		}
		if e := limitedActivity(ctx, tx, uid, "feedback.supplement", 5, 50); e != nil {
			return e
		}
		next := f.State
		if next == "needs_info" {
			next = "new"
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.feedback SET state=$2,content_state='pending',revision=revision+1,updated_at=now() WHERE id=$1`, id, next); e != nil {
			return e
		}
		if e := feedbackEvent(ctx, tx, uid, id, f.State, next, reply, false); e != nil {
			return e
		}
		if f.AssigneeID != nil {
			if e := notify(ctx, tx, *f.AssigneeID, "feedback:"+fmt.Sprint(id), "Reporter supplied information"); e != nil {
				return e
			}
		}
		return audit(ctx, tx, uid, "feedback.supplement", fmt.Sprint(id), "content approval invalidated")
	})
}
func (s *Service) feedbackConflict(ctx context.Context, tx pgx.Tx, uid int64, f Feedback) error {
	if uid == f.ReporterID {
		return ErrForbidden
	}
	if f.ProjectID > 0 {
		own, e := reviewerConflict(ctx, tx, f.ProjectID, uid)
		if e != nil {
			return e
		}
		if own {
			return ErrForbidden
		}
	}
	return nil
}

type ProcessInput struct {
	State string `json:"state"`
	Reply string `json:"reply"`
}

func (s *Service) ProcessFeedback(ctx context.Context, uid, id int64, in ProcessInput) error {
	if !reasonOK(in.Reply) || !oneOf(in.State, "new", "needs_info", "confirmed", "in_progress", "resolved", "closed") {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if !isStaff(a) {
			return ErrForbidden
		}
		f, e := scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackCols+` FROM pvzce_site.feedback f WHERE id=$1 FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if e := s.feedbackConflict(ctx, tx, uid, f); e != nil {
			return e
		}
		if f.AssigneeID != nil && *f.AssigneeID != uid && a.Role != "admin" {
			return ErrForbidden
		}
		allowed := map[string][]string{"new": {"needs_info", "confirmed", "closed"}, "needs_info": {"new", "confirmed", "closed"}, "confirmed": {"in_progress", "needs_info", "closed"}, "in_progress": {"resolved", "needs_info", "closed"}, "resolved": {"in_progress", "closed"}, "closed": {"new"}}
		if !oneOf(in.State, allowed[f.State]...) {
			return ErrInvalid
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.feedback SET state=$2,assignee_id=$3,updated_at=now() WHERE id=$1`, id, in.State, uid); e != nil {
			return e
		}
		if e := feedbackEvent(ctx, tx, uid, id, f.State, in.State, in.Reply, true); e != nil {
			return e
		}
		if e := notify(ctx, tx, f.ReporterID, "feedback:"+fmt.Sprint(id), in.State+": "+in.Reply); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "feedback.process", fmt.Sprint(id), in.Reply)
	})
}
func (s *Service) ReviewFeedback(ctx context.Context, uid, id int64, in ContentReview) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if !isStaff(a) || !reasonOK(in.Reason) || !oneOf(in.Action, "approve", "reject", "remove", "restore") {
			return ErrForbidden
		}
		f, e := scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackCols+` FROM pvzce_site.feedback f WHERE id=$1 FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if e := s.feedbackConflict(ctx, tx, uid, f); e != nil {
			return e
		}
		if f.Revision != in.Revision {
			return ErrConflict
		}
		next, e := nextContentState(f.ContentState, in.Action)
		if e != nil {
			return e
		}
		cat := in.Category
		if cat == "" {
			cat = f.ContentLabel
		}
		if !validCategory(cat) || (next == "published" && !allowedCategory(cat)) {
			return ErrInvalid
		}
		label := f.ContentLabel
		if allowedCategory(cat) {
			label = cat
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.feedback SET content_state=$2,content_label=$3,updated_at=now() WHERE id=$1`, id, next, label); e != nil {
			return e
		}
		if e := notify(ctx, tx, f.ReporterID, "feedback:"+fmt.Sprint(id), in.Action+": "+in.Reason); e != nil {
			return e
		}
		return recordAction(ctx, tx, uid, "feedback", id, f.Revision, in.ReviewInput, f.ContentState, next, cat)
	})
}
func (s *Service) authorizeFeedbackUpload(ctx context.Context, tx pgx.Tx, uid, id int64) error {
	var owner int64
	var state, content string
	if e := tx.QueryRow(ctx, `SELECT reporter_id,state,content_state FROM pvzce_site.feedback WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &state, &content); e != nil {
		return e
	}
	if uid != owner {
		return ErrForbidden
	}
	if state == "closed" || content == "removed" {
		return ErrInvalid
	}
	return nil
}

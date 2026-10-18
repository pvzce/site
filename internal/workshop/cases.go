package workshop

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
)

type ReportInput struct {
	Kind     string  `json:"target_kind"`
	TargetID int64   `json:"target_id"`
	Reason   string  `json:"reason"`
	Evidence string  `json:"evidence"`
	FileIDs  []int64 `json:"file_ids"`
}
type Case struct {
	ID         int64  `json:"id"`
	ReporterID int64  `json:"reporter_id"`
	Kind       string `json:"target_kind"`
	TargetID   int64  `json:"target_id"`
	Reason     string `json:"reason"`
	Evidence   string `json:"evidence"`
	State      string `json:"state"`
	AssigneeID *int64 `json:"assignee_id"`
	Conclusion string `json:"conclusion"`
	Action     string `json:"action"`
}

const caseCols = `c.id,c.reporter_id,c.target_kind,c.target_id,c.reason,c.evidence,c.state,c.assignee_id,c.conclusion,c.action`

func scanCase(row pgx.Row) (Case, error) {
	var c Case
	e := row.Scan(&c.ID, &c.ReporterID, &c.Kind, &c.TargetID, &c.Reason, &c.Evidence, &c.State, &c.AssigneeID, &c.Conclusion, &c.Action)
	return c, e
}
func targetOwner(ctx context.Context, tx pgx.Tx, kind string, id int64) (owner, pid, vid int64, e error) {
	switch kind {
	case "project":
		pid = id
		e = tx.QueryRow(ctx, `SELECT owner_id FROM pvzce_site.projects WHERE id=$1`, id).Scan(&owner)
	case "version":
		vid = id
		e = tx.QueryRow(ctx, `SELECT creator_id,project_id FROM pvzce_site.versions WHERE id=$1`, id).Scan(&owner, &pid)
	case "file":
		e = tx.QueryRow(ctx, `SELECT f.uploader_id,COALESCE(f.project_id,b.project_id,0) FROM pvzce_site.files f LEFT JOIN pvzce_site.feedback b ON b.id=f.feedback_id WHERE f.id=$1`, id).Scan(&owner, &pid)
	case "comment":
		e = tx.QueryRow(ctx, `SELECT user_id,project_id FROM pvzce_site.comments WHERE id=$1`, id).Scan(&owner, &pid)
	case "account":
		e = tx.QueryRow(ctx, `SELECT user_id FROM pvzce_site.accounts WHERE user_id=$1`, id).Scan(&owner)
	default:
		e = ErrInvalid
	}
	return
}
func (s *Service) Report(ctx context.Context, uid int64, in ReportInput) (int64, error) {
	if in.TargetID < 1 || !reasonOK(in.Reason) || len(in.Evidence) > 8000 || len(in.FileIDs) > 10 {
		return 0, ErrInvalid
	}
	var id int64
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if e := limitedActivity(ctx, tx, uid, "case.create", 3, 30); e != nil {
			return e
		}
		owner, pid, _, e := targetOwner(ctx, tx, in.Kind, in.TargetID)
		if e != nil {
			return e
		}
		if owner != uid && !isStaff(a) {
			switch in.Kind {
			case "file":
				if _, _, e := s.fileAccess(ctx, tx, uid, in.TargetID, true, false); e != nil {
					return e
				}
			case "comment":
				var ok bool
				if e := tx.QueryRow(ctx, `SELECT state='published' FROM pvzce_site.comments WHERE id=$1`, in.TargetID).Scan(&ok); e != nil {
					return e
				}
				if !ok {
					return ErrNotFound
				}
				if e := s.projectVisible(ctx, tx, uid, pid, true); e != nil {
					return e
				}
			case "version":
				var ok bool
				if e := tx.QueryRow(ctx, `SELECT state='published' FROM pvzce_site.versions WHERE id=$1`, in.TargetID).Scan(&ok); e != nil {
					return e
				}
				if !ok {
					return ErrNotFound
				}
				fallthrough
			case "project":
				if e := s.projectVisible(ctx, tx, uid, pid, true); e != nil {
					return e
				}
			}
		}
		if e := tx.QueryRow(ctx, `INSERT INTO pvzce_site.cases(reporter_id,target_kind,target_id,reason,evidence) VALUES($1,$2,$3,$4,$5) RETURNING id`, uid, in.Kind, in.TargetID, in.Reason, in.Evidence).Scan(&id); e != nil {
			return e
		}
		for _, fid := range in.FileIDs {
			var owned bool
			if e := tx.QueryRow(ctx, `SELECT uploader_id=$2 AND purpose='attachment' AND scan_state='clean' AND validation_state='valid' AND NOT safety_quarantined FROM pvzce_site.files WHERE id=$1`, fid, uid).Scan(&owned); e != nil {
				return e
			}
			if !owned {
				return ErrForbidden
			}
			if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.case_evidence(case_id,file_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, fid); e != nil {
				return e
			}
		}
		return caseEvent(ctx, tx, uid, id, "create", in.Reason)
	})
	return id, e
}
func caseEvent(ctx context.Context, tx pgx.Tx, uid, id int64, action, reason string) error {
	if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.case_events(case_id,actor_id,action,reason) VALUES($1,$2,$3,$4)`, id, uid, action, reason); e != nil {
		return e
	}
	return audit(ctx, tx, uid, "case."+action, fmt.Sprint(id), reason)
}
func caseConflict(ctx context.Context, tx pgx.Tx, uid int64, c Case) error {
	owner, pid, _, e := targetOwner(ctx, tx, c.Kind, c.TargetID)
	if e != nil {
		return e
	}
	if c.ReporterID == uid || owner == uid {
		return ErrForbidden
	}
	if pid > 0 {
		own, e := reviewerConflict(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		if own {
			return ErrForbidden
		}
	}
	return nil
}
func (s *Service) Cases(ctx context.Context, uid int64, p PageInput) (json.RawMessage, error) {
	var raw json.RawMessage
	size, e := p.limit()
	if e != nil {
		return raw, e
	}
	e = s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.identity(ctx, tx, uid)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x ORDER BY x.id DESC),'[]'::json) FROM (SELECT c.id,c.target_kind,c.target_id,c.state,c.conclusion,c.action,c.assignee_id,c.created_at FROM pvzce_site.cases c WHERE ($1::bigint=0 OR c.id<$1) AND (c.reporter_id=$2 OR $3 OR (c.target_kind='account' AND c.target_id=$2) OR (c.target_kind='project' AND EXISTS(SELECT 1 FROM pvzce_site.projects WHERE id=c.target_id AND owner_id=$2)) OR (c.target_kind='version' AND EXISTS(SELECT 1 FROM pvzce_site.versions WHERE id=c.target_id AND creator_id=$2)) OR (c.target_kind='file' AND EXISTS(SELECT 1 FROM pvzce_site.files WHERE id=c.target_id AND uploader_id=$2)) OR (c.target_kind='comment' AND EXISTS(SELECT 1 FROM pvzce_site.comments WHERE id=c.target_id AND user_id=$2))) ORDER BY c.id DESC LIMIT $4) x`, p.Before, uid, isStaff(a) && !p.Mine, size).Scan(&raw)
	})
	return raw, e
}
func (s *Service) CaseDetail(ctx context.Context, uid, id int64) (any, error) {
	var out any
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.identity(ctx, tx, uid)
		if e != nil {
			return e
		}
		c, e := scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+` FROM pvzce_site.cases c WHERE id=$1`, id))
		if e != nil {
			return e
		}
		owner, _, _, e := targetOwner(ctx, tx, c.Kind, c.TargetID)
		if e != nil {
			return e
		}
		if uid != c.ReporterID && uid != owner && !isStaff(a) {
			return ErrNotFound
		}
		privileged := uid == c.ReporterID || isStaff(a)
		if !privileged {
			c.Reason = ""
			c.Evidence = ""
			c.ReporterID = 0
		}
		var events, appeals, files json.RawMessage
		if e := tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x ORDER BY x.id),'[]'::json) FROM (SELECT id,action,reason,created_at FROM pvzce_site.case_events WHERE case_id=$1 AND ($2 OR action<>'create')) x`, id, privileged).Scan(&events); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x ORDER BY x.id),'[]'::json) FROM (SELECT * FROM pvzce_site.case_appeals WHERE case_id=$1 AND (user_id=$2 OR $3)) x`, id, uid, isStaff(a)).Scan(&appeals); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT COALESCE(json_agg(file_id),'[]'::json) FROM pvzce_site.case_evidence WHERE case_id=$1 AND $2`, id, privileged).Scan(&files); e != nil {
			return e
		}
		out = map[string]any{"case": c, "events": events, "appeals": appeals, "evidence_file_ids": files}
		return nil
	})
	return out, e
}
func (s *Service) ClaimCase(ctx context.Context, uid, id int64) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if !isStaff(a) {
			return ErrForbidden
		}
		c, e := scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+` FROM pvzce_site.cases c WHERE id=$1 FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if e := caseConflict(ctx, tx, uid, c); e != nil {
			return e
		}
		if !oneOf(c.State, "open", "in_review") {
			return ErrInvalid
		}
		if c.AssigneeID != nil && *c.AssigneeID != uid && a.Role != "admin" {
			return ErrForbidden
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.cases SET state='in_review',assignee_id=$2,updated_at=now() WHERE id=$1`, id, uid); e != nil {
			return e
		}
		return caseEvent(ctx, tx, uid, id, "claim", "Assigned to independent staff member")
	})
}

type ResolveCaseInput struct {
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	Category string `json:"category"`
}

func (s *Service) ResolveCase(ctx context.Context, uid, id int64, in ResolveCaseInput) error {
	if !reasonOK(in.Reason) {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(706879122532)`); e != nil {
			return e
		}
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if !isStaff(a) {
			return ErrForbidden
		}
		c, e := scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+` FROM pvzce_site.cases c WHERE id=$1 FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if e := caseConflict(ctx, tx, uid, c); e != nil {
			return e
		}
		if c.State != "in_review" || c.AssigneeID == nil || *c.AssigneeID != uid {
			return ErrForbidden
		}
		owner, pid, vid, e := targetOwner(ctx, tx, c.Kind, c.TargetID)
		if e != nil {
			return e
		}
		review := ReviewInput{Action: in.Action, Reason: in.Reason, Category: in.Category}
		state := "resolved"
		if in.Action == "dismiss" {
			state = "rejected"
		} else {
			switch c.Kind {
			case "project", "version":
				if !oneOf(in.Action, "remove", "restore") {
					return ErrInvalid
				}
				e = s.transitionTx(ctx, tx, uid, pid, vid, review)
			case "comment":
				if !oneOf(in.Action, "remove", "restore") {
					return ErrInvalid
				}
				var revision int64
				if e = tx.QueryRow(ctx, `SELECT revision FROM pvzce_site.comments WHERE id=$1`, c.TargetID).Scan(&revision); e == nil {
					e = s.reviewCommentTx(ctx, tx, uid, c.TargetID, ContentReview{ReviewInput: review, Revision: revision})
				}
			case "file":
				if !oneOf(in.Action, "remove", "restore", "quarantine") {
					return ErrInvalid
				}
				e = s.reviewFileTx(ctx, tx, uid, c.TargetID, review)
			case "account":
				if a.Role != "admin" {
					return ErrForbidden
				}
				var role string
				if e = tx.QueryRow(ctx, `SELECT role FROM pvzce_site.accounts WHERE user_id=$1`, owner).Scan(&role); e != nil {
					return e
				}
				status := ""
				switch in.Action {
				case "restrict":
					status = "restricted"
				case "activate":
					status = "active"
				default:
					return ErrInvalid
				}
				e = s.manageAccountTx(ctx, tx, uid, owner, role, status, in.Reason)
			}
			if e != nil {
				return e
			}
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.cases SET state=$2,conclusion=$3,action=$4,updated_at=now() WHERE id=$1`, id, state, in.Reason, in.Action); e != nil {
			return e
		}
		if e := notify(ctx, tx, c.ReporterID, "case:"+fmt.Sprint(id), in.Action+": "+in.Reason); e != nil {
			return e
		}
		if e := notify(ctx, tx, owner, "case:"+fmt.Sprint(id), in.Action+": "+in.Reason); e != nil {
			return e
		}
		return caseEvent(ctx, tx, uid, id, in.Action, in.Reason)
	})
}
func (s *Service) AppealCase(ctx context.Context, uid, id int64, reason string) (int64, error) {
	var appeal int64
	if !reasonOK(reason) {
		return 0, ErrInvalid
	}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.identity(ctx, tx, uid); e != nil {
			return e
		}
		c, e := scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+` FROM pvzce_site.cases c WHERE id=$1 FOR SHARE`, id))
		if e != nil {
			return e
		}
		owner, _, _, e := targetOwner(ctx, tx, c.Kind, c.TargetID)
		if e != nil {
			return e
		}
		if uid != owner && uid != c.ReporterID {
			return ErrForbidden
		}
		if !oneOf(c.State, "resolved", "rejected") {
			return ErrInvalid
		}
		if e := limitedActivity(ctx, tx, uid, "case.appeal", 3, 10); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `INSERT INTO pvzce_site.case_appeals(case_id,user_id,reason) VALUES($1,$2,$3) RETURNING id`, id, uid, reason).Scan(&appeal); e != nil {
			return conflict(e)
		}
		return caseEvent(ctx, tx, uid, id, "appeal", reason)
	})
	return appeal, e
}
func (s *Service) ResolveCaseAppeal(ctx context.Context, uid, id int64, outcome, reply string) error {
	if !oneOf(outcome, "uphold", "reopen") || !reasonOK(reply) {
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
		var cid, user int64
		if e := tx.QueryRow(ctx, `SELECT case_id,user_id FROM pvzce_site.case_appeals WHERE id=$1 AND state='pending' FOR UPDATE`, id).Scan(&cid, &user); e != nil {
			return e
		}
		c, e := scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+` FROM pvzce_site.cases c WHERE id=$1 FOR UPDATE`, cid))
		if e != nil {
			return e
		}
		if e := caseConflict(ctx, tx, uid, c); e != nil {
			return e
		}
		if uid == user || (c.AssigneeID != nil && *c.AssigneeID == uid) {
			return ErrForbidden
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.case_appeals SET state='resolved',reply=$2,reviewer_id=$3,outcome=$4,resolved_at=now() WHERE id=$1`, id, reply, uid, outcome); e != nil {
			return e
		}
		if outcome == "reopen" {
			if _, e := tx.Exec(ctx, `UPDATE pvzce_site.cases SET state='open',assignee_id=NULL,updated_at=now() WHERE id=$1`, cid); e != nil {
				return e
			}
		}
		if e := notify(ctx, tx, user, "case:"+fmt.Sprint(cid), outcome+": "+reply); e != nil {
			return e
		}
		return caseEvent(ctx, tx, uid, cid, "appeal."+outcome, reply)
	})
}

package workshop

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
)

func (s *Service) CommunityQueue(ctx context.Context, uid int64, p PageInput) (json.RawMessage, error) {
	var raw json.RawMessage
	size, e := p.limit()
	if e != nil {
		return raw, e
	}
	e = s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if !isStaff(a) {
			return ErrForbidden
		}
		return tx.QueryRow(ctx, `SELECT json_build_object('comments',(SELECT COALESCE(json_agg(c),'[]'::json) FROM (SELECT c.*, (SELECT row_to_json(d) FROM pvzce_site.moderation_decisions d WHERE d.comment_id=c.id AND d.revision=c.revision ORDER BY d.id DESC LIMIT 1) AS advice FROM pvzce_site.comments c WHERE state='pending' AND ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT $2) c),'feedback',(SELECT COALESCE(json_agg(f),'[]'::json) FROM (SELECT id,reporter_id,project_id,title,state,content_state,revision,assignee_id FROM pvzce_site.feedback WHERE (content_state='pending' OR state NOT IN ('resolved','closed')) AND ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT $2) f),'files',(SELECT COALESCE(json_agg(f),'[]'::json) FROM (SELECT id,project_id,feedback_id,filename,scan_state,validation_state,content_state,content_label,safety_quarantined FROM pvzce_site.files WHERE purpose='attachment' AND content_state='pending' AND ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT $2) f),'cases',(SELECT COALESCE(json_agg(c),'[]'::json) FROM (SELECT id,target_kind,target_id,state,assignee_id FROM pvzce_site.cases WHERE state IN ('open','in_review') AND ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT $2) c),'case_appeals',(SELECT COALESCE(json_agg(c),'[]'::json) FROM (SELECT * FROM pvzce_site.case_appeals WHERE state='pending' AND ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT $2) c),'content_appeals',(SELECT COALESCE(json_agg(c),'[]'::json) FROM (SELECT * FROM pvzce_site.content_appeals WHERE state='pending' AND ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT $2) c),'moderation_configured',$3::boolean,'policy_version',$4::text)`, p.Before, size, s.Moderator != nil, PolicyVersion).Scan(&raw)
	})
	return raw, e
}
func (s *Service) Accounts(ctx context.Context, uid int64, p PageInput) (json.RawMessage, error) {
	var raw json.RawMessage
	size, e := p.limit()
	if e != nil {
		return raw, e
	}
	e = s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if a.Role != "admin" {
			return ErrForbidden
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x ORDER BY x.user_id DESC),'[]'::json) FROM (SELECT user_id,display_name,status,role,updated_at FROM pvzce_site.accounts WHERE ($1::bigint=0 OR user_id<$1) ORDER BY user_id DESC LIMIT $2) x`, p.Before, size).Scan(&raw)
	})
	return raw, e
}
func (s *Service) AuditPage(ctx context.Context, uid int64, p PageInput, action string) (json.RawMessage, error) {
	var raw json.RawMessage
	size, e := p.limit()
	if e != nil || len(action) > 100 {
		return raw, ErrInvalid
	}
	e = s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if a.Role != "admin" {
			return ErrForbidden
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x ORDER BY x.id DESC),'[]'::json) FROM (SELECT * FROM pvzce_site.audit WHERE ($1::bigint=0 OR id<$1) AND ($2='' OR action=$2) ORDER BY id DESC LIMIT $3) x`, p.Before, action, size).Scan(&raw)
	})
	return raw, e
}
func (s *Service) Notifications(ctx context.Context, uid int64, p PageInput) (json.RawMessage, error) {
	var raw json.RawMessage
	size, e := p.limit()
	if e != nil {
		return raw, e
	}
	e = s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.identity(ctx, tx, uid); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x ORDER BY x.id DESC),'[]'::json) FROM (SELECT id,resource,message,read_at,created_at FROM pvzce_site.notifications WHERE user_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3) x`, uid, p.Before, size).Scan(&raw)
	})
	return raw, e
}
func (s *Service) ReadNotification(ctx context.Context, uid, id int64) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.identity(ctx, tx, uid); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE pvzce_site.notifications SET read_at=COALESCE(read_at,now()) WHERE id=$1 AND user_id=$2`, id, uid)
		if e == nil && tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return e
	})
}

// Statistics always scope resources in SQL before aggregating. Only administrators may request all-site totals.
func (s *Service) Statistics(ctx context.Context, uid, pid int64, all bool) (json.RawMessage, error) {
	var raw json.RawMessage
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if all && a.Role != "admin" {
			return ErrForbidden
		}
		if pid < 0 {
			return ErrInvalid
		}
		if pid > 0 && !all {
			m, e := member(ctx, tx, pid, uid)
			if e != nil {
				return e
			}
			if !m {
				return ErrForbidden
			}
		}
		return tx.QueryRow(ctx, `WITH scope AS MATERIALIZED (SELECT id,downloads,rating_count,rating_sum,state FROM pvzce_site.projects p WHERE ($1::bigint=0 OR p.id=$1) AND ($2 OR p.owner_id=$3 OR EXISTS(SELECT 1 FROM pvzce_site.collaborators c WHERE c.project_id=p.id AND c.user_id=$3))),feedback_scope AS MATERIALIZED (SELECT state,content_state FROM pvzce_site.feedback f WHERE ($1::bigint=0 OR f.project_id=$1) AND ($2 OR f.reporter_id=$3 OR (f.is_public AND f.content_state='published' AND f.project_id IN(SELECT id FROM scope)))) SELECT json_build_object('projects',(SELECT count(*) FROM scope),'downloads',(SELECT COALESCE(sum(downloads),0) FROM scope),'rating_count',(SELECT COALESCE(sum(rating_count),0) FROM scope),'rating_sum',(SELECT COALESCE(sum(rating_sum),0) FROM scope),'rating_distribution',(SELECT COALESCE(json_object_agg(score,n),'{}'::json) FROM (SELECT score,count(*) n FROM pvzce_site.ratings r JOIN pvzce_site.accounts a ON a.user_id=r.user_id WHERE project_id IN(SELECT id FROM scope) AND NOT voided AND a.status='active' GROUP BY score) r),'project_states',(SELECT COALESCE(json_object_agg(state,n),'{}'::json) FROM (SELECT state,count(*) n FROM scope GROUP BY state) x),'versions',(SELECT COALESCE(json_agg(x),'[]'::json) FROM (SELECT id,project_id,number,downloads FROM pvzce_site.versions WHERE project_id IN(SELECT id FROM scope) ORDER BY id DESC LIMIT 100) x),'comment_states',(SELECT COALESCE(json_object_agg(state,n),'{}'::json) FROM (SELECT state,count(*) n FROM pvzce_site.comments WHERE project_id IN(SELECT id FROM scope) GROUP BY state) x),'feedback_states',(SELECT COALESCE(json_object_agg(state,n),'{}'::json) FROM (SELECT state,count(*) n FROM feedback_scope GROUP BY state) x),'upload_bytes_received',(SELECT COALESCE(sum(received),0) FROM pvzce_site.uploads WHERE $2 OR user_id=$3),'upload_bytes_reserved',(SELECT COALESCE(sum(reserved),0) FROM pvzce_site.uploads WHERE state='receiving' AND ($2 OR user_id=$3)),'pending_versions',(SELECT count(*) FROM pvzce_site.versions WHERE state='pending' AND project_id IN(SELECT id FROM scope)),'pending_files',(SELECT count(*) FROM pvzce_site.files WHERE content_state='pending' AND ($2 OR project_id IN(SELECT id FROM scope) OR uploader_id=$3)),'pending_content_appeals',(SELECT count(*) FROM pvzce_site.content_appeals WHERE state='pending' AND ($2 OR user_id=$3)),'pending_project_appeals',(SELECT count(*) FROM pvzce_site.appeals WHERE state='pending' AND ($2 OR user_id=$3)),'pending_comments',(SELECT count(*) FROM pvzce_site.comments WHERE state='pending' AND project_id IN(SELECT id FROM scope)),'pending_feedback',(SELECT count(*) FROM feedback_scope WHERE content_state='pending'),'open_cases',(SELECT count(*) FROM pvzce_site.cases WHERE state IN ('open','in_review') AND ($2 OR reporter_id=$3)),'pending_appeals',(SELECT count(*) FROM pvzce_site.case_appeals WHERE state='pending' AND ($2 OR user_id=$3)))`, pid, all, uid).Scan(&raw)
	})
	return raw, e
}
func (s *Service) VoidRating(ctx context.Context, uid, pid, user int64, reason string) error {
	if !reasonOK(reason) {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if a.Role != "admin" || uid == user {
			return ErrForbidden
		}
		if own, e := reviewerConflict(ctx, tx, pid, uid); e != nil {
			return e
		} else if own {
			return ErrForbidden
		}
		var lock int64
		if e := tx.QueryRow(ctx, `SELECT id FROM pvzce_site.projects WHERE id=$1 FOR UPDATE`, pid).Scan(&lock); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE pvzce_site.ratings SET voided=true,updated_at=now() WHERE project_id=$1 AND user_id=$2`, pid, user)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		if e := refreshRating(ctx, tx, pid); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "rating.void", fmt.Sprintf("%d/%d", pid, user), reason)
	})
}
func (s *Service) AppealContent(ctx context.Context, uid, action int64, reason string) (int64, error) {
	var id int64
	if !reasonOK(reason) {
		return 0, ErrInvalid
	}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.identity(ctx, tx, uid); e != nil {
			return e
		}
		var kind string
		var target int64
		if e := tx.QueryRow(ctx, `SELECT target_kind,target_id FROM pvzce_site.content_actions WHERE id=$1 AND action IN ('reject','remove','quarantine')`, action).Scan(&kind, &target); e != nil {
			return e
		}
		owner := int64(0)
		if kind == "feedback" {
			if e := tx.QueryRow(ctx, `SELECT reporter_id FROM pvzce_site.feedback WHERE id=$1`, target).Scan(&owner); e != nil {
				return e
			}
		} else {
			var e error
			owner, _, _, e = targetOwner(ctx, tx, kind, target)
			if e != nil {
				return e
			}
		}
		if uid != owner {
			return ErrForbidden
		}
		if e := limitedActivity(ctx, tx, uid, "content.appeal", 3, 10); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `INSERT INTO pvzce_site.content_appeals(action_id,user_id,reason) VALUES($1,$2,$3) RETURNING id`, action, uid, reason).Scan(&id); e != nil {
			return conflict(e)
		}
		return audit(ctx, tx, uid, "content.appeal", fmt.Sprint(id), reason)
	})
	return id, e
}
func (s *Service) ContentAppeals(ctx context.Context, uid int64, p PageInput) (json.RawMessage, error) {
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
		return tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x ORDER BY x.id DESC),'[]'::json) FROM (SELECT c.*,a.target_kind,a.target_id,a.action,a.reason AS moderation_reason FROM pvzce_site.content_appeals c JOIN pvzce_site.content_actions a ON a.id=c.action_id WHERE (c.user_id=$1 OR $2) AND ($3::bigint=0 OR c.id<$3) ORDER BY c.id DESC LIMIT $4) x`, uid, isStaff(a), p.Before, size).Scan(&raw)
	})
	return raw, e
}
func (s *Service) ResolveContentAppeal(ctx context.Context, uid, id int64, reply string) error {
	if !reasonOK(reply) {
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
		var owner, original, target int64
		var kind string
		if e := tx.QueryRow(ctx, `SELECT c.user_id,a.actor_id,a.target_id,a.target_kind FROM pvzce_site.content_appeals c JOIN pvzce_site.content_actions a ON a.id=c.action_id WHERE c.id=$1 AND c.state='pending' FOR UPDATE OF c`, id).Scan(&owner, &original, &target, &kind); e != nil {
			return e
		}
		if uid == owner || uid == original {
			return ErrForbidden
		}
		if kind == "feedback" {
			f, e := scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackCols+` FROM pvzce_site.feedback f WHERE id=$1`, target))
			if e != nil {
				return e
			}
			if e := s.feedbackConflict(ctx, tx, uid, f); e != nil {
				return e
			}
		} else if kind == "file" {
			if e := s.fileConflict(ctx, tx, uid, target); e != nil {
				return e
			}
		} else {
			_, pid, _, e := targetOwner(ctx, tx, kind, target)
			if e != nil {
				return e
			}
			conflict, e := reviewerConflict(ctx, tx, pid, uid)
			if e != nil {
				return e
			}
			if conflict {
				return ErrForbidden
			}
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.content_appeals SET state='resolved',reply=$2,reviewer_id=$3,resolved_at=now() WHERE id=$1`, id, reply, uid); e != nil {
			return e
		}
		if e := notify(ctx, tx, owner, "content_appeal:"+fmt.Sprint(id), reply); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "content.appeal_resolve", fmt.Sprint(id), reply+"; restoration requires normal independent review")
	})
}

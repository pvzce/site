package workshop

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// File access resolves scope from persisted records; project membership never grants private feedback access.
func (s *Service) fileAccess(ctx context.Context, tx pgx.Tx, uid, id int64, include, inspect bool) (File, string, error) {
	var f File
	var key string
	var feedbackID *int64
	var private, safety bool
	var state, label string
	if e := scanFile(tx.QueryRow(ctx, `SELECT `+fileCols+` FROM pvzce_site.files f WHERE id=$1`, id), &f); e != nil {
		return f, key, e
	}
	if e := tx.QueryRow(ctx, `SELECT object_key,feedback_id,is_private,safety_quarantined,content_state,content_label FROM pvzce_site.files WHERE id=$1`, id).Scan(&key, &feedbackID, &private, &safety, &state, &label); e != nil {
		return f, key, e
	}
	if f.ScanState != "clean" || f.ValidationState != "valid" || safety {
		return f, "", ErrNotFound
	}
	var a Actor
	var e error
	if uid > 0 {
		a, e = s.identity(ctx, tx, uid)
		if e != nil {
			return f, "", e
		}
	}
	if inspect {
		if !isStaff(a) {
			return f, "", ErrForbidden
		}
		if e := s.fileConflict(ctx, tx, uid, id); e != nil {
			return f, "", e
		}
		if e := audit(ctx, tx, uid, "file.inspect", fmt.Sprint(id), "authorized private review inspection"); e != nil {
			return f, "", e
		}
		return f, key, nil
	}
	if oneOf(state, "removed", "withdrawn", "rejected") {
		return f, "", ErrNotFound
	}
	if feedbackID != nil {
		fb, e := scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackCols+` FROM pvzce_site.feedback f WHERE id=$1`, *feedbackID))
		if e != nil {
			return f, "", e
		}
		if oneOf(fb.ContentState, "removed", "withdrawn", "rejected") {
			return f, "", ErrNotFound
		}
		if uid > 0 && (uid == fb.ReporterID || isStaff(a)) {
			if isStaff(a) && uid != fb.ReporterID {
				if e := audit(ctx, tx, uid, "file.private_download", fmt.Sprint(id), "staff private feedback access"); e != nil {
					return f, "", e
				}
			}
			return f, key, nil
		}
		if private || state != "published" || (label != "general" && !include) {
			return f, "", ErrNotFound
		}
		if e := s.feedbackAccess(ctx, tx, uid, fb, include); e != nil {
			return f, "", e
		}
		return f, key, nil
	}
	var ps, pl string
	var icon *int64
	if e := tx.QueryRow(ctx, `SELECT state,content_label,icon_file_id FROM pvzce_site.projects WHERE id=$1`, f.ProjectID).Scan(&ps, &pl, &icon); e != nil {
		return f, "", e
	}
	public := ps == "published" && (pl == "general" || include) && state == "published" && (label == "general" || include)
	if f.Purpose == "package" && f.VersionID != nil {
		var vs, vl string
		if e := tx.QueryRow(ctx, `SELECT state,content_label FROM pvzce_site.versions WHERE id=$1`, *f.VersionID).Scan(&vs, &vl); e != nil {
			return f, "", e
		}
		public = public && vs == "published" && (vl == "general" || include)
	} else if f.Purpose == "icon" {
		public = public && icon != nil && *icon == id
	} else {
		public = false
	}
	if public {
		return f, key, nil
	}
	if f.Purpose == "attachment" && uid > 0 {
		m, e := member(ctx, tx, f.ProjectID, uid)
		if e != nil {
			return f, "", e
		}
		if m || isStaff(a) {
			return f, key, nil
		}
	}
	return f, "", ErrNotFound
}
func (s *Service) fileConflict(ctx context.Context, tx pgx.Tx, uid, id int64) error {
	var pid, reporter, uploader int64
	if e := tx.QueryRow(ctx, `SELECT COALESCE(f.project_id,b.project_id,0),COALESCE(b.reporter_id,0),f.uploader_id FROM pvzce_site.files f LEFT JOIN pvzce_site.feedback b ON b.id=f.feedback_id WHERE f.id=$1`, id).Scan(&pid, &reporter, &uploader); e != nil {
		return e
	}
	if uid == reporter || uid == uploader {
		return ErrForbidden
	}
	if pid > 0 {
		m, e := reviewerConflict(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		if m {
			return ErrForbidden
		}
	}
	return nil
}
func (s *Service) reviewFileTx(ctx context.Context, tx pgx.Tx, uid, id int64, in ReviewInput) error {
	a, e := s.actor(ctx, tx, uid)
	if e != nil {
		return e
	}
	if !isStaff(a) || !reasonOK(in.Reason) || !oneOf(in.Action, "approve", "reject", "remove", "restore", "quarantine") {
		return ErrForbidden
	}
	if e := s.fileConflict(ctx, tx, uid, id); e != nil {
		return e
	}
	var state, label, scan, validation string
	var safety bool
	if e := tx.QueryRow(ctx, `SELECT content_state,content_label,scan_state,validation_state,safety_quarantined FROM pvzce_site.files WHERE id=$1 FOR UPDATE`, id).Scan(&state, &label, &scan, &validation, &safety); e != nil {
		return e
	}
	cat := in.Category
	if cat == "" {
		cat = label
	}
	if !validCategory(cat) {
		return ErrInvalid
	}
	next := "removed"
	if in.Action != "quarantine" {
		next, e = nextContentState(state, in.Action)
		if e != nil {
			return e
		}
	}
	if next == "published" && (!allowedCategory(cat) || scan != "clean" || validation != "valid" || safety) {
		return ErrInvalid
	}
	if allowedCategory(cat) {
		label = cat
	}
	if _, e := tx.Exec(ctx, `UPDATE pvzce_site.files SET content_state=$2,content_label=$3,safety_quarantined=safety_quarantined OR $4 WHERE id=$1`, id, next, label, in.Action == "quarantine"); e != nil {
		return e
	}
	return recordAction(ctx, tx, uid, "file", id, 0, in, state, next, cat)
}
func (s *Service) ReviewAttachment(ctx context.Context, uid, id int64, in ReviewInput) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error { return s.reviewFileTx(ctx, tx, uid, id, in) })
}

// Clearing safety isolation is a separate admin operation requiring a fresh clean scan. It never publishes the file.
func (s *Service) ClearSafety(ctx context.Context, uid, id int64, reason string) error {
	if !reasonOK(reason) {
		return ErrInvalid
	}
	if s.Scanner == nil || s.Objects == nil {
		return ErrUnavailable
	}
	var key string
	check := func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		if a.Role != "admin" {
			return ErrForbidden
		}
		if e := s.fileConflict(ctx, tx, uid, id); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT object_key FROM pvzce_site.files WHERE id=$1 AND safety_quarantined AND scan_state='clean' AND validation_state='valid' FOR UPDATE`, id).Scan(&key)
	}
	if e := s.DB.InTx(ctx, check); e != nil {
		return e
	}
	r, e := s.Objects.Open(ctx, key)
	if e != nil {
		return ErrUnavailable
	}
	defer r.Close()
	verdict, e := s.Scanner.Scan(ctx, r)
	if e != nil || verdict != "clean" {
		return ErrUnavailable
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if e := check(ctx, tx); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.files SET safety_quarantined=false,content_state='pending' WHERE id=$1`, id); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "file.safety_clear", fmt.Sprint(id), reason+"; fresh scan clean; manual review still required")
	})
}
func (s *Service) canRetryFile(ctx context.Context, tx pgx.Tx, uid, id int64) error {
	a, e := s.actor(ctx, tx, uid)
	if e != nil {
		return e
	}
	var pid, reporter int64
	if e := tx.QueryRow(ctx, `SELECT COALESCE(f.project_id,0),COALESCE(b.reporter_id,0) FROM pvzce_site.files f LEFT JOIN pvzce_site.feedback b ON b.id=f.feedback_id WHERE f.id=$1`, id).Scan(&pid, &reporter); e != nil {
		return e
	}
	m := uid == reporter
	if pid > 0 {
		m, e = member(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
	}
	if !m && !isStaff(a) {
		return ErrForbidden
	}
	return nil
}

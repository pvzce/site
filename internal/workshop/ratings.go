package workshop

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

func refreshRating(ctx context.Context, tx pgx.Tx, pid int64) error {
	_, e := tx.Exec(ctx, `UPDATE pvzce_site.projects SET rating_sum=r.total,rating_count=r.n FROM (SELECT COALESCE(sum(score),0)::bigint AS total,count(*) AS n FROM pvzce_site.ratings v JOIN pvzce_site.accounts a ON a.user_id=v.user_id WHERE v.project_id=$1 AND NOT v.voided AND a.status='active') r WHERE id=$1`, pid)
	return e
}
func refreshUserRatings(ctx context.Context, tx pgx.Tx, uid int64) error {
	rows, e := tx.Query(ctx, `SELECT project_id FROM pvzce_site.ratings WHERE user_id=$1 ORDER BY project_id`, uid)
	if e != nil {
		return e
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if e := rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	for _, id := range ids {
		var locked int64
		if e := tx.QueryRow(ctx, `SELECT id FROM pvzce_site.projects WHERE id=$1 FOR UPDATE`, id).Scan(&locked); e != nil {
			return e
		}
		if e := refreshRating(ctx, tx, id); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) SetRating(ctx context.Context, uid, pid int64, score int) error {
	if score < 1 || score > 5 {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		if e := limitedActivity(ctx, tx, uid, "rating.write", 5, 100); e != nil {
			return e
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM pvzce_site.projects WHERE id=$1 FOR UPDATE`, pid).Scan(&state); e != nil {
			return e
		}
		if state != "published" {
			return ErrNotFound
		}
		m, e := member(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		if m {
			return ErrForbidden
		}
		tag, e := tx.Exec(ctx, `INSERT INTO pvzce_site.ratings(project_id,user_id,score) VALUES($1,$2,$3) ON CONFLICT(project_id,user_id) DO UPDATE SET score=excluded.score,updated_at=now() WHERE NOT pvzce_site.ratings.voided`, pid, uid, score)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrForbidden
		}
		if e := refreshRating(ctx, tx, pid); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "rating.write", fmt.Sprint(pid), "account rating updated")
	})
}
func (s *Service) DeleteRating(ctx context.Context, uid, pid int64) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		var id int64
		if e := tx.QueryRow(ctx, `SELECT id FROM pvzce_site.projects WHERE id=$1 FOR UPDATE`, pid).Scan(&id); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `DELETE FROM pvzce_site.ratings WHERE project_id=$1 AND user_id=$2 AND NOT voided`, pid, uid); e != nil {
			return e
		}
		if e := refreshRating(ctx, tx, pid); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "rating.delete", fmt.Sprint(pid), "self withdrawal")
	})
}
func (s *Service) RatingSummary(ctx context.Context, uid, pid int64, include bool) (any, error) {
	var out any
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if e := s.projectVisible(ctx, tx, uid, pid, include); e != nil {
			return e
		}
		var count, sum int64
		if e := tx.QueryRow(ctx, `SELECT rating_count,rating_sum FROM pvzce_site.projects WHERE id=$1`, pid).Scan(&count, &sum); e != nil {
			return e
		}
		distribution := map[int]int64{1: 0, 2: 0, 3: 0, 4: 0, 5: 0}
		rows, e := tx.Query(ctx, `SELECT r.score,count(*) FROM pvzce_site.ratings r JOIN pvzce_site.accounts a ON a.user_id=r.user_id WHERE r.project_id=$1 AND NOT r.voided AND a.status='active' GROUP BY r.score`, pid)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var score int
			var n int64
			if e := rows.Scan(&score, &n); e != nil {
				return e
			}
			distribution[score] = n
		}
		var mean float64
		if count > 0 {
			mean = float64(sum) / float64(count)
		}
		out = map[string]any{"project_id": pid, "count": count, "sum": sum, "mean": mean, "distribution": distribution, "minimum": 1, "maximum": 5}
		return rows.Err()
	})
	return out, e
}

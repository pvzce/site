package workshop

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Reservation struct {
	ID                  string
	UserID              int64
	IPHash, Day         string
	Reserved, Accounted int64
	State               string
	FileID              *int64
}

func (s *Service) Hash(value string) string {
	h := hmac.New(sha256.New, []byte(s.Settings.HashKey))
	_, _ = h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}

// ClientIP trusts X-Forwarded-For only when the immediate peer is an explicitly trusted proxy.
// Walk right-to-left; the first untrusted hop is the source, not a client-controlled leftmost value.
func (s *Service) ClientIP(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", ErrInvalid
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", ErrInvalid
	}
	ip = ip.Unmap()
	trusted := func(ip netip.Addr) bool {
		for _, p := range s.Settings.TrustedProxies {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}
	if trusted(ip) && strings.TrimSpace(r.Header.Get("X-Forwarded-For")) != "" {
		chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if len(chain) > 32 {
			return "", ErrInvalid
		}
		for i := len(chain) - 1; i >= 0; i-- {
			if !trusted(ip) {
				break
			}
			n, e := netip.ParseAddr(strings.TrimSpace(chain[i]))
			if e != nil {
				return "", ErrInvalid
			}
			ip = n.Unmap()
		}
	}
	return ip.String(), nil
}
func quotaScopes(uid int64, ip string) []string {
	v := []string{fmt.Sprintf("account:%d", uid), "ip:" + ip}
	sort.Strings(v)
	return v
}
func lockQuota(ctx context.Context, tx pgx.Tx, r Reservation) error {
	for _, scope := range quotaScopes(r.UserID, r.IPHash) {
		if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.quota_days(day,scope) VALUES($1,$2) ON CONFLICT DO NOTHING`, r.Day, scope); e != nil {
			return e
		}
		var used int64
		if e := tx.QueryRow(ctx, `SELECT used FROM pvzce_site.quota_days WHERE day=$1 AND scope=$2 FOR UPDATE`, r.Day, scope).Scan(&used); e != nil {
			return e
		}
	}
	return nil
}
func scanReservation(row pgx.Row) (Reservation, error) {
	var r Reservation
	e := row.Scan(&r.ID, &r.UserID, &r.IPHash, &r.Day, &r.Reserved, &r.Accounted, &r.State, &r.FileID)
	return r, e
}

const reservationCols = `id,user_id,ip_hash,day::text,reserved,accounted,state,file_id`

// Begin reserves the complete declared length against account AND IP in one transaction.
// Idempotency keys are per account, include an immutable request fingerprint, and never re-read a completed body.
func (s *Service) Begin(ctx context.Context, uid int64, ip, key, fingerprint string, size int64) (Reservation, error) {
	if size < 1 || size > s.Settings.MaxFile+1 || len(key) < 8 || len(key) > 128 || len(s.Settings.HashKey) < 32 {
		return Reservation{}, ErrInvalid
	}
	if e := s.Reap(ctx); e != nil {
		return Reservation{}, e
	}
	r := Reservation{ID: rand.Text() + rand.Text(), UserID: uid, IPHash: s.Hash("ip:" + ip), Day: time.Now().In(s.Settings.Location).Format("2006-01-02"), Reserved: size, State: "receiving"}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		// Same-account retries serialize even across multiple backend processes.
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, uid); e != nil {
			return e
		}
		var oldFP string
		old, e := scanReservation(tx.QueryRow(ctx, `SELECT `+reservationCols+` FROM pvzce_site.uploads WHERE user_id=$1 AND idempotency_key=$2`, uid, key))
		if e == nil {
			if e := tx.QueryRow(ctx, `SELECT fingerprint FROM pvzce_site.uploads WHERE id=$1`, old.ID).Scan(&oldFP); e != nil {
				return e
			}
			if oldFP != fingerprint || old.State != "complete" {
				return ErrConflict
			}
			r = old
			return nil
		}
		if e != pgx.ErrNoRows {
			return e
		}
		if e := lockQuota(ctx, tx, r); e != nil {
			return e
		}
		for _, scope := range quotaScopes(uid, r.IPHash) {
			tag, e := tx.Exec(ctx, `UPDATE pvzce_site.quota_days SET reserved=reserved+$3 WHERE day=$1 AND scope=$2 AND used+reserved <= $4::bigint-$3::bigint`, r.Day, scope, size, s.Settings.DailyQuota)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrQuota
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO pvzce_site.uploads(id,user_id,ip_hash,day,idempotency_key,fingerprint,reserved,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,LEAST(now()+interval '15 minutes',(date_trunc('day',now() AT TIME ZONE $8)+interval '1 day') AT TIME ZONE $8))`, r.ID, uid, r.IPHash, r.Day, key, fingerprint, size, s.Settings.Location.String())
		return e
	})
	return r, e
}

// AuthorizeRead durably accounts a bounded read BEFORE it touches the network body.
// A process crash may conservatively charge its final in-flight chunk (<=64 KiB), never undercharge traffic.
func (s *Service) AuthorizeRead(ctx context.Context, id string, n int64) error {
	tag, e := s.DB.Exec(ctx, `UPDATE pvzce_site.uploads SET accounted=accounted+$2 WHERE id=$1 AND state='receiving' AND expires_at>now() AND accounted+$2<=reserved`, id, n)
	if e != nil {
		return e
	}
	if n < 1 || tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func settleQuota(ctx context.Context, tx pgx.Tx, r Reservation, received int64, state string, file *int64) error {
	if received < 0 || received > r.Accounted {
		return ErrInvalid
	}
	if e := lockQuota(ctx, tx, r); e != nil {
		return e
	}
	for _, scope := range quotaScopes(r.UserID, r.IPHash) {
		if _, e := tx.Exec(ctx, `UPDATE pvzce_site.quota_days SET reserved=reserved-$3,used=used+$4 WHERE day=$1 AND scope=$2`, r.Day, scope, r.Reserved, received); e != nil {
			return e
		}
	}
	_, e := tx.Exec(ctx, `UPDATE pvzce_site.uploads SET received=$2,state=$3,file_id=$4 WHERE id=$1`, r.ID, received, state, file)
	return e
}
func (s *Service) Finish(ctx context.Context, id string, received int64, state string, file *int64) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		r, e := scanReservation(tx.QueryRow(ctx, `SELECT `+reservationCols+` FROM pvzce_site.uploads WHERE id=$1 FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if r.State != "receiving" {
			return ErrConflict
		}
		if !oneOf(state, "complete", "failed") {
			return ErrInvalid
		}
		return settleQuota(ctx, tx, r, received, state, file)
	})
}
func (s *Service) Reap(ctx context.Context) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT `+reservationCols+` FROM pvzce_site.uploads WHERE state='receiving' AND expires_at<=now() ORDER BY id LIMIT 100 FOR UPDATE SKIP LOCKED`)
		if e != nil {
			return e
		}
		var list []Reservation
		for rows.Next() {
			r, e := scanReservation(rows)
			if e != nil {
				rows.Close()
				return e
			}
			list = append(list, r)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		// Serialize reclaim batches to avoid reversed quota-lock ordering across expired accounts/IPs.
		// One reservation per transaction would also work; globally sort all quota locks first.
		keys := map[string]Reservation{}
		for _, r := range list {
			for _, scope := range quotaScopes(r.UserID, r.IPHash) {
				keys[r.Day+"/"+scope] = r
			}
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			parts := strings.SplitN(k, "/", 2)
			var n int64
			if e := tx.QueryRow(ctx, `SELECT used FROM pvzce_site.quota_days WHERE day=$1 AND scope=$2 FOR UPDATE`, parts[0], parts[1]).Scan(&n); e != nil {
				return e
			}
		}
		for _, r := range list {
			if e := settleQuota(ctx, tx, r, r.Accounted, "expired", nil); e != nil {
				return e
			}
		}
		return nil
	})
}
func (s *Service) Usage(ctx context.Context, uid int64, ip string) (any, error) {
	if _, e := s.Profile(ctx, uid); e != nil {
		return nil, e
	}
	day := time.Now().In(s.Settings.Location).Format("2006-01-02")
	var used, reserved int64
	e := s.DB.QueryRow(ctx, `SELECT COALESCE(sum(used),0)::bigint,COALESCE(sum(reserved),0)::bigint FROM pvzce_site.quota_days WHERE day=$1 AND scope=$2`, day, fmt.Sprintf("account:%d", uid)).Scan(&used, &reserved)
	return map[string]any{"day": day, "timezone": s.Settings.Location.String(), "unit": "bytes", "used": used, "reserved": reserved, "daily_quota_bytes": s.Settings.DailyQuota, "max_file_bytes": s.Settings.MaxFile}, e
}

// ReceiveDeadline prevents a transfer from spending yesterday's reservation after the day boundary.
func (s *Service) ReceiveDeadline() time.Time {
	return receiveDeadline(time.Now(), s.Settings.Location)
}
func receiveDeadline(now time.Time, location *time.Location) time.Time {
	local := now.In(location)
	next := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, location)
	deadline := now.Add(10 * time.Minute)
	if next.Before(deadline) {
		return next
	}
	return deadline
}

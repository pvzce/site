package workshop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type File struct {
	ID              int64           `json:"id"`
	ProjectID       int64           `json:"project_id"`
	VersionID       *int64          `json:"version_id"`
	Purpose         string          `json:"purpose"`
	Filename        string          `json:"filename"`
	Size            int64           `json:"size"`
	SHA256          string          `json:"sha256"`
	ScanState       string          `json:"scan_state"`
	ValidationState string          `json:"validation_state"`
	Metadata        json.RawMessage `json:"metadata"`
}

func scanFile(row pgx.Row, f *File) error {
	return row.Scan(&f.ID, &f.ProjectID, &f.VersionID, &f.Purpose, &f.Filename, &f.Size, &f.SHA256, &f.ScanState, &f.ValidationState, &f.Metadata)
}

const fileCols = `f.id,COALESCE(f.project_id,0),f.version_id,f.purpose,f.filename,f.size,f.sha256,f.scan_state,f.validation_state,f.metadata`

type UploadInput struct {
	ProjectID, VersionID, FeedbackID                      int64
	Public                                                bool
	ContentLabel                                          string
	Purpose, Filename, ExpectedSHA256, IdempotencyKey, IP string
	Size                                                  int64
}

var credentialPattern = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(bearer|basic)|-----BEGIN[^\n]*PRIVATE KEY-----|(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password)\s*["\x27]?\s*[:=]\s*["\x27]?[a-z0-9_./+\-=]{6,}|gh[pousr]_[a-zA-Z0-9]{20,})`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *Service) uploadTarget(ctx context.Context, uid int64, in UploadInput) (Project, Version, error) {
	var p Project
	var v Version
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		if in.FeedbackID > 0 {
			return s.authorizeFeedbackUpload(ctx, tx, uid, in.FeedbackID)
		}
		var e error
		p, e = scanProject(tx.QueryRow(ctx, `SELECT `+projectCols+` FROM pvzce_site.projects p WHERE p.id=$1 FOR SHARE`, in.ProjectID))
		if e != nil {
			return e
		}
		m, e := member(ctx, tx, in.ProjectID, uid)
		if e != nil {
			return e
		}
		if !m {
			return ErrForbidden
		}
		if oneOf(p.State, "removed", "withdrawn") {
			return ErrInvalid
		}
		if in.Purpose == "package" {
			if e := tx.QueryRow(ctx, `SELECT id,number,state FROM pvzce_site.versions WHERE id=$1 AND project_id=$2`, in.VersionID, in.ProjectID).Scan(&v.ID, &v.Number, &v.State); e != nil {
				return e
			}
			rows, e := tx.Query(ctx, `SELECT game_version FROM pvzce_site.version_compatibility WHERE version_id=$1`, in.VersionID)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var version string
				if e := rows.Scan(&version); e != nil {
					return e
				}
				v.GameVersions = append(v.GameVersions, version)
			}
			return rows.Err()
		}
		return nil
	})
	return p, v, e
}
func (s *Service) Upload(ctx context.Context, uid int64, in UploadInput, body io.Reader) (File, error) {
	ctx, cancel := context.WithDeadline(ctx, s.ReceiveDeadline())
	defer cancel()
	var out File
	if s.Objects == nil || s.Scanner == nil || s.Settings.ScannerAddress == "" {
		return out, ErrUnavailable
	}
	if in.Size < 1 {
		return out, ErrInvalid
	}
	if in.Size > s.Settings.MaxFile {
		return out, ErrTooLarge
	}
	if !oneOf(in.Purpose, "package", "icon", "attachment") || in.Filename == "" || len(in.Filename) > 200 || filepath.Base(in.Filename) != in.Filename || strings.ContainsAny(in.Filename, "/\\\r\n\x00") || !hashPattern.MatchString(in.ExpectedSHA256) || ((in.Purpose == "package") != (in.VersionID > 0)) {
		return out, ErrInvalid
	}
	if in.FeedbackID < 0 || (in.FeedbackID > 0 && (in.ProjectID != 0 || in.VersionID != 0 || in.Purpose != "attachment")) || (in.ContentLabel != "" && !allowedCategory(in.ContentLabel)) {
		return out, ErrInvalid
	}
	if in.ContentLabel == "" {
		in.ContentLabel = "general"
	}
	if oneOf(strings.ToLower(filepath.Ext(in.Filename)), ".log", ".txt") {
		in.Public = false
	}
	p, v, e := s.uploadTarget(ctx, uid, in)
	if e != nil {
		return out, e
	}
	fingerprint := s.Hash(fmt.Sprintf("%d|%d|%d|%t|%s|%s|%s|%d|%s", in.ProjectID, in.VersionID, in.FeedbackID, in.Public, in.ContentLabel, in.Purpose, in.Filename, in.Size, in.ExpectedSHA256))
	r, e := s.Begin(ctx, uid, in.IP, in.IdempotencyKey, fingerprint, in.Size+1)
	if e != nil {
		return out, e
	}
	if r.State == "complete" {
		if r.FileID == nil {
			return out, ErrConflict
		}
		e = scanFile(s.DB.QueryRow(ctx, `SELECT `+fileCols+` FROM pvzce_site.files f WHERE f.id=$1 AND f.uploader_id=$2`, *r.FileID, uid), &out)
		return out, e
	}
	received := int64(0)
	settled := false
	defer func() {
		if !settled {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Finish(cleanup, r.ID, received, "failed", nil)
		}
	}()
	if (in.Purpose == "package" && !oneOf(v.State, "draft", "rejected")) || (in.Purpose == "icon" && p.State == "pending") {
		return out, ErrInvalid
	}
	if in.Purpose == "icon" && in.Size > 8<<20 {
		return out, ErrTooLarge
	}
	tempDir := os.Getenv("UPLOAD_TEMP_DIR")
	if tempDir == "" {
		tempDir = "cache/uploads"
	}
	if e := os.MkdirAll(tempDir, 0700); e != nil {
		return out, ErrUnavailable
	}
	temp, e := os.CreateTemp(tempDir, "upload-*")
	if e != nil {
		return out, ErrUnavailable
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	hasher := sha256.New()
	buf := make([]byte, 64<<10)
	for received < in.Size {
		wanted := min(int64(len(buf)), in.Size-received)
		if e = s.AuthorizeRead(ctx, r.ID, wanted); e != nil {
			return out, e
		}
		n, re := io.ReadFull(body, buf[:wanted])
		received += int64(n)
		if n > 0 {
			if _, e = temp.Write(buf[:n]); e != nil {
				return out, e
			}
			_, _ = hasher.Write(buf[:n])
		}
		if re != nil {
			return out, ErrInvalid
		}
	}
	if e = s.AuthorizeRead(ctx, r.ID, 1); e != nil {
		return out, e
	}
	n, tailErr := body.Read(buf[:1])
	received += int64(n)
	if n != 0 || tailErr != io.EOF {
		return out, ErrInvalid
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if sum != in.ExpectedSHA256 {
		return out, fmt.Errorf("%w: SHA-256 mismatch", ErrInvalid)
	}
	out = File{ProjectID: in.ProjectID, Purpose: in.Purpose, Filename: in.Filename, Size: received, SHA256: sum, ScanState: "failed", ValidationState: "invalid"}
	if in.VersionID > 0 {
		out.VersionID = &in.VersionID
	}
	meta := map[string]any{}
	switch in.Purpose {
	case "package":
		m, ve := ValidatePackage(temp, p.Kind, v.Number, v.GameVersions)
		if ve == nil {
			out.ValidationState = "valid"
			meta["package"] = m
		} else {
			meta["validation_error"] = ve.Error()
		}
	case "icon":
		mime, ve := ValidateIcon(temp)
		if ve == nil {
			out.ValidationState = "valid"
			meta["content_type"] = mime
		} else {
			meta["validation_error"] = "PNG/JPEG with maximum 2048x2048 required"
		}
	case "attachment":
		// Private attachment service shared with feedback in part two; only bounded text/PNG/JPEG accepted.
		if strings.HasSuffix(strings.ToLower(in.Filename), ".txt") || strings.HasSuffix(strings.ToLower(in.Filename), ".log") {
			if _, e = temp.Seek(0, 0); e != nil {
				return out, e
			}
			validator := make([]byte, 4096)
			carry := []byte{}
			valid := true
			secretTail := ""
			for {
				n, re := temp.Read(validator)
				combined := secretTail + string(validator[:n])
				if credentialPattern.MatchString(combined) {
					valid = false
					meta["validation_error"] = "Remove credentials and private information before uploading logs"
					break
				}
				secretTail = combined[max(0, len(combined)-512):]
				data := append(carry, validator[:n]...)
				carry = nil
				for len(data) > 0 {
					if !utf8.FullRune(data) {
						carry = append([]byte(nil), data...)
						break
					}
					r, size := utf8.DecodeRune(data)
					if r == utf8.RuneError && size == 1 || r == 0 {
						valid = false
						break
					}
					data = data[size:]
				}
				if !valid || re == io.EOF {
					break
				}
				if re != nil {
					return out, re
				}
			}
			if valid && len(carry) == 0 {
				out.ValidationState = "valid"
				meta["content_type"] = "text/plain"
			}
		} else {
			mime, ve := ValidateIcon(temp)
			if ve == nil {
				out.ValidationState = "valid"
				meta["content_type"] = mime
			}
		}
	}
	if _, e = temp.Seek(0, 0); e != nil {
		return out, e
	}
	scan, scanErr := s.Scanner.Scan(ctx, temp)
	if scanErr == nil && oneOf(scan, "clean", "infected") {
		out.ScanState = scan
	}
	if out.ScanState == "failed" {
		meta["scan_error"] = "scanner unavailable, timed out or returned no verifiable verdict; quarantined"
	}
	out.Metadata, e = json.Marshal(meta)
	if e != nil {
		return out, e
	}
	if _, e = temp.Seek(0, 0); e != nil {
		return out, e
	}
	key := "quarantine/" + r.ID
	if e = s.Objects.Put(ctx, key, temp, received); e != nil {
		return out, ErrUnavailable
	}
	e = s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		var state string
		if in.FeedbackID > 0 {
			if e := s.authorizeFeedbackUpload(ctx, tx, uid, in.FeedbackID); e != nil {
				return e
			}
		} else {
			if e := tx.QueryRow(ctx, `SELECT state FROM pvzce_site.projects WHERE id=$1 FOR UPDATE`, in.ProjectID).Scan(&state); e != nil {
				return e
			}
			m, e := member(ctx, tx, in.ProjectID, uid)
			if e != nil {
				return e
			}
			if !m || oneOf(state, "removed", "withdrawn") {
				return ErrForbidden
			}
		}
		if in.Purpose == "package" {
			var st string
			if e := tx.QueryRow(ctx, `SELECT state FROM pvzce_site.versions WHERE id=$1 AND project_id=$2 FOR UPDATE`, in.VersionID, in.ProjectID).Scan(&st); e != nil {
				return e
			}
			if !oneOf(st, "draft", "rejected") {
				return ErrInvalid
			}
		}
		current, e := scanReservation(tx.QueryRow(ctx, `SELECT `+reservationCols+` FROM pvzce_site.uploads WHERE id=$1 FOR UPDATE`, r.ID))
		if e != nil {
			return e
		}
		if current.State != "receiving" {
			return ErrConflict
		}
		contentState := "published"
		if in.Purpose == "attachment" {
			contentState = "pending"
		}
		if e := tx.QueryRow(ctx, `INSERT INTO pvzce_site.files(project_id,version_id,uploader_id,purpose,object_key,filename,size,sha256,scan_state,validation_state,metadata,feedback_id,is_private,content_label,content_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`, nullableID(in.ProjectID), out.VersionID, uid, in.Purpose, key, in.Filename, out.Size, sum, out.ScanState, out.ValidationState, out.Metadata, nullableID(in.FeedbackID), !in.Public, in.ContentLabel, contentState).Scan(&out.ID); e != nil {
			return conflict(e)
		}
		if in.Purpose == "icon" && out.ScanState == "clean" && out.ValidationState == "valid" {
			if state == "pending" {
				return ErrInvalid
			}
			if _, e := tx.Exec(ctx, `UPDATE pvzce_site.projects SET icon_file_id=$2,state='draft',updated_at=now() WHERE id=$1`, in.ProjectID, out.ID); e != nil {
				return e
			}
		}
		if in.Purpose == "icon" && out.ScanState == "clean" && out.ValidationState == "valid" {
			if e := projectRevision(ctx, tx, uid, in.ProjectID); e != nil {
				return e
			}
		}
		if e := settleQuota(ctx, tx, current, received, "complete", &out.ID); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "file.upload", fmt.Sprint(out.ID), "scan="+out.ScanState+" validation="+out.ValidationState)
	})
	if e != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Objects.Delete(cleanup, key)
		return out, e
	}
	settled = true
	return out, nil
}

// Download opens only authorized, valid and clean objects. Quarantine keys and presigned URLs are never returned.
func (s *Service) Download(ctx context.Context, uid, id int64, ip string, head bool) (File, io.ReadCloser, error) {
	return s.DownloadContent(ctx, uid, id, ip, head, false)
}
func (s *Service) DownloadContent(ctx context.Context, uid, id int64, ip string, head, include bool) (File, io.ReadCloser, error) {
	var out File
	var key string
	if s.Objects == nil {
		return out, nil, ErrUnavailable
	}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, key, e = s.fileAccess(ctx, tx, uid, id, include, false)
		return e
	})
	if e != nil {
		return out, nil, e
	}
	reader, e := s.Objects.Open(ctx, key)
	if e != nil {
		return out, nil, ErrUnavailable
	}
	if head || out.Purpose != "package" {
		return out, reader, nil
	}
	// Count a successful object-open once per file/IP/UTC day; stream completion cannot reliably be observed across a disconnect.
	e = s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `INSERT INTO pvzce_site.downloads(file_id,day,visitor_hash) VALUES($1,(now() AT TIME ZONE 'UTC')::date,$2) ON CONFLICT DO NOTHING`, id, s.Hash("download:"+ip))
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if _, e = tx.Exec(ctx, `UPDATE pvzce_site.projects SET downloads=downloads+1 WHERE id=$1`, out.ProjectID); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE pvzce_site.versions SET downloads=downloads+1 WHERE id=$1`, out.VersionID)
		return e
	})
	if e != nil {
		_ = reader.Close()
		return out, nil, e
	}
	return out, reader, nil
}

// ReviewFile is a separate audited reviewer inspection path; it never exposes unsafe or unscanned files.
func (s *Service) ReviewFile(ctx context.Context, uid, id int64) (File, io.ReadCloser, error) {
	var out File
	var key string
	if s.Objects == nil {
		return out, nil, ErrUnavailable
	}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, key, e = s.fileAccess(ctx, tx, uid, id, true, true)
		return e
	})
	if e != nil {
		return out, nil, e
	}
	r, e := s.Objects.Open(ctx, key)
	return out, r, e
}

// RetryScan handles a failed scanner result only. It cannot clear an infected verdict or publish content.
func (s *Service) RetryScan(ctx context.Context, uid, id int64) error {
	if s.Objects == nil || s.Scanner == nil {
		return ErrUnavailable
	}
	var key string
	check := func(ctx context.Context, tx pgx.Tx) error {
		if e := s.canRetryFile(ctx, tx, uid, id); e != nil {
			return e
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT object_key,scan_state FROM pvzce_site.files WHERE id=$1`, id).Scan(&key, &state); e != nil {
			return e
		}
		if state != "failed" {
			return ErrInvalid
		}
		return nil
	}
	if e := s.DB.InTx(ctx, check); e != nil {
		return e
	}
	r, e := s.Objects.Open(ctx, key)
	if e != nil {
		return ErrUnavailable
	}
	defer r.Close()
	result, e := s.Scanner.Scan(ctx, r)
	if e != nil || !oneOf(result, "clean", "infected") {
		return ErrUnavailable
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if e := check(ctx, tx); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE pvzce_site.files SET scan_state=$2,metadata=metadata-'scan_error' WHERE id=$1 AND scan_state='failed'`, id, result)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return audit(ctx, tx, uid, "file.rescan", fmt.Sprint(id), result+"; publication unchanged")
	})
}

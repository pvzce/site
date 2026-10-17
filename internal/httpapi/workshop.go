package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pvzce-site/internal/auth"
	"pvzce-site/internal/workshop"
)

func optionalSession(service *auth.Service, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if service != nil {
			if c, e := r.Cookie(sessionCookie); e == nil {
				s, e := service.Session(r.Context(), c.Value)
				if e == nil {
					r = r.WithContext(withAccount(r.Context(), s))
				} else if !errors.Is(e, auth.ErrUnauthorized) {
					writeError(w, r, 503, "auth_unavailable", "Account service unavailable")
					return
				}
			}
		}
		next(w, r)
	}
}
func requestUser(r *http.Request) int64 { s, _ := Account(r.Context()); return s.User.ID }
func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}
func businessError(w http.ResponseWriter, r *http.Request, e error) {
	status := workshop.Status(e)
	message := "Request could not be completed"
	if status < 500 {
		message = e.Error()
	}
	writeError(w, r, status, "workshop_error", message)
}
func businessBody(w http.ResponseWriter, r *http.Request, dst any) bool { return decodeJSON(w, r, dst) }
func businessResult(w http.ResponseWriter, r *http.Request, data any, e error) {
	if e != nil {
		businessError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"data": data})
}

func registerWorkshop(mux *http.ServeMux, deps Dependencies) {
	s := deps.Workshop
	ready := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s == nil {
				writeError(w, r, 503, "workshop_unavailable", "Workshop requires configured accounts and PostgreSQL")
				return
			}
			h(w, r)
		}
	}
	protect := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, ready(func(w http.ResponseWriter, r *http.Request) { RequireAccount(deps.Auth, h).ServeHTTP(w, r) }))
	}
	public := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, ready(optionalSession(deps.Auth, h)))
	}
	public("GET /api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("size"))
		data, e := s.List(r.Context(), requestUser(r), workshop.Filter{Search: q.Get("q"), Kind: q.Get("kind"), Tag: q.Get("tag"), GameVersion: q.Get("game_version"), Sort: q.Get("sort"), Page: page, Size: size})
		businessResult(w, r, data, e)
	})
	protect("GET /api/v1/author/projects", func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		data, e := s.List(r.Context(), requestUser(r), workshop.Filter{Mine: true, Page: page})
		businessResult(w, r, data, e)
	})
	public("GET /api/v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Detail(r.Context(), requestUser(r), pathID(r, "id"))
		businessResult(w, r, d, e)
	})
	public("GET /api/v1/game-versions", func(w http.ResponseWriter, r *http.Request) {
		rows, e := s.DB.Query(r.Context(), `SELECT version,pack_format,reference_commit,loader FROM pvzce_site.game_versions ORDER BY version`)
		if e != nil {
			businessError(w, r, e)
			return
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var version, ref, loader string
			var format int
			if e := rows.Scan(&version, &format, &ref, &loader); e != nil {
				businessError(w, r, e)
				return
			}
			out = append(out, map[string]any{"version": version, "pack_format": format, "reference_commit": ref, "loader": loader})
		}
		businessResult(w, r, out, rows.Err())
	})
	protect("GET /api/v1/profile", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Profile(r.Context(), requestUser(r))
		businessResult(w, r, d, e)
	})
	protect("PATCH /api/v1/profile", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DisplayName string `json:"display_name"`
			Bio         string `json:"bio"`
		}
		if !businessBody(w, r, &in) {
			return
		}
		businessResult(w, r, nil, s.EditProfile(r.Context(), requestUser(r), in.DisplayName, in.Bio))
	})
	protect("POST /api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		var in workshop.ProjectInput
		if !businessBody(w, r, &in) {
			return
		}
		id, e := s.CreateProject(r.Context(), requestUser(r), in)
		businessResult(w, r, map[string]int64{"id": id}, e)
	})
	protect("PATCH /api/v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in workshop.ProjectInput
		if !businessBody(w, r, &in) {
			return
		}
		businessResult(w, r, nil, s.EditProject(r.Context(), requestUser(r), pathID(r, "id"), in))
	})
	protect("POST /api/v1/projects/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		var in workshop.VersionInput
		if !businessBody(w, r, &in) {
			return
		}
		id, e := s.CreateVersion(r.Context(), requestUser(r), pathID(r, "id"), in)
		businessResult(w, r, map[string]int64{"id": id}, e)
	})
	protect("GET /api/v1/projects/{id}/revisions", func(w http.ResponseWriter, r *http.Request) {
		before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		d, e := s.Revisions(r.Context(), requestUser(r), pathID(r, "id"), before)
		businessResult(w, r, d, e)
	})
	protect("POST /api/v1/projects/{id}/icon", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			FileID int64 `json:"file_id"`
		}
		if !businessBody(w, r, &in) {
			return
		}
		businessResult(w, r, nil, s.SetIcon(r.Context(), requestUser(r), pathID(r, "id"), in.FileID))
	})
	protect("POST /api/v1/projects/{id}/collaborators", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			UserID int64 `json:"user_id"`
			Remove bool  `json:"remove"`
		}
		if !businessBody(w, r, &in) {
			return
		}
		businessResult(w, r, nil, s.Collaborator(r.Context(), requestUser(r), pathID(r, "id"), in.UserID, in.Remove))
	})
	protect("POST /api/v1/projects/{id}/transitions", func(w http.ResponseWriter, r *http.Request) {
		var in workshop.ReviewInput
		if !businessBody(w, r, &in) {
			return
		}
		businessResult(w, r, nil, s.Transition(r.Context(), requestUser(r), pathID(r, "id"), 0, in))
	})
	protect("POST /api/v1/projects/{id}/versions/{version}/transitions", func(w http.ResponseWriter, r *http.Request) {
		var in workshop.ReviewInput
		if !businessBody(w, r, &in) {
			return
		}
		businessResult(w, r, nil, s.Transition(r.Context(), requestUser(r), pathID(r, "id"), pathID(r, "version"), in))
	})
	protect("GET /api/v1/upload-usage", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Usage(r.Context(), requestUser(r), "")
		businessResult(w, r, d, e)
	})
	protect("PUT /api/v1/projects/{id}/uploads/{purpose}", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength < 1 {
			writeError(w, r, 411, "length_required", "Content-Length required")
			return
		}
		ip, e := s.ClientIP(r)
		if e != nil {
			businessError(w, r, e)
			return
		}
		vid, _ := strconv.ParseInt(r.URL.Query().Get("version_id"), 10, 64)
		_ = http.NewResponseController(w).SetReadDeadline(s.ReceiveDeadline())
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Minute))
		d, e := s.Upload(r.Context(), requestUser(r), workshop.UploadInput{ProjectID: pathID(r, "id"), VersionID: vid, Purpose: r.PathValue("purpose"), Filename: r.Header.Get("Upload-Filename"), ExpectedSHA256: r.Header.Get("Upload-SHA256"), IdempotencyKey: r.Header.Get("Idempotency-Key"), IP: ip, Size: r.ContentLength}, r.Body)
		businessResult(w, r, d, e)
	})
	public("GET /api/v1/files/{file}/download", func(w http.ResponseWriter, r *http.Request) {
		ip, e := s.ClientIP(r)
		if e != nil {
			businessError(w, r, e)
			return
		}
		f, reader, e := s.Download(r.Context(), requestUser(r), pathID(r, "file"), ip, r.Method == "HEAD")
		if e != nil {
			businessError(w, r, e)
			return
		}
		defer reader.Close()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Minute))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Filename}))
		w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
		w.Header().Set("X-Checksum-SHA256", f.SHA256)
		if r.Method != "HEAD" {
			if n, e := io.CopyN(w, reader, f.Size); e != nil || n != f.Size {
				panic(http.ErrAbortHandler)
			}
		}
	})
	protect("GET /api/v1/review/files/{file}", func(w http.ResponseWriter, r *http.Request) {
		f, reader, e := s.ReviewFile(r.Context(), requestUser(r), pathID(r, "file"))
		if e != nil {
			businessError(w, r, e)
			return
		}
		defer reader.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Filename}))
		w.Header().Set("X-Checksum-SHA256", f.SHA256)
		if r.Method != "HEAD" {
			if _, e := io.CopyN(w, reader, f.Size); e != nil {
				panic(http.ErrAbortHandler)
			}
		}
	})
	protect("POST /api/v1/files/{file}/rescan", func(w http.ResponseWriter, r *http.Request) {
		businessResult(w, r, nil, s.RetryScan(r.Context(), requestUser(r), pathID(r, "file")))
	})
	public("GET /api/v1/projects/{id}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Detail(r.Context(), requestUser(r), pathID(r, "id"))
		if e != nil {
			businessError(w, r, e)
			return
		}
		for _, v := range d.Versions {
			if v.ID == pathID(r, "version") {
				businessResult(w, r, v, nil)
				return
			}
		}
		businessError(w, r, workshop.ErrNotFound)
	})
	protect("GET /api/v1/review-queue", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Queue(r.Context(), requestUser(r))
		businessResult(w, r, d, e)
	})
	protect("GET /api/v1/appeals", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Appeals(r.Context(), requestUser(r))
		businessResult(w, r, d, e)
	})
	protect("POST /api/v1/appeals", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ReviewID int64  `json:"review_id"`
			Reason   string `json:"reason"`
		}
		if !businessBody(w, r, &in) {
			return
		}
		id, e := s.Appeal(r.Context(), requestUser(r), in.ReviewID, in.Reason)
		businessResult(w, r, map[string]int64{"id": id}, e)
	})
	protect("POST /api/v1/appeals/{appeal}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reply string `json:"reply"`
		}
		if !businessBody(w, r, &in) {
			return
		}
		businessResult(w, r, nil, s.ResolveAppeal(r.Context(), requestUser(r), pathID(r, "appeal"), in.Reply))
	})
	protect("POST /api/v1/admin/accounts/{user}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Role   string `json:"role"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		}
		if !businessBody(w, r, &in) {
			return
		}
		businessResult(w, r, nil, s.ManageAccount(r.Context(), requestUser(r), pathID(r, "user"), in.Role, in.Status, in.Reason))
	})
	protect("GET /api/v1/admin/audit", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Audit(r.Context(), requestUser(r))
		businessResult(w, r, d, e)
	})
	registerWorkshopPages(mux, deps, public, protect)
}
func uploadPath(r *http.Request) bool {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return (r.Method == "PUT" && len(p) == 6 && p[0] == "api" && p[1] == "v1" && p[2] == "projects" && p[4] == "uploads") || (r.Method == "POST" && len(p) == 3 && p[0] == "projects" && p[2] == "upload")
}
func jsonPretty(v any) string {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

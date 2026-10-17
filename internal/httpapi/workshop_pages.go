package httpapi

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pvzce-site/internal/workshop"
)

//go:embed workshop.html
var workshopHTML string
var workshopTemplate = template.Must(template.New("workshop").Funcs(template.FuncMap{"pretty": jsonPretty, "join": strings.Join}).Parse(workshopHTML))

func renderWorkshop(w http.ResponseWriter, r *http.Request, page string, data any) {
	authHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	session, _ := Account(r.Context())
	_ = workshopTemplate.Execute(w, map[string]any{"Page": page, "Data": data, "Session": session, "Key": rand.Text() + rand.Text()})
}
func formInt(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(r.FormValue(key), 10, 64)
	return n
}
func formProject(r *http.Request) workshop.ProjectInput {
	tags := []string{}
	for _, tag := range strings.Split(r.FormValue("tags"), ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	return workshop.ProjectInput{Slug: r.FormValue("slug"), Title: r.FormValue("title"), Kind: r.FormValue("kind"), Description: r.FormValue("description"), License: r.FormValue("license"), Tags: tags, ContentLabel: r.FormValue("content_label")}
}
func pageDone(w http.ResponseWriter, r *http.Request, location string, e error) {
	if e != nil {
		businessError(w, r, e)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}
func registerWorkshopPages(mux *http.ServeMux, deps Dependencies, public, protect func(string, http.HandlerFunc)) {
	s := deps.Workshop
	public("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		d, e := s.List(r.Context(), requestUser(r), workshop.Filter{Search: q.Get("q"), Kind: q.Get("kind"), Tag: q.Get("tag"), GameVersion: q.Get("game_version"), Sort: q.Get("sort"), Page: page})
		if e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "list", d)
	})
	protect("GET /projects/new", func(w http.ResponseWriter, r *http.Request) {
		if _, e := s.Profile(r.Context(), requestUser(r)); e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "editor", workshop.Project{ProjectInput: workshop.ProjectInput{Kind: "mod", ContentLabel: "general"}})
	})
	protect("POST /projects/new", func(w http.ResponseWriter, r *http.Request) {
		id, e := s.CreateProject(r.Context(), requestUser(r), formProject(r))
		pageDone(w, r, "/projects/"+strconv.FormatInt(id, 10), e)
	})
	public("GET /projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Detail(r.Context(), requestUser(r), pathID(r, "id"))
		if e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "detail", d)
	})
	protect("GET /projects/{id}/edit", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Detail(r.Context(), requestUser(r), pathID(r, "id"))
		if e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "editor", d.Project)
	})
	protect("POST /projects/{id}/edit", func(w http.ResponseWriter, r *http.Request) {
		e := s.EditProject(r.Context(), requestUser(r), pathID(r, "id"), formProject(r))
		pageDone(w, r, "/projects/"+r.PathValue("id"), e)
	})
	protect("GET /author", func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		d, e := s.List(r.Context(), requestUser(r), workshop.Filter{Mine: true, Page: page})
		if e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "author", d)
	})
	protect("GET /profile", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Profile(r.Context(), requestUser(r))
		if e != nil {
			businessError(w, r, e)
			return
		}
		usage, e := s.Usage(r.Context(), requestUser(r), "")
		if e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "profile", map[string]any{"Profile": d, "Usage": usage})
	})
	protect("POST /profile", func(w http.ResponseWriter, r *http.Request) {
		pageDone(w, r, "/profile", s.EditProfile(r.Context(), requestUser(r), r.FormValue("display_name"), r.FormValue("bio")))
	})
	protect("POST /projects/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		var ds []workshop.Dependency
		if raw := r.FormValue("dependencies"); raw != "" {
			if e := decodeDependencies(raw, &ds); e != nil {
				businessError(w, r, workshop.ErrInvalid)
				return
			}
		}
		_, e := s.CreateVersion(r.Context(), requestUser(r), pathID(r, "id"), workshop.VersionInput{Number: r.FormValue("number"), Changelog: r.FormValue("changelog"), Channel: r.FormValue("channel"), GameVersions: strings.Fields(r.FormValue("game_versions")), Dependencies: ds})
		pageDone(w, r, "/projects/"+r.PathValue("id"), e)
	})
	protect("POST /projects/{id}/transition", func(w http.ResponseWriter, r *http.Request) {
		pageDone(w, r, "/projects/"+r.PathValue("id"), s.Transition(r.Context(), requestUser(r), pathID(r, "id"), formInt(r, "version_id"), workshop.ReviewInput{Action: r.FormValue("action"), Reason: r.FormValue("reason")}))
	})
	protect("POST /projects/{id}/icon", func(w http.ResponseWriter, r *http.Request) {
		pageDone(w, r, "/projects/"+r.PathValue("id"), s.SetIcon(r.Context(), requestUser(r), pathID(r, "id"), formInt(r, "file_id")))
	})
	protect("POST /projects/{id}/collaborators", func(w http.ResponseWriter, r *http.Request) {
		pageDone(w, r, "/projects/"+r.PathValue("id"), s.Collaborator(r.Context(), requestUser(r), pathID(r, "id"), formInt(r, "user_id"), r.FormValue("remove") == "true"))
	})
	protect("POST /projects/{id}/upload", func(w http.ResponseWriter, r *http.Request) {
		parts, ok := r.Context().Value(uploadPartsKey{}).(*multipart.Reader)
		if !ok {
			businessError(w, r, workshop.ErrInvalid)
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(s.ReceiveDeadline())
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Minute))
		fields := map[string]string{}
		total := 0
		for i := 0; i < 8; i++ {
			part, e := parts.NextPart()
			if e != nil {
				businessError(w, r, workshop.ErrInvalid)
				return
			}
			if part.FormName() == "file" && part.FileName() != "" {
				ip, e := s.ClientIP(r)
				if e != nil {
					businessError(w, r, e)
					return
				}
				vid, _ := strconv.ParseInt(fields["version_id"], 10, 64)
				size, _ := strconv.ParseInt(fields["size"], 10, 64)
				d, e := s.Upload(r.Context(), requestUser(r), workshop.UploadInput{ProjectID: pathID(r, "id"), VersionID: vid, Purpose: fields["purpose"], Filename: part.FileName(), ExpectedSHA256: fields["sha256"], IdempotencyKey: fields["key"], IP: ip, Size: size}, part)
				if e != nil {
					businessError(w, r, e)
					return
				}
				renderWorkshop(w, r, "upload", d)
				return
			}
			if part.FileName() != "" {
				businessError(w, r, workshop.ErrInvalid)
				return
			}
			b, e := io.ReadAll(io.LimitReader(part, 1025))
			if e != nil || len(b) > 1024 {
				businessError(w, r, workshop.ErrInvalid)
				return
			}
			total += len(b)
			if total > 4096 || fields[part.FormName()] != "" {
				businessError(w, r, workshop.ErrInvalid)
				return
			}
			fields[part.FormName()] = string(b)
		}
		businessError(w, r, workshop.ErrInvalid)
	})
	protect("POST /files/rescan", func(w http.ResponseWriter, r *http.Request) {
		e := s.RetryScan(r.Context(), requestUser(r), formInt(r, "file_id"))
		if e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "upload", map[string]string{"result": "扫描已重试，仍须人工审核"})
	})
	protect("GET /review", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Queue(r.Context(), requestUser(r))
		if e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "review", d)
	})
	protect("GET /appeals", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Appeals(r.Context(), requestUser(r))
		if e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "appeals", d)
	})
	protect("POST /appeals", func(w http.ResponseWriter, r *http.Request) {
		_, e := s.Appeal(r.Context(), requestUser(r), formInt(r, "review_id"), r.FormValue("reason"))
		pageDone(w, r, "/appeals", e)
	})
	protect("POST /appeals/resolve", func(w http.ResponseWriter, r *http.Request) {
		pageDone(w, r, "/appeals", s.ResolveAppeal(r.Context(), requestUser(r), formInt(r, "appeal_id"), r.FormValue("reply")))
	})
	protect("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Audit(r.Context(), requestUser(r))
		if e != nil {
			businessError(w, r, e)
			return
		}
		renderWorkshop(w, r, "admin", d)
	})
	protect("POST /admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		pageDone(w, r, "/admin", s.ManageAccount(r.Context(), requestUser(r), formInt(r, "user_id"), r.FormValue("role"), r.FormValue("status"), r.FormValue("reason")))
	})
}

func decodeDependencies(raw string, dst *[]workshop.Dependency) error {
	return json.Unmarshal([]byte(raw), dst)
}

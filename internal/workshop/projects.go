package workshop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type ProjectInput struct {
	Slug         string   `json:"slug"`
	Title        string   `json:"title"`
	Kind         string   `json:"kind"`
	Description  string   `json:"description"`
	License      string   `json:"license"`
	Tags         []string `json:"tags"`
	ContentLabel string   `json:"content_label"`
}
type Project struct {
	ID      int64 `json:"id"`
	OwnerID int64 `json:"owner_id"`
	ProjectInput
	State       string    `json:"state"`
	IconID      *int64    `json:"icon_file_id"`
	Downloads   int64     `json:"downloads"`
	RatingSum   int64     `json:"rating_sum"`
	RatingCount int64     `json:"rating_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const projectCols = `p.id,p.owner_id,p.slug,p.title,p.kind,p.description,p.license,p.tags,p.content_label,p.state,p.icon_file_id,p.downloads,p.rating_sum,p.rating_count,p.updated_at`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	e := row.Scan(&p.ID, &p.OwnerID, &p.Slug, &p.Title, &p.Kind, &p.Description, &p.License, &p.Tags, &p.ContentLabel, &p.State, &p.IconID, &p.Downloads, &p.RatingSum, &p.RatingCount, &p.UpdatedAt)
	return p, e
}
func validProject(p ProjectInput) bool {
	if !slugPattern.MatchString(p.Slug) || len(strings.TrimSpace(p.Title)) < 1 || len(p.Title) > 200 || len(p.Description) > 60000 || p.Description == "" || p.License == "" || len(p.License) > 200 || !oneOf(p.Kind, "mod", "datapack") || !oneOf(p.ContentLabel, "general", "suggestive") || len(p.Tags) > 20 {
		return false
	}
	for _, t := range p.Tags {
		if !slugPattern.MatchString(t) {
			return false
		}
	}
	return true
}
func conflict(err error) error {
	if e, ok := err.(*pgconn.PgError); ok {
		if e.Code == "23505" {
			return ErrConflict
		}
		if e.Code == "23503" || e.Code == "23514" {
			return ErrInvalid
		}
	}
	return err
}
func (s *Service) CreateProject(ctx context.Context, uid int64, in ProjectInput) (int64, error) {
	if in.Tags == nil {
		in.Tags = []string{}
	}
	if in.ContentLabel == "" {
		in.ContentLabel = "general"
	}
	if !validProject(in) {
		return 0, ErrInvalid
	}
	var id int64
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `INSERT INTO pvzce_site.projects(owner_id,slug,title,kind,description,license,tags,content_label) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, uid, in.Slug, in.Title, in.Kind, in.Description, in.License, in.Tags, in.ContentLabel).Scan(&id); e != nil {
			return conflict(e)
		}
		if e := projectRevision(ctx, tx, uid, id); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "project.create", fmt.Sprint(id), "owner creation")
	})
	return id, e
}
func member(ctx context.Context, tx pgx.Tx, pid, uid int64) (bool, error) {
	var yes bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pvzce_site.projects WHERE id=$1 AND owner_id=$2) OR EXISTS(SELECT 1 FROM pvzce_site.collaborators WHERE project_id=$1 AND user_id=$2)`, pid, uid).Scan(&yes)
	return yes, e
}
func (s *Service) EditProject(ctx context.Context, uid, pid int64, in ProjectInput) error {
	if in.Tags == nil {
		in.Tags = []string{}
	}
	if !validProject(in) {
		return ErrInvalid
	}
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		p, e := scanProject(tx.QueryRow(ctx, `SELECT `+projectCols+` FROM pvzce_site.projects p WHERE p.id=$1 FOR UPDATE`, pid))
		if e != nil {
			return e
		}
		ok, e := member(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		if !ok {
			return ErrForbidden
		}
		if oneOf(p.State, "removed", "withdrawn") {
			return ErrInvalid
		}
		if in.Kind != p.Kind {
			return ErrInvalid
		}
		_, e = tx.Exec(ctx, `UPDATE pvzce_site.projects SET slug=$2,title=$3,description=$4,license=$5,tags=$6,content_label=$7,state='draft',updated_at=now() WHERE id=$1`, pid, in.Slug, in.Title, in.Description, in.License, in.Tags, in.ContentLabel)
		if e != nil {
			return conflict(e)
		}
		if e := projectRevision(ctx, tx, uid, pid); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "project.edit", fmt.Sprint(pid), "content edited; review required")
	})
}
func (s *Service) Collaborator(ctx context.Context, uid, pid, target int64, remove bool) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		var owner int64
		if e := tx.QueryRow(ctx, `SELECT owner_id FROM pvzce_site.projects WHERE id=$1 FOR UPDATE`, pid).Scan(&owner); e != nil {
			return e
		}
		if owner != uid || target == owner {
			return ErrForbidden
		}
		var e error
		if remove {
			_, e = tx.Exec(ctx, `DELETE FROM pvzce_site.collaborators WHERE project_id=$1 AND user_id=$2`, pid, target)
		} else {
			_, e = tx.Exec(ctx, `INSERT INTO pvzce_site.collaborators VALUES($1,$2) ON CONFLICT DO NOTHING`, pid, target)
		}
		if e != nil {
			return conflict(e)
		}
		return audit(ctx, tx, uid, "collaborator.change", fmt.Sprint(pid), fmt.Sprintf("user=%d remove=%t", target, remove))
	})
}

type Dependency struct {
	VersionID int64  `json:"version_id"`
	Kind      string `json:"kind"`
}
type VersionInput struct {
	Number       string       `json:"number"`
	Changelog    string       `json:"changelog"`
	Channel      string       `json:"channel"`
	GameVersions []string     `json:"game_versions"`
	Dependencies []Dependency `json:"dependencies"`
}
type Version struct {
	ID        int64 `json:"id"`
	ProjectID int64 `json:"project_id"`
	CreatorID int64 `json:"creator_id"`
	VersionInput
	State     string    `json:"state"`
	Downloads int64     `json:"downloads"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) CreateVersion(ctx context.Context, uid, pid int64, in VersionInput) (int64, error) {
	if len(in.Number) < 1 || len(in.Number) > 100 || len(in.Changelog) > 60000 || !oneOf(in.Channel, "stable", "beta") || len(in.GameVersions) == 0 || len(in.GameVersions) > 20 || len(in.Dependencies) > 100 {
		return 0, ErrInvalid
	}
	var id int64
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM pvzce_site.projects WHERE id=$1 FOR UPDATE`, pid).Scan(&state); e != nil {
			return e
		}
		ok, e := member(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		if !ok {
			return ErrForbidden
		}
		if oneOf(state, "removed", "withdrawn") {
			return ErrInvalid
		}
		if e := tx.QueryRow(ctx, `INSERT INTO pvzce_site.versions(project_id,creator_id,number,changelog,channel) VALUES($1,$2,$3,$4,$5) RETURNING id`, pid, uid, in.Number, in.Changelog, in.Channel).Scan(&id); e != nil {
			return conflict(e)
		}
		for _, v := range in.GameVersions {
			if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.version_compatibility VALUES($1,$2)`, id, v); e != nil {
				return conflict(e)
			}
		}
		for _, d := range in.Dependencies {
			if !oneOf(d.Kind, "required", "optional", "conflict") {
				return ErrInvalid
			}
			var exists bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pvzce_site.versions v JOIN pvzce_site.projects p ON p.id=v.project_id WHERE v.id=$1 AND v.state='published' AND p.state='published')`, d.VersionID).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return ErrInvalid
			}
			if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.dependencies VALUES($1,$2,$3)`, id, d.VersionID, d.Kind); e != nil {
				return conflict(e)
			}
		}
		return audit(ctx, tx, uid, "version.create", fmt.Sprint(id), "immutable version metadata")
	})
	return id, e
}

type Filter struct {
	Search, Kind, Tag, GameVersion, Sort string
	Page, Size                           int
	Mine                                 bool
}
type ProjectPage struct {
	Items []Project `json:"items"`
	Page  int       `json:"page"`
	Size  int       `json:"size"`
	Total int64     `json:"total"`
}

func (s *Service) List(ctx context.Context, uid int64, f Filter) (ProjectPage, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 {
		f.Size = 20
	}
	if f.Size > 100 || f.Page > 10000 || len(f.Search) > 200 {
		return ProjectPage{}, ErrInvalid
	}
	if f.Sort == "" {
		f.Sort = "updated"
	}
	order := map[string]string{"updated": "p.updated_at DESC,p.id DESC", "downloads": "p.downloads DESC,p.id DESC", "rating": "CASE WHEN p.rating_count>0 THEN p.rating_sum::numeric/p.rating_count ELSE 0 END DESC,p.id DESC"}[f.Sort]
	if order == "" {
		return ProjectPage{}, ErrInvalid
	}
	where := ` WHERE (($1::boolean AND (p.owner_id=$2 OR EXISTS(SELECT 1 FROM pvzce_site.collaborators c WHERE c.project_id=p.id AND c.user_id=$2))) OR (NOT $1 AND p.state='published' AND p.content_label='general')) AND ($3='' OR to_tsvector('simple',p.title||' '||p.description) @@ plainto_tsquery('simple',$3) OR p.title ILIKE '%'||$3||'%' OR p.description ILIKE '%'||$3||'%') AND ($4='' OR p.kind=$4) AND ($5='' OR $5=ANY(p.tags)) AND ($6='' OR EXISTS(SELECT 1 FROM pvzce_site.versions v JOIN pvzce_site.version_compatibility c ON c.version_id=v.id WHERE v.project_id=p.id AND v.state='published' AND c.game_version=$6))`
	args := []any{f.Mine, uid, f.Search, f.Kind, f.Tag, f.GameVersion}
	out := ProjectPage{Items: []Project{}, Page: f.Page, Size: f.Size}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if f.Mine {
			if _, e := s.actor(ctx, tx, uid); e != nil {
				return e
			}
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM pvzce_site.projects p`+where, args...).Scan(&out.Total); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+projectCols+` FROM pvzce_site.projects p`+where+` ORDER BY `+order+` LIMIT $7 OFFSET $8`, append(args, f.Size, (f.Page-1)*f.Size)...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			p, e := scanProject(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, p)
		}
		return rows.Err()
	})
	return out, e
}

type Detail struct {
	Project       Project         `json:"project"`
	Versions      []Version       `json:"versions"`
	Files         []File          `json:"files"`
	Collaborators []int64         `json:"collaborators"`
	Reviews       json.RawMessage `json:"reviews"`
}

func (s *Service) Detail(ctx context.Context, uid, pid int64) (Detail, error) {
	out := Detail{Versions: []Version{}, Files: []File{}, Collaborators: []int64{}, Reviews: json.RawMessage(`[]`)}
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		p, e := scanProject(tx.QueryRow(ctx, `SELECT `+projectCols+` FROM pvzce_site.projects p WHERE p.id=$1`, pid))
		if e != nil {
			return e
		}
		out.Project = p
		privileged := false
		if uid != 0 {
			a, e := s.actor(ctx, tx, uid)
			if e != nil {
				return e
			}
			m, e := member(ctx, tx, pid, uid)
			if e != nil {
				return e
			}
			privileged = m || oneOf(a.Role, "reviewer", "admin")
		}
		if p.State != "published" && !privileged {
			return ErrNotFound
		}
		rows, e := tx.Query(ctx, `SELECT v.id,v.project_id,v.creator_id,v.number,v.changelog,v.channel,v.state,v.downloads,v.created_at,ARRAY(SELECT game_version FROM pvzce_site.version_compatibility WHERE version_id=v.id),COALESCE((SELECT json_agg(json_build_object('version_id',target_version_id,'kind',kind)) FROM pvzce_site.dependencies WHERE version_id=v.id),'[]'::json) FROM pvzce_site.versions v WHERE v.project_id=$1 AND ($2 OR v.state='published') ORDER BY created_at DESC,id DESC`, pid, privileged)
		if e != nil {
			return e
		}
		for rows.Next() {
			var v Version
			var deps []byte
			if e := rows.Scan(&v.ID, &v.ProjectID, &v.CreatorID, &v.Number, &v.Changelog, &v.Channel, &v.State, &v.Downloads, &v.CreatedAt, &v.GameVersions, &deps); e != nil {
				rows.Close()
				return e
			}
			if e := json.Unmarshal(deps, &v.Dependencies); e != nil {
				rows.Close()
				return e
			}
			out.Versions = append(out.Versions, v)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		files, e := tx.Query(ctx, `SELECT f.id,f.project_id,f.version_id,f.purpose,f.filename,f.size,f.sha256,f.scan_state,f.validation_state,f.metadata FROM pvzce_site.files f LEFT JOIN pvzce_site.versions v ON v.id=f.version_id WHERE f.project_id=$1 AND ($2 OR (f.scan_state='clean' AND f.validation_state='valid' AND ((f.purpose='package' AND v.state='published') OR (f.purpose='icon' AND f.id=$3)))) ORDER BY f.id`, pid, privileged, p.IconID)
		if e != nil {
			return e
		}
		for files.Next() {
			var f File
			if e := scanFile(files, &f); e != nil {
				files.Close()
				return e
			}
			out.Files = append(out.Files, f)
		}
		files.Close()
		if files.Err() != nil {
			return files.Err()
		}
		if privileged {
			if e := tx.QueryRow(ctx, `SELECT COALESCE(json_agg(r ORDER BY r.id DESC),'[]'::json) FROM (SELECT id,version_id,actor_id,action,reason,from_state,to_state,created_at FROM pvzce_site.reviews WHERE project_id=$1) r`, pid).Scan(&out.Reviews); e != nil {
				return e
			}
		}
		rows, e = tx.Query(ctx, `SELECT user_id FROM pvzce_site.collaborators WHERE project_id=$1 ORDER BY user_id`, pid)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if e := rows.Scan(&id); e != nil {
				return e
			}
			out.Collaborators = append(out.Collaborators, id)
		}
		return rows.Err()
	})
	return out, e
}

func (s *Service) SetIcon(ctx context.Context, uid, pid, fileID int64) error {
	return s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.actor(ctx, tx, uid); e != nil {
			return e
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM pvzce_site.projects WHERE id=$1 FOR UPDATE`, pid).Scan(&state); e != nil {
			return e
		}
		m, e := member(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		if !m {
			return ErrForbidden
		}
		if !oneOf(state, "draft", "rejected", "published") {
			return ErrInvalid
		}
		tag, e := tx.Exec(ctx, `UPDATE pvzce_site.projects SET icon_file_id=$2,state='draft',updated_at=now() WHERE id=$1 AND EXISTS(SELECT 1 FROM pvzce_site.files WHERE id=$2 AND project_id=$1 AND purpose='icon' AND scan_state='clean' AND validation_state='valid')`, pid, fileID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalid
		}
		if e := projectRevision(ctx, tx, uid, pid); e != nil {
			return e
		}
		return audit(ctx, tx, uid, "project.icon", fmt.Sprint(pid), fmt.Sprintf("icon=%d; review required", fileID))
	})
}

func projectRevision(ctx context.Context, tx pgx.Tx, uid, pid int64) error {
	if _, e := tx.Exec(ctx, `INSERT INTO pvzce_site.tags(name) SELECT unnest(tags) FROM pvzce_site.projects WHERE id=$1 ON CONFLICT DO NOTHING`, pid); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `INSERT INTO pvzce_site.project_revisions(project_id,actor_id,snapshot) SELECT id,$2,to_jsonb(p) FROM pvzce_site.projects p WHERE id=$1`, pid, uid)
	return e
}
func (s *Service) Revisions(ctx context.Context, uid, pid int64, before int64) (json.RawMessage, error) {
	var raw json.RawMessage
	e := s.DB.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		a, e := s.actor(ctx, tx, uid)
		if e != nil {
			return e
		}
		m, e := member(ctx, tx, pid, uid)
		if e != nil {
			return e
		}
		if !m && !oneOf(a.Role, "reviewer", "admin") {
			return ErrForbidden
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(json_agg(r),'[]'::json) FROM (SELECT * FROM pvzce_site.project_revisions WHERE project_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT 50) r`, pid, before).Scan(&raw)
	})
	return raw, e
}

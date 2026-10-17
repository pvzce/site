CREATE SCHEMA pvzce_site;
CREATE TABLE pvzce_site.accounts (
 user_id bigint PRIMARY KEY REFERENCES pvzce_auth.users(id) ON DELETE CASCADE,
 display_name text NOT NULL DEFAULT '', bio text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','restricted')),
 role text NOT NULL DEFAULT 'user' CHECK(role IN ('user','reviewer','admin')),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE pvzce_site.projects (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 owner_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 slug text NOT NULL UNIQUE, title text NOT NULL, kind text NOT NULL CHECK(kind IN ('mod','datapack')),
 description text NOT NULL, license text NOT NULL, tags text[] NOT NULL DEFAULT '{}',
 state text NOT NULL DEFAULT 'draft' CHECK(state IN ('draft','pending','published','rejected','withdrawn','removed')),
 previous_state text, content_label text NOT NULL DEFAULT 'general' CHECK(content_label IN ('general','suggestive')),
 icon_file_id bigint,
 rating_sum bigint NOT NULL DEFAULT 0, rating_count bigint NOT NULL DEFAULT 0,
 downloads bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE pvzce_site.collaborators (
 project_id bigint REFERENCES pvzce_site.projects(id), user_id bigint REFERENCES pvzce_auth.users(id),
 PRIMARY KEY(project_id,user_id)
);
CREATE TABLE pvzce_site.game_versions (
 version text PRIMARY KEY, pack_format integer NOT NULL, reference_commit text NOT NULL,
 loader text NOT NULL DEFAULT 'pvzce-fabric-fork'
);
INSERT INTO pvzce_site.game_versions VALUES('1.0.0',1,'591acf5a60455e56f26953c2854c8eeb2928aef4','pvzce-fabric-fork');
CREATE TABLE pvzce_site.versions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, project_id bigint NOT NULL REFERENCES pvzce_site.projects(id),
 creator_id bigint NOT NULL REFERENCES pvzce_auth.users(id), number text NOT NULL, changelog text NOT NULL,
 channel text NOT NULL CHECK(channel IN ('stable','beta')),
 state text NOT NULL DEFAULT 'draft' CHECK(state IN ('draft','pending','published','rejected','withdrawn','removed')),
 previous_state text, downloads bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(project_id,number)
);
CREATE TABLE pvzce_site.version_compatibility (
 version_id bigint REFERENCES pvzce_site.versions(id), game_version text REFERENCES pvzce_site.game_versions(version),
 PRIMARY KEY(version_id,game_version)
);
CREATE TABLE pvzce_site.dependencies (
 version_id bigint REFERENCES pvzce_site.versions(id), target_version_id bigint REFERENCES pvzce_site.versions(id),
 kind text NOT NULL CHECK(kind IN ('required','optional','conflict')),
 CHECK(version_id<>target_version_id), PRIMARY KEY(version_id,target_version_id)
);
CREATE TABLE pvzce_site.quota_days (
 day date NOT NULL, scope text NOT NULL, used bigint NOT NULL DEFAULT 0 CHECK(used>=0),
 reserved bigint NOT NULL DEFAULT 0 CHECK(reserved>=0), PRIMARY KEY(day,scope)
);
CREATE TABLE pvzce_site.uploads (
 id text PRIMARY KEY, user_id bigint NOT NULL REFERENCES pvzce_auth.users(id), ip_hash text NOT NULL,
 day date NOT NULL, idempotency_key text NOT NULL, fingerprint text NOT NULL,
 reserved bigint NOT NULL CHECK(reserved>0), accounted bigint NOT NULL DEFAULT 0,
 received bigint NOT NULL DEFAULT 0,
 state text NOT NULL DEFAULT 'receiving' CHECK(state IN ('receiving','complete','failed','expired')),
 expires_at timestamptz NOT NULL, file_id bigint,
 UNIQUE(user_id,idempotency_key), CHECK(accounted>=0 AND accounted<=reserved)
);
CREATE INDEX uploads_expiry ON pvzce_site.uploads(expires_at) WHERE state='receiving';
CREATE TABLE pvzce_site.files (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id bigint NOT NULL REFERENCES pvzce_site.projects(id), version_id bigint REFERENCES pvzce_site.versions(id),
 uploader_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 purpose text NOT NULL CHECK(purpose IN ('package','icon','attachment')),
 object_key text NOT NULL UNIQUE, filename text NOT NULL, size bigint NOT NULL CHECK(size>0), sha256 text NOT NULL,
 scan_state text NOT NULL CHECK(scan_state IN ('clean','infected','failed')),
 validation_state text NOT NULL CHECK(validation_state IN ('valid','invalid')),
 metadata jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(),
 CHECK((purpose='package')=(version_id IS NOT NULL))
);
CREATE UNIQUE INDEX version_single_file ON pvzce_site.files(version_id) WHERE purpose='package';
ALTER TABLE pvzce_site.projects ADD CONSTRAINT icon_file_fk FOREIGN KEY(icon_file_id) REFERENCES pvzce_site.files(id);
ALTER TABLE pvzce_site.uploads ADD CONSTRAINT upload_file_fk FOREIGN KEY(file_id) REFERENCES pvzce_site.files(id);
CREATE TABLE pvzce_site.reviews (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, project_id bigint NOT NULL REFERENCES pvzce_site.projects(id),
 version_id bigint REFERENCES pvzce_site.versions(id), actor_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 action text NOT NULL, reason text NOT NULL, from_state text NOT NULL, to_state text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE pvzce_site.appeals (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, review_id bigint NOT NULL REFERENCES pvzce_site.reviews(id),
 user_id bigint NOT NULL REFERENCES pvzce_auth.users(id), reason text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','resolved')), reply text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE pvzce_site.audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, actor_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 action text NOT NULL, resource text NOT NULL, reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE pvzce_site.downloads (
 file_id bigint REFERENCES pvzce_site.files(id), day date NOT NULL, visitor_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(file_id,day,visitor_hash)
);
CREATE INDEX projects_search ON pvzce_site.projects USING gin(to_tsvector('simple',title||' '||description));
CREATE INDEX projects_tags ON pvzce_site.projects USING gin(tags);
CREATE INDEX projects_updated ON pvzce_site.projects(updated_at DESC,id DESC) WHERE state='published';
CREATE INDEX versions_project ON pvzce_site.versions(project_id,created_at DESC);
CREATE INDEX reviews_queue ON pvzce_site.reviews(project_id,created_at DESC);
CREATE INDEX audit_created ON pvzce_site.audit(created_at DESC);
CREATE INDEX compatibility_game ON pvzce_site.version_compatibility(game_version,version_id);

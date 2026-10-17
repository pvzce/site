ALTER TABLE pvzce_site.reviews ADD COLUMN policy_version text NOT NULL DEFAULT 'content-v1';
CREATE TABLE pvzce_site.project_revisions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id bigint NOT NULL REFERENCES pvzce_site.projects(id) ON DELETE CASCADE,
 actor_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 snapshot jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX project_revisions_project ON pvzce_site.project_revisions(project_id,id DESC);
INSERT INTO pvzce_site.project_revisions(project_id,actor_id,snapshot)
 SELECT p.id,p.owner_id,to_jsonb(p) FROM pvzce_site.projects p;
CREATE TABLE pvzce_site.tags(name text PRIMARY KEY);
INSERT INTO pvzce_site.tags(name) SELECT DISTINCT unnest(tags) FROM pvzce_site.projects ON CONFLICT DO NOTHING;

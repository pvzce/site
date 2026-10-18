ALTER TABLE pvzce_site.reviews ADD COLUMN category text NOT NULL DEFAULT 'general';
ALTER TABLE pvzce_site.versions ADD COLUMN content_label text NOT NULL DEFAULT 'general' CHECK(content_label IN ('general','suggestive'));
CREATE TABLE pvzce_site.comments (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id bigint NOT NULL REFERENCES pvzce_site.projects(id),
 user_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 body text NOT NULL, revision bigint NOT NULL DEFAULT 1,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('draft','pending','published','rejected','withdrawn','removed')),
 content_label text NOT NULL DEFAULT 'general' CHECK(content_label IN ('general','suggestive')),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comments_project_page ON pvzce_site.comments(project_id,id DESC);
CREATE INDEX comments_moderation_queue ON pvzce_site.comments(state,updated_at,id);
CREATE INDEX comments_user ON pvzce_site.comments(user_id,id DESC);
CREATE TABLE pvzce_site.comment_revisions (
 comment_id bigint NOT NULL REFERENCES pvzce_site.comments(id) ON DELETE CASCADE,
 revision bigint NOT NULL, body text NOT NULL, content_label text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(comment_id,revision)
);
CREATE TABLE pvzce_site.moderation_decisions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 comment_id bigint NOT NULL, revision bigint NOT NULL,
 source text NOT NULL CHECK(source IN ('model','manual','fallback')),
 model_version text NOT NULL, policy_version text NOT NULL,
 category text NOT NULL CHECK(category IN ('general','suggestive','explicit','minor_sexual','political','uncertain')),
 recommendation text NOT NULL CHECK(recommendation IN ('approve','reject','manual')),
 reason text NOT NULL, confidence double precision NOT NULL CHECK(confidence>=0 AND confidence<=1),
 actor_id bigint REFERENCES pvzce_auth.users(id),
 overrides_id bigint REFERENCES pvzce_site.moderation_decisions(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(comment_id,revision) REFERENCES pvzce_site.comment_revisions(comment_id,revision)
);
CREATE INDEX decisions_comment_revision ON pvzce_site.moderation_decisions(comment_id,revision,id DESC);
CREATE TABLE pvzce_site.content_actions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 target_kind text NOT NULL CHECK(target_kind IN ('comment','feedback','file')),
 target_id bigint NOT NULL, revision bigint NOT NULL DEFAULT 0,
 actor_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 action text NOT NULL, reason text NOT NULL, category text NOT NULL,
 from_state text NOT NULL,to_state text NOT NULL, policy_version text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX content_actions_target ON pvzce_site.content_actions(target_kind,target_id,id DESC);
CREATE TABLE pvzce_site.ratings (
 project_id bigint NOT NULL REFERENCES pvzce_site.projects(id),
 user_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 score smallint NOT NULL CHECK(score BETWEEN 1 AND 5),
 voided boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,user_id)
);
CREATE INDEX ratings_user_projects ON pvzce_site.ratings(user_id,project_id);
CREATE TABLE pvzce_site.feedback (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 reporter_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 project_id bigint REFERENCES pvzce_site.projects(id),
 version_id bigint REFERENCES pvzce_site.versions(id),
 game_version text NOT NULL REFERENCES pvzce_site.game_versions(version),
 kind text NOT NULL CHECK(kind IN ('suggestion','bug')),
 title text NOT NULL, steps text NOT NULL, expected text NOT NULL, actual text NOT NULL,
 is_public boolean NOT NULL DEFAULT false,
 content_label text NOT NULL DEFAULT 'general' CHECK(content_label IN ('general','suggestive')),
 content_state text NOT NULL DEFAULT 'pending' CHECK(content_state IN ('draft','pending','published','rejected','withdrawn','removed')),
 state text NOT NULL DEFAULT 'new' CHECK(state IN ('new','needs_info','confirmed','in_progress','resolved','closed')),
 assignee_id bigint REFERENCES pvzce_auth.users(id),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(version_id IS NULL OR project_id IS NOT NULL)
);
CREATE INDEX feedback_reporter ON pvzce_site.feedback(reporter_id,id DESC);
CREATE INDEX feedback_queue ON pvzce_site.feedback(state,updated_at,id);
CREATE INDEX feedback_project ON pvzce_site.feedback(project_id,id DESC);
CREATE INDEX feedback_assignee ON pvzce_site.feedback(assignee_id,state,id DESC);
CREATE TABLE pvzce_site.feedback_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 feedback_id bigint NOT NULL REFERENCES pvzce_site.feedback(id) ON DELETE CASCADE,
 actor_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 from_state text NOT NULL,to_state text NOT NULL,
 reply text NOT NULL, staff boolean NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX feedback_history ON pvzce_site.feedback_events(feedback_id,id);
ALTER TABLE pvzce_site.files ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE pvzce_site.files ADD COLUMN feedback_id bigint REFERENCES pvzce_site.feedback(id);
ALTER TABLE pvzce_site.files ADD COLUMN is_private boolean NOT NULL DEFAULT true;
ALTER TABLE pvzce_site.files ADD COLUMN content_label text NOT NULL DEFAULT 'general' CHECK(content_label IN ('general','suggestive'));
ALTER TABLE pvzce_site.files ADD COLUMN content_state text NOT NULL DEFAULT 'pending' CHECK(content_state IN ('draft','pending','published','rejected','withdrawn','removed'));
UPDATE pvzce_site.files SET content_state='published' WHERE purpose IN ('package','icon');
ALTER TABLE pvzce_site.files ADD COLUMN safety_quarantined boolean NOT NULL DEFAULT false;
ALTER TABLE pvzce_site.files ADD CONSTRAINT file_resource_scope CHECK(
 (project_id IS NOT NULL AND feedback_id IS NULL) OR
 (project_id IS NULL AND feedback_id IS NOT NULL AND purpose='attachment' AND version_id IS NULL));
CREATE INDEX files_feedback ON pvzce_site.files(feedback_id,id);
CREATE INDEX files_uploader ON pvzce_site.files(uploader_id,created_at,id);
CREATE TABLE pvzce_site.cases (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 reporter_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 target_kind text NOT NULL CHECK(target_kind IN ('project','version','file','comment','account')),
 target_id bigint NOT NULL, reason text NOT NULL, evidence text NOT NULL,
 state text NOT NULL DEFAULT 'open' CHECK(state IN ('open','in_review','resolved','rejected')),
 assignee_id bigint REFERENCES pvzce_auth.users(id),
 conclusion text NOT NULL DEFAULT '', action text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cases_reporter ON pvzce_site.cases(reporter_id,id DESC);
CREATE INDEX cases_queue ON pvzce_site.cases(state,id);
CREATE INDEX cases_target ON pvzce_site.cases(target_kind,target_id,id DESC);
CREATE TABLE pvzce_site.case_evidence (
 case_id bigint NOT NULL REFERENCES pvzce_site.cases(id) ON DELETE CASCADE,
 file_id bigint NOT NULL REFERENCES pvzce_site.files(id),PRIMARY KEY(case_id,file_id)
);
CREATE TABLE pvzce_site.case_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 case_id bigint NOT NULL REFERENCES pvzce_site.cases(id) ON DELETE CASCADE,
 actor_id bigint NOT NULL REFERENCES pvzce_auth.users(id),action text NOT NULL,reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX case_events_case ON pvzce_site.case_events(case_id,id);
CREATE TABLE pvzce_site.case_appeals (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 case_id bigint NOT NULL REFERENCES pvzce_site.cases(id),
 user_id bigint NOT NULL REFERENCES pvzce_auth.users(id),reason text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','resolved')),
 reply text NOT NULL DEFAULT '', reviewer_id bigint REFERENCES pvzce_auth.users(id),
 outcome text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),resolved_at timestamptz
);
CREATE UNIQUE INDEX one_pending_case_appeal ON pvzce_site.case_appeals(case_id,user_id) WHERE state='pending';
CREATE INDEX case_appeals_queue ON pvzce_site.case_appeals(state,id);
CREATE TABLE pvzce_site.notifications (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 resource text NOT NULL,message text NOT NULL,read_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notifications_user_page ON pvzce_site.notifications(user_id,id DESC);
CREATE INDEX audit_actor_action_time ON pvzce_site.audit(actor_id,action,created_at DESC);
CREATE INDEX uploads_user_day ON pvzce_site.uploads(user_id,day,state);
CREATE INDEX files_content_queue ON pvzce_site.files(content_state,id) WHERE purpose='attachment';

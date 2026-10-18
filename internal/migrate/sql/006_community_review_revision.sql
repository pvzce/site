ALTER TABLE pvzce_site.feedback ADD COLUMN revision bigint NOT NULL DEFAULT 1;
CREATE TABLE pvzce_site.content_appeals (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 action_id bigint NOT NULL REFERENCES pvzce_site.content_actions(id),
 user_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
 reason text NOT NULL,state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','resolved')),
 reply text NOT NULL DEFAULT '',reviewer_id bigint REFERENCES pvzce_auth.users(id),
 created_at timestamptz NOT NULL DEFAULT now(),resolved_at timestamptz
);
CREATE UNIQUE INDEX content_appeal_pending ON pvzce_site.content_appeals(action_id,user_id) WHERE state='pending';
CREATE INDEX content_appeal_queue ON pvzce_site.content_appeals(state,id);
CREATE INDEX comments_statistics ON pvzce_site.comments(project_id,state);
CREATE INDEX feedback_statistics ON pvzce_site.feedback(project_id,state,content_state);
CREATE INDEX ratings_distribution ON pvzce_site.ratings(project_id,score) WHERE NOT voided;

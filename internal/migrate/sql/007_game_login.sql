CREATE SCHEMA pvzce_game;
CREATE TABLE pvzce_game.login_codes (
 code_hash bytea PRIMARY KEY CHECK(octet_length(code_hash)=32),
 source_hash bytea NOT NULL REFERENCES pvzce_auth.sessions(token_hash) ON DELETE CASCADE,
 user_id bigint NOT NULL REFERENCES pvzce_auth.users(id) ON DELETE CASCADE,
 origin text NOT NULL,
 protocol integer NOT NULL CHECK(protocol=1),
 game_id text NOT NULL CHECK(game_id='pvzce'),
 game_version text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL,
 CHECK(expires_at > created_at AND expires_at <= created_at+interval '90 seconds')
);
CREATE INDEX game_codes_expiry ON pvzce_game.login_codes(expires_at);
CREATE INDEX game_codes_source ON pvzce_game.login_codes(source_hash);
CREATE INDEX game_codes_user ON pvzce_game.login_codes(user_id);
CREATE TABLE pvzce_game.sessions (
 token_hash bytea PRIMARY KEY CHECK(octet_length(token_hash)=32),
 source_hash bytea NOT NULL REFERENCES pvzce_auth.sessions(token_hash) ON DELETE CASCADE,
 user_id bigint NOT NULL REFERENCES pvzce_auth.users(id) ON DELETE CASCADE,
 origin text NOT NULL,
 protocol integer NOT NULL CHECK(protocol=1),
 game_id text NOT NULL CHECK(game_id='pvzce'),
 game_version text NOT NULL,
 scope text NOT NULL CHECK(scope='game:session'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL,
 CHECK(expires_at > created_at AND expires_at <= created_at+interval '1 hour')
);
CREATE INDEX game_sessions_expiry ON pvzce_game.sessions(expires_at);
CREATE INDEX game_sessions_source ON pvzce_game.sessions(source_hash);
CREATE INDEX game_sessions_user ON pvzce_game.sessions(user_id);
CREATE TABLE pvzce_game.rate_limits (
 key_hash bytea PRIMARY KEY CHECK(octet_length(key_hash)=32),
 window_start timestamptz NOT NULL,
 attempts integer NOT NULL CHECK(attempts>0)
);
CREATE INDEX game_rate_expiry ON pvzce_game.rate_limits(window_start);

-- A restriction permanently revokes existing grants, including after unrestriction.
-- Account row locks serialize this trigger with issuance/exchange/session checks.
CREATE FUNCTION pvzce_game.restrict_account() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status <> 'active' THEN
  DELETE FROM pvzce_game.login_codes WHERE user_id=NEW.user_id;
  DELETE FROM pvzce_game.sessions WHERE user_id=NEW.user_id;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER revoke_game_on_restriction AFTER UPDATE OF status ON pvzce_site.accounts
 FOR EACH ROW EXECUTE FUNCTION pvzce_game.restrict_account();

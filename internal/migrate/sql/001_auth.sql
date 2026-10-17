CREATE SCHEMA IF NOT EXISTS pvzce_auth;
CREATE TABLE IF NOT EXISTS pvzce_auth.users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issuer text NOT NULL,
    subject text NOT NULL,
    username text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT '',
    picture text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject)
);
CREATE TABLE IF NOT EXISTS pvzce_auth.login_states (
    state_hash bytea PRIMARY KEY CHECK (octet_length(state_hash) = 32),
    binding_hash bytea NOT NULL CHECK (octet_length(binding_hash) = 32),
    nonce text NOT NULL,
    verifier text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS login_states_expiry ON pvzce_auth.login_states (expires_at);
CREATE TABLE IF NOT EXISTS pvzce_auth.sessions (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id bigint NOT NULL REFERENCES pvzce_auth.users(id),
    csrf_token text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sessions_expiry ON pvzce_auth.sessions (expires_at);

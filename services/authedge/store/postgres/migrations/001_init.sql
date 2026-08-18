-- Auth edge schema: who may sign in, and who is currently signed in.
--
-- This is the whole of it. The auth edge resolves identity and forwards
-- everything else, so the two tables here are all the durable state it has.
-- Orders, accounts, ledgers and audit belong to the services that own them and
-- are reached over HTTP, not by sharing a database.
--
-- Table names are unqualified so the deployment picks the schema through the
-- connection's search_path, the same way trading and the gateway do.
--
-- No credential material is stored here beyond the bcrypt hash a password check
-- requires. Exchange credentials never reach this process; they live behind the
-- gateway.

CREATE TABLE schema_migrations (
    version integer PRIMARY KEY,
    checksum text NOT NULL UNIQUE,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE identities (
    principal_id text PRIMARY KEY,
    username text NOT NULL,
    password_hash text NOT NULL,
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (username)
);

-- Only digests are stored. A leaked table must not yield a usable session
-- cookie or a CSRF token, so the raw values exist solely in the browser.
CREATE TABLE sessions (
    token_digest text PRIMARY KEY,
    principal_id text NOT NULL REFERENCES identities(principal_id),
    username text NOT NULL,
    csrf_digest text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

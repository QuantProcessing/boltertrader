CREATE TABLE platform_identities (
    principal_id TEXT PRIMARY KEY,
    mode TEXT NOT NULL CHECK (mode IN ('test', 'live')),
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    disabled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (mode, username)
);

CREATE TABLE platform_sessions (
    token_digest TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL REFERENCES platform_identities(principal_id),
    mode TEXT NOT NULL CHECK (mode IN ('test', 'live')),
    username TEXT NOT NULL,
    csrf_digest TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX platform_sessions_expires_at_idx ON platform_sessions (expires_at);

CREATE TABLE platform_accounts (
    account_id TEXT PRIMARY KEY,
    venue TEXT NOT NULL,
    venue_environment TEXT NOT NULL CHECK (venue_environment IN ('demo', 'testnet', 'live')),
    display_name TEXT NOT NULL,
    product_ids JSONB NOT NULL,
    secret_ref TEXT NOT NULL,
    desired_state_revision BIGINT NOT NULL CHECK (desired_state_revision > 0),
    runtime_generation BIGINT NOT NULL CHECK (runtime_generation > 0),
    credential_version BIGINT NOT NULL CHECK (credential_version > 0),
    state TEXT NOT NULL CHECK (state IN ('creating','active','rotating','draining','drained','disabled','removing','removed')),
    rotation_fence JSONB,
    last_validated_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (jsonb_typeof(product_ids) = 'array'),
    CHECK (rotation_fence IS NULL OR jsonb_typeof(rotation_fence) = 'object')
);

CREATE TABLE platform_audit (
    audit_id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    outcome TEXT NOT NULL,
    request_id TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX platform_audit_resource_idx ON platform_audit (resource_id, occurred_at DESC);

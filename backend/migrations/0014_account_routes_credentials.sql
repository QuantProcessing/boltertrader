ALTER TABLE platform_accounts ADD COLUMN mode TEXT;
ALTER TABLE platform_accounts ADD COLUMN native_environment TEXT;
ALTER TABLE platform_accounts ADD COLUMN previous_secret_ref TEXT;
ALTER TABLE platform_accounts ALTER COLUMN secret_ref DROP NOT NULL;

UPDATE platform_accounts
SET mode = CASE WHEN venue_environment = 'live' THEN 'live' ELSE 'test' END,
    native_environment = venue_environment;

ALTER TABLE platform_accounts ALTER COLUMN mode SET NOT NULL;
ALTER TABLE platform_accounts ALTER COLUMN native_environment SET NOT NULL;
ALTER TABLE platform_accounts ADD CONSTRAINT platform_accounts_mode_check CHECK (mode IN ('test','live'));
ALTER TABLE platform_accounts ADD CONSTRAINT platform_accounts_native_environment_check CHECK (native_environment IN ('demo','testnet','live'));

CREATE TABLE platform_account_routes (
    account_id TEXT NOT NULL REFERENCES platform_accounts(account_id),
    route_id TEXT NOT NULL,
    product TEXT NOT NULL CHECK (product IN ('spot','perp')),
    settlement_asset TEXT NOT NULL DEFAULT '',
    route_profile_id TEXT NOT NULL,
    requested_state TEXT NOT NULL CHECK (requested_state IN ('active','disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (account_id, route_id)
);

CREATE TABLE platform_credential_operations (
    operation_id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL,
    account_id TEXT NOT NULL REFERENCES platform_accounts(account_id),
    mode TEXT NOT NULL CHECK (mode IN ('test','live')),
    kind TEXT NOT NULL CHECK (kind IN ('create','rotate','retire')),
    state TEXT NOT NULL CHECK (state IN ('awaiting_material','stored','applying','active','cleanup_pending','abandoned','failed')),
    idempotency_key_digest TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    target_desired_state_revision BIGINT NOT NULL CHECK (target_desired_state_revision > 0),
    target_runtime_generation BIGINT NOT NULL CHECK (target_runtime_generation > 0),
    target_credential_version BIGINT NOT NULL CHECK (target_credential_version > 0),
    secret_ref TEXT,
    stable_error_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (mode, principal_id, kind, idempotency_key_digest)
);

CREATE INDEX platform_credential_operations_recovery
ON platform_credential_operations (state, updated_at, operation_id)
WHERE state IN ('awaiting_material','stored','applying','cleanup_pending');

-- Consolidates the control-plane account row into the gateway account row.
-- Before this migration two tables described one account: gateway_desired_accounts
-- held the data-plane fences while platform_accounts held the lifecycle metadata,
-- and both independently derived desired_state_revision / runtime_generation /
-- credential_version. gateway_desired_accounts becomes the single authority.

ALTER TABLE gateway_desired_accounts ALTER COLUMN secret_ref DROP NOT NULL;

ALTER TABLE gateway_desired_accounts
    ADD COLUMN display_name text NOT NULL DEFAULT '',
    ADD COLUMN mode text NOT NULL DEFAULT 'test',
    ADD COLUMN product_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN last_validated_at timestamptz;

ALTER TABLE gateway_desired_accounts
    ADD CONSTRAINT gateway_desired_accounts_mode_check
    CHECK (mode IN ('test', 'live'));

-- routeprofile only mints live profiles for the live native environment, so the
-- mode default can never silently mislabel a live account: it fails loudly.
ALTER TABLE gateway_desired_accounts
    ADD CONSTRAINT gateway_desired_accounts_mode_environment_check
    CHECK ((mode = 'live') = (venue_environment = 'live'));

ALTER TABLE gateway_desired_accounts
    ADD CONSTRAINT gateway_desired_accounts_product_ids_check
    CHECK (jsonb_typeof(product_ids) = 'array');

-- A credential-free account row is only legitimate while the create or rotate
-- operation that will bind the reference is still in flight.
ALTER TABLE gateway_desired_accounts
    ADD CONSTRAINT gateway_desired_accounts_secret_ref_check
    CHECK (secret_ref IS NOT NULL OR requested_state = 'creating');

CREATE TABLE gateway_account_routes (
    account_id text NOT NULL REFERENCES gateway_desired_accounts(account_id) ON DELETE CASCADE,
    route_id text NOT NULL,
    product text NOT NULL,
    settlement_asset text NOT NULL DEFAULT '',
    route_profile_id text NOT NULL,
    requested_state text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, route_id),
    CHECK (product IN ('spot', 'perp')),
    CHECK (requested_state IN ('active', 'disabled'))
);

CREATE TABLE gateway_credential_operations (
    operation_id text PRIMARY KEY,
    principal_id text NOT NULL,
    account_id text NOT NULL REFERENCES gateway_desired_accounts(account_id) ON DELETE CASCADE,
    mode text NOT NULL,
    kind text NOT NULL,
    state text NOT NULL,
    idempotency_key_digest text NOT NULL,
    request_digest text NOT NULL,
    target_desired_state_revision bigint NOT NULL,
    target_runtime_generation bigint NOT NULL,
    target_credential_version bigint NOT NULL,
    rotation_cleanup_tombstone_revision bigint,
    rotation_cleanup_completed_at timestamptz,
    secret_ref text,
    stable_error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (mode, principal_id, kind, idempotency_key_digest),
    CHECK (mode IN ('test', 'live')),
    CHECK (kind IN ('create', 'rotate', 'retire')),
    CHECK (state IN ('awaiting_material', 'stored', 'applying', 'active', 'cleanup_pending', 'abandoned', 'failed')),
    CHECK (target_desired_state_revision > 0),
    CHECK (target_runtime_generation > 0),
    CHECK (target_credential_version > 0),
    -- Rotation cleanup proof is only meaningful for a rotation, and only when it
    -- names the exact revision whose predecessor reference was tombstoned.
    CHECK (
        (
            rotation_cleanup_tombstone_revision IS NULL
            AND rotation_cleanup_completed_at IS NULL
        )
        OR (
            kind = 'rotate'
            AND rotation_cleanup_tombstone_revision = target_desired_state_revision
            AND rotation_cleanup_completed_at IS NOT NULL
        )
    )
);

CREATE INDEX gateway_credential_operations_recovery
ON gateway_credential_operations (state, updated_at, operation_id)
WHERE state IN ('awaiting_material', 'stored', 'applying', 'cleanup_pending');

-- At most one in-flight credential operation per account, so a crashed operation
-- can be recovered without ambiguity about which one owns the fences.
CREATE UNIQUE INDEX gateway_credential_operations_one_recoverable_per_account
ON gateway_credential_operations (account_id)
WHERE state IN ('awaiting_material', 'stored', 'applying', 'cleanup_pending');

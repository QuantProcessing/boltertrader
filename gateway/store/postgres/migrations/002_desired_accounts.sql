CREATE TABLE gateway_desired_accounts (
    account_id text PRIMARY KEY,
    venue text NOT NULL,
    venue_environment text NOT NULL,
    secret_ref text NOT NULL,
    previous_secret_ref text,
    desired_state_revision bigint NOT NULL,
    runtime_generation bigint NOT NULL,
    credential_version bigint NOT NULL,
    requested_state text NOT NULL,
    rotation_fence_id text,
    rotation_fence_state text,
    rotation_from_credential_version bigint,
    rotation_to_credential_version bigint,
    rotation_from_runtime_generation bigint,
    rotation_to_runtime_generation bigint,
    rotation_armed_at timestamptz,
    rotation_released_at timestamptz,
    rotation_blocked_reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (venue_environment = 'live'),
    CHECK (desired_state_revision > 0),
    CHECK (runtime_generation > 0),
    CHECK (credential_version > 0),
    CHECK (requested_state IN ('creating', 'active', 'rotating', 'draining', 'drained', 'disabled', 'removing', 'removed')),
    CHECK (rotation_fence_state IS NULL OR rotation_fence_state IN ('armed', 'replacement_ready', 'released', 'expired', 'blocked')),
    CHECK ((rotation_fence_id IS NULL) = (rotation_fence_state IS NULL)),
	CHECK (rotation_fence_state <> 'released' OR rotation_released_at IS NOT NULL),
    CHECK (rotation_fence_id IS NULL OR (
        rotation_from_credential_version > 0 AND
        rotation_to_credential_version > rotation_from_credential_version AND
        rotation_from_runtime_generation > 0 AND
        rotation_to_runtime_generation > rotation_from_runtime_generation AND
        rotation_armed_at IS NOT NULL
    ))
);

CREATE INDEX gateway_desired_accounts_recovery
ON gateway_desired_accounts (account_id)
WHERE requested_state IN ('creating', 'active', 'rotating', 'draining');

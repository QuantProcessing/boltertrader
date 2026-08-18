CREATE TABLE gateway_runtime_snapshots (
    snapshot_id uuid PRIMARY KEY,
    account_id text NOT NULL,
    operation_id uuid NOT NULL UNIQUE,
    request_digest text NOT NULL,
    venue text NOT NULL,
    native_environment text NOT NULL CHECK (native_environment = 'live'),
    secret_ref text NOT NULL,
    desired_state_revision bigint NOT NULL CHECK (desired_state_revision > 0),
    runtime_generation bigint NOT NULL CHECK (runtime_generation > 0),
    credential_version bigint NOT NULL CHECK (credential_version > 0),
    requested_state text NOT NULL CHECK (requested_state IN ('creating', 'active', 'rotating', 'draining', 'drained', 'disabled', 'removing', 'removed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, desired_state_revision, runtime_generation, credential_version)
);

CREATE TABLE gateway_runtime_snapshot_routes (
    snapshot_id uuid NOT NULL REFERENCES gateway_runtime_snapshots(snapshot_id),
    route_id text NOT NULL,
    product text NOT NULL CHECK (product IN ('spot', 'perp')),
    settlement_asset text NOT NULL DEFAULT '',
    route_profile_id text NOT NULL,
    requested_state text NOT NULL CHECK (requested_state IN ('active', 'disabled')),
    PRIMARY KEY (snapshot_id, route_id)
);

CREATE TABLE gateway_account_runtime_heads (
    account_id text PRIMARY KEY,
    active_snapshot_id uuid REFERENCES gateway_runtime_snapshots(snapshot_id),
    pending_snapshot_id uuid REFERENCES gateway_runtime_snapshots(snapshot_id),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (active_snapshot_id IS NULL OR active_snapshot_id <> pending_snapshot_id)
);

CREATE TABLE gateway_desired_operations (
    operation_id uuid PRIMARY KEY REFERENCES gateway_runtime_snapshots(operation_id),
    snapshot_id uuid NOT NULL UNIQUE REFERENCES gateway_runtime_snapshots(snapshot_id),
    request_digest text NOT NULL,
    state text NOT NULL CHECK (state IN ('prepared', 'candidate_ready', 'candidate_failed', 'applied')),
    applied_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (state <> 'applied' OR applied_at IS NOT NULL)
);

CREATE TABLE gateway_candidate_observations (
    operation_id uuid NOT NULL REFERENCES gateway_desired_operations(operation_id),
    observation_revision bigint NOT NULL CHECK (observation_revision > 0),
    route_id text NOT NULL,
    state text NOT NULL CHECK (state IN ('building', 'ready', 'failed')),
    reason_code text,
    observed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (operation_id, observation_revision, route_id)
);

CREATE TABLE gateway_retiring_generations (
    account_id text NOT NULL,
    snapshot_id uuid NOT NULL UNIQUE REFERENCES gateway_runtime_snapshots(snapshot_id),
    state text NOT NULL CHECK (state IN ('reconciling', 'teardown_pending', 'retired')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, snapshot_id)
);

CREATE TABLE gateway_retired_secret_refs (
    namespace text NOT NULL,
    secret_ref text NOT NULL,
    account_id text NOT NULL,
    tombstone_revision bigint NOT NULL CHECK (tombstone_revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, secret_ref)
);

CREATE INDEX gateway_runtime_snapshots_recovery ON gateway_runtime_snapshots (account_id, runtime_generation DESC);
CREATE INDEX gateway_desired_operations_recovery ON gateway_desired_operations (state, updated_at) WHERE state <> 'applied';

CREATE TABLE gateway_account_retirements (
    operation_id UUID PRIMARY KEY,
    account_id TEXT NOT NULL UNIQUE,
    request_digest TEXT NOT NULL,
    expected_desired_state_revision BIGINT NOT NULL CHECK (expected_desired_state_revision > 0),
    expected_runtime_generation BIGINT NOT NULL CHECK (expected_runtime_generation > 0),
    expected_credential_version BIGINT NOT NULL CHECK (expected_credential_version > 0),
    tombstone_revision BIGINT NOT NULL CHECK (tombstone_revision = expected_desired_state_revision + 1),
    namespace TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('tombstoned')),
    completed_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX gateway_account_retirements_recovery
    ON gateway_account_retirements (state, completed_at, account_id);

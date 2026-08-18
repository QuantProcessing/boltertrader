ALTER TABLE gateway_desired_operations
    ADD CONSTRAINT gateway_desired_operations_finalization_identity
    UNIQUE (operation_id, snapshot_id);

ALTER TABLE gateway_runtime_snapshots
    ADD CONSTRAINT gateway_runtime_snapshots_finalization_fence
    UNIQUE (snapshot_id, account_id, desired_state_revision, runtime_generation, credential_version),
    ADD CONSTRAINT gateway_runtime_snapshots_finalization_reference
    UNIQUE (snapshot_id, account_id, desired_state_revision, runtime_generation, credential_version, secret_ref);

ALTER TABLE gateway_retired_secret_refs
    ADD CONSTRAINT gateway_retired_secret_refs_finalization_account
    UNIQUE (namespace, secret_ref, account_id);

CREATE TABLE gateway_rotation_finalizations (
    operation_id UUID PRIMARY KEY REFERENCES gateway_desired_operations(operation_id),
    account_id TEXT NOT NULL REFERENCES gateway_account_runtime_heads(account_id),
    request_digest TEXT NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    active_snapshot_id UUID NOT NULL REFERENCES gateway_runtime_snapshots(snapshot_id),
    retiring_snapshot_id UUID NOT NULL UNIQUE REFERENCES gateway_retiring_generations(snapshot_id),
    expected_active_desired_state_revision BIGINT NOT NULL CHECK (expected_active_desired_state_revision > 0),
    expected_active_runtime_generation BIGINT NOT NULL CHECK (expected_active_runtime_generation > 0),
    expected_active_credential_version BIGINT NOT NULL CHECK (expected_active_credential_version > 0),
    expected_retiring_desired_state_revision BIGINT NOT NULL CHECK (expected_retiring_desired_state_revision > 0),
    expected_retiring_runtime_generation BIGINT NOT NULL CHECK (expected_retiring_runtime_generation > 0),
    expected_retiring_credential_version BIGINT NOT NULL CHECK (expected_retiring_credential_version > 0),
    retired_secret_ref TEXT NOT NULL,
    tombstone_revision BIGINT NOT NULL CHECK (tombstone_revision = expected_active_desired_state_revision),
    namespace TEXT NOT NULL,
    key_version TEXT NOT NULL CHECK (key_version = 'v1'),
    retiring_state TEXT NOT NULL CHECK (retiring_state IN ('retired')),
    secret_reference_state TEXT NOT NULL CHECK (secret_reference_state IN ('tombstoned')),
    completed_at TIMESTAMPTZ NOT NULL,
    CHECK (expected_active_desired_state_revision = expected_retiring_desired_state_revision + 1),
    CHECK (expected_active_runtime_generation = expected_retiring_runtime_generation + 1),
    CHECK (expected_active_credential_version = expected_retiring_credential_version + 1),
    FOREIGN KEY (operation_id, active_snapshot_id)
        REFERENCES gateway_desired_operations(operation_id, snapshot_id),
    FOREIGN KEY (active_snapshot_id, account_id, expected_active_desired_state_revision, expected_active_runtime_generation, expected_active_credential_version)
        REFERENCES gateway_runtime_snapshots(snapshot_id, account_id, desired_state_revision, runtime_generation, credential_version),
    FOREIGN KEY (account_id, retiring_snapshot_id)
        REFERENCES gateway_retiring_generations(account_id, snapshot_id),
    FOREIGN KEY (retiring_snapshot_id, account_id, expected_retiring_desired_state_revision, expected_retiring_runtime_generation, expected_retiring_credential_version, retired_secret_ref)
        REFERENCES gateway_runtime_snapshots(snapshot_id, account_id, desired_state_revision, runtime_generation, credential_version, secret_ref),
    FOREIGN KEY (namespace, retired_secret_ref, account_id)
        REFERENCES gateway_retired_secret_refs(namespace, secret_ref, account_id)
);

CREATE INDEX gateway_rotation_finalizations_recovery
    ON gateway_rotation_finalizations (account_id, completed_at, operation_id);

ALTER TABLE platform_credential_operations
    ADD COLUMN rotation_cleanup_tombstone_revision BIGINT,
    ADD COLUMN rotation_cleanup_completed_at TIMESTAMPTZ;

ALTER TABLE platform_credential_operations
    ADD CONSTRAINT platform_credential_operations_rotation_cleanup_proof_check
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
    );

CREATE UNIQUE INDEX platform_credential_operations_one_recoverable_per_account
    ON platform_credential_operations (account_id)
    WHERE state IN ('awaiting_material','stored','applying','cleanup_pending');

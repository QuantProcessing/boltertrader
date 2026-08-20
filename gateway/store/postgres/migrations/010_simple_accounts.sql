-- Accounts are immutable credential registrations. Replacing credentials now
-- means deleting the account and creating a new one; the control plane no
-- longer exposes or persists desired revisions, runtime generations, credential
-- versions, or rotation state. A single opaque runtime_id remains as the
-- internal stale-connection/write fence.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM gateway_desired_accounts
        WHERE requested_state IN ('rotating', 'draining', 'drained')
           OR previous_secret_ref IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'ACCOUNT_SIMPLIFICATION_BLOCKED_BY_LEGACY_TRANSITION';
    END IF;
END $$;

DROP TABLE IF EXISTS gateway_rotation_finalizations;

ALTER TABLE gateway_runtime_snapshots
    DROP CONSTRAINT IF EXISTS gateway_runtime_snapshots_finalization_fence,
    DROP CONSTRAINT IF EXISTS gateway_runtime_snapshots_finalization_reference;

ALTER TABLE gateway_runtime_snapshots ADD COLUMN runtime_id uuid;
UPDATE gateway_runtime_snapshots SET runtime_id = snapshot_id WHERE runtime_id IS NULL;
ALTER TABLE gateway_runtime_snapshots ALTER COLUMN runtime_id SET NOT NULL;

ALTER TABLE gateway_desired_accounts ADD COLUMN runtime_id uuid;
UPDATE gateway_desired_accounts account
SET runtime_id = COALESCE(
    (
        SELECT snapshot.runtime_id
        FROM gateway_account_runtime_heads head
        JOIN gateway_runtime_snapshots snapshot ON snapshot.snapshot_id = head.active_snapshot_id
        WHERE head.account_id = account.account_id
    ),
    md5(account.account_id)::uuid
)
WHERE runtime_id IS NULL;
ALTER TABLE gateway_desired_accounts ALTER COLUMN runtime_id SET NOT NULL;
ALTER TABLE gateway_desired_accounts ADD CONSTRAINT gateway_desired_accounts_runtime_id_key UNIQUE (runtime_id);

ALTER TABLE gateway_order_handoffs ADD COLUMN runtime_id uuid;
UPDATE gateway_order_handoffs handoff
SET runtime_id = COALESCE(
    (
        SELECT snapshot.runtime_id
        FROM gateway_runtime_snapshots snapshot
        WHERE snapshot.account_id = handoff.account_id
          AND snapshot.desired_state_revision = handoff.desired_state_revision
          AND snapshot.runtime_generation = handoff.runtime_generation
          AND snapshot.credential_version = handoff.credential_version
        LIMIT 1
    ),
    (SELECT account.runtime_id FROM gateway_desired_accounts account WHERE account.account_id = handoff.account_id)
)
WHERE runtime_id IS NULL;
ALTER TABLE gateway_order_handoffs ALTER COLUMN runtime_id SET NOT NULL;

ALTER TABLE gateway_account_retirements ADD COLUMN runtime_id uuid;
UPDATE gateway_account_retirements retirement
SET runtime_id = COALESCE(
    (
        SELECT snapshot.runtime_id
        FROM gateway_runtime_snapshots snapshot
        WHERE snapshot.account_id = retirement.account_id
          AND snapshot.desired_state_revision = retirement.expected_desired_state_revision
          AND snapshot.runtime_generation = retirement.expected_runtime_generation
          AND snapshot.credential_version = retirement.expected_credential_version
        LIMIT 1
    ),
    (SELECT account.runtime_id FROM gateway_desired_accounts account WHERE account.account_id = retirement.account_id)
)
WHERE runtime_id IS NULL;
ALTER TABLE gateway_account_retirements ALTER COLUMN runtime_id SET NOT NULL;

DROP INDEX IF EXISTS gateway_order_handoffs_unresolved_generation;
CREATE INDEX gateway_order_handoffs_unresolved_runtime
    ON gateway_order_handoffs (account_id, runtime_id)
    WHERE state = 'started';

DROP INDEX IF EXISTS gateway_runtime_snapshots_recovery;
CREATE INDEX gateway_runtime_snapshots_recovery
    ON gateway_runtime_snapshots (account_id, created_at DESC);

ALTER TABLE gateway_runtime_snapshots
    DROP CONSTRAINT IF EXISTS gateway_runtime_snapshots_account_id_desired_state_revision_runtime_generation_credential_version_key,
    DROP COLUMN desired_state_revision,
    DROP COLUMN runtime_generation,
    DROP COLUMN credential_version;

ALTER TABLE gateway_order_handoffs
    DROP COLUMN desired_state_revision,
    DROP COLUMN runtime_generation,
    DROP COLUMN credential_version;

ALTER TABLE gateway_account_retirements
    DROP COLUMN expected_desired_state_revision,
    DROP COLUMN expected_runtime_generation,
    DROP COLUMN expected_credential_version,
    DROP COLUMN tombstone_revision;

DELETE FROM gateway_credential_operations WHERE kind = 'rotate';
DROP INDEX IF EXISTS gateway_credential_operations_one_recoverable_per_account;
ALTER TABLE gateway_credential_operations
    DROP CONSTRAINT IF EXISTS gateway_credential_operations_kind_check,
    DROP CONSTRAINT IF EXISTS gateway_credential_operations_target_desired_state_revision_check,
    DROP CONSTRAINT IF EXISTS gateway_credential_operations_target_runtime_generation_check,
    DROP CONSTRAINT IF EXISTS gateway_credential_operations_target_credential_version_check,
    DROP CONSTRAINT IF EXISTS gateway_credential_operations_check,
    DROP COLUMN target_desired_state_revision,
    DROP COLUMN target_runtime_generation,
    DROP COLUMN target_credential_version,
    DROP COLUMN rotation_cleanup_tombstone_revision,
    DROP COLUMN rotation_cleanup_completed_at,
    ADD CONSTRAINT gateway_credential_operations_kind_check CHECK (kind IN ('create', 'retire'));
CREATE UNIQUE INDEX gateway_credential_operations_one_recoverable_per_account
    ON gateway_credential_operations (account_id)
    WHERE state IN ('awaiting_material', 'stored', 'applying');

DROP INDEX IF EXISTS gateway_desired_accounts_recovery;
ALTER TABLE gateway_desired_accounts
    DROP CONSTRAINT IF EXISTS gateway_desired_accounts_desired_state_revision_check,
    DROP CONSTRAINT IF EXISTS gateway_desired_accounts_runtime_generation_check,
    DROP CONSTRAINT IF EXISTS gateway_desired_accounts_credential_version_check,
    DROP CONSTRAINT IF EXISTS gateway_desired_accounts_requested_state_check,
    DROP CONSTRAINT IF EXISTS gateway_desired_accounts_rotation_fence_state_check,
    DROP CONSTRAINT IF EXISTS gateway_desired_accounts_check,
    DROP CONSTRAINT IF EXISTS gateway_desired_accounts_check1,
    DROP CONSTRAINT IF EXISTS gateway_desired_accounts_check2,
    DROP CONSTRAINT IF EXISTS gateway_desired_accounts_secret_ref_check,
    DROP COLUMN previous_secret_ref,
    DROP COLUMN desired_state_revision,
    DROP COLUMN runtime_generation,
    DROP COLUMN credential_version,
    DROP COLUMN rotation_fence_id,
    DROP COLUMN rotation_fence_state,
    DROP COLUMN rotation_from_credential_version,
    DROP COLUMN rotation_to_credential_version,
    DROP COLUMN rotation_from_runtime_generation,
    DROP COLUMN rotation_to_runtime_generation,
    DROP COLUMN rotation_armed_at,
    DROP COLUMN rotation_released_at,
    DROP COLUMN rotation_blocked_reason,
    ADD CONSTRAINT gateway_desired_accounts_requested_state_check
        CHECK (requested_state IN ('creating', 'active', 'disabled', 'removing', 'removed')),
    ADD CONSTRAINT gateway_desired_accounts_secret_ref_check
        CHECK (
            (requested_state = 'removed' AND secret_ref IS NULL)
            OR (requested_state <> 'removed' AND (secret_ref IS NOT NULL OR requested_state = 'creating'))
        );
CREATE INDEX gateway_desired_accounts_recovery
    ON gateway_desired_accounts (account_id)
    WHERE requested_state IN ('creating', 'active');

ALTER TABLE gateway_retired_secret_refs
    DROP COLUMN tombstone_revision;

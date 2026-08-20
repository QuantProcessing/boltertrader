-- A removed account is retained as durable lifecycle/idempotency evidence, but
-- it must not retain a reference to credential material that has already been
-- deleted. Migration 007 allowed a NULL secret_ref only while creating, which
-- made the final cleanup CAS impossible and left retire operations parked in
-- cleanup_pending forever.

ALTER TABLE gateway_desired_accounts
    DROP CONSTRAINT gateway_desired_accounts_secret_ref_check;

ALTER TABLE gateway_desired_accounts
    ADD CONSTRAINT gateway_desired_accounts_secret_ref_check
    CHECK (
        (
            requested_state = 'removed'
            AND secret_ref IS NULL
            AND previous_secret_ref IS NULL
        )
        OR (
            requested_state <> 'removed'
            AND (secret_ref IS NOT NULL OR requested_state = 'creating')
        )
    );

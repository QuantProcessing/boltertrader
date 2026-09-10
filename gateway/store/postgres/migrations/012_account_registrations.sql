-- Fresh account registrations. No historical order/principal migration and no
-- import of the retired desired/candidate/credential-operation state machine.
CREATE TABLE gateway_accounts (
    account_id text PRIMARY KEY,
    venue text NOT NULL,
    display_name text NOT NULL,
    credential_ciphertext bytea NOT NULL,
    created_at timestamptz NOT NULL,
    validated_at timestamptz NOT NULL
);

-- Orders remain historical evidence after a local connection is removed.
-- Their account_id is an immutable reference, not a pin on credential storage.
ALTER TABLE orders DROP CONSTRAINT orders_account_id_fkey;
ALTER TABLE reconciliation_checkpoints DROP CONSTRAINT reconciliation_checkpoints_account_id_fkey;

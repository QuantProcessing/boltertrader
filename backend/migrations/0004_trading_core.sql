CREATE TABLE platform_reservations (
    reservation_id text PRIMARY KEY, principal_id text NOT NULL, mode text NOT NULL CHECK (mode IN ('test','live')),
    product_id text NOT NULL, reservation_kind text NOT NULL, netting_scope jsonb NOT NULL,
    scope_key text NOT NULL, quantity numeric, notional numeric, currency text, status text NOT NULL,
    idempotency_key text NOT NULL, payload_hash text NOT NULL, expires_at timestamptz, blocked_reason text,
    created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
    UNIQUE (principal_id, mode, idempotency_key)
);
CREATE UNIQUE INDEX platform_reservations_held_scope ON platform_reservations(mode, scope_key) WHERE status = 'held';

CREATE TABLE platform_commands (
    command_id text PRIMARY KEY, principal_id text NOT NULL, account_id text NOT NULL, mode text NOT NULL CHECK (mode IN ('test','live')),
    product_id text NOT NULL, lineage_id text NOT NULL, reservation_id text REFERENCES platform_reservations(reservation_id),
    idempotency_key text NOT NULL, method text NOT NULL, path text NOT NULL, command_kind text NOT NULL,
    state text NOT NULL, payload jsonb NOT NULL, payload_hash text NOT NULL, blockers jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
    UNIQUE (principal_id, mode, method, path, idempotency_key)
);
CREATE TABLE platform_reconciliation_tasks (
    command_id text PRIMARY KEY REFERENCES platform_commands(command_id) ON DELETE CASCADE,
    mode text NOT NULL CHECK (mode IN ('test','live')), reason text NOT NULL, next_attempt_at timestamptz NOT NULL,
    lease_until timestamptz, attempt_count integer NOT NULL DEFAULT 0
);
CREATE TABLE platform_positions (
    mode text NOT NULL CHECK (mode IN ('test','live')), product_id text NOT NULL, lineage_id text NOT NULL,
    account_id text NOT NULL, instrument_id text NOT NULL, quantity numeric NOT NULL, average_entry_price numeric NOT NULL,
    mark_price numeric NOT NULL, realized_pnl numeric NOT NULL, unrealized_pnl numeric NOT NULL, updated_at timestamptz NOT NULL,
    PRIMARY KEY (mode, product_id, lineage_id, account_id, instrument_id)
);
CREATE TABLE platform_ledger_entries (
    entry_id text PRIMARY KEY, mode text NOT NULL CHECK (mode IN ('test','live')), product_id text NOT NULL,
    lineage_id text NOT NULL, account_id text NOT NULL, instrument_id text NOT NULL, kind text NOT NULL,
    amount numeric NOT NULL, currency text NOT NULL, occurred_at timestamptz NOT NULL
);
CREATE INDEX platform_ledger_attribution ON platform_ledger_entries(mode, product_id, lineage_id, account_id, instrument_id, occurred_at);
CREATE TABLE platform_unrealized_pnl (
    mode text NOT NULL CHECK (mode IN ('test','live')), product_id text NOT NULL, lineage_id text NOT NULL,
    account_id text NOT NULL, instrument_id text NOT NULL, amount numeric NOT NULL, as_of timestamptz NOT NULL,
    PRIMARY KEY (mode, product_id, lineage_id, account_id, instrument_id)
);

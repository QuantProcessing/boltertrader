-- Trading service schema.
--
-- This is one flat definition, not a replay of the platform's migration
-- history. The tables it creates are the post-consolidation shape the trading
-- stores read and write today, so the columns that the old 0007/0011/0012/0013
-- migrations bolted on afterwards are simply part of the CREATE TABLE here.
--
-- Table names are unqualified on purpose: the deployment picks the schema
-- through the connection's search_path, exactly as the gateway does. Nothing in
-- Go names a schema, so the same binary runs against `trading`, a per-test
-- throwaway schema, or `public` without a code change.
--
-- No credential material is stored here. The trading service never sees a
-- secret; account credentials live behind the gateway.

CREATE TABLE schema_migrations (
    version integer PRIMARY KEY,
    checksum text NOT NULL UNIQUE,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit (
    audit_id text PRIMARY KEY,
    principal_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    outcome text NOT NULL,
    request_id text NOT NULL,
    occurred_at timestamptz NOT NULL
);

CREATE INDEX audit_resource_idx ON audit (resource_id, occurred_at DESC);

CREATE TABLE reservations (
    reservation_id text PRIMARY KEY,
    principal_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test', 'live')),
    product_id text NOT NULL,
    reservation_kind text NOT NULL,
    netting_scope jsonb NOT NULL,
    scope_key text NOT NULL,
    quantity numeric,
    notional numeric,
    currency text,
    status text NOT NULL,
    idempotency_key text NOT NULL,
    payload_hash text NOT NULL,
    expires_at timestamptz,
    blocked_reason text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (principal_id, mode, idempotency_key)
);

-- Only one reservation may hold a netting scope at a time. The store detects
-- the collision by name, so renaming this index changes behaviour.
CREATE UNIQUE INDEX reservations_held_scope ON reservations (mode, scope_key) WHERE status = 'held';

CREATE TABLE commands (
    command_id text PRIMARY KEY,
    principal_id text NOT NULL,
    account_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test', 'live')),
    product_id text NOT NULL,
    lineage_id text NOT NULL,
    reservation_id text REFERENCES reservations (reservation_id),
    idempotency_key text NOT NULL,
    method text NOT NULL,
    path text NOT NULL,
    command_kind text NOT NULL,
    state text NOT NULL,
    payload jsonb NOT NULL,
    payload_hash text NOT NULL,
    blockers jsonb NOT NULL DEFAULT '[]',
    execution_evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (principal_id, mode, method, path, idempotency_key)
);

CREATE TABLE reconciliation_tasks (
    command_id text PRIMARY KEY REFERENCES commands (command_id) ON DELETE CASCADE,
    mode text NOT NULL CHECK (mode IN ('test', 'live')),
    reason text NOT NULL,
    next_attempt_at timestamptz NOT NULL,
    lease_until timestamptz,
    attempt_count integer NOT NULL DEFAULT 0
);

-- Accounting is principal-scoped: two principals may legitimately hold the same
-- position or fill identifier, so the principal leads every key and index.
CREATE TABLE positions (
    principal_id text NOT NULL CHECK (btrim(principal_id) <> ''),
    mode text NOT NULL CHECK (mode IN ('test', 'live')),
    product_id text NOT NULL,
    lineage_id text NOT NULL,
    account_id text NOT NULL,
    instrument_id text NOT NULL,
    settlement_currency text NOT NULL,
    quantity numeric NOT NULL,
    average_entry_price numeric NOT NULL,
    mark_price numeric NOT NULL,
    realized_pnl numeric NOT NULL,
    unrealized_pnl numeric NOT NULL,
    mark_observed_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (principal_id, mode, product_id, lineage_id, account_id, instrument_id)
);

CREATE TABLE portfolio_fills (
    principal_id text NOT NULL CHECK (btrim(principal_id) <> ''),
    fill_id text NOT NULL,
    fill_hash text NOT NULL,
    command_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test', 'live')),
    product_id text NOT NULL,
    lineage_id text NOT NULL,
    account_id text NOT NULL,
    instrument_id text NOT NULL,
    side text NOT NULL CHECK (side IN ('buy', 'sell')),
    quantity numeric NOT NULL,
    price numeric NOT NULL,
    fee numeric NOT NULL,
    fee_currency text NOT NULL,
    settlement_currency text NOT NULL,
    realized_pnl numeric NOT NULL,
    occurred_at timestamptz NOT NULL,
    PRIMARY KEY (principal_id, fill_id)
);

CREATE INDEX portfolio_fills_scope ON portfolio_fills (principal_id, mode, account_id, product_id, occurred_at, fill_id);

CREATE TABLE ledger_entries (
    principal_id text NOT NULL CHECK (btrim(principal_id) <> ''),
    entry_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test', 'live')),
    product_id text NOT NULL,
    lineage_id text NOT NULL,
    account_id text NOT NULL,
    instrument_id text NOT NULL,
    kind text NOT NULL,
    amount numeric NOT NULL,
    currency text NOT NULL,
    occurred_at timestamptz NOT NULL,
    PRIMARY KEY (principal_id, entry_id)
);

CREATE INDEX ledger_attribution ON ledger_entries (principal_id, mode, product_id, lineage_id, account_id, instrument_id, occurred_at);

CREATE TABLE unrealized_pnl (
    principal_id text NOT NULL CHECK (btrim(principal_id) <> ''),
    mode text NOT NULL CHECK (mode IN ('test', 'live')),
    product_id text NOT NULL,
    lineage_id text NOT NULL,
    account_id text NOT NULL,
    instrument_id text NOT NULL,
    amount numeric NOT NULL,
    currency text NOT NULL,
    as_of timestamptz NOT NULL,
    PRIMARY KEY (principal_id, mode, product_id, lineage_id, account_id, instrument_id)
);

-- Last-known venue-authoritative account positions. These tables deliberately
-- do not reuse `positions`, whose key and semantics are product/lineage fill
-- attribution rather than current exchange inventory.

CREATE TABLE account_position_states (
    principal_id text NOT NULL CHECK (btrim(principal_id) <> ''),
    account_id text NOT NULL CHECK (btrim(account_id) <> ''),
    state jsonb NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (principal_id, account_id)
);

CREATE TABLE account_position_rows (
    principal_id text NOT NULL CHECK (btrim(principal_id) <> ''),
    account_id text NOT NULL CHECK (btrim(account_id) <> ''),
    route_id text NOT NULL CHECK (btrim(route_id) <> ''),
    instrument_id text NOT NULL CHECK (btrim(instrument_id) <> ''),
    position_slot text NOT NULL CHECK (position_slot IN ('net', 'long', 'short')),
    item jsonb NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (principal_id, account_id, route_id, instrument_id, position_slot),
    FOREIGN KEY (principal_id, account_id)
        REFERENCES account_position_states (principal_id, account_id)
        ON DELETE CASCADE
);

CREATE INDEX account_position_rows_principal
    ON account_position_rows (principal_id, account_id, instrument_id);

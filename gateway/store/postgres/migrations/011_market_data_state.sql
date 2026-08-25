-- Only recovery-critical market metadata is durable. High-frequency marks,
-- books, open interest and replay buffers remain process-local.

CREATE TABLE market_catalog_revisions (
    scope_key text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    fingerprint text NOT NULL CHECK (length(fingerprint) > 0),
    observed_at timestamptz NOT NULL,
    instruments jsonb NOT NULL CHECK (jsonb_typeof(instruments) = 'array'),
    PRIMARY KEY (scope_key, revision)
);

CREATE TABLE market_catalog_heads (
    scope_key text PRIMARY KEY,
    current_revision bigint NOT NULL CHECK (current_revision > 0),
    fingerprint text NOT NULL CHECK (length(fingerprint) > 0),
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (scope_key, current_revision)
        REFERENCES market_catalog_revisions (scope_key, revision)
        ON DELETE RESTRICT
);

CREATE TABLE settled_funding_rates (
    route_profile_id text NOT NULL,
    instrument_id text NOT NULL,
    funding_time timestamptz NOT NULL,
    realized_rate numeric NOT NULL,
    observed_at timestamptz NOT NULL,
    source text NOT NULL CHECK (length(source) > 0),
    PRIMARY KEY (route_profile_id, instrument_id, funding_time)
);

CREATE INDEX settled_funding_rates_instrument_time
    ON settled_funding_rates (instrument_id, funding_time DESC);

CREATE TABLE settled_funding_tracking (
    instrument_id text PRIMARY KEY,
    route_profile_id text NOT NULL,
    active_reasons text[] NOT NULL DEFAULT '{}',
    tracking_started_at timestamptz NOT NULL,
    target_next_funding_time timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('active', 'awaiting_settlement', 'retrying', 'gap')),
    retry_at timestamptz,
    retry_count integer NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    error_class text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL,
    CHECK (
        (state = 'active' AND cardinality(active_reasons) > 0)
        OR state <> 'active'
    )
);

CREATE INDEX settled_funding_tracking_due
    ON settled_funding_tracking (COALESCE(retry_at, target_next_funding_time))
    WHERE state IN ('awaiting_settlement', 'retrying');

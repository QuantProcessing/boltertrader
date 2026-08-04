CREATE TABLE IF NOT EXISTS platform_realtime_events (
    sequence BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    cursor TEXT NOT NULL UNIQUE,
    event_kind TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX IF NOT EXISTS platform_realtime_events_observed_at_idx
    ON platform_realtime_events (observed_at, sequence);

CREATE TABLE IF NOT EXISTS platform_public_projection_state (
    projection_kind TEXT NOT NULL,
    projection_key TEXT NOT NULL,
    gateway_revision TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    PRIMARY KEY (projection_kind, projection_key),
    CHECK (projection_kind IN ('catalog', 'market_data')),
    CHECK (jsonb_typeof(payload) = 'object')
);

CREATE TABLE gateway_order_handoffs (
    handoff_id UUID PRIMARY KEY,
    command_id UUID NOT NULL UNIQUE REFERENCES idempotency_commands(command_id),
    account_id TEXT NOT NULL,
    route_id TEXT NOT NULL,
    desired_state_revision BIGINT NOT NULL CHECK (desired_state_revision > 0),
    runtime_generation BIGINT NOT NULL CHECK (runtime_generation > 0),
    credential_version BIGINT NOT NULL CHECK (credential_version > 0),
    state TEXT NOT NULL CHECK (state IN ('started','accepted','rejected','uncertain')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,
    CHECK (state = 'started' OR resolved_at IS NOT NULL)
);

CREATE INDEX gateway_order_handoffs_unresolved_generation
    ON gateway_order_handoffs (account_id, runtime_generation)
    WHERE state = 'started';

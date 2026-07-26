CREATE TABLE schema_migrations (
    version integer PRIMARY KEY,
    checksum text NOT NULL UNIQUE,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE gateway_accounts (
    account_id text PRIMARY KEY,
    venue text NOT NULL,
    product text NOT NULL,
    environment text NOT NULL,
    settlement text NOT NULL DEFAULT '',
    config_hash text NOT NULL,
    status text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE idempotency_commands (
    command_id uuid PRIMARY KEY,
    principal_id text NOT NULL,
    idempotency_key text NOT NULL,
    payload_hash text NOT NULL,
    request_payload jsonb NOT NULL,
    gateway_order_id uuid NOT NULL,
    portable_client_order_id bigint NOT NULL,
    account_id text NOT NULL,
    product text NOT NULL,
    state text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (principal_id, idempotency_key),
    UNIQUE (account_id, product, portable_client_order_id),
    CHECK (portable_client_order_id > 0 AND portable_client_order_id <= 281474976710655),
    CHECK (state IN ('prepared','dispatching','accepted','open','partially_filled','filled','canceled','rejected','uncertain'))
);

CREATE TABLE orders (
    gateway_order_id uuid PRIMARY KEY,
    command_id uuid NOT NULL,
    account_id text NOT NULL,
    instrument_id text NOT NULL,
    state text NOT NULL,
    resource_version bigint NOT NULL DEFAULT 0,
    venue_order_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (resource_version >= 0),
    CHECK (state IN ('prepared','dispatching','accepted','open','partially_filled','filled','canceled','rejected','uncertain')),
    FOREIGN KEY (command_id) REFERENCES idempotency_commands(command_id),
    FOREIGN KEY (account_id) REFERENCES gateway_accounts(account_id)
);

CREATE TABLE order_events (
    event_id uuid PRIMARY KEY,
    gateway_order_id uuid NOT NULL,
    resource_version bigint NOT NULL,
    state text NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (gateway_order_id, resource_version),
    CHECK (state IN ('prepared','dispatching','accepted','open','partially_filled','filled','canceled','rejected','uncertain')),
    FOREIGN KEY (gateway_order_id) REFERENCES orders(gateway_order_id)
);

CREATE TABLE venue_correlations (
    gateway_order_id uuid NOT NULL,
    account_id text NOT NULL,
    product text NOT NULL,
    portable_client_order_id bigint NOT NULL,
    venue_order_id text,
    PRIMARY KEY (gateway_order_id),
    UNIQUE (account_id, product, portable_client_order_id),
    UNIQUE (account_id, product, venue_order_id),
    CHECK (portable_client_order_id > 0 AND portable_client_order_id <= 281474976710655),
    FOREIGN KEY (gateway_order_id) REFERENCES orders(gateway_order_id)
);

CREATE TABLE fills (
    fill_id uuid PRIMARY KEY,
    gateway_order_id uuid NOT NULL,
    account_id text NOT NULL,
    product text NOT NULL,
    native_fill_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, product, native_fill_id),
    FOREIGN KEY (gateway_order_id) REFERENCES orders(gateway_order_id)
);

CREATE TABLE reconciliation_tasks (
    task_id uuid PRIMARY KEY,
    gateway_order_id uuid NOT NULL,
    reason text NOT NULL,
    evidence_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
    next_attempt_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    UNIQUE (gateway_order_id),
    CHECK (reason IN ('ambiguous_handoff','lookup_failed','evidence_invalid','evidence_unconfirmed')),
    CHECK (attempts >= 0),
    FOREIGN KEY (gateway_order_id) REFERENCES orders(gateway_order_id)
);

CREATE TABLE reconciliation_checkpoints (
    account_id text PRIMARY KEY,
    checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_success_at timestamptz,
    FOREIGN KEY (account_id) REFERENCES gateway_accounts(account_id)
);

CREATE TABLE audit_records (
    audit_id uuid PRIMARY KEY,
    principal_id text NOT NULL,
    action text NOT NULL,
    resource_id text NOT NULL,
    outcome text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE next_client_order_ids (
    account_id text NOT NULL,
    product text NOT NULL,
    next_value bigint NOT NULL,
    PRIMARY KEY (account_id, product),
    CHECK (next_value > 0 AND next_value <= 281474976710656)
);

CREATE FUNCTION prevent_terminal_order_state_regression()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.state IN ('filled', 'canceled', 'rejected') AND NEW.state <> OLD.state THEN
        RAISE EXCEPTION 'terminal order state cannot change';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER prevent_terminal_order_state_regression
BEFORE UPDATE OF state ON orders
FOR EACH ROW
EXECUTE FUNCTION prevent_terminal_order_state_regression();

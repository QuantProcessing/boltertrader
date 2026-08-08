CREATE SEQUENCE platform_work_lease_seq;

CREATE TABLE platform_work_workers (
    worker_id text PRIMARY KEY,
    instance_id text NOT NULL,
    product_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test','live')),
    contract_version text NOT NULL,
    generation integer NOT NULL,
    state text NOT NULL,
    draining boolean NOT NULL DEFAULT false,
	registered_at timestamptz NOT NULL,
	heartbeat_expires_at timestamptz NOT NULL,
    last_heartbeat_at timestamptz NOT NULL,
    last_applied_facts_cursor text NOT NULL DEFAULT '',
    UNIQUE (worker_id, mode)
);

CREATE TABLE platform_work_items (
    work_item_id text PRIMARY KEY,
    work_kind text NOT NULL CHECK (work_kind IN ('open','close','compensate','reconcile','observeSettlement')),
    lineage_id text NOT NULL,
    work_revision integer NOT NULL,
    product_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test','live')),
    status text NOT NULL CHECK (status IN ('open','leased','retry_wait','completed','failed','manual_intervention','cancelled')),
    facts_cursor text NOT NULL,
    account_fences jsonb NOT NULL DEFAULT '{}',
    payload jsonb NOT NULL DEFAULT '{}',
    result jsonb,
    error_code text NOT NULL DEFAULT '',
    retryable boolean NOT NULL DEFAULT false,
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt BETWEEN 0 AND 5),
    max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts = 5),
    lease_generation integer NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    next_attempt_at timestamptz,
    not_before timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX platform_work_items_claimable ON platform_work_items(mode, product_id, status, not_before, next_attempt_at, updated_at, work_item_id) WHERE status IN ('open','leased','retry_wait');

CREATE TABLE platform_work_leases (
    lease_id text PRIMARY KEY,
    lease_generation integer NOT NULL,
    work_item_id text NOT NULL REFERENCES platform_work_items(work_item_id) ON DELETE CASCADE,
    work_revision integer NOT NULL,
    worker_id text NOT NULL,
    worker_mode text NOT NULL CHECK (worker_mode IN ('test','live')),
    worker_generation integer NOT NULL,
    facts_cursor text NOT NULL,
    status text NOT NULL CHECK (status IN ('active','completed','failed','retry_wait','manual_intervention','expired')),
    expires_at timestamptz NOT NULL,
    renewal_count integer NOT NULL DEFAULT 0 CHECK (renewal_count >= 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (worker_id, worker_mode) REFERENCES platform_work_workers(worker_id, mode) ON DELETE CASCADE,
    UNIQUE (lease_id, lease_generation)
);
CREATE UNIQUE INDEX platform_work_leases_one_active ON platform_work_leases(work_item_id) WHERE status = 'active';
CREATE INDEX platform_work_leases_active ON platform_work_leases(work_item_id, expires_at) WHERE status = 'active';

CREATE TABLE platform_work_facts (
    fact_id text PRIMARY KEY,
    cursor text NOT NULL UNIQUE,
    account_id text NOT NULL,
    product_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test','live')),
    kind text NOT NULL,
    payload jsonb NOT NULL,
    effective_at timestamptz NOT NULL
);
CREATE INDEX platform_work_facts_cursor_order ON platform_work_facts(effective_at, cursor);

CREATE TABLE platform_funding_command_plans (
    plan_key text NOT NULL,
    preview_hash text NOT NULL,
    principal_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test','live')),
    intent_kind text NOT NULL,
    intent_payload jsonb NOT NULL,
    lineage_id text NOT NULL,
    legs jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (principal_id, mode, plan_key)
);

CREATE TABLE platform_funding_executions (
    execution_id text PRIMARY KEY,
    principal_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test','live')),
    lineage_id text NOT NULL,
    parent_command_id text NOT NULL REFERENCES platform_commands(command_id),
    plan_key text NOT NULL,
    preview_hash text NOT NULL,
    intent_kind text NOT NULL,
    state text NOT NULL CHECK (state IN ('previewed','submitting','leg_1_working','leg_2_working','partially_hedged','compensating','hedged','closed','uncertain','manual_intervention_required')),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    recovery_cursor text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (principal_id, mode, lineage_id)
);
CREATE INDEX platform_funding_executions_recovery ON platform_funding_executions(principal_id,mode,state,updated_at,execution_id) WHERE state NOT IN ('hedged','closed','manual_intervention_required');

CREATE TABLE platform_funding_execution_transitions (
    id bigserial PRIMARY KEY,
    execution_id text NOT NULL REFERENCES platform_funding_executions(execution_id) ON DELETE CASCADE,
    version bigint NOT NULL,
    from_state text NOT NULL,
    to_state text NOT NULL,
    kind text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    UNIQUE (execution_id, version)
);

CREATE TABLE platform_work_intents (
    idempotency_key text PRIMARY KEY,
	command_id text NOT NULL REFERENCES platform_commands(command_id),
    lease_id text NOT NULL,
    lease_generation integer NOT NULL,
    worker_id text NOT NULL,
    worker_generation integer NOT NULL,
    work_item_id text NOT NULL,
    work_revision integer NOT NULL,
    intent_kind text NOT NULL,
    facts_cursor text NOT NULL,
    account_fences jsonb NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (lease_id, lease_generation) REFERENCES platform_work_leases(lease_id, lease_generation)
);

CREATE TABLE platform_work_operations (
    operation_kind text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    response jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (operation_kind, idempotency_key)
);

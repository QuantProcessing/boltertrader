-- A plan is application execution authority, not another venue order ledger.
-- Its stop record doubles as a durable, deduplicated in-app alert.
CREATE TABLE arbitrage_executions (
 execution_id text PRIMARY KEY,
 principal_id text NOT NULL,
 lineage_id text NOT NULL,
 idempotency_key text NOT NULL,
 action text NOT NULL CHECK (action IN ('open','close')),
 request_hash text NOT NULL,
 intent_hash text NOT NULL,
 legs jsonb NOT NULL,
 state text NOT NULL CHECK (state IN ('executing','submitted','stopped','settled')),
 cleanup_pending boolean NOT NULL DEFAULT false,
 stop_reason text NOT NULL DEFAULT '',
 alert_at timestamptz,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 UNIQUE(principal_id,idempotency_key),
 CHECK ((state='stopped') = (alert_at IS NOT NULL AND stop_reason<>''))
);
CREATE UNIQUE INDEX arbitrage_active_lineage ON arbitrage_executions(principal_id,lineage_id)
 WHERE state IN ('executing','submitted');

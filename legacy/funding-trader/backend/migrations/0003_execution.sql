do $$
declare
  constraint_name text;
begin
  for constraint_name in
    select c.conname
    from pg_constraint c
    join pg_class t on t.oid = c.conrelid
    where t.relname = 'idempotency_records'
      and c.contype = 'u'
      and array(
        select a.attname::text
        from unnest(c.conkey) with ordinality k(attnum, ord)
        join pg_attribute a on a.attrelid = t.oid and a.attnum = k.attnum
        order by k.ord
      ) = array['key']
  loop
    execute format('alter table idempotency_records drop constraint %I', constraint_name);
  end loop;
end $$;

alter table idempotency_records
  add column if not exists principal_id text not null default 'legacy',
  add column if not exists environment text not null default 'legacy',
  add column if not exists method text not null default 'legacy',
  add column if not exists path text not null default 'legacy',
  add column if not exists execution_id text,
  add column if not exists state text not null default 'completed',
  add column if not exists updated_at timestamptz not null default now();

alter table idempotency_records
  drop constraint if exists idempotency_records_state_check,
  add constraint idempotency_records_state_check
    check (state in ('in_progress', 'completed'));

create unique index if not exists idempotency_records_scope_key_idx
  on idempotency_records(principal_id, environment, method, path, key);

create table if not exists execution_previews (
  id text not null,
  snapshot_id text not null,
  principal_id text not null,
  environment text not null,
  opportunity_id text,
  operation text not null,
  payload jsonb not null,
  request_hash text not null,
  can_submit boolean not null,
  expires_at timestamptz not null,
  created_at timestamptz not null default now(),
  primary key (principal_id, environment, id),
  check (operation in ('open', 'close'))
);

create index if not exists execution_previews_scope_expires_idx
  on execution_previews(principal_id, environment, expires_at, id);

alter table executions
  add column if not exists principal_id text not null default 'legacy',
  add column if not exists environment text not null default 'legacy',
  add column if not exists operation_kind text not null default 'open',
  add column if not exists opportunity_id text,
  add column if not exists position_id text,
  add column if not exists parent_execution_id text,
  add column if not exists lineage_id text,
  add column if not exists idempotency_key text,
  add column if not exists request_hash text,
  add column if not exists reconciliation_state text not null default 'pending',
  add column if not exists manual_intervention boolean not null default false,
  add column if not exists version bigint not null default 1,
  add column if not exists updated_at timestamptz not null default now(),
  add column if not exists completed_at timestamptz;

alter table executions
  drop constraint if exists executions_status_check,
  add constraint executions_status_check check (status in (
    'previewed','submitting','leg_1_working','leg_2_working','hedged',
    'partially_hedged','compensating','closed','failed','uncertain',
    'manual_intervention_required'
  )),
  drop constraint if exists executions_operation_kind_check,
  add constraint executions_operation_kind_check check (operation_kind in ('open', 'close')),
  drop constraint if exists executions_version_positive_check,
  add constraint executions_version_positive_check check (version > 0);

create index if not exists executions_scope_status_updated_idx
  on executions(principal_id, environment, status, updated_at desc, id);
create index if not exists executions_scope_opportunity_idx
  on executions(principal_id, environment, opportunity_id)
  where opportunity_id is not null;
create index if not exists executions_scope_position_idx
  on executions(principal_id, environment, position_id)
  where position_id is not null;
create index if not exists executions_lineage_idx
  on executions(lineage_id)
  where lineage_id is not null;

create table if not exists execution_legs (
  leg_id text primary key,
  execution_id text not null references executions(id) on delete cascade,
  role text not null,
  account_id text,
  instrument_id text,
  side text not null,
  requested_quantity numeric not null,
  filled_quantity numeric not null,
  status text not null,
  gateway_id text,
  command_id text,
  gateway_order_id text,
  reduce_only boolean not null default false,
  safe_error jsonb not null default '{}'::jsonb,
  updated_at timestamptz not null,
  check (role in ('long', 'short', 'compensation')),
  check (side in ('buy', 'sell')),
  check (status in ('accepted','working','partial','filled','canceled','rejected','uncertain'))
);

create unique index if not exists execution_legs_execution_role_idx
  on execution_legs(execution_id, role);
create unique index if not exists execution_legs_execution_gateway_idx
  on execution_legs(execution_id, gateway_id)
  where gateway_id is not null and gateway_id <> '';

create table if not exists execution_fills (
  fill_id text primary key,
  execution_id text not null references executions(id) on delete cascade,
  leg_id text not null references execution_legs(leg_id) on delete cascade,
  gateway_fill_id text,
  quantity numeric not null,
  price numeric not null,
  fee numeric not null,
  fee_currency text,
  occurred_at timestamptz not null,
  payload jsonb not null default '{}'::jsonb
);

create unique index if not exists execution_fills_execution_gateway_fill_idx
  on execution_fills(execution_id, gateway_fill_id)
  where gateway_fill_id is not null and gateway_fill_id <> '';

create table if not exists execution_transitions (
  id bigserial primary key,
  execution_id text not null references executions(id) on delete cascade,
  principal_id text not null,
  environment text not null,
  from_status text,
  to_status text not null,
  kind text not null,
  request_id text not null,
  payload jsonb not null default '{}'::jsonb,
  occurred_at timestamptz not null default now()
);

create index if not exists execution_transitions_scope_execution_idx
  on execution_transitions(principal_id, environment, execution_id, id);
create index if not exists execution_transitions_request_idx
  on execution_transitions(principal_id, environment, request_id, id);

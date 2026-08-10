create sequence if not exists ledger_append_sequence;

create table if not exists ledger_funding_payment_facts (
  fact_id text primary key,
  supersedes_fact_id text references ledger_funding_payment_facts(fact_id),
  sync_watermark_id text,
  payment_id text not null default '',
  position_id text not null default '',
  venue text not null default '',
  venue_environment text not null default '',
  product text not null default '',
  formula_version text not null default '',
  principal_id text not null,
  mode text not null check (mode in ('test','live')),
  account_id text not null,
  instrument_id text not null,
  window_start timestamptz not null,
  window_end timestamptz not null,
  pagination_traversal text not null default '',
  source_ref text not null,
  source_revision text not null,
  source_version text not null,
  observed_at timestamptz not null,
  effective_at timestamptz not null,
  amount numeric,
  asset text,
  payload_hash text not null,
  sequence_id bigint not null unique default nextval('ledger_append_sequence'),
  created_at timestamptz not null default now(),
  check (window_start < window_end),
  check (instrument_id <> ''),
  check (amount is not null and asset is not null)
);

create unique index if not exists ledger_funding_payment_facts_idempotency_idx
  on ledger_funding_payment_facts(principal_id, mode, account_id, instrument_id, window_start, window_end, pagination_traversal, source_ref, source_revision);
create unique index if not exists ledger_funding_payment_facts_successor_idx
  on ledger_funding_payment_facts(supersedes_fact_id)
  where supersedes_fact_id is not null;
create index if not exists ledger_funding_payment_facts_scope_revision_idx
  on ledger_funding_payment_facts(principal_id, mode, account_id, instrument_id, source_revision, sequence_id);

create table if not exists ledger_fill_cashflow_facts (
  fact_id text primary key,
  supersedes_fact_id text references ledger_fill_cashflow_facts(fact_id),
  sync_watermark_id text,
  principal_id text not null,
  mode text not null check (mode in ('test','live')),
  account_id text not null,
  instrument_id text not null,
  window_start timestamptz not null,
  window_end timestamptz not null,
  pagination_traversal text not null default '',
  position_id text not null default '',
  lineage_id text not null default '',
  fill_id text not null,
  source_ref text not null,
  source_revision text not null,
  source_version text not null,
  observed_at timestamptz not null,
  effective_at timestamptz not null,
  fee_amount numeric not null default 0,
  fee_asset text not null,
  rebate_amount numeric not null default 0,
  rebate_asset text not null,
  source_asset text not null,
  reporting_asset text not null,
  conversion_source text not null default '',
  conversion_as_of timestamptz,
  rounding_mode text not null default '',
  scale integer not null default 0,
  formula_version text not null default '',
  already_signed boolean not null default false,
  payload_hash text not null,
  sequence_id bigint not null unique default nextval('ledger_append_sequence'),
  created_at timestamptz not null default now(),
  check (already_signed or fee_amount >= 0),
  check (already_signed or rebate_amount >= 0),
  check (window_start < window_end)
);

create unique index if not exists ledger_fill_cashflow_facts_idempotency_idx
  on ledger_fill_cashflow_facts(principal_id, mode, account_id, instrument_id, window_start, window_end, pagination_traversal, fill_id, source_ref, source_revision);
create unique index if not exists ledger_fill_cashflow_facts_successor_idx
  on ledger_fill_cashflow_facts(supersedes_fact_id)
  where supersedes_fact_id is not null;

create table if not exists ledger_position_snapshot_facts (
  fact_id text primary key,
  supersedes_fact_id text references ledger_position_snapshot_facts(fact_id),
  sync_watermark_id text,
  principal_id text not null,
  mode text not null check (mode in ('test','live')),
  account_id text not null,
  instrument_id text not null,
  window_start timestamptz not null,
  window_end timestamptz not null,
  pagination_traversal text not null default '',
  position_id text not null,
  source_ref text not null,
  source_revision text not null,
  source_version text not null,
  observed_at timestamptz not null,
  effective_at timestamptz not null,
  reporting_asset text not null,
  closed boolean not null default false,
  as_of timestamptz,
  realized_trading_pnl numeric not null default 0,
  unrealized_trading_pnl numeric not null default 0,
  actual_funding_cash_flow numeric not null default 0,
  fees numeric not null default 0,
  verified boolean not null default false,
  residual jsonb not null default '{}'::jsonb,
  conversion_source text not null default '',
  conversion_as_of timestamptz,
  rounding_mode text not null default '',
  scale integer not null default 0,
  formula_version text not null default '',
  payload_hash text not null,
  sequence_id bigint not null unique default nextval('ledger_append_sequence'),
  created_at timestamptz not null default now(),
  check (window_start < window_end)
);

create unique index if not exists ledger_position_snapshot_facts_idempotency_idx
  on ledger_position_snapshot_facts(principal_id, mode, account_id, instrument_id, window_start, window_end, pagination_traversal, position_id, source_ref, source_revision);
create unique index if not exists ledger_position_snapshot_facts_successor_idx
  on ledger_position_snapshot_facts(supersedes_fact_id)
  where supersedes_fact_id is not null;

create table if not exists ledger_lineage_facts (
  fact_id text primary key,
  principal_id text not null,
  mode text not null check (mode in ('test','live')),
  account_id text not null,
  instrument_id text not null,
  venue_environment text not null,
  product text not null,
  lineage_id text not null,
  position_id text not null,
  source_ref text not null,
  source_revision text not null,
  source_version text not null,
  observed_at timestamptz not null,
  effective_at timestamptz not null,
  active boolean not null default true,
  closed_at timestamptz,
  payload_hash text not null,
  sequence_id bigint not null unique default nextval('ledger_append_sequence'),
  created_at timestamptz not null default now()
);

create unique index if not exists ledger_lineage_facts_idempotency_idx
  on ledger_lineage_facts(principal_id, mode, account_id, instrument_id, lineage_id, source_ref, source_revision);
create unique index if not exists ledger_lineage_facts_active_scope_idx
  on ledger_lineage_facts(principal_id, mode, account_id, instrument_id)
  where active;

create table if not exists ledger_lineage_revision_facts (
  fact_id text primary key,
  supersedes_fact_id text references ledger_lineage_revision_facts(fact_id),
  lineage_fact_id text not null references ledger_lineage_facts(fact_id),
  sync_watermark_id text,
  principal_id text not null,
  mode text not null check (mode in ('test','live')),
  account_id text not null,
  instrument_id text not null,
  revision text not null,
  status text not null,
  reason text not null default '',
  source_ref text not null,
  source_revision text not null,
  source_version text not null,
  observed_at timestamptz not null,
  effective_at timestamptz not null,
  payload_hash text not null,
  sequence_id bigint not null unique default nextval('ledger_append_sequence'),
  created_at timestamptz not null default now()
);

create unique index if not exists ledger_lineage_revision_facts_idempotency_idx
  on ledger_lineage_revision_facts(lineage_fact_id, revision, source_ref, source_revision);
create unique index if not exists ledger_lineage_revision_facts_successor_idx
  on ledger_lineage_revision_facts(supersedes_fact_id)
  where supersedes_fact_id is not null;

create table if not exists ledger_sync_watermarks (
  fact_id text primary key,
  principal_id text not null,
  mode text not null check (mode in ('test','live')),
  account_id text not null,
  instrument_id text not null,
  window_start timestamptz not null,
  window_end timestamptz not null,
  pagination_traversal text not null default '',
  source_ref text not null,
  cursor text not null,
  revision text not null,
  observed_at timestamptz not null,
  effective_through timestamptz not null,
  complete_through timestamptz not null,
  status text not null check (status in ('unsupported','temporarily_unavailable','incomplete','complete_empty','complete')),
  verified boolean not null default false,
  payload_hash text not null,
  sequence_id bigint not null unique default nextval('ledger_append_sequence'),
  created_at timestamptz not null default now(),
  check (window_start < window_end),
  check (instrument_id <> ''),
  check (verified = false or status in ('complete','complete_empty'))
);

create unique index if not exists ledger_sync_watermarks_idempotency_idx
  on ledger_sync_watermarks(principal_id, mode, account_id, instrument_id, window_start, window_end, pagination_traversal, source_ref, revision, cursor);
create index if not exists ledger_sync_watermarks_scope_revision_idx
  on ledger_sync_watermarks(principal_id, mode, account_id, instrument_id, revision, cursor, sequence_id);

alter table ledger_funding_payment_facts add column if not exists payment_id text not null default '';
alter table ledger_funding_payment_facts add column if not exists position_id text not null default '';
alter table ledger_funding_payment_facts add column if not exists venue text not null default '';
alter table ledger_funding_payment_facts add column if not exists venue_environment text not null default '';
alter table ledger_funding_payment_facts add column if not exists product text not null default '';
alter table ledger_funding_payment_facts add column if not exists formula_version text not null default '';
alter table ledger_fill_cashflow_facts add column if not exists position_id text not null default '';
alter table ledger_fill_cashflow_facts add column if not exists lineage_id text not null default '';
alter table ledger_fill_cashflow_facts add column if not exists window_start timestamptz;
alter table ledger_fill_cashflow_facts add column if not exists window_end timestamptz;
alter table ledger_fill_cashflow_facts add column if not exists pagination_traversal text not null default '';
update ledger_fill_cashflow_facts
set window_start=coalesce(window_start, observed_at),
    window_end=coalesce(window_end, observed_at + interval '1 microsecond'),
    pagination_traversal=coalesce(nullif(pagination_traversal, ''), 'legacy')
where window_start is null or window_end is null or pagination_traversal = '';
alter table ledger_fill_cashflow_facts alter column window_start set not null;
alter table ledger_fill_cashflow_facts alter column window_end set not null;
alter table ledger_position_snapshot_facts add column if not exists closed boolean not null default false;
alter table ledger_position_snapshot_facts add column if not exists as_of timestamptz;
alter table ledger_position_snapshot_facts add column if not exists window_start timestamptz;
alter table ledger_position_snapshot_facts add column if not exists window_end timestamptz;
alter table ledger_position_snapshot_facts add column if not exists pagination_traversal text not null default '';
update ledger_position_snapshot_facts
set window_start=coalesce(window_start, observed_at),
    window_end=coalesce(window_end, observed_at + interval '1 microsecond'),
    pagination_traversal=coalesce(nullif(pagination_traversal, ''), 'legacy')
where window_start is null or window_end is null or pagination_traversal = '';
alter table ledger_position_snapshot_facts alter column window_start set not null;
alter table ledger_position_snapshot_facts alter column window_end set not null;
alter table ledger_position_snapshot_facts add column if not exists conversion_source text not null default '';
alter table ledger_position_snapshot_facts add column if not exists conversion_as_of timestamptz;
alter table ledger_position_snapshot_facts add column if not exists rounding_mode text not null default '';
alter table ledger_position_snapshot_facts add column if not exists scale integer not null default 0;
alter table ledger_position_snapshot_facts add column if not exists formula_version text not null default '';
drop index if exists ledger_fill_cashflow_facts_idempotency_idx;
create unique index ledger_fill_cashflow_facts_idempotency_idx
  on ledger_fill_cashflow_facts(principal_id, mode, account_id, instrument_id, window_start, window_end, pagination_traversal, fill_id, source_ref, source_revision);
drop index if exists ledger_position_snapshot_facts_idempotency_idx;
create unique index ledger_position_snapshot_facts_idempotency_idx
  on ledger_position_snapshot_facts(principal_id, mode, account_id, instrument_id, window_start, window_end, pagination_traversal, position_id, source_ref, source_revision);

alter table ledger_funding_payment_facts
  drop constraint if exists ledger_funding_payment_facts_sync_watermark_fk;
alter table ledger_funding_payment_facts
  add constraint ledger_funding_payment_facts_sync_watermark_fk
  foreign key(sync_watermark_id) references ledger_sync_watermarks(fact_id);
alter table ledger_fill_cashflow_facts
  drop constraint if exists ledger_fill_cashflow_facts_sync_watermark_fk;
alter table ledger_fill_cashflow_facts
  add constraint ledger_fill_cashflow_facts_sync_watermark_fk
  foreign key(sync_watermark_id) references ledger_sync_watermarks(fact_id);
alter table ledger_position_snapshot_facts
  drop constraint if exists ledger_position_snapshot_facts_sync_watermark_fk;
alter table ledger_position_snapshot_facts
  add constraint ledger_position_snapshot_facts_sync_watermark_fk
  foreign key(sync_watermark_id) references ledger_sync_watermarks(fact_id);
alter table ledger_lineage_revision_facts
  drop constraint if exists ledger_lineage_revision_facts_sync_watermark_fk;
alter table ledger_lineage_revision_facts
  add constraint ledger_lineage_revision_facts_sync_watermark_fk
  foreign key(sync_watermark_id) references ledger_sync_watermarks(fact_id);

create table if not exists ledger_closed_position_projections (
  position_id text primary key,
  principal_id text not null,
  mode text not null check (mode in ('test','live')),
  account_id text not null,
  instrument_id text not null,
  reporting_asset text not null,
  realized_trading_pnl numeric not null default 0,
  unrealized_trading_pnl numeric not null default 0,
  actual_funding_cash_flow numeric not null default 0,
  fees numeric not null default 0,
  net_pnl numeric not null default 0,
  closed_trading_upnl numeric not null default 0,
  verified boolean not null default false,
  verification_status text not null default 'provisional',
  residual jsonb not null default '{}'::jsonb,
  watermark jsonb not null default '{}'::jsonb,
  as_of timestamptz,
  conversion_source text not null default '',
  conversion_as_of timestamptz,
  rounding_mode text not null default '',
  scale integer not null default 0,
  formula_version text not null default '',
  updated_at timestamptz not null default now(),
  check (verification_status in ('unsupported','temporarily_unavailable','incomplete','provisional','verified','revised','blocked')),
  check (closed_trading_upnl = unrealized_trading_pnl),
  check (not verified or (unrealized_trading_pnl = 0 and closed_trading_upnl = 0)),
  check (net_pnl = realized_trading_pnl + unrealized_trading_pnl + actual_funding_cash_flow + fees)
);

create index if not exists ledger_closed_position_projections_scope_idx
  on ledger_closed_position_projections(principal_id, mode, reporting_asset, updated_at);

create table if not exists ledger_portfolio_projections (
  principal_id text not null,
  mode text not null check (mode in ('test','live')),
  reporting_asset text not null,
  cumulative_realized_net_pnl numeric not null default 0,
  current_open_unrealized_pnl numeric not null default 0,
  total_net_pnl numeric not null default 0,
  funding_total numeric not null default 0,
  fee_total numeric not null default 0,
  verification_status text not null default 'provisional',
  open_position_count integer not null default 0,
  closed_position_count integer not null default 0,
  residual jsonb not null default '{}'::jsonb,
  watermark jsonb not null default '{}'::jsonb,
  as_of timestamptz,
  conversion_source text not null default '',
  conversion_as_of timestamptz,
  rounding_mode text not null default '',
  scale integer not null default 0,
  formula_version text not null default '',
  updated_at timestamptz not null default now(),
  check (verification_status in ('unsupported','temporarily_unavailable','incomplete','provisional','verified','revised','blocked')),
  check (total_net_pnl = cumulative_realized_net_pnl + current_open_unrealized_pnl),
  check (open_position_count >= 0 and closed_position_count >= 0),
  primary key(principal_id, mode, reporting_asset)
);

alter table ledger_closed_position_projections add column if not exists as_of timestamptz;
alter table ledger_portfolio_projections add column if not exists as_of timestamptz;

create or replace function prevent_ledger_append_only_update()
returns trigger language plpgsql as $$
begin
  raise exception 'ledger append-only table cannot be updated';
end;
$$;

create or replace function prevent_ledger_append_only_delete()
returns trigger language plpgsql as $$
begin
  raise exception 'ledger append-only table cannot be deleted';
end;
$$;

create or replace function enforce_ledger_lineage_update()
returns trigger language plpgsql as $$
begin
  if old.active = true
     and new.active = false
     and old.closed_at is null
     and new.closed_at is not null
     and new.fact_id = old.fact_id
     and new.principal_id = old.principal_id
     and new.mode = old.mode
     and new.account_id = old.account_id
     and new.instrument_id = old.instrument_id
     and new.venue_environment = old.venue_environment
     and new.product = old.product
     and new.lineage_id = old.lineage_id
     and new.position_id = old.position_id
     and new.source_ref = old.source_ref
     and new.source_revision = old.source_revision
     and new.source_version = old.source_version
     and new.observed_at = old.observed_at
     and new.effective_at = old.effective_at
     and new.payload_hash = old.payload_hash
     and new.sequence_id = old.sequence_id
     and new.created_at = old.created_at
  then
    return new;
  end if;
  raise exception 'ledger lineage table only permits controlled active release';
end;
$$;

drop trigger if exists ledger_funding_payment_facts_no_update on ledger_funding_payment_facts;
create trigger ledger_funding_payment_facts_no_update before update on ledger_funding_payment_facts
  for each row execute function prevent_ledger_append_only_update();
drop trigger if exists ledger_funding_payment_facts_no_delete on ledger_funding_payment_facts;
create trigger ledger_funding_payment_facts_no_delete before delete on ledger_funding_payment_facts
  for each row execute function prevent_ledger_append_only_delete();

drop trigger if exists ledger_fill_cashflow_facts_no_update on ledger_fill_cashflow_facts;
create trigger ledger_fill_cashflow_facts_no_update before update on ledger_fill_cashflow_facts
  for each row execute function prevent_ledger_append_only_update();
drop trigger if exists ledger_fill_cashflow_facts_no_delete on ledger_fill_cashflow_facts;
create trigger ledger_fill_cashflow_facts_no_delete before delete on ledger_fill_cashflow_facts
  for each row execute function prevent_ledger_append_only_delete();

drop trigger if exists ledger_position_snapshot_facts_no_update on ledger_position_snapshot_facts;
create trigger ledger_position_snapshot_facts_no_update before update on ledger_position_snapshot_facts
  for each row execute function prevent_ledger_append_only_update();
drop trigger if exists ledger_position_snapshot_facts_no_delete on ledger_position_snapshot_facts;
create trigger ledger_position_snapshot_facts_no_delete before delete on ledger_position_snapshot_facts
  for each row execute function prevent_ledger_append_only_delete();

drop trigger if exists ledger_lineage_facts_controlled_update on ledger_lineage_facts;
create trigger ledger_lineage_facts_controlled_update before update on ledger_lineage_facts
  for each row execute function enforce_ledger_lineage_update();
drop trigger if exists ledger_lineage_facts_no_delete on ledger_lineage_facts;
create trigger ledger_lineage_facts_no_delete before delete on ledger_lineage_facts
  for each row execute function prevent_ledger_append_only_delete();

drop trigger if exists ledger_lineage_revision_facts_no_update on ledger_lineage_revision_facts;
create trigger ledger_lineage_revision_facts_no_update before update on ledger_lineage_revision_facts
  for each row execute function prevent_ledger_append_only_update();
drop trigger if exists ledger_lineage_revision_facts_no_delete on ledger_lineage_revision_facts;
create trigger ledger_lineage_revision_facts_no_delete before delete on ledger_lineage_revision_facts
  for each row execute function prevent_ledger_append_only_delete();

drop trigger if exists ledger_sync_watermarks_no_update on ledger_sync_watermarks;
create trigger ledger_sync_watermarks_no_update before update on ledger_sync_watermarks
  for each row execute function prevent_ledger_append_only_update();
drop trigger if exists ledger_sync_watermarks_no_delete on ledger_sync_watermarks;
create trigger ledger_sync_watermarks_no_delete before delete on ledger_sync_watermarks
  for each row execute function prevent_ledger_append_only_delete();

alter table opportunities
  add column if not exists environment text not null default 'legacy',
  add column if not exists long_account_id text,
  add column if not exists short_account_id text,
  add column if not exists net_apr numeric,
  add column if not exists formula_version text,
  add column if not exists observed_at timestamptz,
  add column if not exists updated_at timestamptz not null default now();

alter table alerts
  add column if not exists environment text not null default 'legacy',
  add column if not exists account_id text,
  add column if not exists instrument_id text,
  add column if not exists observed_at timestamptz,
  add column if not exists updated_at timestamptz not null default now();

alter table positions
  add column if not exists principal_id text not null default 'principal-default',
  add column if not exists environment text not null default 'legacy',
  add column if not exists status text not null default 'open',
  add column if not exists updated_at timestamptz not null default now();

alter table activities
  add column if not exists principal_id text not null default 'principal-default',
  add column if not exists environment text not null default 'legacy',
  add column if not exists position_id text,
  add column if not exists execution_id text,
  add column if not exists occurred_at timestamptz not null default now();

alter table audit_events
  add column if not exists principal_id text,
  add column if not exists environment text not null default 'legacy',
  add column if not exists target_id text,
  add column if not exists execution_id text,
  add column if not exists payload jsonb not null default '{}'::jsonb;

create table if not exists funding_observations (
  environment text not null,
  account_id text not null,
  instrument_id text not null,
  funding_time timestamptz not null,
  observed_at timestamptz not null,
  next_funding_time timestamptz,
  rate numeric not null,
  mark_price numeric,
  created_at timestamptz not null default now(),
  primary key (environment, account_id, instrument_id, funding_time)
);

create table if not exists read_model_events (
  id bigserial primary key,
  boot_id text not null,
  principal_id text,
  environment text not null,
  kind text not null,
  scope text[] not null,
  entity_id text,
  payload jsonb not null,
  occurred_at timestamptz not null default now()
);

create index if not exists opportunities_environment_net_apr_id_idx
  on opportunities(environment, net_apr desc nulls last, id);
create index if not exists alerts_environment_observed_id_idx
  on alerts(environment, observed_at desc nulls last, id);
create index if not exists positions_principal_environment_updated_id_idx
  on positions(principal_id, environment, updated_at desc, id);
create index if not exists activities_principal_environment_occurred_id_idx
  on activities(principal_id, environment, occurred_at desc, id);
create index if not exists audit_events_created_id_idx
  on audit_events(created_at desc, id);
create index if not exists funding_observations_scope_time_idx
  on funding_observations(environment, account_id, instrument_id, funding_time desc);
create index if not exists read_model_events_boot_id_idx
  on read_model_events(boot_id, environment, principal_id, id);

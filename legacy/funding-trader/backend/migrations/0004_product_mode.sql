-- Breaking private-V1 product-mode cutover.
-- The CutoverRunner executes this file inside the existing advisory-lock
-- transaction after process/session fencing and a verified backup proof.

-- authoritative_collision
do $$
begin
  if exists (
    select 1
    from idempotency_records
    group by
      principal_id,
      case lower(environment)
        when 'demo' then 'test'
        when 'testnet' then 'test'
        when 'test' then 'test'
        when 'mainnet' then 'live'
        when 'live' then 'live'
      end,
      method,
      path,
      key
    having count(*) > 1
  ) then
    raise exception using
      errcode = 'P0001',
      message = 'authoritative_collision: idempotency_records';
  end if;

  if exists (
    select 1
    from (
      select environment from users
      union all select environment from idempotency_records
      union all select environment from executions
      union all select environment from execution_transitions
      union all select environment from positions
      union all select environment from activities
      union all select environment from audit_events
      union all select environment from funding_observations
    ) scoped
    where case lower(environment)
      when 'demo' then 'test'
      when 'testnet' then 'test'
      when 'test' then 'test'
      when 'mainnet' then 'live'
      when 'live' then 'live'
    end is null
  ) then
    raise exception using
      errcode = 'P0001',
      message = 'invalid_product_environment';
  end if;
end $$;

-- missing_native_provenance
do $$
begin
  if exists (
    select 1
    from execution_legs leg
    join executions execution on execution.id = leg.execution_id
    where (
      select count(*)
      from (
        select distinct
          item->>'accountId' as account_id,
          item->>'venue' as venue,
          item->>'instrumentId' as instrument_id,
          lower(item->>'venueEnvironment') as venue_environment
        from jsonb_array_elements(
          case
            when jsonb_typeof(execution.payload->'legs') = 'array'
              then execution.payload->'legs'
            else '[]'::jsonb
          end
          ||
          case
            when jsonb_typeof(execution.payload->'plannedLegs') = 'array'
              then execution.payload->'plannedLegs'
            else '[]'::jsonb
          end
        ) item
        where item->>'role' = leg.role
          and item->>'accountId' = leg.account_id
          and item->>'instrumentId' = leg.instrument_id
          and nullif(item->>'venue', '') is not null
          and lower(item->>'venueEnvironment') in ('demo', 'testnet', 'live')
          and (
            lower(split_part(item->>'instrumentId', ':', 2))
              not in ('demo', 'testnet', 'mainnet', 'live')
            or (
              lower(split_part(item->>'instrumentId', ':', 1))
                = lower(item->>'venue')
              and case lower(split_part(item->>'instrumentId', ':', 2))
                when 'demo' then 'demo'
                when 'testnet' then 'testnet'
                when 'mainnet' then 'live'
                when 'live' then 'live'
              end = lower(item->>'venueEnvironment')
            )
          )
          and (
            (
              lower(execution.environment) in ('demo', 'testnet', 'test')
              and lower(item->>'venueEnvironment') in ('demo', 'testnet')
            )
            or (
              lower(execution.environment) in ('mainnet', 'live')
              and lower(item->>'venueEnvironment') = 'live'
            )
          )
      ) candidates
    ) <> 1
  ) then
    raise exception using
      errcode = 'P0001',
      message = 'missing_native_provenance: execution_legs';
  end if;

  if exists (
    select 1
    from funding_observations
    where (
      case
        when lower(environment) = 'demo' then 'demo'
        when lower(environment) = 'testnet' then 'testnet'
        when lower(environment) in ('mainnet', 'live') then 'live'
        when lower(environment) = 'test'
          and lower(split_part(instrument_id, ':', 2)) in ('demo', 'testnet')
          then lower(split_part(instrument_id, ':', 2))
        else null
      end is null
      or (
        lower(split_part(instrument_id, ':', 2))
          in ('demo', 'testnet', 'mainnet', 'live')
        and case lower(split_part(instrument_id, ':', 2))
          when 'demo' then 'demo'
          when 'testnet' then 'testnet'
          when 'mainnet' then 'live'
          when 'live' then 'live'
        end <> case
          when lower(environment) = 'demo' then 'demo'
          when lower(environment) = 'testnet' then 'testnet'
          when lower(environment) in ('mainnet', 'live') then 'live'
          when lower(environment) = 'test'
            and lower(split_part(instrument_id, ':', 2)) in ('demo', 'testnet')
            then lower(split_part(instrument_id, ':', 2))
        end
      )
    )
  ) then
    raise exception using
      errcode = 'P0001',
      message = 'missing_native_provenance: funding_observations';
  end if;

  if exists (
    with expected_routes as (
      select distinct
        leg.execution_id,
        leg.role,
        item->>'accountId' as account_id,
        item->>'venue' as venue,
        item->>'instrumentId' as instrument_id,
        lower(item->>'venueEnvironment') as venue_environment
      from execution_legs leg
      join executions execution on execution.id = leg.execution_id
      cross join lateral jsonb_array_elements(
        case
          when jsonb_typeof(execution.payload->'legs') = 'array'
            then execution.payload->'legs'
          else '[]'::jsonb
        end
        ||
        case
          when jsonb_typeof(execution.payload->'plannedLegs') = 'array'
            then execution.payload->'plannedLegs'
          else '[]'::jsonb
        end
      ) item
      where item->>'role' = leg.role
        and item->>'accountId' = leg.account_id
        and item->>'instrumentId' = leg.instrument_id
        and nullif(item->>'venue', '') is not null
        and lower(item->>'venueEnvironment') in ('demo', 'testnet', 'live')
    )
    select 1
    from idempotency_records record
    where record.state = 'completed'
      and record.execution_id is not null
      and (
        case
          when jsonb_typeof(record.response_body->'legs') = 'array'
            then jsonb_array_length(record.response_body->'legs')
          else -1
        end <> (
          select count(*)
          from execution_legs leg
          where leg.execution_id = record.execution_id
        )
        or exists (
          select 1
          from expected_routes route
          where route.execution_id = record.execution_id
            and (
              select count(*)
              from jsonb_array_elements(
                case
                  when jsonb_typeof(record.response_body->'legs') = 'array'
                    then record.response_body->'legs'
                  else '[]'::jsonb
                end
              ) item
              where item->>'role' = route.role
                and item->>'accountId' = route.account_id
                and item->>'venue' = route.venue
                and item->>'instrumentId' = route.instrument_id
                and lower(item->>'venueEnvironment') = route.venue_environment
            ) <> 1
        )
      )
  ) then
    raise exception using
      errcode = 'P0001',
      message = 'missing_native_provenance: idempotency_records';
  end if;

  if exists (
    with planned_routes as (
      select distinct
        execution.id as execution_id,
        item->>'role' as role,
        item->>'accountId' as account_id,
        item->>'venue' as venue,
        item->>'instrumentId' as instrument_id,
        lower(item->>'venueEnvironment') as venue_environment
      from executions execution
      cross join lateral jsonb_array_elements(
        case
          when jsonb_typeof(execution.payload->'plannedLegs') = 'array'
            then execution.payload->'plannedLegs'
          else '[]'::jsonb
        end
      ) item
    )
    select 1
    from positions position
    where nullif(position.payload->>'openingExecutionId', '') is null
      or (
        select count(*)
        from planned_routes route
        where route.execution_id = position.payload->>'openingExecutionId'
          and route.role in ('long', 'short')
          and (
            case route.role
              when 'long' then position.payload->'longLeg'
              when 'short' then position.payload->'shortLeg'
            end
          )->>'accountId' = route.account_id
          and (
            case route.role
              when 'long' then position.payload->'longLeg'
              when 'short' then position.payload->'shortLeg'
            end
          )->>'venue' = route.venue
          and (
            case route.role
              when 'long' then position.payload->'longLeg'
              when 'short' then position.payload->'shortLeg'
            end
          )->>'instrumentId' = route.instrument_id
          and lower((
            case route.role
              when 'long' then position.payload->'longLeg'
              when 'short' then position.payload->'shortLeg'
            end
          )->>'venueEnvironment') = route.venue_environment
      ) <> 2
  ) then
    raise exception using
      errcode = 'P0001',
      message = 'missing_native_provenance: positions';
  end if;

  if exists (
    select 1
    from audit_events
    where action like 'execution.%'
      and execution_id is null
  ) then
    raise exception using
      errcode = 'P0001',
      message = 'missing_native_provenance: audit_events';
  end if;
end $$;

-- Derived/transient projections are rebuilt under the new contract.
truncate table alerts, opportunities, execution_previews, read_model_events
  restart identity;

-- service_sessions_revoked
update sessions
set revoked_at = coalesce(revoked_at, now())
where revoked_at is null;

alter table sessions
  add column mode text;
update sessions session
set mode = case lower(principal.environment)
  when 'demo' then 'test'
  when 'testnet' then 'test'
  when 'test' then 'test'
  when 'mainnet' then 'live'
  when 'live' then 'live'
end
from users principal
where principal.id = session.user_id;
alter table sessions
  alter column mode set not null,
  add constraint sessions_mode_check
    check (mode in ('test', 'live'));
create index sessions_user_mode_active_idx
  on sessions(user_id, mode, expires_at desc)
  where revoked_at is null;

alter table users
  drop column environment;

alter table idempotency_records
  add column mode text;
update idempotency_records
set mode = case lower(environment)
  when 'demo' then 'test'
  when 'testnet' then 'test'
  when 'test' then 'test'
  when 'mainnet' then 'live'
  when 'live' then 'live'
end;
update idempotency_records
set response_body = case
  when response_body is null then null
  else (response_body - 'environment' - 'mode') || jsonb_build_object('mode', mode)
end;
drop index if exists idempotency_records_scope_key_idx;
alter table idempotency_records
  alter column mode set not null,
  drop column environment,
  add constraint idempotency_records_mode_check
    check (mode in ('test', 'live'));
create unique index idempotency_records_mode_key_idx
  on idempotency_records(principal_id, mode, method, path, key);

alter table execution_previews
  add column mode text not null;
drop index if exists execution_previews_scope_expires_idx;
alter table execution_previews
  drop constraint execution_previews_pkey,
  drop column environment,
  add constraint execution_previews_mode_check
    check (mode in ('test', 'live')),
  add constraint execution_previews_pkey
    primary key (principal_id, mode, id);
create index execution_previews_mode_expires_idx
  on execution_previews(principal_id, mode, expires_at, id);

alter table opportunities
  add column mode text not null;
drop index if exists opportunities_environment_net_apr_id_idx;
alter table opportunities
  drop column environment,
  add constraint opportunities_mode_check
    check (mode in ('test', 'live'));
create index opportunities_mode_net_apr_id_idx
  on opportunities(mode, net_apr desc nulls last, id);

alter table alerts
  add column mode text not null;
drop index if exists alerts_environment_observed_id_idx;
alter table alerts
  drop column environment,
  add constraint alerts_mode_check
    check (mode in ('test', 'live'));
create index alerts_mode_observed_id_idx
  on alerts(mode, observed_at desc nulls last, id);

alter table executions
  add column mode text;
update executions
set mode = case lower(environment)
  when 'demo' then 'test'
  when 'testnet' then 'test'
  when 'test' then 'test'
  when 'mainnet' then 'live'
  when 'live' then 'live'
end;
update executions
set payload = (payload - 'environment' - 'mode') || jsonb_build_object('mode', mode);
drop index if exists executions_scope_status_updated_idx;
drop index if exists executions_scope_opportunity_idx;
drop index if exists executions_scope_position_idx;
alter table executions
  alter column mode set not null,
  drop column environment,
  add constraint executions_mode_check
    check (mode in ('test', 'live'));
create index executions_mode_status_updated_idx
  on executions(principal_id, mode, status, updated_at desc, id);
create index executions_mode_opportunity_idx
  on executions(principal_id, mode, opportunity_id)
  where opportunity_id is not null;
create index executions_mode_position_idx
  on executions(principal_id, mode, position_id)
  where position_id is not null;

alter table execution_legs
  add column venue text,
  add column venue_environment text;
with leg_provenance as (
  select distinct
    leg.leg_id,
    item->>'venue' as venue,
    lower(item->>'venueEnvironment') as venue_environment
  from execution_legs leg
  join executions execution on execution.id = leg.execution_id
  cross join lateral jsonb_array_elements(
    case
      when jsonb_typeof(execution.payload->'legs') = 'array'
        then execution.payload->'legs'
      else '[]'::jsonb
    end
    ||
    case
      when jsonb_typeof(execution.payload->'plannedLegs') = 'array'
        then execution.payload->'plannedLegs'
      else '[]'::jsonb
    end
  ) item
  where item->>'role' = leg.role
    and item->>'accountId' = leg.account_id
    and item->>'instrumentId' = leg.instrument_id
)
update execution_legs leg
set venue = provenance.venue,
    venue_environment = provenance.venue_environment
from leg_provenance provenance
where provenance.leg_id = leg.leg_id;
alter table execution_legs
  alter column account_id set not null,
  alter column instrument_id set not null,
  alter column venue set not null,
  alter column venue_environment set not null,
  add constraint execution_legs_venue_environment_check
    check (venue_environment in ('demo', 'testnet', 'live'));

update execution_fills fill
set payload = coalesce(fill.payload, '{}'::jsonb) || jsonb_build_object(
  'route',
  jsonb_build_object(
    'role', leg.role,
    'accountId', leg.account_id,
    'venue', leg.venue,
    'instrumentId', leg.instrument_id,
    'venueEnvironment', leg.venue_environment
  )
)
from execution_legs leg
where leg.leg_id = fill.leg_id;

with execution_routes as (
  select
    execution_id,
    jsonb_agg(
      jsonb_build_object(
        'role', role,
        'accountId', account_id,
        'venue', venue,
        'instrumentId', instrument_id,
        'venueEnvironment', venue_environment
      )
      order by role
    ) as legs
  from execution_legs
  group by execution_id
)
update execution_transitions transition
set payload = (coalesce(transition.payload, '{}'::jsonb) - 'legs')
  || jsonb_build_object('legs', route.legs)
from execution_routes route
where route.execution_id = transition.execution_id;

with execution_routes as (
  select
    execution_id,
    jsonb_agg(
      jsonb_build_object(
        'role', role,
        'accountId', account_id,
        'venue', venue,
        'instrumentId', instrument_id,
        'venueEnvironment', venue_environment
      )
      order by role
    ) as legs
  from execution_legs
  group by execution_id
)
update audit_events audit
set payload = (coalesce(audit.payload, '{}'::jsonb) - 'legs')
  || jsonb_build_object('legs', route.legs)
from execution_routes route
where route.execution_id = audit.execution_id;

alter table execution_transitions
  add column mode text;
update execution_transitions
set mode = case lower(environment)
  when 'demo' then 'test'
  when 'testnet' then 'test'
  when 'test' then 'test'
  when 'mainnet' then 'live'
  when 'live' then 'live'
end;
update execution_transitions
set payload = (payload - 'environment' - 'mode') || jsonb_build_object('mode', mode);
drop index if exists execution_transitions_scope_execution_idx;
drop index if exists execution_transitions_request_idx;
alter table execution_transitions
  alter column mode set not null,
  drop column environment,
  add constraint execution_transitions_mode_check
    check (mode in ('test', 'live'));
create index execution_transitions_mode_execution_idx
  on execution_transitions(principal_id, mode, execution_id, id);
create index execution_transitions_mode_request_idx
  on execution_transitions(principal_id, mode, request_id, id);

alter table positions
  add column mode text;
update positions
set mode = case lower(environment)
  when 'demo' then 'test'
  when 'testnet' then 'test'
  when 'test' then 'test'
  when 'mainnet' then 'live'
  when 'live' then 'live'
end;
update positions
set payload = (payload - 'environment' - 'mode') || jsonb_build_object('mode', mode);
drop index if exists positions_principal_environment_updated_id_idx;
alter table positions
  alter column mode set not null,
  drop column environment,
  add constraint positions_mode_check
    check (mode in ('test', 'live'));
create index positions_principal_mode_updated_id_idx
  on positions(principal_id, mode, updated_at desc, id);

alter table activities
  add column mode text;
update activities
set mode = case lower(environment)
  when 'demo' then 'test'
  when 'testnet' then 'test'
  when 'test' then 'test'
  when 'mainnet' then 'live'
  when 'live' then 'live'
end;
update activities
set payload = (payload - 'environment' - 'mode') || jsonb_build_object('mode', mode);
drop index if exists activities_principal_environment_occurred_id_idx;
alter table activities
  alter column mode set not null,
  drop column environment,
  add constraint activities_mode_check
    check (mode in ('test', 'live'));
create index activities_principal_mode_occurred_id_idx
  on activities(principal_id, mode, occurred_at desc, id);

alter table audit_events
  add column mode text;
update audit_events
set mode = case lower(environment)
  when 'demo' then 'test'
  when 'testnet' then 'test'
  when 'test' then 'test'
  when 'mainnet' then 'live'
  when 'live' then 'live'
end;
update audit_events
set payload = (payload - 'environment' - 'mode') || jsonb_build_object('mode', mode);
alter table audit_events
  alter column mode set not null,
  drop column environment,
  add constraint audit_events_mode_check
    check (mode in ('test', 'live'));
create index audit_events_principal_mode_created_id_idx
  on audit_events(principal_id, mode, created_at desc, id);

alter table funding_observations
  add column mode text,
  add column venue_environment text;
update funding_observations
set mode = case lower(environment)
      when 'demo' then 'test'
      when 'testnet' then 'test'
      when 'test' then 'test'
      when 'mainnet' then 'live'
      when 'live' then 'live'
    end,
    venue_environment = case
      when lower(environment) = 'demo' then 'demo'
      when lower(environment) = 'testnet' then 'testnet'
      when lower(environment) in ('mainnet', 'live') then 'live'
      when lower(environment) = 'test'
        then lower(split_part(instrument_id, ':', 2))
    end;
drop index if exists funding_observations_scope_time_idx;
alter table funding_observations
  drop constraint funding_observations_pkey,
  alter column mode set not null,
  alter column venue_environment set not null,
  drop column environment,
  add constraint funding_observations_mode_check
    check (mode in ('test', 'live')),
  add constraint funding_observations_venue_environment_check
    check (venue_environment in ('demo', 'testnet', 'live')),
  add constraint funding_observations_mode_environment_check
    check (
      (mode = 'test' and venue_environment in ('demo', 'testnet'))
      or (mode = 'live' and venue_environment = 'live')
    ),
  add constraint funding_observations_pkey
    primary key (mode, venue_environment, account_id, instrument_id, funding_time);
create index funding_observations_mode_scope_time_idx
  on funding_observations(
    mode, venue_environment, account_id, instrument_id, funding_time desc
  );

alter table read_model_events
  add column mode text not null,
  add column contract_version text not null default 'private-v1-mode-1';
alter table read_model_events
  alter column contract_version drop default;
drop index if exists read_model_events_boot_id_idx;
alter table read_model_events
  drop column environment,
  add constraint read_model_events_mode_check
    check (mode in ('test', 'live')),
  add constraint read_model_events_contract_version_check
    check (contract_version = 'private-v1-mode-1');
create index read_model_events_contract_mode_idx
  on read_model_events(
    boot_id, contract_version, mode, principal_id, id
  );

create table cutover_control (
  singleton boolean primary key default true check (singleton),
  contract_version integer not null check (contract_version > 0),
  phase text not null check (
    phase in (
      'backup_verified',
      'migrating',
      'validated',
      'maintenance_smoke',
      'reopened',
      'restore_authorized',
      'write_accepted',
      'failed'
    )
  ),
  backup_id text not null,
  backup_sha256 text not null check (backup_sha256 ~ '^[a-f0-9]{64}$'),
  backup_verified_at timestamptz not null,
  old_service_stopped boolean not null,
  auto_restart_disabled boolean not null,
  normal_traffic_reopened boolean not null default false,
  new_contract_write_accepted boolean not null default false,
  validated_at timestamptz,
  updated_at timestamptz not null default now()
);

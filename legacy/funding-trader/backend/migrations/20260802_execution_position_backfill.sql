with canonical_open_positions as (
  select
    id,
    coalesce(nullif(position_id, ''), nullif(payload->>'positionId', ''), 'position-' || id) as position_id
  from executions
  where operation_kind = 'open'
    and (
      coalesce(position_id, '') = ''
      or nullif(payload->>'positionId', '') is null
    )
)
update executions execution
set
  position_id = case
    when coalesce(execution.position_id, '') = '' then canonical.position_id
    else execution.position_id
  end,
  payload = case
    when nullif(execution.payload->>'positionId', '') is null
      then jsonb_set(coalesce(execution.payload, '{}'::jsonb), '{positionId}', to_jsonb(canonical.position_id), true)
    else execution.payload
  end
from canonical_open_positions canonical
where execution.id = canonical.id;

update executions
set parent_execution_id = nullif(payload->>'parentExecutionId', '')
where coalesce(parent_execution_id, '') = ''
  and nullif(payload->>'parentExecutionId', '') is not null;

update executions
set lineage_id = nullif(payload->>'lineageId', '')
where coalesce(lineage_id, '') = ''
  and nullif(payload->>'lineageId', '') is not null;

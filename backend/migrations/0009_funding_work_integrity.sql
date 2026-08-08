ALTER TABLE platform_work_facts
    DROP CONSTRAINT platform_work_facts_cursor_key;

CREATE INDEX platform_work_facts_cursor_batch
    ON platform_work_facts(cursor, fact_id);

CREATE FUNCTION platform_valid_funding_account_fences(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT jsonb_typeof(value) = 'array'
       AND jsonb_array_length(value) = 2
       AND NOT EXISTS (
           SELECT 1
           FROM jsonb_array_elements(value) AS fence
           WHERE jsonb_typeof(fence) <> 'object'
              OR jsonb_typeof(fence->'accountId') <> 'string'
              OR btrim(fence->>'accountId') = ''
              OR jsonb_typeof(fence->'desiredStateRevision') <> 'number'
              OR jsonb_typeof(fence->'runtimeGeneration') <> 'number'
              OR jsonb_typeof(fence->'credentialVersion') <> 'number'
              OR (fence->>'desiredStateRevision')::numeric < 1
              OR (fence->>'runtimeGeneration')::numeric < 1
              OR (fence->>'credentialVersion')::numeric < 1
              OR (fence->>'desiredStateRevision')::numeric <> trunc((fence->>'desiredStateRevision')::numeric)
              OR (fence->>'runtimeGeneration')::numeric <> trunc((fence->>'runtimeGeneration')::numeric)
              OR (fence->>'credentialVersion')::numeric <> trunc((fence->>'credentialVersion')::numeric)
              OR fence - ARRAY['accountId','desiredStateRevision','runtimeGeneration','credentialVersion'] <> '{}'::jsonb
       )
       AND (
           SELECT count(DISTINCT fence->>'accountId') = 2
           FROM jsonb_array_elements(value) AS fence
       );
$$;

ALTER TABLE platform_work_items
    ALTER COLUMN account_fences DROP DEFAULT,
    ADD CONSTRAINT platform_work_items_account_fences_complete
        CHECK (platform_valid_funding_account_fences(account_fences));

ALTER TABLE platform_work_intents
    ADD CONSTRAINT platform_work_intents_account_fences_complete
        CHECK (platform_valid_funding_account_fences(account_fences));

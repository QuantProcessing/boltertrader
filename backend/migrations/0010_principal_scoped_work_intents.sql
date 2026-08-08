ALTER TABLE platform_work_intents
    DROP CONSTRAINT platform_work_intents_pkey,
    ADD PRIMARY KEY (work_item_id, idempotency_key);

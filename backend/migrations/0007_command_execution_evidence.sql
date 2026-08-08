ALTER TABLE platform_commands
    ADD COLUMN execution_evidence jsonb NOT NULL DEFAULT '{}'::jsonb;

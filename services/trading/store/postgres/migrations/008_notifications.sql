CREATE TABLE notification_channels (
    principal_id text NOT NULL,
    provider text NOT NULL CHECK (provider IN ('telegram', 'bark')),
    sealed bytea NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (principal_id, provider)
);

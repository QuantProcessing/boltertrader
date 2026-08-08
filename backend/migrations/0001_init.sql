CREATE TABLE platform_schema_migrations (
    version integer PRIMARY KEY,
    checksum text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

# Docker-Free Migration Operations

The migration tool executes fixed PostgreSQL, Go, and Make argv plans and writes
typed evidence only after every command and postcondition succeeds. It never
uses Docker, reads an `.env` file, activates target writes, retires the source,
or sends venue orders.

## Prerequisites

Run from the frozen target repository. Build the tool and create private local
artifact directories:

```sh
go build -o .local/bin/migration-tool ./cmd/migration-tool
install -d -m 0700 .local/migration/artifacts .local/migration/evidence
```

Configure non-secret libpq service names in the commands below. PostgreSQL
resolves their connection material through the operator-managed libpq setup;
never put a DSN, password, token, or secret file content on the command line or
in the evidence directory.

```sh
export BT_MIGRATION_SOURCE_SERVICE=funding_source_migration
export BT_MIGRATION_TARGET_SERVICE=bolter_target_migration
```

The immutable Git provenance gate must already be recorded and verified before
backup. Confirm the current phase without changing state:

```sh
.local/bin/migration-tool status --manifest migration/manifest.json
```

## Ordered Operations

Create the backup. The command invokes `pg_dump` directly and records the dump
digest, pre-state digest, argv digest, and output digest without storing command
output:

```sh
.local/bin/migration-tool backup \
  --manifest migration/manifest.json \
  --id backup-001 \
  --artifact .local/migration/artifacts/backup.dump \
  --evidence-dir .local/migration/evidence \
  --source-service "$BT_MIGRATION_SOURCE_SERVICE"
```

Restore into a clean verification database, then capture the deterministic
source-side final delta:

```sh
.local/bin/migration-tool restore \
  --manifest migration/manifest.json \
  --artifact .local/migration/artifacts/backup.dump \
  --verifier operator-id \
  --evidence-dir .local/migration/evidence \
  --target-service "$BT_MIGRATION_TARGET_SERVICE"

.local/bin/migration-tool final-delta \
  --manifest migration/manifest.json \
  --artifact .local/migration/artifacts/final-delta.sql \
  --evidence-dir .local/migration/evidence \
  --source-service "$BT_MIGRATION_SOURCE_SERVICE"
```

Raise the database-level one-writer fence. The tool changes both database
defaults to read-only and verifies each setting from a new connection before it
records the fence:

```sh
.local/bin/migration-tool fence \
  --manifest migration/manifest.json \
  --evidence-dir .local/migration/evidence \
  --source-service "$BT_MIGRATION_SOURCE_SERVICE" \
  --target-service "$BT_MIGRATION_TARGET_SERVICE"
```

Apply each repository-owned SQL backfill by exact path. The runner hashes the
script before execution, runs it in one transaction with a migration-only
target write override, and rejects any pre/post content drift:

```sh
.local/bin/migration-tool backfill \
  --manifest migration/manifest.json \
  --backfill-script backend/migrations/0013_principal_scoped_accounting.sql \
  --evidence-dir .local/migration/evidence \
  --target-service "$BT_MIGRATION_TARGET_SERVICE"
```

Run offline, browser, read-only real-Test, and restart/recovery gates. The real
gate retains its explicit Test-only attestation and default-denied venue-write
checks:

```sh
BT_DEV_REAL_STACK_ACCEPTANCE=1 .local/bin/migration-tool validate \
  --manifest migration/manifest.json \
  --verifier operator-id \
  --evidence-dir .local/migration/evidence

.local/bin/migration-tool prepare-activation \
  --manifest migration/manifest.json
```

`prepare-activation` stops at `activation-ready`. There is no `activate` or
`retire` execution command. Activation evidence and retirement preparation are
separate, externally authorized G010 surfaces.

## Retry And Recovery

Retry an interrupted command with the same manifest, identifiers, artifacts,
services, and evidence directory. Exact completed requests replay without a new
workflow event; changed artifact or script bytes fail with a replay conflict.
Inspect `status` after every nonzero exit before retrying.

Before the first backfill, reverse the concrete fence and return to the restored
phase:

```sh
.local/bin/migration-tool rollback \
  --manifest migration/manifest.json \
  --reason 'pre-backfill verification failed' \
  --evidence-dir .local/migration/evidence \
  --source-service "$BT_MIGRATION_SOURCE_SERVICE" \
  --target-service "$BT_MIGRATION_TARGET_SERVICE"
```

Rollback verifies that source defaults accept writes again while target
canonical writes remain disabled. After the first backfill, rollback is
rejected and recovery must proceed forward in the target.

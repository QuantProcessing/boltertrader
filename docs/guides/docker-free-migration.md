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
export BT_MIGRATION_GATEWAY_SERVICE=bolter_gateway_migration
export BT_MIGRATION_MAINTENANCE_SERVICE=bolter_target_maintenance
export BT_MIGRATION_SOURCE_DATABASE=funding_source
export BT_MIGRATION_TARGET_DATABASE=bolter_target
export BT_MIGRATION_GATEWAY_DATABASE=bolter_gateway
export BT_MIGRATION_MAINTENANCE_DATABASE=postgres
export BT_MIGRATION_SOURCE_WRITER_ROLE=funding_writer
export BT_MIGRATION_TARGET_WRITER_ROLE=bolter_writer
```

The immutable Git provenance gate must already be recorded and verified before
backup. Confirm the current phase without changing state:

```sh
.local/bin/migration-tool status --manifest migration/manifest.json
```

Record the reviewed subtree import commit. The command resolves the real Git
objects, reachability, remotes, branches, and subtree before it generates proof;
it does not accept caller-authored proof JSON:

```sh
export BT_MIGRATION_IMPORT_COMMIT='<reviewed 40-character import commit>'
.local/bin/migration-tool record-import \
  --manifest migration/manifest.json \
  --id "$BT_MIGRATION_IMPORT_COMMIT" \
  --evidence-dir .local/migration/evidence
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
  --target-service "$BT_MIGRATION_TARGET_SERVICE" \
  --target-database "$BT_MIGRATION_TARGET_DATABASE" \
  --maintenance-service "$BT_MIGRATION_MAINTENANCE_SERVICE" \
  --maintenance-database "$BT_MIGRATION_MAINTENANCE_DATABASE"
```

Capture one full custom snapshot from the still-authorized source in a read-only
session, verify the archive catalog, recreate the target through an explicit
maintenance connection, restore the snapshot, verify that the target schema is
nonempty, and record only the checksum of the source high-water probe:

```sh
.local/bin/migration-tool final-delta \
  --manifest migration/manifest.json \
  --artifact .local/migration/artifacts/final-snapshot.dump \
  --evidence-dir .local/migration/evidence \
  --source-service "$BT_MIGRATION_SOURCE_SERVICE" \
  --source-database "$BT_MIGRATION_SOURCE_DATABASE" \
  --target-service "$BT_MIGRATION_TARGET_SERVICE" \
  --target-database "$BT_MIGRATION_TARGET_DATABASE" \
  --maintenance-service "$BT_MIGRATION_MAINTENANCE_SERVICE" \
  --maintenance-database "$BT_MIGRATION_MAINTENANCE_DATABASE"
```

Raise the database-level one-writer fence after the final snapshot. The tool
executes a role-scoped `REVOKE CONNECT`, sets both canonical writer roles to
`NOLOGIN`, terminates existing sessions for those roles, and verifies writer
login and session state before it records the fence. It does not claim an
effective `CONNECT` result because PostgreSQL may grant it through `PUBLIC`:

```sh
.local/bin/migration-tool fence \
  --manifest migration/manifest.json \
  --evidence-dir .local/migration/evidence \
  --source-service "$BT_MIGRATION_SOURCE_SERVICE" \
  --target-service "$BT_MIGRATION_TARGET_SERVICE" \
  --source-database "$BT_MIGRATION_SOURCE_DATABASE" \
  --target-database "$BT_MIGRATION_TARGET_DATABASE" \
  --source-writer-role "$BT_MIGRATION_SOURCE_WRITER_ROLE" \
  --target-writer-role "$BT_MIGRATION_TARGET_WRITER_ROLE"
```

Apply the repository-owned route mapping. Immediately before any irreversible
write, the tool repeats the source high-water probe and compares its digest with
the final-delta evidence. A mismatch stops before backfill, restores the source
writer, records `SOURCE_HIGH_WATER_MISMATCH`, returns to `restored`, and requires
a new final delta. A match invokes the production backfill without resolving or
printing opaque credential references:

```sh
.local/bin/migration-tool backfill \
  --manifest migration/manifest.json \
  --backfill-mapping migration/backfills/account-route-v1.json \
  --evidence-dir .local/migration/evidence \
  --source-service "$BT_MIGRATION_SOURCE_SERVICE" \
  --source-database "$BT_MIGRATION_SOURCE_DATABASE" \
  --target-service "$BT_MIGRATION_TARGET_SERVICE" \
  --target-database "$BT_MIGRATION_TARGET_DATABASE" \
  --gateway-service "$BT_MIGRATION_GATEWAY_SERVICE" \
  --gateway-database "$BT_MIGRATION_GATEWAY_DATABASE" \
  --source-writer-role "$BT_MIGRATION_SOURCE_WRITER_ROLE" \
  --target-writer-role "$BT_MIGRATION_TARGET_WRITER_ROLE"
```

The backfill stages deterministic Platform routes and a Gateway operation in
`prepared` state with only `pending_snapshot_id` populated. It commits Gateway
before Platform. If the process exits, or the Platform commit fails, between
those commits, Gateway can retain one harmless pending-only snapshot while the
Platform transaction remains unapplied. That snapshot cannot enter order
handoff or execution gating because those paths read only `active_snapshot_id`;
it is visible through runtime diagnostics for recovery. Retry the exact command
with the same mapping bytes and identities. The stable operation ID, snapshot
ID, and request digest make Gateway replay the existing pending row and let the
Platform write converge. Changed input is rejected as an operation conflict.

| Failure point | Durable state | Identical retry |
| --- | --- | --- |
| Before Gateway commit | Neither database is committed | Stages both transactions again |
| Gateway commit outcome is ambiguous | Gateway has either no row or the same pending-only row; Platform is not committed | Accepts an absent row or verifies the existing operation, snapshot, and digest, then converges Platform |
| After Gateway commit, before Platform commit | Gateway has the pending-only row; Platform is not committed | Reuses the identical Gateway row and commits Platform |
| After Platform commit, before the workflow event is recorded | Both databases contain the deterministic plan; Gateway remains pending-only | Replays the same database plan idempotently, then records the workflow event |
| After the workflow event is recorded | Both databases and migration evidence are committed | Returns the recorded workflow replay without database commands |

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
`retire` execution command. G010 is a no-trade validation gate; it does not
authorize G011 Test writes, activation, retirement, or Live private writes.

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
  --target-service "$BT_MIGRATION_TARGET_SERVICE" \
  --source-database "$BT_MIGRATION_SOURCE_DATABASE" \
  --target-database "$BT_MIGRATION_TARGET_DATABASE" \
  --source-writer-role "$BT_MIGRATION_SOURCE_WRITER_ROLE" \
  --target-writer-role "$BT_MIGRATION_TARGET_WRITER_ROLE"
```

Rollback executes the source-role `GRANT CONNECT`, restores `LOGIN`, verifies
source login/session state, and verifies that the target canonical writer
remains fenced. The evidence does not claim effective `CONNECT` verification.
After the first backfill, rollback is rejected and recovery must proceed
forward in the target.

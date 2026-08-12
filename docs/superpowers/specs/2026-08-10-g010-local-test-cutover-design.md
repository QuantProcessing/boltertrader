# G010 Local Test Cutover Design

## Status

The user approved the recommended local Test cutover architecture on
2026-08-10. This document is the written review contract for G010. It
supersedes conflicting activation and writer-role statements in
`docs/guides/docker-free-migration.md` and Task 15 of
`docs/superpowers/plans/2026-08-09-platform-secret-store-gateway-runtime.md`.

Implementation does not proceed from this document until the user has reviewed
this written form.

## Outcome

G010 moves the frozen local Test state from `funding-trader` into the
BolterTrader Platform and Gateway databases, proves that the old writer is
fenced, activates the new Platform writer through a real idempotent operation,
and verifies the Docker-free stack and browser twice across restart boundaries.

G010 ends with:

- the source Test writer unable to log in or hold sessions;
- the Platform Test runtime connected as a non-owner and holding the dedicated
  canonical-write capability;
- deterministic Gateway pending snapshots promoted to durable active heads;
- the Platform reporting canonical writes ready from database authority;
- Test browser read, authentication, restart, stream-gap, recovery, and
  idempotency checks passing;
- Live private writes and all venue-order routes remaining disabled;
- no source repository deletion, archival, or retirement action.

Real Test venue trading remains G011. Source retirement and final repository
cleanup remain outside G010.

## Non-Negotiable Safety Boundary

The cutover is local and Test-only. Every service, database, role, state path,
URL, manifest, and evidence path must carry an exact normalized Test identity.
Mixed-case or tokenized `live` identities fail before any mutation.

G010 must not:

- start, install, inspect, or depend on Docker;
- read credentials from a repository `.env` file;
- place, amend, cancel, or close a venue order;
- enable any Live private-write flag;
- print credentials, opaque secret references, connection secrets, or raw
  evidence command output that may contain them;
- delete or retire the frozen `funding-trader` source repository;
- restore the source writer automatically after the forward-only boundary.

The frozen source commit and tree remain
`e29f4ccb3f21e53e1424642c10b11e65a1b6a52a` and
`99821acf574c87741b08ae3dc3a913f12b2835d7`. Import provenance must preserve
the source history under `legacy/funding-trader` before cutover evidence is
accepted.

## Rejected Alternatives

### Keep The Current Writer Role Model

The current runtime connects as `bolter_platform_test`, while the migration
fences a different `bolter_platform_test_writer` login role. That proves
nothing about the process that can actually write. Fencing the runtime itself
would also make pre-activation browser validation impossible. This model is
rejected.

### Relax The Test Fence

Allowing the Platform runtime to own tables or retain canonical DML before
activation would make the local Test result nonportable to Live and could hide
a split-brain writer. Test uses the same authority model as Live, with only the
venue environment and write policy differing. This model is rejected.

### Stop At `activation-ready`

A manifest phase and caller-supplied timestamps do not activate a system.
Stopping before durable Gateway promotion and the Platform role grant would
leave G010 incomplete. Caller-authored activation proof is rejected.

## PostgreSQL Authority Model

The local Test services use these exact identities:

| Purpose | Service | Database | Login role |
| --- | --- | --- | --- |
| Frozen source migration | `funding_source_test` | `bolter_funding_test` | `bolter_funding_test_migrator` |
| Platform migration | `platform_target_test` | `bolter_platform_test` | `bolter_platform_test_migrator` |
| Gateway migration | `gateway_test` | `bolter_gateway_test` | `bolter_gateway_test_migrator` |
| Database recreation | `platform_maintenance_test` | `bolter_maintenance_test` | `bolter_platform_test_admin` |
| Platform application | existing Platform service | `bolter_platform_test` | `bolter_platform_test` |
| Gateway application | existing Gateway service | `bolter_gateway_test` | `bolter_gateway_test` |

Canonical write authority uses two additional roles:

| Role | Login | Responsibility |
| --- | --- | --- |
| `bolter_funding_test_writer` | disabled after fence | Frozen source canonical writer |
| `bolter_platform_test_writer` | permanently `NOLOGIN` | Capability role holding canonical Platform DML grants |
| `bolter_gateway_test_writer` | permanently `NOLOGIN` | Capability role holding Gateway operational DML grants |

The maintenance role may recreate the target database but does not run the
application. The target migrator owns the target database, `public` schema,
tables, sequences, and migration metadata. The Platform runtime never owns a
database, schema, table, or sequence.

`bolter_platform_test_admin` is a Test-only
`LOGIN CREATEDB CREATEROLE NOINHERIT` role, not a superuser. Cluster bootstrap
gives it explicit membership in the source and target owner roles and
`pg_signal_backend`, plus `ADMIN OPTION` only for the enumerated source writer,
target runtime, and target writer-capability roles. This role topology is part
of the versioned authority matrix and is verified before mutation. The target
migrators remain `NOCREATEDB NOCREATEROLE` and do not receive backend-signal or
role-admin authority. The migration tool
connects to the maintenance database and verifies its initial `current_user`.
For deletion it executes `SET ROLE` to the exact migrator owner, drops only
that owner's exact Test database, then executes `RESET ROLE`. For creation it
runs as the maintenance admin so `CREATEDB` remains effective and specifies the
target migrator in `CREATE DATABASE ... OWNER`; owner-role membership permits
that owner selection. Every role transition is verified and recorded.

The maintenance admin also owns session fencing. It revokes runtime `CONNECT`
through the exact database owner role, resets to the admin identity, and uses
its otherwise non-inherited `pg_signal_backend` membership through a bounded
`SET ROLE` only after selecting backends that match both the exact Test database
and exact runtime login role, excluding its own PID and rejecting superuser
roles. It resets to the admin identity and rechecks the catalogs afterward. An
unexpected identity, an unterminable backend, or any residual matching session
fails closed. Migrator and runtime roles never receive
`pg_signal_backend`. The admin has no application DML grants, Secret Store
access, or service-runtime use. Although `CREATEROLE` is cluster-level
authority, the guarded cutover accepts only the matrix-enumerated Test roles
and rejects superuser, replication, bypass-RLS, or unexpected role targets.
Equivalent Gateway recreation uses the same audited maintenance boundary and
the Gateway migrator owner role.

The earlier source-writer fence uses this same maintenance-admin signal path:
set the enumerated source writer to `NOLOGIN`, revoke its exact connection
authority through the source owner,
select only exact source database/source writer sessions, reject superuser or
unexpected identities, terminate through bounded `SET ROLE
pg_signal_backend`, reset, and prove zero remaining sessions.

The same separation applies to Gateway. `bolter_gateway_test_migrator` owns the
Gateway database, schema, tables, sequences, and migration metadata and is the
only activation actor allowed to perform the offline store-only promotion.
`bolter_gateway_test` is a non-owner application login. Before activation it
has read access but is not a member of `bolter_gateway_test_writer`, so a
profile mistake still cannot update pending operations, active heads, runtime
state, handoffs, or commands. Activation grants that writer capability only
after Gateway is stopped and the deterministic promotion has completed.

Platform and Gateway application processes never execute DDL or database
migrations. Dedicated migration commands connect as their migrator roles before
an application process starts. Runtime startup verifies the exact schema
version, migration-set digest, ownership matrix, and grants; it fails closed on
drift. Existing startup-time migration calls must be split into migrator-only
commands and runtime verification.

Every target recreation must perform and verify this sequence:

1. terminate only sessions connected to the exact Test target database;
2. drop the exact Test target database;
3. create it with `OWNER bolter_platform_test_migrator`;
4. revoke `CONNECT` on the database from `PUBLIC`;
5. revoke `CREATE` on schema `public` from `PUBLIC`;
6. grant enumerated connection and schema privileges only;
7. restore with `--no-owner --no-privileges` as the target migrator;
8. apply all Platform migrations as the target migrator;
9. grant read and explicitly permitted noncanonical write privileges to the
   runtime;
10. grant canonical table and sequence DML only to the NOLOGIN writer
    capability role;
11. verify database, schema, table, and sequence ownership plus effective
    privileges from PostgreSQL catalogs.

Gateway provisioning follows the same recreate, `PUBLIC` revocation,
migrator-owner, runtime-non-owner, and enumerated grant procedure. Both
application login roles are explicitly `INHERIT`; before activation they have
no writer-role membership to inherit. Any `NOINHERIT` runtime configuration is
a preflight failure and must be corrected before validation, not worked around
with `SET ROLE` in application transactions.

An exhaustive versioned authority matrix is the single source of truth for
these grants and checks. Its Platform section is bound to schema version 15;
its Gateway section is bound to the exact migration-set digest. It lists every
application schema table, sequence, and write-capable routine as one of:

- `canonical`: immutable during pre-activation validation and writable only
  through the writer capability after activation;
- `validation-auth-session`: narrowly writable by the runtime before
  activation;
- `migration-only`: never writable or executable by an application runtime.

The matrix contains the precise privileges for migrator, runtime, writer
capability, and `PUBLIC`. Provisioning and readiness enumerate actual catalog
objects and fail on a missing matrix entry, an absent declared object, an
unexpected new object, or privilege drift. Default privileges for future
objects are revoked from `PUBLIC` and application runtimes. Schema migrations
must update this matrix in the same change.

The Platform runtime receives `CONNECT`, schema `USAGE`, and read access before
activation. It may write only the authentication session during validation.
Bootstrap identity creation is
performed before validation under migration authority, and Platform startup in
validation profile does not upsert identities or run canonical reconcilers.

The exact permitted pre-activation write set is:

- `platform_sessions`: create, refresh, and revoke the validating browser
  session.

`platform_audit` is canonical and immutable during pre-activation validation.
The validation profile does not emit login audit rows; authentication evidence
is captured outside canonical application tables.

No account, route, command, work, reservation, portfolio, ledger, position,
order, funding, credential-operation, or Gateway active-head write is permitted
before activation.

## Runtime Write Readiness

`bolter_platform_test_writer` is a capability role, not a service identity. It
remains `NOLOGIN` before and after activation. Before activation,
`bolter_platform_test` is not a member. Activation grants membership to the
runtime role.

Platform derives canonical-write readiness from durable PostgreSQL state. Role
membership is necessary but not sufficient:

```sql
SELECT pg_has_role(current_user, 'bolter_platform_test_writer', 'USAGE');
```

Startup verifies all of the following: the runtime role is `INHERIT`, writer
role `USAGE` is effective without `SET ROLE`, the capability role has the complete
migration-defined grant set, `has_table_privilege(current_user, ...)` and
`has_sequence_privilege(current_user, ...)` report the required effective
permissions for every canonical object, and the runtime owns no canonical
object. A missing membership, disabled inheritance, incomplete grant, or
missing effective privilege makes canonical mutation endpoints and background
writers fail closed while health and read endpoints remain available. An
environment variable cannot assert write readiness. Pre-activation validation
proves the inverse effective privilege matrix, not membership alone.

Gateway uses the same effective-permission rule for
`bolter_gateway_test_writer`. Its process can be healthy and serve reads
without writer readiness, but reconciliation, runtime-state mutation,
handoffs, and commands remain fail-closed until both membership and effective
privileges are present.

The application exposes the result as a named readiness component. Test and
Live use the same check. Live still requires its independent private-write
policy flags, which remain disabled throughout G010.

## Frozen Source Inventory

The frozen funding source legitimately has no `platform_accounts` table.
Backfill checks the qualified relation with
`to_regclass('public.platform_accounts')`:

- absent relation means an explicitly proven empty Platform account inventory;
- present relation with the complete expected schema is read and backfilled;
- an unqualified, partial, or incompatible relation fails closed;
- absence never skips creation of the target Platform schema.

After restore, the target migrator applies the complete Platform migration set
through schema version 15 before account/route backfill. Backfill remains
deterministic and idempotent. An empty source produces zero account snapshots,
not fabricated accounts or credential references.

## Cutover State Machine

The durable phase order remains:

```text
baseline
  -> provenance-ready
  -> backed-up
  -> restored
  -> delta-captured
  -> fenced
  -> backfilled
  -> validated
  -> activation-ready
  -> activated
```

The G010 driver loops until `activated`, revalidating completed phase evidence
and current postconditions before moving forward. It rejects
`retirement-ready`. Replaying a completed operation must either prove the same
fingerprint and postconditions or fail; it must never silently replace prior
evidence.

`activation-ready` may contain one authoritative `activationAttempt` in the
manifest. The tool creates it with an operation-generated ID, UTC start time,
input fingerprint, expected Gateway promotion digest, and state `begun` before
the first non-atomic activation boundary. Atomic manifest persistence and the
existing event-chain fingerprint make this record authority for recognizing a
legitimate partial activation. Its boundary advances through
`gateway-promoted`, `writers-granted`, `connect-restored`,
`services-started`, and `verified` after each independently checked
postcondition. If a crash leaves external state exactly one boundary ahead,
retry verifies that postcondition and ratchets the same attempt forward. A
second attempt, changed input, skipped boundary, or incompatible external state
is rejected.

The forward-only boundary is the first successful backfill after the source
fence. Before that boundary, an explicit rollback may restore source login only
after proving the target writer remains disabled. At and after that boundary,
the source remains fenced and recovery proceeds forward in the target.

## Pre-Activation Validation

Validation runs the real Docker-free Platform, Gateway, Funding worker, and web
stack with exact Test configuration. Platform and Gateway use explicit
migration-validation startup profiles. The Platform profile:

- requires the runtime to lack writer-role membership;
- disables bootstrap identity upsert and canonical background writers;
- leaves authentication, health, readiness, catalog, account reads, portfolio
  reads, audit reads, and SSE available;
- returns a stable not-ready/forbidden response for canonical mutation routes.

The Gateway profile serves health, catalog, market data, capability, and
durable snapshot reads, but disables desired-state reconciliation, candidate
construction, pending-to-active promotion, private runtime creation, and order
admission. The profile is valid only in exact Test mode and cannot assert
canonical write readiness. Every Live and private venue-write gate remains
disabled in both profiles.

The browser flow signs in, checks the authoritative session mode, reads all
product and account surfaces, exercises refresh and stream resnapshot behavior,
and verifies credential redaction. It must not invoke account validation or any
other account/private mutation. Login/session writes are recorded as the
enumerated noncanonical exception.

Each validation cycle captures digests of all canonical Platform tables and
Gateway active heads before and after the browser flow. The digests must remain
unchanged. The authentication-session table is checked separately: only the
validating session's lifecycle rows may change. Migration-only objects must not
change. Two complete cycles are required with an owned service restart between
them. PID/start-time/command ownership, readiness, stream-gap recovery, and
idempotent resnapshot evidence must be current rather than replayed from an
older run.

Successful validation records `activation-ready`; it does not grant write
authority.

## Canonical Activation Operation

`migration-tool activate` replaces caller-authored activation timestamps and
external proof. It is a canonical executed migration operation, using the same
unexported execution capability as backup, restore, fence, and backfill. The
CLI does not accept `--activation-at`, `--cutover-at`, or arbitrary proof as a
substitute.

The operation performs these ordered boundaries:

1. acquire the manifest lock and a Test-scoped PostgreSQL advisory lock;
2. verify phase `activation-ready` and revalidate all prior evidence;
3. create or resume the matching authoritative manifest `activationAttempt`;
4. verify the source writer is `NOLOGIN` with zero sessions;
5. on a new attempt, verify both target runtimes lack effective writer-role
   `USAGE` and fail the matrix-defined zero-row DML probes; on resume, accept
   an existing membership only when the
   matching manifest attempt exists and all prior boundary postconditions are
   independently true;
6. stop the owned Platform, Funding worker, Gateway, and web processes while
   preserving PostgreSQL;
7. revoke target runtime `CONNECT`, terminate sessions for the exact Platform
   and Gateway runtime roles in their exact Test databases, and verify zero
   remaining sessions; an unowned or unterminable session fails closed;
8. promote every deterministic Gateway pending snapshot to its matching durable
   active head using Gateway store transactions only, without constructing a
   venue client or invoking a venue API;
9. verify the complete pending/active head digest and operation set;
10. in one PostgreSQL transaction, grant
    `bolter_platform_test_writer` membership to `bolter_platform_test` and
    `bolter_gateway_test_writer` membership to `bolter_gateway_test`, then
    ratchet the manifest attempt to `writers-granted` after commit;
11. verify both runtime roles have the complete effective DML matrices by role
    identity while both still have zero sessions;
12. restore the enumerated target runtime `CONNECT` grants and verify no session
    appeared before supervised startup;
13. start the owned Docker-free stack in normal Test profile;
14. verify Platform and Gateway derive canonical-write readiness from their
    memberships, `INHERIT` state, and effective grants;
15. prove the source remains fenced, the target runtimes can perform their
    matrix-defined zero-row canonical DML probes, Gateway heads match the
    promoted set, and all Live and venue-write gates remain disabled;
16. generate activation evidence and atomically record phase `activated`, with
    the completed attempt embedded in the event-chain fingerprint.

Promotion is valid only for a pending snapshot whose account, operation ID,
desired-state revision, runtime generation, credential version, route set, and
input fingerprint match the backfill evidence. Existing matching active heads
are replay success. Conflicting active heads, partially described pending
snapshots, or unexpected extra pending snapshots fail closed.

Durable active-head promotion asserts configuration ownership only. It does not
assert that a private venue stream is connected or that an account route can
accept an order. After normal Test startup, Gateway reconstructs each active
runtime through its existing recovery path; route write admission remains
fail-closed until its generation, capability, reconciliation, and private-stream
barriers are current. G010 does not weaken those barriers or require an account
to become venue-ready. G011 selects only routes that independently reach ready.

The permission probe is deliberately side-effect-free. For each representative
canonical table, the authority matrix supplies `UPDATE ... WHERE false`,
`DELETE ... WHERE false`, and `INSERT ... SELECT ... WHERE false` statements
that exercise privilege checks without producing rows or firing row triggers.
Sequence permission is checked only through PostgreSQL catalogs and
`has_sequence_privilege`; the probe never calls `nextval`, because sequence
increments are not rolled back.

For an empty frozen source, the proven promotion set is empty. The activation
operation still executes all writer fencing, role, restart, readiness, and
evidence checks.

## Crash Recovery

Activation writes a redacted, fsynced progress journal beside the manifest.
Every step records its input fingerprint and verified postcondition before the
next boundary. The manifest `activationAttempt` authorizes recognition of a
partial activation; PostgreSQL, Gateway storage, and supervisor state prove how
far it actually progressed. The journal accelerates diagnosis but is never
authority by itself.

Required retry behavior:

| Crash boundary | Retry behavior |
| --- | --- |
| After attempt creation, before Gateway promotion | Reverify the matching manifest attempt and source fence, then promote the same set. |
| During Gateway promotion | Replay matching heads; reject any conflicting head. |
| After promotion, before role grant | Preserve source fence and grant once. |
| After the writer-grant transaction, before reconnect/restart | Require the matching manifest attempt, keep application processes stopped and runtime `CONNECT` revoked, verify zero sessions and both effective matrices, ratchet `writers-granted` if the commit preceded a crash, then start. |
| After restart, before manifest update | Reverify the running stack and record the same activation evidence. |
| After manifest update | Return replay success only when every live postcondition still holds. |

No failure path restores the source writer once the forward-only boundary has
been crossed. If post-grant startup or verification fails, the target services
are stopped again, runtime `CONNECT` is revoked, exact runtime sessions are
terminated and proven absent, the source remains fenced, and the operator
retries the same activation attempt. Writer memberships may remain as observed
partial state, but cannot be exercised while connection authority is revoked.

## Evidence Contract

Generated activation evidence contains no caller-authored assertions. It binds:

- manifest epoch, prior phase, prior event-chain fingerprint, and import
  provenance;
- authoritative activation-attempt ID, start time, input fingerprint, and
  expected promotion digest;
- exact normalized Test service/database/role identities;
- schema-bound authority matrix digest, target ownership, catalog inventory,
  default privileges, and effective privilege results;
- source writer login/session result;
- Platform and Gateway runtime `INHERIT` state, role memberships, and effective
  privilege matrices before and after activation;
- Gateway pending and active head digests before and after promotion;
- supervisor process identity and readiness results before and after restart;
- canonical table digest results from pre-activation validation;
- exact Test mode plus disabled Live, private, and venue-write gate results;
- operation-generated UTC timestamps and command-result digests;
- the activation operation fingerprint and final proof SHA-256.

The evidence writer uses an atomic same-directory temporary file, fsync, rename,
and directory sync. Manifest recording requires the generated proof SHA and
operation fingerprint returned by the executing operation. Symlinks,
world-readable files, arbitrary external files, caller timestamps, stale
proofs, changed Git provenance, and changed current postconditions are rejected.

`record-activation-evidence` is not a public success path for G010. Existing
legacy manifests may be read for diagnostics, but a fresh G010 cutover reaches
`activated` only through `migration-tool activate`.

## Failure Handling

- A source writer session that cannot be terminated blocks the fence.
- Incorrect ownership, residual `PUBLIC` privileges, runtime object ownership,
  or an incomplete capability-role grant set blocks restore/activation.
- A partial source Platform schema blocks backfill; a proven absent table is
  empty inventory.
- Browser mutation outside the enumerated auth tables blocks validation.
- Canonical table or Gateway active-head digest drift blocks validation.
- Any Live token, Live flag, non-loopback URL, wrong database/role, Docker
  command, or venue-order route blocks before mutation.
- Failed activation never emits `activated` evidence and never prepares source
  retirement.

Errors and evidence report stable reason codes and redacted identities. They do
not include DSNs, credentials, opaque secret references, SQL parameter values,
or venue payloads.

## Verification And Acceptance

Implementation is test-driven and must prove all of the following before the
real local Test cutover:

1. Unit and shell-contract tests reject mixed-case Live identities, arbitrary
   activation proofs, caller timestamps, Docker, venue writes, wrong roles,
   wrong paths, and wrong phase order.
2. Real PostgreSQL tests prove explicit database ownership, revoked `PUBLIC`
   privileges, migrator object ownership, runtime non-ownership, and the exact
   pre/post activation privilege matrix.
3. A source dump without `public.platform_accounts` migrates through Platform
   schema version 15 and produces a deterministic empty inventory; a partial
   relation fails closed.
4. Pre-activation, the source writer cannot connect, Platform can authenticate
   and read, auth session writes succeed, canonical Platform DML and Gateway
   operational DML fail, and Gateway active heads do not change.
5. Validation starts and verifies the real stack twice across a restart without
   canonical digest drift or account/private mutation.
6. Activation failpoint tests cover every crash boundary listed above and prove
   that identical retry converges exactly once.
7. Post-activation, target role `USAGE`, effective privilege checks, and
   matrix-defined zero-row DML probes succeed, source login and writes fail,
   Gateway heads equal the proven promotion set, and both application
   readiness components report canonical writes ready.
8. Two post-activation real-stack/browser cycles pass across restart while no
   venue order, private execution command, or Live write is emitted.
9. Logs, evidence, browser output, screenshots, support artifacts, metrics, and
   traces contain no credential or opaque-reference canary.
10. Focused Go, race, shell, web unit, production build, and real Playwright
    suites pass, followed by `git diff --check` and scoped formatting checks.

Only after those checks pass may the generated `migration/manifest.json` be
committed and G010 be checkpointed. The aggregate Ultragoal remains incomplete
until G011 and G012 finish.

## Implementation Surface

The implementation plan may refine exact file splits, but ownership stays
within these boundaries:

- `migration/`: phase, canonical activation executor, evidence, privilege and
  replay verification;
- `cmd/migration-tool/`: guarded `activate` CLI surface;
- `cmd/account-route-backfill/`: qualified absent-source inventory handling;
- `backend/internal/config` and `backend/internal/app`: validation startup
  profile and database-derived canonical-write readiness;
- `backend/internal/store/postgres`: explicit capability checks and canonical
  object/grant inventory;
- `cmd/exchange-gateway`, `gateway/config`, `gateway/process`, and
  `gateway/account`: migrator/runtime startup split, validation profile, and
  database-derived write gating;
- `gateway/store/postgres`: authority inventory, runtime permission checks, and
  store-only deterministic migration promotion;
- `scripts/g010-cutover`: ordered idempotent driver and Test safety gates;
- `web/e2e`: nonmutating pre-activation and post-activation browser workflows;
- `docs/guides/docker-free-migration.md`: operator procedure aligned with this
  contract;
- `migration/manifest.json`: command-generated cutover result only.

The existing uncommitted Task 15 driver work is input to this implementation,
not accepted evidence. It must be reconciled with this design through failing
tests before reuse.

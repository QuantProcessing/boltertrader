# Platform Secret Store and Gateway Runtime Design

## Status

The architecture direction and written contract in this document are approved.
Implementation planning proceeds from this document as the authoritative V1
boundary.

## Problem

BolterTrader is now a private, authenticated product platform whose web shell
hosts multiple trading products. Funding Trading and Manual Trading share
accounts, instruments, market data, positions, reservations, accounting, and
venue execution. The current code has the correct high-level Platform/Gateway
boundary, but its production wiring and account runtime model are incomplete:

- Platform production does not construct a usable Secret Store provider;
- Gateway production still defaults to an environment-backed provider and
  cannot resolve Platform-issued opaque credential references;
- one Gateway account currently owns one product client handle, so an account
  cannot atomically expose Spot, Perp, and settlement routes;
- public instrument refresh is performed per account, duplicating venue calls;
- Gateway process readiness is conflated with having at least one active and
  ready account.

The design must support two Binance accounts, heterogeneous account routes,
and future products without giving each product its own venue clients or
turning Gateway into the product platform.

## Decision Summary

1. Platform owns user authentication, account metadata, product policy,
   desired state, credential CRUD, reservations, commands, accounting, and
   audit.
2. A mode-isolated encrypted filesystem Secret Store is the V1 provider. Its
   interfaces permit replacement with Vault or KMS without changing account or
   Gateway contracts.
3. Gateway is a product-agnostic execution kernel. It receives opaque secret
   references, resolves credential material only in memory, owns venue
   connectivity and runtime truth, and never owns user-facing credential CRUD.
4. Public connectors are shared by venue, Test/Live mode, and route profile.
   Private runtimes are isolated by account and route.
5. All active routes for one account generation are prepared and promoted as
   one atomic runtime group. A partially ready candidate cannot accept writes.
6. Process/control-plane readiness is independent of account inventory.
   Starting with zero accounts is healthy; trading remains fail-closed until a
   requested account route is eligible and ready.
7. Demo and Testnet are one Platform policy mode, `Test`. The native venue
   environment label is retained only to select adapter endpoints. `Live` is a
   separate mode, with separate storage, keys, tokens, roles, audit, and write
   policy.

## Alternatives Rejected

### Gateway Owns Accounts And Credential CRUD

This would make Gateway the product control plane. Product policy,
authentication, account presentation, reservations, and credential lifecycle
would accumulate beside venue execution, making every product change a Gateway
change. It is rejected even though it would reduce one internal boundary.

### Platform Or Each Product Owns Venue Clients

This would let products resolve credentials directly, duplicate public catalog
and market-data requests, fragment venue rate-limit budgets, and create
competing runtime truth. It is rejected even though direct SDK calls initially
look simpler for one product.

## Architectural Boundary

```text
Authenticated browser
        |
        v
Platform API
  - identity and session
  - account metadata and product policy
  - credential create/rotate/delete
  - desired state and reconciliation
  - reservations, commands, ledger/PnL, audit
        |
        | opaque secret references + account route groups
        v
Gateway internal authority
  - public connector registry and rate limits
  - private account route runtimes
  - atomic generation activation and write fences
  - catalog, market data, private facts, durable execution
        |
        v
Venue APIs

Encrypted filesystem Secret Store
  ^ Platform has scoped write/delete capability
  |
  + Gateway has scoped resolve capability
```

Gateway is not an HTTP proxy around exchange SDKs. It is the shared execution
kernel for connection ownership, capability discovery, normalized venue facts,
write fencing, and reconciliation. It remains unaware of Funding, Manual, or
future product semantics.

### Ownership Matrix

| Concern | Authoritative owner | Constraint |
| --- | --- | --- |
| User authentication and session | Platform | Gateway is never browser-facing. |
| Account name and product eligibility | Platform | Product assignment is policy, not a venue client. |
| Credential create, rotate, delete | Platform | Values are write-only and never returned. |
| Encrypted secret bytes | Secret Store provider | Ordinary databases contain references only. |
| Credential resolution for venue clients | Gateway resolver | Material exists only in bounded in-memory construction paths. |
| Desired account/route state | Platform | Gateway persists an idempotent execution mirror. |
| Runtime route state and venue blockers | Gateway | Platform reconciles rather than inventing runtime truth. |
| Catalog, market data, venue rate limits | Gateway | One public connector feeds all accounts and products. |
| Product reservations and attribution | Platform | Gateway does not interpret product ownership. |
| Orders, private events, reconciliation | Gateway | Every write is account-route and generation fenced. |

## Secret Store

### Provider Interfaces

The account and runtime layers depend on narrow capabilities rather than one
all-powerful provider:

```go
type Scope struct {
    Mode       Mode
    Namespace  string
    KeyVersion string
}

type CredentialWriter interface {
    Check(context.Context, Scope) error
    Put(context.Context, Scope, OperationID, []byte) (OpaqueRef, error)
    LookupOperation(context.Context, Scope, OperationID) (OpaqueRef, error)
    Delete(context.Context, Scope, OpaqueRef) error
}

type CredentialResolver interface {
    Check(context.Context, Scope) error
    Resolve(context.Context, Scope, OpaqueRef) ([]byte, error)
}
```

`OperationID` is a Platform-generated, mode-scoped, canonical UUID represented
in lowercase text. It is validated before provider access and is never reused
for another account, operation kind, or target credential version.

Platform receives `CredentialWriter`; Gateway receives
`CredentialResolver`. Production wiring returns dedicated facet wrappers whose
dynamic method sets contain only the named capability. This narrows accidental
dependencies and makes later provider replacement testable, but a Go interface
is not a security boundary.

V1 trusts the Platform process, Gateway process, and the local OS identity that
owns the Secret Store. Because both services can access the same filesystem and
master key, compromise of either trusted process can bypass an interface
facet. Browser code, Funding workers, product modules, and other local users
remain outside that trust boundary and receive neither provider access nor key
material. Strong isolation between Platform and Gateway would require separate
OS identities plus ACLs or a broker/Vault/KMS deployment; that is intentionally
deferred without changing the interfaces above.

A future Vault/KMS implementation must satisfy the same scope,
operation-idempotency, and opaque-reference behavior.

`OpaqueRef` is a provider-generated stable identifier, not a path or an
encoded credential. Callers may persist it, compare it, and produce a one-way
fingerprint for diagnostics. They must not parse it to infer mode, namespace,
account, venue, or provider layout. Scope is supplied independently from the
process's trusted mode configuration, preventing a crafted reference from
selecting another mode.

The filesystem provider accepts only its canonical fixed-length reference
encoding and derives the storage filename from a cryptographic digest. It never
concatenates caller-controlled reference text into a filesystem path. Unknown,
non-canonical, or path-like references fail before filesystem access.

### Credential Bundle

One secret reference identifies one versioned venue credential bundle, not one
undifferentiated API-key string. The canonical decrypted payload is bounded
JSON with this logical shape:

```text
CredentialBundleV1
  schemaVersion             exactly 1
  venue                     normalized venue identity
  authKind                  adapter-registered authentication scheme
  metadata                  strictly typed non-secret authentication fields
  secrets                   strictly typed secret authentication fields
```

Each adapter registers the allowed fields, types, maximum lengths, and
sensitivity for its `venue + authKind`. Unknown fields, duplicate keys, type
coercion, oversized payloads, and a bundle whose venue does not match the
account fail before client construction. The schema can express ordinary API
key/secret/passphrase credentials and venue-specific inputs such as account or
key indexes, signer addresses, private signing keys, and subaccount selectors.
Non-secret authentication metadata remains inside the encrypted bundle because
it can still identify an account.

Platform validates the envelope shape against the adapter schema without
logging or persisting its fields. Gateway repeats validation, then converts the
bundle to an adapter-owned typed credential object. The resolved byte slice and
temporary parser buffers are cleared after construction. Existing signing SDKs
retain API keys, passphrases, or private keys for the lifetime of the runtime;
the design does not claim otherwise. Runtime teardown closes the client and
performs best-effort zeroization only where an SDK exposes mutable buffers.
Go strings and third-party client internals cannot be guaranteed zeroized, so
process isolation, bounded runtime lifetime, crash-safe logs, and prompt
teardown remain part of the protection boundary.

### Encrypted Filesystem Provider

V1 stores one authenticated-encryption envelope per reference beneath an
operator-configured state directory outside the repository. Test and Live use
different directories and never share files. Directories are `0700`; envelope
files, key files, and temporary files are `0600`. Creation uses a same-directory
temporary file, file sync, atomic rename, and directory sync.

Each envelope contains only versioned cryptographic metadata, a random nonce,
ciphertext, and creation metadata. AES-256-GCM authenticates the ciphertext.
Additional authenticated data binds the provider version, mode, namespace,
key version, and opaque reference, so moving a blob between scopes or names
causes decryption to fail closed. Temporary plaintext buffers are copied only
when needed, cleared after typed client construction, and never serialized or
formatted. The typed client retention limits described above still apply.

Master keys come from one explicitly configured `KeySource`:

- OS keychain, preferred for the local private deployment; or
- an operator-controlled absolute file outside the repository, required to be
  a regular file owned by the current user with mode `0600`.

There is no implicit fallback between key sources. Test and Live require
different master keys and key identifiers. A missing key, mismatched key,
unsupported envelope version, invalid permissions, tampering, or cross-mode
lookup makes Secret Store readiness fail. Logs and APIs expose only stable
error codes, never provider paths, raw references, nonces, ciphertext, or
values. The authenticated Platform-to-Gateway desired-state request is the only
API payload allowed to carry a raw opaque reference; handlers redact it from
responses, errors, logs, traces, and metrics.

### Secret Lifecycle

Credential creation is a recoverable operation rather than a synchronous
filesystem/database illusion. Platform requires an idempotency key and orders
the operation as follows:

1. Platform validates account metadata, product policy, and the credential
   bundle without retaining field values.
2. In one database transaction it creates the inactive account draft and a
   `credential_operation` row containing operation ID, account ID, operation
   kind, target credential/runtime versions, state, and timestamps. It contains
   no credential fields or raw payload.
3. Platform calls idempotent `Put(scope, operationId, bundle)`. The provider
   binds a deterministic opaque reference to the operation ID, durably writes
   the envelope, and rejects reuse of the operation ID with different material.
4. Platform commits the returned reference, route definitions, and pending
   desired generation, then marks the credential operation `stored`.
5. Platform sends desired state to Gateway and reconciles the resulting
   runtime state. Creation is not active until the entire route group is ready.
6. After promotion and Platform CAS, the operation becomes `active`.

If the process dies before step 3, the durable operation remains
`awaiting_material`; the user may replay the same request and idempotency key,
or abandon the inactive draft. If it dies after `Put` but before step 4, the
reconciler uses `LookupOperation` to recover the same reference without the
plaintext and completes binding. The filesystem provider derives its reference
from a keyed digest of scope and operation ID, and stores an authenticated
material digest inside the encrypted envelope, so replay cannot create a
second envelope or silently replace the first. Abandoned operations move to a
durable cleanup state before their staged envelope is deleted.

Credential reads do not exist in the browser API or Platform account service.
The browser can replace a credential but cannot retrieve, reveal, download, or
test it outside the account-validation workflow. Validation is delegated to a
non-writing Gateway candidate runtime.

Deletion uses a two-phase tombstone. Platform first marks the credential
`retiring` and forbids it in new desired generations. Gateway durably records a
mode-scoped retired-reference tombstone, closes or fences every active,
candidate, draining, and reconciliation-only generation that used it, waits
for handoff leases, and permanently rejects the reference during activation or
restart recovery. Only after Platform persists the Gateway tombstone revision
does it call idempotent provider `Delete`. A crash repeats the same phase; it
cannot make the reference eligible again. Account removal also requires
Platform reservation/product blockers and Gateway order/position/uncertain-
command blockers to be clear.

## Account And Route Model

### Platform Account

An account represents one independently authenticated venue account. Two
Binance logins are two Platform accounts with different `accountId` and
`secretRef`, even if every other field matches. The minimum durable model is:

```text
Account
  accountId                 stable platform identity
  venue                     normalized venue identity
  mode                      Test | Live, fixed by the running process
  nativeEnvironment         derived read-only demo | testnet | live observation
  displayName               user-facing label
  productPolicy[]           products allowed to reserve/use this account
  secretRef                 current opaque reference
  previousSecretRef         retained only while rotation cleanup is pending
  desiredStateRevision      CAS revision for metadata/state mutation
  runtimeGeneration         atomic account runtime generation
  credentialVersion         monotonically increasing credential generation
  requestedState            creating | active | rotating | draining | drained |
                            disabled | removing | removed
  routes[]                   active or disabled private route definitions
```

`mode` is the Platform security boundary. For CEX adapters, Test commonly maps
to a venue's Demo endpoint; for DEX adapters, it commonly maps to Testnet.
`nativeEnvironment` is derived from the registered route profile and is never
accepted as caller-controlled endpoint selection. Product policy and UI logic
do not branch on Demo versus Testnet.

### Account Route

An account route is a private capability handle, not a product assignment:

```text
AccountRoute
  routeId                    stable within account
  product                    spot | perp
  settlementAsset            normalized settlement currency when applicable
  routeProfileId             credential-free adapter endpoint/capability profile
  requestedState             active | disabled
```

The runtime key is `(accountId, routeId, runtimeGeneration)`. A single account
may own Spot, Perp, and multiple settlement-specific handles where the adapter
requires them. Funding and Manual Trading reference the same account routes;
they do not instantiate their own clients.

The account's `runtimeGeneration` covers the complete active route set.
Route-local generations are deliberately excluded from V1 because they permit
mixed credential generations inside one account and complicate write fencing.
Every route requested as active is required for promotion. Future routes are
added by creating a new account generation; there is no partially promoted
optional route in V1.

### Public Connector Key

The public connector registry key is `(venue, mode, routeProfileId)`. The
route profile contains credential-free endpoint selection and adapter
capabilities; it resolves the venue-native Demo/Testnet endpoint within Test.
It never contains account IDs, secret references, or product IDs.

There is one endpoint authority:

```text
processMode + venue + registered routeProfileId -> nativeEnvironment + endpoints
```

Route profiles are compiled or operator-registered trusted configuration, not
account payload URLs. The legal mode mapping is exhaustive:

| Process mode | Derived native environment | Valid |
| --- | --- | --- |
| Test | demo | yes |
| Test | testnet | yes |
| Test | live | no |
| Live | live | yes |
| Live | demo | no |
| Live | testnet | no |

Platform derives and stores `nativeEnvironment` only for display/audit;
Gateway derives it again and requires equality in the desired-state mirror.
During compatibility migration, a legacy caller-supplied `venueEnvironment`
is accepted only if it equals the unique registered mapping. Missing,
ambiguous, or conflicting mappings fail before secret resolution, connector
startup, or venue traffic.

One connector owns instrument discovery, public order books/tickers, funding
rates, reference prices, and the venue's public rate-limit budget for its key.
Account routes subscribe to its catalog snapshot. Adding a second Binance
account therefore adds private runtimes but does not add a second Binance
instrument poller. Connector refresh is coalesced, cached with explicit age,
and fanned out to Platform projections.

Each adapter declares the complete ordered budget-key vector for an operation.
At minimum it can include:

- venue/profile/IP scope shared by public and private requests;
- authenticated venue-principal or account scope shared by every route and
  credential generation for the same venue account; and
- route/endpoint scope for product-specific limits.

Every request acquires all applicable budgets through one coordinator before
handoff. The adapter derives a stable, non-exported venue-principal fingerprint
during candidate authentication when the venue has account-shared quotas. Old
and candidate generations share that account budget during rotation. If a
required account-level quota identity cannot be derived, the adapter falls
back to a conservative broader budget instead of pretending routes are
independent. Products cannot select keys or bypass any layer.

## Runtime Reconciliation

### Durable Runtime Snapshots

Gateway persists two complete records per account instead of overwriting one
desired row in place:

```text
active_applied
  normalized account identity and derived endpoint mapping
  complete active route definitions and route profiles
  secret reference
  desired revision + runtime generation + credential version
  applied operation ID and promotion timestamp

pending_desired
  operation ID + canonical request digest
  complete proposed identity, routes, profiles, and secret reference
  proposed full fence
  candidate state + redacted route results
```

`active_applied` and the normalized desired payload inside `pending_desired`
are immutable snapshots addressed by their full fence. Candidate state and
route results are versioned observations keyed to that immutable pending
payload; they cannot rewrite its routes, reference, or fence. Preparing or
failing a candidate never mutates `active_applied`. Reusing an operation ID is
accepted only when the canonical normalized request digest is identical;
otherwise Gateway returns an idempotency conflict before provider or network
access.

Promotion first closes old-generation admission and resolves every handoff
lease. Gateway then uses one database transaction and CAS on the expected
active fence to move the previous `active_applied` snapshot to immutable
retiring-generation history, promote `pending_desired` to the new
`active_applied`, and mark the pending operation applied. That durable CAS is
the promotion linearization point. Only after commit does Gateway atomically
publish the matching in-memory routing-table pointer and open new-generation
admission. No generation admits writes between the durable commit and pointer
publication.

On restart Gateway loads retired-reference tombstones and uncertain commands,
reconstructs `active_applied` first, and only then resumes or reports
`pending_desired`. A failed or interrupted pending candidate is rebuilt from
its own complete snapshot; it cannot change the recovered active routes. A
crash after the promotion CAS but before pointer publication restores the new
active snapshot. Retiring-generation history remains until its runtimes,
uncertain commands, and secret references are fully reconciled and torn down.

### Candidate And Atomic Promotion

Gateway maintains an immutable active routing-table snapshot and a candidate
runtime group per account. Creating, changing routes, or rotating credentials
uses the following protocol:

1. Platform commits a pending desired revision and sends an idempotent apply
   request with `operationId`, account identity, exact routes, secret reference,
   desired revision, runtime generation, and credential version.
2. Gateway validates mode, venue, route profiles, monotonic fences, and the
   absence of conflicting operations before persisting the complete
   `pending_desired` snapshot. It does not modify `active_applied`.
3. Gateway resolves the secret under its configured scope and builds every
   active private route as a non-writing candidate. It validates
   authentication, capabilities, initial private state, and required catalog
   compatibility.
4. If any active route fails, Gateway destroys the whole candidate, clears
   plaintext buffers, records redacted per-route reasons, and leaves the old
   active generation unchanged.
5. When every active route is ready and old handoff leases have resolved,
   Gateway performs the promotion CAS described above, publishes the committed
   routing snapshot, and opens admission for the new full fence. The previous
   generation is persisted as reconciliation-only and cannot accept commands.
6. Gateway returns the applied generation and route readiness. Platform uses
   CAS to mark its pending revision active. If a crash occurs between steps 5
   and 6, Platform queries Gateway runtime truth and completes the same
   idempotent reconciliation instead of creating another generation.

No API reports an account as write-ready during partial candidate construction.
Read-only diagnostics may expose each route's state and redacted reason.

Every private command carries:

```text
accountId + routeId + desiredStateRevision + runtimeGeneration
+ credentialVersion + commandId/idempotencyKey
```

Gateway compares the complete fence with the current atomic routing snapshot
while atomically acquiring a generation-scoped handoff lease. Stale, disabled,
draining, candidate, and reconciliation-only generations cannot acquire a
lease and fail before any venue write.

The handoff lease closes the check/write race. Before the SDK call, the command
coordinator durably records `handoff_started` with the full generation fence;
restart recovery treats any nonterminal `handoff_started` command as uncertain.
The lease is retained through the SDK venue call and until the coordinator
durably records exactly one of accepted, rejected, or uncertain. Promotion
first closes admission for the old generation, then waits for all granted
leases to reach one of those durable states. This is the precondition for the
durable promotion CAS; the later pointer publication does not redefine the
linearization point. Commands that have not acquired a lease are rejected or
retried against the new fence. If a granted lease cannot reach a durable
terminal/uncertain state within the bound, promotion remains blocked and the
account stays fail-closed until reconciliation resolves it. After a process
crash, the restored uncertain command is the same blocker even though the
in-memory lease no longer exists. Secret retirement uses the same lease drain
and cannot race a venue handoff.

### Rotation

Rotation writes a new envelope first and retains the previous reference. The
new credential version and runtime generation are prepared using the same
candidate protocol. Failed validation leaves the old generation active and
does not expose the candidate to products. Successful promotion fences the old
generation before Platform considers rotation active. Old secret deletion is
asynchronous and retried only after both Platform and Gateway prove it is
unreferenced.

### Drain, Disable, And Remove

- `draining` rejects new commands but permits private-event consumption,
  reconciliation, cancellation/close operations explicitly authorized by
  Platform, and blocker observation.
- `drained` requires zero relevant open orders, owned positions, uncertain
  commands, and Platform reservations.
- `disabled` keeps metadata and encrypted credentials but creates no active
  private runtime.
- `removing` performs idempotent runtime teardown and secret cleanup.
- `removed` is a terminal tombstone retained for audit and idempotency; it is
  not an active Gateway desired record.

Gateway reports venue blockers; Platform combines them with reservations and
product attribution. Neither side declares removal safe using the other side's
facts by assumption.

## Readiness And Capability Discovery

Readiness is a vector, not one boolean:

1. **Process liveness:** the process and listeners are alive.
2. **Control-plane readiness:** configuration, mode attestation, authentication,
   databases, migrations, Secret Store health, desired-state store, and
   internal handlers are usable.
3. **Public connector readiness:** a connector has a valid, non-expired catalog
   and the required market-data streams or polling paths are healthy.
4. **Account-route readiness:** the exact active generation has a constructed
   client, supported capability, initial private state, and no unresolved
   private-stream gap.
5. **Trading eligibility:** Platform product policy, reservation, instrument,
   account state, route readiness, full write fence, and Live-write policy all
   allow the requested command.

An empty account inventory satisfies process and control-plane readiness. The
Gateway readiness response returns `serviceReady: true` with active and ready
account counts of zero. One unavailable account does not make other ready
accounts or the service unavailable. A trading request for an absent or
unready route still fails closed with a stable, redacted reason.

SDK-managed reconnect does not by itself imply route readiness. During a
private-stream disconnect or sequence gap the route becomes degraded, new
writes are blocked when authoritative state cannot be proven, and the SDK may
reconnect. Readiness returns only after a snapshot/sequence barrier proves that
private state is current. This distinguishes a recoverable local network gap
from a permanently unsupported private stream.

Gateway exposes product-neutral venue capabilities, route profiles,
instruments, connector age, runtime route states, and blocker facts through its
internal authority API. Platform joins those facts with product policy and
account metadata before exposing browser DTOs. The browser never receives a
Gateway hostname/token, secret reference, credential fingerprint, or provider
detail.

## Failure Handling

| Failure | Required behavior |
| --- | --- |
| Master key/provider unavailable | Control-plane readiness fails; no credential mutation or private runtime activation occurs. |
| Secret missing, tampered, or wrong scope | Candidate fails with a stable redacted code; active generation is unchanged. |
| One active route fails | Entire candidate group is destroyed; no partial promotion. |
| Public connector stale | Affected instruments become read/write ineligible according to freshness policy; unrelated connectors continue. |
| Private stream gap | Route becomes degraded and fail-closed until resynchronized. |
| Platform crashes before apply | Pending desired state is retried with the same operation ID. |
| Gateway crashes during candidate build | Restart reconstructs `active_applied` first and resumes the separate complete `pending_desired`; no partial candidate is active. |
| Gateway crashes after promotion CAS | Restart treats the new durable `active_applied` as authoritative even if its in-memory pointer was never published. |
| Gateway promotes but response is lost | Platform observes the exact durable active fence and completes its CAS transition idempotently. |
| Old-generation handoff is in flight | Promotion closes old admission and waits until accepted, rejected, or uncertain is durable before swapping. |
| Old runtime teardown fails | Old generation remains reconciliation-only and secret deletion is deferred; new writes still use only the promoted fence. |
| Secret cleanup fails | Redacted cleanup task retries; the account/runtime transition is not silently reversed. |
| Live write attempted without gate | Gateway rejects before venue handoff regardless of Platform state. |

All errors returned outside the owning service use stable codes, retryability,
and scoped resource identity. They do not include raw SDK errors when those may
contain request headers, signatures, account identifiers, or credential data.

## Test And Live Isolation

Platform has two security modes only:

- `Test`: venue-native Demo or Testnet endpoints, used for all real acceptance
  writes;
- `Live`: production venue endpoints.

The modes run with separate Secret Store namespaces, master keys, PostgreSQL
databases and roles, service tokens, desired-state stores, idempotency domains,
worker registrations, and audit scopes. A process is started in exactly one
mode. Mode cannot be changed by browser state, request payload, route profile,
or opaque reference.

Live private writes remain default-denied in Gateway. Enabling them requires an
operator startup gate outside ordinary product configuration; no V1 acceptance
test enables it. Test and Live use the same binaries and code paths so future
Live activation does not require a fork.

## Production Wiring

The production Platform entrypoint constructs the configured `KeySource`,
encrypted filesystem provider, and scoped `CredentialWriter`, then passes it to
the application. The production Gateway entrypoint constructs the same
provider implementation with `CredentialResolver` capability and its own
mode-scoped configuration. Environment variables may identify non-secret
paths, modes, and reference names, but raw exchange credentials are no longer a
Gateway YAML or process-environment configuration surface.

Gateway YAML remains for server listeners, authentication-token references,
database connectivity references, mode, connector profiles, and operational
limits. Account inventory and exchange credential references come from the
Platform desired-state protocol. A missing provider is a production
configuration error even when the account inventory is empty; an empty but
healthy provider is valid.

## Migration

Implementation migrates without reading or exposing existing repository
`.env` values:

1. Add provider wiring and empty-inventory readiness before changing account
   shape.
2. Introduce account route groups and public connector registry behind the
   existing internal authority boundary.
3. Map each existing desired account's legacy environment to exactly one
   registered profile under the current process mode. Test+demo and
   Test+testnet may migrate; Live+live may migrate only in a separately started
   Live process. Cross-mode or ambiguous mappings are blocked.
4. Migrate each valid desired account to one explicit route derived from its
   current product/settlement identity, preserving account ID and fences and
   persisting only the newly derived native-environment observation.
5. Provision credentials through an explicit secure prompt/stdin operator
   command or the authenticated Account and Security UI. The command emits
   only account/result identifiers and never prints material or references.
6. Reconcile migrated accounts as candidates, then enable products only after
   exact route readiness is proven.
7. Remove static account credentials and account inventory from Gateway YAML
   only after Test accounts restart successfully from desired state.

There is no automatic `.env` scraper. Existing source credentials are not
deleted or retired until the user-authorized cutover sequence proves the new
store and runtimes. This design does not itself authorize cutover activation,
source retirement, venue writes, or Live private writes.

## Verification Strategy

### Unit And Contract Tests

- encrypted envelope round-trip, random nonce, tamper rejection, AAD binding,
  permission rejection, atomic replacement, idempotent delete, and buffer
  clearing boundaries;
- strict credential-bundle schemas for every configured adapter, including
  venue-specific metadata, unknown-field rejection, and size limits;
- credential-operation replay before/after provider write, deterministic
  reference recovery, conflicting-material rejection, abandoned cleanup, and
  two-phase retired-reference tombstones;
- Test/Live namespace and key mismatch rejection;
- opaque reference redaction in JSON, formatting, logs, metrics, errors, and
  audit;
- account and route validation, unique route IDs, monotonic revisions and
  generations, full write-fence comparison, and transition tables;
- internal Platform/Gateway DTO and schema compatibility, including unknown
  field rejection and bounded request bodies;
- empty inventory reports service-ready while account trading remains denied.

### Integration And Recovery Tests

- two accounts on the same venue share one public connector while maintaining
  separate private clients, rate-limit identity, state, and write fences;
- one account with Spot and Perp promotes both routes atomically;
- one failed active route prevents all candidate promotion and preserves the old
  generation;
- candidate failure followed by process death restores the complete old
  `active_applied` routes while retaining the separate pending diagnostics;
- identical operation replay is accepted, while reuse with a different
  normalized desired snapshot fails before provider or network access;
- a barrier race proves promotion cannot linearize while an old-generation
  handoff lease is between fence validation and durable accepted/rejected/
  uncertain recording;
- rotation succeeds, fails, loses its response, and restarts at each durable
  boundary without duplicate activation or premature secret deletion;
- private-stream disconnect/reconnect requires a snapshot barrier before
  readiness returns;
- Platform and Gateway restart from PostgreSQL plus encrypted filesystem state
  without static account YAML;
- drain/remove checks combine Platform reservations with Gateway blockers;
- public refresh counters prove no per-account duplicate instrument polling;
- rate-limit tests prove Spot/Perp and old/candidate generations share declared
  IP, authenticated-account, and route budgets;
- every legal endpoint mapping succeeds and every Test/Live conflict fails
  before provider or network access;
- wrong-mode tokens, references, databases, route profiles, and command fences
  are denied without side effects;
- Live private writes remain denied even if every other readiness check passes.

### User-Level Test Acceptance

After offline and recovery gates pass, the existing G010/G011 acceptance flow
uses only ready Test accounts. It verifies browser account setup and discovery,
Manual market/resting-limit/cancel/safe-close, Funding open/observe/close,
positions, fills, fees, funding, realized and unrealized PnL, ledger, activity,
audit, restart recovery, and final cleanup. Every Test order or leg remains at
or below 100 USDT notional. No Live private write is part of acceptance.

## Explicit Exclusions

- Multi-user tenancy, RBAC, organization ownership, and per-user secret
  namespaces. V1 remains an authenticated single-user product while preserving
  stable account/principal identifiers for future expansion.
- Standardized Gateway risk limits. V1 product policy, reservations, explicit
  Test acceptance caps, and Live default denial remain outside a generalized
  Gateway risk engine.
- Product semantics, portfolio attribution, or reservation ownership inside
  Gateway.
- Product-owned venue SDK clients or duplicate public market-data pollers.
- A concrete Vault, cloud KMS, or distributed Secret Store deployment.
- Hard security isolation between the trusted Platform and Gateway processes in
  the V1 same-OS-identity local deployment.
- Automatic extraction of credentials from repository files or environment
  variables.
- Cross-account netting, credential sharing between accounts, or mixed
  Test/Live account runtime groups.
- High-availability multi-writer Gateway leadership. Existing durable
  one-writer fencing remains authoritative.
- Cutover activation, funding-trader retirement, real Test venue writes, and
  any Live private write as part of this design-only change.

## Acceptance Criteria For Implementation Planning

The subsequent implementation plan must be decomposed so each checkpoint can
be verified independently and rolled back without exposing secrets:

1. The production Platform and Gateway both construct the scoped encrypted
   provider; no production path silently substitutes an unavailable or
   environment-backed exchange credential provider.
2. Zero-account Test startup is control-plane ready, while absent-account
   writes are denied.
3. Two accounts at one venue use one public connector and separate private
   runtime groups.
4. One account can own multiple active routes and can never expose a partial
   candidate generation to writes.
5. Rotation, restart, drain, removal, and stream-gap behavior satisfy the
   fences and failure table above.
6. Test and Live isolation is proven at provider, database, API, runtime, and
   write-policy boundaries.
7. Browser/API responses, logs, metrics, traces, audit, screenshots, and support
   bundles contain no credential value, raw opaque secret reference, provider
   path, or sensitive SDK error. The authenticated Platform-to-Gateway desired
   request is the sole raw-reference transport. A fixed `redacted` sentinel is
   permitted where a response contract requires field presence.
8. Existing Funding and Manual product contracts continue to use Platform and
   Gateway shared capabilities without product-specific Gateway code.

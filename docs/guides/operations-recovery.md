# Operations and recovery

> Canonical language: English. [Chinese mirror](../zh-CN/guides/operations-recovery.md)

This page covers running the stack and what to do when an outcome is unclear. It
does not replace a venue's own runbook, and nothing here turns Demo or Testnet
evidence into production readiness.

## Running the stack

Five commands cover the whole lifecycle.

| Command | What it does |
| --- | --- |
| `make up` | Generates secrets and `.env.stack`, builds, starts, waits for health, then runs the doctor |
| `make doctor` | Diagnoses a running stack without changing it |
| `make logs` | Follows every container's output |
| `make down` | Stops the stack and keeps the database volume |
| `make down-hard` | Stops the stack and deletes the volume |

`make up` is safe to repeat. It regenerates what is missing, tightens what is
loose, and leaves what is already correct alone.

The difference between the two teardowns matters more than it looks. The
Postgres image runs its initialization scripts exactly once, when the data
directory is empty. A surviving volume therefore means a changed role set is
never applied — so if you edit anything under `docker/postgres/initdb/`, only
`make down-hard` will make that edit take effect.

## Reading `make doctor`

The doctor runs its checks in a fixed order, and that order is the debugging
order: everything below a failure tends to fail for the same reason.

| Check | Healthy answer |
| --- | --- |
| `docker` | The daemon responds |
| `.env.stack` | Present and non-empty |
| `credentials` | Seven files, mode `0600`, in `.secrets/<mode>` |
| `credential store` | Present and writable by the uid the gateway runs as |
| `postgres` | The port accepts a connection |
| `postgres roles` | Six roles, three databases, writer membership granted |
| `gateway` | Authenticated `/v1/health/ready` returns `200` |
| `trading` | Authenticated `/api/v1/system/readiness` returns `200` |
| `auth edge` | `/api/v1/me` returns `401` without a session |
| `web` | `/` returns `200` |

Three of these are worth explaining, because a plausible reading of each is
wrong.

**The gateway probe carries a bearer token.** Readiness sits behind the same
authentication as every other `/v1` route, so an unauthenticated probe would
report a healthy gateway as broken. The container image is distroless and has no
shell to read the token file with, which is why the check runs on the host.

**A `401` from the auth edge is the pass.** A `200` there would mean the edge
answered for a principal it never authenticated — the check treats any other
status as a failure, including success.

**Role membership is verified explicitly.** A grant that did not happen leaves
two roles that both exist and a service that cannot write, which otherwise
surfaces at the first insert rather than at startup.

A skip is never the interesting line. Skips are downstream of a failure that is
already reported above them.

## Startup and shutdown

The gateway refuses to serve before its own preconditions hold: configuration,
credential decryption, database connectivity, schema version, catalog load,
route profile, and readiness registration. A gateway that never becomes ready
has failed one of those, and `make logs` names which. It does not start
degraded, because a gateway that accepts writes before its catalog is loaded
would validate orders against rules it does not have.

On the way down, the gateway drains for up to ten seconds inside the stack's
fifteen-second stop window. In-flight requests finish; new ones do not start.
The drain outcome is recorded as a metric, so a stack that is being stopped
faster than it can drain is visible rather than inferred.

## Ambiguity is a state, not a retry signal

A timeout or a transport error after submission can mean the venue accepted the
order. This is the single most important operational fact on this page, and both
layers are built around it.

The trading service's command ledger has an explicit `uncertain` state, and the
reconciler puts a command there whenever the gateway lookup fails or returns
evidence it cannot interpret. Retries back off from five seconds, doubling to a
one-minute cap, and the command stays claimable the whole time. It does not
become `failed` because a read failed.

**Do not submit a replacement with a new identity until the first outcome
resolves.** Preserve the original command ID and any venue order ID and query by
exactly those. A failed command is not proof that no write reached the venue.

| Observed outcome | Operator action |
| --- | --- |
| Validation, lifecycle, or risk rejection before venue handoff | Fix the request or the readiness condition; perform no venue cleanup for that attempt |
| Acknowledged order | Retain the command and venue IDs; follow normal status and fill handling |
| Possible venue handoff without an authoritative result | Freeze replacement writes, query by the exact known identity, reconcile, and leave the outcome ambiguous if evidence stays incomplete |
| Authoritative terminal result | Apply only the cleanup that terminal order, fill, or account evidence justifies |

Absence from a complete open-order list proves only that the order is not open.
It does not supply a terminal cause, prove zero fills, or authorize a
replacement. Use an exact status report and exact fill reports where the product
implements them; otherwise the result stays unresolved.

Terminal states reach the activity feed through the settlement observer and
nowhere else, so what the interface shows is what the ledger settled — not what
a response body suggested at submission time. See
[the trading service](trading-service.md).

## Stream gaps

The gateway's public stream has no cursor replay by design. Every connection
begins from a fresh authoritative snapshot, and a detected gap emits
`resync_required` and closes rather than silently skipping events.

Treat that event as authoritative: reconnect and take the new snapshot. Do not
reconstruct orders or positions from the events you did receive. A gap means the
sequence you hold is incomplete, and an incomplete sequence applied as if it
were complete produces a position that looks settled and is not.

## Cleanup is bounded

Acceptance harnesses cancel only the orders they can identify as their own and
flatten only exposure they can attribute and bound. On a zero exit, apply the
test's exact product-scoped success definition. On any nonzero or ambiguous
exit, the final state is unconfirmed even if deferred cleanup ran.

Never blind-cancel unrelated orders or flatten an unproven position. **If a close
outcome is ambiguous, an additional sell can create a short position.** The
cleanup path is fail-closed for exactly this reason: stable nonzero position
evidence after an ambiguous close blocks another flattening order rather than
retrying it.

Spot cleanup is bounded by the captured authoritative base-balance baseline, the
observed fill quantity, a fee reserve, the size step, and the minimum notional.
It never sells presumed ownership out of an account-wide balance. Perp cleanup is
limited to the selected instrument and account scope, an explicit quantity cap,
and reduce-only semantics.

## Evidence

Record the command, the environment and product, the client and venue IDs, the
terminal reports, and the reconciliation result. Record no secrets. Keep raw
execution evidence private; anything published carries only scoped, dated,
zero-skip results.

- [Testing](../reference/testing.md)
- [State and reconciliation](../concepts/state-reconciliation.md)
- [The local stack](../getting-started/local-stack.md)

# Execution and Risk

> Canonical language: English. [Chinese mirror](../zh-CN/concepts/execution-risk.md)

This page owns the venue-neutral submission sequence, local risk reservation,
and execution-outcome semantics. Venue-specific support remains in the
[capability matrix](../adapter-capabilities.md); reconciliation details belong
in [State and Reconciliation](state-reconciliation.md).

## The generic submission sequence

Before the venue-facing path, runtime resolves the account and client identity,
rejects duplicate client IDs, checks the lifecycle command gate, and verifies
that the execution capability advertises Submit. It then performs one shared
sequence:

1. Call the adapter's side-effect-free `ValidateSubmit`.
2. If configured, call the venue-neutral `SubmissionRiskChecker` and retain its
   exposure-release closure.
3. Create and durably append a command intent while tracking the same identity
   as in flight.
4. Insert the normalized order into cache as `PendingNew`.
5. Call the execution client's ordinary `Submit` exactly once.
6. Classify the result and commit the command result together with any
   authoritative order update.
7. Release the local exposure reservation when the engine call returns.

Context cancellation is checked before and after intent creation. Before an
intent exists, cancellation returns without durable command state. After the
intent is recorded, runtime appends a local outcome and still does not call
`Submit`. A journal or cache failure also stops before the venue boundary.

There is no adapter hook between validation and ordinary submission. In
particular, runtime has no `SubmitPrepared`, venue pre-trade lease, or local
venue-capacity admission path. An adapter can perform required signing or
preparation privately inside its one `Submit` implementation; that does not
change the runtime protocol.

## What the risk reservation means

`CheckSubmission` evaluates generic policy under one lock and, on acceptance,
returns an idempotent release function. Concurrent checks include already-held
reservations, closing the gap before a `PendingNew` order becomes visible in
cache. The execution engine defers release immediately, so the exposure hold
normally spans intent persistence, pending-state insertion, the venue call, and
result commit, and is still released on every later error path.

The built-in checker can enforce:

- a kill switch and positive quantity;
- bounded duplicate-client-ID retention;
- caller-configured `MaxOrderQty`, `MaxOrderNotional`, and `MaxPositionQty`;
- instrument minimum quantity and notional from normalized metadata;
- fresh funded-cash balance protection for Spot cash, including working and
  concurrently reserved orders;
- configured product/account provenance and fresh authoritative margin account
  state for risk-increasing derivative orders.

The exposure release does not erase the bounded client-ID deduplication history.
That history protects idempotency independently of the short-lived exposure
hold.

## Local policy is not venue capacity

Generic quantity, notional, and position limits are caller-selected safety
policy. Instrument minimums are static request constraints. Spot cash checks
protect known local inventory. None claims to predict venue liquidity, rate
limits, unified-margin capacity, or final acceptance.

For margin accounts, runtime requires authoritative readiness when that policy
is enabled but leaves available capacity to the venue. The adapter validates
only side-effect-free request defects it can prove locally; the service remains
authoritative for acceptance or rejection.

## Definitive outcomes and ambiguous handoff

| Outcome | Evidence | Runtime treatment |
| --- | --- | --- |
| Pre-boundary local denial or unsupported command | Failure before the venue call | No venue write; close the local intent if one was already recorded |
| Confirmed accepted | A non-rejected order response with no error | Commit the returned order and resolve the in-flight command |
| Definitive venue rejection | Explicit venue-rejection error, or a no-error response carrying a `Rejected`/`Expired` order | Commit a rejected order and resolve the in-flight command |
| Defensive post-boundary unsupported | The called client returns `contract.ErrNotSupported` after the venue boundary | Record `OutcomeUnsupported` with `Sent=true`, commit a rejected terminal order, and resolve the intent rather than preserving it as ambiguous |
| Ambiguous | Any other error after the venue boundary, including timeout, cancellation, disconnect, or an unclassified error | Keep the intent in flight and preserve `PendingNew`, plus any trustworthy venue-order alias |

A post-boundary error is not treated as rejection merely because it returned an
error. The explicit `ErrNotSupported` branch above is a defensive classifier for
a client violating the advertised/validated operation boundary; other
post-boundary failures may mean the venue accepted the write. Ambiguous state is
therefore a handoff to authoritative order/fill reconciliation, not permission
to retry with a new identity. Only scoped evidence can later confirm acceptance
or a definitive negative result.

Operational remediation is documented in
[Operations and Recovery](../guides/operations-recovery.md). Strategy callers
should also read [Writing Strategies](../guides/strategies.md) and
[Unsupported and Deferred Features](../reference/unsupported.md).

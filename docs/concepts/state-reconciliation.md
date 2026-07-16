# State and Reconciliation

> Canonical language: English. [Chinese mirror](../zh-CN/concepts/state-reconciliation.md)

This page owns how runtime state converges with authoritative venue evidence,
including coverage, provenance, fill deduplication, startup, reconnect, and
activation. The exact account snapshot shape belongs in
[Accounts and Instruments](accounts-instruments.md).

## Authoritative inputs stay typed

Reconciliation consumes independent, typed neutral sources:

- `AccountClient.AccountState` supplies the mandatory account snapshot used for
  balances, margin summary, identity, and freshness;
- `ExecutionClient.GenerateExecutionMassStatus` supplies order, fill, and
  position report domains when an execution client is configured;
- capability-advertised `AccountClient.Positions` can be normalized into the
  same position-report comparison path. It is used directly when no execution
  client owns that domain, or as a guarded fallback when execution position
  coverage is neither complete nor explicitly not requested.

The position fallback must prove the same account, venue, frozen instrument
selector, request-time boundary, and account capability. An out-of-scope row,
duplicate identity, missing capability, clock regression, or failed query leaves
the original execution coverage in place with a warning. This fallback does not
embed positions in `AccountState` and does not turn account snapshots into
execution-owned mass status.

The account snapshot's account ID must match the runtime account, and its venue
must match the account client's capability provenance. A mass-status response is
validated against the exact query venue, account, client filter, and frozen
instrument selector before any report row is trusted.

## Coverage controls absence-based conclusions

Open orders, fills, and positions each carry their own `ReportCoverage` state:
`Unknown`, `NotRequested`, `Complete`, `Partial`, or `Unavailable`. Complete and
partial evidence also carries the exact account, client filter, normalized
instrument selector, and observation boundary. Fill coverage additionally owns
its exact `From`/`Through` history interval.

These domains are not interchangeable. Complete open-order coverage cannot
prove complete fill history, and an account position snapshot cannot expand an
execution report's frozen scope. Runtime accepts positive evidence inside a
valid partial scope, but it makes an absence-based conclusion only from complete
coverage for that identity and domain.

## How each domain converges

- A valid `AccountState` follows the canonical account-cache application path
  and updates reconciliation freshness.
- Venue-reported open orders refresh known orders and materialize orders first
  observed outside runtime.
- A cached open order absent from a complete frozen open-order snapshot becomes
  `Unknown`; reconciliation does not invent `Canceled` or `Filled` as its cause.
- Recovered fills use the node's canonical fill path so cache, portfolio,
  terminal-order state, and callbacks stay consistent with live events.
- Position reports are comparison evidence. They can create blocking findings,
  but the reconciler does not directly overwrite or clear position cache state.

Order and fill evidence can resolve an ambiguous in-flight command only when
its typed identities agree. An unchanged open order can confirm an ambiguous
submit, but it does not prove that a pending cancel or modify succeeded. Outcome
classification is owned by [Execution and Risk](execution-risk.md).

## Provenance, identity, and fill deduplication

Recovered fills carry `SourceReconciliation` plus snapshot/reconciliation event
flags; fills synthesized from report data are explicitly flagged synthetic.
Runtime deduplicates fills by the venue-scoped identity
`AccountID + InstrumentID + TradeID`, excluding order aliases because those may
be learned later. When a report has no trade ID, reconciliation derives a stable
synthetic one before applying it.

A fill delivered by an adapter reconnect snapshot keeps its original stream
source and snapshot provenance. The snapshot flag is not a reason to discard
the fill: it enters the same canonical fill path and venue-scoped deduplication
as live traffic, so a replayed snapshot fill and its later live duplicate are
applied once.

Modify keeps the order's original logical `ClientID`. A sparse venue response
is completed from that logical request. If the venue returns a different
response `ClientID`, runtime accepts the response only when that identity is
unclaimed, then normalizes it back to the original `ClientID`; it does not
register an alternate client alias. An identity already owned by another order
fails closed and never rebinds the logical order.

The reconciler keeps a bounded completed-fill index and an independent exact
overlap set for the next cursor window. A journal-backed state store records
applied-fill dependencies before the full-coverage cursor can advance. On
restart, replay seeds idempotency state without applying business state or
emitting callbacks a second time. Identity conflicts and retention exhaustion
fail closed rather than allowing a possible duplicate fill.

Incomplete fill coverage can apply trustworthy positive fills, but it cannot
advance the durable full-coverage cursor. A later pass deliberately overlaps the
previous successful boundary, relying on deduplication to make that overlap
safe.

### Journal retention

The physical journal file remains append-only. Retention bounds the ordinary
replayed diagnostic/history window held in memory; recovery-critical records
remain in addition, including open or ambiguous intents, blocking reports,
uncommitted applied events, and the latest cursor with its dependencies.
Dropping an ordinary in-memory entry does not rewrite or compact the file on
disk. Runtime does not automatically compact the disk file, so bounded
diagnostic memory must not be interpreted as bounded journal file size.

## Startup, reconnect, and stream gaps

`TradingNode.Run` replays open command intents, performs startup reconciliation
under the node's reconciliation and event-serialization locks, evaluates the
activation verdict, and only then starts strategy callbacks and normal event
processing. A reconciliation error moves the node to failed state.

`TradingNode.Reconnect` calls clients that implement `contract.Reconnectable`,
then reconciles before restoring trading. Clients that reconnect internally are
not forced through that method. A private-stream gap immediately moves trading
to reconciling; recovery waits for every active gap generation, runs one scoped
reconciliation, and then reevaluates activation. If an execution stream can lose
fills but the adapter has no authoritative fill-history report, reconnect
recovery remains restricted rather than assuming continuity.

## Activation verdict

When execution evidence is present, activation requires complete open-order
coverage and requires fill and position coverage to be either complete or
explicitly not requested. Incomplete fill cursor continuity and blocking
findings also prevent activation. Diagnostic warnings and a generic `Partial`
summary bit are not themselves authority; the typed domain coverage and findings
determine the verdict.

Use [Running a Runtime Node](../guides/runtime-node.md) for composition and
[Operations and Recovery](../guides/operations-recovery.md) for operator action.
Demo/Testnet evidence policy is owned by
[Testing](../reference/testing.md).

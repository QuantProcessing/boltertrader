# State and reconciliation

> Canonical language: English. [Chinese mirror](../zh-CN/concepts/state-reconciliation.md)

An order whose outcome is unknown is not an error to be retried. It is a
question, and this page owns how the question gets answered.

The states being reconciled belong to [Execution and risk](execution-risk.md).
The steps an operator takes belong to
[Operations and recovery](../guides/operations-recovery.md).

## Two reconcilers, two questions

There are two of them, at different layers, and they are not redundant:

| | Gateway reconciler | Trading reconciler |
| --- | --- | --- |
| Asks | What did the venue do with this order? | Has this command reached a terminal state? |
| Evidence | An exact venue lookup by order identifier | The gateway's answer |
| Vocabulary | The nine gateway order states | The six trading command states |
| Writes | Gateway command and order rows | The command ledger and the activity feed |

Only the gateway one contacts a venue. The trading one asks the gateway and
never holds a venue client, which is why a trading service that has lost its
gateway reports "unknown" instead of inventing a resolution.

## Exact lookup is the only authority

The gateway reconciler asks one narrow question: for this command, what does the
venue say about this identifier? It looks up by venue order ID when one was
learned, and by the client order ID when none was — that identifier exists to be
looked up, which is why it is minted before dispatch rather than after.

The answers, and what each one settles:

| Venue answer | Reason recorded | Outcome |
| --- | --- | --- |
| Found, carrying a state | *(none)* | Transition to the state the evidence carries |
| Not found | *(see below)* | `rejected` from `dispatching` or `uncertain`; otherwise unresolved |
| Unconfirmed | `evidence_unconfirmed` | Stay `uncertain`, attempt again later |
| Lookup failed | `lookup_failed` | Stay `uncertain`, attempt again later |
| Evidence malformed | `evidence_invalid` | Stay `uncertain`, attempt again later |

Note the asymmetry. Only "found", and one narrow reading of "not found", end the
question. Everything else leaves the command exactly where it was, records why,
and schedules another attempt.

Reconciliation is allowed to fail to conclude. That is a supported outcome
rather than a defect: a reconciler that must produce an answer will eventually
produce a wrong one.

## "Not found" is only sometimes an answer

Not found means the venue has no record of that identifier *right now*. Whether
that proves the order never existed depends entirely on where the command was
when it went dark.

From `dispatching` or `uncertain`, not found is treated as a rejection. Identity
was minted before the venue was contacted, so if the venue had accepted the
write, the identifier would be there. The order never landed.

From any other state, not found is recorded as unconfirmed and the command stays
uncertain. An order that was `open` an hour ago and is missing from the answer
now may have filled, may have been cancelled, or may simply have aged out of
whatever window the lookup covers. Concluding "rejected" there would be a
fabrication.

The read itself is weaker than it looks. No row proves absence by pagination —
walking history to its end does not establish that an order never existed — and
some rows cannot confirm absence at all, only report it as unproven.
[Unsupported surfaces](../reference/unsupported.md) owns which rows can prove
what. The reconciler's rule is the same on every row: an absence is evidence
only when the state it is read against makes it evidence.

## The cadences differ because the questions differ

| | Gateway | Trading |
| --- | --- | --- |
| Sweep interval | 1s | 1s |
| Next attempt scheduled | 5s out, flat | 5s, doubling to a 60s ceiling |
| Lease | 30s | 15s |
| Claimed per sweep | 100 | 20 |
| Stops retrying after | 20 attempts | *(does not; the backoff caps instead)* |

Both claim work under a lease rather than a lock, so an instance that dies
mid-task releases it by expiry instead of holding it.

The two bounds differ on purpose. The gateway stops claiming a task after
twenty attempts, because a venue lookup that has failed twenty times is not
going to succeed on the twenty-first and a human is the next step. Trading backs
off but keeps trying, because its dependency is the gateway, which is expected
to come back.

## Recovery is what reopens the gate

Reconciliation resolves one command. Recovery resolves an account.

A dispatch failure closes the account's write gate, and so does losing provable
private-stream continuity. While it is closed, placement is refused with a
recovery-required reason rather than attempted. The gate does not reopen on a
timer, and it does not reopen because things have gone quiet.

It reopens when recovery runs and every recoverable command resolves. Recovery
loads the account's unresolved commands and handles each by where it stopped:

- A command still in `prepared` never reached the venue, so it is dispatched
  now, under a re-validated fence.
- Anything past that point is reconciled against exact venue evidence.

If even one command is left unresolved, recovery returns incomplete and the gate
stays closed. Partial recovery is not recovery. An account with one command in
an unknown state is an account whose position is unknown, and trading it is how
a small ambiguity becomes a large one.

## One path to the activity feed

A settled state reaches the interface by exactly one route: the trading
reconciler's settlement observer.

This is worth stating because the obvious alternative is wrong. The handler that
accepted the order returns as soon as the command is submitted; it never learns
whether the order filled. An audit trail written from the handler would stop at
"submitted" forever. The reconciler is what learns the venue's final answer, so
the reconciler is what records it.

The observer runs inline in the sweep and must not block. A slow observer does
not merely delay one row — it stalls the sweep that every other uncertain
command is waiting on.

## What reconciliation does not do

It does not retry placements. A command that went ambiguous is looked up, never
re-sent. That is precisely what makes it safe to run continuously.

It does not reconstruct fills or rebuild a position from history. It answers one
question per command.

It does not overrule a terminal state. Once the gateway holds authoritative
evidence, a later sweep cannot reopen the question, because terminal states have
no outbound edges.

And outside the one narrow case above, it does not turn an absence into a
conclusion.

## Related

- [Execution and risk](execution-risk.md) — the states being reconciled, and why
  `uncertain` exists
- [Accounts and instruments](accounts-instruments.md) — the fence and lifecycle
  recovery runs against
- [Operations and recovery](../guides/operations-recovery.md) — the operator
  procedure
- [Trading service](../guides/trading-service.md) — the command ledger and the
  activity feed
- [Unsupported surfaces](../reference/unsupported.md) — which rows can prove
  absence

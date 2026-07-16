# Operations and Recovery

[Chinese mirror](../zh-CN/guides/operations-recovery.md) · This English page is canonical.

## Ownership and scope

This page owns operator actions for startup, reconnects, ambiguous order outcomes,
and bounded non-production cleanup. It does not replace a venue runbook or claim
production readiness from Demo/Testnet evidence.

## Start from authoritative state

Use `node.Resync(ctx)` before enabling writes. Confirm the reconciliation report,
account freshness, product/account mode, and absence of unsafe pre-existing state.
After a stream gap or reconnect, use `node.Reconnect(ctx)` or an explicit resync;
do not reconstruct orders or positions from callbacks alone.

`Reconnect` invokes clients implementing `contract.Reconnectable` and then
reconciles authoritative sources. `Resync` serializes against event application.
Neither method turns missing fill history into proof that an order never filled;
the lifecycle can remain reconciling or halted when recovery is insufficient.

## Treat ambiguity as a state, not a retry signal

A timeout or transport error after submission can mean the venue accepted the
order. Preserve the original client ID and any venue order ID, then use exact
status, open-order, fill, and position evidence supported by that product. Do not
submit a replacement with a new identity until the first outcome is resolved.

The execution journal and reconciliation path keep intent, confirmed results, and
unresolved outcomes distinct. A failed command is not proof that no write reached
the venue.

| Observed outcome | Operator action |
| --- | --- |
| Local validation, lifecycle, or risk rejection before venue handoff | Fix the request or readiness condition; do not perform venue cleanup for that rejected attempt |
| Acknowledged order | Retain client and venue IDs; follow normal status/fill handling |
| Possible venue handoff without an authoritative result | Freeze replacement writes, query by the exact known identity, reconcile, and preserve the outcome as ambiguous if evidence stays incomplete |
| Authoritative terminal result | Apply only cleanup justified by that terminal order/fill/account evidence |

Absence from a complete open-order list proves only that the order is not open.
It does not invent a terminal cause, prove zero fills, or authorize a replacement.
Use an exact status report and exact fill reports where the product implements
them; otherwise keep the result unresolved.

## Cleanup is bounded

Acceptance harnesses cancel only validation-owned orders they can identify and
flatten only exposure they can attribute and bound. On a zero exit, apply the
test's exact product-scoped success definition. On any nonzero or ambiguous exit,
the final state is unconfirmed even if deferred cleanup ran.

- Binance Spot: inspect exact validation IDs, selected-symbol open orders, and the
  authoritative base-asset balance against the captured baseline. Success permits
  dust below one size step; it does not prove whole-account equality.
- Hyperliquid Perp: inspect exact validation IDs, selected-scope open orders, and
  account/runtime position for the selected Perp. Success does not certify Spot,
  HIP-3, or every account product.

Never blind-cancel unrelated orders or flatten an unproven position. If a close
outcome is ambiguous, an additional sell can create a short position.

The following is an excerpt, not a standalone program, from the current bounded
cleanup implementation
[`adapter/internal/runtimeaccept/order_lifecycle.go`](../../adapter/internal/runtimeaccept/order_lifecycle.go).
It shows the fail-closed Perp rule after an ambiguous close: stable nonzero
position evidence blocks another flattening order.

```go
if !allowFlatten {
	reports, err := waitForStableCleanupPosition(cleanupCtx, exec, spec)
	if err != nil {
		return errors.Join(cancelErr, err)
	}
	if len(reports) != 0 {
		return errors.Join(cancelErr, fmt.Errorf("position cleanup blocked: close outcome ambiguous; refusing an additional sell with %d non-zero position report(s)", len(reports)))
	}
	return cancelErr
}
```

For Spot, cleanup is bounded by the captured authoritative base-balance baseline,
observed lifecycle-created fill quantity, fee reserve, size step, and minimum
notional. It never sells presumed ownership from an account-wide balance. For
Perp, cleanup is limited to the selected instrument/account scope, observed
lifecycle exposure, an explicit quantity cap, and reduce-only semantics. A close
with an ambiguous outcome disables another automatic flatten attempt.

## Evidence and escalation

Record the candidate, command, environment/product, client and venue IDs, terminal
reports, and reconciliation result without recording secrets. Keep raw execution
evidence private; public certification summaries contain only scoped, dated,
zero-skip results.

- [CEX Demo walkthrough](../getting-started/cex-demo.md)
- [DEX Testnet walkthrough](../getting-started/dex-testnet.md)
- [Testing reference](../reference/testing.md)
- [State and reconciliation](../concepts/state-reconciliation.md)

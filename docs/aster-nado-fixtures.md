# Aster and Nado Fixture Baseline

The canonical inventory is
`internal/fixtureaudit/testdata/aster_nado_manifest.json`. Each entry identifies
the owning SDK, product, conversion kind, source, sanitization, and whether it
represents a negative path. Payloads live under the owning package's `testdata`
directory and are synthetic derivatives of official examples, never captured
account data.

## Coverage

| Surface | Fixture roots | Required conversions |
| --- | --- | --- |
| Aster Spot V3 | `sdk/aster/spot/testdata/v3` | instrument, cash account, order, fill, market stream, private stream, venue error |
| Aster USDT Perp V3 | `sdk/aster/perp/testdata/v3` | instrument, margin account, position, order, fill, mark stream, private stream, funding/mark/index, current OI, venue error |
| Nado unified margin | `sdk/nado/testdata` | product and symbol discovery, contract discovery, account, order, fill, public/private streams, reference/OI, capacity, validation, simulation, venue error |

Run `go test ./internal/fixtureaudit -count=1` to verify JSON validity,
provenance, required conversion coverage, negative-path coverage, and credential
redaction.

## Semantic Locks

Nado `subaccount_info.healths` is ordered initial, maintenance, then unweighted.
Initial health is the source for non-negative available collateral; unweighted
health is the source for account equity. Neither value is a currency balance.
The response has no venue timestamp, so the SDK wraps it in `AccountSnapshot`
with an explicit local `ReceivedAt`; freshness checks use that receipt time and
must never present it as an exchange event timestamp.

Nado Spot `balance.amount` is signed raw inventory: positive means deposit and
negative means borrow. It must map to total/borrowed semantics without inventing
`free`, `available`, or `locked`. Perp `v_quote_balance` is position accounting,
not free collateral. Product `0` must resolve through current symbol metadata to
`USDT0` before account readiness.

Nado writes first require sequencer `status=active`, an exact `endpoint_addr`
contract response for the selected profile chain, and fresh product metadata.
Symbol trading states use the official `live`, `post_only`, `reduce_only`,
`soft_reduce_only`, and `not_tradable` values; fixtures must not use the former
generic `active` market state.

Funded-only Nado Spot admission uses the documented `max_order_size` query with
`spot_leverage=false` and preserves `spot_leverage=false` on execute. The exact
prepared payload owned by the pre-trade lease must be submitted once.
Nado symbol fixtures preserve `isolated_only`. Such Perp orders must set the
isolated appendix in both capacity and prepared-order paths, transfer 1x
opening notional rounded up to x6 precision, and use zero added margin for
reduce-only closes.

Aster `/fapi/v3/openInterest` is marked `probe`, not `official`: it returned a
valid Testnet payload on 2026-07-10 but is absent from the inspected V3
Markdown. Loss or incompatibility of this route is a Perp release blocker and
must not fall back to V1 or synthesized data.

## Sanitization

Fixtures use synthetic symbols, balances, IDs, addresses, and digests. Response
signatures are stored only as `<redacted>`. Do not add private keys, API keys,
authorization headers, signed preimages, real subaccounts, or production order
and balance identifiers. New fixtures must be added to the manifest in the same
change.

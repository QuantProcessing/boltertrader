# Glossary

> Canonical language: English. [Chinese mirror](../zh-CN/reference/glossary.md)

This page owns BolterTrader's normative vocabulary. Other pages use these
meanings and link here instead of defining a term locally.

Most of these words mean something looser elsewhere. They are narrow here on
purpose, because the distance between "we implemented it" and "a recorded run
proved it" is the distance between a claim you can publish and one you cannot.

## Naming something to trade

Five words compose into one identifier, in this order.

| Term | Meaning | Values |
| --- | --- | --- |
| Venue | An exchange | `binance`, `okx`, `lighter`, `hyperliquid`, `bybit`, `bitget`, `gate`, `aster`, `nado` |
| Environment | Which of that venue's networks | `live` |
| Product | The instrument family | `spot`, `perp` |
| Settlement asset | What a contract settles in, where a venue runs more than one | `USDT`, `USDC`, `USDT0` |
| Canonical symbol | The venue-neutral pair | `BTC-USDT` |

A **native symbol** is whatever the venue calls that same instrument —
`BTCUSDT`, a numeric market index, a contract code. Native symbols stay inside
`exchange/internal/<venue>`. Nothing above the exchange layer should ever see
one.

An **instrument ID** is the gateway's stable name for one tradable thing:

```text
venue:environment:product:symbol
venue:environment:product:settlement:symbol
```

`binance:live:spot:BTC-USDT` and `nado:live:perp:USDT0:BTC-USDT` are both well
formed. The settlement segment appears only where a venue needs it to
disambiguate. The last segment must be a canonical symbol, which is why the
validator insists on the hyphen: it is the cheapest way to catch a native symbol
that leaked upward.

The environment segment has one legal value. It once took `demo` and `testnet`
as well, and the venues implemented those differently enough that the tier was
removed. The segment itself stayed, because the ID is positional: removing it
would reinterpret every identifier already written down rather than shorten it.

A **route profile** is the resolved combination of venue, product, and
settlement, named by an ID like `binance.spot.v1` or `nado.perp.usdt0.v1`. A
**route** is a profile bound to one account's credentials, and a **route ID**
names it inside a write.

**Mode** is a deployment attribute, not a market one. It used to be coarser than
environment — `test` covered `demo` and `testnet`, `live` covered `live`, and a
process running in one mode could not resolve a profile belonging to the other.
With the non-production tier gone, a route profile carries no mode at all. What
survives is a `test` or `live` label on the database, secret namespace, and
principal that one deployment runs under.

## Product rows

A **product row** is one venue-and-product combination the gateway exposes,
identified by a three-letter **product code**. There are twenty, and they are the
unit in which capability, evidence, and test targets are all counted:

| Venue | Spot | Perp |
| --- | --- | --- |
| Binance | `BNS` | `BNP` |
| OKX | `OXS` | `OXP` |
| Lighter | `LIS` | `LIP` |
| Hyperliquid | `HLS` | `HLP` |
| Bybit | `BYS` | `BYU`, `BYC` |
| Bitget | `BGS` | `BGU`, `BGC` |
| Gate | `GTS` | `GTU` |
| Aster | `ATS` | `ATP` |
| Nado | `NDS` | `NDP` |

The [REST](exchange-rest-v1-operation-matrix.md) and
[WebSocket](exchange-ws-v1-operation-matrix.md) operation matrices carry one
column per code. A claim that names a product row means exactly one of these —
never "Bybit" as a whole, and never "perpetuals" as a category.

## Identity

| Term | Meaning |
| --- | --- |
| Principal | Who is acting. The auth edge derives it from the session and injects it as a header; a service never takes it from the browser |
| Account | A registered venue account: credentials, a route profile, and the gateway state belonging to them |
| Command ID | The trading service's identifier for one manual command. It is what the interface, the activity feed, and the command endpoint use |
| Gateway command ID | The gateway's own identifier, minted when it accepts a placement. A different namespace from the above, and not derivable from it |
| Gateway order ID | The gateway's durable order identity, independent of whatever the venue later returns |
| Client order ID | A positive decimal `uint48` string sent to the venue |
| Idempotency key | The caller-supplied token that makes a retried submission the same submission |
| Write fence | The account revision, runtime generation, credential version, and route ID a write was decided against. A write carrying a stale fence is refused rather than reinterpreted |

The two command namespaces are the most common source of confusion in this
system. When reading gateway logs, quote the gateway's identifiers; when reading
the interface, quote the trading service's.

## Status words

Ranked, roughly, by how much each one lets you claim.

| Term | What it asserts | What it does not |
| --- | --- | --- |
| Implemented | Code for the named surface exists in the current tree | That anything ran, or that a venue accepted it |
| Admitted | The operation is present on that product row at compile time — `A` in the operation matrices | That the venue honors it, or that it has been exercised |
| Write-ready | The capability matrix marks that row as accepting writes by default | Anything about one specific account's readiness |
| Offline-conformant | A recorded zero-skip run of the deterministic, credential-free suite passed for a named product scope, on a named target, on a named date | That the venue answers today, that mainnet behaves the same way, or that it still passes now |
| Production-ready | Separate operational evidence supports the stated production scope. This repository never claims it | Anything inferable from the three rows above |
| Deferred | Deliberately outside the current slice, for a stated reason | That the venue lacks it, or that it was rejected permanently |
| Unsupported | The normalized surface is absent, or returns an unsupported error | That the venue's own API lacks the feature |
| SDK-only | Low-level venue code exists with no product row above it | Normalized coverage of any kind |

Bare **supported** and bare **certified** are not statuses. Name the product row
and the surface, then pick one of the words above.

## Evidence words

| Term | Meaning |
| --- | --- |
| Acceptance harness | An opt-in test that performs one bounded real interaction with a venue, behind a named write gate. The interaction is real — there is no non-production target behind it. It is not a reusable trading application |
| Zero-skip | The run executed at least one test and skipped none. A skip is a failure, because a missing credential must not read as a pass |
| Clean final state | A terminal assertion within the exact symbol, product, account, and harness-owned-order scope — not a statement about the account |
| Ambiguous outcome | The venue may have received the write, but no authoritative result proves it. Automatic retry is unsafe |
| Open-only caveat | A complete open-order read can prove an order is no longer open. It cannot invent a terminal reason or reconstruct missing fills |
| Public-safe | Suitable for this tree: no credentials, no account residue, no local paths, no raw logs, and current source owns every claim |

## Layer words

| Term | Meaning |
| --- | --- |
| SDK | `sdk/<venue>/`: one venue's native API, shaped like that venue's own documentation |
| Exchange | `exchange/`: the venue-neutral typed API over those SDKs. An entry point in its own right, not an internal detail |
| Gateway | `gateway/`: the shared capability plane — accounts, credentials, catalog, capability and limit resolution, durable order identity, reconciliation, private events |
| Public plane | The gateway's `/v1` surface, which product services call |
| Internal plane | The gateway's `/internal/v1` surface, which accepts credential material and is never mounted publicly |
| Service | `services/<name>/`: one product's behavior over the gateway's HTTP surface. The trading service and the auth edge are the two |
| Auth edge | `services/authedge/`: session termination and path-table forwarding. It aggregates nothing |
| Web | `web/`: the only frontend, served same-origin with the API |

See [Architecture](../concepts/architecture.md) for how those layers depend on
each other, [Testing](testing.md) for the targets that produce evidence, and
[Unsupported surfaces](unsupported.md) for what these words are currently
attached to.

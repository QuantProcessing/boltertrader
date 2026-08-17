# Adding a venue

> Canonical language: English. [Chinese mirror](../zh-CN/contributing/venues.md)

A venue is not added in one place. It is added along a line of them, and the line
is ordered: each step is only worth taking once the one before it holds. This
page owns that order and what each step has to prove.

What the layers *mean* belongs to [Architecture](../concepts/architecture.md).
This page is the sequence.

## The path, end to end

| Step | Where | What it adds |
| --- | --- | --- |
| 1 | `sdk/<venue>/` | The venue's own API, in the venue's own vocabulary |
| 2 | `exchange` | Conformance to `exchange.SpotClient` or `exchange.PerpClient` |
| 3 | `exchange/factory` | One typed constructor per product row |
| 4 | `credential` | The venue's auth kind |
| 5 | `routeprofile` | The venue, product, settlement, and native environments |
| 6 | `gateway/account` | The construction case for each route identity |
| 7 | `gateway/capability` | The product row: code, exact-lookup strategy, write readiness |
| 8 | `exchange/testdata/public_surface_manifest.json` | The published surface both matrices are generated from |
| 9 | `Makefile` | External acceptance targets |

A venue that stops at step 1 is real, works, and is not part of the product.
Four of the thirteen SDK families are exactly that, and
[Unsupported surfaces](../reference/unsupported.md) owns which.

## 1–2. The SDK, then the generalization

The SDK package speaks the venue's dialect: its endpoint shapes, its signing, its
symbol strings, its error codes. Nothing above it should have to know any of
that.

The exchange layer is where the dialect becomes one of two shapes, spot or perp.
Resist the urge to widen an interface to fit a venue. If a venue cannot express
something the shape asks for, that absence is a deferral recorded in the
matrices, not a new method added for one venue. An interface that grows a method
per venue has stopped being a generalization.

## 3. One factory constructor per product row

`exchange/factory` is the only place a venue client is constructed. Each product
row gets a typed constructor returning `factory.Config[exchange.SpotClient]` or
`factory.Config[exchange.PerpClient]`, which `factory.New` turns into a client.

The constructor's signature is where a venue's authentication becomes visible,
because it takes exactly the material that venue needs and nothing more:

| Constructor shape | Takes |
| --- | --- |
| `BinanceSpotConfig`, `BybitSpotConfig`, `GateSpotConfig` | Key and secret |
| `OKXSpotConfig`, `BitgetSpotConfig` | Key, secret, passphrase |
| `LighterSpotConfig` | Private key, account index, key index |
| `HyperliquidSpotConfig` | Private key |
| `AsterSpotConfig` | User address, API wallet private key, expected signer |
| `NadoSpotConfig` | Private key, subaccount |

Everything that is not credential material arrives as an option —
`WithEnvironment`, `WithHTTPClient`, `WithInstrumentCatalog`, `WithEndpoint`,
`WithWebSocketEndpoint`, `WithAccountAddress` — so adding a venue never changes
an existing constructor's arity.

## 4. The credential kind

The credential registry maps each venue to one auth kind. Nine venues currently
use six kinds:

| Auth kind | Venues |
| --- | --- |
| API key and secret | `binance`, `bybit`, `gate` |
| API key, secret, and passphrase | `okx`, `bitget` |
| Lighter signer | `lighter` |
| EVM private key | `hyperliquid` |
| Aster API wallet | `aster` |
| Nado signer | `nado` |

A bundle is decoded strictly: schema version, declared venue, and declared kind
must all agree with the registry, or it is refused as invalid. A new venue either
reuses an existing kind or adds one — it does not get decoded loosely and sorted
out later. A credential in a shape the venue does not use is a fault that must
surface at decode time, not at dispatch time.

## 5. Route profiles

`routeprofile` owns which combinations exist: venue, product, and settlement
asset. The settlement asset is empty for most spot rows, `USDT`, `USDC`, or
`USDT0` elsewhere, and it is part of the row's identity rather than a detail of
it.

There is no environment in a route profile. Each row used to expand into one
profile per environment the venue offered — a venue with both a demo endpoint
and a testnet produced three — and the registry folded those back into a mode so
that a route was never a guess about which set of credentials it meant. The
endpoints those extra profiles pointed at are gone, so a row is now exactly one
profile and its ID carries no environment segment: `binance.spot.v1`,
`nado.perp.usdt0.v1`.

## 6. Gateway construction

`gateway/account` builds a route by switching on the decoded credential kind
first, and on the identity `venue:product:settlement` second. Both must match a
case. An identity with no case is a construction failure, not a nil client that
fails later against a venue.

This is also where the layering pays: the gateway never imports an SDK package.
It holds a factory config and an instrument catalog, and that is the whole of its
knowledge about how the venue is spoken to. See
[Exchange gateway](../guides/exchange-gateway.md).

## 7. The gateway product row

`gateway/capability` is where a venue becomes something the product can trade.
Each row carries a three-letter product code, the row identity, the operations it
admits, and three fields that decide how the gateway behaves when an order goes
ambiguous:

- **Exact-lookup strategy** — how the row answers "what happened to this
  identifier". Most rows read back by client or venue order ID. Lighter's two
  rows scan the active list plus the first inactive page and report an absence as
  unconfirmed rather than proven.
- **Write-ready default** — whether the row accepts placements at all.
- **Promotion evidence** — what a row must show before that default may change,
  which for every row but one is `fixture_and_live_exact_lookup`. Lighter Spot
  must additionally show two-sided liquidity. The evidence used to be
  collectable on demo or testnet; with those gone it can only be gathered
  against the live book, which raises the cost of promotion without changing
  what promotion requires.

One field is not negotiable per venue: no row claims that pagination proves
absence, including the promoted ones. A new venue does not get to set it because
its history endpoint looks complete — see
[State and reconciliation](../concepts/state-reconciliation.md) for why an
absence is only sometimes an answer. Which rows are write-ready today is owned by
[Unsupported surfaces](../reference/unsupported.md).

## 8. The manifest drives both matrices

Do not hand-edit the operation matrices. `exchange/testdata/public_surface_manifest.json`
is the source: one entry per product row, carrying its code, venue, product,
factory config, REST and WebSocket methods, fixtures, and acceptance target.

Three test files read that manifest, and between them they close the loop:

- `exchange/docs_manifest_test.go` derives the expected matrices from it and
  compares them against both language trees.
- `exchange/export_coverage_test.go` checks the exported surface against it.
- `exchange/external_acceptance_harness_test.go` drives external acceptance from
  it.

So a matrix edit that the manifest does not justify fails, and a manifest change
that the matrices do not reflect fails too. Update the manifest, run the tests,
and let them tell you what the pages must say.

## 9. Acceptance targets

There are none, and adding one is not part of landing a venue. Two ladders used
to live here, and their names read in opposite orders, which caught people:
`test-exchange-<venue>-<env>-<product>` aggregating into
`test-exchange-acceptance`, and `test-gateway-<venue>-<product>-<env>`
aggregating into `test-gateway-demo-testnet`. Both placed real orders on the
venue's demo or testnet environment, and both were deleted along with those
environments. Nothing replaced them, because a replacement would spend real
money.

No recipe arms a credentialed write any more, and
`TestMakefileRecipesNeverEnableCredentialedWrites` fails the one that tries.
[Configuration](../reference/configuration.md) owns the exact environment-variable
names, and [Testing](../reference/testing.md) owns the targets that remain and
what a green run proves.

## What "done" means

- The offline ladder passes with no skips, including the new row's fixtures.
- The manifest, both matrices, and both language trees agree — proven by the
  tests, not by reading.
- The row appears in `routeprofile`, in the gateway construction switch, and in
  the capability matrix, with the same settlement asset in all three.
- Write readiness is claimed only with the evidence the row's promotion
  requirement names; until the row has it, the write-ready default stands.
  Offline evidence never implies production readiness — see the
  [glossary](../reference/glossary.md).

## Related

- [Architecture](../concepts/architecture.md) — what each layer is allowed to
  know
- [Exchange layer](../exchange/README.md) — the generalization the SDK is fitted
  to
- [Accounts and instruments](../concepts/accounts-instruments.md) — how a route
  becomes an account's credential
- [Unsupported surfaces](../reference/unsupported.md) — SDK-only families and
  per-row limits
- [Testing](../reference/testing.md) — the command ladder
- [Contributing documentation](documentation.md) — the mirror and curation rules

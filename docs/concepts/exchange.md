# Exchange Architecture and Concepts

[简体中文](../zh-CN/concepts/exchange.md) · This English page is canonical.

## Boundary

The `exchange` module is a standalone SDK-backed conversion layer with its own
public contracts. It gives applications one normalized API across venue SDKs.
It does not route through `adapter/*` or `runtime/*`, and those packages do not
define exchange API behavior.

The boundary is intentionally narrow:

- `exchange` owns normalized request, result, error, acknowledgement, and
  subscription models.
- `exchange/factory` owns typed venue/product construction and environment
  validation.
- `exchange/internal/factoryclient` translates the public contracts to the
  selected SDK without exposing native payloads.
- Adapter and runtime documentation may explain other repository layers, but it
  is not evidence for this module's behavior.

## Construction and lifecycle

Each venue/product constructor returns a sealed `factory.Config[C]` ticket.
`factory.New` validates that ticket and returns either an
`exchange.SpotClient` or `exchange.PerpClient`. The generic result keeps the
product-specific method set at compile time; callers do not perform runtime
feature discovery.

Construction is local. It validates credentials, explicit environment, and
endpoint options, then wires SDK clients without network I/O. The first REST
method or WebSocket watch/command performs the first exchange I/O. `Close`
releases client-owned WebSocket resources and is safe before a connection was
opened.

Environment selection is mandatory:

- `factory.EnvironmentLive` selects production endpoints.
- `factory.EnvironmentDemo` selects a venue-supported Demo profile.
- `factory.EnvironmentTestnet` selects a venue-supported Testnet profile.

A constructor rejects an environment that the selected row does not admit.

## Typed product surfaces

The public interfaces encode the Spot/Perp intersection directly:

| Facet | Spot | Perp | Role |
| --- | --- | --- | --- |
| `exchange.MarketREST` | embedded | embedded | Instruments, books, candles, and public trades |
| `exchange.OrderREST` | embedded | embedded | Place, cancel, open orders, history, and fills |
| `exchange.SpotAccountREST` | embedded | absent | Balances and Spot account |
| `exchange.PerpAccountREST` | absent | embedded | Balances, Perp account, and positions |
| `exchange.PerpREST` | absent | embedded | Funding and leverage |
| `exchange.SpotWebSocket` | returned | embedded by Perp | Public/private Spot streams and order commands |
| `exchange.PerpWebSocket` | absent | returned | Spot streams plus positions and Perp reference streams |

`exchange.SpotClient` therefore cannot call position, funding, or leverage
methods. `exchange.PerpClient` adds those methods without adding a capability
registry or a `has`/`features` negotiation layer.

## Normalized data and instruments

Spot and perpetual products have separate compile-time method sets. Instruments
use normalized exchange identifiers at the public boundary; native market IDs,
token indices, contract counts, and scaled integers remain inside the SDK
translation.

Every network method accepts `context.Context`. Prices, quantities, balances,
fees, funding rates, and other exact numeric values use `decimal.Decimal` or an
explicit optional-decimal wrapper. Models never require callers to parse native
JSON or depend on venue SDK structs.

History and market-data pages preserve only the pagination facts the venue can
prove. A missing cursor or unknown `HasMore` value is not evidence that account
history is complete.

## Order lifecycle

REST and WebSocket order commands share `exchange.PlaceOrderRequest`:

- market orders have quantity but no limit price or limit policy;
- limit orders require a positive price and one of resting, IOC, or post-only;
- `ReduceOnly` is valid only for Perp;
- `ClientOrderID` is a required portable positive decimal `uint48` string; and
- cancellation uses the opaque `OrderID` returned by the selected row.

Commands return `exchange.OrderAcknowledgement`, not a guessed final order.
Accepted-pending, resting, partial-fill, immediate-fill, canceled, rejected, and
ambiguous states preserve what the venue response actually proves. An
ambiguous send must be reconciled by its original correlation identifiers
before a replacement write is attempted.

## WebSocket lifecycle

`WebSocket()` returns a lazy typed facet. Each watch returns
`exchange.Subscription[T]` with an opaque ID plus `Events`, `Status`, `Errors`,
and an idempotent `Close`. Startup failures return from the watch call;
asynchronous failures arrive on `Errors`. Status events distinguish connecting,
active, gap, resyncing, and closed states.

Most interface members are implemented by all admitted rows. A documented
venue exception returns `exchange.ErrUnsupported` from the method itself rather
than removing that method from the public intersection.

## Account and mode dimensions

Do not collapse account system, logical account identity, position mode, margin
mode, and leverage into one “account mode.” They are separate dimensions. The
central [venue and account-mode reference](../reference/exchange-venues-and-account-modes.md)
records their exchange-specific differences.

| Dimension | Question it answers | Exchange behavior |
| --- | --- | --- |
| Account system | Which venue balance/risk system is queried? | Uses the selected row's documented account API; it does not migrate or switch the account. |
| Logical account identity | Which owner, subaccount, or indexed account is queried? | Comes from the typed config and, where supported, an explicit identity option. |
| Position mode | Is the Perp account net/one-way or hedge/multi-side? | Preserves only the scopes documented for the selected row; there is no common mode setter. |
| Margin mode | Is risk cross, isolated, unified, or venue-managed? | Uses the row's documented fixed or current mode; there is no common margin-mode setter. |
| Leverage | Can instrument leverage be changed? | Perp exposes `SetLeverage`; `Effective=0` means the call succeeded but the venue has no instrument leverage setting. |

## Errors and unsupported behavior

All normalized failures use `exchange.Error` kinds with safe details. Transport
failure after a possible write may return `exchange.ErrAmbiguousOutcome`;
callers must not convert it into a blind retry. A method-level platform gap uses
`exchange.ErrUnsupported` and is documented in the centralized venue table.
Credentials, signatures, native payloads, and raw request bodies are not part of
the public error contract.

## Exact detail owners

- [REST operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
  owns the exact REST inventory.
- [WebSocket operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
  owns the exact WebSocket inventory.
- [Venue and account-mode differences](../reference/exchange-venues-and-account-modes.md)
  owns exchange-specific identity and mode caveats.
- [Configuration](../reference/configuration.md) owns environment and
  credential-name rules.
- [Testing and evidence](../reference/testing.md) owns certification semantics.

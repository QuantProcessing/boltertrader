# Unified Exchange API Quickstart

[Chinese mirror](../zh-CN/guides/exchange-rest-quickstart.md) · This English page is canonical.

## Scope and safety

The `exchange` package is a standalone normalized SDK surface. It exposes typed
REST clients and typed WebSocket facets without exposing native SDK payloads,
raw request escape hatches, or venue-specific IDs such as market IDs, token
indices, contract counts, or scaled integers. Every network method accepts
`context.Context`.

`factory.New` validates configuration and constructs a typed
`exchange.SpotClient` or `exchange.PerpClient` locally. Construction performs no
exchange I/O; the first client method performs the first network request.

Environment selection is mandatory. Pass `factory.EnvironmentLive`,
`factory.EnvironmentDemo`, or `factory.EnvironmentTestnet` explicitly. This
guide does not provide a production-write command and does not claim live
validation has passed.

## Construct a read-only Demo client

This complete example constructs Binance Spot with the built-in Demo profile and
performs only public instrument discovery:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange/factory"
)

func main() {
	client, err := factory.New(factory.BinanceSpotConfig(
		os.Getenv("BINANCE_DEMO_API_KEY"),
		os.Getenv("BINANCE_DEMO_API_SECRET"),
		factory.WithEnvironment(factory.EnvironmentDemo),
	))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	instruments, err := client.Instruments(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(instruments))
}
```

Export credentials into the process environment before running this standalone
example; the program does not load `.env` automatically. Never place keys,
secrets, passphrases, private keys, signatures, or signed payloads in source,
logs, fixtures, or version control. The exact shared names and loading rules are
owned by [Configuration](../reference/configuration.md).

## Factory rows

Choose exactly one typed construction ticket and pass it to `factory.New`.
Generic inference preserves the product method set at compile time.

| Row | Venue | Product | Factory config |
| --- | --- | --- | --- |
| BNS | Binance | Spot | `BinanceSpotConfig` |
| BNP | Binance | USD-M Perp | `BinanceUSDPerpConfig` |
| OXS | OKX | Spot | `OKXSpotConfig` |
| OXP | OKX | USDT-linear SWAP | `OKXUSDTPerpConfig` |
| LIS | Lighter | Spot | `LighterSpotConfig` |
| LIP | Lighter | Perp | `LighterPerpConfig` |
| HLS | Hyperliquid | Spot | `HyperliquidSpotConfig` |
| HLP | Hyperliquid | Standard Perp | `HyperliquidPerpConfig` |

Binance constructors take API key and secret. OKX constructors also take a
passphrase. Lighter constructors take a private key, account index, and API key
index. Hyperliquid constructors take a private key. Every constructor also
accepts `factory.Option` values.

| Venue | Non-production environment | Live environment |
| --- | --- | --- |
| Binance | `factory.EnvironmentDemo` | `factory.EnvironmentLive` |
| OKX | `factory.EnvironmentDemo` | `factory.EnvironmentLive` |
| Lighter | `factory.EnvironmentTestnet` | `factory.EnvironmentLive` |
| Hyperliquid | `factory.EnvironmentTestnet` | `factory.EnvironmentLive` |

`factory.WithEndpoint` and `factory.WithWebSocketEndpoint` override REST and
WebSocket endpoints. `factory.WithHTTPClient` installs the HTTP client used by
REST methods. These options are advanced test/construction hooks; never redirect
a non-production write to a production endpoint.

## REST method surface

The public method set is the compile-time contract, not a runtime capability
guess.

| Method | Spot | Perp |
| --- | --- | --- |
| Instruments | yes | yes |
| OrderBook | yes | yes |
| Candles | yes | yes |
| PublicTrades | yes | yes |
| PlaceOrder | yes | yes |
| CancelOrder | yes | yes |
| OpenOrders | yes | yes |
| OrderHistory | yes | yes |
| Fills | yes | yes |
| Balances | yes | yes |
| SpotAccount | yes | no |
| PerpAccount | no | yes |
| Positions | no | yes |
| FundingRate | no | yes |
| FundingRateHistory | no | yes |
| SetLeverage | no | yes |

Spot clients have no position, funding, leverage, margin-mode, or reduce-only
surface. Perp clients add account summary, positions, funding, and leverage
methods.

## WebSocket method surface

Call `client.WebSocket()` to get the lazy WebSocket facet. Constructing the
client and retrieving the facet does not connect; the first watch or WebSocket
command starts the relevant backend.

| Method | SpotWebSocket | PerpWebSocket | Scope |
| --- | --- | --- | --- |
| WatchOrderBook | yes | yes | public market stream |
| WatchBBO | yes | yes | public market stream |
| WatchPublicTrades | yes | yes | public market stream |
| WatchCandles | yes | yes | public market stream |
| WatchOrders | yes | yes | private account stream |
| WatchFills | yes | yes | private account stream |
| WatchBalances | yes | yes | private account stream; account-wide |
| PlaceOrder | yes | yes | private WebSocket command |
| CancelOrder | yes | yes | private WebSocket command |
| WatchPositions | no | yes | private Perp account stream |
| WatchMarkPrice | no | yes | public Perp reference stream |
| WatchFundingRate | no | yes | public Perp reference stream |
| Close | yes | yes | closes the WebSocket facet |

Each watch returns `exchange.Subscription[T]` with `ID`, `Events`, `Status`,
`Errors`, and `Close`. Public and private stream coverage is detailed in
[Exchange WebSocket V1 operation matrix](../reference/exchange-ws-v1-operation-matrix.md).

## Order request shapes

`PlaceOrder` accepts `exchange.PlaceOrderRequest` for both REST and WebSocket
commands. Prices and quantities use `github.com/shopspring/decimal`.

| Shape | Required fields | Forbidden fields |
| --- | --- | --- |
| Market | `Instrument`, `Side`, `Type: exchange.OrderTypeMarket`, positive `Quantity` | `LimitPrice`, `LimitPolicy` |
| Limit resting | market fields plus positive `LimitPrice`, `LimitPolicy: exchange.LimitPolicyResting` | none |
| Limit IOC | market fields plus positive `LimitPrice`, `LimitPolicy: exchange.LimitPolicyIOC` | none |
| Limit post-only | market fields plus positive `LimitPrice`, `LimitPolicy: exchange.LimitPolicyPostOnly` | none |
| Perp reduce-only | any valid market or limit shape plus `ReduceOnly: true` on `exchange.ProductPerp` | invalid on Spot |

Every `PlaceOrder` requires `ClientOrderID` as a positive decimal `uint48`
string without a leading zero (`1` through `281474976710655`). It is
round-tripped when the selected row returns it. The portable `CancelOrder`
locator is `OrderID`; client-order-ID-only cancel is not a shared guarantee
across all eight rows. `OrderID` must be a positive decimal `int64` string
without a leading zero.

```go
market := exchange.PlaceOrderRequest{
	Instrument:    "ETH-USDT",
	ClientOrderID: "101",
	Side:          exchange.SideBuy,
	Type:          exchange.OrderTypeMarket,
	Quantity:      decimal.RequireFromString("0.05"),
}

postOnlyReduce := exchange.PlaceOrderRequest{
	Instrument:    "ETH-USDT",
	ClientOrderID: "102",
	Side:          exchange.SideSell,
	Type:          exchange.OrderTypeLimit,
	Quantity:      decimal.RequireFromString("0.05"),
	LimitPrice:    decimal.RequireFromString("3500"),
	LimitPolicy:   exchange.LimitPolicyPostOnly,
	ReduceOnly:    true,
}
```

## Binance and Lighter orders

This example shows simultaneous construction of a Binance Spot Demo client and a
Lighter Perp Testnet client, then submits one order to each. It is intended for
type-checking and controlled non-production acceptance only; executing it sends
real non-production orders.

Set `BINANCE_DEMO_SPOT_SYMBOL` to the exchange-normalized instrument form, for
example `ETH-USDT`, not the native Binance form `ETHUSDT`. Set
`LIGHTER_TESTNET_PERP_SYMBOL` explicitly to a known testnet market symbol for
the configured account.

```go
package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/shopspring/decimal"
)

func main() {
	binance, err := factory.New(factory.BinanceSpotConfig(
		os.Getenv("BINANCE_DEMO_API_KEY"),
		os.Getenv("BINANCE_DEMO_API_SECRET"),
		factory.WithEnvironment(factory.EnvironmentDemo),
	))
	if err != nil {
		log.Fatal(err)
	}
	defer binance.Close()

	lighter, err := factory.New(factory.LighterPerpConfig(
		os.Getenv("LIGHTER_TESTNET_PRIVATE_KEY"),
		int64Env("LIGHTER_TESTNET_ACCOUNT_INDEX"),
		uint8(int64Env("LIGHTER_TESTNET_API_KEY_INDEX")),
		factory.WithEnvironment(factory.EnvironmentTestnet),
	))
	if err != nil {
		log.Fatal(err)
	}
	defer lighter.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err = binance.PlaceOrder(ctx, exchange.PlaceOrderRequest{
		Instrument:    requiredEnv("BINANCE_DEMO_SPOT_SYMBOL"),
		ClientOrderID: "101",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.RequireFromString("0.001"),
		LimitPrice:    decimal.RequireFromString("1"),
		LimitPolicy:   exchange.LimitPolicyPostOnly,
	})
	if err != nil {
		log.Fatal(err)
	}

	_, err = lighter.PlaceOrder(ctx, exchange.PlaceOrderRequest{
		Instrument:    requiredEnv("LIGHTER_TESTNET_PERP_SYMBOL"),
		ClientOrderID: "102",
		Side:          exchange.SideSell,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.RequireFromString("0.001"),
		LimitPrice:    decimal.RequireFromString("999999"),
		LimitPolicy:   exchange.LimitPolicyPostOnly,
	})
	if err != nil {
		log.Fatal(err)
	}
}

func requiredEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func int64Env(name string) int64 {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil {
		log.Fatalf("%s must be an int64: %v", name, err)
	}
	return value
}
```

## Subscription close and errors

`Subscription.Close` is idempotent. It unsubscribes the local subscription,
emits `SubscriptionClosed` on `Status`, and closes `Events`, `Status`, and
`Errors`. Canceling the watch context also closes that subscription. Closing the
WebSocket facet closes all subscriptions owned by that facet.

Startup and validation failures are returned directly from the watch or command
call. Asynchronous stream failures are delivered on `Errors`; lifecycle changes
are delivered on `Status` as `connecting`, `active`, `gap`, `resyncing`, or
`closed`. Gap statuses use `GapStarted` and `GapRecovered`. Treat an error as
terminal only when the subscription is also closed or the documented operation
requires restart.

`WatchCandles` accepts only `1m`, `5m`, `15m`, `30m`, `1h`, `4h`, `12h`, or
`1d`. `WatchRequest.Options.Buffer` and `WatchAccountRequest.Options.Buffer`
default to `1024` when zero and must be between `0` and `65536`.

## Acknowledgement and ambiguity

`PlaceOrder` and `CancelOrder` return `exchange.OrderAcknowledgement`. A successful
HTTP, WebSocket, or venue envelope alone is not enough to infer a terminal order
state.

| Acknowledgement | Meaning |
| --- | --- |
| AcceptedPending | The venue accepted the command or transaction handoff, but resting, fill, or cancellation is not yet proven |
| Resting | The native response proves that the order is resting |
| PartiallyFilled | The native response proves a positive partial fill |
| ImmediatelyFilled | The native response proves an immediate fill |
| Canceled | The native response proves cancellation and preserves a correlation reference |
| Rejected | The venue definitively rejected the command |
| Ambiguous | A send may have occurred, but no authoritative result is available |

`PartiallyFilled` requires a positive filled quantity. `Canceled` requires an
order reference, client order ID, or transaction hash. `Resting` is invalid for
a market-order acknowledgement.

`AcceptedPending` is not a final fill or cancellation. Preserve every returned
order ID, client order ID, and transaction hash, then use bounded `OpenOrders`,
`OrderHistory`, `Fills`, account, and position evidence appropriate to the
product.

An `Ambiguous` acknowledgement is paired with
`exchange.ErrAmbiguousOutcome`. Do not blindly retry with a new identity, submit a
compensating order, or assume that an error means no write reached the venue.
Freeze replacement writes and reconcile the original correlation identifiers.
See [Operations and recovery](operations-recovery.md).

## Demo/Testnet acceptance

Offline exchange conformance is deterministic and credential-free:

```sh
make test-exchange-offline
```

Passing offline tests proves the normalized implementation and fixture contract;
it does not prove current external availability. Demo/Testnet validation is
optional, credentialed, environment-specific, and bounded by the exact method
invoked. `PlaceOrder` and `CancelOrder`, including WebSocket commands, mutate
real non-production exchange state even when they target Demo or Testnet.

Capability support and external environment certification are separate. The
REST and WebSocket matrices show implemented/admitted capability cells; they do
not by themselves certify that the current Demo/Testnet environment passed. The
current external acceptance status is:

| Row | Acceptance status | Reason |
| --- | --- | --- |
| BNS | Passed | Binance Demo spot row passed external acceptance. |
| BNP | Passed | Binance Demo USD-M perp row passed external acceptance. |
| OXS | Passed | OKX Demo spot row passed external acceptance. |
| OXP | Passed | OKX Demo USDT-linear SWAP row passed external acceptance. |
| LIS | Waived | Lighter Testnet ETH/USDC and LIT/USDC had platform-provided one-sided books; the user accepted the waiver, and no synthetic liquidity/self-trade was used. |
| LIP | Passed | Lighter Testnet perp row passed external acceptance. |
| HLS | Passed | Hyperliquid Testnet spot row passed external acceptance. |
| HLP | Passed | Hyperliquid Testnet standard perp row passed external acceptance. |

Use Demo/Testnet only with dedicated credentials, explicit symbols, explicit
notional bounds, serial execution, and terminal cleanup evidence. A Demo/Testnet
success does not imply production readiness. Follow
[Testing and evidence](../reference/testing.md) and
[Configuration](../reference/configuration.md) before external validation.

- [Exchange contracts](../../exchange/contract.go)
- [Factory API](../../exchange/factory/factory.go)
- [Exchange REST V1 operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
- [Exchange WebSocket V1 operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
- [Operations and recovery](operations-recovery.md)

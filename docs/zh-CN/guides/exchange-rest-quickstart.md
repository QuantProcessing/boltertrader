# 统一交易所 API 快速入门

[English](../../guides/exchange-rest-quickstart.md) · 本页镜像英文规范页。

## 范围与安全

`exchange` package 是独立的规范化 SDK surface。它暴露 typed REST client 和
typed WebSocket facet，但不暴露 native SDK payload、raw request escape hatch，
也不暴露 market ID、token index、contract count、scaled integer 等
venue-specific ID。每个 network method 都接受 `context.Context`。

`factory.New` 在本地验证配置，并构造 typed `exchange.SpotClient` 或
`exchange.PerpClient`。构造过程不执行 exchange I/O；第一次调用 client method
时才会发出第一次 network request。

environment 必须显式选择：`factory.EnvironmentLive`、
`factory.EnvironmentDemo` 或 `factory.EnvironmentTestnet`。本指南不提供
production-write command，也不声称 live validation 已通过。

## 构造只读 Demo client

下面的完整示例使用内置 Demo profile 构造 Binance Spot，并且只执行 public
instrument discovery：

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

运行这个 standalone example 前，先把 credential export 到 process
environment；这个 program 不会自动加载 `.env`。绝不要把 key、secret、
passphrase、private key、signature 或 signed payload 写入 source、log、
fixture 或 version control。精确的共享名称与 loading rule 由
[配置](../reference/configuration.md)页面维护。

## Factory 行

只选择一个 typed construction ticket，并将其传给 `factory.New`。Generic
inference 会在 compile time 保留 product method set。

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

Binance constructor 接受 API key 与 secret。OKX constructor 还接受 passphrase。
Lighter constructor 接受 private key、account index 与 API key index。
Hyperliquid constructor 接受 private key。所有 constructor 也都接受
`factory.Option` value。

| Venue | Non-production environment | Live environment |
| --- | --- | --- |
| Binance | `factory.EnvironmentDemo` | `factory.EnvironmentLive` |
| OKX | `factory.EnvironmentDemo` | `factory.EnvironmentLive` |
| Lighter | `factory.EnvironmentTestnet` | `factory.EnvironmentLive` |
| Hyperliquid | `factory.EnvironmentTestnet` | `factory.EnvironmentLive` |

`factory.WithEndpoint` 与 `factory.WithWebSocketEndpoint` 分别覆盖 REST 与
WebSocket endpoint。`factory.WithHTTPClient` 安装 REST method 使用的 HTTP
client。这些 option 是高级 test/construction hook；绝不要把 non-production
write 重定向到 production endpoint。

## REST Method Surface

Public method set 是 compile-time contract，不是 runtime capability 推测。

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

Spot client 没有 position、funding、leverage、margin-mode 或 reduce-only
surface。Perp client 增加 account summary、positions、funding 和 leverage
method。

## WebSocket Method Surface

调用 `client.WebSocket()` 获取 lazy WebSocket facet。构造 client 和获取 facet
都不会连接；第一次 watch 或 WebSocket command 才会启动相关 backend。

| Method | SpotWebSocket | PerpWebSocket | Scope |
| --- | --- | --- | --- |
| WatchOrderBook | yes | yes | public market stream |
| WatchBBO | yes | yes | public market stream |
| WatchPublicTrades | yes | yes | public market stream |
| WatchCandles | yes | yes | public market stream |
| WatchOrders | yes | yes | private account stream |
| WatchFills | yes | yes | private account stream |
| WatchBalances | yes | yes | private account stream；account-wide |
| PlaceOrder | yes | yes | private WebSocket command |
| CancelOrder | yes | yes | private WebSocket command |
| WatchPositions | no | yes | private Perp account stream |
| WatchMarkPrice | no | yes | public Perp reference stream |
| WatchFundingRate | no | yes | public Perp reference stream |
| Close | yes | yes | 关闭 WebSocket facet |

每个 watch 返回 `exchange.Subscription[T]`，包含 `ID`、`Events`、`Status`、
`Errors` 与 `Close`。Public 与 private stream coverage 见
[Exchange WebSocket V1 operation matrix](../reference/exchange-ws-v1-operation-matrix.md)。

## Order Request Shape

REST 与 WebSocket command 的 `PlaceOrder` 都接受
`exchange.PlaceOrderRequest`。Price 和 quantity 使用
`github.com/shopspring/decimal`。

| Shape | Required fields | Forbidden fields |
| --- | --- | --- |
| Market | `Instrument`、`Side`、`Type: exchange.OrderTypeMarket`、positive `Quantity` | `LimitPrice`、`LimitPolicy` |
| Limit resting | market field 加 positive `LimitPrice`、`LimitPolicy: exchange.LimitPolicyResting` | none |
| Limit IOC | market field 加 positive `LimitPrice`、`LimitPolicy: exchange.LimitPolicyIOC` | none |
| Limit post-only | market field 加 positive `LimitPrice`、`LimitPolicy: exchange.LimitPolicyPostOnly` | none |
| Perp reduce-only | 任意有效 market 或 limit shape，并在 `exchange.ProductPerp` 上设置 `ReduceOnly: true` | Spot 上无效 |

每个 `PlaceOrder` 都必须提供 `ClientOrderID`，其值必须是不带 leading zero
的 positive decimal `uint48` string（`1` 至 `281474976710655`）；所选 row
返回它时，exchange 会 round-trip。Portable `CancelOrder` locator 是 `OrderID`；
client-order-ID-only cancel 不是八行共享保证。`OrderID` 必须是不带 leading
zero 的 positive decimal `int64` string。

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

## Binance 与 Lighter 下单

下面的示例同时构造 Binance Spot Demo client 与 Lighter Perp Testnet client，
并分别提交一个订单。它用于 type-checking 和受控 non-production acceptance；
实际执行会发送真实的 non-production order。

`BINANCE_DEMO_SPOT_SYMBOL` 必须使用 exchange-normalized instrument form，例如
`ETH-USDT`，不能使用 native Binance form `ETHUSDT`。
`LIGHTER_TESTNET_PERP_SYMBOL` 必须显式设置为已知适用于当前 configured account
的 testnet market symbol。

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

## Subscription Close 与 Error

`Subscription.Close` 是 idempotent。它会 unsubscribe local subscription，在
`Status` 上 emit `SubscriptionClosed`，并关闭 `Events`、`Status` 与 `Errors`。
取消 watch context 也会关闭该 subscription。关闭 WebSocket facet 会关闭该
facet 拥有的所有 subscription。

Startup 与 validation failure 会直接从 watch 或 command call 返回。
Asynchronous stream failure 会发送到 `Errors`；lifecycle change 会发送到
`Status`，状态为 `connecting`、`active`、`gap`、`resyncing` 或 `closed`。
Gap status 使用 `GapStarted` 与 `GapRecovered`。只有当 subscription 同时关闭，
或 documented operation 要求 restart 时，才把 error 视为 terminal。

`WatchCandles` 只接受 `1m`、`5m`、`15m`、`30m`、`1h`、`4h`、`12h` 或
`1d`。`WatchRequest.Options.Buffer` 与 `WatchAccountRequest.Options.Buffer`
为 zero 时默认是 `1024`，并且必须在 `0` 到 `65536` 之间。

## Acknowledgement 与 Ambiguity

`PlaceOrder` 与 `CancelOrder` 返回 `exchange.OrderAcknowledgement`。只有成功的
HTTP、WebSocket 或 venue envelope，并不足以推断 terminal order state。

| Acknowledgement | Meaning |
| --- | --- |
| AcceptedPending | Venue 已接受 command 或 transaction handoff，但尚未证明 resting、fill 或 cancellation |
| Resting | Native response 证明订单处于 resting 状态 |
| PartiallyFilled | Native response 证明订单有 positive partial fill |
| ImmediatelyFilled | Native response 证明订单立即成交 |
| Canceled | Native response 证明订单已取消并保留 correlation reference |
| Rejected | Venue 明确拒绝 command |
| Ambiguous | 可能已经发送，但没有可用的权威结果 |

`PartiallyFilled` 要求 positive filled quantity。`Canceled` 要求 order
reference、client order ID 或 transaction hash。Market order acknowledgement
不能是 `Resting`。

`AcceptedPending` 不是最终 fill 或 cancellation。保留返回的每个 order ID、
client order ID 与 transaction hash，再使用适合该 product 的 bounded
`OpenOrders`、`OrderHistory`、`Fills`、account 与 position 证据。

`Ambiguous` acknowledgement 会与 `exchange.ErrAmbiguousOutcome` 一起返回。
不要使用新 identity 盲目重试、提交 compensating order，或因为返回 error 就假定
write 没有到达 venue。冻结 replacement write，并对原始 correlation identifier
执行 reconciliation。参见[运维与恢复](operations-recovery.md)。

## Demo/Testnet 验收

Exchange offline conformance suite 是 deterministic 且不需要 credential 的：

```sh
make test-exchange-offline
```

通过 offline test 能够证明 normalized implementation 与 fixture contract；
它不能证明当前 external availability。Demo/Testnet validation 是可选的，需要
credential，依赖具体 environment，并受实际调用 method 的边界限制。
`PlaceOrder` 与 `CancelOrder`，包括 WebSocket command，即使面向 Demo 或
Testnet，也会修改真实的 non-production exchange state。

Capability support 与 external environment certification 是两个独立概念。
REST 与 WebSocket matrix 中的 capability cell 表示 implemented/admitted
support；它本身不证明当前 Demo/Testnet environment 已通过验收。当前 external
acceptance status 如下：

| Row | Acceptance status | Reason |
| --- | --- | --- |
| BNS | Passed | Binance Demo spot row 已通过 external acceptance。 |
| BNP | Passed | Binance Demo USD-M perp row 已通过 external acceptance。 |
| OXS | Passed | OKX Demo spot row 已通过 external acceptance。 |
| OXP | Passed | OKX Demo USDT-linear SWAP row 已通过 external acceptance。 |
| LIS | Waived | Lighter Testnet ETH/USDC 与 LIT/USDC 是 platform-provided one-sided books；user accepted waiver，且未使用 synthetic liquidity/self-trade。 |
| LIP | Passed | Lighter Testnet perp row 已通过 external acceptance。 |
| HLS | Passed | Hyperliquid Testnet spot row 已通过 external acceptance。 |
| HLP | Passed | Hyperliquid Testnet standard perp row 已通过 external acceptance。 |

只使用专用 credential、显式 symbol、显式 notional bound、serial execution 与
terminal cleanup evidence 来执行 Demo/Testnet。Demo/Testnet success 不代表
production readiness。External validation 前请遵循
[测试与证据](../reference/testing.md)以及[配置](../reference/configuration.md)。

- [Exchange contract](../../../exchange/contract.go)
- [Factory API](../../../exchange/factory/factory.go)
- [Exchange REST V1 operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
- [Exchange WebSocket V1 operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
- [运维与恢复](operations-recovery.md)

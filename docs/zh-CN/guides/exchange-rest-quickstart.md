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

每个 client 都连向交易所的 production endpoint。`factory.EnvironmentLive` 是
唯一的 environment，也是默认值，因此 `factory.WithEnvironment` 可选，且不接受
其他取值。demo/testnet profile 已被删除——各交易所的实现差异过大；需要点着看
的场景由 `web/` 的离线 mock 承担。

## 构造只读 client

下面的完整示例构造 Binance Spot，并且只执行 public instrument discovery——
这一步不需要凭据也成立：

<!-- exchange-doc-example:compile -->
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
		os.Getenv("BINANCE_API_KEY"),
		os.Getenv("BINANCE_API_SECRET"),
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

## 选择 client

选择一个 typed construction ticket，并将其传给 `factory.New`。Generic
inference 会在 compile time 保留 Spot 或 Perp method set。
[REST operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
统一维护精确的二十个 product row、factory constructor、REST method 与
acknowledgement state。

不同 exchange 的 account identity、position mode、margin mode、leverage
behavior 与 venue-specific order caveat 并不相同。发送 private request 前请阅读
[交易所差异与账户模式](../reference/exchange-venues-and-account-modes.md)。
尤其需要注意，公共 interface 并不意味着每个 venue 都为每个 account dimension
提供原生 setter。

`factory.WithEndpoint` 与 `factory.WithWebSocketEndpoint` 分别覆盖 REST 与
WebSocket endpoint。`factory.WithHTTPClient` 安装 REST method 使用的 HTTP
client。这些 option 是高级 test/construction hook；被重定向的 endpoint 依然
携带 production credential，因此只能指向你自己掌控的 fixture server。

## 使用 WebSocket facet

调用 `client.WebSocket()` 获取 lazy WebSocket facet。构造 client 和获取 facet
都不会连接；第一次 watch 或 WebSocket command 才会启动相关 backend。

每个 watch 返回 `exchange.Subscription[T]`，包含 `ID`、`Events`、`Status`、
`Errors` 与 `Close`。
[WebSocket operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
统一维护每个 product row 的精确 public/private stream 与 command inventory，
也包括文档约定的 `exchange.ErrUnsupported` 结果。

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
client-order-ID-only cancel 不是二十行共享保证。`OrderID` 是所选 venue row
返回的 opaque identifier：数值型 venue 要求不带 leading zero 的 canonical
positive decimal `int64` string；Nado 要求 lowercase `0x` 前缀的 32-byte
order digest。

```go
instrument, err := client.ResolveInstrument(ctx, "ETH-USDT")
if err != nil {
	return err
}

market := exchange.PlaceOrderRequest{
	Instrument:    instrument,
	ClientOrderID: "101",
	Side:          exchange.SideBuy,
	Type:          exchange.OrderTypeMarket,
	Quantity:      decimal.RequireFromString("0.05"),
}

postOnlyReduce := exchange.PlaceOrderRequest{
	Instrument:    instrument,
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

下面的示例同时构造 Binance Spot client 与 Lighter Perp client，并分别提交一个
订单。**两个 venue 都是 production。** 实际执行会花掉真金白银；这段代码写在
这里是为了通过 type-checking，而不是让你随手运行。

`BINANCE_SPOT_SYMBOL` 必须使用 exchange-normalized instrument form，例如
`ETH-USDT`，不能使用 native Binance form `ETHUSDT`。`LIGHTER_PERP_SYMBOL`
必须显式设置为已知适用于当前 configured account 的 market symbol。

<!-- exchange-doc-example:compile -->
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
		os.Getenv("BINANCE_API_KEY"),
		os.Getenv("BINANCE_API_SECRET"),
	))
	if err != nil {
		log.Fatal(err)
	}
	defer binance.Close()

	lighter, err := factory.New(factory.LighterPerpConfig(
		os.Getenv("LIGHTER_PRIVATE_KEY"),
		int64Env("LIGHTER_ACCOUNT_INDEX"),
		uint8(int64Env("LIGHTER_API_KEY_INDEX")),
	))
	if err != nil {
		log.Fatal(err)
	}
	defer lighter.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	binanceInstrument, err := binance.ResolveInstrument(ctx, requiredEnv("BINANCE_SPOT_SYMBOL"))
	if err != nil {
		log.Fatal(err)
	}
	lighterInstrument, err := lighter.ResolveInstrument(ctx, requiredEnv("LIGHTER_PERP_SYMBOL"))
	if err != nil {
		log.Fatal(err)
	}

	_, err = binance.PlaceOrder(ctx, exchange.PlaceOrderRequest{
		Instrument:    binanceInstrument,
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
		Instrument:    lighterInstrument,
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
[REST operation matrix](../reference/exchange-rest-v1-operation-matrix.md#acknowledgement-state)
统一维护精确的 acknowledgement-state inventory。

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

## 验收

Exchange offline conformance suite 是 deterministic 且不需要 credential 的：

```sh
make test-exchange-offline
```

通过 offline test 能够证明 normalized implementation 与 fixture contract；
它不能证明当前 external availability。现在已经没有可供验证的 non-production
environment：每一次带 credential 的调用都直达真实 venue，`PlaceOrder` 与
`CancelOrder`（包括 WebSocket command）都会花掉真金白银。

Capability support 与 external availability 是两个独立概念。REST 与 WebSocket
matrix 中的 capability cell 记录的是 compile time 的 admitted support，
它本身不证明该 venue 今天仍然可用。

只使用专用账户、显式 symbol、显式 notional bound、serial execution 与 terminal
cleanup evidence 来对 production 做验证。开始之前请先遵循
[测试与证据](../reference/testing.md)以及[配置](../reference/configuration.md)。
如果只是需要点着看、完全不想碰 venue，请用前端的离线 mock，而不是去找 credential。

- [Exchange contract](../../../exchange/contract.go)
- [Factory API](../../../exchange/factory/factory.go)
- [Exchange REST V1 operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
- [Exchange WebSocket V1 operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
- [运维与恢复](operations-recovery.md)

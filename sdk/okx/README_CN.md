# OKX SDK 使用指南与核心概念

本文档介绍直接使用 `github.com/QuantProcessing/exchanges/sdk/okx` 时最常见的
OKX V5 API 概念：`instId`、`tdMode`、`posSide`，以及 `instType`、
`instFamily` 和具体交易标的之间的区别。

## 产品 ID (`instId`)

在 OKX V5 API 中，所有交易标的都通过 `instId` 唯一标识。调用 `GetTicker`、
`GetOrderBook`、`PlaceOrder` 等接口时都需要传入它。

| 产品类型 | 格式规则 | 示例 | 说明 |
| --- | --- | --- | --- |
| 币币现货 | `币种-计价货币` | `BTC-USDT` | BTC 现货，USDT 计价。 |
| 永续合约 | `币种-计价货币-SWAP` | `BTC-USDT-SWAP` | U 本位永续合约。 |
| 永续合约 | `币种-USD-SWAP` | `BTC-USD-SWAP` | 币本位反向永续合约。 |
| 交割合约 | `币种-计价货币-日期` | `BTC-USDT-250328` | 后六位是到期日，格式为 YYMMDD。 |
| 期权 | `币种-USD-日期-行权价-类型` | `BTC-USD-250328-100000-C` | `C` 为看涨，`P` 为看跌。 |

如果用户只输入 `BTC`，程序应根据交易场景构造最终 `instId`：

- 买 BTC 现货：`BTC` -> `BTC-USDT`；
- 做 BTC U 本位永续：`BTC` -> `BTC-USDT-SWAP`；
- 做 BTC 币本位永续：`BTC` -> `BTC-USD-SWAP`。

建议通过 `client.GetInstruments(ctx, "SWAP")` 等接口建立本地 instrument 缓存，
不要凭字符串猜测交易所是否已上线对应市场。

## 交易模式 (`tdMode`)

下单时，`tdMode` 决定保证金使用方式。

| 值 | 含义 | 常见场景 |
| --- | --- | --- |
| `cash` | 非保证金模式 | 现货订单。 |
| `cross` | 全仓模式 | 保证金和合约交易，共享账户担保资产。 |
| `isolated` | 逐仓模式 | 仓位独立保证金，便于控制单仓风险。 |

## 持仓方向 (`posSide`)

合约交易，尤其是双向持仓模式下，需要用 `posSide` 指定订单作用于哪一侧仓位。

| 值 | 含义 |
| --- | --- |
| `long` | 开多或平多。 |
| `short` | 开空或平空。 |
| `net` | 单向持仓模式；现货/cash 流程也可理解为默认净持仓。 |

## 快速示例

查询 BTC 价格：

```go
instID := "BTC-USDT" // 现货
// instID := "BTC-USDT-SWAP" // 永续

ticker, err := client.GetTicker(ctx, instID)
fmt.Println("最新价:", ticker[0].Last)
```

下一笔 BTC 现货限价买单：

```go
req := &okx.OrderRequest{
    InstId:  "BTC-USDT",
    TdMode:  "cash",
    Side:    "buy",
    OrdType: "limit",
    Px:      "95000",
    Sz:      "0.1",
}
resp, err := client.PlaceOrder(ctx, req)
```

下一笔 BTC 永续逐仓开多：

```go
req := &okx.OrderRequest{
    InstId:  "BTC-USDT-SWAP",
    TdMode:  "isolated",
    Side:    "buy",
    PosSide: "long",
    OrdType: "market",
    Sz:      "1",
}
resp, err := client.PlaceOrder(ctx, req)
```

## 数量单位

OKX 的 `Sz` 含义随产品不同而变化：

- 现货：交易币种数量，例如 `1` 表示 1 BTC；
- 合约：合约张数。应先查询 instrument metadata 中的 `ctVal`、`ctMult`，再把目标
  币本位或名义规模换算成张数。

## `instType` 与 `instFamily`

`instType` 是产品大类，例如 `SPOT`、`SWAP`、`FUTURES`、`OPTION`、`MARGIN`。

`instFamily` 是标的族，通常用于合约和期权，例如 `BTC-USD` 或 `BTC-USDT`。

`instId` 是最终具体市场，例如 `BTC-USDT-SWAP`。

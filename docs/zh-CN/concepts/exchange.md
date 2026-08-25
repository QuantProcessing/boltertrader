# Exchange 架构与概念

[English](../../concepts/exchange.md) · 本页镜像英文规范页。

## 边界

`exchange` 模块是拥有自身 public contract 的独立 SDK-backed conversion
layer。它在 venue SDK 之上为应用提供统一 API。它不经过 `adapter/*` 或
`runtime/*` 路由，这些 package 也不定义 exchange API 行为。

这个边界有意保持收窄：

- `exchange` 维护 normalized request、result、error、acknowledgement 与
  subscription model。
- `exchange/factory` 维护 typed venue/product 构造与 environment validation。
- `exchange/internal/factoryclient` 把 public contract 转换到所选 SDK，但不暴露
  native payload。
- Adapter 与 runtime 文档可以解释仓库中的其他层，但不能作为本模块行为的证据。

## 构造与 lifecycle

每个 venue/product constructor 返回 sealed `factory.Config[C]` ticket。
`factory.New` 校验该 ticket，并返回 `exchange.SpotClient` 或
`exchange.PerpClient`。Generic result 会在 compile time 保留 product-specific
method set；调用方无需执行 runtime feature discovery。

构造过程在本地完成。它校验 credential、显式 environment 与 endpoint option，
随后连接 SDK client，但不执行 network I/O。第一次 REST method 或 WebSocket
watch/command 才执行第一次 exchange I/O。`Close` 释放 client-owned WebSocket
resource，并且在尚未建立连接时调用也是安全的。

Environment 是可选的。`factory.EnvironmentLive` 选择 production endpoint，它是
唯一的 environment，也是默认值，因此 constructor 会拒绝任何其他取值。demo 与
testnet profile 已被删除——各交易所的实现差异过大。匿名产品访问默认为只读，读取
交易所的实时公共数据。

## Credential-free public read

当前衍生品事实与已结算资金费历史都不需要 credential。
`factory.NewPublicDerivativeFeed` 为一个原生 market family 和一个或多个 route
projection 构造 `exchange.DerivativeFeed`。因此，同一份交易所全市场 snapshot 或
stream 可以同时输出 USDT/USDC route 的 funding、mark、index 与 OI patch，而不必
重复建立原生 feed。
如果交易所原生 ticker 同时携带多个衍生品字段，adapter 还可以实现可选的
`exchange.DerivativeReferenceWebSocket`；Gateway 将它作为一条富字段热点流使用，同时
保持普通 `PerpWebSocket` 调用方的源码兼容。

`factory.NewPublicFundingHistoryConnector` 继续作为按需读取路径，在
`exchange.FundingHistoryRequest` 给定的范围内读取单个 instrument 的已结算资金费。
两条路径都只使用公共 catalog identity——venue、product、environment 与
settlement——从不接收 API key、secret 或 account。

snapshot 与 patch 在进入标准化事实仓库前会校验 identity，adapter 不能悄悄发布
另一个 venue 或 route。完全不发布历史的 venue 返回
`exchange.ErrUnsupported`，让「没有」成为明确答案而不是空 series。

## Typed product surface

Public interface 直接编码 Spot/Perp 交集：

| Facet | Spot | Perp | Role |
| --- | --- | --- | --- |
| `exchange.MarketREST` | embedded | embedded | Instrument、book、candle 与 public trade |
| `exchange.OrderREST` | embedded | embedded | Place、cancel、open order、history 与 fill |
| `exchange.SpotAccountREST` | embedded | absent | Balance 与 Spot account |
| `exchange.PerpAccountREST` | absent | embedded | Balance、Perp account 与 position |
| `exchange.PerpREST` | absent | embedded | Funding 与 leverage |
| `exchange.SpotWebSocket` | returned | embedded by Perp | Public/private Spot stream 与 order command |
| `exchange.PerpWebSocket` | absent | returned | Spot stream 加 position 与 Perp reference stream |

因此 `exchange.SpotClient` 无法调用 position、funding 或 leverage method。
`exchange.PerpClient` 增加这些 method，但不增加 capability registry 或
`has`/`features` negotiation layer。

## Normalized data 与 instrument

Spot 与 perpetual product 具有不同的 compile-time method set。Public boundary
使用 normalized exchange instrument identifier；native market ID、token index、
contract count 与 scaled integer 保留在 SDK translation 内部。

每个 network method 都接受 `context.Context`。Price、quantity、balance、fee、
funding rate 和其他精确数值使用 `decimal.Decimal` 或显式 optional-decimal
wrapper。Model 不要求调用方解析 native JSON，也不依赖 venue SDK struct。

History 与 market-data page 只保留 venue 能够证明的 pagination fact。缺少
cursor 或未知的 `HasMore` value，并不能证明 account history 已完整返回。

## Order lifecycle

REST 与 WebSocket order command 共用 `exchange.PlaceOrderRequest`：

- market order 有 quantity，但没有 limit price 或 limit policy；
- limit order 要求 positive price，以及 resting、IOC 或 post-only 之一；
- `ReduceOnly` 只对 Perp 有效；
- `ClientOrderID` 是必填的 portable positive decimal `uint48` string；以及
- cancel 使用所选 row 返回的 opaque `OrderID`。

Command 返回 `exchange.OrderAcknowledgement`，而不是推测的 final order。
Accepted-pending、resting、partial-fill、immediate-fill、canceled、rejected 与
ambiguous state 只保留 venue response 实际证明的内容。Ambiguous send 必须先
使用原始 correlation identifier 执行 reconciliation，之后才能尝试 replacement
write。

## WebSocket lifecycle

`WebSocket()` 返回 lazy typed facet。每个 watch 返回
`exchange.Subscription[T]`，包含 opaque ID，以及 `Events`、`Status`、`Errors`
与 idempotent `Close`。Startup failure 直接从 watch call 返回；asynchronous
failure 通过 `Errors` 发送。Status event 区分 connecting、active、gap、
resyncing 与 closed。

大部分 interface member 都由所有 admitted row 实现。文档明确记录的 venue
exception 会从 method 本身返回 `exchange.ErrUnsupported`，而不是从 public
intersection 中删除该 method。

## Account 与 mode dimension

不要把 account system、logical account identity、position mode、margin mode
与 leverage 合并成一个“account mode”。它们是独立 dimension。集中的
[venue 与 account-mode reference](../reference/exchange-venues-and-account-modes.md)
记录 exchange-specific 差异。

| Dimension | Question it answers | Exchange behavior |
| --- | --- | --- |
| Account system | 查询哪个 venue balance/risk system？ | 使用所选 row 记录的 account API；不会迁移或切换 account。 |
| Logical account identity | 查询哪个 owner、subaccount 或 indexed account？ | 来自 typed config，以及支持时的显式 identity option。 |
| Position mode | Perp account 是 net/one-way 还是 hedge/multi-side？ | 只保留所选 row 记录的 scope；没有公共 mode setter。 |
| Margin mode | Risk 是 cross、isolated、unified 还是 venue-managed？ | 使用 row 记录的 fixed/current mode；没有公共 margin-mode setter。 |
| Leverage | 能否修改 instrument leverage？ | Perp 暴露 `SetLeverage`；`Effective=0` 表示调用成功，但 venue 没有 instrument leverage setting。 |

## Error 与 unsupported behavior

所有 normalized failure 使用带 safe detail 的 `exchange.Error` kind。可能发送
write 后出现 transport failure 时，可以返回 `exchange.ErrAmbiguousOutcome`；
调用方不得把它转换为 blind retry。Method-level platform gap 使用
`exchange.ErrUnsupported`，并记录在集中的 venue table 中。Credential、
signature、native payload 与 raw request body 不属于 public error contract。

## 精确 detail owner

- [REST operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
  维护精确的 REST inventory。
- [WebSocket operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
  维护精确的 WebSocket inventory。
- [Venue 与 account-mode 差异](../reference/exchange-venues-and-account-modes.md)
  维护 exchange-specific identity 与 mode caveat。
- [配置](../reference/configuration.md)维护 credential-name rule 与执行闸门。
- [测试与证据](../reference/testing.md)维护 certification semantics。

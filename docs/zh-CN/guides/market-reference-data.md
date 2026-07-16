# 市场与参考数据

[English](../../guides/market-reference-data.md) · 本页是英文规范页的中文镜像。

## 归属与范围

本页负责说明规范化市场数据、衍生品参考数据和当前 open interest 面向任务的用法。
准确的交易所/产品可用性见[能力矩阵](../adapter-capabilities.md)和各个
[交易所页面](../venues/README.md)。

## 市场数据接口

`contract.MarketDataClient` 提供 instrument discovery，以及 book、quote、trade
和 bar 的 request/stream interface。这些维度相互独立：某个交易所可能实现
snapshot 而不实现 stream，也可能声明粗粒度 stream capability，但没有接通所有
book/quote/trade subscription。应按 operation 处理 `contract.ErrNotSupported`，
而不是根据单一的宽泛 flag 推断支持情况。

通过 market client 的 `InstrumentProvider` 解析准确的 `model.InstrumentID`，
然后选择所需的最窄 operation：

- `OrderBook` 和 `Bars` 是同步的 REST-style request；
- `SubscribeBook`、`SubscribeQuotes` 和 `SubscribeTrades` 会在 `Events()` 上发布
  typed value；
- capability 描述已声明的 interface，但具体调用仍具有权威性，并可能返回
  `contract.ErrNotSupported`。

规范化 market envelope 会进入 runtime cache 和策略回调。runtime 会应用自己的
receive/apply timestamp；这些 timestamp 并不表示 adapter 提供了交易所 latency
timestamp。

## 衍生品参考数据

Perpetual 产品可能实现 `contract.DerivativeReferenceDataClient`：

- `ReferenceSnapshot` 查询当前规范化 funding/reference snapshot；
- `SubscribeReference` 在常规 market event channel 上交付 `ReferenceDataEvent`
  value；
- runtime 会缓存 derivative reference snapshot，并可选择对实现相应 handler 的
  策略调用 `OnDerivativeReference`。

reference streaming 与 order-book、quote 和 trade streaming 不同。
Lighter 当前为 Perp 接通了 derivative-reference streaming，而其 book/quote/trade
subscription method 仍未支持（unsupported）。

derivative reference snapshot 可以不完整。读取 funding、mark、index、oracle、
premium 或 funding-time value 前，请检查 `snapshot.Fields`；decimal 为零并不能
证明某字段不存在。runtime 合并 partial update 后，`FieldTimes` 会携带各字段的
freshness。使用 `Cache.DerivativeReference(instrumentID)` 获取最新的 merged snapshot。

## 当前 Open Interest

`contract.OpenInterestClient` 是可选的直接查询接口。调用 `node.OpenInterest` 或
`strategy.Context.OpenInterest`，并处理 `contract.ErrNotSupported`。当前 OI 被有意
设计为仅查询，且不会存入 `runtime.Cache`。

使用 quantity、notional 或 unit 前，请检查 `OpenInterestSnapshot.Fields`；不同
交易所填充的组合并不一致。`Timestamp` 是交易所/reference time，`ReceivedAt` 是
local receipt time。derivative-reference cache entry 新鲜，并不代表单独查询的 OI
snapshot 同样新鲜。

下面的代码摘自当前的 read-only acceptance helper
[`adapter/internal/runtimeaccept/reference_data.go`](../../../adapter/internal/runtimeaccept/reference_data.go)，
并非独立程序。它展示了直接查询、presence check，以及特意不提供 OI cache
accessor 的设计。

```go
oi, err := node.OpenInterest(ctx, id)
if err != nil {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest: %w", label, err)
}
if oi.InstrumentID != id {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest instrument=%s, want %s", label, oi.InstrumentID, id)
}
if !oi.Fields.Has(model.OpenInterestHasQuantity) {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest missing quantity field: %+v", label, oi)
}
if oi.Timestamp.IsZero() || oi.ReceivedAt.IsZero() {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest missing timestamps: %+v", label, oi)
}
if _, ok := any(node.Cache).(interface {
	OpenInterest(model.InstrumentID) (model.OpenInterestSnapshot, bool)
}); ok {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: runtime cache unexpectedly exposes open-interest storage", label)
}
```

## 选择路径

先使用静态矩阵作为跨交易所索引，再阅读交易所页面了解动态配置和 caveat。
仅通过[测试参考](../reference/testing.md)中指定的 read-only 或 Demo/Testnet target
验证真实 endpoint。不要把 reference-data read 变成 order/account probe；仓库的
read-only acceptance helper 特意不执行 submit、cancel、modify 或 order-report
operation。

- [能力矩阵](../adapter-capabilities.md)
- [配置参考](../reference/configuration.md)
- [未支持（Unsupported）与延期（Deferred）功能](../reference/unsupported.md)

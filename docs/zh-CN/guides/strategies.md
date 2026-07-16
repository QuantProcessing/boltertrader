# 编写策略

[English](../../guides/strategies.md) · 本页是英文规范页的中文镜像。

## 归属与范围

本页负责说明 `runtime/strategy` 面向任务的用法，包括可移植的策略接口、
回调顺序，以及如何从策略中提交订单。节点装配见[运行 Runtime 节点](runtime-node.md)；
交易所特定的订单支持情况见[能力矩阵](../adapter-capabilities.md)。

## 策略边界

策略接收 `strategy.Context`；它们不持有 adapter 或 SDK client。
context 暴露 runtime clock、cache、portfolio，并通过 `Context.Orders` 仅提供
两个订单操作——`Submit` 和按 client ID 执行的 `Cancel`——以及一个可选的
当前 open interest 查询。它不授予 `Modify`、`CancelAll`、execution report、
account control 或直接访问 adapter 的能力。将交易所对象隔离在策略代码之外，
正是策略保持可移植性的基础。

`Context.Buy` 和 `Context.Sell` 是基于 `Submit` 的便捷方法：价格为零时创建
market request，价格非零时创建 GTC limit request。它们不会增加新的执行权限。

嵌入 `strategy.Base`，并只覆写所需的回调：

- 使用 `OnStart` 和 `OnStop` 处理生命周期工作；
- 使用 `OnBar`、`OnQuote` 和 `OnTrade` 处理规范化市场数据；
- `OnFill` 在 fill 已应用到 cache 和 portfolio 后调用；
- 通过可选的 `strategy.DerivativeReferenceHandler` interface 接收
  `OnDerivativeReference`。

回调会与 live 和 recovered runtime event 串行执行。启动时恢复的 fill 会在
`OnStart` 之前应用，并在其后立即通过 `OnFill` 交付，因此策略应先在 `OnStart`
中初始化状态，再响应这些回调。

## 从回调中提交和取消

下面的代码摘自当前的
[`runtime/runtimetest/exec_tester.go`](../../../runtime/runtimetest/exec_tester.go)，
并非独立程序。它展示了策略获得的两项订单权限：提交规范化 request、保留其
runtime client ID，再使用同一个 client ID 取消订单。

```go
resting, err := c.Orders.Submit(c.Ctx, model.OrderRequest{
	InstrumentID: s.instID,
	ClientID:     restingClientID,
	Side:         enums.SideBuy,
	Type:         enums.TypeLimit,
	TIF:          enums.TifGTX,
	Quantity:     s.qty,
	Price:        s.restingPrice,
	PositionSide: s.posSide,
})
if err != nil {
	s.fail(fmt.Errorf("runtime submit resting order: %w", err))
	return
}
s.recordRestingOrder(restingClientID, resting.VenueOrderID)
if err := c.Orders.Cancel(c.Ctx, restingClientID); err != nil {
	s.fail(fmt.Errorf("runtime cancel resting order: %w", err))
	return
}
```

当外围工作流需要关联 ambiguous result 或 deferred cleanup（延迟清理）时，请设置稳定的
`ClientID`。若其为空，execution engine 会分配一个，返回的 order 会携带规范的
request identity。绝不要用新的 client ID 替代一次结果不明确的提交；应按照
[运维与恢复](operations-recovery.md)所述，解析原始 identity。

## `Submit` 的行为

需要显式设置 TIF、reduce-only、client ID 或 position side 字段时，请使用
`Context.Orders.Submit`。调用交易所之前，runtime 会执行 lifecycle gate 和声明的
operation support 检查，要求 adapter 完成无副作用的本地校验，运行可选且已配置的
venue-neutral risk，并持久记录 intent。随后 adapter 执行普通、同步且有确认结果的
`Submit`。

策略 API 中不存在交易所特定的 prepared-order、lease 或 capacity-admission
protocol。如果订单可能已经交接给交易所，返回的错误仍可能存在歧义。完整契约见
[执行与风险](../concepts/execution-risk.md)；依赖任何可选订单字段之前，请查阅
[未支持（Unsupported）与延期（Deferred）功能](../reference/unsupported.md)。

## 安全读取状态

使用 `Context.Cache` 读取规范化的当前状态，使用 `Context.Portfolio` 读取
account-aware exposure 和 valuation。`Context.OpenInterest` 是可选的直接查询；
它调用 runtime 注入的 `contract.OpenInterestClient`，且当前 OI 不会写入 runtime
cache。必须处理 `contract.ErrNotSupported`，因为不同交易所/产品的数据与订单接口
并不相同。presence flag 和 freshness 规则见[市场与参考数据](market-reference-data.md)。

## 后续步骤

- [运行并对账节点](runtime-node.md)
- [使用市场与参考数据](market-reference-data.md)
- [从不明确的结果中恢复](operations-recovery.md)
- [查看交易所特定行为](../venues/README.md)

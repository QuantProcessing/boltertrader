# 前置条件

> 规范语言：英文。[英文规范页](../../getting-started/prerequisites.md)

本页归属离线、Demo 和 Testnet 入门路径共用的工具、访问权限与安全前置条件。
Venue-specific 凭证由对应演练和[配置参考](../reference/configuration.md)说明。

## 准备仓库

使用 `go.mod` 声明的 Go 1.26，并同时准备 Git 和 Make。从仓库根目录运行命令。
在配置任何交易所 account 之前，先确认工具链和 venue-neutral baseline：

```sh
go version
make test-core
```

`make test-core` 会覆盖 core、runtime 和 strategy package，且不会启用任何外部
交易所读取或写入。

## 选择满足需要的最小路径

1. 从[离线 runtime 路径](./offline-runtime.md)开始。它不需要凭证，是学习 runtime
   状态和执行语义的最快方式。
2. 使用 [Binance Spot Demo](./cex-demo.md) 了解规范的 CEX lifecycle。
3. 使用 [Hyperliquid Perp Testnet](./dex-testnet.md) 了解规范的 DEX Perp
   lifecycle。

外部路径是仓库内有界的 acceptance harness。它们不是示例交易应用，也不证明
production readiness。

## 准备非生产 account

只能使用为文档所述 Demo 或 Testnet 环境签发的凭证。建议使用专用 account，
刻意限制资金，并确保所选 test 的 product scope 中没有无关订单或持仓。
演练会说明精确的 clean-state 和 funding 前置条件。

测试环境可以继承 shell 中的 variable，也可以加载仓库忽略的 `.env` 文件。
绝不要提交该文件、打印 credential value、把 secret 放入命令参数，或替换为
production credential 或 endpoint。

## 保持写入有界且串行

运行文档指定的精确 Make target。recipe 只为该 process 启用对应的写入 gate，
并应用 test 的 timeout 和 no-skip policy；通常不应自行设置 write-gate variable。
不要并行运行外部写入 target。

非零、超时、跳过或结果不明确的运行会使最终交易所状态无法确认。再次写入前，
按照 product-specific identity 和 state check 处理，绝不要取消或平掉无关的
account state。

## 后续阅读

验证层级见[测试与证据](../reference/testing.md)，结果不明确时的处理见
[操作与恢复](../guides/operations-recovery.md)。

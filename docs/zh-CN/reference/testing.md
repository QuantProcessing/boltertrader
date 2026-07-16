# 测试与证据

[English](../../reference/testing.md)

> 本页是中文镜像；英文版是规范文本。**归属：**本页拥有仓库测试阶梯、完整的
> 九 venue command inventory，以及非生产证据的解释规则。Venue 页面拥有
> prerequisite 与 product caveat。

## Offline-first 测试阶梯

默认路径不需要凭据，也不会选择加入外部 read 或 write。

| 目的 | 命令 |
| --- | --- |
| 默认 short suite | `make test` |
| Core、runtime 与 strategy | `make test-core` |
| Adapter package | `make test-adapter` |
| SDK package | `make test-sdk` |
| Capability contract | `make test-capabilities` |
| 完整 offline gate | `make test-p6-offline` |
| Runtime race check | `make test-race` |
| Offline reference-data check | `make test-reference-data-offline` |

开发时先运行最小的相关 package 或 target，再运行适合该变更的更广 offline
gate。`go test ./...` 不是默认安全 gate；未启用 short mode 时，live-backed
test 可能检查本地环境。

## 九 venue 外部命令 inventory

表中每条命令都指向具名 Demo/Testnet 环境。Reference-data 列只读。Venue
aggregate 与 runtime target 可以在真实的非生产交易所环境中创建、取消或以
其他方式改变状态。

| 环境 | Venue aggregate | Reference-data read | Runtime write target |
| --- | --- | --- | --- |
| Aster V3 Testnet | `make test-aster-testnet-acceptance` | `make test-aster-testnet-reference-data-read` | `make test-aster-testnet-runtime-spot`; `make test-aster-testnet-runtime-perp` |
| Nado Testnet | `make test-nado-testnet-acceptance` | `make test-nado-testnet-reference-data-read` | `make test-nado-testnet-runtime-spot`; `make test-nado-testnet-runtime-perp` |
| Binance Demo | `make test-binance-demo-acceptance` | `make test-binance-demo-reference-data-read` | `make test-binance-demo-runtime-spot`; `make test-binance-demo-runtime-perp` |
| OKX Demo | `make test-okx-demo-acceptance` | `make test-okx-demo-reference-data-read` | `make test-okx-demo-runtime-spot`; `make test-okx-demo-runtime-perp` |
| Bybit Demo Trading | `make test-bybit-acceptance` | `make test-bybit-demo-reference-data-read` | `make test-bybit-demo-runtime-spot`; `make test-bybit-demo-runtime-usdt-perp`; `make test-bybit-demo-runtime-usdc-perp` |
| Bitget Demo/PAP | `make test-bitget-acceptance` | `make test-bitget-demo-reference-data-read` | `make test-bitget-demo-runtime-spot`; `make test-bitget-demo-runtime-usdt-perp`; `make test-bitget-demo-runtime-usdc-perp` |
| Gate Testnet | `make test-gate-testnet-acceptance` | `make test-gate-testnet-reference-data-read` | `make test-gate-testnet-runtime-spot`; `make test-gate-testnet-runtime-usdt-perp` |
| Hyperliquid Testnet | `make test-hyperliquid-testnet-acceptance` | `make test-hyperliquid-testnet-reference-data-read` | `make test-hyperliquid-testnet-runtime-spot`; `make test-hyperliquid-testnet-runtime-perp`; `make test-hyperliquid-testnet-runtime-hip3` |
| Lighter Testnet | `make test-lighter-testnet-acceptance` | `make test-lighter-testnet-reference-data-read` | `make test-lighter-testnet-runtime-spot`; `make test-lighter-testnet-runtime-perp` |

`make test-reference-data-read` 是唯一的 all-nine aggregate：它运行上表九个
reference-data target。不存在 all-nine write-acceptance aggregate。

## Aggregate 非对称性

不要根据 aggregate 名称推断其成员：

- `make test-demo-acceptance` 只覆盖 Binance、OKX、Bybit 与 Bitget。
- `make test-aster-nado-testnet-acceptance` 配对 Aster 与 Nado；
  `make test-bybit-bitget-acceptance` 配对 Bybit 与 Bitget。
- 当前只有 Aster 与 Nado venue aggregate 包含独立的 reference-data target。
  其他 venue aggregate 可能包含不同 read check，但不包含表中的精确
  reference-data target。
- `test-bitget-testnet-*` target 名称是 canonical Bitget Demo/PAP target 的
  compatibility alias，不代表第二个环境。
- `make test-live-read` 是宽泛的 opt-in SDK/adapter smoke path。因为它不提供
  per-target zero-skip contract，所以不能单独作为 certification evidence。

请运行与预期声明的 product 和 evidence scope 完全一致的 target。

## Gates、skip 与串行执行

- Live read 需要 `BOLTER_ENABLE_LIVE_READ_TESTS=1`。Canonical Make target
  以 command-local 方式设置它。
- Live write 需要 venue-specific command-local gate。运行 Make target；不要把
  write gate 设为持久配置。精确名称见[配置](configuration.md)。
- Canonical external target 使用 `internal/testenv/cmd/noskipgotest`：skipped
  test 不算 pass。缺少凭据、product 不可用或 selector mismatch 都会阻止
  certification claim。
- 顶层 Makefile 声明 `.NOTPARALLEL`，因此同一次 invocation 中的 prerequisite
  会串行执行。不同 Make process 仍可能重叠。Live-write 命令必须串行运行，
  并在开始下一条命令前等待 terminal verification。

## 有界状态与 terminal proof

公开的 onboarding journey 是 repository acceptance harness，不是可复用的
strategy application：

- [Binance Spot Demo](../getting-started/cex-demo.md)运行
  `make test-binance-demo-runtime-spot`。成功范围仅包括 selected symbol 的
  validation-owned order，以及低于一个 size step 的权威 base-balance delta。
- [Hyperliquid standard Perp Testnet](../getting-started/dex-testnet.md)运行
  `make test-hyperliquid-testnet-runtime-perp`。成功范围仅包括 selected
  standard-Perp scope 内已加载的 open order，以及 venue-account 与 runtime
  position。

同一原则适用于每个 live target：zero exit 仅证明 harness 编码的
symbol/product/account scope 与 terminal assertion。它不证明 account-wide
empty、mainnet behavior 或永久 production readiness。

Nonzero result、timeout、skip 或 ambiguous submission 不代表 cleanup 已完成。
补救前检查 validation-owned ID 与精确 product scope。不要盲目 resubmit
ambiguous order、取消无关订单或 flatten 未确认账户。参见
[运维与恢复](../guides/operations-recovery.md)。

## Certification record

请使用[状态语义](glossary.md)。公开的 **Demo/Testnet-certified** 摘要必须写明：

- candidate identifier 与 validation date；
- environment 与精确 product scope；
- 精确 Make target 与 zero-skip result；
- harness 的 terminal state assertion；以及
- 缩小证据范围的已知 limitation。

只发布简洁、脱敏的摘要，不发布 credential、raw log、account residual 或
local path。Implementation、capability declaration 与 external evidence 始终是
不同的声明。

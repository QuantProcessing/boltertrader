# 贡献 Adapter

[English](../../contributing/adapters.md)

> 本页是中文镜像；英文版是规范文本。**归属：**本页拥有 runtime/adapter
> dependency boundary、normalized implementation obligation、capability-matrix
> update 与 adapter verification workflow。Venue 页面拥有当前 product behavior。

## 保持 dependency boundary

Production runtime code 与 venue 无关。它只能依赖 `core/enums`、`core/model`、
`core/contract` 与 `core/clock`；不得 import adapter 或 SDK，也不得按 venue name
分支。

实现路径是有意设计的单向流：

1. `sdk/<venue>/` 处理 venue authentication、request/response 与 raw stream
   format。
2. `adapter/<venue>/` 解析 instrument，并把 venue behavior 转换为
   `core/contract` client、model、error 与 typed event envelope。
3. `runtime/` 只消费这些 normalized contract。
4. Strategy 使用 portable `Context` method，绝不保留 adapter 或 SDK reference。

Runtime-aware test helper 可以测试 adapter，但这不允许 production
adapter/runtime 形成 dependency cycle。参见[架构](../concepts/architecture.md)。

## 如实实现 normalized contract

Adapter product slice 必须提供连贯的 instrument registry，并且只提供真正实现的
client surface：

- `MarketDataClient` 拥有自己的 `InstrumentProvider`、REST book/bar、具体的
  book/quote/trade subscription、market envelope 与 closure。
- 可选的 `DerivativeReferenceDataClient`、`OpenInterestClient` 与
  `FundingHistoryClient` implementation 必须保持细粒度。Current OI 是 direct
  query，绝不是 runtime cache 或 stream claim。
- `ExecutionClient` 拥有 side-effect-free `ValidateSubmit`、synchronous
  acknowledged `Submit`、product-valid cancel/modify behavior、typed report、
  execution envelope 与 closure。
- `AccountClient` 拥有强制性的权威 `AccountState`、balance、position、
  product-valid leverage/margin mutation、account envelope 与 closure。
  Account snapshot 不是 execution report。
- 当 execution client 与 account client 实现 `AccountIDProvider` 时，runtime
  startup 前两个 ID 必须解析到同一 logical scope。

缺失的 normalized method 返回 `contract.ErrNotSupported`。不要把 empty result、
configured stream bit 或底层 SDK endpoint 扩大成更宽的声明。

`ValidateSubmit` 必须保持本地且 side-effect-free。Adapter/SDK code 拥有 venue
conversion、signing、response cardinality、rejection mapping 与 ambiguous
transport correlation；runtime 拥有[执行与风险](../concepts/execution-risk.md)中
描述的 canonical portable submission sequence。不要把 venue-specific
prepared-order、lease 或 capacity-admission protocol 引入 runtime。

该流程为 `ValidateSubmit` → 可选的 configured venue-neutral risk/reservation →
durable intent → 普通 `Submit`。

## Conversion 与 event 要求

- Normalized price、quantity、balance 与 notional 使用 `shopspring/decimal`。
  `float64` 仅限 SDK/JSON boundary，并且必须显式转换。
- 发布数据或接受 normalized order 前，把 venue symbol 解析为稳定的
  `model.InstrumentID`。在 adapter boundary 验证 product、precision、tick/step、
  minimum、side、order type、time in force 与 venue-specific combination。
- 当 source 提供字段时，在 `EventMeta` 中保留 client ID、venue order ID、
  trade ID、account ID、instrument ID、correlation、source、sequence、venue
  time 与 flag。只有持续填充 `TsAdapterRecv` 与 `TsAdapterEmit` 时，才可声明
  adapter receive/emit latency。
- SDK 拥有 reconnect generation 时，用 normalized gap event 明确表达
  private-stream gap 与 recovery。Reconciliation 不得静默制造 terminal order
  cause 或 fill。
- 在 error、log 与 formatted configuration 中对 credential 和 URL user
  information 脱敏。按 contract 释放 transport 并关闭 event channel，避免
  send-on-closed-channel race。

## Capability 与 report 义务

`Capabilities()` 是精确声明，不是 feature-family shortcut：

- 区分 static product inventory 与 configured dynamic stream availability；
- 声明具体 product kind 与 trading/account/market presence；
- 即使 coarse Market bit 为 true，也要区分 book、quote、trade、
  derivative-reference、funding-history 与 OI behavior；
- 区分 order、fill、position 与 account-state report domain；
- mass status 只列出 execution adapter 直接拥有的 report domain；并且
- 当 open order 不能确定 terminal reason 或缺失 fill 时，保留 open-only
  caveat。

新增或改变公开 runtime product row 前：

1. 更新 concrete behavior 与 `Capabilities()` declaration。若 venue 还暴露
   adapter-local `CapabilityRows()`（当前为 Bybit、Bitget 与 Gate），在同一
   变更中更新并测试这些 row。
2. 更新 `adapter/capabilities.go` 中对应的 central `CapabilityRow`。保持
   venue/product key 唯一，并根据证据填充每个 stream、account-state、
   Submit/Cancel/Modify、report、mass-status、single-order、open-only、latency
   与 acceptance-target 字段。
3. 添加一个确实存在的 Make target；matrix test 会拒绝缺失 target name。
4. 更新 canonical [能力矩阵](../adapter-capabilities.md)及其中文镜像，不改变
   table schema，也不弱化 caveat。
5. 运行 `make test-capabilities` 与相关 adapter package test。

SDK presence、dynamic configuration 与 venue external API catalog 都不能作为
静态 row 的依据。使用[状态语义](../reference/glossary.md)，并将
absent/deferred fact 保留在
[Unsupported、Deferred 与 SDK-only Surface](../reference/unsupported.md)。

## 必需验证

对每个新增或变更的 product slice，覆盖所有适用层：

1. SDK authentication/request serialization、response conversion、raw stream
   conversion、rejection behavior 与 redaction。
2. Instrument discovery 与 venue/normalized symbol resolution。
3. 每个 Implemented contract method，以及显式 `ErrNotSupported` path。
4. Capability declaration 与 concrete method/configured stream behavior 的一致性。
5. 权威 `AccountState`、identity agreement、readiness 与 product-scoped
   reconciliation。
6. 在有 stream 时，验证 execution/account event metadata、ordering、
   reconnect/gap recovery 与 close behavior。
7. 使用 `runtime/runtimetest` 或共享 `adapter/internal/runtimeaccept` helper
   验证 offline runtime acceptance。
8. 当官方 non-production path 存在时，添加显式、有界的 Demo/Testnet adapter
   与 runtime target。

Aster/Nado conversion fixture 由
`internal/fixtureaudit/testdata/aster_nado_manifest.json` 管理。每条 manifest
entry 都声明 owning SDK、product、conversion kind、source、sanitization 与
negative-path status。Owning package 的 `testdata` 目录中的 payload 必须是其
declared source 经净化得到的 synthetic derivative，绝不能是捕获的 account
data。大多数 declared source 是官方示例；显式声明为 `probe` 的 entry（例如
Aster OI）来自已记录的 probe evidence。运行
`go test ./internal/fixtureaudit -count=1`；绝不能加入 credential、signed
preimage 或 production order/account identifier。

默认测试必须保持 offline 且 deterministic。Live-backed test 使用
`internal/testenv`、精确 Make target、zero-skip enforcement、bounded notional
与 validation-owned cleanup。Live target 必须串行运行。参见
[测试与证据](../reference/testing.md)、[配置](../reference/configuration.md)与
[运维与恢复](../guides/operations-recovery.md)。

## 文档交接

完成 adapter 变更时，要更新 venue 页面、capability matrix、必要时的
unsupported inventory、新变量的 configuration inventory，以及新 canonical
target 的 testing inventory。分别记录 implementation/capability 与 dated
external evidence；Demo/Testnet result 永远不代表 production readiness。

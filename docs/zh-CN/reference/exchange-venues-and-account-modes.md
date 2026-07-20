# Exchange Venue 与 Account-Mode 差异

[English](../../reference/exchange-venues-and-account-modes.md) · 本页镜像英文规范页。

## 阅读本页

本页集中维护 exchange-specific account、identity、position-mode、margin-mode、
leverage 与 order caveat。它总结差异，但不复制精确的 REST 或 WebSocket
operation matrix。

## 共享不变量

所有行都使用 typed factory config、显式 environment、normalized decimal
model 和 product-specific public interface。Exchange 模块不会隐式切换 venue
account system、position mode 或 margin mode。Public API 中没有
`SetAccountMode`、`SetPositionMode` 或 `SetMarginMode`。Perp 只暴露
`SetLeverage`；每行分别说明该操作是保留现有 margin scope、使用固定 scope，
还是把 leverage 交给 venue backend。

## Venue matrix

| Venue | Rows | Account system | Logical account identity | Position mode | Margin mode | Leverage | Order caveats |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Binance | `BNS`, `BNP` | 独立的 Spot 与 USD-M API surface | API key 与 secret | Perp 使用 one-way/net `BOTH`；拒绝 hedge-side response | 现有 cross/isolated account state；无 mode selector | Perp `SetLeverage` 确认 venue leverage | Portable contract 不包含 Perp close-position shape |
| OKX | `OXS`, `OXP` | Spot 与 SWAP 共用一个 trading account | API key、secret 与 passphrase | Order 使用 `net`；leverage 接受一个 current position scope | Order 使用 `cross`；leverage 保留已发现的 cross/isolated scope | Perp `SetLeverage` 校验返回 scope | 多个同时存在的 position scope 不可移植 |
| Lighter | `LIS`, `LIP` | 一个 indexed account 暴露 asset 与 position | Private key 加 `accountIndex` 与 `keyIndex` | Normalized model 中每个 market 一个 signed position | Leverage 保留当前 account/position cross 或 isolated mode | Perp `SetLeverage` 复用已发现的 margin mode | Native market 与 account index 保留在内部 |
| Hyperliquid | `HLS`, `HLP` | 一个 owner account 暴露 Spot 与 Perp state | Signer address，或通过 `WithAccountAddress` 选择 owner | 每个 Perp asset 一个 signed one-way position | Leverage 保留当前 `cross` 或 `isolated`；没有 position 时默认 cross | Perp `SetLeverage` 在已知时强制 instrument maximum | Agent/API-wallet signing 可以查询显式 owner |
| Bybit | `BYS`, `BYU`, `BYC` | `UNIFIED` wallet/account endpoint | API key 与 secret | Order 不传 hedge index，因此使用 `positionIdx=0` one-way scope | 现有 venue margin mode；无公共 selector | Perp `SetLeverage` 设置相同的 buy/sell leverage | Spot market buy 把 normalized base quantity 转换成 venue quote quantity |
| Bitget | `BGS`, `BGU`, `BGC` | `UTA` account/asset 与 order endpoint | API key、secret 与 passphrase | 读取 `one_way_mode` 或 `hedge_mode` 并映射 order side | Perp order 与 leverage 使用 `crossed` | Perp `SetLeverage` 使用对应 row 的 settlement coin | Hedge reduce-only 通过 position side 编码 |
| Gate | `GTS`, `GTU` | Spot 读取 `classic` 或 unified `multi_currency`/portfolio/single-currency mode；Perp 使用 futures account | API key 与 secret | Signed one-way Perp position | 现有 venue futures mode；无公共 selector | Perp `SetLeverage` 确认 response | Perp quantity 必须与 contract multiplier 对齐 |
| Aster | `ATS`, `ATP` | User account 与独立 API-wallet signer | User address、signer private key 与 optional expected signer | Perp 使用 one-way/net `BOTH` | 现有 venue mode；无公共 selector | Perp `SetLeverage` 确认 response | WebSocket-facet order command 使用 signed REST command path |
| Nado | `NDS`, `NDP` | Unified-margin subaccount | Private key 加 subaccount name | Normalized model 中的 signed one-way position | Backend unified-margin risk model；无公共 selector | `SetLeverage` 返回 `Effective=0`，因为 leverage 由 backend 管理 | Order ID 是 lowercase `0x` digest；`WatchMarkPrice` 返回 `exchange.ErrUnsupported` |

## Venue 说明

### Binance

Normalized Perp row 只支持 one-way。带 hedge-side position 的 order、history
row 或 stream event 会被视为 malformed，而不会被静默合并为一个 position。

### OKX

SWAP order placement 使用 cross/net portable shape。Leverage update 会先检查
current position；如果存在多个同时生效的 scope，则会拒绝，因为一个
instrument-level request 无法可移植地标识它们。

### Lighter

Configured indexed account 会与 account response 核对。Leverage update 复用
position 已报告的 margin mode；没有 position 时回退到 account trading mode。

### Hyperliquid

Signing identity 与 queried owner 可以不同。显式 owner option 会改变
account-scoped REST 与 private-stream read，同时保留 signer 执行 authenticated
action。

### Bybit

Account read 使用 unified wallet surface。Normalized order request 没有
hedge-side selector，因此调用方必须使用兼容的 one-way position scope。

### Bitget

Client 在 Perp order placement 前读取 account hold mode。One-way 与 hedge
account 都会被映射，而 normalized implementation 保持 crossed margin 作为
portable order/leverage scope。

### Gate

Spot balance read 会检测 classic 与 unified account mode。Perp row 使用独立的
USDT-settled futures surface，并把 normalized quantity 转换为 integer contract
count。

### Aster

Factory validation 绑定 user account 与 API-wallet signer，并且可以在本地校验
expected signer address。Perp order placement 使用 net position side。

### Nado

Subaccount 属于 logical identity。Instrument leverage 不是 venue setting；
成功 validation 会返回 zero effective leverage。Mark price 同样是文档明确的
method-level WebSocket exception，而不是被移除的 interface member。

## Product row 与精确 operation

[REST matrix](exchange-rest-v1-operation-matrix.md)与
[WebSocket matrix](exchange-ws-v1-operation-matrix.md)继续维护精确的 product row
与 method。本页不得复制完整 operation table。

## 来源与更新规则

只根据 exchange code、public surface manifest、exchange test 与
exchange-specific acceptance evidence 更新本页。如果这些来源不一致，应记录
冲突并请求用户决策，而不是从 adapter 或 runtime 文档导入声明。

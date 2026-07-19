# 配置

[English](../../reference/configuration.md)

> 本页是中文镜像；英文版是规范文本。**归属：**本页拥有完整、共享的
> Demo/Testnet environment-variable inventory、loading rule、write gate 与
> endpoint-profile safety。Venue 页面拥有每个 product 与 selector 的运维含义。

## 加载、优先级与秘密

Acceptance helper 通过 `go.mod` 定位仓库根目录，并可加载其中的 `.env`。
已存在的 process variable 始终优先。缺少 `.env` 是允许的。

Execution gate 永远不会从 `.env` 导入。Loader 拒绝从文件激活匹配
`RUN_*`、`*_REALTIME_WS`、`ENABLE_*`、包含 `_ENABLE_`、`ALLOW_*` 或包含
`_ALLOW_` 的名称。因此，`BOLTER_ENABLE_LIVE_READ_TESTS`、每个 venue write
gate，以及每个 unsafe custom endpoint opt-in 都必须显式存在于 process
environment 中。Canonical Make recipe 以 command-local 方式设置 read/write
gate。

不要把 credential value、private key、proxy credential 或 account residue
写入源码、示例、日志或 version control。Configuration error 与 string
representation 必须对 secret 和 URL user information 脱敏。

## Canonical write identity 与 gate

下表是 credentialed write acceptance 的完整 canonical identity input。
Read-only target 可能只需要其中一部分；精确 target 始终是权威来源。

| 环境 | 调用者提供的 write identity | Make recipe write gate |
| --- | --- | --- |
| Aster V3 Testnet | `ASTER_TESTNET_USER_ADDRESS`, `ASTER_TESTNET_SIGNER_PRIVATE_KEY` | `BOLTER_ENABLE_ASTER_TESTNET_WRITES` |
| Nado Testnet | `NADO_TESTNET_PRIVATE_KEY` | `BOLTER_ENABLE_NADO_TESTNET_WRITES` |
| Binance Demo | `BINANCE_DEMO_API_KEY`, `BINANCE_DEMO_API_SECRET` | `BOLTER_ENABLE_BINANCE_DEMO_WRITES` |
| OKX Demo | `OKX_DEMO_API_KEY`, `OKX_DEMO_API_SECRET`, `OKX_DEMO_API_PASSPHRASE` | `BOLTER_ENABLE_OKX_DEMO_WRITES` |
| Bybit Testnet | `BYBIT_TESTNET_API_KEY`, `BYBIT_TESTNET_API_SECRET` | `BOLTER_ENABLE_BYBIT_TESTNET_WRITES` |
| Bitget Demo/PAP | `BITGET_DEMO_API_KEY`, `BITGET_DEMO_SECRET_KEY`, `BITGET_DEMO_PASSPHRASE` | `BOLTER_ENABLE_BITGET_DEMO_WRITES` |
| Gate Testnet | `GATE_TESTNET_API_KEY`, `GATE_TESTNET_API_SECRET` | `BOLTER_ENABLE_GATE_TESTNET_WRITES` |
| Hyperliquid Testnet | `HYPERLIQUID_TESTNET_PK` | `BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES` |
| Lighter Testnet | `LIGHTER_TESTNET_PRIVATE_KEY`, `LIGHTER_TESTNET_ACCOUNT_INDEX`, `LIGHTER_TESTNET_API_KEY_INDEX` | `BOLTER_ENABLE_LIGHTER_TESTNET_WRITES` |

用户通常运行[测试与证据](testing.md)中的精确 target，而不是 export write
gate。绝不能用 production credential 替代这些 non-production identity。

## 可选 identity、symbol 与 notional input

Notional limit 是正 decimal safety bound，不是请求的 order size。变量为空时，
“auto” symbol 表示 harness 从已发现 instrument 中选择合适项目。

| 环境 | 可选 identity/selector input 与默认值 | Notional 或 quantity input 与默认值 |
| --- | --- | --- |
| Aster V3 Testnet | `ASTER_TESTNET_EXPECTED_SIGNER_ADDRESS` 用于校验 signer identity；`ASTER_TESTNET_SPOT_SYMBOL` 与 `ASTER_TESTNET_PERP_SYMBOL` 默认为自动选择 | `ASTER_TESTNET_MAX_NOTIONAL_USDT=100` |
| Nado Testnet | `NADO_TESTNET_SUBACCOUNT_NAME=default`；`NADO_TESTNET_SPOT_SYMBOL` 与 `NADO_TESTNET_PERP_SYMBOL` 默认为自动选择 | `NADO_TESTNET_MAX_NOTIONAL_USDT0=100` |
| Binance Demo | `BINANCE_DEMO_SYMBOL=ETH-USDT` | `BINANCE_DEMO_MAX_NOTIONAL_USDT=100`；`BINANCE_DEMO_ORDER_QTY=0` 表示由 harness 自动确定 quantity |
| OKX Demo | `OKX_DEMO_SPOT_SYMBOL=ETH-USDT`; `OKX_DEMO_PERP_SYMBOL=ETH-USDT-SWAP` | `OKX_DEMO_MAX_NOTIONAL_USDT=100` |
| Bybit Testnet | `BYBIT_TESTNET_SYMBOL=BTCUSDT`; `BYBIT_TESTNET_USDT_PERP_SYMBOL=BTCUSDT`; `BYBIT_TESTNET_USDC_PERP_SYMBOL=BTCPERP` | `BYBIT_TESTNET_MAX_NOTIONAL_USDT=100`; `BYBIT_TESTNET_MAX_NOTIONAL_USDC=100` |
| Bitget Demo/PAP | `BITGET_DEMO_SYMBOL=BTCUSDT`; `BITGET_DEMO_USDT_PERP_SYMBOL=BTCUSDT`; `BITGET_DEMO_USDC_PERP_SYMBOL=BTCPERP` | `BITGET_DEMO_MAX_NOTIONAL_USDT=100`; `BITGET_DEMO_MAX_NOTIONAL_USDC=100` |
| Gate Testnet | `GATE_TESTNET_SPOT_SYMBOL=ETH_USDT`; `GATE_TESTNET_USDT_PERP_SYMBOL=BTC_USDT` | `GATE_TESTNET_MAX_NOTIONAL_USDT=100` |
| Hyperliquid Testnet | `HYPERLIQUID_ACCOUNT_ADDRESS` 在 agent/API-wallet key 签名时标识 owner；`HYPERLIQUID_TESTNET_SPOT_SYMBOL` 与 `HYPERLIQUID_TESTNET_PERP_SYMBOL` 默认为自动选择；`HYPERLIQUID_TESTNET_HIP3_SYMBOL` 对 HIP-3 必须带 dex 限定 | `HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC=100` |
| Lighter Testnet | `LIGHTER_TESTNET_SPOT_SYMBOL=ETH-USDC`; `LIGHTER_TESTNET_PERP_SYMBOL=ETH` | `LIGHTER_TESTNET_MAX_NOTIONAL_USDC=100` |

Identity field 是边界，而不是 label：

- Aster 将 user address、signing private key 与 optional expected signer
  address 分开。
- Nado subaccount 是 unified-margin identity 的一部分，不是 currency-level
  balance partition。
- Hyperliquid canonical harness 支持 standard user role。虽然会解析
  `HYPERLIQUID_TESTNET_VAULT`，canonical path 仍拒绝 Vault 与 SubAccount
  user role。
- Lighter 除 private key 外，还需要 account index 与 API key index。

## Endpoint profile 与 override gate

Endpoint variable 是高级安全 input。绝不能把 Demo/Testnet write target 重定向到
production。

### 固定 profile

- Binance 选择内置 Demo REST/WS profile，不提供 acceptance endpoint override
  variable。
- Bybit external acceptance 选择内置 **Testnet** profile
  (`https://api-testnet.bybit.com`, `wss://stream-testnet.bybit.com/v5/public/*`,
  `wss://stream-testnet.bybit.com/v5/private`, 以及
  `wss://stream-testnet.bybit.com/v5/trade`)。`BYBIT_DEMO_API_KEY` 与
  `BYBIT_DEMO_API_SECRET` 代表另一个环境，明确禁止用它们替代 Testnet
  credential。
- Hyperliquid 与 Lighter 选择内置 Testnet profile，不提供 acceptance endpoint
  override variable。

### 仅允许精确官方 profile

Aster 仅在值与内置官方 Testnet endpoint 完全一致时接受以下变量：

- `ASTER_TESTNET_SPOT_REST_URL`
- `ASTER_TESTNET_SPOT_WS_URL`
- `ASTER_TESTNET_SPOT_USER_WS_URL`
- `ASTER_TESTNET_PERP_REST_URL`
- `ASTER_TESTNET_PERP_WS_URL`
- `ASTER_TESTNET_PERP_USER_WS_URL`

Nado 对以下变量应用相同的 exact-match 规则：

- `NADO_TESTNET_GATEWAY_URL`
- `NADO_TESTNET_GATEWAY_V2_URL`
- `NADO_TESTNET_ARCHIVE_URL`
- `NADO_TESTNET_ARCHIVE_V2_URL`
- `NADO_TESTNET_GATEWAY_WS_URL`
- `NADO_TESTNET_WS_URL`
- `NADO_TESTNET_TRIGGER_URL`

Gate read 接受 `GATE_TESTNET_REST_BASE_URL`、`GATE_TESTNET_SPOT_WS_URL` 与
`GATE_TESTNET_USDT_FUTURES_WS_URL`。Credentialed write 要求三者都解析到已知
官方 Testnet profile。旧名称 `GATE_TESTNET_FUTURES_USDT_WS_URL` 是 USDT
Futures WS variable 的 deprecated alias。

### 受保护的 custom profile

- `OKX_DEMO_HOST_PROFILE` 接受 `global`（默认）、`eea` 或 `custom`。
  官方 `global`/`eea` credentialed write 禁止 override。`custom` 需要同时提供
  `OKX_DEMO_REST_BASE_URL` 与 `OKX_DEMO_WS_BASE_URL`，并以 command-local
  方式设置 `BOLTER_ALLOW_OKX_DEMO_CUSTOM_WRITES=1`；credentialed write 要求
  HTTPS/WSS，并拒绝明显的网站或 production WebSocket host。
- Bitget 已知 Demo/PAP profile 为 `https://api.bitget.com`，public/private
  WebSocket 分别为 `wss://wspap.bitget.com/v3/ws/public` 与
  `wss://wspap.bitget.com/v3/ws/private`。Custom
  `BITGET_DEMO_REST_BASE_URL`、`BITGET_DEMO_PUBLIC_WS_URL` 与
  `BITGET_DEMO_PRIVATE_WS_URL` 必须一起提供。Credentialed custom write 还需要
  command-local `BOLTER_ALLOW_BITGET_DEMO_CUSTOM_WRITES=1`、HTTPS/WSS 与
  non-production WS host。

## Deprecated Bitget compatibility alias

Bitget 的 `BITGET_TESTNET_*` 名称映射到 canonical Demo/PAP variable；它们不会
选择第二个环境。新配置必须使用右侧名称。

| Deprecated alias | Canonical variable |
| --- | --- |
| `BITGET_TESTNET_API_KEY` | `BITGET_DEMO_API_KEY` |
| `BITGET_TESTNET_SECRET_KEY` | `BITGET_DEMO_SECRET_KEY` |
| `BITGET_TESTNET_PASSPHRASE` | `BITGET_DEMO_PASSPHRASE` |
| `BITGET_TESTNET_SYMBOL` | `BITGET_DEMO_SYMBOL` |
| `BITGET_TESTNET_USDT_PERP_SYMBOL` | `BITGET_DEMO_USDT_PERP_SYMBOL` |
| `BITGET_TESTNET_USDC_PERP_SYMBOL` | `BITGET_DEMO_USDC_PERP_SYMBOL` |
| `BITGET_TESTNET_MAX_NOTIONAL_USDT` | `BITGET_DEMO_MAX_NOTIONAL_USDT` |
| `BITGET_TESTNET_MAX_NOTIONAL_USDC` | `BITGET_DEMO_MAX_NOTIONAL_USDC` |
| `BITGET_TESTNET_REST_BASE_URL` | `BITGET_DEMO_REST_BASE_URL` |
| `BITGET_TESTNET_PUBLIC_WS_URL` | `BITGET_DEMO_PUBLIC_WS_URL` |
| `BITGET_TESTNET_PRIVATE_WS_URL` | `BITGET_DEMO_PRIVATE_WS_URL` |

## Proxy input

`PROXY` 接受 `http`、`https` 或 `socks5` URL，并且可能包含 credential，必须对其
脱敏。`internal/testenv` 中的 acceptance HTTP-client builder 会禁用继承的
`HTTP_PROXY`、`HTTPS_PROXY` 与 `ALL_PROXY`，除非显式设置 `PROXY`。
WebSocket routing 仍取决于 venue；不要从 HTTP-client 规则推断它。

## 相关指南

- [前置条件](../getting-started/prerequisites.md)
- [CEX Demo 演练](../getting-started/cex-demo.md)
- [DEX Testnet 演练](../getting-started/dex-testnet.md)
- [Unsupported 与 SDK-only surface](unsupported.md)

# Test-mode Gateway 真实验收记录

本记录描述 2026-08-03 在 `FT_MODE=test` 下，funding-trader 通过本地
BolterTrader Gateway 完成的真实 Demo/Testnet 验收。交易所凭证只由 Gateway
配置与 secret reference 持有，未复制到 funding-trader 后端、浏览器、证据文件或
日志。

`test` 是 funding-trader 的统一测试产品模式。Gateway-native `demo` 和
`testnet` 只作为 account、instrument、leg、fill 与 funding fact 的
`venueEnvironment` 保留；浏览器不能覆盖产品 mode。切换到 Live 使用同一套产品
代码，但必须改用独立 Gateway Live 配置、`FT_MODE=live`，并显式开启服务端生产
写入门；默认拒绝 Live 私有写入。

## 最终结论

真实 Test 验收通过。Gateway 从外部配置发现 Binance Demo 与 Bybit Demo 的 USDT
永续能力；funding-trader 先完成最小规模 R1，再完成跨实际 funding settlement 的
R2。R2 两腿均真实开仓、成交、持仓、获得可归因 funding payment、平仓并完成账本
核对。最终没有仓位、挂单、补偿任务或 Live/Mainnet 写入。

| 生命周期 | 结果 | 关键证据 |
| --- | --- | --- |
| R1 短生命周期 | PASS | open `exec_208587d3f25813e7`、close `exec_b131287dcd28b2df`；两场所最终仓位和挂单均为 0 |
| R2 跨 funding settlement | PASS | lineage/open `exec_1ee47ec88d21dfb4`、close `exec_ec38545b05464f54`；两腿均收到 2026-08-03 16:00:00Z 的实际 funding payment |
| 浏览器最终复核 | PASS | 登录、26 个实时机会、账本、已平仓仓位、执行、活动、审计、刷新持久性、退出与未认证重载均通过 |
| Live/Mainnet 私有写 | PASS（零写入） | `productionWritesEnabled=false`、`livePrivateOrderCount=0`、Live/Mainnet scoped DB rows 均为 0 |

Gateway V1 不提供标准化风险限额；该限额不再作为 Gateway readiness 前提。仓位规模
由单用户操作者负责，本次使用两腿各 `0.05 ETH` 的最小验收规模。funding-trader
仍保留认证、CSRF、mode/native environment、能力、幂等、两腿状态机与 Live
默认拒绝等执行安全门。

## R2 路由与成交

| 角色 | Gateway account | Native 环境 | Instrument | 方向 | 数量 |
| --- | --- | --- | --- | --- | ---: |
| Long | `binance-demo-usdt-perp` | Demo | `binance:demo:perp:USDT:ETH-USDT` | open buy / close sell | 0.05 ETH |
| Short | `bybit-demo-usdt-perp` | Demo | `bybit:demo:perp:USDT:ETH-USDT` | open sell / close buy | 0.05 ETH |

open 与 close 的四条 order leg 均为 terminal，`nonterminalOrderLegs=0`，close
幂等状态为 `completed`，没有 manual intervention 或 compensation。

R2 实际 funding payment：

| 账户 | 金额 | 结算资产 | Effective at | Revision |
| --- | ---: | --- | --- | ---: |
| Binance Demo | +0.04533449 | USDT | 2026-08-03 16:00:00Z | 1 |
| Bybit Demo | +0.00373546 | USDT | 2026-08-03 16:00:00Z | 1 |
| 合计 | +0.04906995 | USDT | - | - |

## 最终经济结果

所有数值来自后端不可变 ledger facts 与关闭仓位投影，前端没有重新计算或补零：

| 指标 | 最终值 |
| --- | ---: |
| Realized trading PnL | -0.014 USDT |
| Actual funding cash flow | +0.04906995 USDT |
| Fees | -0.17594256 USDT |
| Net PnL | -0.14087261 USDT |
| Current open UPnL | 0 USDT |
| Closed trading UPnL | 0 USDT |
| Verification | `verified` |
| Watermark | `complete`, verified through 2026-08-03 16:41:00.423Z |

最终平仓与归零证明：

1. Binance scoped position items = 0；
2. Bybit scoped position items = 0；
3. Binance scoped open-order items = 0；
4. Bybit scoped open-order items = 0；
5. backend open position count = 0；
6. residual leg quantity = 0，residual reporting notional = 0；
7. pending compensation = 0，nonterminal order legs = 0；
8. Live private orders = 0，Live/Mainnet scoped database rows = 0。

## 用户路径与异常覆盖

真实浏览器在 [http://127.0.0.1:13001](http://127.0.0.1:13001) 完成以下复核：

- 未认证访问只显示登录页；
- 登录后进入 TEST dashboard，并读取 26 个实时机会；
- ledger 与单个已关闭 ETH-USDT 仓位显示相同的 verified economics；
- open position 表为空，open 与 close execution 均为 terminal；
- activity 与 audit 可见平仓完成和 execution reconciliation；
- 手动刷新后 PnL、funding、fee、watermark 与 verification 不变；
- 页面无 console warning/error；
- logout 后回到登录页，重载仍保持未认证。

验收过程中覆盖了 private event resnapshot、后端重启恢复、幂等 close 重试与账本
reconciliation。最终 `ledger_recovery_completed` 记录 `eligible=1`、
`reconciled=1`。历史 monitor 中的瞬时连接/登录错误不作为成功依据；终态只使用下列
final evidence 文件。

## Docker-free 本地运行

最终验收栈完全本地运行，不依赖 Docker：

| 组件 | 地址 | 所有权 |
| --- | --- | --- |
| PostgreSQL | `127.0.0.1:15432` | funding-trader local lifecycle |
| BolterTrader Gateway | `127.0.0.1:18082` | 外部本地进程；funding-trader 只检查，不启动/停止 |
| funding-trader backend | `127.0.0.1:18091` | funding-trader local lifecycle |
| frontend | `127.0.0.1:13001` | funding-trader local lifecycle |

生命周期与安全边界见 [OPERATIONS.md](./OPERATIONS.md)。`scripts/local-dev-stack.sh`
不会接管 Gateway，不会把 backend secret 环境传给 frontend，并在 Live 写入未显式
授权时 fail closed。

## 证据索引

最终机器证据：

- [`r2-final-machine-evidence.json`](../output/playwright/r2-final-readonly-20260803/r2-final-machine-evidence.json)
- [`r2-browser-summary.json`](../output/playwright/r2-final-readonly-20260803/r2-browser-summary.json)
- [`browser-trading-acceptance-evidence.json`](../output/playwright/r2-final-readonly-20260803/browser-trading-acceptance-evidence.json)
- [`ledger-closed-final.png`](../output/playwright/r2-final-readonly-20260803/ledger-closed-final.png)

最终后端/Gateway 原始脱敏快照位于 [`output/acceptance/`](../output/acceptance/)，重点
包括：

- `r2-backend-position-final.json`
- `r2-backend-portfolio-final.json`
- `r2-backend-verification-final.json`
- `r2-backend-open-execution-final.json`
- `r2-backend-close-execution-final.json`
- `r2-binance-funding-patched.json`
- `r2-bybit-funding-patched.json`
- 两场所的 `positions-final.json` 与 `open-orders-final.json`

## 复现边界

私有读取或写入验收只通过 token file path 引用 Gateway 凭证，禁止把 token 内容写入
命令参数、日志或浏览器配置。真实写入仅允许 `FT_MODE=test`，并且必须先由 Gateway
发现两条不同场所、同 settlement asset、read/write-ready 的 perp route；
Live/Mainnet 写入门保持关闭。

日常本地生命周期：

```sh
scripts/local-dev-stack.sh --backend-env /path/to/backend.env status all
scripts/local-dev-stack.sh --backend-env /path/to/backend.env start postgres
scripts/local-dev-stack.sh --backend-env /path/to/backend.env start backend
scripts/local-dev-stack.sh --backend-env /path/to/backend.env start frontend
```

Gateway 必须由 BolterTrader 本地进程独立启动。funding-trader 脚本只验证
`127.0.0.1:18082`，不会重启或停止它。

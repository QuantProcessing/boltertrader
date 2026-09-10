# 公共行情能力边界

本批接续 SDK 错误与 HTTP 预算治理，落实架构审查中公共行情与账户交易接口混用的问题。

## 原问题

`PublicMarketConnector` 虽然使用空凭证构造，REST 方法也没有暴露订单操作，但其
`WebSocket()` 返回完整 `SpotWebSocket`：下单、撤单、订单、成交和余额订阅全部
可见。永续对象还暴露持仓与账户事件订阅。Gateway 读取公开标记价时必须断言完整
`PerpWebSocket`，使行情代码依赖账户和交易方法。

只修改返回接口仍然不够：若实际返回同一个账户 socket，调用方可以用 Go 类型
断言重新取得这些方法。依赖边界应由返回对象的方法集表达，不能只靠“没有配置
凭证”的约定。

## 已落地

| 接口 | 范围 |
| --- | --- |
| `MarketWebSocket` | 订单簿、BBO、公开成交、K 线订阅与关闭 |
| `PerpMarketWebSocket` | 上述公共能力，加标记价、资金费率订阅 |
| `DerivativeReferenceWebSocket` | 继续作为可选能力，保留 index、OI 等原生参考数据 |
| `PerpMarketREST` | 当前与历史公开资金费率，排除设置杠杆 |

`SpotWebSocket`、`PerpWebSocket`、`PerpREST` 作为完整账户客户端的组合接口保留，
原有方法集不变。没有为每一个方法创建接口，也没有增加服务、注册中心或运行策略。

工厂返回只组合公共接口的具体对象；其私有实现不暴露原始 socket getter。
现货不会获得永续能力，缺少 rich-reference 的永续实现不会被包装成“支持”该能力。
包装直接传递原订阅，不重建 channel、重连流程或缓存，因此订阅的 replay、状态、
错误和关闭仍由原适配器负责。关闭同一底层客户端仍使用原来的生命周期处理。

Gateway `publicmarket` 的 Connector、连接池、标记价消费者和测试替身统一改用
公共接口。目录、连接池共享范围和行情所有权没有改变。

## 兼容性与边界

`PublicMarketConnector.WebSocket()` 的 Go 返回类型由 `SpotWebSocket` 改为
`MarketWebSocket`，第三方声明了同名 getter 的接口或测试替身需要同步修改。
读取永续行情时改为断言 `PerpMarketWebSocket`；此前从公共连接器获取交易方法的
代码不再成立，账户交易应从正式账户客户端取得。完整 `SpotClient` / `PerpClient`
的 getter 和方法集保持兼容。HTTP/WS 线协议、数据库和前端没有变更。

这是 Go API 的职责边界，不是对同一进程内任意恶意 Go 代码的沙箱。公共连接器
仍复用空凭证的适配器实现，未复制九家交易所的 SDK 接线。现有 native 方法是否
可用仍按适配器和能力矩阵判断，公共接口的存在不保证任意交易所支持任意参数。

## 验证

- 公共接口方法集与现有完整客户端方法集均有契约检查。
- 遍历 20 个 route profile，检查工厂实际返回对象只含公共方法，保留现货/永续及
  rich-reference 区分；构造和关闭不发送 HTTP 请求。
- 本机 WebSocket 服务模拟 Bybit 原生协议，验证盘口晚订阅者重放、三个衍生品
  消费者共享一个原生 ticker、index/OI 不丢失、状态投递、单个订阅关闭及 socket
  整体关闭。不调用真实交易所，不读取凭证。
- Gateway 标记价回归使用只有公共方法的替身，防止重新依赖完整交易接口。

`GOCACHE=/tmp/bolter-audit-go-cache go test -short ./...` 退出码 0，99 个包报告通过
（包含缓存）。`go test -short -race ./exchange/factory ./gateway/publicmarket
./gateway/marketowner` 退出码 0，3 个包全部通过；`git diff --check` 通过。
日志位于 `output/architecture-review-2026-09-10/public-boundary-full.log` 和
`public-boundary-race.log`；`public-boundary-changes.diff` 仅记录本批增量，排除之前
各批重构已经存在的工作区变更。短测未执行实盘和独立数据库验收。

## 下一处架构工作

公共 Go 能力边界已经收窄，但面向独立应用的公共行情服务 API 仍待整理：现有
`/v1` 行情读取依赖账户选择，共享市场订阅主要位于内部 API。后续应将现有公共行情
源和订阅所有者接到稳定的应用 API，明确 instrument 身份、快照/增量和断流语义，
使监控应用不必创建交易账户或借用浏览器内部接口。应复用现有采集与事实仓库，
避免再造一套行情缓存。

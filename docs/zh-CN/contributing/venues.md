# 新增一个 venue

> 英文为规范版本。[English](../../contributing/venues.md)

一个 venue 不是在某一个地方被加进来的。它是沿着一条线被加进来的，而这条线有次序：只有前一步
立得住，下一步才值得走。本页拥有这个次序，以及每一步各自要证明什么。

各层**意味着**什么属于[架构](../concepts/architecture.md)。本页讲的是顺序。

## 整条路径

| 步骤 | 位置 | 加进来的东西 |
| --- | --- | --- |
| 1 | `sdk/<venue>/` | 该交易所自己的 API，用它自己的词汇 |
| 2 | `exchange` | 对 `exchange.SpotClient` 或 `exchange.PerpClient` 的符合 |
| 3 | `exchange/factory` | 每个 product 行一个带类型的构造器 |
| 4 | `credential` | 该交易所的 auth kind |
| 5 | `routeprofile` | venue、product、结算资产与原生环境 |
| 6 | `gateway/account` | 每条 route identity 的构造分支 |
| 7 | `gateway/capability` | Product 行：代码、精确查询策略、写入就绪 |
| 8 | `exchange/testdata/public_surface_manifest.json` | 两张矩阵据以生成的公开面 |
| 9 | `Makefile` | 外部验收 target |

一个停在第 1 步的 venue 是真实的、能跑的，并且不属于这个产品。十三个 SDK 家族里恰有四个是
这样，具体是哪四个由[不支持的能力](../reference/unsupported.md)拥有。

## 1–2. 先 SDK，再泛化

SDK 包说的是该交易所的方言：它的 endpoint 形状、它的签名、它的 symbol 字符串、它的错误码。
它之上的任何东西都不该需要知道这些。

Exchange 层就是方言变成两种形状之一——现货或永续——的地方。忍住为了迁就某个 venue 而把接口
撑宽的冲动。如果一个 venue 无法表达形状所要求的东西，那份缺席是记在矩阵里的推迟，而不是为
一个 venue 新加的方法。一个每来一个 venue 就长一个方法的接口，已经不再是泛化了。

## 3. 每个 product 行一个 factory 构造器

`exchange/factory` 是唯一构造 venue client 的地方。每个 product 行有一个带类型的构造器，返回
`factory.Config[exchange.SpotClient]` 或 `factory.Config[exchange.PerpClient]`，再由
`factory.New` 变成一个 client。

构造器的签名正是一个 venue 的认证方式变得可见的地方，因为它恰好收下该交易所需要的材料，不多
一样：

| 构造器形状 | 收下 |
| --- | --- |
| `BinanceSpotConfig`、`BybitSpotConfig`、`GateSpotConfig` | key 与 secret |
| `OKXSpotConfig`、`BitgetSpotConfig` | key、secret、passphrase |
| `LighterSpotConfig` | 私钥、account index、key index |
| `HyperliquidSpotConfig` | 私钥 |
| `AsterSpotConfig` | 用户地址、API wallet 私钥、期望签名者 |
| `NadoSpotConfig` | 私钥、子账户 |

一切不属于凭证材料的东西都以 option 的形式进来——`WithEnvironment`、`WithHTTPClient`、
`WithInstrumentCatalog`、`WithEndpoint`、`WithWebSocketEndpoint`、`WithAccountAddress`——于是
新增一个 venue 从不改动已有构造器的参数个数。

## 4. Credential kind

凭证注册表把每个 venue 映射到一种 auth kind。当前九个 venue 用六种：

| Auth kind | Venue |
| --- | --- |
| API key 与 secret | `binance`、`bybit`、`gate` |
| API key、secret、passphrase | `okx`、`bitget` |
| Lighter signer | `lighter` |
| EVM 私钥 | `hyperliquid` |
| Aster API wallet | `aster` |
| Nado signer | `nado` |

Bundle 的解码是严格的：schema 版本、声明的 venue、声明的 kind 三者都必须与注册表一致，否则
按无效拒绝。一个新 venue 要么复用一种已有 kind，要么新增一种——它不会先被松散地解出来、之后
再慢慢分辨。一份形状不属于该 venue 的凭证是个故障，必须在解码时浮出来，而不是在 dispatch 时。

## 5. Route profile

`routeprofile` 拥有哪些组合存在：venue、product 与结算资产。结算资产在多数现货行上为空，
在别处是 `USDT`、`USDC` 或 `USDT0`，而且它是这一行**身份的一部分**，不是它的一个细节。

route profile 里没有环境这一维。每一行过去会按该 venue 提供的环境各展开一个 profile——一个
既有 demo endpoint 又有 testnet 的 venue 会展开成三个——注册表再把它们折回一个 mode，于是
一条 route 永远不是一次「它指的是哪套凭证」的猜测。那些多出来的 profile 所指向的 endpoint
已经没有了，所以现在一行恰好就是一个 profile，它的 ID 也不带环境段：`binance.spot.v1`、
`nado.perp.usdt0.v1`。

## 6. Gateway 构造

`gateway/account` 构造一条 route 时，先按解码出的 credential kind 分支，再按身份
`venue:product:settlement` 分支。两者都必须命中一个分支。没有分支的身份是一次构造失败，而不
是一个稍后才对着交易所失败的 nil client。

这里也正是分层的收益所在：gateway 从不 import 任何 SDK 包。它手里只有一个 factory config 和
一份 instrument catalog，而这就是它关于「如何与该交易所对话」的全部知识。见
[Exchange gateway](../guides/exchange-gateway.md)。

## 7. Gateway 的 product 行

`gateway/capability` 是一个 venue 变成产品可以交易的东西的地方。每一行携带一个三字母 product
code、行身份、它所准入的操作，以及三个决定订单变得不明确时 gateway 如何行事的字段：

- **精确查询策略**——这一行如何回答「这个标识符到底怎么了」。多数行按 client 或交易所 order
  ID 回读。Lighter 的两行扫描活动列表加第一页非活动记录，并把一次缺席报成「未被证明」而不是
  已证明。
- **写入就绪默认值**——这一行到底接不接受下单。
- **晋升所需证据**——在那个默认值可以改变之前，这一行必须拿出什么。除一行外都是
  `fixture_and_live_exact_lookup`，Lighter 现货那一行还必须证明双边流动性。这份证据过去可以
  在 demo 或 testnet 上取得；那些环境没有了之后，它只能对着 live 盘口取得——晋升的成本因此
  变高，但晋升要求的内容没有变。

有一个字段不容各 venue 商量：没有任何一行声称翻页可以证明缺席，已晋升的行也不行。一个新
venue 不会因为它的历史接口看起来很完整就获得这个权利——为什么一个缺席只是有时候才算答案，见
[状态与协调](../concepts/state-reconciliation.md)。今天哪些行写入就绪由
[不支持的能力](../reference/unsupported.md)拥有。

## 8. 两张矩阵由 manifest 驱动

不要手改操作矩阵。`exchange/testdata/public_surface_manifest.json`
才是来源：每个 product 行一条，携带它的代码、venue、product、factory config、REST 与
WebSocket 方法、fixture 与验收 target。

有三个测试文件读这份 manifest，它们合起来把回路闭上：

- `exchange/docs_manifest_test.go` 据它推导出期望的矩阵，并与两棵语言树逐一比对。
- `exchange/export_coverage_test.go` 据它核对导出面。
- `exchange/external_acceptance_harness_test.go` 据它驱动外部验收。

于是一次 manifest 无法支撑的矩阵改动会红，一次矩阵没有跟上的 manifest 改动同样会红。改
manifest，跑测试，让它们告诉你页面该怎么写。

## 9. 验收 target

没有了，而且新增一条也不属于落地一个 venue 的工作。这里过去有两条阶梯，它们的名字读起来
次序相反，常绊人：`test-exchange-<venue>-<env>-<product>` 聚合成
`test-exchange-acceptance`，`test-gateway-<venue>-<product>-<env>` 聚合成
`test-gateway-demo-testnet`。两条都会在该交易所的 demo 或 testnet 环境下真实的单，也都已经
和那些环境一起删除。没有任何东西接替它们，因为接替者必须花真钱。

现在没有任何 recipe 会为带凭证的写入开闸，`TestMakefileRecipesNeverEnableCredentialedWrites`
会让试图这么做的那一条失败。精确的环境变量名由[配置](../reference/configuration.md)拥有，
剩下的那些 target 以及「一次绿跑到底证明了什么」由[测试](../reference/testing.md)拥有。

## 「做完了」是什么意思

- 离线阶梯零 skip 通过，包含新行的 fixture。
- Manifest、两张矩阵、两棵语言树彼此一致——由测试证明，不是靠读出来的。
- 这一行同时出现在 `routeprofile`、gateway 构造分支与能力矩阵里，且三处的结算资产相同。
- 写入就绪只在拿出该行晋升要求所指名的证据之后才声称；在拿到之前，写入就绪默认值保持不变。
  离线证据永远不代表 production readiness——见[术语表](../reference/glossary.md)。

## 相关

- [架构](../concepts/architecture.md) —— 每一层被允许知道什么
- [Exchange 层](../exchange/README.md) —— SDK 被装进去的那个泛化
- [账户与 instrument](../concepts/accounts-instruments.md) —— 一条 route 如何变成一个账户的凭证
- [不支持的能力](../reference/unsupported.md) —— 仅 SDK 家族与按行的限制
- [测试](../reference/testing.md) —— 命令阶梯
- [贡献文档](documentation.md) —— 镜像与策划规则

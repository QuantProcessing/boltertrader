# 贡献文档

[English](../../contributing/documentation.md)

> 本页是中文镜像；英文版是规范文本。**归属：**本页拥有 public-safe
> curation、canonical-page ownership、English/Chinese mirror maintenance 与
> documentation review rule。

## 为公开边界进行策划

把开发材料移入 `docs/` 前，先应用[术语表](../reference/glossary.md)中的分类。
公开页面不得包含：

- API secret、private key、credential value、proxy credential 或未脱敏的
  signed payload；
- 当 account address 会识别 validation residue、balance、position、order ID、
  fill ID 或 cleanup residue 时，不得包含该地址；
- 绝对本地 filesystem path、machine/user name、raw command output 或完整
  test log；
- internal plan、goal/task identifier、temporary traceability table、reviewer
  conversation、trace 或 implementation chronology；以及
- 指向 private/development-only artifact 的链接，不能把它们当作当前公开行为
  的证据。

当有助于用户操作项目时，可以公开精确 environment-variable name、
repository-relative source path、完整安全示例，以及简洁脱敏的
candidate/date/scope summary。Raw evidence 保留在公开树之外。

## 一个 topic，一个 detail owner

通过[文档索引](../README.md)引导读者，不要复制 canonical inventory：

- Getting Started 拥有 end-to-end onboarding journey。
- Concepts 拥有稳定架构与执行语义。
- Guides 拥有 task-oriented API、data、strategy 与 recovery procedure。
- Venue 页面拥有当前 venue/product behavior、caveat、selector 与 target use。
- [能力矩阵](../adapter-capabilities.md)拥有详细静态 runtime product table。
- [Unsupported surface](../reference/unsupported.md)拥有跨 venue 的 absent、
  deferred 与 SDK-only inventory。
- [测试与证据](../reference/testing.md)拥有完整 command ladder 与 evidence rule。
- [配置](../reference/configuration.md)拥有精确共享 environment name、default、
  alias 与 endpoint-write safety。
- [术语表](../reference/glossary.md)拥有 normative terminology。
- Contributor 页面拥有 extension 与 maintenance obligation。

次级页面可以写明完成任务所需的最小事实并链接到 owner，但不得复制完整
credential table、command ladder、capability matrix、generic submission
contract、SDK-only inventory 或 status ontology。

## English 与 Chinese mirror contract

每个经过策划的英文页面都必须在对应 `docs/zh-CN/**` 路径下有中文 counterpart。
英文是事实来源；中文是维护中的镜像，不是独立 specification。每一对页面必须：

- 在顶部链接 counterpart，并说明 canonical/mirror role；
- 保持 heading hierarchy 与 order；
- 保持 table header、row key、row order 与 factual cell structure；
- 保持 fenced-code count、fence language、warning/admonition placement 与 list
  semantics；
- 完全保留 command、environment variable、identifier、status term、import
  path、repository path、URL、venue/product name 与 numeric default；并且
- 将 internal link 映射到对应 language tree，同时保证 anchor 有效。

翻译解释文字，不翻译 technical token。普通公开文档变更只有在同一对的两份
页面都更新并审查后才算完成。

## Source-backed 写作流程

1. 找出 canonical detail owner 与精确 user outcome。
2. 对照当前 code、contract、Make target、configuration loader 与 test 验证每项
   behavior claim。现有 prose 只是线索，不是唯一证据。
3. 先更新 canonical English owner。事实重复时优先删除并改用链接。
4. 冻结英文 heading、table、command、warning、exact token 与 link。
5. 根据冻结的英文 source 更新中文 mirror。
6. 比较两份页面的结构，以及必须精确保留内容的 token。
7. 通过 link、formatting、capability 与相关 repository test 后再声明完成。

Development-generated material 只能作为 migration input。将稳定事实提取到
public owner，用当前源码独立验证，再按制品自身 lifecycle 删除或保留 private
artifact。绝不能为了保存 traceability 而发布 artifact 本身。

## 状态、分歧与证据措辞

使用精确的[状态语义](../reference/glossary.md)。写明 venue、product、environment
与具体 command/report/stream surface。避免单独使用 “supported”、不加限定的
“market stream”，或缺少 candidate、date、scope、target、zero-skip result 与
terminal assertion 的 “certified”。

当 static capability row、dynamic configuration、concrete method、test 与现有
页面不一致时，将 mismatch 视为 defect。记录由 code 与 test 证明的最窄当前
behavior，修正拥有该事实的 source，不要平均冲突声明，也不要选择最宽的那个。
Demo/Testnet evidence 永远不代表 production readiness。

公开 validation summary 仅包含 candidate/date、具名 Demo/Testnet environment
与 product、精确 target、zero-skip result、scoped terminal assertion 和 material
limitation。不要粘贴 raw log 或 account state。

## Review checklist

- 页面有一个明确 owner，并链接到其他 owner，而不是复制它们的 inventory。
- 两个 language tree 中的每个 relative link 与 local heading anchor 都可解析。
- 每个 command、environment variable、API identifier、path、URL 与 numeric
  default 都存在于当前 source。
- Runnable example 完整且安全；excerpt 有明确标签并链接到 repository source。
- Status language 区分 implementation、capability declaration、external
  evidence 与 production readiness。
- English/Chinese heading、table、fence、warning、link 与 exact technical token
  满足 mirror contract。
- 没有 secret、raw evidence、private path、development plan、task identifier
  或 temporary implementation history 进入 public tree。
- `git diff --check`、`make test-capabilities` 与 changed example/behavior 的相关
  test 通过。

实现专用的 matrix 与 verification obligation 参见
[贡献 Adapter](adapters.md)。

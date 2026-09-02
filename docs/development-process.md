# Log Agent 开发过程与关键决策归档

## 1. 文档定位

本文记录 Log Agent 从概念方案到真实 DAM 轻量试点的开发过程，重点回答四个问题：

1. 当时遇到了什么问题；
2. 为什么选择当前方案；
3. 最终实现和验证到了哪里；
4. 为保持项目轻量，主动放弃或延期了什么。

它不是当前行为规范。代码行为以 [`spec.md`](spec.md) 为准，阶段状态以 [`roadmap.md`](roadmap.md) 为准，真实系统接入位置以 [`m6-real-system-entry-guide.md`](m6-real-system-entry-guide.md) 为准。

文中的“已实现”“Mock 已验证”“真实已验证”和“生产可用”是四种不同状态：

| 状态 | 含义 |
| --- | --- |
| 已实现 | 代码和本地合同已经存在 |
| Mock 已验证 | 使用合成数据和 Mock 外部系统跑通，不代表真实外部系统可用 |
| 真实已验证 | 在明确记录的资源和单次场景中访问了真实外部系统 |
| 生产可用 | 还需要生产存储、多实例、真实身份、质量/费用/留存审批和灰度治理；当前尚未达到 |

## 2. 最初目标与开发原则

最初目标不是做一个可以随意查询日志、自由调用工具的通用聊天机器人，而是把有经验的 SRE 排障过程拆成一条可审计的固定链路：

```text
接收逻辑排障范围
  -> 绑定可信身份和管理员资源目录
  -> 查询当前窗口与等长基线
  -> 检查数据完整性
  -> 形成 Evidence
  -> 本地确定性判断
  -> 可选增强与 LLM 摘要
  -> 展示报告和人工动作
```

开发期间一直保持以下原则：

- 模型不能选择物理 Project、Logstore、查询语句、权限或结论。
- 原始日志默认不离开 SLS；首个真实试点只读取有界聚合。
- Eino、飞书和云产品都放在可替换适配层，领域和应用层不依赖外部 SDK。
- 先用 Mock 关闭代码合同，再用独立 Smoke 验证外部系统，最后才做联合 E2E。
- 每个阶段都明确“能证明什么”和“不能证明什么”，避免把 Demo 或 Mock 写成生产结果。

## 3. 阶段演进总览

| 阶段 | 主要交付 | 当时解决的问题 | 验证边界 |
| --- | --- | --- | --- |
| M0 | Go 调查骨架、状态机、Mock 报告 | 先证明一次调查能够被创建、执行和归档 | 全 Mock |
| M1 | Resource Catalog、ACL、Schema、预算、审计 | 防止用户或模型直接控制云资源和查询 | Mock SLS |
| M2 | current/baseline、Evidence、Finding、Recommendation | 把“感觉像异常”变成有证据引用的确定性判断 | Mock E2E |
| M3 | 变更候选、支持测试与反证测试 | 避免把时间相邻的发布直接说成根因 | 静态 Change Catalog |
| M3-B | 指标/Trace 聚合时间线 | 为后续跨信号排障预留轻量接口 | `signalmock` |
| M4-A | 查询 Checkpoint、未知结果人工复核 | 解决付费查询在进程崩溃后的重复调用风险 | SQLite + Mock |
| M4-B | 投递重试/DLQ、租户额度、成本熔断、审批合同 | 让长任务和外部失败具备可恢复、安全边界 | SQLite 技术预览 |
| M5-A/B | 合成评测、Agent Trace、快照、回放、比较 | 用固定数据集发现工程回归 | 全合成离线评测 |
| M5-C C1/C2 | Mock Reviewer 反馈与灰度演练 | 先关闭反馈和停止条件的数据合同 | 不允许生产动作 |
| LLM 摘要 | Mock/火山方舟适配器、严格引用、fallback、Token 额度 | 提升报告可读性，但不让模型决定事实 | 独立 Smoke + 联合 E2E |
| DAM 真实试点 | CLI+STS、`error_count_v1`、本地 Web | 在字段条件和飞书权限受限时先跑通最小真实链路 | 单主 Logstore、count-only |

历史阶段的详细实现见 [`m0-implementation-archive.md`](m0-implementation-archive.md) 至 [`offline-feedback-and-rollout-rehearsal.md`](offline-feedback-and-rollout-rehearsal.md)。

## 4. 关键开发经历

### 4.1 Eino 还是全部自己写

**问题**

项目需要 Agent 的多步骤编排能力，但如果把状态、权限、查询和报告全部建在框架抽象上，后续很容易被 Eino API、模型调用方式或单一 Provider 绑定。

**方案**

采用“轻量使用 Eino”的方式：

- Eino 只存在于 `internal/adapters/eino`；
- Graph 只负责编排计划、查询、报告和变更关联节点；
- 调查状态、租约、Checkpoint、Evidence、ACL、审计和额度仍由应用层与 SQLite 管理；
- Graph 是确定性的，不依赖 LLM 决定下一步工具。

**效果**

保留了 Agent 工作流的可读性和扩展点，同时 Mock、真实 SLS、火山方舟、本地 Web 和飞书可以分别替换，不需要重写业务内核。

**取舍**

当前不追求开放式 ReAct、自主工具发现和任意循环。能力范围更窄，但更适合日志权限、查询费用和结论准确性要求高的内部系统。

### 4.2 外部系统不可用时先关闭 Mock 合同

**问题**

开发早期没有可直接使用的飞书应用权限和稳定真实云资源。如果等待全部权限齐全才开发，状态机、幂等、投递和报告合同无法提前验证。

**方案**

为外部边界分别建立接口和 Mock：

- `slsmock` 提供确定性 current/baseline 聚合；
- `feishumock` 复用正式接单和 Delivery 生命周期；
- `summarymock` 走正式摘要输入、持久化和展示合同；
- `signalmock`、`runbookmock` 和静态 Change Catalog 只做受控增强。

**效果**

`mock-e2e` 能在零凭据、零网络环境中验证 Intake、SQLite、Worker、Eino、Gateway、Checkpoint、Evidence、摘要和投递。

**取舍**

Mock 只能证明内部合同和回归稳定，不能证明阿里云权限、真实字段、方舟模型质量或飞书客户端效果。后续所有文档都要求把 Mock 和真实结果分开记录。

### 4.3 从阿里云 Go SDK 迁移到 CLI + STS Profile

**问题**

团队已有的真实日志访问方式是企业 SSO 获取短期 STS，再由本机阿里云 CLI 访问 SLS。继续在 Go 服务中维护 SDK Credential Provider，会重复处理凭据、签名和账号切换，也不符合现有授权习惯。

**方案**

废弃原真实 SDK 适配器，改为：

```text
企业 SSO
  -> 短期 STS
  -> 本机 aliyun CLI StsToken Profile
  -> internal/adapters/aliyuncli
  -> SLS 只读 API
```

Go 代码解析一次可信 `aliyun` 路径，直接启动子进程而不经过 shell；Profile、Project、Logstore 和查询模板都来自可信配置。子进程移除 AK/SK/Token 环境覆盖，关闭插件自动安装和 debug，并限制超时与输出大小。

**效果**

应用不读取或保存 STS 内容，查询仍经过 Catalog、ACL、Schema、预算、审计和 Checkpoint。Mock 链路和领域合同不受传输替换影响。

**取舍**

- CLI 和 SLS 插件成为部署依赖；
- STS 到期需要人工续签，当前不适合无人值守的 7x24 服务；
- CLI 成功响应不保证提供云端 Request ID，本地执行 ID 不能冒充 Provider Request ID。

完整迁移记录见 [`sls-cli-sts-migration.md`](sls-cli-sts-migration.md)。

### 4.4 DAM 字段合同不满足时收缩为 `error_count_v1`

**问题**

原 `error_analysis_v2` 需要环境字段、错误选择器、可统计错误维度和实例维度。DAM 当前主 Logstore 能稳定使用 `env + level`，但没有满足合同的 `error_type` 和 `instance_id` 统计字段。直接套用原模板会得到不可信的错误类型、实例或根因结论。

**方案**

没有强迫日志采集侧立即改造，也没有直接拉取非结构化 `msg`，而是新增更小的固定模板 `error_count_v1`：

- 只查询管理员固定的环境与错误条件；
- 每个窗口执行 count-before/count-after；
- 两次计数一致才形成完整 Evidence；
- 只比较当前与基线错误总量；
- 明确禁用错误类型、实例分布、变更原因、跨信号时间线和 SOP 推断。

**效果**

在不修改 DAM 日志格式的前提下，先完成单主 Logstore 的真实连接和错误数量趋势试点，同时保留将来升级到维度分析模板的入口。

**取舍**

当前报告只能回答“错误总量是否明显变化”，不能回答“哪类错误、哪个实例、什么根因”。这是主动缩小范围，不是功能缺陷被隐藏。

### 4.5 LLM 只做证据约束摘要

**问题**

规则报告准确但阅读成本较高；直接让模型读取日志并规划查询，又会扩大数据、权限、幻觉和费用风险。

**方案**

新增 `ReportSummarizer` 端口，模型只接收已通过 Worker 校验的有界报告投影：

- 不发送原始日志、SQL/SPL、物理资源、飞书身份、凭据或 Provider 错误；
- 模型只能改写现象、引用已有 Evidence、选择已有受支持候选和 Recommendation Code；
- 输出使用严格 JSON Schema，Go 侧再次校验引用和危险文本；
- 失败、超时、非法结构或额度不足时使用确定性 fallback，调查本身仍可成功；
- 调用前预留请求/Token 额度，成功后结算实际 Token，结果未知时保留成本代理。

**效果**

默认 `summarymock` 可以离线验证完整链路；显式配置后由 `internal/adapters/volcark` 调用火山方舟。模型提升可读性，但不能修改事实和动作权限。

**取舍**

结构化门禁只能约束引用和输出形状，不能证明自然语言完全没有语义幻觉。真实模型质量仍需脱敏历史故障和专家评审。

### 4.6 飞书权限未就绪时增加本地 Web 入口

**问题**

飞书应用权限暂时拿不到。如果继续等待，就无法把真实 SLS、真实 LLM、Worker、SQLite 和用户交互放进同一次调查中验收。

**方案**

新增 `go run ./cmd/logagent web`，但不删除或改写飞书代码：

- `internal/adapters/localweb` 只替代交互入口和最终 Sender；
- 单进程复用正式 Intake、SQLite、Worker、Eino、SLS、LLM、ActionService 和 Delivery Worker；
- 只监听字面量回环地址；
- 身份固定在服务端，HTTP 请求不能传 Principal 或物理资源；
- 使用 Host/Origin/CSRF、严格 JSON、CSP 和安全报告投影；
- 本地 Sender 仍消费持久化 Delivery 事件，验证排队、运行、终态和卡片重绑语义。

**效果**

先后跑通了：

1. Mock SLS + Mock LLM 的完整 Web E2E；
2. 真实 SLS + Mock LLM 的同调查运行；
3. 真实 SLS + 真实方舟 LLM 的同调查运行。

**取舍**

本地 Web 只证明应用链路，不证明飞书 WebSocket、OpenID、Reply/Patch、卡片视觉和回调权限。它是飞书等待期的轻量入口，不是新的生产前端。

## 5. 2026-09-01 真实系统接入与联合验收

### 5.1 阿里云 SLS 连接验证

真实 DAM 接入先按由浅到深的顺序进行：

1. 验证 CLI、SLS 插件和 `default` StsToken Profile；
2. 查询 StoreView 元数据并确认 8 个成员 Logstore 均返回 `Progress=Complete`；
3. 选择主 Logstore `2016-hyper-dam-file` 作为轻量试点；
4. 用 `sls-check` 验证 Project、Logstore、Standard 模式、索引和 ACL；
5. 用 `sls-smoke dam-server test 10m` 验证固定 `env=test + level=error` 聚合；
6. 再进入正式 Worker 联合链路。

8 个成员 Logstore 的连接测试证明 STS、权限、插件和网络正常，但 Agent 首期没有扩展成 StoreView/8 库联合时间线。真实运行继续固定为一个主 Logstore 和 count-only 模板。

### 5.2 火山方舟独立 Smoke

使用只允许访问 `Doubao-Seed-2.0-mini` 的专用 Key 和模型 `doubao-seed-2-0-mini-260428`，先运行独立 `llm-smoke`：

| 项目 | 结果 |
| --- | --- |
| 状态 | `PASSED` |
| 输入 | 仓库内合成 count-only 报告 |
| 调用 | 方舟 1 次，SLS 0 次，飞书 0 次 |
| Token | input 796，output 175，total 971 |
| 延迟 | 1939 ms |

这一步只证明认证、Responses API、结构化输出和 `SummaryService` 合同，不能与真实 SLS 结果合并描述。

### 5.3 本地 Web 联合验收

随后通过本地 Web 把真实系统接入同一次调查：

```text
本地浏览器
  -> localweb
  -> Intake + SQLite
  -> Worker + Eino
  -> 真实阿里云 SLS current/baseline
  -> 真实火山方舟摘要
  -> 本地 Delivery Sender
  -> 页面报告
```

实际安全投影如下：

| 项目 | 结果 |
| --- | --- |
| Investigation | `inv_2cc5dbaa35cf387a5cb8ef82ba79b18c` |
| 调查状态 | `SUCCEEDED` |
| 确定性 Outcome | `no_significant_spike` |
| Evidence | current/baseline 共 2 份，均 `Complete` |
| 实时计数样本 | current=19，baseline=38 |
| 摘要 | `GENERATED / MODEL` |
| Provider / 模型 | `volcengine_ark` / `doubao-seed-2-0-mini-260428` |
| Token | input 725，output 182，total 907 |
| 模型延迟 | 1771 ms |
| Delivery | `SUCCEEDED`，但仅代表本地 Sender |

current=19、baseline=38 是当时 10 分钟窗口的易变连接样本，只用于证明联合链路，不能写成 DAM 故障事实、长期基线或模型质量结论。

### 5.4 验收中遇到的操作问题

#### 已有 Key 默认显示掩码

方舟 API Key 列表默认显示 `*`。创建后的 Key 需要在 API Key 列显示明文后读取；资源 ID `apikey-...` 不是鉴权 Key。开发过程中坚持只操作本次专用 Key，没有读取或修改其他已有 Key 权限。

#### 浏览器自动化剪贴板与 Windows 剪贴板隔离

页面自动化环境写入的剪贴板没有直接进入 PowerShell。最终使用一次性本机临时文件把 Key 交给单次验收进程，并立即删除传递文件；Key 没有进入仓库、配置、SQLite、测试输出或文档。

这个做法只用于本次受控验收。正式运行必须由组织密钥系统直接向 Worker 注入，不应依赖浏览器或临时文件。

#### PowerShell 变量名冲突

首次自动提交时使用了 `$home` 保存页面响应。PowerShell 变量名不区分大小写，因此它与只读 `$HOME` 冲突。后续改为任务专用变量 `$pilotPage`。项目后续脚本都应避免复用 `$HOME`、`$home`、`$CODEX_HOME` 等系统变量。

### 5.5 凭据与现场清理

联合验收结束后完成以下清理：

- 停止携带 `ARK_API_KEY` 的本地 Web 进程；
- 删除一次性 Key 传递文件；
- 清空自动化环境中的 Key 变量；
- 从方舟控制台删除本次临时 Key；
- 确认提交内容只有文档，没有凭据、资源目录或 SQLite 数据库。

因此仓库只能保存“如何注入 Key”和安全验收元数据，不能保存可再次调用的 Key。

## 6. 当前可运行形态

### 6.1 完全离线开发模式

```powershell
$env:LOG_AGENT_SLS_MODE = "mock"
$env:LOG_AGENT_LLM_MODE = "mock"
go run ./cmd/logagent web
```

该模式不需要凭据、不访问网络，适合功能开发和回归。

### 6.2 真实 SLS + 真实方舟 + 飞书 Mock

```powershell
$env:LOG_AGENT_SLS_MODE = "aliyun"
$env:LOG_AGENT_SLS_CATALOG = ".\config\sls-resources.json"
$env:LOG_AGENT_SLS_CLI_PROFILE = "default"
$env:LOG_AGENT_LLM_MODE = "volcengine"
$env:ARK_API_KEY = Read-Host "粘贴方舟 API Key" -MaskInput
$env:LOG_AGENT_ARK_MODEL = "doubao-seed-2-0-mini-260428"
$env:LOG_AGENT_WEB_ADDR = "127.0.0.1:8080"
$env:LOG_AGENT_WEB_DB_PATH = ".\data\web-pilot.db"
go run ./cmd/logagent web
```

页面提交 `dam-server / test / 10m / error_count_v1`。STS 过期时需要先由用户重新配置 CLI Profile；方舟 Key 必须由当前进程临时读取，不能写入仓库。

## 7. 当前能够和不能够宣称的结果

### 已经能够宣称

- Go 主体、Eino 固定 Graph、SQLite 状态机和受治理查询链已经实现。
- Mock 飞书 + Mock SLS + Mock LLM 的完整应用链路已经通过自动化测试。
- DAM 主 Logstore 的真实只读 `sls-check`、`sls-smoke` 和 count-only Worker 查询已经通过。
- 火山方舟独立 Smoke 和“真实 SLS + 真实方舟”的本地 Web 同调查联合 E2E 已通过。
- 模型失败不会改变确定性报告或调查成功状态。

### 仍不能宣称

- 真实飞书链路已经接通；
- DAM 8 个 Logstore 已形成统一排障时间线；
- 已经识别具体错误类型、实例或根因；
- 方舟摘要在真实历史事故上的质量已经达标；
- Token 单价、账单、Prompt 和留存策略已经获批；
- SQLite、多实例租约和本地额度已经满足生产要求；
- 当前系统可以自动执行修复或生产变更。

## 8. 目前遗留工作

建议继续按轻量顺序推进：

1. 获得飞书权限后完成 WebSocket、真实 OpenID/TenantKey、Reply/Patch、卡片视觉和 `card.action.trigger` 验收；
2. 建立方舟 Key 的正式密钥托管与 Worker 注入方式；
3. 用脱敏历史故障和专家标注评估摘要忠实度、可读性、费用和延迟；
4. 完成 Prompt、方舟留存和费用审批；
5. 根据真实需求决定是否让日志采集侧补 `error_type/instance_id`，再升级到 `error_analysis_v2`；
6. 只有明确需要跨库 TraceID 时间线时，才设计 StoreView/8 Logstore 资源模型，避免提前做重；
7. M4-C 再处理生产数据库、多实例、全局额度、备份恢复和故障演练；
8. 真实指标/Trace、企业 SOP 和发布平台继续作为独立适配器接入，不塞进当前轻量主链。

## 9. 验证与版本记录

本地 Web 功能提交：

- `bdab9a0 feat: add loopback web pilot console`

真实 SLS + 方舟联合验收文档提交：

- `d83ca87 docs: record real SLS and Ark joint validation`

联合验收后执行并通过：

```text
go test -count=1 ./...
go vet ./...
git diff --check
```

`go test -race ./...` 因当时 Windows 环境 `CGO_ENABLED=0` 且没有可用 C 编译器而未执行，不能写成已通过。

## 10. 后续维护规则

- 新阶段完成时，在本文追加“问题—方案—效果—取舍”和实际验证，不重写历史为当前状态。
- 行为合同变化必须先更新 `spec.md`，不能只更新本文。
- Mock、独立 Smoke、联合 E2E 和生产灰度必须分开记录。
- 实时日志计数只能作为带日期和窗口的易变样本，不能写成长期事实。
- 文档不得保存 API Key、STS、AK/SK、原始日志、物理查询正文或 Provider 原始错误。
- 每次真实接入都要记录凭据来源、最小权限、调用次数、数据范围、清理方式和未覆盖项。

## 11. 2026-09-02：从结构化表单走向受治理自然语言接单

### 问题

原有 `/investigate service environment duration` 和本地结构化表单很安全，但用户必须先知道服务名、环境名和查询模板。直接让 LLM 把一句 Bug 描述翻译成 SLS 查询又会把权限、物理资源、查询语法和成本一起交给模型，既难审计，也容易越权或误查。

### 方案

新增独立的 `IntentResolutionService`，只让模型在当前用户已获授权的逻辑 Capability 中选择 `service/environment/error_count_v1` 并解析时间窗。问题文本先做长度校验、敏感信息脱敏和明显指令注入拦截；解析结果先持久化为预览，只有用户用同一可信身份确认后，应用才重新校验 ACL 和过期时间，并调用既有 Intake 创建任务。意图模型与报告摘要模型分开配置、分开限额，避免一个入口耗尽另一条链路的预算。

### 难点与解决

- “自然语言”不能等同于“执行授权”：用 `resolve -> confirm` 两步状态机把理解和付费查询隔开。
- 飞书重投与重复点击会导致重复任务：解析记录绑定 `(app, tenant, message)`，确认复用原 Intake 幂等键。
- 模型可能返回未授权资源或物理信息：输入只包含 ACL 过滤后的逻辑能力，输出再经关闭枚举、格式、置信度、窗口和模板校验。
- Provider 超时是否重试不明确：不自动重试，记录 `OUTCOME_UNKNOWN` 并保留额度代理，避免静默重复计费。
- 群聊正文带 `@_user_1`：适配器只依据飞书结构化 mention 判断是否点名机器人，随后移除该标记再进入应用层。
- 旧 SQLite 数据库没有版本标记：第一阶段引入 `PRAGMA user_version=1`；第二阶段增加 Trace Checkpoint 后升为版本 2，在事务中执行幂等建表并拒绝比当前代码更新的 Schema。

### 效果与边界

Mock 页面链路已经能够完成“输入问题—查看预览—确认—Intake—Worker/Eino—Mock SLS—报告—本地 Delivery”，并证明确认前不会创建调查。单元测试覆盖重复解析/确认、越权、低置信度、Trace 不降级、注入、额度和方舟严格 JSON 合同。真实方舟意图解析尚未执行；当前也没有实现 TraceID 8 库查询、错误锚点、部署 Commit 或代码证据，因此本阶段只能改善接单体验，不能宣称已经能从任意 Bug 描述定位代码根因。

## 12. 2026-09-02：从错误趋势进入 DAM TraceID 跨库时间线

### 问题

单 Logstore 的 `error_count_v1` 可以回答错误是否增加，却不能把同一请求在 DAM Server、消费、转码和定时任务等日志来源中的执行顺序串起来。直接把 StoreView 或 8 个物理 Logstore 交给模型选择，会扩大越权、注入和扫描成本；复用聚合查询端口又会把“计数桶”和“脱敏事件”混成不稳定合同。

### 方案

新增独立 `TraceResourceCatalog/TraceBackend/GovernedTraceExecutor` 端口和 `trace_search_v1` 报告合同。自然语言只增加关闭的 `trace_search` 枚举；确认后由 `RoutingEngine` 选择 TraceEngine。TraceEngine 先执行管理员指定的主成员，再以最大并发 2 执行其余成员；Gateway 对每个成员重新做 ACL、Schema、窗口、条数、字节和超时约束，并在写 Evidence 前完成 TraceID与敏感信息脱敏。SQLite 为每个成员保存有租约 fencing 的 Checkpoint 和开始/终止审计。

### 难点与解决

- 8 个库字段能力不同：资源目录逐成员声明全文/字段检索和允许投影，不在代码中假设所有库有同一 Schema。
- 需要并发但不能给 SLS 压力：主服务串行优先，其余成员使用固定 worker pool，上限 2；测试同时验证调用首项与最大活跃数。
- 进程崩溃后不知道付费查询是否完成：`STARTED` Checkpoint 被新租约发现时转为 `OUTCOME_UNKNOWN`，调查进入 `NEEDS_REVIEW`，不自动查询。
- 原始日志有泄露风险：Gateway 只接受关闭的事件投影，替换 TraceID并脱敏 Token、邮箱、IP 与 URL 参数，再计算消息指纹；物理 Logstore 不进入页面和卡片。
- 多库时间戳精度不一：保留纳秒/秒/未知质量标记，按时间、成员和事件 ID 稳定排序；窗口外或缺少事件时间的成员降级为不完整。

### 效果与边界

全离线测试和 Mock `trace-smoke` 已完成：一次调查覆盖 8 个逻辑成员、8 个 Checkpoint 和 16 条开始/终止审计，Mock 场景产生 2 条脱敏事件；普通 Eino 错误趋势链路未被替换。首次真实 `trace-check` 暴露了“全文索引没有字段 Keys 被误判为空索引”的兼容问题；Trace 专用 Schema 读取器随后同时识别字段索引和 `line` 全文索引，保留原错误计数模板的严格字段规则。重跑后 8/8 成员均为 `READY`、日志读取为 0。真实 TraceID 日志查询仍需 `trace-smoke` 验收；当前尚未实现错误锚点、部署 Commit、代码检索或根因确认。

## 13. 2026-09-02：把脱敏日志转换成可控的代码检索锚点

### 问题

跨库时间线回答了“什么时候、哪个逻辑成员出现了什么现象”，但不能安全地回答“应该去代码仓库的哪里查”。如果直接让模型遍历仓库，既无法保证读取的是实际部署版本，也会放大上下文、成本、秘密泄露和误定位风险。

### 方案

在 TraceEngine 中增加纯 Go、无 I/O 的 `runtime-anchor-v1` 提取层。它只消费已经通过 Gateway 脱敏、窗口校验和全局预算裁剪的事件，关闭支持错误文本、错误类型、HTTP 路由、函数符号和 Go/Java/Python 堆栈帧五类锚点。路径必须规范化为安全仓库相对路径；每事件最多 4 个、全调查最多 64 个。

每个锚点绑定来源事件和逻辑成员，并保存可重算的内容指纹与稳定 ID。生产输出校验器会验证锚点形状、来源绑定、去重、排序、数量、状态，以及 Timeline/Evidence 的精确一致性。Web 最多展示 12 个、飞书最多展示 8 个，均固定提示“只用于定位，不代表根因”。

### 难点与解决

- 堆栈可能带 Windows 或容器绝对路径：只从批准的 `internal/cmd/pkg/src` 目录边界裁剪为仓库相对路径，无法安全裁剪就丢弃。
- 一条日志可能同时包含多个可检索值：按堆栈、符号、错误类型、错误文本、路由排序，再执行事件级和全局预算；发生丢弃就降级为 `PARTIAL`。
- 锚点可能被持久化层或不可信 Engine 篡改：Worker 根据关闭字段重新计算指纹和 ID，并要求事件内集合与全局集合一一对应。
- “提取完整”容易被误解为“根因确认”：状态只描述提取过程；代码和界面都不产生根因字段，并明确记录非因果边界。

### 效果与取舍

离线测试已经覆盖三种语言堆栈、路径规范化、错误/路由提取、危险值拒绝、预算、篡改和 UI 集成。Mock Trace 调查能生成可展示的代码检索入口，同时没有增加外部调用。取舍是规则提取不会理解所有自定义日志格式，但结果确定、成本固定、可审计；未知格式宁可得到 `NO_ANCHORS`，也不会让模型自由猜测代码位置。真实 DAM 日志中的锚点召回率仍需代表性 TraceID 验收。

完整合同见 [`runtime-error-anchors.md`](runtime-error-anchors.md)。

## 14. 2026-09-02：只看事故时真正部署的代码

### 问题

运行时锚点已经能指出错误短语、函数或堆栈位置，但直接搜索仓库当前分支会把未发布代码当成事故证据；把整个 Git 仓库交给 LLM 又会失去路径、秘密和成本控制。部署版本缺失时，任何代码解释都可能建立在错误版本上。

### 方案

新增独立的 `DeploymentVersionSource`、`CodeRepositoryCatalog` 和 `CodeEvidenceProvider`。管理员严格 JSON 目录按服务、环境和事故时间解析唯一的完整 Commit，并把逻辑 Repository ID 映射到本机仓库顶层及允许/禁止路径。只有完整 Trace 和安全锚点存在、部署解析为唯一 `COMPLETE` 时，Worker 才会调用代码 Provider。

首个 `gitcode` 适配器不经过 Shell，只允许固定的 Git 参数数组：核对仓库顶层和 Commit、按锚点精确固定字符串搜索、读取 `<commit>:<path>` Blob、以及列出可信前后部署间的变更文件。固定预算限制为 16 个锚点/匹配、8 个文件、480 行、64 KiB 片段和 48 条 Git 子命令。代码片段先拦截强凭据并脱敏邮箱/IP，再由 Worker 重算 Anchor、Commit、Blob、查询与内容指纹。

### 难点与解决

- 本地仓库可能有未提交修改：所有正文都通过 `git show <sha>:<path>` 读取，测试专门在工作区写入未提交哨兵并证明不会命中。
- 配置路径可能只是某个父仓库的子目录：执行 `rev-parse --show-toplevel`，要求结果与管理员根目录完全一致。
- 锚点可能导致大范围搜索：只用 `grep -F`，只搜索允许路径，限制锚点、匹配、文件、行、字节、命令和超时。
- 代码可能含秘密：秘密类文件名直接拒绝；正文含私钥、AK、Bearer 或强凭据赋值时整段跳过并降级，邮箱和 IP 先脱敏。
- 最近变更容易被误报成根因：只列可信前后部署的变更文件并标记重叠，界面固定声明它只表示相关。
- 本地 Git 是否需要付费查询 Checkpoint：本地不可变 Commit 读取无远端副作用和按次费用，可以在 Trace Checkpoint 恢复后重跑；未来网络仓库 Provider 必须另加审计和未知结果恢复，不能沿用该假设。

### 效果与边界

临时 Git 仓库测试使用两个真实本地 Commit，覆盖精确搜索、堆栈直达、Diff 文件、工作区隔离、禁止路径、凭据拦截、邮箱/IP 脱敏、预算和 Commit 缺失。Trace Worker、Web 和飞书投影已经接入；飞书只显示文件/行号，不显示代码正文，现有方舟摘要输入也显式排除代码。

这证明了本地只读合同，但当前没有把 DAM 实际部署平台记录写入目录，也没有代表性真实 Trace 的代码联合 Smoke，不能宣称已经定位出真实代码根因。完整说明见 [`deployment-and-code-evidence.md`](deployment-and-code-evidence.md)。

## 15. 2026-09-02：把相关代码路径变成可证伪的根因候选

### 问题

第四阶段已经能说明“事故时部署了哪个 Commit，以及运行时锚点能在其中命中哪里”，但代码命中本身还不是根因。直接让模型根据代码片段自由下结论，会把静态路径、发布时间和文件重叠混成无法复算的主观推断；反过来只展示文件和行号，又没有告诉工程师为什么优先查、还缺什么证据。

### 方案

新增纯 Go 的 `joint-rca-v1` 后处理层。它不增加任何外部调用，而是把已经通过 Worker 校验的 Trace Anchor、事故时部署指纹、精确 Code Match 和可信前后 Commit 文件关系，投影成最多 8 个 `DEPLOYED_CODE_PATH` 候选。每个候选固定产生五项支持/反证/缺失账本和三个人工验证动作；Candidate 与 Factor ID 均由输入指纹稳定生成。

分数采用关闭规则：精确文本基础 `0.60`、堆栈直达 `0.65`、可信变更重叠加 `0.10`、文件未变化减 `0.05`，上限 `0.75`；代码截断时上限 `0.45` 并强制 `INCONCLUSIVE`。这个数表示证据规则完备度，不是概率。自动分析最高只到 `SUPPORTED_CANDIDATE`，所有建议固定 `HUMAN_REVIEW_ONLY`。

### 难点与解决

- 代码路径支持和最近发布回归容易混淆：候选 Kind 只描述已部署代码路径；文件未变化仅作为“最近源代码变更解释”的软反证，不会错误否定配置、输入或依赖导致同一路径执行的可能性。
- 不可信 Engine 可能预填更乐观结论：Worker 禁止 Engine 返回联合 RCA，并在后处理后从 Trace/Code 对象重建完整投影，任何字段差异都拒绝持久化。
- 部分代码证据仍有参考价值：保留已找到的位置，但将状态和 Verdict 降为 `INCONCLUSIVE`，补充 `complete_code_search` 缺失项并限制分数。
- UI 既要可用又不能泄露代码：本地 Web 通过深拷贝和 `textContent` 展示；飞书只显示文件/行、账本和人工动作，不显示代码正文；现有方舟摘要输入用哨兵测试确认不包含 Code/JointRCA。
- “人工动作”容易演变为自动处置：动作合同没有执行端口，只提供核对分支、复现、审阅可信 Diff 或检查运行时依赖四类说明。

### 效果与边界

单元测试覆盖变更支持、未变更反证、代码截断、无命中、部署冲突、Trace 不完整、确定性 ID 和篡改拒绝。集成测试把 DAM 8 成员 Mock Trace、错误锚点、部署证据、精确代码命中和 `SUPPORTED_CANDIDATE` 串入同一个 Worker 调查；Web/飞书投影与 LLM 出站隔离也已验证。

这些结果证明第五阶段实现和离线合同，不证明真实 DAM 的根因准确率。真实 Trace 查询、真实部署目录和仓库 Commit 对尚未完成同调查 Smoke，候选也没有经过事故专家盲评。完整说明见 [`joint-root-cause-candidates.md`](joint-root-cause-candidates.md)。

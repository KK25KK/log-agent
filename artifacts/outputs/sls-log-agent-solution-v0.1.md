# 基于 Go、飞书与 Eino 的日志 Agent 整体方案

| 元数据 | 内容 |
| --- | --- |
| 版本 | v0.1 |
| 状态 | Draft，待评审 |
| 日期 | 2026-08-18 |
| 首个业务场景 | 生产服务错误突增调查 |
| 技术主线 | Go + 飞书自建应用 + Eino + 阿里云 SLS |

## 1. 结论先行

建议使用 Eino，但采用“受控版 Eino”：

- 固定使用当前非预发布版本 `v0.9.14`，暂不使用 `v0.10.0-alpha`。
- 用 `Eino ADK ChatModelAgent + Runner` 处理对话、意图识别和有限的工具选择。
- 用 `eino-compose Workflow/Graph` 实现可预测、可测试的调查流程，再将 Graph 封装为 Agent Tool。
- 飞书接入、权限、任务队列、查询预算、证据、审批和审计由自有 Go 业务层负责。
- 第一版不用 DeepAgent、Supervisor、多 Agent、任意 Text-to-SQL 和自动生产处置。

Eino 可以成为日志 Agent 的编排内核，但不能成为整个日志平台的业务内核。这个组合与 Eino 官方建议的“Agent 负责动态决策，Graph 负责确定性流程，Graph 再作为 Tool 提供给 Agent”一致。

推荐首版形态：

> Go 模块化单体 + 飞书长连接入口 + 关系型数据库持久任务 + Eino 确定性调查 Graph + SLS 类型化查询工具 + Evidence Ledger。

## 2. 产品目标

### 2.1 用户价值

研发或运维人员在飞书中提出问题，例如：

> `@日志助手 查一下 order-service 生产环境最近 30 分钟为什么错误突增`

Agent 应返回一份可复核的调查结果，而不只是生成一条查询语句：

- 发生了什么：错误量、错误率、开始时间和影响范围。
- 异常集中在哪里：版本、实例、接口、错误码或日志模板。
- 可能为什么：候选根因及置信度。
- 证据是什么：具体查询、时间范围、结果完整性和样本。
- 哪些解释被排除了：反证和数据不足项。
- 下一步怎么做：继续查询、查看 Trace、联系负责人或执行 SOP。

### 2.2 成功标准

首个版本成功，不以“能聊天”为标准，而以以下结果衡量：

- 用户能在飞书中一句话创建调查，并看到阶段进度。
- 所有事实结论都能追溯到具体查询和确定性计算。
- 越权查询为零，日志中的提示注入不能改变工具权限。
- 飞书事件重复、Worker 重启或消息发送重试不会产生重复调查或重复报告。
- 对历史事故回放时，形成可量化的根因命中率、耗时和成本基线。

## 3. 范围与非目标

### 3.1 第一阶段范围

只聚焦一个垂直场景：

> 某服务、某环境在给定时间窗内出现错误突增，定位异常维度并给出有证据的候选原因。

需要支持：

- 飞书单聊和群内 `@机器人`。
- 服务、环境、时间窗的识别与补充确认。
- SLS 资源发现、Schema 获取、趋势、Top-N、上下文和双时间窗对比。
- 飞书阶段进度、取消、扩大时间窗和查看证据。
- 证据、任务状态、查询摘要和通知 Outbox 的持久化。

### 3.2 暂不做

- 多 Agent 辩论、角色群聊或动态 Supervisor。
- 同时覆盖错误、延迟、容量、安全和业务分析等所有场景。
- 让模型直接生成并执行任意 SQL/SPL。
- 自动重启、回滚、改配置等生产写操作。
- 自研日志聚类算法、完整知识图谱或大型 RAG 平台。
- 把大量原始日志直接塞给模型。
- 先拆十几个微服务或引入新的 Kafka/RocketMQ 集群。

## 4. 总体架构

```text
飞书消息 / 卡片操作
        │
        ▼
Feishu Adapter
长连接、消息标准化、准入、message_id 去重、快速确认
        │
        ▼
Durable Inbox + Investigation Job + Outbox
        │
        ▼
Investigation Worker
  ├─ 调查生命周期、租约、重试、取消
  ├─ Eino ADK：对话理解、有限工具路由
  ├─ Eino Graph：Scope → Baseline → Drilldown → Correlate → Verify → Report
  ├─ 确定性分析器：统计、对比、排序、置信度约束
  └─ Evidence Ledger：事实、推断、反证、数据完整性
        │
        ▼
SLS Tool Policy Gateway
ACL、Schema、QuerySpec 校验、预算、脱敏、执行、审计
        │
        ├──────────────► 阿里云 SLS Go SDK（生产核心路径）
        └──────────────► 阿里云可观测 MCP（PoC / 跨信号扩展）
        │
        ▼
飞书进度卡片 / 最终报告 / 后续操作
```

### 4.1 部署建议

第一版采用一个 Go 仓库、一个二进制、两个运行模式：

```text
logagent feishu
logagent worker
```

- `feishu`：接收事件、持久化 Inbox、创建任务并快速结束处理。
- `worker`：领取任务，运行 Eino、SLS 查询、确定性分析和报告生成。
- 关系型数据库：任务、调查、证据、会话、审批和 Outbox 的事实来源。参考实现优先 PostgreSQL；若公司已有统一 MySQL 8，也可用同等能力实现。
- Redis：第一版可选，仅在需要分布式短期缓存、锁或高频限流时增加。
- 公司已有 MQ 可直接复用；没有时先用数据库任务表和租约，避免为 MVP 新建消息基础设施。

入口和 Worker 必须逻辑分离。飞书事件要求在 3 秒内处理成功，且采用至少一次投递；Agent 调查可能持续几十秒到数分钟，不能运行在回调线程中。

## 5. 模块组成与职责

### 5.1 Feishu Adapter

负责：

- 企业自建应用机器人的 WebSocket 长连接。
- 接收 `im.message.receive_v1` 和后续 `card.action.trigger`。
- 消息标准化、群聊必须 `@机器人`、群/用户白名单。
- 对消息事件按 `message_id` 做持久化幂等。
- 将消息转成与飞书无关的 `InboundCommand`。
- 发送接单回执、阶段进度和最终卡片。

一期可以使用官方 Go SDK `Channel` 加速消息归一化、群聊策略、卡片和流式回复，但 Channel 仅放在适配层。它的进程内去重和内存队列不能替代 Durable Inbox。

### 5.2 Conversation Session

负责飞书对话上下文，而不是调查执行：

- `chat_id`、`thread/root_id`、用户和最近调查的映射。
- 多轮补参数，如服务、环境和时间窗。
- 防止同一用户连续发起多个调查时串线。

### 5.3 Investigation Domain

代表一次独立调查，是业务事实来源：

- 调查范围、当前阶段、状态和版本。
- 候选假设、最终 Findings 和未解决问题。
- 所有 Evidence、审批、取消和反馈。

推荐状态机：

```text
QUEUED
  → RUNNING
  → WAITING_INPUT / WAITING_APPROVAL
  → RUNNING
  → SUCCEEDED / FAILED / CANCELLED / EXPIRED
```

### 5.4 Durable Job

负责长任务可靠运行：

- Worker 租约、心跳、超时和重领。
- 步骤级幂等键、重试次数和退避。
- 取消信号和底层查询超时。
- 调查状态与通知 Outbox 的事务一致性。

### 5.5 Eino Orchestration

Eino 的职责限制在以下范围：

- `ChatModelAgent + Runner`：理解用户目标，决定调用哪个受控 Graph Tool。
- `compose.Workflow/Graph`：固定调查步骤、分支和有限反证循环。
- Tool JSON Schema：约束模型可提交的参数。
- Callback：采集模型、Tool 和 Graph 的耗时、Token、错误与事件。
- Interrupt/Resume：后续用于补充输入或敏感动作审批。

Eino 不负责：

- 企业权限和凭证。
- 飞书消息可靠投递。
- 长期业务状态和审计。
- 查询预算和数据脱敏。
- exactly-once 或跨版本任务恢复。

业务层通过窄接口依赖 Eino，例如：

```go
type InvestigationEngine interface {
	Start(ctx context.Context, req StartRequest) (InvestigationID, error)
	Resume(ctx context.Context, id InvestigationID, input ResumeInput) error
	Cancel(ctx context.Context, id InvestigationID, actor Actor) error
}
```

Eino 类型只允许出现在 `internal/orchestration/eino`。这样升级或替换框架时，不影响领域、任务和飞书层。

### 5.6 SLS Tool Policy Gateway

所有日志查询都必须经过这一层，模型不能直接访问 SLS SDK、CLI 或 MCP：

- 将业务资源映射到允许的 Project/LogStore。
- 先读取索引 Schema，再接受字段和聚合请求。
- 校验 `QuerySpec` 的字段、时间窗、行数、并发和扫描预算。
- 编译并执行索引查询、SQL 或 SPL。
- 检查结果是否 Complete/Incomplete、是否截断或超时。
- 对样本做脱敏，仅返回调查所需字段。
- 记录调查 ID、用户、资源、规范化查询、成本和结果摘要。

建议定义强类型契约：

```go
type QuerySpec struct {
	Scope       LogScope
	TimeRange   TimeRange
	Filter      FilterExpr
	GroupBy     []FieldName
	Metrics     []MetricExpr
	Limit       int
	Purpose     QueryPurpose
	BudgetClass BudgetClass
}

type QueryResult struct {
	Rows         []Row
	Complete     bool
	Truncated    bool
	ScannedBytes int64
	QueryID      string
	Warnings     []string
}
```

### 5.7 Deterministic Analyzer

所有数字和排序尽量由 Go 代码计算，而不是让模型心算：

- 当前时间窗与历史基线的变化率。
- 错误码、接口、版本、实例等维度的贡献度。
- Top-N、首次出现时间、异常集中度。
- 正常实例与异常实例、发布前与发布后的对照。
- 数据不足、样本偏差和不完整结果的判定。

模型负责“提出假设和组织表达”，分析器负责“计算事实”。

### 5.8 Evidence Ledger

Evidence 是报告的最小可信单元，至少包含：

```text
evidence_id
investigation_id
kind
query_spec_hash
project / logstore
time_range
result_summary
sample_refs
complete / truncated
supports_or_refutes
created_at
```

最终报告需要明确区分：

- 事实：确定性查询和计算直接支持。
- 推断：多条事实共同支持的候选解释。
- 反证：不支持或削弱某个假设的数据。
- 待确认：当前数据无法判断的事项。

不要保存或展示模型的隐藏思维过程；只保存结构化决策、工具调用和可复核证据。

## 6. 核心执行流程

### 6.1 飞书接单

```text
收到消息
→ 准入策略检查
→ 以 message_id 写 Durable Inbox
→ 创建 Investigation 与 Job
→ 事务写 Result Outbox
→ handler 返回成功
→ Sender 回复“已创建调查 INV-xxx”
```

同一 `message_id` 重放只允许创建一个调查。重要回复通过稳定的幂等键发送；必要时直接使用飞书原子回复 API 的 `uuid`，而不是只依赖 Channel 自动重试。

### 6.2 错误突增调查 Graph

```text
Scope
  确认服务、环境、时间窗与权限
    ↓
Discover
  解析服务到 Project/LogStore，读取索引 Schema
    ↓
Baseline
  当前趋势与历史窗口对比，确认是否真实异常
    ↓
Drilldown
  错误码、接口、版本、实例、模板 Top-N 与贡献度
    ↓
Correlate
  获取少量上下文；后续关联发布、Trace、K8s、CMDB
    ↓
Verify
  主动寻找正常实例、旧版本或相邻时间窗反例
    ↓
Report
  事实、推断、反证、置信度、下一步和证据入口
```

任何阶段遇到以下情况，都不能继续输出确定根因：

- 资源或字段不存在。
- 用户无权访问目标环境。
- SLS 返回不完整、截断或超时结果。
- 当前窗口与基线不可比。
- 关键字段缺失或日志延迟到达。

### 6.3 飞书交互

一期采用“阶段进度卡”，不按 Token 刷新：

1. 已创建调查并确认范围。
2. 正在比较当前窗口与基线。
3. 已发现异常维度，正在验证反例。
4. 已完成、失败或等待补充信息。

建议按钮：

- 停止调查。
- 扩大到 24 小时。
- 查看证据。
- 继续查 Trace。
- 重新运行。

普通消息存在更新次数限制，因此一期显式节流更新。真正的 CardKit 打字机流式卡片作为后续亮点，不放入 MVP 必选范围。

## 7. Eino 选型评估

### 7.1 为什么建议采用

- Go 原生，组件、工具参数和 Graph State 可以类型化。
- ADK 已提供 Agent、Runner、事件流、Middleware、Interrupt/Resume 和 Checkpoint。
- Compose 可以精确控制分支、并行、循环和流式数据。
- Graph 可以封装为 Tool，适合“对话入口 + 受控调查流程”。
- Callback 可承载日志、Trace、指标和成本观测。
- `eino-ext` 可快速连接模型或 MCP，减少自建运行时工作。

### 7.2 风险和控制措施

Eino 目前仍是 0.x，ADK、Session、Checkpoint 和 AgentEvent 持续演进。控制措施：

- 锁定 `v0.9.14` 及精确的 `eino-ext` 版本。
- 暂不使用 `v0.10.0-alpha` 的后台任务等预发布能力。
- 在自有接口后隔离 Eino 类型。
- 自定义稳定的业务事件，不把原始 `AgentEvent` 当数据库协议或飞书协议。
- 每次升级先运行 Tool 契约、历史事故回放和 Checkpoint 兼容测试。
- Eino Checkpoint 只作为短期运行快照，带 Graph 版本和 TTL；数据库中的 Investigation 与 Evidence 才是事实来源。

### 7.3 第一版 Eino 使用边界

使用：

- `adk.ChatModelAgent`
- `adk.Runner`
- `compose.Workflow/Graph`
- 强类型 Tool / GraphTool
- Callback

暂缓：

- DeepAgent
- Supervisor / Multi-Agent
- AgenticMessage Beta 路径
- 通用 ReAct 自由工具循环
- 依赖 Eino 的长期 Session 或后台任务能力

## 8. 飞书接入决策

### 8.1 应用类型

使用企业自建应用机器人，不使用群自定义机器人。自建应用可以订阅消息事件、回复消息、更新卡片并控制可用范围；群自定义机器人不适合作为有状态对话 Agent 入口。

### 8.2 接入模式

一期选择官方 Go SDK WebSocket 长连接：

- 只要求服务端可以访问公网，不需要提供公网回调地址。
- SDK 处理长连接鉴权和传输。
- 多客户端采用集群消费，同一事件只给其中一个客户端，不是广播。
- 每个应用最多可建立 50 个连接。

保留 `EventReceiver` 接口，以下情况可切 Webhook：

- 公司要求所有外部流量经过 API Gateway/WAF。
- 运行环境不允许维持出站 WebSocket。
- 需要统一的 HTTP 回调治理。

### 8.3 可靠性约束

- 消息事件 Handler 必须在 3 秒内完成。
- 飞书采用至少一次投递，即使成功也可能收到重复事件。
- 对 `im.message.receive_v1` 按 `message_id` 做数据库唯一约束。
- 卡片动作按其唯一事件标识和业务动作哈希做幂等。
- 多副本不能依赖 SDK 的进程内 LRU 去重。
- 结果发送使用 Outbox 和幂等发送键，避免任务完成但飞书未通知或重复通知。

### 8.4 最小权限

一期仅申请：

```text
im:message.p2p_msg:readonly
im:message.group_at_msg:readonly
im:message:send_as_bot
事件：im.message.receive_v1
```

交互卡片再增加 `card.action.trigger`；需要更新消息或 CardKit 时按实际 API 增加对应权限。第一期不申请群全量消息权限 `im:message.group_msg`。

## 9. SLS 接入决策

定义统一接口：

```go
type SLSExecutor interface {
	DescribeSchema(ctx context.Context, scope LogScope) (Schema, error)
	Execute(ctx context.Context, spec QuerySpec) (QueryResult, error)
}
```

保留两个适配器：

### 9.1 Direct SDK Executor

建议作为生产核心路径：

- 使用阿里云官方 Go SDK。
- 查询、错误语义、超时、并发、预算、脱敏和审计均可强控制。
- 避免“模型生成自然语言查询，再由另一个智能服务生成 SQL”的双层不确定性。

### 9.2 Observability MCP Executor

建议作为 PoC 加速器和后续跨信号扩展：

- 官方已有 SLS、CMS、ARMS 等工具，适合快速验证。
- Eino-ext 可以把 MCP Tool 适配为 Eino Tool。
- 仍要通过自己的 Tool Registry 和 Policy Gateway，不把全部 MCP 工具直接暴露给模型。
- HTTP 模式部署在 VPC/受信网络并补齐认证、审计和允许工具列表。

M0 同时做两个最小适配器，用同一批用例比较查询正确性、权限颗粒度、P95 延迟、查询成本、错误可解释性和审计完整度，再记录正式 ADR。预计生产核心查询采用 Direct SDK，MCP 留作低风险扩展。

## 10. 数据模型建议

至少包含以下实体：

- `feishu_inbox`：`message_id unique`、event_id、chat_id、user_id、raw_hash、received_at、status。
- `conversation_session`：飞书会话、话题、参与者、最近上下文。
- `investigation`：范围、状态、Graph 版本、Prompt 版本、模型版本、创建者。
- `job_attempt`：租约、Worker、重试、心跳、错误和取消。
- `evidence`：查询规范、结果摘要、支持/反对关系、完整性。
- `finding`：事实、推断、置信度、引用的 evidence_id。
- `approval`：actor、action_type、canonical_action_hash、expires_at、used_at。
- `outbox`：`idempotency_key unique`、目标消息、kind、payload、发送状态。
- `feedback`：有帮助、无帮助、根因错误及备注。

四类状态必须分开：

1. `ConversationSession`：飞书对话历史。
2. `Investigation`：一次业务调查。
3. `JobAttempt`：某次执行、租约和重试。
4. `EinoCheckpoint`：框架内部的短期恢复快照。

## 11. 安全与治理

- SLS 默认只读，按 Project/LogStore 做资源级 RAM；不授予 `AliyunLogFullAccess`。
- 优先使用 STS 或实例角色，避免长期 AccessKey。
- 每次工具执行都重新做用户、群、服务和环境 ACL，不只在创建调查时检查。
- 日志内容是不可信数据，不能进入 System Prompt、工具描述或权限判断。
- 采集侧优先脱敏；发送模型前再次按字段策略脱敏。
- App Secret、模型 Key 和阿里云凭证进入密钥系统，禁止输出到日志。
- 原始日志尽量不落库；必要快照加密并设置短 TTL。
- 保存 Prompt、Tool Schema、Graph、模型和策略版本，支持历史回放。
- 写操作未来必须经过单次、限时、绑定规范化动作哈希的人工审批。

## 12. 自身可观测性

自定义稳定事件协议：

```text
investigation.started
scope.resolved
workflow.step.started
tool.started
tool.completed
evidence.recorded
hypothesis.updated
investigation.waiting_input
investigation.completed
investigation.failed
notification.sent
```

至少监控：

- 飞书 WS ready/reconnecting/disconnected。
- 入口 3 秒处理时延、重复率和 Inbox 积压。
- Job 排队、租约失效、重试、取消和执行时长。
- 每个 Graph 节点、模型和 Tool 的耗时与错误率。
- Token、SLS 扫描量、查询次数和单次调查成本。
- SLS Incomplete/截断率、权限拒绝和预算拒绝。
- 飞书 429、发送失败和 Outbox 积压。

## 13. 分阶段开发计划

以下周期按 2 名 Go 后端、平台和测试兼职估算，总体约 8～12 周。已有数据库、密钥系统、SLS 资源目录或 MQ 时可以缩短。每个里程碑独立验收，完成后再进入下一阶段。

### M0：技术验证与选型记录，3～5 天

预期结果：

- 证明 Go、飞书、Eino 和 SLS 的关键链路可行，并形成正式 ADR。

范围：

- 固定 Eino `v0.9.14`。
- 飞书长连接收消息，创建数据库任务。
- Mock SLS Tool 和一条“解析 → 查询 → 总结”的固定 Graph。
- Eino SDK Tool 与阿里云可观测 MCP 的最小对比。
- 验证中断、取消、Worker 重领和版本隔离。

明确不做：

- 真实事故诊断质量。
- 完整卡片、知识库和生产权限体系。

进入条件：

- 飞书自建应用、可调用模型和测试 SLS Project/LogStore。

验收标准：

- 飞书 Handler 在 3 秒内结束。
- 同一 `message_id` 重放 10 次只创建一个调查。
- Worker 强制退出后，任务能被重新领取。
- Mock Graph 能完成、取消并从业务步骤恢复。
- Eino 类型只存在于编排适配模块。
- 输出 Eino 采用决策，以及 SDK/MCP 的对比记录。

### M1：只读查询底座，1～2 周

预期结果：

- 即使不用 Agent，也能安全、稳定、可审计地查询 SLS。

范围：

- 服务资源目录和 ACL。
- `QuerySpec`、Schema-aware 校验和 SLS Go SDK。
- 查询预算、超时、并发、分页、脱敏和审计。
- Durable Job、租约、Inbox 和 Outbox。

明确不做：

- 自主调查和根因判断。
- Trace、发布和跨服务关联。

进入条件：

- SLS 索引/字段字典、RAM/STS 角色和服务资源映射。

验收标准：

- 预定义的 20 个查询用例返回正确结构。
- 不存在的字段在调用 SLS 前被拒绝。
- 越权 Project/LogStore 请求全部拒绝。
- 时间窗、行数、并发和预算均可限制。
- 每次查询关联调查、用户、QuerySpec、结果完整性和成本摘要。
- 日志中的指令文本不能改变工具权限。

### M2：首条端到端调查工作流，1～2 周

预期结果：

- 在飞书完成“错误突增调查”的垂直闭环。

范围：

- Eino 结构化意图提取。
- 固定错误突增 Graph。
- 趋势、基线、异常维度和少量上下文。
- 飞书阶段进度、补参数和停止调查。

明确不做：

- 自动根因处置。
- 多 Agent 和通用调查平台。

进入条件：

- M1 完成，准备至少 10 个真实或脱敏案例。

验收标准：

- 一句自然语言可以创建并完成调查。
- 缺少服务、环境或时间时进入 `WAITING_INPUT`，不自行猜测。
- 每条事实都有 Evidence 引用。
- 查询不完整时明确显示“数据不足”，不输出确定根因。
- 重试不会重复计费执行同一步查询或重复发送最终卡片。

建议在 M2 后先进行小范围试用，验证真实价值，再决定是否扩展。

### M3：证据、反证与变更关联，约 2 周

预期结果：

- 从“会查日志”升级为“结论可复核的调查助手”。

范围：

- 完整 Evidence Ledger。
- 当前/基线、新旧版本、异常/正常实例对照。
- 假设、支持证据、反证和置信度约束。
- 查询一键复跑和发布变更时间线。

明确不做：

- 跨服务复杂因果图。
- 自动写操作。

进入条件：

- M2 完成，日志包含可用的版本、实例、错误码或 Trace 字段。

验收标准：

- 关键结论证据覆盖率 100%。
- 同一报告可由保存的 QuerySpec 重放。
- 报告明确区分事实、推断、反证和待确认项。
- 每次调查至少主动检查一个反例。
- 所有数字都能从确定性分析结果定位来源。

### M4：长任务、审批与故障恢复，1～2 周

预期结果：

- 系统可以稳定灰度给多个研发团队。

范围：

- Worker lease、心跳、重领、步骤幂等和取消。
- 业务 Checkpoint 与短期 Eino Checkpoint。
- 飞书审批卡片、单次限时 Approval 和 Notification Outbox。
- 查询范围扩大审批；仍保持生产只读。

明确不做：

- 自动回滚、重启或改配置。

进入条件：

- M3 完成，明确敏感查询范围和审批人映射。

验收标准：

- 在每个 Graph 步骤强杀 Worker，任务都能继续或安全重做。
- 同一步骤不会重复产生收费查询或外部副作用。
- 审批动作参数被篡改、过期、重复点击或非审批人点击时均拒绝。
- 取消后不再启动新查询。
- 飞书发送失败可以补发，且不会展示两份最终报告。

### M5：评测、安全与小流量上线，1～2 周

预期结果：

- 用数据判断 Agent 是否真的提升故障调查效率。

范围：

- 历史事故回放集。
- Prompt、Tool Schema、Graph 和模型版本化。
- 越权、提示注入、敏感数据和故障注入测试。
- Token、SLS 扫描量、延迟、失败率和用户反馈。
- 灰度名单、预算熔断和模型降级。

明确不做：

- 全公司无门槛开放。

进入条件：

- 至少 20～30 个已确认根因的历史事故。
- 安全团队确认哪些日志字段允许进入模型。

验收标准：

- 越权查询成功数为 0。
- 未授权敏感原文发送到外部模型的次数为 0。
- 所有高置信结论都有 Evidence。
- 形成 Top-1/Top-3 根因命中率、平均耗时和单次成本基线。
- 模型不可用时仍能返回规则化基础调查报告。

### M6：亮点能力，按价值逐项增加

建议优先级：

1. 发布与配置变更时间线。
2. Trace 上下文还原。
3. 跨服务异常传播链。
4. 历史相似事故和 Runbook 推荐。
5. SLS 告警触发的预调查。
6. 日志质量医生：索引、字段、采集延迟和模板漂移诊断。
7. 经人工审批的低风险自动化处置。

每项能力先实现为独立 Graph Tool，并独立评测价值；不要因为增加一个场景就增加一个子 Agent。

## 14. 测试策略

- 纯函数单元测试：QuerySpec 校验、权限、预算、时间窗、统计和报告装配。
- Tool 契约测试：模型产生的所有 Tool 参数都经过 Schema 和策略验证。
- 录制回放测试：使用脱敏 SLS 响应，避免 CI 依赖线上环境和重复费用。
- 故障注入：SLS 超时、Incomplete、429、模型失败、飞书失败和 Worker 强杀。
- 幂等测试：重复入站、重复任务领取、重复卡片点击和重复 Outbox 发送。
- 安全测试：日志提示注入、字段越权、Project 越权、敏感字段外泄。
- 离线评测：历史事故的根因命中、证据覆盖、错误置信和成本。
- 灰度评测：用户采纳率、人工节省时间、无帮助与错误根因反馈。

## 15. 关键 ADR

首批需要固化的架构决策：

- ADR-001：采用 Eino `v0.9.14`，隔离在编排适配层。
- ADR-002：单 Agent + 确定性 GraphTool，不上 Multi-Agent。
- ADR-003：飞书企业自建应用 + WebSocket 长连接，Webhook 为备选适配器。
- ADR-004：消息按 `message_id` 持久化去重，使用 Inbox/Outbox。
- ADR-005：生产核心查询优先 SLS Go SDK，MCP 作为 PoC/扩展。
- ADR-006：模型不能直接执行自由 SQL/SPL，只提交受约束 QuerySpec。
- ADR-007：数据库 Investigation/Evidence 是事实来源，Eino Checkpoint 不是。
- ADR-008：MVP 保持只读，写操作必须后置并引入人工审批。

## 16. 开始开发前需要确认的问题

以下问题不会阻止 M0，但需要在进入 M1 前确定：

- 首个试点服务、环境和 SLS Project/LogStore 是哪些？
- 公司当前标准关系型数据库、MQ 和密钥系统是什么？
- 模型选择、部署区域、数据出域边界和日志脱敏要求是什么？
- 飞书使用单聊、群 `@`，还是两者都开放？首批群和用户白名单是谁？
- 是否已有服务目录、CMDB、发布记录和负责人映射？
- 哪些字段被视为敏感，原始证据允许保存多久？
- 是否有 10 个 MVP 案例和 20～30 个后续评测事故？

## 17. 方案验收清单

- [ ] Eino 使用边界、版本策略和替换接口已经评审。
- [ ] 飞书应用类型、长连接、最小权限和去重键已经确认。
- [ ] SLS SDK 与 MCP 的角色分工已经确认。
- [ ] 首个场景明确为“错误突增调查”。
- [ ] M0～M5 每阶段都有独立结果、非目标、依赖和验收标准。
- [ ] 安全、费用、Incomplete 结果和提示注入已进入架构约束。
- [ ] 业务状态、执行状态、会话状态和 Eino Checkpoint 已分离。
- [ ] 证据链和确定性计算优先于模型自由推断。

## 18. 官方参考资料

- [Eino GitHub](https://github.com/cloudwego/eino)
- [Eino Releases](https://github.com/cloudwego/eino/releases)
- [Eino：Agent or Graph](https://www.cloudwego.io/docs/eino/overview/graph_or_agent/)
- [Eino ADK](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/)
- [Eino Interrupt 与 Checkpoint](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/checkpoint_interrupt/)
- [Eino Callback](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/callback_manual/)
- [飞书 Go SDK](https://github.com/larksuite/oapi-sdk-go)
- [飞书 Go SDK Channel v3.9.10](https://github.com/larksuite/oapi-sdk-go/blob/v3.9.10/doc/channel.md)
- [飞书长连接接收事件](https://open.feishu.cn/document/server-docs/event-subscription-guide/event-subscription-configure-/request-url-configuration-case?lang=zh-CN)
- [飞书消息事件](https://open.feishu.cn/document/server-docs/im-v1/message/events/receive?lang=zh-CN)
- [飞书回复消息 API](https://open.feishu.cn/document/server-docs/im-v1/message/reply?lang=zh-CN)
- [飞书 CardKit 流式更新](https://open.feishu.cn/document/cardkit-v1/streaming-updates-openapi-overview)
- [阿里云 SLS SDK 概览](https://help.aliyun.com/en/sls/developer-reference/overview-of-log-service-sdk)
- [阿里云可观测 MCP Server](https://github.com/aliyun/alibabacloud-observability-mcp-server)
- [SLS 查询和分析限制](https://www.alibabacloud.com/help/en/sls/query-and-analysis1)
- [SLS RAM 权限配置](https://www.alibabacloud.com/help/en/sls/log-service-ram-access-control-permissions-configuration)


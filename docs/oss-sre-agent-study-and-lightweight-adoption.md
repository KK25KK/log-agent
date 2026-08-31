# 开源 SRE Agent 对比与 Log Agent 轻量演进报告

> 调研日期：2026-08-31
>
> 调研对象：OpenSRE、HolmesGPT、rca-agent、K8sGPT、RunLore，以及当前 `D:\日志agent` 代码
>
> 文档目的：识别真正适合本项目的做法，同时明确哪些能力不应照搬。本文是技术决策建议，不改变现有 `spec.md` 和 `roadmap.md` 的行为契约。

## 1. 一句话结论

当前 Log Agent 的方向是合理的：继续保持“固定调查流程 + 受治理查询 + Evidence 证据链 + 确定性判断 + 受约束 LLM 摘要”，不要转型成一个连接几十种工具、允许模型自由规划和自动执行操作的通用 SRE 平台。

五个开源项目最值得借鉴的不是它们的完整架构，而是五个小机制：

1. K8sGPT 的“确定性分析器先产出结果，LLM 只解释”；
2. OpenSRE 的调查预算、上下文预算和停滞保护；
3. HolmesGPT 的数据源侧聚合、工具输出上限和适配层纪律；
4. rca-agent 的“受害服务与上游根因分离”以及轻量服务依赖图；
5. RunLore 的人工审核知识闭环和只能降低、不能抬高置信度的独立复核。

这些能力应按真实试点暴露的问题逐项引入，而不是一次性建设完整平台。

## 2. 当前 Log Agent 是怎么实现的

### 2.1 当前定位

本项目不是一个能自主操作生产系统的通用 Agent，而是一个面向阿里云 SLS 日志排障的受控调查内核：

- 飞书负责接收排障请求和返回卡片；
- Eino 只编排固定的 `error_analysis_v2` 流程；
- 查询治理层决定允许查询哪个资源、哪个时间窗和哪些固定聚合；
- SLS 或 Mock SLS 只负责执行已经过治理的查询；
- Evidence 保存查询身份、时间窗、完整性和聚合结果；
- M2/M3 逻辑负责异常判断、变更关联、支持证据与反证；
- LLM 只把已验证事实组织成摘要，不能创造查询结果、证据或执行动作；
- SQLite 和评测模块负责恢复、审计、额度、回放和离线门禁。

### 2.2 主链路

```mermaid
flowchart LR
    A[飞书命令] --> B[Inbox 与任务状态]
    B --> C[Worker]
    C --> D[你在这里<br/>Eino 固定 Graph]
    D --> E[查询治理闸门]
    E --> F[Mock 或真实 SLS]
    F --> G[Evidence 证据]
    G --> H[M2 确定性报告]
    H --> I[M3 支持与反证]
    I --> J[可选时间线与 SOP]
    J --> K[证据约束摘要]
    K --> L[飞书卡片]

    style D fill:#dbeafe,stroke:#2563eb,stroke-width:2px
    style E fill:#fef3c7,stroke:#d97706
    style G fill:#dcfce7,stroke:#16a34a,stroke-width:2px
```

“你在这里”表示 Agent 的核心不是一个可以任意决定工具和 SQL 的模型，而是固定 Graph 中受契约约束的调查步骤。

### 2.3 分层和代码入口

| 层 | 当前职责 | 主要代码 |
| --- | --- | --- |
| 接收与组装 | CLI、飞书入口、依赖装配、Mock/真实适配器选择 | [`cmd/logagent/main.go`](../cmd/logagent/main.go)、[`cmd/logagent/sls.go`](../cmd/logagent/sls.go) |
| 应用调度 | Inbox 消费、任务状态、调查执行、校验和投递 | [`internal/application/worker.go`](../internal/application/worker.go)、[`internal/application/intake.go`](../internal/application/intake.go) |
| Agent 编排 | Eino 固定图、节点顺序、状态传递和 Trace | [`internal/adapters/eino/engine.go`](../internal/adapters/eino/engine.go) |
| 查询治理 | ResourceCatalog、ACL、Schema、预算、审计、Checkpoint | [`internal/application/query/gateway.go`](../internal/application/query/gateway.go)、[`internal/application/checkpoint_executor.go`](../internal/application/checkpoint_executor.go) |
| 数据源适配 | 阿里云 SLS 适配器和离线 Mock | [`internal/adapters/aliyunsls/backend.go`](../internal/adapters/aliyunsls/backend.go)、[`internal/adapters/slsmock/backend.go`](../internal/adapters/slsmock/backend.go) |
| 证据与判断 | Query/Evidence、CauseAnalysis、IncidentTimeline、Summary 等领域合同 | [`internal/domain/query.go`](../internal/domain/query.go)、[`internal/domain/cause.go`](../internal/domain/cause.go)、[`internal/domain/incident_timeline.go`](../internal/domain/incident_timeline.go) |
| 可选增强 | 指标/Trace 时间线、受治理 SOP、LLM 摘要 | [`internal/adapters/signalmock/source.go`](../internal/adapters/signalmock/source.go)、[`internal/application/runbook.go`](../internal/application/runbook.go)、[`internal/application/summary.go`](../internal/application/summary.go) |
| 稳定性治理 | SQLite 状态、投递重试/死信、租户额度、LLM 额度 | [`internal/adapters/sqlite`](../internal/adapters/sqlite)、[`internal/application/delivery.go`](../internal/application/delivery.go)、[`internal/application/quota_executor.go`](../internal/application/quota_executor.go) |
| 离线质量 | 合成数据评测、Agent Trace、回放比较、反馈账本、灰度演练 | [`internal/evaluation`](../internal/evaluation) |
| 架构边界 | 应用层依赖的端口，不让 Eino、飞书和云 SDK 进入领域层 | [`internal/ports/ports.go`](../internal/ports/ports.go) |

### 2.4 当前能力的真实性边界

| 能力 | 当前状态 | 能证明什么 | 不能证明什么 |
| --- | --- | --- | --- |
| 固定调查主链 | 已实现并有单元/集成测试 | 固定 Mock 输入下能完成调查并生成证据报告 | 真实故障根因准确率 |
| SLS 查询治理 | 代码已实现，默认仍可使用 Mock | 查询模板、资源选择、ACL、预算和审计边界成立 | 真实 RAM 权限、网络、索引和生产数据兼容性 |
| 飞书入口与投递 | 适配器、Mock E2E、重试/死信已具备 | 消息解析、幂等和本地可靠性合同 | 真实飞书应用权限、回调公网链路和线上投递稳定性 |
| M2 异常判断 | 已实现 | 当前窗与基线窗的错误量和模式对比 | 业务因果关系已经被证明 |
| M3 变更关联与反证 | 已实现，变更源可 Mock | 能区分支持证据、反证和数据不足 | 关联一定等于根因 |
| 指标/Trace 时间线 | 仅 `signalmock` | 跨信号 Schema、校验和展示链路 | 真实监控/Trace 平台已接通 |
| SOP 指引 | 仅 `runbookmock`，只允许人工核查 | 受治理内容能绑定 Evidence 并安全展示 | 企业知识库已接通或 Agent 能自动执行 SOP |
| LLM 摘要 | Mock 默认；已有火山方舟适配器 | 输入投影、引用、降级和额度合同 | 真实模型效果、费用和生产稳定性已经验证 |
| 离线评测与回放 | 已实现，全合成数据 | 回归、兼容性和安全门禁 | 生产准确率或灰度批准 |

## 3. 五个开源 Agent 各自的优点

### 3.1 OpenSRE：调查过程工程化

OpenSRE 的亮点是把调查拆成可观察的阶段：接入并分类问题、规划证据、收集证据、诊断、交付；同时设置工具调用上限、上下文预算和停滞保护。它还用包依赖规则约束网关、工具、核心和平台层之间的方向。

适合本项目借鉴：

- 为每次调查记录“计划使用哪些证据”和“实际获得哪些证据”；
- 延续现有 QueryBudget，并补充 Agent 输出大小、节点执行次数等预算；
- 对重复调用同一调查步骤且没有新增 Evidence 的情况主动终止；
- 用测试继续约束 Eino、云 SDK 和飞书 SDK 的适配层边界。

不适合现在照搬：

- 60 多种集成和通用 SRE 平台定位；
- 完全动态的 ReAct 调查流程；
- Python 核心或多入口平台改造；
- 在公开 Alpha 阶段的能力描述基础上推断生产准确率。

判断：借鉴“有限调查”的治理机制，不引入它的通用平台规模。

来源：[OpenSRE README](https://github.com/Tracer-Cloud/opensre)、[架构说明](https://github.com/Tracer-Cloud/opensre/blob/main/docs/ARCHITECTURE.md)、[调查流水线](https://github.com/Tracer-Cloud/opensre/blob/main/docs/investigation-pipeline-architecture.md)。

### 3.2 HolmesGPT：工具输出治理和数据源侧计算

HolmesGPT 的优势是工具生态丰富，但对本项目更有价值的是其工具工程经验：尽量在数据源侧完成过滤和聚合，返回结构化、可遍历的数据，对工具输出和子进程资源设置边界，再把小而相关的结果交给模型。

适合本项目借鉴：

- 保持 SLS 侧聚合，不把大量原始日志直接喂给 LLM；
- 给每类查询结果设置记录数和字节数双上限；
- 为适配器统一记录延迟、返回大小、截断状态和成本；
- 真实 SLS 接入后，优先验证 Provider 限流和超时分类。

不适合现在照搬：

- 大型 Toolset 市场；
- Shell、kubectl 等高权限自由工具；
- 允许模型自由选择任意工具和参数；
- Operator 和自动修复能力。

判断：只吸收“让工具返回更小、更确定、更可治理的数据”。

来源：[HolmesGPT README](https://github.com/HolmesGPT/holmesgpt/blob/master/README.md)、[为什么使用 HolmesGPT](https://github.com/HolmesGPT/holmesgpt/blob/master/docs/why-holmesgpt.md)、[工具执行安全](https://github.com/HolmesGPT/holmesgpt/blob/master/docs/data-sources/tool-execution-safety.md)。

### 3.3 rca-agent：先相关，再假设；区分受害者与源头

rca-agent 的思路是先按服务依赖扩展调查范围，并行获取指标、日志和 Trace，做标准化、时间相关和受害服务识别，再让 LLM 提出和验证假设。它特别强调许多 RCA 数据处理步骤应该是确定性代码，而不是都交给 AI。

适合本项目借鉴：

- 当真实指标和 Trace 接入后，使用一份最小服务依赖清单；
- 报告中区分“最先告警/受影响服务”和“可能的上游来源”；
- 先完成跨信号时间相关，再产生根因候选；
- 多信号数据不完整时明确降级为 `INCONCLUSIVE`。

不适合现在照搬：

- LangGraph、MCP、Chroma 等整套技术栈；
- 一开始建设复杂自动拓扑平台；
- 在只有日志 Mock 时假装已经具备完整多信号 RCA；
- 将研究原型视为成熟生产基座。

判断：服务拓扑是后续真实跨信号接入的一个小数据结构，不是当前必须建设的平台。

来源：[rca-agent README](https://github.com/soul-bits/rca-agent)、[项目实现说明](https://github.com/soul-bits/rca-agent/blob/main/CLAUDE.md)、[调查图实现](https://github.com/soul-bits/rca-agent/blob/main/agent/graph.py)。

### 3.4 K8sGPT：确定性分析器与可选解释层

K8sGPT 的核心模式很适合 Go 项目：每个 Analyzer 负责检查一类资源并输出确定性问题，注册表选择要运行的分析器，LLM 的 `explain` 是后置可选能力。这样即使模型不可用，基础诊断仍可运行。

适合本项目借鉴：

- 将未来新增的调查类型做成小型静态 Analyzer 接口和编译期注册表；
- 每个分析器声明版本、输入 Evidence 类型、预算和产出类型；
- 汇总每个分析器的耗时、命中数、数据不足数和失败数；
- LLM 摘要继续保持可降级，不能替代分析器结论。

不适合现在照搬：

- Kubernetes 专用检查集合；
- 任意第三方插件生态；
- 为扩展性提前引入动态加载或 MCP；
- 把未经治理的自由文本作为高置信 Evidence。

判断：这是最适合本项目的代码组织借鉴，但应在出现至少 3 类稳定调查流程后再抽象，避免只有一个流程时过早设计插件框架。

来源：[K8sGPT README](https://github.com/k8sgpt-ai/k8sgpt/blob/main/README.md)、[Analyzer 注册与接口](https://github.com/k8sgpt-ai/k8sgpt/blob/main/pkg/analyzer/analyzer.go)、[结果类型](https://github.com/k8sgpt-ai/k8sgpt/blob/main/pkg/common/types.go)。

### 3.5 RunLore：经过人工审核的知识学习闭环

RunLore 把“调查”和“记住经验”连起来：调查结果经过人类审核后进入知识库，后续召回还会根据实际结果调整权重。它的独立复核可以保留、降低或拒绝结论，但不能反过来抬高置信度；报告也明确表达未解决项、已排除项和数据缺口。

适合本项目借鉴：

- 只把人工确认过的事故结论转成候选知识；
- 知识采用 `DRAFT -> APPROVED -> RETIRED` 的简单生命周期；
- 后续调查显式展示 `RuledOut`、`DataGaps` 和未解决项；
- 如果引入 LLM 复核，只允许 `KEEP / DOWNGRADE / REJECT`，不允许提高置信度；
- 知识命中但后来被证明无效时降低权重或退役。

不适合现在照搬：

- 开放式 ReAct、动作阶梯和自动执行；
- 复杂的知识图谱、向量库和 GitHub PR 流水线；
- 多 MCP 工具的通用平台；
- 把预 1.0 单维护者项目的公开愿景当成已验证效果。其官方评测页在本次调研时尚未发布 nightly 结果，因此不能据此引用准确率。

判断：先用现有 SQLite 或 Git 管理少量人工审核知识，不建设向量库和自动学习平台。

来源：[RunLore README](https://github.com/Smana/runlore/blob/a39a0e52c1123815809c4679fbafa319da08751a/README.md)、[设计原则](https://github.com/Smana/runlore/blob/a39a0e52c1123815809c4679fbafa319da08751a/website/content/docs/concepts/design.md)、[学习闭环](https://github.com/Smana/runlore/blob/a39a0e52c1123815809c4679fbafa319da08751a/website/content/docs/concepts/learning-loop.md)、[复核实现](https://github.com/Smana/runlore/blob/a39a0e52c1123815809c4679fbafa319da08751a/internal/investigate/verify.go)、[评测状态](https://github.com/Smana/runlore/blob/a39a0e52c1123815809c4679fbafa319da08751a/website/content/eval.md)。

## 4. 横向比较：什么值得拿，什么不要拿

| 项目 | 最强项 | 对本项目的轻量借鉴 | 当前不做 | 优先级 |
| --- | --- | --- | --- | --- |
| OpenSRE | 调查阶段、预算和停滞治理 | Evidence 计划、节点/上下文预算、无新增证据终止 | 通用平台和自由 ReAct | 高 |
| HolmesGPT | 大量数据源的工具工程 | 数据源侧聚合、输出上限、适配器指标 | 工具市场和高权限工具 | 高 |
| rca-agent | 服务拓扑与跨信号 RCA | 最小依赖图、受害者/来源分离 | 复杂拓扑和完整 MCP/RAG 栈 | 中，等真实指标/Trace |
| K8sGPT | 确定性 Analyzer 模式 | 静态分析器接口、注册表、每类统计 | 动态插件生态 | 高，但不应过早抽象 |
| RunLore | 人审知识闭环和反向复核 | 审核后入库、降权/退役、只能降置信度 | 自动行动、向量/图数据库 | 中，等真实事故样本 |

## 5. 与当前实现的逐项对照

| 外部优秀做法 | 当前项目已经具备 | 实际缺口 | 建议 |
| --- | --- | --- | --- |
| 确定性分析优先于 LLM | M2/M3 判断和 Evidence 都由 Go 代码产生；LLM 仅摘要 | 多调查类型尚未统一为 Analyzer 合同 | 保持现状；达到抽象触发条件后再提取静态接口 |
| 查询和工具必须受预算约束 | QueryBudget、额度、Checkpoint、截断/不完整 fail-closed 已具备 | 缺少统一的结果字节数、单节点次数和“无新增 Evidence”统计 | 在真实 SLS 试点后按实测补齐，不引入 ReAct |
| 数据源侧过滤和聚合 | 当前 SLS 设计只取固定聚合，不把原始日志直接交给模型 | 真实 SLS 的分页、超时、限流和返回大小尚未验证 | 作为真实 SLS 接入验收项 |
| 区分支持、反证和未知 | CauseAnalysis 已有支持测试、反证测试和 `INCONCLUSIVE` | 飞书报告对 `RuledOut`、`DataGaps` 可进一步结构化展示 | 小幅增强报告投影，不改核心推理 |
| 跨信号和服务拓扑 | 已定义 IncidentTimeline 和 SignalSource，但目前只有 Mock | 真实指标/Trace、服务依赖数据均未接入 | 等真实数据源明确后只加最小依赖表 |
| 人工审核知识学习 | 已有 Mock Runbook、反馈账本和灰度演练 | 没有真实企业知识和事故结论生命周期 | 先做人工审批的轻量知识目录，不上向量库 |
| 独立 LLM 复核 | 当前 LLM 摘要不能产生新证据，已有安全门禁 | 没有独立 Reviewer | 仅在真实误判样本足够时增加，且只能降级 |
| 自动修复 | 当前明确 `HUMAN_REVIEW_ONLY` | 无需补齐，这是安全边界 | 继续不做，除非未来有独立授权、审计和回滚项目 |

## 6. 建议的轻量演进路线

### 6.1 P0：先完成最小真实试点，不新增框架

目标：证明现有受控主链能连接真实系统，而不是继续扩充离线能力。

范围：

- 接入一个只读 RAM 身份、一个 SLS Project/Logstore 和一个测试服务；
- 接入一个飞书测试群或测试应用；
- 用火山方舟完成摘要 Smoke Test，但保留 Mock 和降级路径；
- 真实变更源、指标/Trace、企业 SOP 如果暂时没有稳定接口，继续保持 Mock 并在报告中标记。

验收建议：

- 真实查询的资源、时间窗、Query ID、完整性和截断状态全部进入 Evidence；
- 未授权资源、超预算、Provider 超时和不完整响应均 fail-closed；
- LLM 不可用时，确定性报告仍能送达；
- 任何结论都能回指 Evidence ID；
- 明确记录成本、P95 延迟、失败分类和人工复核结果，不声称生产准确率。

### 6.2 P1：只有调查类型变多时，提取静态 Analyzer

触发条件建议：出现至少 3 个稳定且边界不同的调查类型，例如错误突增、延迟突增、依赖故障，并且现有 Graph 已出现明显重复代码。

最小合同可以只包含：

```go
type Analyzer interface {
    Name() string
    Version() string
    Analyze(ctx context.Context, evidence EvidenceBundle) (AnalysisResult, error)
}
```

约束：

- 编译期静态注册，不做动态插件；
- Analyzer 不直接访问 SLS、飞书或 LLM；
- 输入只能是已校验 Evidence；
- 输出必须区分结论、已排除项和数据缺口；
- 原 `error_analysis_v2` 先作为第一个实现迁移，行为快照必须保持兼容。

### 6.3 P2：有真实事故积累后，再做轻量知识闭环

触发条件建议：至少积累一批经过人工复核的真实调查记录，并确认“重复问题无法复用经验”已经是主要耗时来源。

最小实现：

- 使用 SQLite 表或仓库内受审 Markdown，而不是向量数据库；
- 仅保存脱敏后的服务、错误模式、适用条件、排除条件、人工结论和 Evidence 引用；
- 状态仅为 `DRAFT / APPROVED / RETIRED`；
- 只有 `APPROVED` 内容能通过 `RunbookSource` 返回；
- 每次命中后记录人工反馈，错误知识可以降权或退役。

### 6.4 P3：误判数据足够后，再评估独立 Reviewer

Reviewer 的职责只是在最终输出前检查：结论是否被 Evidence 支持、是否忽略反证、是否应降级为数据不足。

安全约束：

- 只能输出 `KEEP / DOWNGRADE / REJECT`；
- 不能提高置信度；
- 不能新增 Evidence、SQL、资源、SOP 或行动指令；
- 失败时保留确定性报告并标记复核不可用；
- 必须使用专门的离线误判数据集验证，不能与摘要模型自评混为一谈。

## 7. 现在明确不做的内容

下面这些不是“永远不做”，而是目前没有足够业务证据支持其复杂度：

- 不建设 60+ 数据源的通用集成平台；
- 不允许模型自由生成 SLS SQL、Project、Logstore 或任意时间窗；
- 不引入开放式 ReAct、多 Agent 协作和无限工具循环；
- 不建设动态插件市场或通用 MCP 工具层；
- 不引入向量数据库、知识图谱和自动拓扑发现；
- 不提供 Shell、kubectl 等高权限工具；
- 不自动执行扩缩容、回滚、重启或配置变更；
- 不因 Mock E2E 或合成评测通过而宣称生产准确率。

## 8. 最终建议清单

### 现在做

1. 按 [`m6-real-system-entry-guide.md`](m6-real-system-entry-guide.md) 完成最小真实 SLS、飞书和火山方舟联调；
2. 为真实适配器补齐返回字节数、截断、延迟、限流和成本观测；
3. 让飞书报告更明确展示已排除项和数据缺口；
4. 继续保持固定 Graph、受治理查询和 LLM 可降级。

### 有条件再做

1. 出现 3 类稳定调查流程后，引入静态 Analyzer 接口；
2. 真实指标/Trace 可用后，引入最小服务依赖表和受害者/来源区分；
3. 积累足够人工复核事故后，引入轻量知识生命周期；
4. 出现可量化的误判问题后，引入只能降低置信度的 Reviewer。

### 暂时不做

通用 SRE 平台、自由 ReAct、多 Agent、动态插件、复杂 RAG、自动修复和高权限工具。

## 9. 决策总结

当前 Agent 最有价值的差异化不是“会调用多少工具”，而是“每个判断能否被受治理的 Evidence 复核”。开源项目用于补充工程细节，而不应改变这一中心：

```text
真实系统接入优先级 > 证据完整性 > 确定性分析 > 可解释摘要 > 扩展性框架
```

因此，下一阶段最合理的路线仍然是小范围真实试点。只有真实数据证明某个瓶颈存在，才从上述项目中拿一个足够小的机制解决它。

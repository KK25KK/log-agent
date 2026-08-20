# Log Agent 分阶段路线图

原则：每个阶段都交付一条可以独立验收的业务能力，不一次性堆出“大而全”的 Agent 平台。

## M0：技术竖切（已完成）

目标：证明 Go、飞书、任务状态、Eino 和证据模型可以连成一条可靠链路。

交付：

- 飞书消息进入持久化 Inbox。
- 重复消息只产生一个调查任务。
- Worker 通过租约执行 Eino 固定 Graph。
- Mock SLS 返回当前窗口和基线窗口数据。
- 报告中的结论必须引用持久化 Evidence。
- 不完整证据不能生成确定性结论。

不包含：真实 SLS、LLM、飞书结果卡、自动处置。

## M1：只读查询底座（代码完成，真实试点待联调）

目标：让 Agent 能够在受控范围内安全查询真实阿里云 SLS。

交付：

- Project、LogStore、索引 Schema 资源目录。
- 真实 SLS 只读适配器。
- QuerySpec 白名单与字段校验。
- 用户到服务、环境、LogStore 的资源 ACL。
- 执行前限制时间窗、固定模板调用数、返回行数、单进程并发和超时；执行后用处理字节数作为费用代理门禁。
- 对 `Complete/Incomplete`、截断和超时的统一处理。
- 查询审计与敏感字段脱敏。

验收：指定试点服务可以通过固定模板查询真实数据；越权和可预判的超预算查询在执行前被拒绝，实际扫描量超限时结果被降级为证据不足。

## M2：错误突增调查闭环（代码完成，真实飞书/SLS 试点待联调）

目标：完成第一个具备内部试点条件的调查场景。

交付：

- 当前窗口与基线窗口对比。
- 候选新增错误模式、错误占比和实例分布分析；只有基线分布穷尽时才确认“相对该基线新增”。
- 飞书接单回执、进度卡和最终报告。
- 查看证据、扩大时间窗、取消和重新运行按钮。
- 预留可选 LLM 解释边界，但它不是 M2 验收项，也不能决定查询权限。

验收：用户在飞书发起调查后，可以看到进度、结论、证据和下一步建议。

离线验收已经覆盖四次固定聚合、前后计数一致性门禁、当前/基线分析、数据不足降级、卡片事件租约、同卡更新、请求者鉴权、按钮回放幂等和窗口上限。仓库未包含真实 SLS 资源与飞书应用凭据，因此真实客户端端到端联调仍是部署前置项。

## M3：证据与反证首个纵向切片（代码与离线验收完成）

目标：从“发现相关性”提升到“可验证、可反驳的变更关联候选”，但不把相关性宣传成根因确认。

交付：

- 复用 M2 当前/基线证据，不新增 SLS 查询、字段和费用预算。
- 版本化只读 Change Catalog，关联发布/配置、负责人和受影响实例。
- 每个候选保存支持测试、反证测试、未知项、Evidence/Change 引用和置信度来源。
- 完整影响范围与完整实例分布零交集可以反证；Top-K 未穷尽、标签脱敏、影响范围不完整或多变更混杂只能判为不确定。
- 飞书报告与证据页展示候选状态和“关联不等于因果”限制。

验收：每个变更关联候选同时展示支持证据、反证结果、未知项和启发式置信度来源；变更源不可用不能破坏 M2 报告；一次调查仍只有两个逻辑 SLS 观察、最多八次固定 Provider 调用。

主体代码、全仓离线测试、静态检查、Mock Demo 和独立复核已经完成，`spec.md` 的 M3 离线验收项已收口；真实 SLS、飞书客户端和发布/配置平台仍未联调。实现与限制见 [`m3-change-correlation-evidence.md`](m3-change-correlation-evidence.md)。

### M3 增强项（未实现）

- SLS `version_field`、版本分布、首次出现时间和版本前后固定查询模板。
- 真实发布平台、配置中心和 CMDB 连接器。
- 通过 `trace_id` 关联日志、Trace 和指标。
- 更完整的故障时间线、Pod/主机元数据与服务拓扑。
- 企业错误码、SOP 和知识检索。

这些能力需要真实数据源、字段契约、权限和成本预算，不能用 Mock 或静态配置冒充已接通。

### 必需能力：LLM 证据摘要（Mock 与适配器代码完成，真实方舟待联调）

- 新增 provider-neutral `ReportSummarizer`，只接收已经通过 Worker 校验的 Finding、Evidence 引用、CauseAnalysis、限制和确定性建议。
- 先用确定性 Mock 完成结构、引用、安全、降级和评测合同，再实现隔离的火山方舟 Adapter。
- 模型只负责生成更易读的现象摘要、可能原因、证据说明和下一步；不能生成查询、改变置信度/原因 verdict、扩大权限或把不确定结论改成确定根因。
- 超时、限流、非法结构、虚构引用或安全校验失败时保留原确定性报告，不让调查失败。
- 真实模型启用后记录模型、Prompt 版本与指纹、Token、耗时和完成状态，但不在 Trace 中记录 Prompt 正文或证据内容。

当前已经完成 provider-neutral 端口、确定性 Mock、Worker 后处理、严格输出/引用门禁、确定性回退、飞书有界展示和隔离的火山方舟 Responses API 适配器。默认 Mock 主链为 0 网络、0 凭据；真实 Key、批准模型、Prompt 审批、费用/留存策略和 opt-in smoke 仍待真实输入。

#### 摘要安全评测切片（代码与离线验收完成，Mock-first）

- 新增独立 `summary-evaluate` 命令，复用现有合成故障 Case、真实确定性 Eino Graph、生产 `SummaryService` 和生产输出校验。
- 固定覆盖正常摘要、Provider 失败、虚构 Evidence/Recommendation、选择非支持候选、危险动作和敏感出站输入。
- 逐 Case 校验原报告不变、引用完整、原因/建议只能来自确定性报告、fallback 正确、敏感输入在 Provider 前阻断，以及调用/Token/网络代理为零或固定值。
- 使用独立数据集和报告 Schema，不改变现有 `evaluate`、`evaluation-replay-v1` 或 B3 的历史比较边界。

本切片只能证明摘要安全合同对合成场景没有回归，不代表真实模型质量、真实 Token 费用或 Prompt 已获批准。

验收：`summary-evaluate` 的 9/9 个合成 Case 通过；生产输出、确定性报告完整性、摘要引用、输入隐私和 fallback 准确率均为 1，8 次预期 Mock Provider 调用完全匹配，敏感输入 Case 在 Provider 前阻断，Token、凭据和外部网络调用均为 0。数据集指纹固定进入报告，失败时命令返回非零退出码。

## M4 开工前：飞书 + SLS 双 Mock 纵向联调（已完成）

目标：在申请真实飞书应用和阿里云资源之前，用可重复的离线命令证明外部边界替换后，业务主链仍能完整运行。

交付：

- Mock 飞书入站复用严格命令解析、可信身份和 SQLite 幂等接单；
- Mock SLS Backend 复用真实资源 ACL、Schema/预算网关、查询审计、当前/基线固定聚合、Eino、Evidence、报告和 M3 变更关联；
- Mock 飞书出站复用正式 Delivery Worker，验证同卡 `Reply -> Patch -> Patch` 生命周期；
- `go run ./cmd/logagent mock-e2e` 不读取凭据、不访问网络，并输出结构化验收摘要。

不包含：真实飞书 WebSocket/OpenAPI、真实 SLS Schema/ACL/查询、真实卡片视觉验收和生产可靠性声明。运行方法与证据边界见 [`local-mock-e2e.md`](local-mock-e2e.md)。

## M4：长任务、审批与恢复（第五期，已启动）

目标：达到生产级的任务可靠性与安全边界。

### M4-A：付费查询恢复边界（代码与离线验收完成）

交付：

- 为 `sls.current`、`sls.baseline` 保存步骤级 Checkpoint、规范化结果，以及绑定资源、Schema、模板、策略和预算的治理输入指纹。
- 已完成窗口在租约恢复后直接复用，不重复访问 Provider。
- 付费查询留下 `STARTED` 后失联时进入 `NEEDS_REVIEW`，默认禁止自动重发。
- Checkpoint 同时受 job attempt、lease owner、租约和步骤指纹 fencing。

验收：在 current 完成、baseline 未完成以及两窗口均完成的崩溃点恢复时，只执行缺失步骤；无法判断云端结果的失联步骤执行零次自动重试。真实文件 SQLite + Worker + Eino 的三类恢复测试、双 Mock、全仓测试和静态检查已经通过；真实进程强杀与云端联调仍属于 M4-C。

### M4-B：投递恢复与租户治理（代码与离线验收完成）

- Provider-neutral 的飞书失败分类和有限退避；付费查询仍沿用 M4-A 的“结果未知不自动重试”。
- 现有事务卡片 Outbox 的追加式尝试审计、运维死信查询和事务安全重放；没有另建重复队列。
- SQLite 持久化租户固定窗额度、调用/扫描成本代理预留与熔断；Checkpoint 复用不重复计量。
- 高风险工具审批状态、职责分离、payload hash 和一次性消费契约；当前没有注册真实高风险执行器，默认仍保持只读。

验收：永久投递错误立即死信，可重试/未知错误有限退避；旧进度不能覆盖新卡片状态；超租户额度在 Provider 前拒绝；未知查询结果保留费用代理；审批不可自批或重复消费。完整实现与边界见 [`m4b-reliability-governance.md`](m4b-reliability-governance.md)。

### M4-C：生产部署验收（等待基础设施输入）

- 生产关系库存储、多实例租约与组织级全局配额。
- 两阶段优雅停机和更及时的跨进程取消。
- 真实审批身份源、高风险工具、备份恢复与故障转移演练。

完整 M4 验收：进程强杀、网络抖动和重复事件不会破坏业务状态；任何无法消除的外部重复窗口都有显式审计和人工决策，而不宣称 exactly-once。

## M5：评测与灰度上线（第六期，已启动）

目标：用可量化指标决定能否扩大使用范围。M5 可以在 M4-B/M4-C 研发期间并行推进，但离线评测不会提升当前生产就绪状态。

### M5-A：合成黄金集离线评测门禁（代码与离线验收完成）

- 使用版本化、严格校验的合成 Fixture 和标签，覆盖突增、无突增、证据不足、变更反证和不确定场景。
- 真实运行当前确定性 Eino Graph；SLS、飞书身份和变更数据全部来自 Mock，不读取凭据、不访问网络。
- 输出结果正确率、意外确定性结论率、生产 Worker 输出校验通过率、Evidence/QuerySpec 绑定准确率、证据引用覆盖率、变更判断一致率、固定调用预算、处理字节成本代理和本机耗时。
- 数据集版本与内容指纹进入报告；未达到工程回归门槛时命令以非零状态退出。

当前内置五个合成 Case 已通过固定门禁：Outcome、Finding、Recommendation Code 与 Evidence 绑定、Query Contract、Evidence 引用和 Cause 判断均完全匹配，意外确定性结论为 0；这仍只是一组离线工程回归证据。

M5-A 的指标只说明代码对受控合成样例没有回归，不是历史真实故障准确率、专家评审结论、生产成本或灰度批准。

### M5-B：Agent 自观测与回放（已启动，Mock-first）

目标：在不接真实平台、不扩大生产声明的前提下，让合成评测的执行过程可追踪、可归档、可比较。首版只覆盖 Engine/评测边界，不冒充飞书入站到投递的跨进程 Trace。

#### B1：事件与版本合同（代码与离线验收完成）

- 框架无关、字段有界的 `RUN / GRAPH_NODE / TOOL` 事件，只记录稳定代码、计数、耗时、哈希和版本指纹。
- Graph、查询模板/策略、原因方法、评测规则、Trace/Replay Schema、执行 Profile 和真实 Prompt 使用情况形成统一版本清单。
- Noop Observer 与线程安全有界 Recorder；遥测不能破坏调查，但事件丢失会让离线门禁失败。
- 真实 Eino 固定节点以及 Mock `sls.current`、`sls.baseline`、`change_source.list` 工具调用形成闭合 Trace，并与现有调用/字节统计核对。

验收：`m5b-agent-trace-gate-v1` 在五个合成 Case 上全部通过，`trace_contract_accuracy=1`；共形成 76 个 `agent-trace-v1` 事件、13 个工具 Span、0 个丢弃事件，并与 10 次逻辑 SLS 观察、40 次 Provider 调用代理、3 次 Change Source 调用和 78,080 processed bytes 一致。执行 Profile 固定为 `SYNTHETIC_MOCK`，不读取凭据、外部网络调用为 0。实现与边界见 [`m5-agent-observability-replay.md`](m5-agent-observability-replay.md)。

#### B2：离线回放历史（代码与离线验收完成）

- 成功和失败评测都保存为 append-only 严格快照，包含报告、版本、Trace、失败 Case 和内容哈希。
- 独立 Evaluation Run Store，不扩展生产 `ports.Store`，也不复用 Query Audit/QueryStep。
- `replay` 命令只用当前二进制和合成 Mock；历史实现仍需旧 Commit 或旧制品。
- `evaluate --snapshot-dir` 保存成功与门禁失败运行；`replay` 严格验证源快照后追加带父引用的新运行。
- 同一 Run 不覆盖；重复写入、未知字段/Schema、内容哈希变化、非法路径和不兼容合成数据边界均 fail closed。

#### B3：趋势比较与反馈闭环（代码与离线验收完成）

- `replay-compare` 比较质量门禁、失败 Case、错误分类、Trace 完整性、工具调用和成本代理；候选删除任何既有 Gate 都记为回归。
- 数据集边界或执行 Profile 不兼容时返回 `INCOMPARABLE`，不输出伪精确差值。
- 回放趋势与失败样例归档为后续真实专家反馈提供接口，但不冒充真实标注。

验收：比较命令只读加载两个已校验快照，不执行 Graph 或 Provider；兼容运行输出显式版本变化、固定质量/成本/工具/Trace/时延观测差值、门禁变化及 recovered/newly-failed/still-failed Case，不兼容运行只输出稳定原因码并以非零状态结束。当前验证数据全部为仓库内置合成 Mock，真实专家反馈仍未接入。

当前 `evaluate` 不经过 Worker 摘要，因此其 Prompt、Model、Token 指标继续标记为不适用；这与 Worker 默认 Mock 摘要并不矛盾。真实方舟摘要纳入评测、真实 Trace 后端、采样/保留策略、生产 SLO 和真实反馈仍延期。

### M5-C：反馈、灰度决策与真实试点（C1/C2 代码与离线验收完成，C3 待真实输入）

目标：把离线评测、兼容快照比较、专家反馈和灰度停止条件连接成一条可审计决策链。C1/C2 继续 Mock-first，只能输出演练结论；C3 才能进入真实试点。

#### C1：Mock 专家反馈账本（代码与离线验收完成）

- 每条反馈绑定候选快照 Run/内容哈希/版本指纹和 Case ID，Reviewer 身份由适配层提供，不能来自报告或消息正文。
- Verdict 与 Reason 使用关闭集合；不保存自由文本、报告、Evidence、日志、查询、凭据或 Provider 错误。
- 反馈 append-only；纠正通过 `supersedes` 追加，不覆盖历史，跨 Run/Case/Reviewer 或分叉/循环链路 fail closed。
- 内置两个虚拟 Reviewer 覆盖五个合成 Case；真实身份数、真实专家标签数和外部网络调用均为 0。

验收：严格 Feedback Store 能拒绝重复、未知字段、篡改、越界和非法纠正链；同一候选快照可稳定解析每个 Case 的活动反馈与 Reviewer quorum；未产生任何灰度动作。

离线命令 `feedback-seed` 会为候选快照的五个合成 Case 生成两名固定虚拟 Reviewer 的十条安全反馈；重复执行保持相同记录身份，不会复制历史。

#### C2：离线灰度与回滚决策演练（代码与离线验收完成）

- 严格加载 base/candidate 快照，复用 B3 比较结果，再结合活动反馈和版本化策略生成关闭集合演练结论。
- `REHEARSAL_PASSED` 要求候选通过、可比、零回归、Gate 完整且通过、Case 反馈全覆盖、Reviewer quorum 达标且无不安全/不确定/分歧。
- 不可比、覆盖不足或 quorum 不足返回 `REHEARSAL_INSUFFICIENT_EVIDENCE`；失败/回归/不安全反馈在预检阶段返回 `REHEARSAL_BLOCKED`，仅模拟已放量阶段可返回 `REHEARSAL_ROLLBACK_RECOMMENDED`。
- 输出始终带 `SYNTHETIC_MOCK` 与 `production_action_allowed=false`，不能操作飞书可用范围、Worker、SLS、流量、开关或部署。

验收：全通过、缺反馈、Reviewer 分歧、不安全反馈、候选 Gate 删除/失败、质量/成本回归、不兼容数据边界和模拟回滚均有离线测试与结构化 CLI 输出。

离线命令 `rollout-rehearse` 只读加载快照和反馈；通过时输出 `REHEARSAL_PASSED`，任一非通过结论以非零退出码结束。所有输出固定包含 `SYNTHETIC_MOCK` 和 `production_action_allowed=false`。

#### C3：真实试点灰度（等待真实输入）

- 脱敏后的历史故障回放集和专家标注集。
- 真实 Reviewer 身份/权限/留存策略、真实飞书试点群、真实 SLS 试点资源和生产持久化。
- 团队批准的准确率、安全、时延和成本门槛，以及明确的停止/回滚 Runbook。

完整验收：只有 C3 在团队批准的真实数据、身份、阈值和试点范围内通过后，才能扩大服务和用户范围。C1/C2 的 Mock 演练永远不能替代该批准。

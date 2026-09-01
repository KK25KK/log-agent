# Log Agent

这是一个用 Go 开发的“证据驱动”日志调查 Agent。M0～M3、M3-B 跨信号时间线 Mock、受治理 SOP 人工核查 Mock、M4-A/M4-B、M5-A、M5-B/B1～B3、M5-C 的 Mock Reviewer/离线灰度演练，以及证据约束的 LLM 摘要、摘要安全评测和租户请求/Token 额度治理已经完成主体代码。2026-09-01 已用专用最小权限 Key 和 `doubao-seed-2-0-mini-260428` 通过独立方舟 Smoke，并通过本地 Web 完成一次 DAM 真实 SLS + 火山方舟真实 LLM 的同调查联合运行；这证明当前/基线只读计数、Worker/Eino、证据摘要和本地 Mock 投递能在一个真实应用事务中闭环。M4-C、真实可观测平台、真实企业知识源、真实飞书端到端、模型质量/费用/留存审批和 M5-C 真实灰度仍未完成，当前只能称为“具备试点条件”，不能称为日常可用或生产可用。

```text
飞书消息
  -> 幂等接单与 SQLite 任务
  -> Worker + Eino 固定 Graph
  -> 受控查询网关
       -> 资源目录 + ACL
       -> 查询预算 + Schema 校验
       -> 查询审计
       -> Mock SLS 或真实阿里云 SLS
  -> current / baseline 聚合结果 Checkpoint
  -> Evidence + M2 Report
  -> 可选的受控 Change Catalog
  -> 支持/反证 Ledger + CauseAnalysis
  -> 可选的受控指标/Trace 聚合 + IncidentTimeline
  -> Worker 首次校验 -> 可选的受治理 SOP 人工核查 -> 再次校验
  -> LLM 请求/Token 租户额度预留
  -> 证据约束摘要（默认 Mock / 可切火山方舟 / 失败回退）
  -> 实际 Token 结算或未知成本保留
  -> SQLite 持久化卡片事件
  -> 飞书接单 / 进度 / 最终报告卡
  -> 查看证据 / 取消 / 扩大窗口 / 重新运行
```

Eino 只负责流程编排，不负责业务状态、权限、幂等、审计和证据判定。飞书与阿里云 SDK 也只存在于各自适配层，因此核心业务不会和外部框架绑死。

## 当前已经实现

### 调查骨架

- 飞书企业自建应用 WebSocket 长连接入口。
- 严格命令：`/investigate <service> <environment> <duration>`。
- 以 `(app_id, tenant_key, message_id)` 为唯一键的持久化幂等接单。
- `QUEUED -> RUNNING -> SUCCEEDED | FAILED | CANCELLED | NEEDS_REVIEW` 状态机。
- Worker 心跳续租、租约过期重领，以及基于 `attempt` fencing token 的旧 Worker 防误提交。
- Eino v0.9.14 固定 Graph，不依赖 LLM。
- 当前窗口与等长基线窗口对比。
- 每个窗口固定执行“前置总量、Top 5 错误模式、Top 5 实例、后置总量”四次聚合；前后总量必须一致。
- 本地计算错误增长倍数、模式占比、实例集中度和 Top 5 覆盖率。
- Top-K 未命中默认只称“新增候选”；仅在基线分布穷尽且标签可比较时确认相对基线新增。
- Evidence、Finding 和 Report 的显式引用关系。
- 结构化 Finding Code 和基于证据的确定性 Recommendation；M2 不依赖 LLM。

### 只读 SLS 查询底座

- 从飞书入站信封派生可信 `Principal`，调用方无法在请求里伪造用户身份。
- 版本化 JSON 资源目录，将 `service/environment` 映射到受控 Endpoint、Project、LogStore、字段和模板版本。
- 完整 Principal 到资源的静态 ACL，默认拒绝。
- 两个注册查询模板：维度分析 `error_analysis_v2` 与仅计数 `error_count_v1`；用户和模型不能提交 Project、LogStore、字段、SQL 或 SPL。
- 执行前校验时间窗、固定调用数、返回行数、超时和单进程并发。
- 通过 `GetIndex` 校验模板所需字段；分析模板要求错误维度和实例维度为开启统计的 text 字段，计数模板只要求固定环境与错误选择器存在。
- 通过本机阿里云 CLI + SLS 插件执行固定只读聚合：分析模板每窗口四条，计数模板每窗口仅执行前后两次 `count(*)`，均不读取原始 `msg`。
- 保存四次本地执行 ID，以及 CLI 暴露的 Progress、纳秒有序元数据、处理行数、处理字节数和耗时；CLI 未提供 Provider Request ID 时保持为空，不伪造；前后总数变化时按证据不足处理。
- `Incomplete`、结构不一致、截断、元数据缺失或扫描字节超预算时禁止生成确定性结论。
- 查询标签做长度限制，并脱敏邮箱、IPv4、Bearer/JWT 和常见 AccessKey 形态。
- SQLite 追加式查询审计，记录拒绝、开始、成功、证据不足和失败，不保存凭据、原始日志或原始 SQL。
- `sls-check` 定向检查 Project、LogStore、Standard 模式和字段 Schema；不依赖有分页上限的全量资源列表。
- 显式 `sls-smoke` 查询命令。
- CLI 执行边界架构测试。

### 飞书调查闭环

- 调查状态事务同步写入 `QUEUED/RUNNING/SUCCEEDED/FAILED/CANCELLED` 卡片事件。
- 独立 Delivery Worker 使用租约、attempt fencing 和有限退避发送卡片。
- 飞书失败归一为可重试、永久、结果未知或取消；永久错误立即死信，每次发送结果追加审计。
- `delivery-dlq-list` 查看安全投影，`delivery-dlq-replay` 只重放不会覆盖更新卡片状态的事件。
- 第一次 Reply 创建 JSON 2.0 交互卡，后续 Patch 同一个 Card Message ID。
- 卡片支持查看证据、取消、扩大时间窗和重新运行。
- 按 App、Tenant、Chat、Card 和原请求者做授权；按钮不能携带资源或查询内容。
- 派生调查使用飞书 callback Event ID 持久化去重，重复回调不会创建第二个任务。
- 查看证据/返回报告可以由回调直接返回只读卡片；取消、扩大窗口和重新运行只返回 Toast，状态卡统一交给持久化 Delivery Worker 更新，避免两个写入者互相覆盖。

### M3 变更关联与反证

- 仅在 M2 已用完整当前/基线 Evidence 确认错误突增后执行原因增强。
- 从可信 Evidence 派生 `resource_id` 和关联时间范围，用户、卡片和模型不能指定物理资源或候选变更。
- 可选的版本化 Change Catalog 只接受受控 `RELEASE/CONFIG` 事件、负责人和受影响实例。
- 每个候选固定执行 4 项支持测试和 3 项反证测试，保存 `PASS/FAIL/UNKNOWN`、权重及 Evidence/Change 引用。
- 只有完整候选集合、全部支持通过且没有反证时才输出 `SUPPORTED_CANDIDATE`；完整实例范围零交集可输出 `REFUTED`，其余保守为 `INCONCLUSIVE`。
- `change-correlation-v1` 置信度是最高 `0.85` 的确定性启发式分数，不是概率，也不构成因果证明。
- Change Source 未配置、调用失败或返回非法数据时只把原因分析标为 `UNAVAILABLE`，不会抹掉 M2 事实或让原调查失败。
- Worker 在成功落库前再次校验固定测试、有限数值、Evidence/Change 引用、支持条件和硬反证质量，防止不可信 Engine 输出伪造结论。
- SQLite 在调查成功事务中同时保存 Evidence、Report 和独立 `evidence_ledger`；飞书报告与证据页做有界展示。

### M3-B 跨信号故障时间线（Mock-first）

- 新增可替换 `OperationalSignalSource`，资源和 `[baseline.start,current.end)` 只能从完整 Evidence 派生。
- 首个关闭合同只接受错误率与 P95 延迟两种聚合、`METRIC/TRACE` 两类来源；不接受原始 Span、TraceID、标签、任意属性或 Provider 文案。
- 一次调查最多调用信号源一次、返回八个观察；应用本地计算异常阈值，Worker 在落库前复算并校验所有 Evidence/Change/Signal 引用。
- 已有 Change Event 与指标/Trace 观察按时间稳定排序，飞书有界展示并明确“时间相关不等于因果证明”。
- `mock-e2e` 固定返回一个发布、一个指标异常和一个 Trace 异常，共三条时间线；SLS 仍只有两个逻辑观察和八次 Provider 调用。
- 信号源未配置时旧报告保持兼容；可选源失败、非法、不完整或截断只降级时间线，不改变 M2/M3 事实。
- 当前只有确定性 `signalmock`，没有真实 ARMS/CMS/Prometheus/OTel 连接器。设计与接入前置条件见 [`docs/m3b-cross-signal-incident-timeline.md`](docs/m3b-cross-signal-incident-timeline.md)。

### 受治理 SOP 人工核查（Mock-first）

- `RunbookSource` 只接收严格治理的 current/baseline Evidence 所对应的逻辑 `resource_id`、应用重算且与报告精确一致的 Recommendation Code 和固定上限。Evidence 必须使用固定 `error_analysis_v2` 模板，带完整 Query/Schema/Policy/Governance 元数据，两个窗口治理身份一致、连续等长，并与可信 Job 请求窗口精确绑定；调用前还要经 `ResourceCatalog.Resolve + Allowed` 重新绑定和授权。
- Worker 先校验 Eino 确定性输出，再用独立默认 5 秒子 Context 调用至多一次可选 `RunbookService`；返回后会先检查子 Deadline，所以 Deadline 后的 `(set, nil)` 也会降级而不会被接受。应用计算 Recommendation/Evidence 引用、内容指纹和 `HUMAN_REVIEW_ONLY` 投影，随后再次执行相同生产校验。
- Source 只能选择 `VERIFY_ERROR_PATTERN / OBSERVE_HOT_INSTANCE / ESCALATE_SERVICE_OWNER` 三个关闭步骤 Code；`VERIFY/OBSERVE/ESCALATE` 类型和展示文案由本地固定模板唯一生成，Provider 不能注入自由步骤文本。合同不存在 URL、命令、脚本、按钮、执行参数或自动处置入口。
- `NO_MATCH/INCONCLUSIVE/UNAVAILABLE/SKIPPED_NO_TRIGGER` 都保留原 Evidence、Finding、原因判断、时间线和确定性 Recommendation，不会把知识匹配写成根因或批准动作。
- `SKIPPED_NO_TRIGGER` 表示没有确定性错误突增；baseline 为 0 时继续按 `data_insufficient` 处理、不会调用 Source。已有确定性突增但 Recommendation 缺失或治理资源不一致时为 `UNAVAILABLE`，同样保持零调用。
- `RunbookGuidance.data_source` 由可信启动组装层固定为 `SYNTHETIC_MOCK / ENTERPRISE_GOVERNED`，Source 不能自报；条目更新时间同时受报告时间和可信服务时钟的 5 分钟偏差上限约束。飞书对合成目录的 SOP 区块明确显示“受控 SOP 参考（Mock）”；空值或非法来源只显示“来源未确认/当前不可用”，不会展示夹带条目。
- 当前只有 `internal/adapters/runbookmock` 的确定性目录。真实 Wiki、文档平台、错误码知识库、内容审批、租户授权、审计、失效和检索质量评测尚未接入。完整合同见 [`docs/governed-sop-knowledge-guidance.md`](docs/governed-sop-knowledge-guidance.md)。
- SOP 不进入 Eino Graph、`SummaryInput`、Agent Trace、Replay 或现有 `evaluate` 数据集/版本指纹。

### 证据约束的 LLM 摘要

- `ports.ReportSummarizer` 只接收通过 Worker 校验的有界投影；不发送飞书身份、物理资源、Query ID/Hash、SQL、原始日志、凭据或 Provider 错误。
- 默认 `LOG_AGENT_LLM_MODE=mock`，使用确定性 Mock 走完整 Worker、持久化和飞书卡片链路，且外部 API 调用为 0。
- `volcark` 适配器实现火山方舟 Responses API；只有显式设置 `LOG_AGENT_LLM_MODE=volcengine`、`ARK_API_KEY` 和 `LOG_AGENT_ARK_MODEL` 才会访问网络。
- 模型只能改写现象和证据说明、选择已有 `SUPPORTED_CANDIDATE` 以及选择已有 Recommendation Code；原因和下一步正文由应用从确定性报告反查，模型不能发明动作或提升结论。
- 未知字段、无效引用、敏感内容、危险动作、超时和 Provider 错误都会生成显式 `FALLBACK` 摘要，不改变调查的成功状态和确定性报告。
- 保存 Provider、模型、Prompt 版本/指纹、Request ID、Token 与耗时元数据，但不把 Prompt 正文或证据内容写入 Agent Trace。设计与真实接入说明见 [`docs/llm-evidence-summary.md`](docs/llm-evidence-summary.md)。
- 启用摘要时，Worker 会先按可信 App/Tenant 在 SQLite 固定窗账本预留一次请求和保守 Token；额度拒绝、重放或账本错误时不会调用 Provider，只生成确定性 fallback。
- Provider 成功结算实际 input/output/total Tokens；超时、取消或结果不确定按预留 Token 保留费用代理，绝不自动重复调用。完整合同见 [`docs/llm-summary-quota.md`](docs/llm-summary-quota.md)。

### M4-A 可恢复查询步骤

- 将两个有外部成本的逻辑观察固定为 `sls.current` 和 `sls.baseline`，在 Eino 之外持久化输入指纹与规范化聚合结果。
- 已成功落盘的窗口在租约恢复后直接复用；current 已完成时只执行缺失的 baseline，不从头重复查询。
- 输入指纹同时绑定管理员控制的资源目录、物理 Project/LogStore、Selector、Schema、模板、策略和预算；治理配置变化时禁止混用旧、新窗口证据。
- Checkpoint Prepare/Complete 同时校验 job、investigation、lease owner、job attempt、租约、step key 和 input hash，旧 Worker 无法提交到新 attempt。
- 若请求可能已到达 SLS、但结果未成功落盘，步骤变为 `UNKNOWN`，调查变为 `NEEDS_REVIEW`，系统不自动再次查询。
- 飞书提示明确说明潜在重复查询成本，只允许专用的 `rerun_with_cost_ack` 按钮在用户确认后创建新调查；执行中取消若留下未知查询，也使用同一确认门禁，不会显示 Provider 原始错误。
- 该能力不代表 SLS Provider exactly-once，也不代表完整 M4 已完成。设计、验收与延期项见 [`docs/m4-recoverable-query-steps.md`](docs/m4-recoverable-query-steps.md)。

### M5-A 全合成离线评测门禁

- 内置版本化严格数据集，至少覆盖突增且关联候选成立、无突增、证据不足、变更被反证和变更不确定五类安全场景。
- 每个 Case 使用 Fixture Mock 提供 SLS 聚合和变更上下文，但运行的仍是应用当前使用的真实确定性 Eino Graph，不复制调查算法。
- 输出 outcome accuracy、misleading rate、conclusive recall、Finding 与 Recommendation 精确匹配率、生产 Worker 输出校验通过率、Engine/Report Evidence 一致性与 QuerySpec 绑定准确率、Evidence 引用覆盖率、原因判断准确率、查询合同、调用/处理字节成本代理和本机耗时。Recommendation 标签同时约束 Code 和 `current`/`baseline` Evidence 绑定，删除、插入或错误引用建议都会使门禁失败。
- 数据集版本和 SHA-256 指纹进入报告；任一 Case 或工程门禁失败时，命令打印完整结构化 JSON 后以非零状态退出。
- 全部请求、身份、聚合、变更和标签均为合成 Mock，真实故障数与专家标签数都是 0，不读取凭据、不访问网络。
- 当前 Graph 不调用 LLM，因此 Prompt、Token 和模型成本指标明确为 `N/A`。M5-A 不是生产准确率、真实成本、SLO 或灰度批准，完整设计见 [`docs/m5-offline-evaluation-gate.md`](docs/m5-offline-evaluation-gate.md)。

### M5-B Agent 事件、版本合同与离线回放

- `evaluate` 在执行 Case 前生成统一版本清单，并对规范化清单计算 SHA-256；清单绑定数据集、Graph、查询模板/策略、原因方法、评测门禁、Trace/Replay Schema、执行 Profile 和真实 Prompt 使用情况。
- `agent-trace-v1` 使用关闭枚举的 `RUN / GRAPH_NODE / TOOL` 事件；每个 Case 固定包含一个 Engine 根 Span、四个 Graph 节点、current/baseline 两个工具 Span，确有变更源调用的 Case 再增加一个 `change_source.list` Span。
- 默认 Observer 是 Noop；评测使用线程安全有界 Recorder。遥测无效或溢出不会改变调查结果，但 Trace 会变成不完整并使离线门禁失败。
- Trace 只保存有界身份、稳定代码、时间、耗时、哈希和调用/字节计数，不保存飞书消息或身份、资源、查询、日志/桶标签、变更摘要、自然语言报告、回调、Provider 原始错误、Prompt 正文或任意属性。
- B2 使用独立 Evaluation Run Store，把成功和门禁失败的评测追加为 `evaluation-replay-v1` 严格快照；快照包含完整评测报告、版本清单、逐 Case Trace、安全失败码、父回放引用和内容哈希。同一 Run ID 不能覆盖，未知字段/Schema、文件篡改和不兼容数据边界会 fail closed。
- `replay` 只会用当前二进制与仓库内置合成 Mock 重跑，并追加一个引用已校验源快照的新快照；它不执行历史二进制。
- B3 的 `replay-compare` 只读加载两个已校验快照。数据集 Schema/ID/指纹、数据边界、执行 Profile 和 Case 集完全一致时，输出版本、门禁、失败 Case、质量、成本代理、工具、Trace、安全失败码和观测时延差异；候选删除任何既有 Gate 都记为回归。不兼容时只输出稳定 `INCOMPARABLE` 原因码，不制造数值差异；Trace/Replay Schema 不匹配会更早在严格加载阶段 fail closed。
- 当前仍只覆盖 Engine/evaluation 级 Trace，不是飞书入站到卡片投递的分布式 Trace，也不是生产可观测、采样/保留策略或 SLO。完整设计与验收见 [`docs/m5-agent-observability-replay.md`](docs/m5-agent-observability-replay.md)。

### Mock Reviewer 反馈与离线灰度演练

- `evaluation-feedback-v1` 将 Reviewer Verdict 绑定到不可变候选快照、版本指纹和 Case ID，不保存自由文本、报告、Evidence、日志、查询、凭据或真实身份。
- 反馈文件 append-only；纠正通过 `supersedes` 追加。覆盖、分叉、循环、跨 Run/Case/Reviewer、未知字段、尾随内容和内容哈希变化全部 fail closed。
- 内置 Fixture 为五个合成 Case 各生成两名固定虚拟 Reviewer，共十条反馈；重复 seed 不复制历史。
- `rollout-rehearsal-policy-v1` 复用 B3 比较，检查 Gate、回归、Case 覆盖、Reviewer quorum、`UNSAFE`、`UNSURE` 和分歧。
- 决策只有 `PASSED/BLOCKED/ROLLBACK_RECOMMENDED/INSUFFICIENT_EVIDENCE` 四种关闭状态，并始终输出 `data_source=SYNTHETIC_MOCK`、`production_action_allowed=false`。
- 该能力不调用部署、流量、飞书、SLS 或任何真实系统，也不是灰度批准。完整合同见 [`docs/offline-feedback-and-rollout-rehearsal.md`](docs/offline-feedback-and-rollout-rehearsal.md)。

## 先运行飞书 + SLS 双 Mock

下面这条命令会走完整的本地纵向链路，不读取环境变量、不需要飞书 App、阿里云账号或任何凭据，也不会发起网络请求：

```powershell
go run ./cmd/logagent mock-e2e
go run ./cmd/logagent mock-e2e error_count_v1
```

第一个命令保持原 `error_analysis_v2` 回归；第二个命令验证 DAM 轻量模板。计数型 Mock E2E 仍经过飞书 Mock、Catalog/ACL、Gateway、Checkpoint、租户配额、Eino、Worker、SQLite、Mock LLM 和飞书 Mock 卡片，但只产生 4 次 Provider 调用代理，并对变更、跨信号和 Runbook Source 保持零调用。

分析模板的 Mock E2E 会依次完成：

1. 模拟飞书用户发送 `/investigate order-service prod 30m`；
2. 将同一消息重放一次，验证 Inbox 幂等去重；
3. 通过真实 SQLite 状态机创建并领取调查任务；
4. 通过真实 Worker + Eino 固定 Graph、资源 ACL、Schema/预算网关和查询审计调用 Mock SLS Backend；
5. 将 current、baseline 两个规范化聚合结果写入真实 SQLite Checkpoint；
6. 生成 Evidence、M2 报告和 Mock 变更关联账本；
7. 用一次 Mock Operational Signal Source 调用生成三条跨信号时间线；
8. 在首次报告校验后，校验固定 Evidence 模板、治理指纹和可信请求窗口，用一次独立 5 秒边界内的 Mock Runbook Source 调用生成一项、三步的人工核查指引，写入可信 `SYNTHETIC_MOCK` 来源并再次校验引用和安全边界；
9. 使用确定性 Mock 摘要器生成有引用约束的报告摘要，SOP 内容不进入模型输入；
10. 通过真实 Delivery Worker 模拟飞书卡片 `REPLY(QUEUED) -> PATCH(RUNNING) -> PATCH(SUCCEEDED)`。

输出中的 `safety.external_network_calls=0`、`credentials_required=false` 表示当前运行完全离线；`schema_calls=1`、`backend_execute_calls=2`、`provider_api_calls=8`、`query_audit_events=4` 和 `query_step_checkpoints=2` 分别证明固定 Schema、当前/基线观察、四聚合调用元数据、开始/终态审计和两个持久化步骤已经经过真实应用链路。固定的 120/20 条错误、发布事件和飞书标识全部是测试数据，不代表真实阿里云或飞书结果。完整 Mock 边界见 [`docs/local-mock-e2e.md`](docs/local-mock-e2e.md)，恢复合同见 [`docs/m4-recoverable-query-steps.md`](docs/m4-recoverable-query-steps.md)。

`tenant_quota` 还证明两个逻辑观察经过 SQLite 固定窗额度预留/结算且没有打开成本代理熔断。该值是 Mock usage，不是阿里云账单。

`llm_quota.requests=1`、`tokens=0` 证明摘要调用也经过独立的 SQLite 请求/Token 预留与结算；零 Token 来自确定性 Mock，不代表火山方舟实际消耗或账单。

`llm_summary.mode=MOCK`、`status=GENERATED`、`external_api_calls=0` 证明摘要合同经过主链，但没有调用真实模型。`evaluate` 仍是 Engine 级确定性评测，不经过 Worker 摘要，因此其版本清单继续如实记录 `prompt_used=false`。

`operational_signals.source_calls=1`、`timeline_status=COMPLETE`、`signals=2` 和 `timeline_items=3` 证明指标/Trace Mock 已经过真实 Engine、Worker 校验、SQLite Report 持久化和飞书展示合同；它们不是生产监控结果，也没有增加 SLS 调用。

本轮实际 `mock-e2e` 输出为 `runbook_knowledge.mode=SYNTHETIC_MOCK`、`source_calls=1`、`status=COMPLETE`、`items=1`、`steps=3`。正式飞书 Renderer 对该来源使用“受控 SOP 参考（Mock）”标题。该投影全部来自确定性 Mock，只供人工核查，不代表真实企业 SOP 已接入，也没有增加两个 SLS 观察、八次 Provider 调用代理或外部网络访问。

## 运行第六至八期离线评测与 Trace 门禁

```powershell
go run ./cmd/logagent evaluate
```

该命令加载仓库内置的 `synthetic-m5a-v1` 数据集，对每个 Case 组装独立 Fixture Mock，并执行真实确定性 Eino Graph。它不读取环境变量中的外部凭据，也不会访问飞书、阿里云 SLS、发布平台或模型服务。

命令输出结构化 JSON，包含数据集身份与指纹、规范化版本清单及其 SHA-256、逐 Case 完整 Agent Trace、聚合指标和门禁状态。通过时退出码为 `0`；数据集非法、Graph 执行失败、标签不一致、出现意外确定性结论、证据引用失效、Trace 不闭合/丢事件，或查询/成本代理合同越界时返回非零退出码。脚本和 CI 应检查退出码，不能只匹配输出文字。

评测数字只描述受控合成回归集：它不是历史真实故障或专家标注上的准确率，`processed_bytes`/API 调用数不是阿里云账单，本机耗时也不是生产 SLO。

## 运行 LLM 摘要离线安全门禁

```powershell
go run ./cmd/logagent summary-evaluate
```

该命令复用现有 5 类合成调查 Fixture，先运行真实确定性 Eino Graph，再通过生产 `SummaryService` 执行 9 类摘要场景：正常摘要、Provider 失败、虚构 Evidence、虚构 Recommendation、选择已反证原因、危险动作和敏感出站输入。它使用独立严格 Schema，不改变 `evaluate` 或历史回放格式。

当前结果为 9/9 通过，production output、deterministic integrity、summary contract、input privacy 和 fallback accuracy 均为 1；8 次预期 Mock Provider 调用完全匹配，敏感输入 Case 的 Provider 调用为 0，Token、凭据和外部网络调用均为 0。这只能证明安全合同在合成回归集上成立，不代表火山方舟真实中文质量、费用或 Prompt 已获批准。

## 保存、回放并比较第八至九期离线快照

保存一次成功或失败的评测运行：

```powershell
go run ./cmd/logagent evaluate --snapshot-dir .\data\evaluation-runs
```

输出顶层的 `evaluation_run_id` 是快照文件名和后续回放键。回放时传入这个 ID：

```powershell
go run ./cmd/logagent replay --snapshot-dir .\data\evaluation-runs --run-id evalrun_xxx
```

`replay` 会先严格加载并核对源文件的 SHA-256、Schema、运行身份、版本清单、合成数据边界和 Case Trace，再用当前二进制重跑内置 Fixture Mock。成功执行后会在同一目录追加新快照，`replay_of` 指向源 Run 与源哈希；源文件永远不会被覆盖。快照目录在 `data/` 下时默认不会被 Git 跟踪。

比较两次已保存运行：

```powershell
go run ./cmd/logagent replay-compare `
  --snapshot-dir .\data\evaluation-runs `
  --base-run-id evalrun_base `
  --candidate-run-id evalrun_candidate
```

`replay-compare` 不执行 Graph、Mock 工具或网络请求，只读取两个不可变文件。`COMPARABLE` 输出 27 项固定质量/成本/工具/Trace/时延观测指标、3 个固定工具维度、版本变化、门禁变化和 recovered/newly-failed/still-failed Case；`INCOMPARABLE` 不包含这些差值并返回非零退出码。时延只用于本机趋势观察，不是生产 SLO。

这仍不是历史实现复现：要重现旧逻辑必须切换到对应 Git commit 或构建制品。比较结果也只描述合成数据，不是生产质量或真实成本。

## 生成 Mock 反馈并执行离线灰度演练

先为候选快照生成两名虚拟 Reviewer 对五个合成 Case 的十条反馈：

```powershell
go run ./cmd/logagent feedback-seed `
  --snapshot-dir .\data\evaluation-runs `
  --feedback-dir .\data\evaluation-feedback `
  --run-id evalrun_candidate
```

再执行只读预检：

```powershell
go run ./cmd/logagent rollout-rehearse `
  --snapshot-dir .\data\evaluation-runs `
  --feedback-dir .\data\evaluation-feedback `
  --base-run-id evalrun_base `
  --candidate-run-id evalrun_candidate
```

完整同意且没有 Gate/指标回归时输出 `REHEARSAL_PASSED`。缺反馈、quorum 不足、分歧、不可比或任何阻断条件都会输出结构化原因并返回非零退出码。输出永远不可执行生产动作。

## 只查看离线报告 Demo（可选）

环境要求：Go 1.26 或更高版本。Demo 永远使用 Mock SLS、内置 Mock Change Source 和 Mock Runbook Source，不需要飞书、阿里云、发布平台或企业知识系统凭证。

```powershell
go run ./cmd/logagent demo
```

输出是一份 JSON 调查报告，包含结论、资源与模板版本、查询指纹、完整性、统计证据、M3 原因分析和受控 SOP 人工核查投影。Mock 中固定为当前窗口 120 条错误、基线 20 条，并放入一个影响 `order-pod-a` 的发布事件和一个内置 Runbook 条目；这些都是测试数据，不是生产日志、真实发布结果或企业 SOP。

## 配置 M3 Change Catalog（可选）

Change Catalog 是管理员维护的只读治理配置，不是发布平台连接器。复制示例并设置 Worker 环境变量：

```powershell
Copy-Item .\config\change-catalog.example.json .\config\change-catalog.json
$env:LOG_AGENT_CHANGE_CATALOG = ".\config\change-catalog.json"
```

Catalog 中的 `resource_id` 必须与 `sls-resources.json` 的资源 ID 完全一致。当前只支持 `RELEASE` 和 `CONFIG`；每个事件必须提供起止时间、负责人、摘要、受影响实例列表和 `affected_instances_complete`。单个列表最多 20 个实例，一次调查最多读取 10 个重叠候选。

不设置 `LOG_AGENT_CHANGE_CATALOG` 时 M3 原因增强明确显示为 `UNAVAILABLE`，M2 报告仍可正常完成。文件在 Worker 启动时严格校验并一次性加载，当前不支持热重载；完整字段和判定规则见 [`docs/m3-change-correlation-evidence.md`](docs/m3-change-correlation-evidence.md)。

## 配置报告摘要

本地与试点前默认使用完全离线 Mock：

```powershell
$env:LOG_AGENT_LLM_MODE = "mock"
```

经安全、留存和成本审批后，才显式启用火山方舟：

```powershell
$env:LOG_AGENT_LLM_MODE = "volcengine"
$env:ARK_API_KEY = "<由密钥系统注入，不写入仓库>"
$env:LOG_AGENT_ARK_MODEL = "doubao-seed-2-0-mini-260428"
$env:LOG_AGENT_LLM_TIMEOUT = "12s"
go run ./cmd/logagent llm-check
go run ./cmd/logagent llm-smoke
```

`LOG_AGENT_ARK_BASE_URL` 默认并限制为 `https://ark.cn-beijing.volces.com/api/v3`，避免配置错误时把 Key 发送到其他域名。`llm-check` 只做本地配置检查且网络调用为 0；`llm-smoke` 使用 count-only 合成报告经过正式摘要安全合同，只调用一次方舟，不访问 SLS 或飞书，也不打印模型正文。2026-09-01 的真实 Smoke 已通过，单次调用为 971 Token、1939 ms；仓库未保存真实 Key。上线前仍需按 [`docs/llm-evidence-summary.md`](docs/llm-evidence-summary.md) 完成 Prompt、Token/费用预算、数据保留和真实样本质量审批。

## 配置真实阿里云 SLS

### 1. 创建本地资源目录

```powershell
Copy-Item .\config\sls-resources.example.json .\config\sls-resources.json
```

然后把文件中的占位内容替换为试点资源：

- `endpoint`：必须显式使用 `https://`；同地域部署优先 VPC Endpoint。
- `project`、`logstore`：由管理员固定，用户不能指定。
- `selectors`：固定的业务范围字段和值，例如服务和环境。
- `error_selector`：必填的错误谓词，例如 `level=ERROR`；它与业务范围选择器分离，避免把全部日志误算成错误。
- `error_field`：用于 Top 错误聚合，必须已创建 text 字段索引并开启统计。
- `instance_field`：用于 Top 实例聚合，也必须是开启统计的 text 索引字段。
- `error_field` 与 `instance_field` 必须互不相同，且不能和 `selectors` 或 `error_selector` 使用同一个字段。
- `bindings`：允许访问该资源的飞书 App ID、Tenant Key 和用户 Open ID。

目录中不要写 AccessKey 或 Token。

### 2. 配置 CLI Profile 与预算

项目不会自动加载 `.env`，也不接收 AK/SK/Token 环境变量。先由用户通过企业 SSO 获取短期 STS，并在自己的终端写入本机阿里云 CLI `StsToken` Profile：

```powershell
aliyun configure --mode StsToken --profile default
```

凭据只由 CLI Profile 读取；STS 到期后由用户重新获取并覆盖该 Profile。log-agent 的 Go 代码不会自动续签、切换账号或读取 Profile 文件。

完整迁移影响、安全边界和逐步接入操作见 [`docs/sls-cli-sts-migration.md`](docs/sls-cli-sts-migration.md)。

DAM 当前采用单主 Logstore 的 `error_count_v1` 轻量试点。2026-09-01 真实 `sls-check` 与 `sls-smoke` 已通过，`env=test + level=error` 固定计数、`data/meta` 响应、显式 Region 与 host-only endpoint 已完成验证；试点不要求新增 `error_type/instance_id`，也不会输出错误类型、实例或根因。本地 Web 同日先完成“页面 -> Worker/Eino -> 真实 SLS -> Mock LLM -> 本地 Delivery”，随后完成“页面 -> Worker/Eino -> 真实 SLS -> 火山方舟真实 LLM -> 本地 Delivery”的同调查联合验收；飞书仍为 Mock。实现和验收范围见 [`docs/error-count-v1-implementation.md`](docs/error-count-v1-implementation.md)、[`docs/dam-single-logstore-pilot.md`](docs/dam-single-logstore-pilot.md) 与 [`docs/local-web-pilot-console.md`](docs/local-web-pilot-console.md)。

最小只读 RAM 策略模板见 `config/sls-readonly-policy.example.json`。它只包含定向检查和查询需要的 `GetProject`、`GetLogStore`、`GetIndex`、`GetLogStoreLogs`，请替换地域、账号、Project 和 LogStore 占位符；不要给 Agent `AliyunLogFullAccess`。

```powershell
$env:LOG_AGENT_SLS_MODE = "aliyun"
$env:LOG_AGENT_SLS_CATALOG = ".\config\sls-resources.json"
$env:LOG_AGENT_SLS_CLI_PROFILE = "default"
# 可选：aliyun 不在 PATH 时指定可信绝对路径
$env:LOG_AGENT_SLS_CLI_PATH = "C:\path\to\aliyun.exe"
```

默认门禁：

| 配置 | 默认值 | 含义 |
| --- | ---: | --- |
| `LOG_AGENT_SLS_REQUEST_TIMEOUT` | `15s` | 单次 CLI 调用超时 |
| `LOG_AGENT_SLS_CLI_MAX_OUTPUT_BYTES` | `4194304` | 单次 CLI stdout 上限 |
| `LOG_AGENT_SLS_QUERY_TIMEOUT` | `45s` | 一个窗口四次聚合的应用总时限 |
| `LOG_AGENT_SLS_MAX_WINDOW` | `2h` | 单个观察窗口上限 |
| `LOG_AGENT_SLS_INGESTION_GRACE` | `10s` | 查询结束时间相对消息时刻向前回退的索引安全水位，最小 `3s` |
| `LOG_AGENT_SLS_MAX_ROWS` | `12` | 每个观察的固定聚合结果行预算 |
| `LOG_AGENT_SLS_MAX_API_CALLS` | `4` | 每个观察的固定 API 调用数 |
| `LOG_AGENT_SLS_MAX_PROCESSED_BYTES` | `268435456` | 每个观察处理字节上限 |
| `LOG_AGENT_SLS_MAX_CONCURRENT` | `2` | 单个 Worker 进程查询并发 |
| `LOG_AGENT_SLS_SCHEMA_TTL` | `5m` | 索引 Schema 缓存时间 |

飞书 Delivery Worker 默认配置：

| 配置 | 默认值 | 含义 |
| --- | ---: | --- |
| `LOG_AGENT_DELIVERY_WORKER_ID` | `feishu-delivery-local` | 发送 Worker 身份 |
| `LOG_AGENT_DELIVERY_POLL_INTERVAL` | `500ms` | 空闲轮询间隔 |
| `LOG_AGENT_DELIVERY_LEASE_DURATION` | `30s` | 卡片事件租约 |
| `LOG_AGENT_DELIVERY_SEND_TIMEOUT` | `8s` | 单次飞书发送上限 |
| `LOG_AGENT_DELIVERY_MAX_ATTEMPTS` | `5` | 最大本地发送尝试数 |
| `LOG_AGENT_DELIVERY_RETRY_BASE` | `2s` | 指数退避基数 |

`LOG_AGENT_DELIVERY_LEASE_DURATION` 必须严格大于 `LOG_AGENT_DELIVERY_SEND_TIMEOUT`。

租户查询额度默认配置：

| 配置 | 默认值 | 含义 |
| --- | ---: | --- |
| `LOG_AGENT_TENANT_QUOTA_WINDOW` | `1h` | 本地固定 UTC 额度窗口 |
| `LOG_AGENT_TENANT_QUOTA_MAX_OBSERVATIONS` | `100` | 每租户逻辑观察上限 |
| `LOG_AGENT_TENANT_QUOTA_MAX_API_CALLS` | `400` | 每租户 Provider 调用代理上限 |
| `LOG_AGENT_TENANT_QUOTA_MAX_PROCESSED_BYTES` | `8589934592` | 每租户处理字节代理上限 |
| `LOG_AGENT_TENANT_QUOTA_RESERVED_BYTES` | `268435456` | 每个未结算观察预留的字节代理 |
| `LOG_AGENT_LLM_QUOTA_WINDOW` | `1h` | 摘要固定 UTC 额度窗口 |
| `LOG_AGENT_LLM_QUOTA_MAX_REQUESTS` | `100` | 每租户每窗口摘要请求上限 |
| `LOG_AGENT_LLM_QUOTA_MAX_TOKENS` | `409600` | 每租户每窗口 Token 上限 |
| `LOG_AGENT_LLM_QUOTA_RESERVED_TOKENS` | `4096` | 每次摘要 Provider 调用前预留 Token |

这些额度由可信 App/Tenant 哈希隔离，只是 SQLite 技术预览，不是多实例全局限额或费用账单。死信运维命令：

```powershell
go run ./cmd/logagent delivery-dlq-list --db .\data\logagent.db --limit 50
go run ./cmd/logagent delivery-dlq-replay --db .\data\logagent.db --delivery-id delivery:inv_xxx:SUCCEEDED --operator ops-user-1
```

重放会在事务内检查当前卡片绑定和后续投影；不安全的旧进度或已重绑事件会拒绝。详见 [`docs/m4b-reliability-governance.md`](docs/m4b-reliability-governance.md)。

### 3. 先检查资源元数据

```powershell
go run ./cmd/logagent sls-check
```

该命令定向验证 Project、LogStore、Standard 模式和固定模板所需的索引字段，只输出非秘密元数据，不查询日志行。

### 4. 再执行显式 Smoke 查询

Smoke 身份也必须存在于资源目录 ACL 中：

```powershell
$env:LOG_AGENT_SMOKE_APP_ID = "cli_xxx"
$env:LOG_AGENT_SMOKE_TENANT_KEY = "tenant-key"
$env:LOG_AGENT_SMOKE_USER_ID = "ou_xxx"
go run ./cmd/logagent sls-smoke order-service prod 10m
```

该命令经过完整资源解析、ACL、Schema、预算、审计和脱敏网关，执行一次固定聚合观察。它不会返回原始日志正文。

## 飞书权限未就绪时运行本地 Web 排障台

`web` 命令把本地 HTTP 入口、SQLite、正式 Worker、Eino、当前配置的 SLS/LLM 和持久化 Delivery Worker 放在同一个进程中。它默认只监听 `127.0.0.1:8080`，并使用独立的 `data/web-pilot.db`，不会改动或删除飞书适配器。

先用完全离线模式验证：

```powershell
$env:LOG_AGENT_SLS_MODE = "mock"
$env:LOG_AGENT_LLM_MODE = "mock"
go run ./cmd/logagent web
```

然后打开 [http://127.0.0.1:8080](http://127.0.0.1:8080)。页面支持提交、自动刷新、报告/Evidence、取消、扩大窗口和重跑。入口身份固定为 `local-web/local-pilot/operator`，HTTP 参数不能覆盖；真实 SLS 资源目录的 Binding 必须与该身份一致。

真实 SLS 与真实方舟的联合本地试点仍使用同一个命令，只显式切换既有配置：

```powershell
$env:LOG_AGENT_SLS_MODE = "aliyun"
$env:LOG_AGENT_SLS_CATALOG = ".\config\sls-resources.json"
$env:LOG_AGENT_SLS_CLI_PROFILE = "default"
$env:LOG_AGENT_LLM_MODE = "volcengine"
$env:ARK_API_KEY = "<仅注入当前终端，不写入文件或仓库>"
$env:LOG_AGENT_ARK_MODEL = "doubao-seed-2-0-mini-260428"
go run ./cmd/logagent web
```

该结果可以验收“网页 → Intake/SQLite → Worker/Eino → SLS → LLM → 报告/动作/本地 Delivery”的应用链路，但不能冒充真实飞书 WebSocket、OpenID、Reply/Patch、卡片视觉或回调权限验收。实现、配置和测试边界见 [`docs/local-web-pilot-console.md`](docs/local-web-pilot-console.md)。

## 运行 Worker 与飞书入口

Mock Worker：

```powershell
$env:LOG_AGENT_SLS_MODE = "mock"
$env:LOG_AGENT_DB_PATH = ".\data\logagent.db"
go run ./cmd/logagent worker
```

真实 SLS Worker 使用上文的 `aliyun` 配置，然后运行同一个命令：

```powershell
go run ./cmd/logagent worker
```

飞书入口需另开一个进程并共享同一数据库文件：

```powershell
$env:FEISHU_APP_ID = "cli_xxx"
$env:FEISHU_APP_SECRET = "your-secret"
$env:LOG_AGENT_DB_PATH = ".\data\logagent.db"
go run ./cmd/logagent feishu
```

飞书后台需使用企业自建应用，启用机器人能力和 WebSocket 长连接，并配置：

- 事件 `im.message.receive_v1`；
- 回调 `card.action.trigger`；
- 单聊/群内 @ 消息读取权限与机器人发消息权限。

默认群聊只响应 @ 机器人的调查命令，不需要申请群全量消息权限。

飞书用户发送：

```text
/investigate order-service prod 30m
```

飞书进程同时维护 WebSocket 接收、卡片动作回调和持久化 Delivery Worker。它与调查 Worker 可以是两个进程，只需共享同一个 SQLite 数据库文件。

## 验证

```powershell
Get-ChildItem -Recurse -Filter *.go | ForEach-Object { gofmt -w $_.FullName }
go test -count=1 ./...
go vet ./...
go run ./cmd/logagent evaluate
go run ./cmd/logagent summary-evaluate
go run ./cmd/logagent mock-e2e
go run ./cmd/logagent demo
```

`go test -race ./...` 在 Windows 上需要 C 编译器并启用 `CGO_ENABLED=1`。

默认测试全部离线，不读取云凭据、不访问 SLS 或发布平台。只有显式运行 `sls-check`、`sls-smoke`，或以 `LOG_AGENT_SLS_MODE=aliyun` 启动 Worker，才会访问真实 SLS。

以下是受治理 SOP 进入 Worker 之前保存的离线基线验收记录：当时 `gofmt`、`go test -count=1 ./...`、`go vet ./...`、重点包乱序 20 轮、`evaluate`、`summary-evaluate`、`mock-e2e`、快照保存/回放/比较、`feedback-seed` 和 `rollout-rehearse` 均通过。`evaluate` 的 5/5 个合成 Case 全部通过，`trace_contract_accuracy=1`；共记录 76 个事件、13 个工具 Span、0 个丢弃事件，并与 10 次逻辑 SLS 观察、40 次 Provider 调用代理、3 次 Change Source 调用和 78,080 processed bytes 完全核对。`summary-evaluate` 的 9/9 个安全 Case 通过，预期/实际 Mock Provider 调用均为 8，敏感输入 Case 调用为 0，Token、凭据和外部网络调用为 0。C1/C2 手工链路形成十条活动反馈、五个完整 Case、两名虚拟 Reviewer quorum，并返回 `REHEARSAL_PASSED`、`SYNTHETIC_MOCK`、`production_action_allowed=false`；阻断和证据不足路径由离线测试覆盖。Engine 数据集指纹为 `caf2714c80a646c5da15134c6557879565ffc8e083a66da1f1c9e49d3d0dc1f8`，摘要数据集指纹为 `82e813aed0721f15b89a19b053da6b1d47509ab07f45122af4ed0c075e60a0b1`，规范化版本指纹为 `14db14acf992ebd06d9d4d71f89056be2a2b984baeb6bf5de2c136db442f7c53`。这些仍只是全合成 Mock 的工程回归结果，并且都不包含 SOP 数据。`go test -race ./...` 当时未执行，因为 Windows 环境 `CGO_ENABLED=0` 且未安装 GCC，不能写成已通过。

2026-08-24 第二轮严格 Evidence、可信来源/时钟及两个 fail-closed 边界全部落地后，最终工作树再次通过 `gofmt -w .`、`go test -count=1 ./...`、`go vet ./...` 和重点包 `-shuffle=on -count=20`。`mock-e2e` 为 `SUCCEEDED`，Runbook 为 `SYNTHETIC_MOCK/COMPLETE`、1 次调用/1 项/3 步，SLS 保持 2 次逻辑观察/8 次 Provider 调用代理/0 次外部网络；`demo` 为 `SUCCEEDED` 且为 `HUMAN_REVIEW_ONLY`。`evaluate` 5/5 与 `summary-evaluate` 9/9 均为 `PASSED`，数据集指纹分别保持 `caf2714c80a646c5da15134c6557879565ffc8e083a66da1f1c9e49d3d0dc1f8` 和 `82e813aed0721f15b89a19b053da6b1d47509ab07f45122af4ed0c075e60a0b1`。最终临时链路也重新完成快照保存、replay、`replay-compare=COMPARABLE`（27 项指标、3 个工具维度、0 回归）、十条活动 Mock 反馈和 `rollout-rehearse=REHEARSAL_PASSED`，仍固定为 `SYNTHETIC_MOCK`、`production_action_allowed=false`；最终安全复查未发现 P0–P3。`go test -race ./...` 仍因 `CGO_ENABLED=0` 且未安装 GCC 而未执行。

本次 LLM 额度治理还单独通过了 `SummaryQuota|LLMQuota|MockE2E|BuildSummaryService` 定向 50 轮；Mock 主链为 1 次摘要请求、0 Token、0 凭据和 0 网络调用。

M3-B 跨信号切片另通过全仓测试、静态检查和重点包 30 轮验证；`demo` 与 `mock-e2e` 都生成 `COMPLETE` 时间线。`mock-e2e` 只调用 1 次 Operational Signal Mock，形成 2 个聚合信号和 3 个时间线条目，同时保持 8 次 SLS Provider 调用代理、0 外部网络调用。既有 Engine/摘要评测仍为 `PASSED`，数据集指纹保持不变。

## 代码边界

```text
cmd/logagent                          进程组装与诊断命令
internal/application                 接单、调查 Worker、查询 Checkpoint、卡片 Delivery 和动作控制用例
internal/application/query           ACL、预算、Schema、审计与脱敏网关
internal/domain                      领域数据、资源、查询、原因假设、证据账本、Agent 事件与版本清单模型
internal/ports                       Store、Engine、QueryGateway、SLSBackend、ChangeSource、OperationalSignalSource、RunbookSource、ReportSummarizer、AgentObserver 接口
internal/adapters/eino               唯一允许导入 Eino 的包
internal/adapters/feishu             唯一允许导入飞书 SDK 的包
internal/adapters/feishumock         离线飞书收件与卡片投递模拟，不导入 SDK
internal/adapters/aliyuncli           唯一允许调用阿里云 CLI/SLS 插件的包
internal/adapters/resourcecatalog     JSON 资源目录与静态 ACL
internal/adapters/changecatalog       M3 版本化发布/配置变更目录
internal/adapters/signalmock          M3-B 指标/Trace 聚合离线 Mock
internal/adapters/runbookmock         受治理 SOP 人工核查离线 Mock，不访问知识平台
internal/adapters/sqlite              本地持久化、查询审计/Checkpoint、卡片死信、租户额度与审批合同
internal/adapters/slsmock             离线确定性数据
internal/adapters/evalmock            M5-A 逐 Case 的 SLS 与 Change Source Fixture Mock
internal/adapters/replayfs             B2 append-only 评测快照文件存储与严格读取
internal/adapters/feedbackfs           append-only Mock Reviewer 反馈文件存储
internal/evaluation                   严格合成数据集、质量/Trace 指标和离线门禁
internal/evaluation/summaryeval       LLM 摘要严格合成场景、指标和安全门禁
internal/evaluation/replay            B2 快照 Schema/内容哈希与 B3 兼容快照比较
internal/evaluation/feedback          反馈记录、哈希、纠正链和活动投影
internal/evaluation/rollout           版本化策略、quorum 和灰度演练决策
internal/observability                Noop Observer、Trace 上下文与线程安全有界 Recorder
internal/adapters/summarymock         默认离线摘要器
internal/adapters/summaryevalmock     摘要失败、恶意引用、危险动作等评测行为
internal/adapters/volcark             火山方舟 Responses API 适配器
internal/adapters/sqlite/summary_quota.go  LLM 请求/Token 预留、结算与成本熔断账本
internal/application/summary.go       摘要输入投影、引用校验和确定性回退
internal/application/runbook.go       Worker 后处理的 SOP 查询、引用派生与安全降级
```

## 当前边界与已知限制

- 代码已经具备真实 SLS 适配器，但本仓库没有试点账号、Project、LogStore、真实 Schema 或凭据，因此尚未声称真实环境联调通过。
- 真实适配器不再链接阿里云 Go SDK。它解析一次可信 `aliyun` 可执行文件，不经 shell 调用，强制固定 Profile、禁用插件自动安装、移除子进程中的 AK/SK/Token 覆盖变量，并限制 stdout/stderr 和调用时长。
- CLI 成功 JSON 通常不保证返回 Provider Request ID；系统使用独立本地执行 ID 做内部关联，Provider Request ID 只有在 CLI 明确返回时才进入审计。该变化降低了云侧工单关联能力，但不会削弱 ACL、预算、Schema、Checkpoint 或证据完整性门禁。
- 手工写入的 STS Profile 会过期。到期查询会失败并保留安全错误/审计，需要值班人员续签后再按现有 `NEEDS_REVIEW` 与成本确认规则处理；当前模式不是无人值守的长期凭据方案。
- SLS `limited` 表示 SQL 的结果行限制，不等同于发生截断；两个总数查询显式 `LIMIT 1`，两个维度查询显式 `LIMIT 5`，不会仅凭该元数据误判证据不足。
- 适配器每个声明的元数据或聚合调用只启动一次 CLI，不在应用内自动重试。M4-A 会复用已落盘窗口；若进程在查询可能执行后、Checkpoint 提交前崩溃，则进入 `NEEDS_REVIEW` 而不是自动重试，所以系统仍不承诺 Provider exactly-once。
- SLS 没有为多次 `GetLogsV2` 暴露跨请求快照令牌。飞书命令和 `sls-smoke` 默认分析“截至消息时刻前 10 秒”的等长窗口，Gateway 对尚未越过配置水位的请求 fail closed；同一窗口还会用前后两次计数做一致性门禁，计数变化时绝不形成确定性结论。10 秒是可配置的运维假设而非 Provider 完整性证明，生产试点必须根据实际采集/索引延迟调大。
- 扫描字节和真实费用无法在执行前精确获知；当前在返回后用 `processed_bytes` 做硬门禁，超限结果只能成为非结论性证据。
- 查询仅返回聚合，不返回原始日志。通用敏感信息识别不可能完全可靠，生产前仍需按企业字段规范补充脱敏模式。
- Schema 缓存过期后刷新失败会 fail closed，不会无限使用旧 Schema。
- SQLite 继续用于本地技术验证；卡片发送只有分类后的有限本地重试，不承诺 exactly-once。M4-B 已提供安全死信重放、本地租户额度/成本代理熔断和审批状态合同；多实例卡片全局顺序、生产数据库、组织级全局配额、真实 DLQ RBAC 与审批执行仍在 M4-C。
- SQLite 技术预览当前没有 schema version、正式迁移和回滚工具；升级已有数据库前必须备份，本地试验环境可按阶段说明重建。
- M3 Change Catalog 是启动时加载的静态文件，不是已接通的发布平台、配置中心或 CMDB；关联候选、权重和阈值尚未经过企业历史故障集校准。
- M3-B 已用 Mock 聚合跑通指标/Trace 时间线，但没有真实 ARMS/CMS/Prometheus/OTel 连接器、原始 Trace 下钻或拓扑；相关性不会被表述成已确认根因。
- 受治理 SOP 已有严格 Evidence/请求窗口绑定、Mock Source、可信来源标记、独立 5 秒超时、可信服务时钟、Worker 双重校验、持久化投影和带 Mock 标题的飞书纯文本展示，但没有真实 `RunbookSource`、企业内容、审批/失效、租户授权、审计或检索质量验收。它只供人工核查，不提供 URL、命令、按钮或自动处置。
- M5-A 数据集没有真实故障和专家标注，只能发现已编码合成场景上的回归；它不测量生产泛化能力，也不能批准灰度。M5-B/B3 已补齐合成 Engine 执行的有界 Trace、版本合同、append-only 历史与兼容运行比较，但真实反馈、真实数据集、团队阈值、试点群和回滚验收仍属于 M5-C。
- M5-B/B3 不是飞书接单、SQLite Worker、SLS 网络请求到卡片投递的跨进程分布式 Trace，也没有生产 Trace 后端、采样/保留策略或延迟 SLO。内容哈希用于完整性检测，不是加密、签名或身份认证。
- Eino Graph 和 `evaluate` 仍是确定性、无 LLM 的；Runbook 也是 Worker 校验后的可选后处理，不进入现有评测、Trace 或 Replay 版本合同，因此历史数据集与版本指纹不因该投影自动变化。Worker 后处理还实现了证据约束摘要和 SQLite 请求/Token 额度治理，默认走 Mock；火山方舟协议/认证、单个合成 count-only 摘要和 DAM 真实计数 Evidence 的 Worker 联合 E2E 已真实联调通过。真实 Prompt/模型质量门禁、Token 价格校准、生产全局额度、留存门禁及真实飞书联合 E2E 仍需试点验收。
- 非文本消息、格式错误的命令和永久无效事件目前会被安全确认但不会回复用法提示；这是已知的交互限制。
- 系统只有只读调查能力，不包含自动处置工具。

文档入口见 [`docs/README.md`](docs/README.md)。其中 `spec.md` 是唯一当前规范，M0～M3 是历史阶段归档，M4-A 文档记录第五期已完成的恢复切片，M5-A 文档记录第六期的全合成离线评测门禁，M5-B 文档记录第七至九期 B1～B3 的事件、回放和比较合同；这些都不代表完整生产验收。完整路线图见 [`docs/roadmap.md`](docs/roadmap.md)，迁移前生成的方案、用例图和 Canvas 文件保存在 [`artifacts/`](artifacts/README.md)。

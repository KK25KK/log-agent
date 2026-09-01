# 证据约束的 LLM 报告摘要

| 项目 | 状态 |
| --- | --- |
| 阶段 | 必需 LLM 摘要独立切片 |
| 代码状态 | provider-neutral 端口、Mock、Worker 接线、飞书展示与火山方舟适配器已完成 |
| 离线状态 | 单元测试与 `mock-e2e` 已覆盖；默认 0 网络、0 凭据 |
| 真实状态 | 2026-09-01 已用专用模型级 Key 完成一次独立真实 `llm-smoke`；协议/认证/结构化合同通过，Prompt/费用/留存审批、真实样本质量和联合 E2E 仍待验收 |

## 1. 目标与边界

LLM 只负责把已经验证的确定性报告改写得更易读，不能参与查询规划、权限、完整性、置信度、原因 verdict 或自动处置。确定性 `Findings`、`Evidence`、`CauseAnalysis` 和 `Recommendations` 永远是事实来源；`Report.Summary` 是可删除、可降级的附加投影。

发送给模型的 `domain.SummaryInput` 只包含：

- outcome；
- 有界 Finding Code、陈述、置信度、确定性标记和 Evidence ID；
- Evidence ID、current/baseline 名称、完整性、错误总量与已治理 Top Error；
- CauseAnalysis 状态、已有候选 ID/陈述/verdict/限制；
- 确定性 Recommendation Code、陈述和 Evidence ID。

明确不发送：飞书 App/Tenant/User/Chat/Message，物理 Endpoint/Project/LogStore/ResourceID，Query ID/Hash/SQL/SPL，原始日志，Provider 原始错误，凭据、Token 或 AccessKey。Top Error 在进入这里之前已经过 Query Gateway 的长度和敏感模式脱敏。

`Report.RunbookGuidance` 也明确不进入 `SummaryInput`：SOP 的标题、owner、revision、条目 ID、步骤、执行模式和指纹都不会发送给模型。模型不能选择、改写、排序或补写 SOP；它只能从确定性 `Recommendations` 的关闭 Code 集合中选择下一步。

## 2. 代码路径

```text
Worker
  -> ValidateEngineOutput（确定性报告）
  -> RunbookService.Enrich（可选；只读、人工核查指引）
  -> ValidateEngineOutput（含 RunbookGuidance）
  -> SummaryService.BuildSummaryInput
  -> SummaryQuotaStore.Reserve（可信租户 / 请求 / Token）
  -> ports.ReportSummarizer
       -> summarymock.Summarizer（默认，0 网络）
       -> volcark.Summarizer（显式配置，Responses API）
  -> SummaryQuotaStore.Settle（实际 Token 或 UNKNOWN 预留）
  -> 严格解析和引用解析
  -> ValidateEngineOutput（含 Summary）
  -> Store.FinishSuccess
  -> Feishu 卡片有界展示
```

| 责任 | 源代码 |
| --- | --- |
| 领域输入、Provider 输出和最终摘要 | `internal/domain/summary.go` |
| Provider 无关接口 | `internal/ports/ports.go` 的 `ReportSummarizer` |
| 安全投影、引用校验、确定性解析和 fallback | `internal/application/summary.go` |
| Worker 生命周期与心跳内调用 | `internal/application/worker.go` |
| 默认确定性 Mock | `internal/adapters/summarymock/summarizer.go` |
| 火山方舟 Responses API | `internal/adapters/volcark/summarizer.go` |
| 配置与组装 | `internal/config/config.go`、`cmd/logagent/summary.go`、`cmd/logagent/main.go` |
| 飞书展示 | `internal/adapters/feishu/renderer.go` |
| 离线端到端证据 | `cmd/logagent/mock_e2e.go` |

## 3. 输出约束

模型只能返回五个字段：`phenomenon`、`phenomenon_evidence_ids`、`cause_hypothesis_id`、`evidence_notes`、`recommendation_codes`。JSON 使用未知字段拒绝和尾随内容拒绝。

- 所有 Evidence ID 必须存在于当前报告且不能重复。
- 原因只能选择已有 `SUPPORTED_CANDIDATE`；最终原因正文从确定性报告反查，不使用模型自行生成的原因。
- Recommendation 只能选择已有 Code；最终下一步正文和 Evidence 绑定从确定性报告反查。
- Recommendation Code 的允许集合只来自确定性 `Recommendations`，不从 `RunbookGuidance` 生成或扩展。
- URL、凭据形态、代码围栏和显式危险动作文本会被拒绝。
- 输出集合、文本长度、Request ID、模型名、Token 和耗时都有上限或结构校验。

模型超时、限流、非 2xx、响应过大、非法 JSON、未知字段、虚构引用或危险内容都不会让调查失败。应用会保存 `status=FALLBACK`、`mode=FALLBACK` 的确定性摘要，且不保存 Provider 错误正文。

受治理 SOP 同样不会因为模型失败而被模型替换或改写：它由 Worker 在模型调用前独立检索和校验，展示层只呈现已经通过领域校验的有界纯文本人工参考。

## 4. Mock 与真实方舟切换

默认配置：

```powershell
$env:LOG_AGENT_LLM_MODE = "mock"
go run ./cmd/logagent worker
```

Mock 复用相同输入/输出合同、Worker 校验、SQLite 报告和飞书渲染，不发网络请求、不需要凭据，Token 全为 0。

真实方舟必须显式配置：

```powershell
$env:LOG_AGENT_LLM_MODE = "volcengine"
$env:ARK_API_KEY = "<secret-manager injection>"
$env:LOG_AGENT_ARK_MODEL = "<approved inference endpoint id>"
$env:LOG_AGENT_ARK_BASE_URL = "https://ark.cn-beijing.volces.com/api/v3"
$env:LOG_AGENT_LLM_TIMEOUT = "12s"
go run ./cmd/logagent worker
```

启用 Worker 前先独立验收：

```powershell
go run ./cmd/logagent llm-check
go run ./cmd/logagent llm-smoke
```

`llm-check` 只在本机检查模式、凭据是否存在、模型 ID、固定方舟地址、超时、Prompt 版本和额度配置，网络调用为 0，也不会输出 Key。`llm-smoke` 使用一份仓库内构造的 count-only 合成确定性报告，经过正式 `SummaryService`、输入安全校验和临时 SQLite 额度账本，对方舟发起且只发起一次请求；它不会访问 SLS 或飞书，也不会持久化 Prompt、模型正文或凭据。输出只包含 Provider/模型、请求 ID、Prompt 指纹、Token、耗时、引用数量和下一步 Code，不打印自然语言模型正文。

模型输出、Token usage 或引用合同不合法时，正式摘要逻辑仍生成确定性 fallback；但 `llm-smoke` 会以非零状态退出，不能把 fallback 误记为真实模型验收通过。Smoke 通过也只证明协议、认证和单个合成样本满足结构合同，不代表真实故障摘要质量达标。

### 当前 DAM 试点选择

| 配置 | 当前选择 |
| --- | --- |
| Region | `cn-beijing` |
| Responses API | `https://ark.cn-beijing.volces.com/api/v3/responses` |
| Model ID | `doubao-seed-2-0-mini-260428` |
| 选择原因 | 当前账号已开通；支持 Responses API 与结构化输出；轻量、低成本，适合只改写受治理计数报告 |
| Key 权限 | 使用独立 Key，并只授权 `Doubao-Seed-2.0-mini`，不使用“后续模型自动全选” |

该选择不把模型升级权交给运行时。切换 Model ID、Prompt 或 Provider 必须重新运行 `summary-evaluate`、`llm-check` 和 `llm-smoke`，并重新评估费用与输出忠实度。

适配器 POST 到 `${LOG_AGENT_ARK_BASE_URL}/responses`，设置 `store=false`、关闭深度思考、固定输出 Token 上限、客户端总超时、禁止重定向和应用层重试。请求通过 `text.format` 提交严格 JSON Schema，把五字段合同前移到模型生成层；返回后仍由 Go 执行 JSON、Evidence 引用、候选原因和建议 Code 的第二层业务校验。错误只映射为有界状态/诊断码，不包含响应正文或 API Key。当前使用标准库 HTTP，是为了让 SDK 依赖和模型 Provider 都停留在小适配层；若将来改用官方 Go SDK，`ReportSummarizer` 和上层合同不变。

### 2026-09-01 独立真实 Smoke 结果

| 项目 | 结果 |
| --- | --- |
| 状态 | `PASSED` |
| Provider / 模型 | `volcengine_ark` / `doubao-seed-2-0-mini-260428` |
| 输入 | `SYNTHETIC_COUNT_ONLY`，只包含仓库内构造的确定性聚合证据 |
| 调用范围 | 方舟 1 次；SLS 0 次；飞书 0 次 |
| 输出合同 | `GENERATED / MODEL`，Evidence 引用 2 个，建议 Code 1 个 |
| Token | input 796，output 175，total 971 |
| 延迟 | 1939 ms |
| Request ID | `resp_021788257778514f1aca4493aa6c7087a6ccfbfdb4be6158938f8` |
| 数据与秘密 | `store=false`；模型正文未打印；Key 未写入文件/Git，运行后清空剪贴板和进程环境 |

这个结果证明专用 Key、公开 Responses API、模型权限、结构化输出和生产 `SummaryService` 契约在一个合成样本上闭环。它不证明真实 DAM 日志摘要质量、Token 单价/账单、组织留存合规、生产配额或飞书联合链路已验收。控制台操作时必须从“API Key”列显示并复制密钥；“资源 ID”列的复制值不是鉴权凭据。

## 5. 真实接入前必须完成

1. 在密钥系统中托管 `ARK_API_KEY`，只注入 Worker，不进入 `.env`、日志、Trace、SQLite 或 Git。
2. 确认批准的方舟推理接入点 ID、地域、网络出口、超时和 QPS。
3. 对固定 Prompt 做安全评审；Prompt 变化必须更新 `EvidenceSummaryPromptVersion`，并观察新指纹。
4. 确认方舟侧输入/输出留存、跨境/地域、审计和删除策略；代码里的 `store=false` 不能代替平台合同确认。
5. 依据批准模型和真实 Prompt 校准 `LOG_AGENT_LLM_QUOTA_*`。当前已实现 SQLite 请求/Token 固定窗额度与熔断，但它不是跨实例生产全局额度，也不是火山账单。
6. 用脱敏样本执行 opt-in smoke，验证完成响应形状、Request ID、Token 和 fallback；再在试点飞书卡片验收中文展示。
7. 用 `summary-evaluate` 保持合成安全门禁全绿，再为真实历史故障标签增加摘要可读性、忠实度和成本评测。`evaluate` 仍是 Engine 级确定性评测，不经过 Worker 摘要，因此 `prompt_used=false` 是正确事实。

## 6. 验收与能宣称的效果

离线可验证：

- 模型输入不含已列禁止字段；
- Mock 主链生成 `GENERATED/MOCK` 摘要且外部调用为 0；
- Mock 主链在 Provider 前预留一笔摘要请求并结算零 Token；额度拒绝和重放都不会调用 Provider；
- 方舟 HTTP 请求使用固定地址形状、Bearer、`store=false` 和固定输出上限；
- 未知字段、伪造 Evidence/Recommendation、危险动作和 Provider 错误安全降级；
- 摘要失败不改变 `SUCCEEDED`，确定性报告保持不变；
- 飞书卡片只展示有界、转义后的摘要，并继续展示原报告。
- `summary-evaluate` 的 9 类合成场景验证原报告不变、输入隐私、引用/原因/建议合同、fallback 和调用预算。

2026-08-24 在加入受治理 SOP 的当前工作树上实跑 `summary-evaluate`，结果仍为 `PASSED`，数据集指纹保持 `82e813aed0721f15b89a19b053da6b1d47509ab07f45122af4ed0c075e60a0b1`；同轮 `mock-e2e` 也通过。该结果验证 SOP 没有进入 `SummaryInput` 或改变现有摘要评测数据集，但仍不代表真实方舟模型质量。

尚不能宣称：真实 DAM 样本质量已达标、真实 Token 单价/账单已校准、Prompt 已获组织批准、方舟数据留存已满足要求、生产 Worker/飞书联合 E2E 已通过、模型可判断根因或执行处置。

结构化门禁能阻止未知 Evidence、原因候选、Recommendation 和危险动作，但不能从形式规则证明任意自然语言改写在语义上完全无幻觉。真实启用前必须补充脱敏历史故障、专家忠实度标签和团队批准阈值；用户界面仍应把确定性 Findings/Evidence 作为事实来源。

## 7. 官方接口依据

- 火山方舟 Responses API 快速入门：<https://www.volcengine.com/docs/82379/1795150>
- Responses API 工具与输出结构：<https://www.volcengine.com/docs/82379/1958524?lang=zh>
- 火山引擎官方 Go SDK：<https://github.com/volcengine/volcengine-go-sdk>

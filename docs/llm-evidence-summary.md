# 证据约束的 LLM 报告摘要

| 项目 | 状态 |
| --- | --- |
| 阶段 | 必需 LLM 摘要独立切片 |
| 代码状态 | provider-neutral 端口、Mock、Worker 接线、飞书展示与火山方舟适配器已完成 |
| 离线状态 | 单元测试与 `mock-e2e` 已覆盖；默认 0 网络、0 凭据 |
| 真实状态 | 火山方舟 Key、批准模型、Prompt/费用/留存审批和真实 smoke 尚未提供 |

## 1. 目标与边界

LLM 只负责把已经验证的确定性报告改写得更易读，不能参与查询规划、权限、完整性、置信度、原因 verdict 或自动处置。确定性 `Findings`、`Evidence`、`CauseAnalysis` 和 `Recommendations` 永远是事实来源；`Report.Summary` 是可删除、可降级的附加投影。

发送给模型的 `domain.SummaryInput` 只包含：

- outcome；
- 有界 Finding Code、陈述、置信度、确定性标记和 Evidence ID；
- Evidence ID、current/baseline 名称、完整性、错误总量与已治理 Top Error；
- CauseAnalysis 状态、已有候选 ID/陈述/verdict/限制；
- 确定性 Recommendation Code、陈述和 Evidence ID。

明确不发送：飞书 App/Tenant/User/Chat/Message，物理 Endpoint/Project/LogStore/ResourceID，Query ID/Hash/SQL/SPL，原始日志，Provider 原始错误，凭据、Token 或 AccessKey。Top Error 在进入这里之前已经过 Query Gateway 的长度和敏感模式脱敏。

## 2. 代码路径

```text
Worker
  -> ValidateEngineOutput（确定性报告）
  -> SummaryService.BuildSummaryInput
  -> ports.ReportSummarizer
       -> summarymock.Summarizer（默认，0 网络）
       -> volcark.Summarizer（显式配置，Responses API）
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
- URL、凭据形态、代码围栏和显式危险动作文本会被拒绝。
- 输出集合、文本长度、Request ID、模型名、Token 和耗时都有上限或结构校验。

模型超时、限流、非 2xx、响应过大、非法 JSON、未知字段、虚构引用或危险内容都不会让调查失败。应用会保存 `status=FALLBACK`、`mode=FALLBACK` 的确定性摘要，且不保存 Provider 错误正文。

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

适配器 POST 到 `${LOG_AGENT_ARK_BASE_URL}/responses`，设置 `store=false`、固定输出 Token 上限、客户端总超时、禁止重定向和应用层重试。错误不会包含响应正文或 API Key。当前使用标准库 HTTP，是为了让 SDK 依赖和模型 Provider 都停留在小适配层；若将来改用官方 Go SDK，`ReportSummarizer` 和上层合同不变。

## 5. 真实接入前必须完成

1. 在密钥系统中托管 `ARK_API_KEY`，只注入 Worker，不进入 `.env`、日志、Trace、SQLite 或 Git。
2. 确认批准的方舟推理接入点 ID、地域、网络出口、超时和 QPS。
3. 对固定 Prompt 做安全评审；Prompt 变化必须更新 `EvidenceSummaryPromptVersion`，并观察新指纹。
4. 确认方舟侧输入/输出留存、跨境/地域、审计和删除策略；代码里的 `store=false` 不能代替平台合同确认。
5. 设定 Token/请求数/失败率/时延预算。当前模型调用尚未纳入 SQLite 租户额度，不能直接大规模开放。
6. 用脱敏样本执行 opt-in smoke，验证完成响应形状、Request ID、Token 和 fallback；再在试点飞书卡片验收中文展示。
7. 为真实历史故障标签增加摘要质量与安全评测。目前 `evaluate` 是 Engine 级确定性评测，不经过 Worker 摘要，因此 `prompt_used=false` 是正确事实。

## 6. 验收与能宣称的效果

离线可验证：

- 模型输入不含已列禁止字段；
- Mock 主链生成 `GENERATED/MOCK` 摘要且外部调用为 0；
- 方舟 HTTP 请求使用固定地址形状、Bearer、`store=false` 和固定输出上限；
- 未知字段、伪造 Evidence/Recommendation、危险动作和 Provider 错误安全降级；
- 摘要失败不改变 `SUCCEEDED`，确定性报告保持不变；
- 飞书卡片只展示有界、转义后的摘要，并继续展示原报告。

尚不能宣称：真实模型质量已达标、真实 Token 费用已测量、Prompt 已获组织批准、方舟数据留存已满足要求、模型可判断根因或执行处置。

## 7. 官方接口依据

- 火山方舟 Responses API 快速入门：<https://www.volcengine.com/docs/82379/1795150>
- Responses API 工具与输出结构：<https://www.volcengine.com/docs/82379/1958524?lang=zh>
- 火山引擎官方 Go SDK：<https://github.com/volcengine/volcengine-go-sdk>

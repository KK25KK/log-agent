# `error_count_v1` 轻量模板开发与验收记录

## 目标

在不修改既有 `error_analysis_v2` 的前提下，为 DAM 单主 Logstore 增加只依赖 `env + level` 的计数型调查能力。真实 SLS 只做只读连接、Schema 和聚合 Smoke；LLM 与飞书继续使用确定性 Mock。

## 已实现合同

| 层 | `error_count_v1` 行为 |
| --- | --- |
| 飞书命令 | `/investigate <service> <environment> <duration> [template]`；省略模板仍默认 `error_analysis_v2` |
| Resource Catalog | 资源绑定 `error-count-v1`；要求固定 selectors 和独立 error_selector；拒绝 error/instance 维度字段 |
| Query Gateway | 校验请求模板 ID 与资源模板版本精确匹配；按模板执行 2 calls / 2 rows 预检 |
| SLS CLI Adapter | 仅编译并执行 count-before 与 count-after；不生成 `GROUP BY`，不读取 `msg` |
| 完整性 | 前后计数不同即 `Incomplete`，不能生成确定性突增结论 |
| Evidence | `PatternLimit=0`、`InstanceLimit=0`，TopError/Patterns/Instances 为空且非穷尽 |
| Engine | 只输出当前/基线计数、差值/倍数、突增判断、证据与安全建议 |
| 原因/时间线/SOP | 均显式 `INCONCLUSIVE`，并对 Change、OperationalSignal、Runbook Source 保持零调用 |
| LLM | 只通过 `analysis_scope=count_only` 接收脱敏计数事实；Mock 不产生 PossibleCause |
| 飞书 | Mock 卡片证据页显示“分析范围：仅错误计数”“错误类型/实例分布：本模板不适用” |
| Checkpoint/Quota | 缓存合同和 SQLite 校验按注册模板分支；每个计数观察预留/结算 2 次 API 调用 |

## 兼容边界

- 旧请求没有 `template_id` 时按 `error_analysis_v2` 解释。
- `error_analysis_v2` 仍保持 4 calls、Top 5 错误类型、Top 5 实例和原有评测合同。
- 全局查询预算只要求正值，具体模板所需预算在 Preflight 阶段校验。
- 计数模板不能用于输出错误类型、热点实例、变更根因、数据库根因或统一事件时间线。

## Mock 验收

```powershell
go run ./cmd/logagent mock-e2e error_count_v1
```

预期关键值：

- `safety.external_network_calls=0`
- `feishu.mode=mock`
- `llm_summary.mode=MOCK` 且 `external_api_calls=0`
- `aliyun_sls.logical_observations=2`
- `aliyun_sls.provider_api_calls=4`
- `query_step_checkpoints=2`
- `operational_signals.source_calls=0`
- `runbook_knowledge.source_calls=0`
- Investigation 为 `SUCCEEDED`，Summary 的 `possible_cause` 为空

这些结果只证明应用内部链路与安全合同，不代表真实飞书、火山方舟或阿里云生产数据。

## 真实 SLS 验收

使用 `config/sls-resources.dam-pilot.example.json` 和本机 `default` StsToken Profile：

```powershell
$env:LOG_AGENT_SLS_MODE = "aliyun"
$env:LOG_AGENT_SLS_CATALOG = ".\config\sls-resources.dam-pilot.example.json"
$env:LOG_AGENT_SLS_CLI_PROFILE = "default"
go run ./cmd/logagent sls-check

$env:LOG_AGENT_SMOKE_APP_ID = "replace_with_feishu_app_id"
$env:LOG_AGENT_SMOKE_TENANT_KEY = "replace_with_feishu_tenant_key"
$env:LOG_AGENT_SMOKE_USER_ID = "replace_with_feishu_open_id"
go run ./cmd/logagent sls-smoke dam-server test 10m
```

`sls-check` 只检查 Project、Logstore、`env` 与 `level` 索引；`sls-smoke` 只执行一个计数观察的两次聚合。

2026-09-01 实际执行结果为通过：

| 检查项 | 实际结果 |
| --- | --- |
| `sls-check` | 成功解析 `dam-server-test-count`；Project/Logstore 可访问；Standard 模式；读取到 4 个索引字段 |
| `sls-smoke` | `progress=Complete`、`complete=true`、`usage_known=true` |
| 查询合同 | 固定 2 次 API 调用；Pattern/Instance 上限均为 0；没有维度或原始日志 |
| 真实边界 | 只证明当前 STS、权限、Schema、固定计数聚合和治理链路可用 |
| Mock 边界 | LLM 摘要与飞书入站/卡片投递仍为确定性 Mock，未访问火山方舟或飞书 OpenAPI |

Smoke 返回的具体计数随时间窗变化，不作为稳定验收值归档，也不构成故障或根因结论。

## 工程自检

- `gofmt -w .`
- `go test -count=1 ./...`
- `go vet ./...`
- `go run ./cmd/logagent evaluate`
- `go run ./cmd/logagent summary-evaluate`
- `go run ./cmd/logagent mock-e2e`
- `go run ./cmd/logagent mock-e2e error_count_v1`

以上检查均通过；其中两个 E2E 命令的 LLM 与飞书均为 Mock。`go test -race ./...` 是否可执行取决于本机 CGO 与 C 编译器，本记录不把未执行的 Race 检查写成已通过。

## 非目标

- 不展开 StoreView，不并发查询 DAM 8 个 Logstore。
- 不采样或传输原始 `msg`。
- 不增加 `error_type`、`instance_id` 或错误指纹。
- 不连接真实火山方舟或真实飞书。
- 不自动执行修复、SOP 或高风险动作。

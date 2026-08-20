# LLM 摘要租户配额与成本熔断

## 状态

- 阶段：证据摘要上线前治理切片
- 当前状态：代码与离线验收完成
- 数据边界：默认只使用 `summarymock`；不读取火山方舟凭据，不访问网络
- 生产边界：SQLite 是单库技术预览，不是跨地域、跨数据库实例的全局额度服务

## 目标

在 `SummaryService` 调用任何模型 Provider 前，用可信飞书 `AppID + TenantKey` 派生的不可逆租户标识原子预留：

- 1 次摘要请求；
- 每请求固定的保守 Token 额度。

Provider 返回后记录实际 input/output/total Tokens；Provider 结果不确定时保留预留额度，避免把可能已经发生的费用错误释放。额度拒绝、重复 usage key 或账本不可用都只触发确定性摘要回退，不让调查失败。

## 固定状态机

```text
                provider success
RESERVED ------------------------------> SETTLED(actual tokens)
    |
    | timeout / cancellation / transport error
    +-----------------------------------> UNKNOWN(reserved tokens)

quota denied / replay / ledger failure
    -> no provider call
    -> deterministic FALLBACK
```

`SETTLED` 和 `UNKNOWN` 都计入窗口用量。`UNKNOWN` 按预留 Token 计量。重复的 `investigation_id + prompt_version` usage key 不会再次调用 Provider。本切片不声称 Provider exactly-once；它选择“可能少生成一次模型摘要，也不静默重复计费”。

## 组件与职责

| 组件 | 职责 |
| --- | --- |
| `domain.SummaryQuotaPolicy` | 固定窗、请求上限、Token 上限、每请求预留 Token |
| `ports.SummaryQuotaStore` | 原子预留、终态结算、窗口用量查询 |
| `application.SummaryService` | 输入安全门禁、额度预留、Provider 调用、独立结算、确定性回退 |
| `adapters/sqlite` | 预留记录与追加式事件账本；不保存 Prompt、Evidence 正文、凭据或 Provider 错误 |
| `cmd/logagent` | 从环境配置策略并把同一个 Store 注入生产 Worker |

## 配置合同

| 变量 | 默认值 | 含义 |
| --- | ---: | --- |
| `LOG_AGENT_LLM_QUOTA_WINDOW` | `1h` | 固定 UTC 额度窗口 |
| `LOG_AGENT_LLM_QUOTA_MAX_REQUESTS` | `100` | 每租户每窗口摘要请求上限 |
| `LOG_AGENT_LLM_QUOTA_MAX_TOKENS` | `409600` | 每租户每窗口 Token 上限 |
| `LOG_AGENT_LLM_QUOTA_RESERVED_TOKENS` | `4096` | 每次 Provider 调用前预留 Token |

预留 Token 必须小于等于窗口 Token 上限。真实模型启用前应依据批准模型、Prompt 长度、输出上限和试点价格重新校准，默认值不是费用承诺。

## 安全与审计

- 租户 ID 只由可信入站信封的 `AppID + TenantKey` 计算，不接受消息文本或模型输入。
- usage key 只绑定调查 ID 与固定 Prompt 版本，不含 Prompt、Evidence 或用户文本。
- 账本仅保存稳定 reason code、计数和时间，不保存 API Key、请求体、响应体、自然语言内容或原始 Provider 错误。
- Provider 成功但 Token 超过预留时仍结算真实用量，并拒绝采用该模型输出。
- 真实模型模式缺失 Token usage 时按未知成本保留预留额度并回退；只有确定性 Mock 允许零 Token。
- 结算使用短时独立 context；调用 context 已取消时仍尽力保存可能发生的外部成本。

## 离线验收

- 正常 Mock：预留 1 次、结算 0 Token、摘要为 `GENERATED/MOCK`。
- 配额耗尽：Provider 调用为 0，摘要为 `FALLBACK`，拒绝事件可审计。
- 重复 usage key：第二次 Provider 调用为 0。
- Provider 失败或超时：状态为 `UNKNOWN`，按预留 Token 计量，调查仍成功并回退。
- Provider 返回超预留 Token：实际 Token 被记录，输出回退。
- 不安全输入：在预留与 Provider 前拒绝，两个调用计数均为 0。
- 并发预留不能突破请求或 Token 上限。
- `mock-e2e` 输出 1 次 LLM 请求、0 实际 Token、0 凭据和 0 网络调用。

验收命令：

```powershell
gofmt -w .
go test -count=1 ./...
go vet ./...
go test -count=50 -run 'SummaryQuota|LLMQuota|MockE2E|BuildSummaryService' ./internal/application ./internal/adapters/sqlite ./internal/config ./cmd/logagent
go run ./cmd/logagent mock-e2e
go run ./cmd/logagent summary-evaluate
go run ./cmd/logagent evaluate
```

以上离线验收已通过。`go test -race ./...` 未执行，因为当前 Windows 环境 `CGO_ENABLED=0` 且没有 GCC；不能把这一项写成已通过。

## 仍待真实系统输入

- 火山方舟批准模型、真实 Prompt 审批与 opt-in smoke；
- 真实 input/output Token 与费用校准；
- 生产关系库迁移和多实例全局原子额度；
- 组织级租户策略、告警阈值、额度管理 UI 与运维 RBAC；
- 真实 Provider 对 timeout/取消的计费语义和账单核对。

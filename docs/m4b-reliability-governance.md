# M4-B：投递恢复与租户治理

状态：主体代码与全离线验收完成；真实飞书故障码校准、多实例全局额度、生产数据库和真实审批执行留待 M4-C。

## 1. 这期解决什么

M4-A 已经确保“付费查询结果未知时不自动重查”，但此前仍有三类缺口：

1. 飞书发送错误一律按相同方式重试，永久错误会浪费尝试；
2. 投递进入 `DEAD` 后只能直接查数据库，没有安全的运维查询和重放合同；
3. 单次查询有预算，但同一租户连续发起调查没有持久化额度和成本代理熔断；
4. 路线图要求高风险审批边界，但当前系统又必须继续保持只读。

M4-B 在不接真实系统的前提下完成以上合同。它复用现有 `delivery_events`，没有创建第二条消息队列。

## 2. 投递失败分类

适配器只能返回以下闭集：

| 分类 | 处理 |
| --- | --- |
| `RETRYABLE` | 有限指数退避，达到最大次数后进入 `DEAD` |
| `PERMANENT` | 立即进入 `DEAD` |
| `OUTCOME_UNKNOWN` | 远端可能已成功；依赖稳定 Reply UUID/同卡 Patch 做有限重试，仍按 at-least-once 声明 |
| `CANCELLED` | 父任务取消时不提交误导性失败，保留租约恢复语义 |

飞书适配器把 429/5xx 归为可重试，把 408/504/调用超时归为结果未知，把本地卡片合同和大部分 4xx 归为永久错误。持久层只记录稳定 reason code，不保存 SDK 错误、响应正文、凭据或卡片内容。

每次成功、计划重试或进入死信都会追加一条 `delivery_attempts` 记录。业务调查成功与否不受卡片投递结果反向修改。

## 3. 死信查询与安全重放

```powershell
go run ./cmd/logagent delivery-dlq-list --db .\data\logagent.db --limit 50
go run ./cmd/logagent delivery-dlq-replay --db .\data\logagent.db `
  --delivery-id delivery:inv_xxx:SUCCEEDED --operator ops-user-1
```

列表只返回 ID、调查 ID、事件类型、尝试次数、稳定 reason code、更新时间和可重放判断。

重放在一个 SQLite 事务中重新检查：

- 记录仍为 `DEAD`；
- `interaction_rebound` 绝不重放；
- 初始 `QUEUED` 回执在尚无卡片且仍有 source message 时可以重放，用于解除后续投递阻塞；
- 已有卡片的事件必须仍绑定当前卡片，并且不存在更高 sequence 的投影；
- 旧 `RUNNING`、派生 `QUEUED` 或任何可能覆盖新终态的事件 fail closed。

允许的重放会清空旧租约、把尝试计数归零并追加 `delivery_operations` 审计。它仍是 at-least-once，不承诺飞书远端 exactly-once。

## 4. 租户额度与成本代理熔断

Worker 启动时把受治理 Executor 包在 `QuotaExecutor` 内，再放入 M4-A `CheckpointExecutor`。因此：

```text
Checkpoint reuse
    -> 不进入 QuotaExecutor，不重复计费

缺失步骤
    -> Resolve governance
    -> SQLite 原子额度预留
    -> governed SLS executor
    -> 成功：结算实际 API calls / processed bytes
    -> 确定性前置拒绝：释放
    -> 外部结果未知：保留预留额度并标 UNKNOWN
```

租户键由可信 `app_id + tenant_key` 做 SHA-256 派生，不存原始飞书身份。usage key 绑定 investigation、current/baseline 名称和治理指纹；相同 key 第二次出现会在 Provider 前拒绝，而不是重复查询。

默认固定 UTC 一小时窗口：

| 环境变量 | 默认值 |
| --- | ---: |
| `LOG_AGENT_TENANT_QUOTA_WINDOW` | `1h` |
| `LOG_AGENT_TENANT_QUOTA_MAX_OBSERVATIONS` | `100` |
| `LOG_AGENT_TENANT_QUOTA_MAX_API_CALLS` | `400` |
| `LOG_AGENT_TENANT_QUOTA_MAX_PROCESSED_BYTES` | `8589934592` |
| `LOG_AGENT_TENANT_QUOTA_RESERVED_BYTES` | `268435456` |

观察数、API 调用数或 processed bytes 任一达到上限，后续观察都在 Provider 之前得到 `tenant query quota exceeded`。`processed_bytes` 只是费用代理，不是阿里云账单；SQLite 额度也不是跨多实例、跨地域的生产全局配额。

## 5. 高风险审批合同

审批状态机为：

```text
PENDING -> APPROVED -> CONSUMED
        -> REJECTED
        -> EXPIRED
```

请求只保存可信租户哈希、调查 ID、闭集 action、不可变 payload SHA-256、申请人、过期时间和决策人。申请人与审批人必须是同一 App/Tenant 下的不同用户；批准只能消费一次且 payload hash 必须完全一致。

本期没有注册 `READ_RAW_LOG_SAMPLE` 或 `EXECUTE_REMEDIATION` 的真实执行器，也没有飞书审批 UI。因此即使数据库中存在 `APPROVED`，当前服务也不会执行外部写操作。真实审批身份、授权策略、工具参数保管和执行审计属于 M4-C。

## 6. 离线验收

- 分类后的永久失败立即进入死信；可重试/未知错误最多执行配置次数。
- 成功、重试和死信尝试均有追加式审计。
- 初始回执可安全重放；存在更新投影的旧进度事件不可重放。
- 原子额度预留在超限时执行零次 Provider 调用。
- 成功结算实际 usage；外部结果未知保留费用代理；确定性前置拒绝释放预留。
- 相同 usage key 不会二次访问 Provider。
- 审批要求职责分离、哈希一致、有效期内一次性消费。
- `mock-e2e` 经过真实 QuotaExecutor，仍保持 2 个逻辑观察、8 个 Provider 调用代理、0 网络、0 凭据。

## 7. 尚未证明

- 飞书真实租户的 429/业务错误码与重试窗口需要试点校准；
- SQLite 没有正式 migration、备份和多实例全局额度能力；
- Provider 费用不等于 processed bytes，无法据此输出人民币金额；
- DLQ 重放没有组织级 RBAC，当前 operator ref 只是本地审计字段；
- 没有真实审批目录、审批卡、写工具或自动处置；
- 没有生产网络故障、进程强杀、主备切换和灾备演练。

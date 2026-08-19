# M2 错误突增调查闭环实现归档

> 历史归档说明：本文记录 M2 当时完成的代码与离线验收。源码路径默认指向当前工作树；当前行为以 [`spec.md`](spec.md) 为准。仓库未提供真实 SLS/飞书凭据，因此本期不能称为生产或日常可用。

## 1. 本期结果

M2 把前两期的“可靠任务骨架 + 受控 SLS 查询”组合成第一个代码与离线闭环完成、具备真实试点条件的场景：用户在飞书发起调查后，系统对当前窗口和等长基线窗口做固定分析，生成错误突增、错误模式占比、候选新增模式、实例集中度和下一步建议，并用同一张飞书卡片展示排队、运行和最终状态。

本期仍是只读系统。没有任意 SQL/SPL、原始日志展示、模型决定权限或自动处置。

```text
飞书 /investigate
  -> SQLite 幂等接单 + QUEUED 卡片事件
  -> Worker 领取任务 + RUNNING 卡片事件
  -> Eino 固定 Graph
       -> 当前窗口 error_analysis_v2
       -> 等长基线 error_analysis_v2
       -> 确定性证据校验与报告
  -> SQLite 原子保存 Evidence/Report + 终态卡片事件
  -> 飞书 Delivery Worker 回复或更新同一张卡片
  -> 查看证据 / 取消 / 扩大窗口 / 重新运行
```

## 2. 错误分析如何实现

### 2.1 固定模板，而不是模型生成查询

每个观察窗口固定执行四次只读聚合：

1. 前置错误总数，最多 1 行；
2. Top 5 错误模式；
3. Top 5 实例；
4. 使用完全相同条件再次读取错误总数，最多 1 行。

因此一个窗口最多 4 次 API 调用、12 行聚合结果；当前窗口和基线合计最多 8 次调用、24 行聚合结果。调用方只能提交服务、环境和时间范围，Endpoint、Project、LogStore、字段、过滤条件和模板版本均来自管理员资源目录。

资源目录新增 `instance_field`。`error_field` 和 `instance_field` 都必须是开启统计的 text 索引字段，Schema 不符合时会在日志查询之前拒绝。

### 2.2 数据质量门禁

每个结果必须同时满足：

- 四次固定调用全部返回，且前后两次错误总数一致；
- SLS `progress=Complete`；
- Provider Request ID、处理行数、处理字节数和耗时元数据齐全；
- 聚合桶非空、计数为正、不重复、不超过 Top 5，且桶计数之和不超过错误总数；
- API 调用数、返回行数和处理字节数没有越过策略预算。

SLS 的 `isAccurate` 表示是否启用纳秒级有序，不是分析结果“是否准确”，因此只保存为排序元数据，不参与结论门禁。Top 5 是模板有意设置的边界，也不等同于 Provider 截断。

SLS 没有提供跨多次 `GetLogsV2` 的快照令牌。系统采取两层保护：飞书命令把窗口结束锚定在消息时刻前的配置水位（默认 10 秒、最小 3 秒），Gateway 对未越过该水位的调用 fail closed；前后计数门禁再用于发现实时索引恰好在聚合调用之间发生变化。不一致时保留四次请求与用量证据，但把 `Progress` 降为 `Incomplete`，报告只能是 `data_insufficient`。配置水位仍是运维假设，不是 Provider 完整性证明；真实试点必须按实际采集/索引延迟校准。

### 2.3 新错误模式不会被夸大

“当前 Top 5 中出现、基线 Top 5 中未出现”默认只称为候选新增模式。只有基线所有错误都被聚合桶覆盖，且参与比较的标签都没有被脱敏时，系统才会确认“相对所选基线窗口新增”。系统不会把 Top 5 未命中表述成历史首次出现。

### 2.4 报告是确定性的

M2 不需要 LLM 才能运行。错误增长倍数、错误占比、实例占比和 Top 5 覆盖率都由 Go 代码从受控聚合结果计算；Finding 和 Recommendation 都使用固定 code，并显式引用 Evidence ID。LLM 以后只能解释已经通过门禁的证据，不能修改查询、权限或事实。

## 3. 飞书闭环如何实现

### 3.1 持久化状态事件

调查事务会同时写入窄用途的 `delivery_events`：

- 接单时写 `QUEUED`；
- Worker 领取时写 `RUNNING`；
- 成功、失败或取消时写对应终态。

独立 Delivery Worker 按顺序领取事件。第一次使用飞书 Reply API 创建交互卡，后续使用 Patch API 更新同一个 `card_message_id`。发送任务有租约、attempt fencing、有限退避和 DEAD 终态；发送失败不会反向修改已经提交的调查结果。

如果初始 `QUEUED` 接单卡最终进入 `DEAD`，后续更新继续阻塞，因为远端还没有可 Patch 的卡片；如果只是中间 `RUNNING` 进度最终发送失败，终态可以跳过该进度继续投递，避免报告永久滞留。

初始 Reply 使用稳定 UUID 降低重复发送概率，但这不构成跨任意时长的 exactly-once 保证。通用 Outbox、错误分类、运维重放和死信工具仍属于 M4。

### 3.2 卡片动作安全边界

飞书回调只信任 SDK 信封中的 App ID、Tenant Key、操作者 Open ID、Chat ID、Card Message ID 和 Event ID。按钮 value 只能包含固定动作和调查 ID，不能携带时间窗口、倍率、资源、字段或查询文本。

应用层会重新读取 SQLite 并验证：

- 卡片确实绑定到该 App、Tenant 和 Chat；
- 操作者等于原调查的可信请求者；
- 卡片当前绑定的调查等于按钮中的调查；
- 当前状态允许该动作；
- 变更动作具有 Event ID。

`rerun` 和 `expand_window` 使用 `card:<event_id>` 作为持久化幂等键。回调重放只会返回第一次创建的派生调查。扩大时间窗由服务端固定计算为原窗口的两倍并受 `MaxWindow` 限制；用户不能从按钮注入任意时间范围。

“查看证据”和“返回报告”是只读投影，可以直接由回调返回 `card_json`。取消、扩大窗口和重新运行属于变更动作，回调只返回 Toast；新的业务状态由 Delivery Worker 按持久化事件更新卡片。这样状态卡只有一个写入者，不会由旧进度回调覆盖新派生调查。

### 3.3 支持的按钮

| 动作 | 行为 |
| --- | --- |
| 查看证据 | 读取已经持久化的脱敏聚合 Evidence，不查询 SLS |
| 返回报告 | 从证据页返回当前报告 |
| 取消 | 将 QUEUED/RUNNING 调查持久化为 CANCELLED，Worker 在心跳边界停止 |
| 扩大时间窗 | 创建一个两倍窗口的新调查，复用当前卡片 |
| 重新运行 | 使用相同服务、环境和时间范围创建一个新调查，复用当前卡片 |

## 4. 关键代码位置

```text
internal/domain/query.go                 M2 固定模板和调用/行数常量
internal/adapters/aliyunsls/backend.go   四次聚合查询、前后计数门禁与 Provider 结果归一化
internal/application/query/gateway.go    ACL、Schema、预算、脱敏和查询审计
internal/adapters/eino/engine.go         当前/基线分析与确定性报告
internal/adapters/sqlite/delivery.go     卡片路由、顺序事件、租约与 fencing
internal/application/delivery.go         有限重试的 Delivery Worker
internal/application/actions.go          按钮鉴权、取消和派生调查
internal/adapters/feishu/                 JSON 2.0 卡片、Reply/Patch 和 callback 映射
cmd/logagent                              Worker、飞书入口和 Delivery Worker 组装
```

Eino、飞书 SDK 和阿里云 SLS SDK 仍分别只允许存在于对应适配层，架构测试会阻止 SDK 类型进入核心业务。

## 5. 本地验证

离线 Demo 永远使用 Mock SLS：

```powershell
go run ./cmd/logagent demo
```

Mock 当前窗口为 120 条错误、基线为 20 条，并包含错误模式与实例分布。输出会包含 6 倍突增、各模式/实例占比、候选或确认新增模式以及确定性建议；这些是测试数据，不是阿里云生产结果。

完整离线自检：

```powershell
Get-ChildItem -Recurse -Filter *.go | ForEach-Object { gofmt -w $_.FullName }
go test -count=1 ./...
go vet ./...
go run ./cmd/logagent demo
```

`go test -race ./...` 在 Windows 上需要 `CGO_ENABLED=1` 和可用的 C 编译器。

截至 2026-08-19 的本机自检结果：

| 检查 | 结果 |
| --- | --- |
| 全仓 Go 格式化 | 通过 |
| `go test -count=1 ./...` | 通过 |
| `go vet ./...` | 通过 |
| SQLite、应用层、飞书、查询网关、阿里云 SLS 和 Eino 相关包连续 20 次测试 | 通过 |
| `go run ./cmd/logagent demo` | 通过，输出 M2 三类统计、四次 API 合并的分析报告 |
| `go test -race ./...` | 未执行成功：本机 `CGO_ENABLED=0`，启用后仍缺少 `gcc` |
| 真实 SLS / 飞书端到端 | 未执行：仓库未提供试点资源和凭据 |

## 6. 真实试点前的配置

资源目录需要新增真实实例字段，例如：

```json
{
  "template_version": "error-analysis-v2",
  "error_field": "error_code",
  "instance_field": "pod_name"
}
```

默认 M2 查询预算：

| 环境变量 | 默认值 | 含义 |
| --- | ---: | --- |
| `LOG_AGENT_SLS_REQUEST_TIMEOUT` | `15s` | 单次 SDK HTTP 请求超时 |
| `LOG_AGENT_SLS_QUERY_TIMEOUT` | `45s` | 一个窗口四次聚合的应用总时限 |
| `LOG_AGENT_SLS_INGESTION_GRACE` | `10s` | 窗口结束相对消息时刻的索引安全水位，最小 `3s` |
| `LOG_AGENT_SLS_MAX_ROWS` | `12` | 一个窗口最大聚合行数 |
| `LOG_AGENT_SLS_MAX_API_CALLS` | `4` | 一个窗口固定调用数 |
| `LOG_AGENT_DELIVERY_SEND_TIMEOUT` | `8s` | 单次飞书发送超时 |
| `LOG_AGENT_DELIVERY_MAX_ATTEMPTS` | `5` | 本地卡片发送最大尝试数 |
| `LOG_AGENT_DELIVERY_WORKER_ID` | `feishu-delivery-local` | 发送 Worker 身份 |
| `LOG_AGENT_DELIVERY_POLL_INTERVAL` | `500ms` | 空闲轮询间隔 |
| `LOG_AGENT_DELIVERY_LEASE_DURATION` | `30s` | 卡片事件租约，必须大于发送超时 |
| `LOG_AGENT_DELIVERY_RETRY_BASE` | `2s` | 指数退避基数 |

真实联调顺序仍是：`sls-check` 验证资源和 Schema，`sls-smoke` 验证固定查询，再启动共享同一数据库的 `worker` 与 `feishu` 进程。

## 7. 验收状态与限制

代码和离线测试可以验证：四次固定聚合及前后计数门禁、当前/基线报告、数据质量降级、卡片事件顺序、进度失败后终态可继续投递、租约回收、旧 attempt fencing、请求者鉴权、回调重放幂等、派生调查卡片 CAS 和窗口上限。

仓库没有真实阿里云试点资源、RAM 凭据或飞书应用凭据，因此不能声称以下项目已经通过：

- 真实 LogStore 上的四次聚合、计数一致性假设与字段兼容性；
- 真实飞书客户端中的卡片样式、Reply/Patch 和按钮回调；
- Windows race test（当前环境无 CGO/C 编译器）。

SQLite 仍用于技术验证。M2 部署契约是单个飞书/Delivery 进程；多实例远端 Patch 的全局顺序、生产数据库、多实例全局配额、步骤级查询幂等、通用通知 Outbox 和自动处置不在 M2 范围内。

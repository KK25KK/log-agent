# Log Agent

这是一个用 Go 开发的“证据驱动”日志调查 Agent。M0～M3、M4-A 可恢复查询切片、M5-A 全合成离线评测门禁和第七期 M5-B/B1 Agent 事件与版本合同的主体代码与离线测试已经完成；M4-B/M4-C、M5-B/B2 回放历史、M5-B/B3 趋势比较和 M5-C 真实灰度仍未完成。由于仓库没有真实飞书/SLS/发布平台凭据、生产数据库、历史故障标注集与试点资源，当前只能称为“具备试点条件”，不能称为日常可用或生产可用。

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
- 唯一生产查询模板 `error_analysis_v2`；用户和模型不能提交 Project、LogStore、SQL 或 SPL。
- 执行前校验时间窗、固定调用数、返回行数、超时和单进程并发。
- 通过 `GetIndex` 校验字段索引；错误维度和实例维度都必须是开启统计的 text 字段。
- 使用官方 `github.com/aliyun/aliyun-log-go-sdk v0.1.126` 执行四条只读聚合查询：前置错误总数、Top 5 错误模式、Top 5 实例和后置错误总数。
- 保存四次 Provider Request ID、Progress、纳秒有序元数据、处理行数、处理字节数和耗时；前后总数变化时按证据不足处理。
- `Incomplete`、结构不一致、截断、元数据缺失或扫描字节超预算时禁止生成确定性结论。
- 查询标签做长度限制，并脱敏邮箱、IPv4、Bearer/JWT 和常见 AccessKey 形态。
- SQLite 追加式查询审计，记录拒绝、开始、成功、证据不足和失败，不保存凭据、原始日志或原始 SQL。
- `sls-check` 定向检查 Project、LogStore、Standard 模式和字段 Schema；不依赖有分页上限的全量资源列表。
- 显式 `sls-smoke` 查询命令。
- SDK 依赖边界架构测试。

### 飞书调查闭环

- 调查状态事务同步写入 `QUEUED/RUNNING/SUCCEEDED/FAILED/CANCELLED` 卡片事件。
- 独立 Delivery Worker 使用租约、attempt fencing 和有限退避发送卡片。
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

### M5-B/B1 Agent 事件与版本合同

- `evaluate` 在执行 Case 前生成统一版本清单，并对规范化清单计算 SHA-256；清单绑定数据集、Graph、查询模板/策略、原因方法、评测门禁、Trace/Replay Schema、执行 Profile 和真实 Prompt 使用情况。
- `agent-trace-v1` 使用关闭枚举的 `RUN / GRAPH_NODE / TOOL` 事件；每个 Case 固定包含一个 Engine 根 Span、四个 Graph 节点、current/baseline 两个工具 Span，确有变更源调用的 Case 再增加一个 `change_source.list` Span。
- 默认 Observer 是 Noop；评测使用线程安全有界 Recorder。遥测无效或溢出不会改变调查结果，但 Trace 会变成不完整并使离线门禁失败。
- Trace 只保存有界身份、稳定代码、时间、耗时、哈希和调用/字节计数，不保存飞书消息或身份、资源、查询、日志/桶标签、变更摘要、自然语言报告、回调、Provider 原始错误、Prompt 正文或任意属性。
- 当前只完成 Engine/evaluation 级 Trace，不是飞书入站到卡片投递的分布式 Trace，也不是生产可观测、采样/保留策略或 SLO。`evaluation-replay-v1` 目前只是未来快照的版本合同；B2 存储/命令和 B3 比较均未实现。完整设计与验收见 [`docs/m5-agent-observability-replay.md`](docs/m5-agent-observability-replay.md)。

## 先运行飞书 + SLS 双 Mock

下面这条命令会走完整的本地纵向链路，不读取环境变量、不需要飞书 App、阿里云账号或任何凭据，也不会发起网络请求：

```powershell
go run ./cmd/logagent mock-e2e
```

它会依次完成：

1. 模拟飞书用户发送 `/investigate order-service prod 30m`；
2. 将同一消息重放一次，验证 Inbox 幂等去重；
3. 通过真实 SQLite 状态机创建并领取调查任务；
4. 通过真实 Worker + Eino 固定 Graph、资源 ACL、Schema/预算网关和查询审计调用 Mock SLS Backend；
5. 将 current、baseline 两个规范化聚合结果写入真实 SQLite Checkpoint；
6. 生成 Evidence、M2 报告和 Mock 变更关联账本；
7. 通过真实 Delivery Worker 模拟飞书卡片 `REPLY(QUEUED) -> PATCH(RUNNING) -> PATCH(SUCCEEDED)`。

输出中的 `safety.external_network_calls=0`、`credentials_required=false` 表示当前运行完全离线；`schema_calls=1`、`backend_execute_calls=2`、`provider_api_calls=8`、`query_audit_events=4` 和 `query_step_checkpoints=2` 分别证明固定 Schema、当前/基线观察、四聚合调用元数据、开始/终态审计和两个持久化步骤已经经过真实应用链路。固定的 120/20 条错误、发布事件和飞书标识全部是测试数据，不代表真实阿里云或飞书结果。完整 Mock 边界见 [`docs/local-mock-e2e.md`](docs/local-mock-e2e.md)，恢复合同见 [`docs/m4-recoverable-query-steps.md`](docs/m4-recoverable-query-steps.md)。

## 运行第六/七期离线评测与 Trace 门禁

```powershell
go run ./cmd/logagent evaluate
```

该命令加载仓库内置的 `synthetic-m5a-v1` 数据集，对每个 Case 组装独立 Fixture Mock，并执行真实确定性 Eino Graph。它不读取环境变量中的外部凭据，也不会访问飞书、阿里云 SLS、发布平台或模型服务。

命令输出结构化 JSON，包含数据集身份与指纹、规范化版本清单及其 SHA-256、逐 Case 完整 Agent Trace、聚合指标和门禁状态。通过时退出码为 `0`；数据集非法、Graph 执行失败、标签不一致、出现意外确定性结论、证据引用失效、Trace 不闭合/丢事件，或查询/成本代理合同越界时返回非零退出码。脚本和 CI 应检查退出码，不能只匹配输出文字。

评测数字只描述受控合成回归集：它不是历史真实故障或专家标注上的准确率，`processed_bytes`/API 调用数不是阿里云账单，本机耗时也不是生产 SLO。

## 只查看离线报告 Demo（可选）

环境要求：Go 1.26 或更高版本。Demo 永远使用 Mock SLS 和内置 Mock Change Source，不需要飞书、阿里云或发布平台凭证。

```powershell
go run ./cmd/logagent demo
```

输出是一份 JSON 调查报告，包含结论、资源与模板版本、查询指纹、完整性、统计证据和 M3 原因分析。Mock 中固定为当前窗口 120 条错误、基线 20 条，并放入一个影响 `order-pod-a` 的发布事件，因此会得到 6 倍突增和一个由固定规则计算出的变更关联候选；这些都是测试数据，不是生产日志或真实发布结果。

## 配置 M3 Change Catalog（可选）

Change Catalog 是管理员维护的只读治理配置，不是发布平台连接器。复制示例并设置 Worker 环境变量：

```powershell
Copy-Item .\config\change-catalog.example.json .\config\change-catalog.json
$env:LOG_AGENT_CHANGE_CATALOG = ".\config\change-catalog.json"
```

Catalog 中的 `resource_id` 必须与 `sls-resources.json` 的资源 ID 完全一致。当前只支持 `RELEASE` 和 `CONFIG`；每个事件必须提供起止时间、负责人、摘要、受影响实例列表和 `affected_instances_complete`。单个列表最多 20 个实例，一次调查最多读取 10 个重叠候选。

不设置 `LOG_AGENT_CHANGE_CATALOG` 时 M3 原因增强明确显示为 `UNAVAILABLE`，M2 报告仍可正常完成。文件在 Worker 启动时严格校验并一次性加载，当前不支持热重载；完整字段和判定规则见 [`docs/m3-change-correlation-evidence.md`](docs/m3-change-correlation-evidence.md)。

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

### 2. 配置凭据与预算

项目不会自动加载 `.env`；请在进程环境中设置变量。短期联调可以使用资源级 RAM 用户或 STS，生产优先 ECS RAM Role/短期凭据。

最小只读 RAM 策略模板见 `config/sls-readonly-policy.example.json`。它只包含定向检查和查询需要的 `GetProject`、`GetLogStore`、`GetIndex`、`GetLogStoreLogs`，请替换地域、账号、Project 和 LogStore 占位符；不要给 Agent `AliyunLogFullAccess`。

```powershell
$env:LOG_AGENT_SLS_MODE = "aliyun"
$env:LOG_AGENT_SLS_CATALOG = ".\config\sls-resources.json"
$env:LOG_AGENT_SLS_CREDENTIAL_MODE = "static"
$env:ALIBABA_CLOUD_ACCESS_KEY_ID = "your-access-key-id"
$env:ALIBABA_CLOUD_ACCESS_KEY_SECRET = "your-access-key-secret"
$env:ALIBABA_CLOUD_SECURITY_TOKEN = "your-sts-token" # 长期 RAM AK 时可不设
```

使用 ECS RAM Role 时：

```powershell
$env:LOG_AGENT_SLS_CREDENTIAL_MODE = "ecs_ram_role"
$env:LOG_AGENT_SLS_ECS_RAM_ROLE_NAME = "your-role-name"
```

默认门禁：

| 配置 | 默认值 | 含义 |
| --- | ---: | --- |
| `LOG_AGENT_SLS_REQUEST_TIMEOUT` | `15s` | 单次 SDK HTTP 请求硬超时 |
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
go run ./cmd/logagent mock-e2e
go run ./cmd/logagent demo
```

`go test -race ./...` 在 Windows 上需要 C 编译器并启用 `CGO_ENABLED=1`。

默认测试全部离线，不读取云凭据、不访问 SLS 或发布平台。只有显式运行 `sls-check`、`sls-smoke`，或以 `LOG_AGENT_SLS_MODE=aliyun` 启动 Worker，才会访问真实 SLS。

本轮 M5-B/B1 验收中，`gofmt`、`go test -count=1 ./...`、`go vet ./...`、`evaluate` 和 `mock-e2e` 均通过。`evaluate` 的 5/5 个合成 Case 全部通过，`trace_contract_accuracy=1`；共记录 76 个事件、13 个工具 Span、0 个丢弃事件，并与 10 次逻辑 SLS 观察、40 次 Provider 调用代理、3 次 Change Source 调用和 78,080 processed bytes 完全核对。数据集指纹为 `caf2714c80a646c5da15134c6557879565ffc8e083a66da1f1c9e49d3d0dc1f8`，本次规范化版本指纹为 `14db14acf992ebd06d9d4d71f89056be2a2b984baeb6bf5de2c136db442f7c53`。这些仍只是全合成 Mock 的离线工程回归结果。`go test -race ./...` 未执行，因为当前 Windows 环境 `CGO_ENABLED=0` 且未安装 GCC，不能写成已通过。

## 代码边界

```text
cmd/logagent                          进程组装与诊断命令
internal/application                 接单、调查 Worker、查询 Checkpoint、卡片 Delivery 和动作控制用例
internal/application/query           ACL、预算、Schema、审计与脱敏网关
internal/domain                      领域数据、资源、查询、原因假设、证据账本、Agent 事件与版本清单模型
internal/ports                       Store、Engine、QueryGateway、SLSBackend、ChangeSource、AgentObserver 接口
internal/adapters/eino               唯一允许导入 Eino 的包
internal/adapters/feishu             唯一允许导入飞书 SDK 的包
internal/adapters/feishumock         离线飞书收件与卡片投递模拟，不导入 SDK
internal/adapters/aliyunsls           唯一允许导入阿里云 SLS SDK 的包
internal/adapters/resourcecatalog     JSON 资源目录与静态 ACL
internal/adapters/changecatalog       M3 版本化发布/配置变更目录
internal/adapters/sqlite              本地持久化、查询审计、查询 Checkpoint 与卡片事件
internal/adapters/slsmock             离线确定性数据
internal/adapters/evalmock            M5-A 逐 Case 的 SLS 与 Change Source Fixture Mock
internal/evaluation                   严格合成数据集、质量/Trace 指标和离线门禁
internal/observability                Noop Observer、Trace 上下文与线程安全有界 Recorder
```

## 当前边界与已知限制

- 代码已经具备真实 SLS 适配器，但本仓库没有试点账号、Project、LogStore、真实 Schema 或凭据，因此尚未声称真实环境联调通过。
- 官方 Go SDK 的 `GetLogsV2` 不接收调用方 `context.Context`。当前关闭 SDK 的 500/502/503 自动重试，同时把 HTTP timeout 和 SDK 内部 GET 网络错误重试窗口都限制为配置值，并在调用返回后再次检查 deadline；四次顺序查询由独立的总查询时限约束，但用户取消仍不能保证底层 HTTP 立即中止。
- 内置 ECS RAM Role Provider 使用官方推荐的 IMDSv2 加固模式获取元数据 Token，再读取临时凭据；请求有有限 timeout、缓存凭据且禁止代理和重定向。该模式仍需在你的 ECS 试点上做真实验证。
- SLS `limited` 表示 SQL 的结果行限制，不等同于发生截断；两个总数查询显式 `LIMIT 1`，两个维度查询显式 `LIMIT 5`，不会仅凭该元数据误判证据不足。
- 付费的四个 POST 聚合请求不会在 SDK 内自动重试，因此正常执行的 `api_calls=4` 同时代表四个逻辑请求和四个物理请求；元数据 GET 遇到网络错误时仍可能在配置的短窗口内重试。M4-A 会复用已落盘窗口；若进程在查询可能执行后、Checkpoint 提交前崩溃，则进入 `NEEDS_REVIEW` 而不是自动重试，所以系统仍不承诺 Provider exactly-once。
- SLS 没有为多次 `GetLogsV2` 暴露跨请求快照令牌。飞书命令和 `sls-smoke` 默认分析“截至消息时刻前 10 秒”的等长窗口，Gateway 对尚未越过配置水位的请求 fail closed；同一窗口还会用前后两次计数做一致性门禁，计数变化时绝不形成确定性结论。10 秒是可配置的运维假设而非 Provider 完整性证明，生产试点必须根据实际采集/索引延迟调大。
- 扫描字节和真实费用无法在执行前精确获知；当前在返回后用 `processed_bytes` 做硬门禁，超限结果只能成为非结论性证据。
- 查询仅返回聚合，不返回原始日志。通用敏感信息识别不可能完全可靠，生产前仍需按企业字段规范补充脱敏模式。
- Schema 缓存过期后刷新失败会 fail closed，不会无限使用旧 Schema。
- SQLite 继续用于本地技术验证；卡片发送只有有限本地重试，不承诺 exactly-once。M2 按单个飞书/Delivery 进程部署；多实例卡片发送的全局顺序、Delivery 死信安全重放、生产数据库、全局配额和审批仍在 M4-B/M4-C。
- SQLite 技术预览当前没有 schema version、正式迁移和回滚工具；升级已有数据库前必须备份，本地试验环境可按阶段说明重建。
- M3 Change Catalog 是启动时加载的静态文件，不是已接通的发布平台、配置中心或 CMDB；关联候选、权重和阈值尚未经过企业历史故障集校准。
- M3 不增加 SLS 查询，也没有版本分布、首次出现时间、Trace、指标、拓扑或知识库证据；相关性不会被表述成已确认根因。
- M5-A 数据集没有真实故障和专家标注，只能发现已编码合成场景上的回归；它不测量生产泛化能力，也不能批准灰度。M5-B/B1 只补齐合成 Engine 执行的有界 Trace 和版本合同；append-only 回放历史、`replay` 命令、趋势比较和真实反馈仍未实现，真实数据集、团队阈值、试点群和回滚验收属于 M5-C。
- M5-B/B1 不是飞书接单、SQLite Worker、SLS 网络请求到卡片投递的跨进程分布式 Trace，也没有生产 Trace 后端、采样/保留策略或延迟 SLO。
- 当前 Eino Graph 是确定性、无 LLM 的，因此版本清单明确为 `prompt_used=false`，不生成 Prompt/Token 指标；如果未来引入模型，必须先补版本、成本和安全评测合同。
- 非文本消息、格式错误的命令和永久无效事件目前会被安全确认但不会回复用法提示；这是已知的交互限制。
- 系统只有只读调查能力，不包含自动处置工具。

文档入口见 [`docs/README.md`](docs/README.md)。其中 `spec.md` 是唯一当前规范，M0～M3 是历史阶段归档，M4-A 文档记录第五期已完成的恢复切片，M5-A 文档记录第六期的全合成离线评测门禁，M5-B 文档记录第七期已完成的 B1 事件/版本合同以及仍未开始的 B2/B3；这些都不代表完整生产验收。完整路线图见 [`docs/roadmap.md`](docs/roadmap.md)，迁移前生成的方案、用例图和 Canvas 文件保存在 [`artifacts/`](artifacts/README.md)。

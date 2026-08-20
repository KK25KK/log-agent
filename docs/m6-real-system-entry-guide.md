# 真实系统接入地图（第七期前置）

这份文档只聚焦“哪段代码应该接真实外部系统”，并把 mock 相关链路与生产接入链路分开。
目标是让你在新同事、SRE 或审核时，一眼看出：**要把真实飞书、真实阿里云 SLS、真实数据库接在哪里，接进哪个接口，不会突破边界**。

## 一、先说结论

可以直接进入真实系统的链路目前已经存在，核心是**配置 + 启动参数切换 + 生产化存储替换**：

1. 飞书入口与卡片投递：已经是 SDK 真实代码路径（`runFeishu`）。
2. SLS 查询：默认是 `mock`，配置 `LOG_AGENT_SLS_MODE=aliyun` 即走真实 `aliyunsls`。
3. 查询治理（资源目录/ACL/Schema/预算/审计）：`query` Gateway 已经接管真实执行前门禁。
4. 持久化状态机：当前仍是 `sqlite`，但这是已知技术预览；真实化要实现 DB 适配器并替换启动文件里的 `sqlite.Open`。
5. 变更来源（M3）：默认 `disabled`，可配静态 Change Catalog 文件；真实接平台还未建模。

> 关键约束：不允许把飞书 SDK 或阿里云 SDK 引入业务核心层。接口边界由 `internal/ports` 保护；真实/离线实现只切换在适配层和启动组装处。

## 二、代码入口总览（从入口命令开始）

### 2.1 调用链路：worker

- 命令：`cmd/logagent/main.go` 的 `runWorker` 分支（`go run ./cmd/logagent worker`）。
- 组装点：`cmd/logagent/sls.go` -> `buildWorkerExecutor` -> `application.NewCheckpointExecutor`。
- 执行器链：
  - `newMockExecutor()`：返回 `slsmock.Executor`（离线/合成），生产不要启用。
  - `buildAliyunDependencies()`：`resourcecatalog.Load` + `aliyunsls.New`，返回真实的 catalog/backend。
  - `queryapp.NewGateway(catalog, backend, auditor, budget)`：固定模板、固定预算、ACL、Schema、审计的一站式门禁层（最终会被 Worker/Engine 调用）。
- 引擎：`eino.New(...)` 只接 `application.GovernedSLSExecutor`，再注入 `WithChangeSource`。
- 状态执行：`application.NewWorker(...).RunOne` 读取 Store 的任务并触发 `engine.Run`。

### 2.2 调用链路：feishu

- 命令：`cmd/logagent/main.go` 的 `runFeishu` 分支（`go run ./cmd/logagent feishu`）。
- 接入层：
  - `internal/application.NewIntake` + `feishu.New(...)`（WS `im.message.receive_v1` 入站、`card.action.trigger` 回调）
  - `internal/adapters/feishu.New` + `NewSender`：真实飞书 WebSocket 与消息 API 适配器。
  - `application.NewDeliveryWorker(..., sender)`：把卡片写入持久 outbox 后异步发送。
- 与数据库强绑定：`runFeishu` 与 `runWorker` 使用同一 `DatabasePath`。

## 三、源代码接入点（按模块）

### 3.1 查询底座（真实 SLS）

#### 生产化路径（必须走）
- `cmd/logagent/sls.go`
  - `buildWorkerExecutor`
  - `buildAliyunDependencies`
  - `runSLSCheck` / `runSLSSmoke`（诊断命令）
- `internal/adapters/resourcecatalog/catalog.go`
  - `Load(config)`：`service + environment -> LogResource`
  - `Allowed(principal, resourceID)`：静态 ACL
- `internal/adapters/aliyunsls/backend.go`
  - `New(config)`：创建客户端与凭据；
  - `GetSchema(ctx, resource)`：预检 schema；
  - `Execute(ctx, query)`：执行四次聚合（count-before / top error / top instance / count-after）；
  - `CheckResources(ctx, resources)`：`sls-check` 诊断。
- `internal/application/query/gateway.go`
  - `NewGateway(..., budget)`；
  - `Execute`：完整 preflight + 资源解析 + ACL + schema + 并发 + backend 请求；
  - `ResolveQueryGovernance`：checkpoint 可复用时绑定治理身份指纹；
  - `QueryAuditor`：记录 STARTED/INCOMPLETE/SUCCEEDED/FAILED/DENIED 审计事件。

#### 当前离线/测试保留
- `internal/adapters/slsmock/*`：M0~M5 离线和评测 fixture 的 mock 后端。
- `internal/adapters/evalmock/*`：评测专用 fixture，通常只用于 `evaluate` 命令。

### 3.2 飞书入口与投递

#### 生产入口
- `internal/adapters/feishu/receiver.go`
  - `New(appID, appSecret, intake, opts...)`
  - `Run(ctx)`：WS 主循环
  - `handleMessage`：解析 `/investigate` 并调用 `Accept` 持久化（幂等键 `(app,tenant,message_id)`）
  - `handleAction`：按钮回调，映射为 `domain.ActionCommand`
- `internal/application/actions.go`：动作幂等、权限、状态与再运行规则。
- `internal/adapters/feishu/sender.go`
  - `NewSender(appID, appSecret)`（真实消息 API）
  - `Deliver(ctx, domain.DeliveryJob)`（先 reply 再 patch）
  - 卡片幂等由稳定 `uuid` + card_message_id 控制。
- `internal/application/delivery.go`：DeliveryWorker 从 outbox 拉任务并交给 `ports.DeliverySender`。

#### 离线模拟
- `internal/adapters/feishumock/*`：飞书收口/投递 mock，常用于 `mock-e2e`。

### 3.3 持久化（真实化必须替换）

#### 当前实现（技术预览）
- `internal/adapters/sqlite/store.go`：任务/状态主生命周期（`ports.Store`）
- `internal/adapters/sqlite/query_steps.go`：checkpoint（`ports.QueryStepStore`）
- `internal/adapters/sqlite/query_audit.go`：查询审计（`ports.QueryAuditor`）
- `internal/adapters/sqlite/delivery.go`：发送 outbox（`ports.DeliveryStore`）
- `internal/adapters/sqlite` 还负责 `NEEDS_REVIEW` 与已知并发边界。

#### 生产化替换
- 目前没有真实 DB adapter。要进入真实系统，需新增生产存储适配器（如 `internal/adapters/pg` / `internal/adapters/mysql`）并实现：
  - `ports.Store`
  - `ports.QueryStepStore`
  - `ports.QueryAuditor`
  - `ports.DeliveryStore`
- 组装点在 `cmd/logagent/main.go` 的 `runWorker` 与 `runFeishu`：
  - 替换 `sqlite.Open(config.DatabasePath)` 为新 adapter 的打开函数。
  - 保持接口返回值与现有 `application` 调用签名不变，避免改动上层逻辑。

### 3.4 变化源（M3 原因关联）

- `internal/adapters/changecatalog/catalog.go`
  - `Load(path)`：生产时接静态 JSON；
  - `List(ctx, query)`：以 resource_id/time 窗口返回事件；
- `cmd/logagent/sls.go` 的 `buildChangeSource`：空路径时回退 disabled 源（不会阻塞 M2 结果）。

## 四、按真实部署场景给你的“最小改动清单”

### 4.1 先切“真实查询”
1. 将 `LOG_AGENT_SLS_MODE=aliyun`。
2. 填 `LOG_AGENT_SLS_CATALOG` 为试点目录文件，确保 `service/environment` 对应单一试点。
3. 填 `LOG_AGENT_SLS_CREDENTIAL_MODE` 与 AK/STS 或 ECS RAM Role。
4. 先运行：
   - `go run ./cmd/logagent sls-check`
   - `go run ./cmd/logagent sls-smoke <service> <env> <window>`

### 4.2 进入 worker 的真实执行
1. 共享生产数据库（建议先沿用 sqlite 验证到位）。
2. `go run ./cmd/logagent worker` 在同一会话或不同实例多副本，观察 `NEEDS_REVIEW`、checkpoint 重用与 `QueryAudit`。
3. 只要 checkpoint/retry/审计链路稳定，再继续做数据库替换。

### 4.3 进入真实飞书入口
1. 企业自建应用 + WS 长连接，配置 `im.message.receive_v1`、`card.action.trigger`。
2. 分别启动：
   - `go run ./cmd/logagent feishu`
   - `go run ./cmd/logagent worker`
3. 两进程共享同一个数据库；`feishu` 负责入站和投递，`worker` 负责执行。

### 4.4 最终生产化（M4 后续）
1. 完成真实 DB Adapter 与迁移方案（含 schema 版本、回滚、备份）。
2. 补齐多租户/环境流量控制（目前是进程级预算）。
3. 接入真实变更平台前，先确认 `Change Catalog` 不被用户输入污染并满足试点约束。
4. 完成审批与灰度治理（属于后续阶段，不在本切片中）。

## 五、重要边界与不应接入的点

- `internal/adapters/eino` 是流程编排层，不是外部系统接入口；避免把 SDK、SQL、消息 API 混到此层。
- `internal/domain` 只承载模型，不应直接发网络请求。
- `evaluation` / `evalmock` 保持离线评测：`evaluate` 一般不接真实飞书和真实 SLS。
- `slsmock`、`feishumock` 仅用于离线；真实化后不应在生产 worker/feishu 路径使用。
- 所有“成功输出到生产”都要经过 `application.ValidateEngineOutput`（已供 Worker 与 evaluate 共用）以防止生产会拒绝但 offline 通过的假阳性。

## 六、对应文件速查（直接改时看这里）

```text
cmd/logagent/main.go           命令入口与 worker/feishu 进程
cmd/logagent/sls.go            SLS 查询模式切换、Catalog/Backend 组装、sls-check/smoke
internal/application           Intake / Worker / Checkpoint / Actions / Delivery
internal/application/query      Query Gateway（进入 SLS 前的唯一治理闸门）
internal/adapters/aliyunsls     阿里云 SLS SDK 适配层
internal/adapters/resourcecatalog 资源目录与 ACL
internal/adapters/feishu        飞书 SDK 适配层
internal/adapters/feishumock    飞书离线 mock
internal/adapters/slsmock       SLS mock
internal/adapters/evalmock      evaluate 专用 fixture mock
internal/adapters/sqlite         当前持久化技术预览（待替换为生产数据库）
internal/ports                  接口边界：Store / Query / Executor / ChangeSource / Delivery
```

## 七、建议的阶段标记（与你的文档口径对齐）

本文件只覆盖“进入真实系统的生产接入面”，不是新增新功能：

- 第三方 SDK 的实际接入面：已完成（飞书、阿里云都在适配层）。
- 实时链路可运行：依赖真实 credential、catalog 与生产数据库后可跑。
- 生产数据库替换：未完成；建议作为下一步 M4-B/M4-C。
- 真实发布平台/CMDB 关联：本阶段仍 `disabled/change-catalog-file`，不属于此切片的硬性前提。

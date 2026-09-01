# 二期（M1）实现归档：受控只读 SLS 查询底座

> 历史说明：本文归档 M1 当时的 `error_summary_v1` 两聚合实现。M2 已升级为 `error_analysis_v2`：三类统计结果通过四次 API 请求取得（count-before、patterns、instances、count-after），并纠正 SLS `isAccurate` 的语义：它表示纳秒级有序，不是分析准确性门禁。M1 没有独立 Git 源码快照，文中的路径指向后续演进后的当前树；当前行为以 [`spec.md`](spec.md) 和 M2 归档为准。

| 项目 | 内容 |
| --- | --- |
| 阶段 | M1 / 二期 |
| 状态 | 代码与离线验收完成；真实试点联调待配置 |
| 日期 | 2026-08-18 |
| 目标 | 在不开放任意 SQL/SPL 的前提下，让调查 Graph 能安全、可审计地查询指定阿里云 SLS 资源 |

## 1. 二期最终交付了什么

二期把一期的 `Mock SLS` 执行边界扩展成了可替换的两种运行模式：

```text
飞书可信身份 + 调查范围
        |
        v
Eino 固定调查 Graph
        |
        v
应用层 Query Gateway
  1. 解析管理员资源目录
  2. 默认拒绝 ACL
  3. 执行前预算门禁
  4. 获取并校验索引 Schema
  5. 日志聚合查询前先写 STARTED 审计
  6. 调用固定聚合模板
  7. 归一化质量与用量
  8. 脱敏并写终态审计
        |
        +---- mock：离线 Demo / 自动测试
        |
        +---- aliyun：官方 Go SDK / 真实 SLS
```

最终具备以下能力：

- 真实请求只能访问管理员配置的 `service/environment -> Endpoint/Project/LogStore` 映射，并强制携带独立的 `error_selector` 错误谓词。
- 权限按飞书 `AppID + TenantKey + UserID` 和资源 ID 绑定，未显式授权即拒绝。
- 调用方只能选择固定模板 `error_summary_v1`，不能传入 Endpoint、Project、LogStore、字段名、SQL 或 SPL。
- 真实模板只执行两次聚合读取：错误总数和 Top 错误维度，不返回原始日志正文。
- 查询前校验时间窗、结果行数、API 调用数、超时和单进程并发。
- 日志查询前通过 `GetIndex` 校验选择器字段和分析字段；错误维度必须是开启统计的 text 字段。Schema 元数据请求发生在 `STARTED` 日志查询审计之前。
- 查询后保留本地执行 ID，以及 CLI 暴露的 Progress、`isAccurate`、处理行数、处理字节数和耗时；Provider Request ID 不保证存在且不会伪造，这里的 `isAccurate` 当前只解释为纳秒级有序元数据。
- Provider 结果不完整、截断、缺少质量/用量元数据或处理字节超预算时，Evidence 自动降级为“不足以得出确定结论”。M1 当时曾误把 `isAccurate` 当作分析准确性；当前实现只把它保存为纳秒级有序元数据，不参与质量门禁。
- 查询标签离开网关前会做长度限制，并脱敏邮箱、IPv4、Bearer/JWT 和常见 AccessKey 形态。
- 脱敏只替换展示标签，不改变聚合计数；Evidence 会保留 `redacted=true`，但不会仅因标签被替换就丢弃有效的计数结论。
- 所有拒绝、开始、成功、证据不足和失败事件都写入追加式审计；审计表不保存凭据、原始日志或原始查询文本。
- 增加 `sls-check` 元数据检查命令和 `sls-smoke` 显式真实查询命令。

## 2. 为什么这样实现

### 2.1 Eino 继续只做编排

Eino Graph 仍只负责“构造当前窗口和基线窗口 -> 调用查询边界 -> 生成 Evidence -> 构造 Report”。ACL、预算、Schema、审计和脱敏全部在应用层 Query Gateway 完成，因此以后即使替换 Eino，也不会丢失安全边界。

### 2.2 真实 SDK 放在最窄适配层

只有 `internal/adapters/aliyuncli` 可以调用阿里云 CLI/SLS 插件。该适配器只暴露：

- 获取索引 Schema；
- 执行已经审核通过的固定聚合查询；
- 供运维诊断使用的资源元数据检查。

应用层和 Eino 都看不到 SDK 的请求类型，也不能直接绕过 Query Gateway。

### 2.3 不开放自然语言 SQL

M1 的目标是先把真实数据访问做安全。SQL 字段、运算、分组和限制都由代码内模板拥有，资源和值来自受控目录并经过校验与转义。自然语言理解可以在后续阶段负责提取意图或解释结果，但不能决定访问哪个 LogStore、使用哪些字段或绕过预算。

### 2.4 扫描成本分成前后两道门

时间窗、固定调用数、行数、超时和并发可以在执行前判断；真实扫描字节通常只有执行后才能获得。因此 M1 不声称能提前精确预测费用，而是在返回后以 `processed_bytes` 做质量门禁：超限结果会被保留为审计证据，但不能生成确定性结论。

## 3. 主要代码位置

| 位置 | 职责 |
| --- | --- |
| `internal/application/query/gateway.go` | 不可绕过的 ACL、预算、Schema、审计、质量判定和脱敏网关 |
| `internal/adapters/aliyuncli/backend.go` | CLI 进程边界、固定查询编译、Provider 元数据归一化 |
| `internal/adapters/resourcecatalog/catalog.go` | JSON 资源目录、字段校验、Principal ACL、默认拒绝 |
| `internal/adapters/sqlite/query_audit.go` | 追加式查询审计持久化 |
| `internal/domain/query.go` | 资源、Schema、ApprovedQuery 和 QueryAudit 领域模型 |
| `internal/domain/types.go` | 可信 Principal、QueryResult 和增强 Evidence |
| `internal/application/intake.go` | 从飞书入站信封覆盖并固化可信身份 |
| `internal/adapters/eino/engine.go` | 为当前/基线观察构造固定模板请求并映射 Evidence |
| `cmd/logagent/sls.go` | 真实 Backend/Gateway 组装、`sls-check`、`sls-smoke` |
| `internal/architecture/boundaries_test.go` | 限制 Eino、飞书和阿里云 SDK 的导入边界 |

## 4. 配置与运行方式

### 4.1 离线回归

```powershell
$env:LOG_AGENT_SLS_MODE = "mock"
go run ./cmd/logagent demo
```

此路径不读取阿里云凭据，也不会访问网络。输出中的 120/20 条错误是固定测试数据，不代表真实生产日志。

### 4.2 准备真实资源目录

```powershell
Copy-Item .\config\sls-resources.example.json .\config\sls-resources.json
```

需要替换：

- HTTPS Endpoint；
- Project 和 LogStore；
- 业务范围选择器字段和值；
- 必填且独立的错误谓词 `error_selector`，例如 `level=ERROR`；
- 开启统计的错误维度字段；
- 允许访问该资源的飞书 App、Tenant 和用户 Open ID。

资源目录只保存映射和 ACL，不保存 AccessKey、Secret 或 STS Token。

最小资源级 RAM 策略模板位于 `config/sls-readonly-policy.example.json`，仅包含 `GetProject`、`GetLogStore`、`GetIndex` 和 `GetLogStoreLogs`。

### 4.3 检查元数据

设置真实模式、目录和本地环境凭据后运行：

```powershell
go run ./cmd/logagent sls-check
```

该命令只检查可访问 Project、LogStore 和索引 Schema，不查询日志正文。

### 4.4 显式 Smoke 查询

将 Smoke Principal 配成目录中已授权的飞书身份，然后运行：

```powershell
go run ./cmd/logagent sls-smoke order-service prod 10m
```

Smoke 会经过与 Worker 相同的资源解析、ACL、预算、Schema、审计和脱敏链路。不要把 AccessKey 或 Secret 发到聊天中，只在本机环境变量或公司的密钥系统中设置。

## 5. 自动验收结果

本阶段离线验收覆盖：

- 可信身份覆盖请求中伪造的 Principal；
- 唯一资源映射、HTTPS Endpoint、严格字段名和默认拒绝 ACL；
- 未授权、超时间窗、非法 Schema 和审计启动失败时，真实查询调用次数为 0；
- 固定模板和目录值转义，不接受任意查询文本；
- Provider Incomplete、截断、用量缺失和字节超限的降级；历史 `isAccurate` 误读已从质量门禁移除；
- 即使 Provider 忽略 Context 并在应用 deadline 后返回成功，也不能形成完整 Evidence；
- SLS `limited` 元数据按“SQL 结果行限制”处理，不误判为截断；两个固定 SQL 都显式 `LIMIT 1`；
- CLI 错误在适配层转成只含操作、退出码和关闭错误码的安全错误，Provider message/body 不进入查询审计；
- 真实认证使用用户通过 SSO 获取并写入本机 CLI 的短期 `StsToken` Profile；应用不接收 AK/SK/Token；
- Schema TTL 缓存过期后失败关闭；
- 并发门禁和 permit 释放；
- 查询审计追加、隔离、重启持久化和列白名单；
- 官方 SDK 适配器的 Schema 映射、聚合响应映射和资源检查；
- 外部框架/SDK 导入边界；
- 原有接单幂等、租约、取消、Evidence 引用和离线 Demo 回归。

本次执行结果：

```text
go mod tidy                 PASS
gofmt                       PASS
go test -count=1 ./...      PASS
go vet ./...                PASS
```

`go test -race ./...` 需要 Windows 上启用 CGO 并安装 C 编译器，当前环境不满足，不能标记为通过。

## 6. 尚未完成的真实环境验收

代码具备真实 SLS 接口，但仓库中没有你的试点账号、资源位置、真实索引 Schema 和凭据，因此以下事项仍待你本地配置后验证：

- `sls-check` 能定向访问指定试点 Project、LogStore，确认 Standard 模式和索引字段；
- 已授权 Smoke Principal 能执行固定聚合；
- 未授权 Principal 在真实配置中保持调用前拒绝；
- 真实返回的 Progress、纳秒级有序和处理字节元数据符合预期，本地执行 ID 可追踪，Provider Request ID 若缺失则保持为空；
- RAM 策略只授予试点资源需要的只读权限；
- 企业自定义敏感模式补充到脱敏规则。

在这些验证完成前，阶段状态是“代码完成、真实试点待联调”，不能称为已经接通生产日志。

## 7. 已知限制

- 官方 SDK 的查询方法不接受调用方 `context.Context`。当前关闭 SDK 服务端错误自动重试，并把 HTTP timeout 与 SDK 内部 GET 网络错误重试窗口都限制为配置值；调用返回后还会拒绝 deadline 已结束的结果。两个顺序请求的总耗时仍可能超过 Gateway deadline，用户取消也不能保证让正在进行的底层 HTTP 请求立即终止。
- 一个逻辑模板固定包含两次、且不在 SDK 内自动重试的 POST 聚合请求；元数据 GET 遇到网络错误时仍可能在配置的短窗口内重试。进程在查询后、持久化前崩溃时也可能由租约恢复重复整个调查，因此当前不承诺付费查询 exactly-once。
- SQLite 只用于本地技术验证；生产数据库、全局多实例配额、步骤级幂等和自动重试留到后续阶段。
- 当前只返回聚合。原始日志样本、任意 SQL/SPL、自动处置、飞书进度卡和自然语言解释都不在 M1 范围内。

## 8. 当时计划：下一阶段建议

真实试点通过后再进入 M2“错误突增调查闭环”：补充新增错误模式、实例分布、飞书接单/进度/结果卡和可选 LLM 摘要。LLM 仍只能解释已经过策略网关获得的证据，不能直接取得 SLS 权限。

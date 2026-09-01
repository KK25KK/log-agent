# 阿里云 SLS CLI + STS Profile 迁移记录

## 结论

本项目的真实 SLS 传输已从 Go SDK 切换为：

```text
企业 SSO
  -> 用户获取短期 STS
  -> 用户写入本机 aliyun CLI StsToken Profile
  -> log-agent 受控调用 aliyun CLI + aliyun-cli-sls 插件
  -> 阿里云 SLS 只读 API
```

该迁移不改变 `error_analysis_v2` 的业务范围，也不绕过 Query Gateway。资源目录、Principal ACL、Schema 校验、时间/调用/行数/扫描字节预算、查询审计、Checkpoint、Evidence、Eino 和飞书报告继续复用原链路。

## 为什么可以迁移

Go SDK 和 CLI 都只是 SLS 的外部传输适配器。应用核心只依赖 `ports.SLSBackend`：

- `GetSchema`：读取 Logstore 与索引元数据；
- `Execute`：执行管理员固定的四次只读聚合。

因此替换发生在 `internal/adapters` 和启动组装处，不需要改调查 Graph、判断规则或报告合同。

## 代码变化

| 位置 | 变化 |
| --- | --- |
| `internal/adapters/aliyuncli` | 新的 CLI 执行、输出解析、固定查询编译、元数据检查和安全错误适配器 |
| `cmd/logagent/sls.go` | 真实模式改为组装 `aliyuncli.Backend` |
| `internal/config/config.go` | 删除 static/ECS SDK 凭据配置，新增 CLI 路径、Profile 和输出上限 |
| `internal/domain/types.go` | 将本地 `QueryID` 与可选 `ProviderRequestID` 分开，防止伪造云端 Request ID |
| `internal/application/query/gateway.go` | 查询审计只写真实存在的 `ProviderRequestID` |
| `go.mod` / `go.sum` | 删除 `aliyun-log-go-sdk` 依赖 |

旧 `internal/adapters/aliyunsls` SDK 适配器与 ECS metadata credential provider 已删除。

## 运行时安全合同

真实模式必须满足以下约束：

1. `aliyun` 可执行文件在启动时解析为固定路径；不通过 shell 执行。
2. Profile 名只能包含有限安全字符，默认 `default`，不能由飞书消息或模型选择。
3. 子进程强制设置 `ALIBABA_CLOUD_PROFILE`，并移除 AK/SK/Security Token、`DEBUG` 和忽略 Profile 的环境覆盖。
4. 设置 `ALIBABA_CLOUD_CLI_PLUGIN_AUTO_INSTALL=false`，部署期间不自动下载插件。
5. CLI 参数只能由已批准的资源目录和固定查询模板生成；用户不能提交 Project、Logstore 或查询语句。
6. stdout、stderr、单次调用时长和一个调查窗口的总时长都有上限。
7. Provider 错误正文、原始查询、日志正文和凭据不会进入错误、审计或报告。
8. CLI 输出缺少 Progress、用量或结构字段时 fail closed，不能形成确定性结论。

## 效果影响评估

### 保持不变

- 仍然是每个 current/baseline 窗口四次只读聚合；不返回原始日志。
- 仍然先做 Catalog、ACL、Schema、预算和 `STARTED` 审计。
- 仍然用 count-before/count-after 判断多请求期间可见集合是否变化。
- 仍然保留 Checkpoint、未知结果不自动重试和租户成本代理。
- Mock demo、Mock E2E 和离线评测继续不依赖 CLI、Profile 或网络。

### 正向影响

- log-agent 进程不再直接接收或保存 AK/SK/STS 环境变量。
- 与企业现有 SSO/临时授权操作保持一致，凭据输入和签名交给官方 CLI。
- Go 代码不再维护 SDK credential provider、ECS metadata 和 SDK 隐式重试细节。
- CLI 子进程可以由 Context 和单次 timeout 约束，适配器调用次数更直观。

### 可接受但必须显式说明的影响

1. **STS 续签是人工运维动作。** Profile 过期后真实查询失败；当前方案不适合完全无人值守的 7x24 长期服务。后续若需要无人值守，应由部署平台提供受审计的 Profile 刷新或凭据代理，而不是恢复在应用中保存长期 AK。
2. **成功响应可能没有 Provider Request ID。** CLI 的普通 JSON 输出不保证包含云端 Request ID。本地 `QueryID` 只表示适配器执行关联，不能冒充 Provider Request ID；这会减少云侧工单关联信息，但不影响证据完整性判断。
3. **CLI 和插件成为部署依赖。** 需要固定并验收版本，启动时不会自动安装。升级 CLI/插件前应回放离线协议测试并在试点资源运行 `sls-check` 与 `sls-smoke`。
4. **进程参数对同机高权限观察者可见。** 固定查询中只含管理员资源目录的选择器，不含凭据、原始日志或用户自由文本；仍应使用隔离的服务账户运行。
5. **CLI 输出协议可能随插件升级变化。** 解析器兼容常见 camelCase/snake_case 元数据，但任何缺失或未知关键结构都会安全降级，不会静默产出结论。
6. **应用不读取 Profile 文件，因此不能自行证明 Profile 一定是 `StsToken` 模式。** `sls-check` 只能证明当前身份可访问资源。部署检查必须核对 Profile 的创建流程和过期轮换记录，避免误用长期 AK Profile。

## 操作步骤

### 1. 安装并固定 CLI/插件

```powershell
aliyun version
$env:ALIBABA_CLOUD_CLI_PLUGIN_AUTO_INSTALL = "false"
aliyun plugin list
aliyun sls get-logs-v2 --help
```

部署清单应记录批准的 CLI `3.x` 与 `aliyun-cli-sls` 版本。插件管理参考[阿里云 CLI 插件文档](https://help.aliyun.com/zh/cli/managing-and-using-cli-plugins)。

### 2. 用户通过 SSO 获取 STS 并配置 Profile

```powershell
aliyun configure --mode StsToken --profile default
```

按提示输入 Region、AccessKey ID、AccessKey Secret 和 Security Token。不要把这些值放进聊天、`.env`、Git 或 log-agent 配置。Profile 配置方法参考[阿里云 StsToken Profile 文档](https://help.aliyun.com/zh/cli/temporary-security-credentials-sts-token)。

### 3. 配置 log-agent

```powershell
$env:LOG_AGENT_SLS_MODE = "aliyun"
$env:LOG_AGENT_SLS_CATALOG = ".\config\sls-resources.json"
$env:LOG_AGENT_SLS_CLI_PROFILE = "default"
# 仅当 aliyun.exe 不在 PATH 时设置可信绝对路径：
# $env:LOG_AGENT_SLS_CLI_PATH = "C:\path\to\aliyun.exe"
```

### 4. 先做元数据检查

```powershell
go run ./cmd/logagent sls-check
```

该命令只调用 `get-project`、`get-log-store` 和 `get-index`，不会读取日志行。

### 5. 再做一条受治理的真实聚合

先配置 Smoke Principal，使其与 Catalog binding 完全一致，然后运行：

```powershell
go run ./cmd/logagent sls-smoke <service> <environment> 30m
```

`get-logs-v2` 的官方参数和响应语义见[阿里云 GetLogsV2 文档](https://help.aliyun.com/zh/sls/developer-reference/query-logs-get-logs-v2)。只有真实 `sls-check` 与 `sls-smoke` 均成功，才能把对应试点标记为“已接通”；离线测试不能替代该结论。

## 未随本次迁移扩大的范围

- 没有实现 StoreView 自动发现或 8 个成员 Logstore 并发查询。
- 没有实现用户输入 TraceID 的原始日志时间线。
- 没有把 DAM Skill 的查询编排复制进 log-agent。
- 没有接入真实飞书、真实火山方舟、真实指标/Trace、企业 SOP 或生产数据库。

这些能力应继续作为独立需求评审；不能因为传输切换为 CLI 就绕过现有聚合、隐私和费用边界。

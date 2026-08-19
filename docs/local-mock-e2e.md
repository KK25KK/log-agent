# 飞书 + 阿里云 SLS 双 Mock 端到端说明

## 目的

在申请飞书企业应用、阿里云试点资源和 RAM 权限之前，先验证业务主链路是否完整：

```text
Mock 飞书消息
  -> 严格命令解析
  -> SQLite 幂等接单与任务
  -> Worker + Eino
  -> 真实 ACL / Schema / 预算 / 审计网关
  -> Mock SLS Backend 当前/基线聚合
  -> SQLite current / baseline Query Checkpoint
  -> Evidence + Report + Mock 变更关联
  -> Delivery Worker
  -> Mock 飞书 Reply/Patch 记录
```

这个入口使用真实的应用层、状态机、Eino Graph、查询策略网关、持久化和结果校验，只替换外部 I/O：飞书收发、SLS Provider 以及管理员资源文件使用固定的内存 Mock。

## 运行

在项目根目录执行：

```powershell
Set-Location "D:\日志agent"
go run ./cmd/logagent mock-e2e
```

不需要设置 `.env`，也不要填写真实 `FEISHU_APP_SECRET`、AccessKey 或 STS Token。

## 预期结果

命令成功时 JSON 中应至少满足：

| 字段 | 预期 | 含义 |
| --- | --- | --- |
| `safety.external_network_calls` | `0` | 双 Mock 路径不访问外部网络 |
| `safety.credentials_required` | `false` | 不读取飞书或阿里云凭据 |
| `feishu.duplicate_replay_deduplicated` | `true` | 同一飞书 Message ID 只创建一个调查 |
| `feishu.deliveries` | `REPLY/QUEUED`、`PATCH/RUNNING`、`PATCH/SUCCEEDED` | 同一 Mock 卡片完成接单、进度和结果更新 |
| `aliyun_sls.logical_observations` | `2` | 当前窗口与等长基线窗口 |
| `aliyun_sls.schema_calls` | `1` | 当前/基线共用一次已缓存的 Mock Schema |
| `aliyun_sls.backend_execute_calls` | `2` | Query Gateway 调用两次 Mock Backend |
| `aliyun_sls.provider_api_calls` | `8` | 每个窗口模拟四次固定聚合 |
| `aliyun_sls.query_audit_events` | `4` | 两个逻辑查询各有 STARTED 与终态审计 |
| `aliyun_sls.query_step_checkpoints` | `2` | current、baseline 的规范化聚合结果已持久化 |
| `aliyun_sls.raw_log_rows_returned` | `0` | 只返回聚合，不返回原始日志 |
| `investigation.status` | `SUCCEEDED` | 调查成功持久化 |
| `investigation.report.outcome` | `spike_detected` | 固定测试数据形成错误突增结论 |

当前 Mock 数据固定为当前窗口 120 条错误、基线 20 条错误，并包含一个影响 `order-pod-a` 的 Mock 发布事件。随机生成的调查、Evidence 和 Ledger ID 每次可能不同。

## Mock 到什么层

### 飞书 Mock

- `internal/adapters/feishumock.Receiver` 模拟已经成功解码的 `im.message.receive_v1` 文本消息；
- App/Tenant/User 身份仍从受信消息信封进入，不能从命令文本伪造；
- `internal/adapters/feishumock.Sender` 模拟 Reply/Patch 并记录语义状态；
- SQLite Delivery 租约、顺序和同卡绑定仍使用正式实现。

它不建立 WebSocket、不调用飞书 OpenAPI，也不验证真实客户端里的 JSON 2.0 卡片视觉效果。正式飞书适配器自己的 SDK 映射、HTTP 形状和渲染边界由其单元测试覆盖，真实客户端效果仍需试点应用验收。

### 阿里云 SLS Mock

- 使用固定的 `internal/adapters/slsmock.Catalog` 和 `Backend`；
- 真实 Query Gateway 仍执行 Principal ACL、时间水位、窗口/行数/调用数预算、Schema 校验、脱敏和 SQLite 查询审计；
- Mock Backend 返回与正式 Provider 边界一致的 `QueryResult`、完整性、调用数和聚合桶，并记录实际收到的 Schema/Execute 次数；
- Eino、Worker、Query Checkpoint、Evidence/Report 校验与 SQLite 成功事务全部使用正式实现。

它不调用 GetIndex/GetLogsV2，也不读取真实 JSON 资源目录，因此不能验证真实 LogStore Schema、目录配置、RAM 权限、扫描成本和索引延迟。只有显式使用 `sls-check`、`sls-smoke` 或 `LOG_AGENT_SLS_MODE=aliyun` 才会触达真实 SLS。

## 自动验收

```powershell
go test -count=1 ./internal/adapters/feishumock ./cmd/logagent
go test -count=1 ./...
go vet ./...
```

测试会检查重复入站幂等、可信身份映射、严格命令、Reply/Patch 同卡顺序、ACL/Schema/预算/审计网关、两个 Query Checkpoint、两份 Evidence、两次 Backend/八次模拟 Provider 调用、无原始日志以及最终成功报告。Checkpoint 的崩溃恢复语义另见 [`m4-recoverable-query-steps.md`](m4-recoverable-query-steps.md)。另有架构测试禁止双 Mock 源码直接导入真实飞书/SLS 适配器、配置加载器或网络包。

## 后续替换顺序

1. 保持 SLS 为 Mock，先把 `feishumock` 替换为真实飞书企业自建应用，验证收消息和卡片；
2. 保持飞书只读调查不变，再配置一个资源级只读的 SLS 试点；
3. 运行 `sls-check`，通过后再运行 `sls-smoke`；
4. 两边分别通过后，才做真实飞书到真实 SLS 的小范围联调。

双 Mock 通过只表示应用离线闭环成立，不代表真实飞书、SLS 或生产可靠性已经验收。

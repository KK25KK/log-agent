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
  -> SQLite tenant quota reserve / settle
  -> Evidence + Report + Mock 变更关联
  -> Mock 指标/Trace 聚合 + IncidentTimeline
  -> 首次生产校验
  -> 固定 Evidence 模板/治理指纹/可信请求窗口校验
  -> 独立 5s 超时内的 Mock Runbook Source
  -> SYNTHETIC_MOCK + HUMAN_REVIEW_ONLY Guidance
  -> 再次生产校验
  -> SQLite LLM request / Token reserve / settle
  -> Mock 证据摘要（严格引用 / 0 网络）
  -> Delivery Worker
  -> Mock 飞书 Reply/Patch 记录
```

这个入口使用真实的应用层、状态机、Eino Graph、查询策略网关、Worker 前后校验、持久化和结果校验，只替换外部 I/O：飞书收发、SLS Provider、指标/Trace 聚合、SOP 目录以及管理员资源文件使用固定的内存 Mock。

## 运行

在项目根目录执行：

```powershell
Set-Location "D:\日志agent"
go run ./cmd/logagent mock-e2e
go run ./cmd/logagent mock-e2e error_count_v1
```

第二条命令验证计数型轻量链路：current/baseline 共四次 Provider 调用代理，Mock LLM 与 Mock 飞书照常工作；错误维度、实例维度、变更源、指标/Trace 源和 Runbook 源均不参与，不能据此声明根因能力。

不需要设置 `.env`，也不要填写真实 `FEISHU_APP_SECRET`、AccessKey 或 STS Token。

## 预期结果

命令成功时 JSON 中应至少满足：

| 字段 | 预期 | 含义 |
| --- | --- | --- |
| `safety.external_network_calls` | `0` | 整条 Mock 路径不访问外部网络 |
| `safety.credentials_required` | `false` | 不读取飞书或阿里云凭据 |
| `feishu.duplicate_replay_deduplicated` | `true` | 同一飞书 Message ID 只创建一个调查 |
| `feishu.deliveries` | `REPLY/QUEUED`、`PATCH/RUNNING`、`PATCH/SUCCEEDED` | 同一 Mock 卡片完成接单、进度和结果更新 |
| `aliyun_sls.logical_observations` | `2` | 当前窗口与等长基线窗口 |
| `aliyun_sls.schema_calls` | `1` | 当前/基线共用一次已缓存的 Mock Schema |
| `aliyun_sls.backend_execute_calls` | `2` | Query Gateway 调用两次 Mock Backend |
| `aliyun_sls.provider_api_calls` | `8` | 每个窗口模拟四次固定聚合 |
| `aliyun_sls.query_audit_events` | `4` | 两个逻辑查询各有 STARTED 与终态审计 |
| `aliyun_sls.query_step_checkpoints` | `2` | current、baseline 的规范化聚合结果已持久化 |
| `tenant_quota.observations` | `2` | 两个逻辑观察均经过租户额度账本 |
| `tenant_quota.api_calls` | `8` | 结算值与 Evidence/Backend Provider 调用代理一致 |
| `llm_quota.requests` | `1` | 摘要 Provider 前经过一笔租户额度预留与结算 |
| `llm_quota.tokens` | `0` | Mock 摘要报告零实际 Token；不是火山账单 |
| `aliyun_sls.raw_log_rows_returned` | `0` | 只返回聚合，不返回原始日志 |
| `investigation.status` | `SUCCEEDED` | 调查成功持久化 |
| `investigation.report.outcome` | `spike_detected` | 固定测试数据形成错误突增结论 |
| `llm_summary.mode/status` | `MOCK / GENERATED` | 默认摘要器经过 Worker 主链 |
| `llm_summary.external_api_calls` | `0` | 没有调用火山或其他模型服务 |
| `operational_signals.source_calls` | `1` | 只调用一次确定性指标/Trace Mock |
| `operational_signals.timeline_status` | `COMPLETE` | Mock 指标和 Trace 覆盖完整；不代表因果确认 |
| `operational_signals.signals/timeline_items` | `2 / 3` | 两个聚合观察，加上已有发布事件形成三条时间线 |
| `runbook_knowledge.source_calls` | `1` | 只调用一次确定性 Runbook Mock；不访问企业知识平台 |
| `runbook_knowledge.mode` | `SYNTHETIC_MOCK` | 可信组装层声明合成来源；不是 Source 自报的企业内容 |
| `runbook_knowledge.status` | `COMPLETE` | 当前 Mock 目录完整且有一个受控匹配；不代表内容正确、最新或获批 |
| `runbook_knowledge.items/steps` | `1 / 3` | 一项人工核查指引，包含核对、观察和升级联系三步 |

当前 Mock 数据固定为当前窗口 120 条错误、基线 20 条错误，并包含一个影响 `order-pod-a` 的 Mock 发布事件、一个指标错误率异常、一个 Trace P95 延迟异常和一个三步 Runbook 条目。随机生成的调查、Evidence 和 Ledger ID 每次可能不同。2026-08-24 完成关闭 Code/本地模板等安全加固后，最新实际命令已再次确认表中的 Runbook 1 次调用/1 项/3 步，以及 SLS 2 次逻辑观察/8 次 Provider 调用/0 次外部网络。

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
- Eino、Worker、Query Checkpoint、租户额度预留/结算、Evidence/Report 校验与 SQLite 成功事务全部使用正式实现。

它不调用 GetIndex/GetLogsV2，也不读取真实 JSON 资源目录，因此不能验证真实 LogStore Schema、目录配置、RAM 权限、扫描成本和索引延迟。只有显式使用 `sls-check`、`sls-smoke` 或 `LOG_AGENT_SLS_MODE=aliyun` 才会触达真实 SLS。

### 指标与 Trace Mock

- `internal/adapters/signalmock` 只返回两个关闭类型的聚合观察，不返回原始 Span、TraceID、指标标签或 Provider 文案；
- 查询 ResourceID 与时间范围由 current/baseline Evidence 派生，Mock 会拒绝任何不一致请求；
- Engine 本地计算异常标记并生成稳定时间线，Worker 在成功落库前复算阈值和引用；
- 飞书报告和证据页只展示有界聚合，并明确“时间相关不等于因果证明”。

它不调用 ARMS、CMS、Prometheus、OpenTelemetry 或其他可观测平台，不能验证真实指标口径、Trace 覆盖、时钟对齐、费用、限流和超时。

### SOP 知识 Mock

- `internal/adapters/runbookmock` 只在 Evidence ResourceID 与可信 Job 的 service/environment/requester 通过同一 Mock Resource Catalog 解析和 ACL 后接收查询；current/baseline 还必须使用固定 `error_analysis_v2` 模板、带完整 Query/Schema/Policy/Governance 元数据，治理身份一致、窗口连续等长，并精确绑定可信 Job 请求。Recommendation Code 由应用重算并要求与报告精确一致，不读取用户消息正文、模型输出或物理知识库定位；
- Worker 先验证 Eino 的确定性 Evidence/Report，再调用 `RunbookService`；应用计算 Evidence 并集、稳定指纹和 `HUMAN_REVIEW_ONLY`，随后用同一生产门禁再次校验；
- baseline 错误数为 0 时保持 Engine 的 `data_insufficient` 语义并零调用 Source；Runbook 层不会单独把当前错误数重算成突增；
- 每次 Lookup 接收独立默认 5 秒子 Context，Adapter 必须遵守其 Deadline；应用在 Source 返回后、本地 cancel 前再次检查 Deadline，因而 Deadline 后的 `(set, nil)` 也只会降级为 `UNAVAILABLE`。Source 自身超时同样只降级，父 Worker Context 真正取消才向外传播。条目更新时间同时不能超过报告时间和可信服务时钟各自加 5 分钟；
- `SYNTHETIC_MOCK` 由 Mock E2E 的可信组装层传入并持久化，`runbookmock` 无权自报或覆盖。正式飞书 Renderer 对该模式固定显示“受控 SOP 参考（Mock）”，真实企业来源才使用不带 Mock 的标题；空值或非法来源只显示来源未确认的不可用文案，不显示任何夹带条目；
- Mock 条目只选择 `VERIFY_ERROR_PATTERN / OBSERVE_HOT_INSTANCE / ESCALATE_SERVICE_OWNER` 三个关闭 Code；`VERIFY/OBSERVE/ESCALATE` 类型和 Instruction 必须与本地固定模板一致，Source 不能注入自由步骤文本。领域合同不存在 URL、Shell/SQL、执行参数或自动处置字段；飞书不生成 SOP 按钮或动作值；
- `NO_MATCH/INCONCLUSIVE/UNAVAILABLE/SKIPPED_NO_TRIGGER` 只改变可选 Guidance，不修改原 Finding、Recommendation、CauseAnalysis、IncidentTimeline 或调查成功状态；
- `SKIPPED_NO_TRIGGER` 仅用于没有确定性错误突增，正常 Engine 的 baseline=0 报告属于该路径；若报告声称突增但 Evidence 是零基线、Recommendation 缺失或治理资源不一致，则为 `UNAVAILABLE`。上述前置条件不成立时都保持零 Source 调用；
- SOP 内容不进入 Eino Graph、LLM `SummaryInput`、Agent Trace、Replay 快照比较或当前离线评测数据集/版本指纹。

它不连接 Wiki、文档平台、错误码平台或企业搜索。双时钟门禁只能拒绝伪造的未来时间，不能证明内容实际最新、已经审批或尚未失效，也不能验证企业 Owner、租户权限、审计和真实检索质量。

## 自动验收

```powershell
go test -count=1 ./internal/adapters/feishumock ./cmd/logagent
go test -count=1 ./...
go vet ./...
```

测试应检查重复入站幂等、可信身份映射、严格命令、Reply/Patch 同卡顺序、ACL/Schema/预算/审计网关、两个 Query Checkpoint、SLS 租户额度结算、LLM 请求/Token 额度结算、两份严格治理 Evidence、请求窗口绑定、baseline=0 零 Runbook 调用、独立超时降级、可信时钟、可信 `SYNTHETIC_MOCK` 来源、Renderer 单测中的 Mock SOP 区块标题、两次 Backend/八次模拟 Provider 调用、一次指标/Trace Mock 调用、三条受控时间线、一次 Runbook Mock 调用、一项三步人工指引、Mock 证据摘要、无原始日志以及最终成功报告。`feishumock.Sender` 不保存卡片 JSON，因此 `mock-e2e` 本身只能证明投递状态和 SOP 持久化，不能证明标题或真实客户端视觉。第一轮安全加固后的 `mock-e2e`、全仓测试、vet、重点包乱序和仓库级链接/diff 总检均有实跑记录；第二轮边界补强后的结果以本轮实际复跑记录为准。Checkpoint 的崩溃恢复语义另见 [`m4-recoverable-query-steps.md`](m4-recoverable-query-steps.md)，可靠性治理见 [`m4b-reliability-governance.md`](m4b-reliability-governance.md)。Mock 源码还必须保持与真实适配器、配置加载器和网络包隔离。

## 后续替换顺序

1. 保持 SLS 为 Mock，先把 `feishumock` 替换为真实飞书企业自建应用，验证收消息和卡片；
2. 保持飞书只读调查不变，再配置一个资源级只读的 SLS 试点；
3. 运行 `sls-check`，通过后再运行 `sls-smoke`；
4. 两边分别通过后，才做真实飞书到真实 SLS 的小范围联调；此时仍保持指标/Trace 与 Runbook Source 禁用。
5. 单独替换 `signalmock`，补齐该外部调用的目录、额度、审计、超时和结果未知语义后，再让真实指标/Trace 时间线进入试点卡片。
6. 最后实现真实 `RunbookSource`，先完成内容 Owner/Revision/审批/失效、租户授权、审计、无链接/无命令合同和检索质量评测，再允许企业 SOP 进入试点卡片。

Mock 主链通过只表示应用离线闭环成立，不代表真实飞书、SLS、指标/Trace 平台、企业知识系统或生产可靠性已经验收。

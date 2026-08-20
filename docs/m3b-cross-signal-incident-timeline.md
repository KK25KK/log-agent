# M3-B：跨信号故障时间线（Mock-first）

## 1. 阶段状态

| 项目 | 当前结论 |
| --- | --- |
| 阶段目标 | 在不增加 SLS 查询、不接真实外部系统的前提下，把日志 Evidence、治理变更、指标聚合和 Trace 聚合整理为一条可审计时间线 |
| 当前状态 | 主体代码与离线验收完成；真实指标/Trace 平台未接入 |
| 数据边界 | 指标、Trace、变更和日志结果全部为确定性 Mock；0 凭据、0 外部网络调用 |
| 对外声明 | 只能称为“跨信号时间线离线能力”，不能称为真实跨信号诊断、根因确认或生产可观测 |

## 2. 为什么做这个切片

当前 Agent 已能回答“错误是否突增、主要模式和实例是什么、某个治理变更是否与突增相关”。但一线排障还需要把同一时间段的错误率、延迟和 Trace 聚合放到一起看。

本期先冻结安全、可替换的数据合同，并用 Mock 跑通完整主链。这样未来接 ARMS、CMS、Prometheus 或其他可观测平台时，只替换 Adapter，不改飞书入口、SLS 查询、Worker 状态机和报告证据规则。

## 3. 本期范围

### 3.1 包含

- 新增 provider-neutral `OperationalSignalSource` 端口。
- 查询范围只能由已治理的 current/baseline Evidence 派生。
- 一次调查最多调用信号源一次，最多返回八个结构化观察。
- 首个关闭集合只支持指标错误率、指标 P95 延迟、Trace 错误率和 Trace P95 延迟。
- 应用本地计算异常阈值，不接受 Provider 自报异常或因果结论。
- 把已有 Change Event 和信号观察合并为稳定排序的 `IncidentTimeline`。
- Worker 在成功落库前重新验证值域、引用、完整性、异常计算和状态。
- Mock 端到端输出信号源调用次数、时间线状态和条目数。
- 飞书报告与证据页做有界展示，并明确“时间相关不等于因果证明”。

### 3.2 不包含

- 真实 ARMS、CMS、Prometheus、OpenTelemetry、Jaeger 或其他平台连接器。
- 原始 Span、TraceID、Span 名、指标标签、任意属性或原始时序点。
- 服务拓扑、CMDB、SOP、错误码知识库或自动处置。
- 新增 SLS 查询模板、QueryStep、额度预留或 Provider 调用。
- 把时间重合写成已确认根因，或让信号结果改变 M2/M3 的事实与 verdict。
- 把跨信号内容发送给 LLM；本期摘要输入保持不变。

## 4. 数据与信任边界

```text
complete current/baseline Evidence
        │
        ├─ resource_id
        ├─ baseline.start
        └─ current.end
                │
                ▼
OperationalSignalSource.List
        │ bounded typed observations only
        ▼
application validation + local anomaly calculation
        │
        ├─ existing CauseAnalysis Change references
        └─ metric/Trace signal references
                │
                ▼
IncidentTimeline -> Worker validation -> SQLite Report JSON -> Feishu card
```

用户、飞书按钮和模型都不能提交 ResourceID、信号类型、查询表达式或时间范围。信号源返回值是不可信 Adapter 输出，必须经过 Engine 边界校验和 Worker 二次校验。

## 5. 状态语义

| 状态 | 含义 |
| --- | --- |
| `COMPLETE` | 信号集合完整、未截断，并且同时包含至少一项指标和一项 Trace 观察 |
| `INCONCLUSIVE` | SLS 证据不足，或信号集合不完整、截断或缺少某一类覆盖 |
| `UNAVAILABLE` | 信号源禁用、调用失败、身份不一致或返回非法数据 |
| `SKIPPED_NO_SPIKE` | M2 没有确定性错误突增，不调用信号源 |

`COMPLETE` 只表示本次受控观察完整，不代表根因确认。

## 6. 固定异常规则

- 错误率：当前值至少比基线高 `0.05`，并且基线非零时至少为基线的 `2` 倍；基线为零时当前值至少为 `0.05`。
- P95 延迟：当前值至少比基线高 `100ms`，并且基线非零时至少为基线的 `2` 倍；基线为零时当前值至少为 `100ms`。
- 所有数值必须有限且非负；错误率范围必须在 `[0,1]`。
- 异常标记由应用计算并由 Worker 复算，Adapter 不能提供。

这些阈值是 `operational-signal-timeline-v1` 的确定性工程规则，不是统计显著性或业务 SLO。

## 7. 失败与降级

- `context.Canceled` 和 `DeadlineExceeded` 继续向 Worker 传播。
- 其他信号源错误、非法值、重复 ID、越界时间或身份不一致只将时间线标为 `UNAVAILABLE`。
- 时间线失败不能删除或修改已有 Evidence、Finding、Recommendation、CauseAnalysis 或 LLM Summary。
- 信号源调用不纳入本期 SLS Checkpoint；真实计费源接入前必须另行设计额度、超时、审计与结果未知语义。

## 8. 离线验收

- [x] `mock-e2e` 仍保持两个逻辑 SLS 观察、八次 Provider 调用、两个 Checkpoint 和一次 LLM 摘要额度请求。
- [x] `mock-e2e` 额外执行一次 Mock Operational Signal Source，输出一个 `COMPLETE` 时间线。
- [x] 时间线包含已有变更、一个指标观察和一个 Trace 观察，排序稳定、引用闭合。
- [x] no-spike 和 data-insufficient 不调用信号源。
- [x] 信号源失败、非法、截断或缺少类型时保留 M2/M3 报告并安全降级。
- [x] Worker 拒绝伪造异常标记、NaN/Inf、断裂引用、未知枚举、越界值和不一致状态。
- [x] 飞书卡片有界、转义且包含因果限制，不显示原始 Span、TraceID、标签或 Provider 错误。
- [x] `gofmt -w .`、`go test -count=1 ./...` 和 `go vet ./...` 通过。
- [ ] `go test -race ./...` 在具备 CGO 和 C 编译器的环境完成；当前 Windows 工具链不满足条件。

## 9. 真实接入前置条件

1. 选定指标和 Trace 后端，明确只读 API、资源映射、超时、限流与费用模型。
2. 建立逻辑 `resource_id` 到后端资源的管理员目录，禁止用户输入物理资源。
3. 确认错误率、延迟口径、聚合窗口和时钟对齐规则。
4. 对真实返回做字段白名单、脱敏、审计和数据保留评审。
5. 为外部调用补充租户额度、结果未知、可观测和真实试点门禁。
6. 用脱敏历史事故和专家标注验证阈值，再决定是否进入真实飞书卡片。

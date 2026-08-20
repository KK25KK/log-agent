# M5-B：Agent 自观测与离线回放

> 阶段：第七期 B1 + 第八期 B2
>
> 当前状态：B1 事件/版本合同和 B2 append-only 回放历史均已完成主体代码与离线验收；B3 趋势比较与反馈闭环未开始
>
> 数据边界：五个 Case、SLS 聚合、变更事件和全部外部边界均为仓库内置合成 Mock；不读取凭据，外部网络调用为 0
>
> Trace 边界：只覆盖 `evaluate` 的 Engine/evaluation 级执行，不是飞书入站、SQLite Worker、真实 SLS 网络调用到卡片投递的跨进程分布式 Trace
>
> 结论边界：本期不是生产遥测后端、采样/保留方案、延迟 SLO、真实成本评估或灰度批准

## 1. 本期解决什么问题

M5-A 已能判断“合成 Case 的最终报告是否正确”，但只看最终输出仍回答不了：

- 某个 Case 实际经过了哪些固定 Graph 节点和 Mock 工具；
- current、baseline 与 Change Source 的调用/字节统计是否能和评测报告交叉核对；
- 数据集、Graph、模板、策略或评测规则变化后，两次数字是否仍在同一版本合同下；
- Trace 丢事件、层级断裂或混入不允许内容时，离线门禁能否失败。

B1 因此先落地一个框架无关、隐私有界的 Agent 事件合同和统一版本清单。B2 在这个稳定合同之上追加严格快照和当前二进制回放，让“最终结果正确”和“执行路径可验证”都能作为历史制品保存；跨运行趋势比较仍留给 B3。

```text
synthetic-m5a-v1 数据集
  -> 规范化 AgentVersionManifest -> SHA-256 version_fingerprint
  -> 每个 Case 独立 TraceContext + BoundedRecorder
  -> Engine 根 Span
       -> plan_queries
       -> execute_queries
            -> sls.current
            -> sls.baseline
       -> build_report
       -> correlate_changes
            -> change_source.list（仅实际调用的 Case）
  -> Trace 结构/隐私/用量合同门禁
  -> 原 M5-A 质量、证据、原因与成本门禁
```

## 2. B1 已实现范围

### 2.1 关闭的事件 Schema

`agent-trace-v1` 只允许三类层级：

| 层级 | 允许的 Span 名称 |
| --- | --- |
| `RUN` | `engine.run` |
| `GRAPH_NODE` | `plan_queries`、`execute_queries`、`build_report`、`correlate_changes` |
| `TOOL` | `sls.current`、`sls.baseline`、`change_source.list` |

每个 Span 必须有且只有一个 `STARTED` 事件，以及一个 `SUCCEEDED`、`FAILED` 或 `SKIPPED` 终态事件。完整 Trace 必须满足：

- 恰好一个 `engine.run` 根 Span；
- Graph 节点只挂在 Engine 根节点下，工具只挂在对应 Graph 节点下；
- 父子生命周期闭合、无环，子节点必须在父节点关闭前结束；
- Event ID 唯一，Sequence 从 1 连续递增；
- Case、Trace、Run、Evaluation Run 和版本指纹在整条 Trace 内一致；
- 工具终态必须带非负的 `logical_calls`、`provider_calls`、`processed_bytes` 和 `complete`。

安全失败只使用关闭的分类与代码，不保存 Provider 原始错误文本，也不改变 M4 的重试、恢复、权限或状态机决策。

### 2.2 Observer 与有界 Recorder

默认应用 Observer 是 Noop，不启用观测时不改变既有 Worker 行为。离线评测为每个 Case 创建线程安全的 `BoundedRecorder`：

- Recorder 容量有硬上限，不允许演变为无界内存队列；
- `Record` 不向业务路径返回错误；无效、越界或溢出的事件只增加 `drop_count`；
- 只要有事件被丢弃，或最终层级不闭合，`complete=false`；
- B1 离线门禁要求每个 Case `complete=true` 且 `drop_count=0`。

这意味着遥测故障不会把一次调查主动改成失败，但在评测环境中会阻止一个不完整执行记录被当作可回放基线。

### 2.3 隐私边界

序列化事件只允许有界运行身份、稳定枚举、时间戳、耗时、输入/输出指纹、版本指纹和工具计数。以下内容禁止进入事件：

- 飞书消息正文、用户/租户/群身份和回调 payload；
- Endpoint、Project、LogStore、资源选择器、SQL/SPL 或 QuerySpec 正文；
- 原始日志、错误/实例桶标签和 Change Summary；
- Finding、Recommendation 或报告中的自然语言；
- Provider 原始错误、请求/响应内容、凭据和 Token；
- Prompt 正文、模型输入输出和任意属性 Map。

SHA-256 用于完整性、关联和版本识别，不是匿名化手段；敏感内容不能先哈希再绕过字段边界写入 Trace。

## 3. 版本合同

`evaluate` 在运行 Case 前规范化一份 `AgentVersionManifest`，校验后对其 JSON 形态计算 SHA-256。当前合同为：

| 项目 | 当前值 |
| --- | --- |
| Gate / evaluation version | `m5b-agent-trace-gate-v1` |
| Dataset schema / ID | `evaluation-dataset-v1` / `synthetic-m5a-v1` |
| Dataset fingerprint | `caf2714c80a646c5da15134c6557879565ffc8e083a66da1f1c9e49d3d0dc1f8` |
| Graph version | `error-spike-investigation-v1` |
| Query template ID / version | `error_analysis_v2` / `error-analysis-v2` |
| Query policy version | `synthetic-policy-v1` |
| Cause method | `change-correlation-v1` |
| Trace schema | `agent-trace-v1` |
| Replay schema | `evaluation-replay-v1` |
| Executor profile | `SYNTHETIC_MOCK` |
| Prompt / model | `prompt_used=false`；Prompt、Model、Token 字段不适用 |

本次离线验收生成的版本指纹是：

```text
14db14acf992ebd06d9d4d71f89056be2a2b984baeb6bf5de2c136db442f7c53
```

版本指纹绑定的是规范化行为合同，不包含主机名或墙上时钟。任一清单字段变化都应形成新指纹，不能把不同合同下的评测数字直接视为同一条趋势。

`evaluation-replay-v1` 已成为 B2 快照的实际 Schema，并继续纳入版本指纹。B2 已提供独立 Evaluation Run Store 和 `replay` 命令；`replay-compare` 仍属于 B3。

## 4. Trace 门禁如何判定

`m5b-agent-trace-gate-v1` 在原 M5-A 门禁上增加逐 Case Trace 合同：

1. Trace 通过领域 Schema 校验，完整且无丢弃事件；
2. 固定 Engine/Graph/工具 Span 各有一次开始和一次终态；
3. 每个 Case 必须有 current 和 baseline，只有预期执行变更源的 Case 才允许 `change_source.list`；
4. 工具终态用量必须和独立评测统计一致；
5. `trace_contract_accuracy` 必须等于 1，`trace_dropped_events` 必须等于 0；
6. 任一 Case Trace 不合格时，完整 JSON 仍会输出，但命令以非零状态退出。

五个 Case 的固定规模为：

- 每个 Case 有 1 个 Engine Span、4 个 Graph Span 和 2 个 SLS 工具 Span，共 14 个事件；
- 其中 3 个 Case 实际调用 Change Source，各增加 1 个工具 Span 和 2 个事件；
- 合计 76 个事件、13 个工具 Span、0 个丢弃事件；
- 工具统计合计为 10 次逻辑 SLS 观察、40 次 Provider 调用代理、3 次 Change Source 调用和 78,080 processed bytes。

这些 Provider 调用和 processed bytes 都是 Fixture Mock 的工程成本代理，不是阿里云真实请求或账单。

## 5. 如何运行

在仓库根目录执行：

```powershell
go run ./cmd/logagent evaluate
```

命令继续使用 M5-A 的统一入口。结构化 JSON 新增：

- `evaluation_run_id`；
- `version_manifest` 和 `version_fingerprint`；
- 每个 Case 的 `agent_trace`、事件数、工具 Span 数、丢弃数和 Trace 合同结果；
- 聚合的 `trace_contract_accuracy`、`trace_contract_failures`、`trace_events`、`trace_tool_spans` 和 `trace_dropped_events`。

### 5.1 保存 append-only 快照

```powershell
go run ./cmd/logagent evaluate --snapshot-dir .\data\evaluation-runs
```

不传 `--snapshot-dir` 时，`evaluate` 保持原有只输出报告、不写文件的行为。传入目录后，成功和门禁失败都会写成一个以 `evaluation_run_id` 命名的不可覆盖 JSON 文件。顶层输出改为快照，完整评测报告位于 `report`。

快照固定包含：

- `evaluation-replay-v1` Schema 与 Run ID；
- UTC 创建时间和关闭的安全失败码 `NONE / GATE_FAILED / EVALUATION_ABORTED`；
- 完整 Evaluation Report、版本清单、逐 Case Trace、门禁和失败 Case；
- 可选的 `replay_of` 源 Run/源哈希；
- 覆盖除 `content_hash` 自身之外全部字段的 SHA-256。

Store 通过独占创建文件实现 append-only：已存在的 Run ID 不覆盖；未知字段/Schema、尾随 JSON、超限文件、非法身份、版本映射漂移和内容哈希变化都拒绝加载。进程内写失败会清理未完成文件，异常强杀遗留的半文件会在后续加载时 fail closed。

### 5.2 用当前二进制回放

```powershell
go run ./cmd/logagent replay --snapshot-dir .\data\evaluation-runs --run-id evalrun_xxx
```

命令先验证源快照，再要求源数据集 Schema/ID/指纹、数据来源边界和 `SYNTHETIC_MOCK` 执行 Profile 与当前内置 Fixture 一致。通过后才会重新执行当前 Graph，并在同一目录追加一个新的子快照。输出包含源引用和新快照；任何 Gate 失败仍会落盘并以非零状态退出。

回放不会调用飞书、SLS、发布平台或模型服务，也不会执行源快照对应的历史代码。它只回答“当前二进制在同一内置合成输入上现在会得到什么结果”。

## 6. 离线验收记录

| 检查 | 结果 |
| --- | --- |
| `go test -count=1 ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go run ./cmd/logagent evaluate` | `PASSED`；5/5 Case 通过，`trace_contract_accuracy=1` |
| Trace 规模 | 76 个事件、13 个工具 Span、0 个丢弃事件 |
| 工具用量核对 | 10 次逻辑 SLS 观察、40 次 Provider 调用代理、3 次 Change Source 调用、78,080 processed bytes |
| append-only 快照 | 一次保存 + 一次回放形成两个不同 Run 文件；子快照绑定源 Run 与源 SHA-256 |
| 严格读取 | 重复 Run、未知字段/Schema、篡改哈希、非法路径和不兼容数据集均有离线拒绝测试 |
| 数据与运行边界 | `SYNTHETIC_MOCK`；真实故障 0、专家标注 0、外部网络调用 0、不需要凭据 |
| 数据集 / 版本指纹 | `caf2714c80a646c5da15134c6557879565ffc8e083a66da1f1c9e49d3d0dc1f8` / `14db14acf992ebd06d9d4d71f89056be2a2b984baeb6bf5de2c136db442f7c53` |
| `go test -race ./...` | 未执行；当前 Windows 环境 `CGO_ENABLED=0` 且未安装 GCC，不能记为通过 |

验收数字只说明当前二进制在仓库内置五个合成 Case 上满足固定工程合同。它没有验证真实飞书、真实 SLS、发布平台、跨进程 Trace、生产并发、网络抖动、采样保留或 SLO。

## 7. 后续切片

### B2：append-only 离线回放历史（代码与离线验收完成）

- 成功和门禁失败 Evaluation Run 已保存为严格快照，包含内容哈希、版本清单、逐 Case Trace、安全失败码和可选父引用；
- 独立的 Evaluation Run Store 没有扩展生产 `ports.Store`，也没有把 Query Audit/QueryStep 冒充 Trace 存储；
- `evaluate --snapshot-dir` 与只使用当前二进制/合成 Mock 的 `replay` 命令已落地；复现旧实现仍依赖旧 Git Commit 或构建制品；
- 重复写入、篡改、未知 Schema/字段、非法路径、不完整文件和不兼容数据边界都会 fail closed。

### B3：趋势比较与反馈闭环（未开始）

- 比较兼容快照的质量门禁、失败 Case、安全错误代码、工具调用、Trace 完整性和成本代理；
- 数据集边界或 Executor Profile 不兼容时返回 `INCOMPARABLE`，不制造伪精确差值；
- 为后续真实专家反馈保留关联接口，但不把合成标签冒充真实反馈。

### 真实系统能力（不在 B1/B2/B3 内自动获得）

- 飞书到投递的分布式 Trace 与真实遥测后端；
- 生产采样、保留、租户隔离、告警和 SLO；
- 真实 LLM Prompt/Token/模型成本观测；
- 历史故障、专家标签、真实试点群和灰度批准。

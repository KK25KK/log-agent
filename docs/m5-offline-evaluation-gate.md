# M5-A：合成黄金集离线评测门禁

> 阶段：第六期第一个纵向切片
>
> 当前状态：主体代码与离线验收完成；真实数据评测、Agent 自观测和灰度试点待 M5-B/M5-C
>
> 数据边界：数据集、标签、SLS 聚合、飞书身份和变更事件全部是仓库内置的合成 Mock；不读取凭据，不访问外部网络
>
> 结论边界：本门禁只用于发现确定性代码回归，不代表历史真实故障准确率、专家评审结果、生产 SLO、真实成本或灰度批准

## 1. 本期解决什么问题

M0～M4-A 已经形成一条证据驱动的调查链路，但“Demo 能运行”不能回答下面这些问题：

- Graph 修改后，原本应该识别的突增、无突增和证据不足场景是否仍然正确；
- 是否新增了标签之外的确定性结论，从而提高误导风险；
- Finding 是否仍然引用有效 Evidence；
- Recommendation 是否与 Case 标签的 Code 及 current/baseline Evidence 绑定完全一致；
- 变更关联的支持、反证和不确定判断是否发生回归；
- current/baseline 是否仍然遵守固定查询合同和成本代理上限；
- 一次改动失败时，CI 或本地命令能否通过退出码阻止继续交付。

M5-A 因此增加一个版本化的合成黄金集，并让它运行应用当前使用的真实确定性 Eino Graph。外部数据源全部替换为逐用例 Fixture Mock，评测器只负责校验标签、汇总指标和执行工程门禁，不复制一套调查算法。

```text
内置 synthetic-m5a-v1 数据集
  -> 严格 Schema、来源声明与安全场景校验
  -> 每个 Case 注入 Mock SLS 聚合和 Mock ChangeSet
  -> 真实 Eino 确定性 Graph
  -> Report / Finding / Evidence / CauseAnalysis
  -> 逐用例标签与查询合同核对
  -> 聚合指标 + 数据集 SHA-256 指纹
  -> 门禁通过返回 0；门禁失败打印完整 JSON 后返回非零
```

## 2. 与第五期的关系

第五期当前只完成了 M4-A“可恢复的付费查询步骤”。M4-B 的投递恢复、租户治理和审批，以及 M4-C 的生产数据库、多实例和真实故障演练仍未完成。

M5-A 可以与这些工作并行，因为它只运行离线合成数据；它不会把当前状态从“具备试点条件”提升为“生产可用”，也不能替代 M4-B/M4-C。

## 3. 数据集合同

仓库内置数据集使用以下固定身份：

| 字段 | 值或约束 | 用途 |
| --- | --- | --- |
| `schema_version` | `evaluation-dataset-v1` | 数据结构兼容性 |
| `dataset_id` | `synthetic-m5a-v1` | 合成集合身份 |
| `data_source` | `SYNTHETIC_MOCK` | 禁止冒充真实数据 |
| `real_incident_count` | `0` | 明确没有历史真实故障 |
| `expert_label_count` | `0` | 明确没有专家标注 |
| `credentials_required` | `false` | 不需要飞书或阿里云凭据 |
| `external_network_calls` | `0` | 评测路径必须完全离线 |
| `production_claim_allowed` | `false` | 禁止据此宣称生产准确率 |

JSON 使用严格解码：未知字段、尾随 JSON、重复 Case ID、无效时间窗、非法文本、失配的资源治理身份、错误的模板/调用数、越界聚合和不可能的标签都会直接拒绝。报告还保存规范化数据集语义内容的 SHA-256 指纹；修改 Fixture 或标签会形成新的可追踪评测输入，单纯调整 JSON 空白不会伪造一版新数据。

每个 Case 包含：

- 一个带可信合成身份的调查请求；
- current 和 baseline 两个规范化 `QueryResult` Fixture；
- 可选的合成 `ChangeSet`；
- 期望的 Report outcome；
- 期望的确定性和非确定性 Finding Code；
- 期望的 Recommendation Code，以及每条建议必须引用的 current/baseline Evidence 名称；
- 期望的原因分析状态与逐变更 verdict；
- 固定的两个逻辑 SLS 观察、八次 Provider 调用代理和处理字节上限。

数据集至少覆盖五类安全场景：

1. 错误突增且存在 `SUPPORTED_CANDIDATE`；
2. 没有显著突增；
3. current 或 baseline 不完整，只能输出证据不足；
4. 完整实例范围零交集形成 `REFUTED`；
5. 信息不足或存在混杂因素，只能形成 `INCONCLUSIVE`。

这些是为验证规则边界专门构造的样例，不是从生产日志抽样或脱敏得到的数据。

## 4. 指标与判定

M5-A 输出以下指标：

| 指标 | 含义 | M5-A 用法 |
| --- | --- | --- |
| Outcome accuracy | 实际 outcome 与标签完全一致的 Case 占比 | 确定性回归门禁 |
| Finding exact accuracy | 确定性与非确定性 Finding Code 均与标签完全一致的 Case 占比 | 防止 Finding 缺失、重复或越权新增 |
| Recommendation exact accuracy | Recommendation Code 与 current/baseline Evidence 名称绑定和标签完全一致的 Case 占比 | 删除、插入、重复或错误引用建议均失败 |
| Production output accuracy | 报告通过生产 Worker 同一套持久化前验证的 Case 占比 | 防止评测绿灯但真实 Worker 拒绝 |
| Evidence contract accuracy | Engine 独立返回的 Evidence 与 Report 投影完全一致，且 current/baseline Evidence 与 QuerySpec、Fixture 治理身份及聚合值完整绑定的 Case 占比 | 防止顶层证据丢失、证据指纹或查询范围失真 |
| Misleading rate | 意外确定性 Finding 数量除以实际确定性 Finding 总数 | 必须为 0 |
| Conclusive recall | 标签要求的确定性 Finding Code 被实际结果覆盖的比例 | 防止安全修复把应有结论全部抹掉 |
| Evidence reference coverage | Finding、Recommendation、Ledger 和 Hypothesis 中有效 grounding item 的比例 | 验证引用完整性，不代表真实世界证据充分性 |
| Cause verdict accuracy | 原因状态和逐变更 verdict 与标签一致的比例 | 验证支持、反证与不确定规则 |
| Query contract compliance | current/baseline 的模板、治理身份和固定调用合同是否满足预期 | 防止查询范围和次数漂移 |
| Logical SLS calls | 每个 Case 的逻辑观察次数 | 固定为 2 |
| Provider API calls | 每个 Case 的 Provider 调用代理 | 固定为 8 |
| Processed bytes | 两个合成观察的处理字节总和 | 成本代理，不是阿里云账单 |
| Elapsed time | 本机执行耗时 | 只记录用于趋势观察，不作为生产 SLO |

确定性数据集的工程门禁采用严格预期：outcome、应有确定性结论、证据引用、原因判断和查询合同必须全部通过，意外确定性结论必须为零，处理字节不能超过每个 Case 的标签上限。只要任一 Case 或聚合门禁失败，命令仍会打印完整结构化报告，然后返回非零退出码。

本机耗时受操作系统、杀毒软件、编译缓存和 CI 资源影响，M5-A 只记录它，不用一个合成样本耗时冒充生产时延承诺。

## 5. 如何运行

在仓库根目录执行：

```powershell
go run ./cmd/logagent evaluate
```

该命令使用内置数据集，不读取飞书、SLS、发布平台或模型凭据，也不会发起外部网络请求。成功时返回退出码 `0`；数据集非法、Graph 执行失败、单 Case 预期不符或聚合门禁失败时返回非零退出码。

结构化输出包含：

- 数据集 Schema、ID、来源和 SHA-256 指纹；
- 真实故障数、专家标签数、凭据要求和外部网络调用数；
- `evaluation_version=m5a-synthetic-gate-v1`，以及 Graph、查询模板、查询策略、原因方法和评测规则版本；
- 每个 Case 的预期、实际结果、调用/字节代理、失败原因和耗时；
- 聚合质量指标、P50/P95/最大本机耗时、门禁状态和逐项门禁结果。

CLI 输出中的具体 JSON 字段以代码和 `docs/spec.md` 当前契约为准。脚本或 CI 应同时检查进程退出码，不能只搜索某一段文本。

## 6. 版本边界

当前调查 Graph 不调用 LLM，因此：

- Prompt version：`N/A`；
- Prompt quality：`N/A`；
- Token usage/cost：`N/A`；
- 不应为了填满指标面板而构造虚假的 Prompt 或 Token 数字。

结构化报告使用 `prompt_used=false` 表达该边界，不输出虚构的 Prompt version 或 Token 数量。

M5-A 必须记录数据集版本和指纹；Graph、查询模板、查询策略、变更关联方法或评测规则发生影响结果的变化时，也必须在输出中保留对应版本，避免把不同合同下的数字直接比较。

## 7. 能证明与不能证明的内容

本切片能证明：

- 当前代码可以对一组版本化合成 Case 重复执行并得到可核对结果；
- Graph 与 Evidence/CauseAnalysis 合同发生已知回归时，命令可以失败；
- 固定查询次数和处理字节代理没有在这些 Case 中越界；
- 评测全程不需要外部凭据和网络。

本切片不能证明：

- 真实 SLS Schema、采集延迟、查询完整性或账单成本；
- 真实飞书客户端交互和卡片视觉效果；
- 企业历史故障上的准确率、召回率和误导率；
- 专家是否认可标签、关联阈值或下一步建议；
- 生产并发、稳定性、SLO、安全审批或可恢复性；
- 已满足试点群灰度条件。

## 8. 离线验收

本轮运行：

```powershell
Get-ChildItem -Recurse -Filter *.go | ForEach-Object { gofmt -w $_.FullName }
go test -count=1 ./...
go vet ./...
go run ./cmd/logagent evaluate
go run ./cmd/logagent mock-e2e
```

本轮结果：

| 检查 | 结果 |
| --- | --- |
| `gofmt` | 通过 |
| `go test -count=1 ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go run ./cmd/logagent evaluate` | `PASSED`；5/5 Case 通过，outcome/finding/recommendation/production-output/evidence-contract/query/evidence-reference/cause 指标均为 1，misleading rate 为 0 |
| 评测查询与成本代理 | 10 次逻辑观察、40 次 Provider 调用代理、78,080 processed bytes，调用/成本越界均为 0 |
| 数据集身份 | `synthetic-m5a-v1` / `evaluation-dataset-v1`，规范化语义 SHA-256 `caf2714c80a646c5da15134c6557879565ffc8e083a66da1f1c9e49d3d0dc1f8` |
| `go run ./cmd/logagent mock-e2e` | 通过；2 个逻辑观察、8 次 Provider 调用、2 个成功 Checkpoint，外部网络调用为 0 |
| `go test -race ./...` | 未执行；当前 Windows 环境 `CGO_ENABLED=0` 且未安装 GCC |

当前 5 个 Case 都在 1 毫秒内完成，因此毫秒精度下 P50/P95/最大耗时均显示为 0；这只说明本地纯内存合成样例很短，不是生产性能结论。

验收记录必须区分“离线命令通过”和“真实系统未验证”。race 测试受环境限制时也必须明确记录为未执行，不能写成通过。

## 9. 后续阶段

### M5-B：Agent 自观测与回放

- 为 Graph、查询策略和未来可能出现的 Prompt 建立统一版本登记；
- 补充 Agent 自身日志、Trace、工具调用、失败分类和回放趋势；
- 在仍无真实输入时继续使用 Mock，并保持 Prompt/Token 指标为不适用。

### M5-C：真实试点灰度

- 在完成审批与脱敏后建立历史故障回放集和专家标注；
- 接入真实飞书试点群、真实 SLS 资源、反馈闭环和回滚方案；
- 由团队批准真实准确率、安全、时延和成本门槛；
- 只有通过这些门槛后，才讨论扩大服务和用户范围。

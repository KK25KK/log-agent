# 受治理 SOP 知识指引（Mock-first）

## 1. 当前状态

| 项目 | 当前结论 |
| --- | --- |
| 目标 | 把已验证的确定性建议映射为有版本、可追溯、只供人工核查的 SOP 步骤 |
| 状态 | 主体代码与两轮安全加固完成；严格 Evidence/请求窗口绑定、可信组装来源标记、独立超时和可信服务时钟已经落地，最新验证记录见第 8、10 节 |
| 数据边界 | 首期全部来自确定性 Mock；真实企业 SOP 条目数为 0，0 凭据、0 外部网络调用 |
| 对外声明 | 只能称为“受控 SOP 指引离线能力”，不能称为已接企业知识库、根因证明、审批授权或自动处置 |

## 2. 目标与价值

当前 Agent 已能输出日志事实、变更关联候选、跨信号时间线和确定性下一步，但这些建议还没有连接团队维护的排障知识。本能力在不改变事实层和推理层的前提下，为已有 Recommendation 增加受控的人工作业清单。

SOP 的作用是告诉值班人员“接下来可核对什么”，不是证明根因，也不是代替负责人执行变更。

## 3. 范围

### 3.1 包含

- provider-neutral `RunbookSource` 端口。
- 查询输入只包含由完整 Evidence 派生的逻辑 `resource_id`、已验证 Recommendation Code 和固定上限。
- 版本化 Source、Entry Revision、更新时间和内容指纹。
- 关闭的步骤 Code `VERIFY_ERROR_PATTERN / OBSERVE_HOT_INSTANCE / ESCALATE_SERVICE_OWNER`，以及由本地 canonical 模板唯一确定的 `VERIFY / OBSERVE / ESCALATE` 类型与 Instruction。
- 应用层计算 Recommendation/Evidence 引用，Worker 落库前重新校验。
- Source 失败、非法、不完整或无匹配时安全降级。
- Mock 主链与飞书纯文本有界展示。

### 3.2 不包含

- 真实 Wiki、文档平台、CMDB、错误码平台或企业搜索连接器。
- RAG、向量检索、生成式 SOP、让模型选择或改写 SOP。
- URL、Markdown 链接、Shell/SQL、脚本、HTTP 方法、执行参数或原始知识正文。
- 重启、回滚、扩缩容、删改配置、审批消费或任何自动处置。
- 把 SOP 匹配写成根因、处理成功、内容实时或专家批准。
- 修改 Eino Graph、M5 合成数据集、Agent Trace/Replay Schema 或 LLM Prompt。

## 4. 架构与顺序

```text
Eino deterministic report
  -> ValidateEngineOutput
  -> RunbookService.Enrich
       -> require fixed error_analysis_v2 metadata, governance identity and exact Job request windows
       -> keep baseline=0 as data insufficient and perform zero source calls
       -> derive candidate resource_id from the governed current/baseline pair
       -> bind it to trusted Job service/environment/requester through ResourceCatalog Resolve + Allowed
       -> recompute closed Recommendation codes from Evidence and require exact report grounding
       -> RunbookSource.Lookup (at most once, independent 5s child timeout)
       -> compute Recommendation/Evidence grounding locally
       -> attach assembly-owned SYNTHETIC_MOCK or ENTERPRISE_GOVERNED data source
  -> ValidateEngineOutput again
  -> SummaryService.Enrich (SOP is excluded from SummaryInput)
  -> ValidateEngineOutput again
  -> SQLite report JSON + Feishu card
```

SOP 是 Worker 后处理增强，不进入 Eino Graph，因此不能参与查询规划、Finding、原因 verdict 或现有离线评测算法。

## 5. 数据合同

### 5.1 查询

`RunbookQuery` 只允许：

- `resource_id`：来自严格治理的 current/baseline Evidence。两者必须使用固定 `error_analysis_v2` 模板和完整的 Query/Schema/Policy/Governance 元数据，治理身份一致、QuerySpecHash 不同、窗口连续等长，并与可信 Job 请求的当前窗口和前置等长基线窗口精确相等；该资源还必须等于 Job 的 service/environment 经 `ResourceCatalog` 重新解析、requester ACL 允许的资源；
- `recommendation_codes`：应用从完整 Evidence 重算关闭建议集合，只保留同一 Report 中 Code 和 Evidence 引用精确一致的项，再去重并稳定排序；
- `limit`：固定上限。

禁止传入用户消息、飞书身份、Service/Environment、日志文本、TopError、TraceID、模型输出或物理 Provider 定位。

### 5.2 Source 输出

每个 `RunbookEntry` 必须包含稳定 ID、Revision、ResourceID、Title、OwnerTeam、UpdatedAt、匹配 Recommendation Codes 和有界 Steps。Source 不得提供 Evidence ID。

每个 Step 有稳定 ID 和关闭 Code。Source 只能选择 Code；Kind 与单行 Instruction 必须精确匹配本地 `CanonicalRunbookStep` 模板，Provider 不能注入自由步骤文本。执行模式不由 Source 决定，报告投影恒为 `HUMAN_REVIEW_ONLY`。

### 5.3 报告投影

`Report.RunbookGuidance` 是可选兼容字段。每个 Guidance Item 保存 Entry 内容指纹、匹配 Recommendation Codes 和由应用计算的 Evidence ID 并集。

`data_source` 使用关闭集合 `SYNTHETIC_MOCK / ENTERPRISE_GOVERNED`，由创建 `RunbookService` 的可信启动组装层指定并进入持久化投影；Engine 与 `RunbookSource` 都无权自报或覆盖来源。飞书在 `SYNTHETIC_MOCK` 时固定把 SOP 区块标题显示为“受控 SOP 参考（Mock）”，不能把合成目录伪装成企业知识。

状态语义：

| 状态 | 含义 |
| --- | --- |
| `COMPLETE` | Source 查询完整且至少有一个受控匹配；不代表 SOP 内容正确或获批 |
| `NO_MATCH` | Source 查询完整、未截断，但当前目录版本没有匹配；不代表企业不存在 SOP |
| `INCONCLUSIVE` | Source 不完整或截断，不能解释未返回条目 |
| `UNAVAILABLE` | 已有确定性突增但 Recommendation/治理资源缺失，或 Source 禁用、失败、返回非法内容；原报告不受影响 |
| `SKIPPED_NO_TRIGGER` | 没有确定性错误突增，或基线错误数为 0、仍属于数据不足；Source 不被调用 |

## 6. 安全与信任边界

- Source 输出是不可信 Adapter 数据，必须在应用边界和 Worker 成功边界校验。
- Engine 报告不能单独决定知识空间：资源必须重新绑定可信 Job 与 `ResourceCatalog` ACL，Recommendation 必须从 Evidence 重算并与报告精确比对。
- “完整 Evidence”不是只看 `Complete=true`：必须同时校验固定模板、QuerySpec SHA-256、Schema/Policy/Governance 指纹、Progress、Usage、纳秒顺序、固定调用/桶上限、两个窗口的连续等长关系，以及与可信 Job 请求窗口的精确绑定。
- 基线错误数为 0 时沿用确定性 Engine 的 `data_insufficient` 语义，不允许 Runbook 层自行把当前错误数解释为突增。
- Entry 匹配 Code 必须存在于同一 Report；Evidence IDs 必须精确等于这些 Recommendation 的 Evidence 并集。
- 标题/Owner 等元数据必须有效 UTF-8、无控制字符、无周边空白、满足长度限制，并拒绝 URL/URI、Markdown、命令解释器和危险处置词形。
- 步骤不是 Provider 自由文本：关闭 Code 只映射到本地固定的核对、观察和升级联系模板；不存在 Executor Port、审批按钮或状态写入。
- 飞书固定展示“仅供人工核查，不会自动执行处置”。
- `SYNTHETIC_MOCK / ENTERPRISE_GOVERNED` 由可信组装层写入，Engine/Source 返回结构中不存在可覆盖该字段的入口；Mock 的 SOP 区块标题必须显式带“（Mock）”，整张卡片 Header 仍按调查状态展示。
- 飞书在展示前再次校验 `data_source`；空值或未知值一律只显示“来源未确认/当前不可用”，即使结构中夹带 `COMPLETE` 条目也不展示条目内容。
- LLM 输入继续只包含原确定性 Recommendation，不含 SOP Title、Owner、Step 或 Entry ID。

## 7. 失败与兼容

- 每次 Lookup 使用独立的 5 秒子 Context，Adapter 必须遵守该 Deadline；该边界不复用 SLS 查询或 LLM 摘要超时。应用在 Source 返回后、主动 cancel 前再次读取子 Context 状态，因此即使 Source 在 Deadline 后返回 `(set, nil)`，该 Set 也不会被接受。只有父 Context 确实取消/超时才向 Worker 传播；健康父 Context 下独立 5 秒耗尽、Source 自身返回或包装的 cancel/timeout 与其他 Source 错误一样生成 `UNAVAILABLE`，不让原调查失败。
- 完整无匹配生成 `NO_MATCH`；不完整或截断生成 `INCONCLUSIVE`。
- 条目 `UpdatedAt` 必须同时不晚于报告 `GeneratedAt + 5 分钟` 和可信服务时钟 `now + 5 分钟`；服务时钟由应用构造器注入、默认 `time.Now`，不能由 Engine 或 Source 提供。任一边界超出都视为非法并降级。
- Guidance 不能修改、删除或追加原 Recommendation，不能改变 Outcome、Finding、CauseAnalysis、IncidentTimeline 或 Summary 事实来源。
- 字段为指针且 `omitempty`，旧 SQLite Report JSON 可继续解码。

## 8. 离线验收

- [x] 应用层测试用例覆盖突增报告最多调用一次 Source，并形成稳定、可追溯的人工核查 Item。
- [x] 应用层实现明确区分：无确定性突增时零 Source 调用并返回 `SKIPPED_NO_TRIGGER`；确定性突增后缺少 Recommendation 或治理资源时零调用并返回 `UNAVAILABLE`。
- [x] 完整无匹配、Source 失败/禁用/非法、不完整和截断的安全降级已落地并有定向用例。
- [x] 关闭步骤 Code、本地 canonical Kind/Instruction、引用、重复 ID、指纹、安全元数据、集合上限和 `HUMAN_REVIEW_ONLY` 校验已落地并有领域/应用测试用例。
- [x] 可信 Job/Resource Catalog/requester ACL 绑定、Evidence 重算 Recommendation、Source 自身 timeout 降级和未来更新时间拒绝已经加入应用边界。
- [x] Evidence 必须具备固定模板、Query/Schema/Policy/Governance 元数据，两个窗口治理身份一致且连续等长，并精确绑定可信 Job 请求；基线为 0 时保持数据不足、零 Source 调用。
- [x] Lookup 使用独立默认 5 秒超时；条目更新时间同时受报告时间与可信服务时钟约束。
- [x] `data_source` 由可信组装层指定为 `SYNTHETIC_MOCK / ENTERPRISE_GOVERNED`，Mock 飞书 SOP 区块标题固定带“（Mock）”，Engine/Source 不能自报来源。
- [x] Source 在独立 Deadline 后返回 `(set, nil)` 仍降级为 `UNAVAILABLE`；飞书遇到空/非法 `data_source` 时不展示任何 SOP 条目，只显示来源未确认的不可用状态。
- [x] 飞书最多展示两个 SOP、每个最多三步，不包含链接、命令、动作值或执行按钮，并有转义、截断和固定状态文案用例。
- [x] 本轮 `mock-e2e` 实测 `source_calls=1`、`items=1`、`steps=3`，同时保持两个 SLS 观察、八次 Provider 调用和零外部网络。
- [x] `RunbookGuidance` 不进入 Eino、`SummaryInput`、Agent Trace/replay 和当前离线评测数据集/指纹；本轮 `evaluate`、`summary-evaluate` 均为 `PASSED`，数据集指纹分别保持 `caf2714c80a646c5da15134c6557879565ffc8e083a66da1f1c9e49d3d0dc1f8`、`82e813aed0721f15b89a19b053da6b1d47509ab07f45122af4ed0c075e60a0b1`。
- [x] 第二轮全部安全边界落地后的最终工作树已实跑 `go test -count=1 ./...`，全仓通过。
- [x] 最终工作树的 `gofmt -w .`、`go vet ./...`、重点包 `-shuffle=on -count=20`、仓库 Markdown 链接检查和 `git diff --check` 均通过。
- [x] 临时目录重跑快照保存、replay、`replay-compare=COMPARABLE`、十条活动 Mock 反馈和 `rollout-rehearse=REHEARSAL_PASSED`；仍为 `SYNTHETIC_MOCK` 且 `production_action_allowed=false`。
- [ ] Race 在具备 CGO 与 C 编译器的环境完成；环境不具备时必须如实记录为未执行。

## 9. 真实接入前置条件

1. 建立知识条目的 Owner、Revision、更新时间、审批状态、失效和回滚流程。
2. 使用可信租户和 Resource Catalog 约束知识空间，禁止用户指定任意索引或文档。
3. 实现严格 Adapter，只返回上述关闭投影；链接必须另行设计白名单，本合同默认无链接。
4. 保留现有独立 5 秒 Lookup Deadline 和父 Context 语义，真实 Adapter 及其传输层必须遵守该 Deadline；同时补齐租户额度、访问审计、缓存、响应上限、结果未知、内容签名/版本和数据保留策略。生产组装必须注入 `ENTERPRISE_GOVERNED` 和可信服务时钟，禁止由 Adapter 自报来源或时间。
5. 用脱敏历史事故与专家标注验证匹配覆盖、误导风险和内容时效后再进入试点卡片。

## 10. 开发与验收记录

| 日期 | 记录 |
| --- | --- |
| 2026-08-24 | 完成现状调研和合同冻结；确定采用 Worker 后处理、Mock-first、Human-only、不改 Eino/LLM/评测 Schema 的最小切片。 |
| 2026-08-24 | 落地领域合同、`RunbookSource`、应用层检索/降级/校验、Worker“首次校验→SOP enrich→再次校验”顺序、`runbookmock`、飞书有界纯文本展示及定向测试用例；真实 Source 与内容治理仍未接入。 |
| 2026-08-24 | 实跑 `mock-e2e`：Runbook 1 次调用/1 项/3 步，SLS 2 次逻辑观察/8 次 Provider 调用/0 次外部网络；实跑 `demo` 为 `SUCCEEDED` 且 SOP 为 `COMPLETE/HUMAN_REVIEW_ONLY`。 |
| 2026-08-24 | 实跑 `go test -count=1 ./...` 通过一次；`evaluate`、`summary-evaluate` 均为 `PASSED` 且各自数据集指纹不变。 |
| 2026-08-24 | 安全复核后新增关闭 StepCode/本地 canonical 模板、可信 Job/Resource Catalog ACL 绑定、Evidence 建议重算、Source-local timeout 降级和未来更新时间拒绝。 |
| 2026-08-24 | 第一轮安全加固工作树完成 `gofmt`、全仓测试、vet、重点包乱序、仓库链接/diff、Mock E2E、Demo、两类评测和快照/replay/比较/反馈/灰度演练；该记录随后被第二轮复审继续加固。 |
| 2026-08-24 | 第二轮复审补齐可信组装 `data_source`、Mock 飞书标题、固定 Evidence 模板/治理指纹/请求窗口绑定、baseline=0 零调用、独立 5 秒 Lookup 超时和可信服务时钟。 |
| 2026-08-24 | 收口两个 fail-closed 边界：Source 即使在子 Deadline 后返回成功 Set 也按不可用处理；飞书对空/非法 `data_source` 不展示夹带条目。最终工作树重新通过全仓测试、vet、重点包乱序 20 轮、Mock E2E、Demo、两类评测和快照/replay/比较/反馈/灰度演练；安全复查未发现 P0–P3。race 因本机 `CGO_ENABLED=0` 且无 GCC 未执行。 |

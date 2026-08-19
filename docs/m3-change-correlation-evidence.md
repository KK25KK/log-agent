# M3 变更关联证据与反证实现归档

> 当前状态：M3 主体代码、离线自检和独立复核已经完成。本文只记录当前代码能够离线验证的能力，不代表真实阿里云 SLS、飞书客户端、发布平台或配置中心已经联调通过。源码路径指向当前工作树；当前规范以 [`spec.md`](spec.md) 为准。

| 项目 | 内容 |
| --- | --- |
| 阶段 | M3 / 第四期 |
| 日期 | 2026-08-19 |
| 目标 | 把“错误突增”推进为有支持证据、反证和未知项的可反驳变更关联候选 |
| 状态 | 代码与离线验收完成；真实系统联调待部署阶段 |
| 数据源 | M2 受控 SLS 聚合 Evidence + 可选的静态 Change Catalog |
| 置信度方法 | `change-correlation-v1`，确定性启发式，最高 `0.85`，不是概率 |

## 1. 阶段目标

M2 可以回答“错误是否突增、主要模式和热点实例是什么”，但时间接近的发布或配置变化只能被列为排查建议，不能形成可复核的候选假设。

M3 的首个纵向切片增加一条保守的变更关联链路：

```text
完整且确定的 M2 错误突增
  -> 从 Evidence 取得可信 resource_id 和调查时间范围
  -> 查询管理员维护的只读 Change Catalog
  -> 对每个发布/配置候选执行固定的支持测试与反证测试
  -> 生成 SUPPORTED_CANDIDATE / REFUTED / INCONCLUSIVE
  -> 保存假设、证据账本、置信度方法和限制
  -> 在飞书报告与证据页做有界展示
```

本期只输出“关联候选”，不会把发布、配置、错误模式或实例宣传成已经确认的根因，也不会执行回滚、扩容或其他处置。

## 2. 相对 M2 的增量

| 维度 | M2 | M3 增量 |
| --- | --- | --- |
| SLS 查询 | 当前/基线各四次固定聚合 | 完全复用，不增加 SLS 字段、调用或费用预算 |
| 外部上下文 | 无 | 可选、版本化、管理员维护的 Change Catalog |
| 报告 | Finding、Recommendation、Evidence | 增加可选 `CauseAnalysis` |
| 推理 | 错误增长、模式和实例分布 | 每个变更执行固定 7 项支持/反证测试 |
| 结论强度 | 确定性错误突增事实 | 关联候选、硬反证或不确定；永不提升成根因事实 |
| 持久化 | Evidence 和 Report | 增加独立 `evidence_ledger` 账本表 |
| 飞书 | 报告与聚合证据 | 增加有界的变更、候选、支持/反证与限制展示 |

一次调查仍然只有两个逻辑 SLS 观察，每个观察四次 Provider 请求，合计最多八次固定请求。Change Catalog 是本地只读补充源，不是新的 SLS 查询模板。

## 3. 架构与执行路径

### 3.1 Change Source 是可替换端口

应用拥有 `ports.ChangeSource`：

```go
List(ctx context.Context, query domain.ChangeQuery) (domain.ChangeSet, error)
```

首个实现是 `internal/adapters/changecatalog` 的严格 JSON Catalog。未设置 `LOG_AGENT_CHANGE_CATALOG` 时使用 Disabled Source，显式返回 `change_source_disabled`，而不是伪造“完整但没有变更”的结果。

变更查询中的 `resource_id` 来自已通过 Query Gateway 的当前/基线 Evidence，时间范围固定为：

```text
[baseline.start_time, current.end_time)
```

用户消息、卡片按钮和模型均不能提交物理资源 ID、Change Catalog 查询条件或候选变更。

### 3.2 Eino Graph 只增加确定性关联节点

当前固定 Graph 为：

```text
plan_queries
  -> execute_queries
  -> build_report
  -> correlate_changes
  -> END
```

`correlate_changes` 仅在 M2 已形成确定性的 `spike_detected` 时访问 Change Source。没有显著突增时状态为 `SKIPPED_NO_SPIKE`；M2 数据不足时状态为 `INCONCLUSIVE`，不会为了归因再发起额外查询。

Change Source 未配置、返回错误或返回不合法数据时，原因分析降级为 `UNAVAILABLE`。已经成立的 M2 错误突增事实和调查成功状态保持不变。只有上游 `context` 取消会继续向 Worker 传播，以维持取消语义。

### 3.3 三层输出校验

1. Change Catalog 在加载时拒绝未知字段、重复 ID、非法时间、越界字符串和非法实例集合。
2. Eino 节点验证 ChangeSet 的资源、时间范围、完整性、数量和引用关系，并生成稳定的 Hypothesis/Ledger ID。
3. Worker 在成功提交前再次校验固定 7 项测试、角色、结果、有限数值、Evidence/Change 引用、置信度重算结果、支持条件和硬反证质量。

任一非法引擎输出都不能作为成功报告持久化。

## 4. 数据模型

### 4.1 变更源模型

- `ChangeQuery`：可信 `resource_id`、开始/结束时间和最大数量。
- `ChangeEvent`：`RELEASE` 或 `CONFIG`、时间范围、版本、负责人、摘要、受影响实例及其完整性标记。
- `ChangeSet`：源版本、候选列表、`complete`、`truncated` 和原因码。

`ChangeSet.complete` 描述“候选集合是否完整”，`affected_instances_complete` 描述“单个变更的实例范围是否完整”。二者不能相互替代。

### 4.2 原因分析模型

`Report.CauseAnalysis` 是可选字段，旧报告没有该字段时仍可以正常解码。

| 类型 | 取值 | 含义 |
| --- | --- | --- |
| Analysis Status | `COMPLETE` | 本次候选集合和测试可以按既定规则完整表达 |
| Analysis Status | `INCONCLUSIVE` | 存在未知输入、候选缺失、截断或无法消除的混杂 |
| Analysis Status | `UNAVAILABLE` | Change Source 未配置、失败或返回非法数据 |
| Analysis Status | `SKIPPED_NO_SPIKE` | M2 没有形成确定性的错误突增 |
| Hypothesis Verdict | `SUPPORTED_CANDIDATE` | 所有支持测试通过、所有反证测试未发现反证，且候选集合完整 |
| Hypothesis Verdict | `REFUTED` | 在完整可比的实例范围上发现零交集硬反证 |
| Hypothesis Verdict | `INCONCLUSIVE` | 其他所有不能安全支持或反驳的情况 |
| Ledger Role | `SUPPORT` / `COUNTER` | 该测试用于支持还是寻找反证 |
| Ledger Result | `PASS` / `FAIL` / `UNKNOWN` | 支持条件成立、反证被发现，或输入不足；含义需结合 Role 阅读 |

对于 `COUNTER` 项，`PASS` 表示发现了反证，`FAIL` 表示已检查但没有发现该反证。`UNKNOWN` 永远不会被折算成“没有反证”。

## 5. Change Catalog 配置

### 5.1 启用方式

复制并修改示例：

```powershell
Copy-Item .\config\change-catalog.example.json .\config\change-catalog.json
$env:LOG_AGENT_CHANGE_CATALOG = ".\config\change-catalog.json"
go run ./cmd/logagent worker
```

不设置或清空 `LOG_AGENT_CHANGE_CATALOG` 会安全关闭变更关联，但 M2 调查仍可运行。Catalog 在 Worker 启动时一次性加载；文件非法会使 Worker 启动失败，当前不支持热重载。

### 5.2 JSON 契约

```json
{
  "version": "2026-08-19.1",
  "events": [
    {
      "id": "chg_order_release_v2",
      "resource_id": "order-service-prod",
      "kind": "RELEASE",
      "started_at": "2026-08-19T09:55:00Z",
      "completed_at": "2026-08-19T09:58:00Z",
      "from_version": "v1",
      "to_version": "v2",
      "owner": "order-team",
      "summary": "经批准的变更摘要",
      "affected_instances": ["order-pod-a"],
      "affected_instances_complete": true
    }
  ]
}
```

关键约束：

- `version` 必填，最大 64 字节；`events` 必须显式为数组，可以为空。
- `resource_id` 必须与 SLS 资源目录中的资源 ID 完全一致。
- `kind` 只允许 `RELEASE` 或 `CONFIG`。
- `completed_at` 必须晚于 `started_at`。
- `RELEASE` 必须提供 `to_version`；同时提供起止版本时，两者必须不同。
- Event ID 和 Resource ID 只能使用字母、数字、点、下划线和连字符，最大 128 字节。
- `owner` 最大 128 字节，`summary` 最大 512 字节；文本不能包含首尾空白或控制字符。
- `affected_instances` 必须显式为数组，最多 20 个、不可重复，每个值最大 128 字节。
- `affected_instances_complete` 必须显式填写。只有 `true` 才允许做实例交集、集中度和基线变化的确定性比较。
- Catalog 按 `resource_id` 精确匹配，只返回与查询半开区间相交的事件，并按开始时间从新到旧排序。
- 单次关联最多返回 10 个候选；超过时标记 `truncated`，不能形成支持结论。
- Catalog 是治理配置，不得放入 AccessKey、Token、原始日志正文或未批准的外部 URL。

## 6. SQLite 表结构与事务

M3 新增：

```sql
CREATE TABLE evidence_ledger (
    entry_id TEXT PRIMARY KEY,
    investigation_id TEXT NOT NULL,
    hypothesis_id TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
```

并建立 `(investigation_id, hypothesis_id, entry_id)` 索引。ChangeEvent、Hypothesis 和完整 CauseAnalysis 同时保存在 `investigations.report_json`；账本项另外以 JSON 保存到 `evidence_ledger`，便于按调查和假设审计。

`FinishSuccess` 在同一个 SQLite 事务中完成：

1. 校验当前 Job 的租约和 attempt fencing；
2. 插入 M2 Evidence；
3. 插入 M3 Evidence Ledger；
4. 保存包含 CauseAnalysis 的 Report；
5. 更新调查和 Job 为 `SUCCEEDED`；
6. 写入终态飞书 Delivery 事件。

账本重复 ID 或任一步骤失败都会回滚整个成功事务，不会留下“报告成功但账本缺失”的部分状态。

## 7. 固定 7 项测试与权重

| Code | Role | 权重 | `PASS` 条件 |
| --- | --- | ---: | --- |
| `error_spike` | SUPPORT | `0.25` | M2 已用完整当前/基线 Evidence 确认显著错误突增 |
| `temporal_precedence` | SUPPORT | `0.20` | 变更完成时间位于 `[baseline.start_time, current.start_time)` |
| `affected_instance_concentration` | SUPPORT | `0.30` | 受影响实例承载当前错误的占比至少 `50%` |
| `baseline_shift` | SUPPORT | `0.10` | 受影响实例错误占比较基线至少上升 `20` 个百分点 |
| `no_instance_overlap` | COUNTER | `0.40` | 完整当前实例分布与完整受影响实例集合零交集；这是硬反证 |
| `preexisting_concentration` | COUNTER | `0.15` | 基线中受影响实例已经承载至少 `50%` 的错误 |
| `confounding_changes` | COUNTER | `0.10` | 完整候选集合中同时存在多个重叠变更 |

置信度计算：

```text
score = 所有 PASS 的 SUPPORT 权重之和
      - 所有 PASS 的 COUNTER 权重之和

confidence = round(clamp(score, 0, 0.85), 2)
```

`FAIL` 和 `UNKNOWN` 不加减分。最高 `0.85` 来自四项支持权重之和，明确给不确定性和未接入的数据源留下空间；该分数不是统计概率、模型概率或因果强度。

## 8. 支持、反证与不确定规则

### 8.1 支持候选

只有同时满足以下条件，Verdict 才能是 `SUPPORTED_CANDIDATE`：

- M2 已形成确定性错误突增；
- ChangeSet 完整且未截断；
- 四项 SUPPORT 全部 `PASS`；
- 三项 COUNTER 全部 `FAIL`；
- 所引用的 Evidence 完整且未截断。

即使全部成立，文案仍只能说“可反驳的变更关联候选”，不能说“根因已确认”。

### 8.2 硬反证

`no_instance_overlap=PASS` 时，候选为 `REFUTED`。该判断只在以下输入同时可信时成立：

- 当前 Evidence 完整且实例分布穷尽；
- 实例标签未脱敏；
- 变更的 `affected_instances_complete=true` 且列表非空。

只要任一前提缺失，结果就是 `UNKNOWN`，不能把 Top-K 未返回某实例解释成零交集。

### 8.3 必须保持不确定的情况

- 当前或基线 Evidence 不完整、截断；
- 实例分布只是未穷尽 Top-K；
- 实例标签被脱敏；
- 变更影响实例范围不完整或为空；
- ChangeSet 不完整、被截断或源版本缺失；
- 完整窗口中没有候选变更；
- 同一窗口存在多个候选变更；
- 时序、当前集中度或相对基线变化未满足支持阈值；
- Change Source 未配置、失败或返回不合法数据。

这些情况分别进入 `INCONCLUSIVE` 或 `UNAVAILABLE`，不会修改已经成立的 M2 Finding，也不会自动生成新的 SLS 查询。

## 9. 飞书展示

最终报告卡增加原因分析状态、首个有界候选、Verdict、启发式置信度和“关联不等于因果”限制。证据页有界展示变更元数据、支持测试和反证测试。

所有 Change Catalog 文本仍被视为不可信展示内容，经过长度限制、Markdown 转义和整体卡片大小限制。卡片不会展示原始日志、原始 SQL、Provider 错误正文或任意可点击外部 URL。

## 10. 验证结果

当前测试代码已覆盖：

- Change Catalog 严格解码、排序、半开区间重叠、截断、取消和防御性复制；
- 支持候选、完整零交集反证、Top-K/脱敏/多变更不确定；
- Change Source 未配置、失败或返回非法数据时降级，无突增跳过，以及取消传播；
- Worker 对固定 7 项测试、引用、权重、有限分数和 Verdict 的二次校验；
- `UNKNOWN`、Top-K、脱敏和多变更混杂不能被包装成支持或硬反证；
- 无 Change 引用、伪造引用、未被假设引用的孤儿账本项，以及证据质量不足的零交集反证都会被拒绝；
- SQLite 账本重启持久化，以及重复账本 ID 时整笔事务回滚；
- 飞书卡片的有界原因摘要、支持与反证展示；
- 默认不配置 Catalog 时的安全关闭行为。

截至 2026-08-19 的最终离线验证结果：

| 检查 | 状态 |
| --- | --- |
| `gofmt -w`（全部 Go 文件） | 通过 |
| `go test -count=1 ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go mod tidy -diff` | 通过，无依赖差异 |
| `go run ./cmd/logagent demo` | 通过，输出一个 Mock `SUPPORTED_CANDIDATE`；仍为 2 个逻辑观察、8 次固定 Provider 调用 |
| M3 关键包重复测试 | 通过，最终收口时重复 20 次 |
| `go test -race ./...` | 未通过环境门槛：当前 `CGO_ENABLED=0`，且 Windows 环境没有可用 C 编译器 |
| 真实 SLS / 飞书 / 发布平台端到端 | 未执行，仓库没有试点资源与凭据 |

离线通过只证明代码合同、持久化语义和适配边界符合当前规范；Race、真实资源权限、真实客户端展示及外部变更数据质量仍属于部署验收。

## 11. 尚未验证和未实现

尚未真实验证：

- Change Catalog 中的资源 ID、实例 ID 与企业真实 SLS 标签是否一致；
- 真实发布/配置记录的时钟、延迟、漏报率和完整性口径；
- 真实飞书客户端对 M3 报告卡和证据页的展示效果；
- 生产数据库、迁移工具、多实例并发和长时间运行；
- 企业对 50%、20 个百分点及各项权重的专家标定。

明确未实现：

- 发布平台、配置中心、CMDB 的实时连接器或 Webhook；
- SLS 版本分布、首次出现时间、发布前后固定查询；
- Trace、指标、Kubernetes 事件、Pod/主机和服务拓扑关联；
- 企业错误码、SOP、知识库检索；
- LLM 生成根因、任意 SQL/SPL 或自动处置。

## 12. 已知限制

- Change Catalog 是进程启动时加载的静态文件，没有热更新、签名、审批流或来源证明。
- `resource_id` 和实例标签依赖运维配置一致性，当前没有跨 Catalog 自动对账。
- 关联窗口只覆盖等长基线开始到当前窗口结束，可能漏掉更早但仍有影响的变更。
- 当前只支持 `RELEASE` 和 `CONFIG`，没有 Pod、主机、依赖、告警或拓扑事件。
- 一个候选固定只有 7 项启发式测试，阈值和权重尚未用历史故障集校准。
- 多变更只标记混杂，不做独立贡献拆分。
- 完整无候选不会被当作“没有变更导致故障”的证明，而是保守保持不确定。
- SQLite 仍没有正式 schema version、迁移和回滚工具。

## 13. 升级与回滚

### 升级

1. 停止 `worker` 与 `feishu` 进程。
2. 备份当前 SQLite 数据库文件。
3. 准备并离线校验 Change Catalog；不要把凭据写入 Catalog。
4. 设置 `LOG_AGENT_CHANGE_CATALOG` 后启动 Worker。
5. 先用 Mock 或测试资源观察 `CauseAnalysis`，确认资源与实例标识一致后再扩大范围。

当前 SQLite 打开时会增量创建 `evidence_ledger` 表，但这不等于已经具备正式数据库迁移能力。

### 功能回滚

清空 `LOG_AGENT_CHANGE_CATALOG` 并重启 Worker，即可把原因增强切回 Disabled Source。之后新的 M2 报告仍会成功，只把 `CauseAnalysis` 标为 `UNAVAILABLE`。历史报告和账本不会自动删除。

二进制降级和数据库回滚没有经过正式验证。需要降级旧版本二进制时，应先停机、保留数据库备份，并在隔离副本上验证；不要直接删除 `evidence_ledger` 或改写历史 `report_json`。

## 14. 关键代码位置

| 位置 | 职责 |
| --- | --- |
| `internal/domain/cause.go` | Change、Hypothesis、Ledger、Status/Verdict 领域契约 |
| `internal/ports/ports.go` | 可替换的只读 `ChangeSource` 端口 |
| `internal/adapters/changecatalog/` | 严格版本化 JSON Catalog 与 Disabled Source |
| `internal/adapters/eino/engine.go` | 关联节点、固定 7 项测试、Verdict 和确定性分数 |
| `internal/application/worker.go` | 原因分析结构、引用、分数和支持结论二次校验 |
| `internal/adapters/sqlite/store.go` | Evidence Ledger 与调查成功事务 |
| `internal/adapters/feishu/renderer.go` | 有界的原因摘要与证据卡片 |
| `cmd/logagent/main.go` | Demo Change Source 与 Worker 依赖组装 |
| `cmd/logagent/sls.go` | `LOG_AGENT_CHANGE_CATALOG` 对应 Source 构造 |

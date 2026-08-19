# M4-A：可恢复的付费查询步骤

> 阶段：第五期第一个纵向切片
>
> 当前状态：主体代码与离线验收完成；真实云端和生产部署验收待后续切片
>
> 数据边界：默认验收使用飞书与 SLS Mock，不代表真实云端联调或生产可靠性

## 1. 为什么先做这一小步

M3 以前，Worker 只把“整次调查”作为一个任务。current 窗口已经查完、baseline 尚未查询时，如果进程崩溃，租约恢复会从头再次查询两个窗口。这不会破坏本地状态，却可能重复消耗 SLS 查询容量。

M4-A 把两个有外部成本的逻辑观察单独持久化：

```text
调查 Job
  -> sls.current  -> 规范化聚合 QueryResult Checkpoint
  -> sls.baseline -> 规范化聚合 QueryResult Checkpoint
  -> 可安全重算的 Evidence / Report / CauseAnalysis
```

Eino 仍只负责确定性编排；步骤状态、结果复用、租约 fencing 和恢复决策属于应用与存储层。

## 2. 能保证什么

- 同一调查、同一逻辑输入和治理输入指纹的 `SUCCEEDED` 步骤会直接复用，不再次访问 SLS。
- current 已落盘而 baseline 未开始时崩溃，恢复后只查询 baseline。
- 两个窗口均已落盘而最终报告事务尚未提交时崩溃，恢复后不会再查询 SLS。
- 每次 Prepare/Complete 同时校验 job ID、investigation ID、lease owner、job attempt、有效租约、step key 和 input hash；旧 Worker 不能覆盖新 attempt。
- 治理输入指纹绑定 Catalog/物理资源、Selector、Schema、模板、策略和全部查询预算；配置漂移时旧 Checkpoint 不会与新查询结果混合比较。
- Checkpoint 只保存 Query Gateway 返回的规范化聚合结果，不保存原始日志、SQL、凭据或 Provider 原始错误。

## 3. 不能保证什么

阿里云 SLS 查询接口没有接受应用幂等键的合同。若进程在“请求可能已经到达 SLS”之后、`SUCCEEDED` Checkpoint 提交之前消失，本地无法证明这次查询执行了还是没有执行。

此时系统必须：

1. 将步骤标记为 `UNKNOWN`；
2. 将调查标记为 `NEEDS_REVIEW`；
3. 不自动再次调用 SLS；
4. 只允许用户明确知晓“可能重复查询成本”后创建一条新调查。

如果用户在查询执行中主动取消，调查保持 `CANCELLED`，但在途 `STARTED` 步骤会原子转为 `UNKNOWN`，调查记录只保存稳定原因码。飞书取消卡必须展示相同的成本风险，并且普通 `rerun` 会被服务端拒绝；只有卡片专用的 `rerun_with_cost_ack` 动作可以创建新调查。

因此 M4-A 不宣称 Provider exactly-once。它保证的是“已确认落盘的步骤不重复”和“结果未知的付费步骤不静默重试”。

## 4. 状态与故障语义

### 调查状态

```text
QUEUED -> RUNNING -> SUCCEEDED
                  -> FAILED
                  -> CANCELLED
                  -> NEEDS_REVIEW
```

`NEEDS_REVIEW` 对当前调查是终态；重新运行会创建新的调查 ID，不复活旧 job。

### 查询步骤状态

```text
不存在 -> STARTED -> SUCCEEDED
                  -> FAILED
                  -> UNKNOWN
```

- `SUCCEEDED`：存在可校验、可复用的规范化结果。
- `FAILED`：已知的策略、Schema 或预算失败，不需要恢复该步骤。
- `UNKNOWN`：外部结果不明，必须人工决定是否新建调查。

## 5. 本切片范围

包含：

- SQLite `query_steps` 增量表；
- application-owned Checkpoint Executor；
- Worker 执行上下文中的 job fencing token；
- `NEEDS_REVIEW` 持久化、Delivery 事件和飞书安全提示卡；
- Demo、双 Mock 与崩溃点离线测试。

不包含：

- 对结果未知的付费查询自动重试；
- 完整的瞬时/永久/限流错误分类和退避调度；
- Delivery DEAD 的运维查询与安全重放；
- 多租户持久配额、成本结算与审批；
- 正式 schema migration/rollback 工具和生产关系库；
- 真实飞书、真实 SLS、进程强杀部署演练。

这些分别进入 M4-B 和 M4-C，不能用 SQLite/Mock 验收冒充生产完成。

## 6. 离线验收

已经通过离线测试证明：

1. current 成功后关闭并重开 SQLite，恢复只执行 baseline；
2. 两个步骤均成功后恢复，Provider 新增调用为零；
3. 遗留 `STARTED` 恢复为 `UNKNOWN/NEEDS_REVIEW`，Provider 新增调用为零；
4. stale attempt、过期租约、输入指纹变化和非法/过大输出全部 fail closed；
5. 双 Mock 主链仍为两个逻辑观察、八次 Provider 调用，并新增两个成功 Checkpoint；
6. `gofmt`、全仓离线测试、`go vet`、`mock-e2e` 和 `demo` 通过；
7. `go test -race` 若受本机 CGO/C 编译器限制，必须如实记录为未执行，而不是写成通过。

本轮验证结果：

| 检查 | 结果 |
| --- | --- |
| 真实文件 SQLite + Worker + Eino 三类崩溃恢复测试 | 通过；聚焦用例连续运行 30 次通过 |
| 全仓 `go test -count=1 ./...` | 通过 |
| 全仓 `go vet ./...` | 通过 |
| `go run ./cmd/logagent mock-e2e` | 通过；2 个逻辑观察、8 次 Provider 调用、2 个成功 Checkpoint |
| `go run ./cmd/logagent demo` | 通过 |
| `go test -race ./...` | 未执行；当前 Windows 环境 `CGO_ENABLED=0` 且未安装 GCC |

## 7. 后续顺序

M4-B 优先演进现有 `delivery_events`，增加 Provider-neutral 错误分类、attempt 审计和安全死信重放；不会再搭建第二套重复的通用队列。之后再加入持久化租户配额和只读之外的审批契约。

M4-C 需要团队给出生产数据库、HA/备份恢复、Tenant 策略、真实审批身份源和试点环境，才能做多实例与故障演练。

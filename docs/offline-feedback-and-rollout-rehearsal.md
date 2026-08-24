# 离线反馈账本与灰度决策演练

| 元数据 | 值 |
| --- | --- |
| 版本 | 1.0 |
| 状态 | 代码与离线验收完成；不允许生产动作 |
| 日期 | 2026-08-20 |
| 父规范 | [`spec.md`](spec.md) |

## 1. 解决什么问题

离线评测和回放比较只能告诉我们“代码指标有没有变化”，不能表达审核人是否认为某个 Case 安全、是否缺少上下文，也不能据此形成可审计的灰度判断。

本能力在不接真实审核人、不接部署平台的前提下，补齐两层合同：

1. 将关闭枚举的审核结论绑定到不可变评测快照和 Case；
2. 将快照比较、活动反馈和版本化策略组合为只读灰度演练结论。

它不会修改飞书可用范围、Worker、SLS、部署、流量、开关或任何生产状态。

## 2. 数据流

```text
base snapshot ───────────────┐
                            ├─ strict replay comparison ─┐
candidate snapshot ─────────┘                            │
       │                                                 ├─ rollout rehearsal decision
       └─ synthetic feedback seed                         │
             └─ append-only feedback files ─ active set ─┘

decision.data_source = SYNTHETIC_MOCK
decision.production_action_allowed = false
```

## 3. 反馈记录合同

Schema 为 `evaluation-feedback-v1`。每条记录只保存：

- 反馈 ID、创建时间和内容哈希；
- 目标评测 Run ID、快照内容哈希和版本指纹；
- Case ID、适配层提供的 Reviewer 引用；
- 关闭的 Verdict、Reason Code；
- 可选的 `supersedes` 纠正引用。

Verdict 只有：

- `SAFE`
- `UNSAFE`
- `UNSURE`

记录不保存自由文本、报告正文、Evidence 正文、日志、查询、凭据、Provider 错误或真实身份。内容哈希用于完整性检测，不构成真实 Reviewer 的身份认证或不可抵赖签名。

纠正只能追加新记录，并且必须指向同一快照、同一 Case、同一 Reviewer 的当前活动记录。覆盖旧文件、分叉、循环、缺失父记录、跨 Run/Case/Reviewer、未知字段、尾随 JSON、哈希变化和越界数量全部 fail closed。

文件存储位于独立 `feedback-dir`，不会扩展生产 Investigation Store，也不会修改 Replay Snapshot Store。

## 4. 灰度演练合同

策略 Schema 为 `rollout-rehearsal-policy-v1`，默认策略要求：

- 每个 Case 至少两名独立 Reviewer；
- 当前 18 个评测 Gate 全部存在并通过；
- B3 比较不存在任何回归；
- 所有 Case 都有完整反馈覆盖；
- 没有 `UNSAFE`、`UNSURE` 或 Reviewer 分歧；
- 数据源固定为 `SYNTHETIC_MOCK`；
- `production_action_allowed` 固定为 `false`。

输出状态只有：

| 状态 | 含义 |
| --- | --- |
| `REHEARSAL_PASSED` | 离线合成合同全部通过，不代表真实灰度批准 |
| `REHEARSAL_INSUFFICIENT_EVIDENCE` | 不可比、覆盖不足、quorum 不足、`UNSURE` 或分歧 |
| `REHEARSAL_BLOCKED` | 预检发现候选失败、Gate/指标回归或一致的 `UNSAFE` 反馈 |
| `REHEARSAL_ROLLBACK_RECOMMENDED` | 仅在显式模拟 active-pilot 阶段发现同类阻断信号 |

任何非通过状态都返回非零退出码。回滚建议只是离线结果，不调用部署或流量接口。

## 5. 运行方式

先保存两次评测运行：

```powershell
go run ./cmd/logagent evaluate --snapshot-dir .\data\evaluation-runs
go run ./cmd/logagent replay `
  --snapshot-dir .\data\evaluation-runs `
  --run-id <base-run-id>
```

为候选 Run 生成两名虚拟 Reviewer 对五个 Case 的十条反馈：

```powershell
go run ./cmd/logagent feedback-seed `
  --snapshot-dir .\data\evaluation-runs `
  --feedback-dir .\data\evaluation-feedback `
  --run-id <candidate-run-id>
```

执行预检演练：

```powershell
go run ./cmd/logagent rollout-rehearse `
  --snapshot-dir .\data\evaluation-runs `
  --feedback-dir .\data\evaluation-feedback `
  --base-run-id <base-run-id> `
  --candidate-run-id <candidate-run-id>
```

模拟已经进入试点后的回滚判断：

```powershell
go run ./cmd/logagent rollout-rehearse `
  --snapshot-dir .\data\evaluation-runs `
  --feedback-dir .\data\evaluation-feedback `
  --base-run-id <base-run-id> `
  --candidate-run-id <candidate-run-id> `
  --phase simulated-active-pilot
```

脚本必须检查退出码，不能只匹配 JSON 文本。

## 6. 代码边界

| 路径 | 职责 |
| --- | --- |
| `internal/evaluation/feedback` | 严格反馈记录、哈希、纠正图和活动投影 |
| `internal/adapters/feedbackfs` | append-only 本地文件存储与严格重载 |
| `internal/evaluation/rollout` | 策略、quorum、决策状态与原因码 |
| `cmd/logagent/rollout.go` | Mock 反馈和只读演练 CLI 组装 |

## 7. 离线验收证据

- 两名虚拟 Reviewer 覆盖五个内置 Case，共十条活动反馈；
- 重复 seed 保持同一记录身份，不复制历史；
- 重开存储后内容哈希和纠正历史稳定；
- 重复、篡改、未知字段、尾随、越界、路径穿越和非法纠正图均被拒绝；
- 完整同意返回 `REHEARSAL_PASSED`；
- 缺反馈、quorum 不足、`UNSURE`、分歧和不可比返回证据不足；
- 候选失败、Gate 删除/失败、指标回归和一致 `UNSAFE` 反馈触发阻断或模拟回滚建议；
- 全链零凭据、零外部网络调用、零生产状态修改。

2026-08-24 在最新受治理 SOP 安全加固工作树上，临时目录链路重新生成十条活动 Mock 反馈，`rollout-rehearse` 返回 `REHEARSAL_PASSED`、`SYNTHETIC_MOCK` 和 `production_action_allowed=false`；临时数据随后从工作树清理。这不是实际 Reviewer 结论或生产灰度批准。

## 8. 不能证明什么

- 虚拟 Reviewer 不是专家标注，也不能预测真实审核意见；
- 合成 Case 不是历史真实事故；
- 默认策略不是团队批准的生产阈值；
- 内容哈希不是身份认证；
- 本地文件存储不是生产反馈数据库；
- `REHEARSAL_PASSED` 不是灰度许可；
- `REHEARSAL_ROLLBACK_RECOMMENDED` 不代表真实回滚已执行或已验证成功。

真实试点仍需要审批过的数据、Reviewer 身份与权限、反馈留存策略、生产数据库、试点范围、团队阈值和人工停止/回滚 Runbook。

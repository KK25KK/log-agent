# 联合 RCA 专家评审单

> 复制本模板到 Git 忽略的 `data/joint-rca-reviews/` 后填写。不要在文档中保存原始 TraceID、原始日志、凭据、客户数据或未批准的代码正文。

## 评审身份

| 字段 | 填写值 |
| --- | --- |
| Case ID |  |
| Report fingerprint |  |
| Reviewer reference |  |
| Reviewer role / service familiarity |  |
| Review time (UTC) |  |
| 是否参与过原事故 | 是 / 否 |

## 数据边界确认

| 检查项 | 结果 |
| --- | --- |
| 原始 TraceID 已删除，仅保留指纹 | 通过 / 不通过 |
| 原始日志与个人数据已删除或脱敏 | 通过 / 不通过 |
| 凭据、Token、内部 URL 参数已删除 | 通过 / 不通过 |
| 未批准代码正文未进入评审材料 | 通过 / 不通过 |
| 部署 Commit 来源可追溯且匹配事故时刻 | 通过 / 不通过 |

任一项不通过时停止评审，不把该 Case 计入真实指标。

## 候选逐项评审

| Candidate ID | 文件/行是否有帮助 | Anchor 是否匹配运行时 | 变更关系是否正确 | Verdict 是否保守 | 结论 |
| --- | --- | --- | --- | --- | --- |
|  | 是 / 否 / 未知 | 是 / 否 / 未知 | 是 / 否 / 未知 | 是 / 否 | ACCEPT / REJECT / UNSURE |

允许的拒绝原因：

- `WRONG_DEPLOYMENT_VERSION`
- `WRONG_CODE_LOCATION`
- `RUNTIME_EVIDENCE_MISSING`
- `CHANGE_CORRELATION_OVERSTATED`
- `MISSING_CRITICAL_INPUT`
- `UNSAFE_RECOMMENDATION`
- `OTHER_REQUIRES_GOVERNANCE_REVIEW`

## 根因标签（人工事实）

| 字段 | 填写值 |
| --- | --- |
| 根因是否由事故复盘确认 | 是 / 否 |
| 根因类别 | 代码 / 配置 / 数据 / 下游依赖 / 基础设施 / 操作 / 未确认 |
| 关联仓库逻辑 ID |  |
| 关联文件/符号（不贴正文） |  |
| 证明材料引用 | 事故复盘、工单或受控文档引用；不要复制敏感正文 |

如果事故本身没有确认根因，必须标记“未确认”，不能为了评测强行补标签。

## 人工动作质量

| Action Code | 是否安全 | 是否可执行 | 是否有助于缩小范围 | 备注 |
| --- | --- | --- | --- | --- |
|  | 是 / 否 | 是 / 否 | 是 / 否 |  |

## 最终 Verdict

- `SAFE_AND_USEFUL`：边界安全，候选或缺失项能帮助排障；
- `SAFE_BUT_NOT_USEFUL`：没有误导，但没有有效缩小范围；
- `UNSAFE_OR_MISLEADING`：泄露、越权、伪确定根因或危险动作；
- `INSUFFICIENT_CONTEXT`：材料不足，不能评价。

最终 Verdict：

补充说明（不得包含敏感正文）：

## 双人复核

真实试点至少由两名独立 Reviewer 对同一不可变 Report fingerprint 评审。存在 `UNSAFE_OR_MISLEADING`、Reviewer 分歧或数据边界不通过时，Case 不得用于扩大灰度。

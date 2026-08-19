# Log Agent 文档索引

## 当前有效文档

| 文档 | 作用 | 维护规则 |
| --- | --- | --- |
| [`spec.md`](spec.md) | 唯一当前技术与行为契约 | 行为代码变更前先更新 |
| [`roadmap.md`](roadmap.md) | 当前阶段边界、完成状态和后续计划 | 只写可独立验收的阶段 |
| [`local-mock-e2e.md`](local-mock-e2e.md) | 飞书 + SLS 双 Mock 的运行、输出与边界 | Mock 行为变化时同步更新 |
| [`m4-recoverable-query-steps.md`](m4-recoverable-query-steps.md) | 第五期 M4-A 的检查点、结果未知与恢复合同 | M4-A 行为或验收变化时同步更新 |
| [`m5-offline-evaluation-gate.md`](m5-offline-evaluation-gate.md) | 第六期 M5-A 的全合成离线评测数据、指标、门禁与声明边界 | 数据集、评测规则或 CLI 行为变化时同步更新 |
| [`m5-agent-observability-replay.md`](m5-agent-observability-replay.md) | 第七期 M5-B 的 Agent 自观测/回放合同；当前 B1 已完成，B2/B3 待实现 | Agent 事件、版本清单、Trace 门禁或回放边界变化时同步更新 |
| [`../README.md`](../README.md) | 运行、配置和项目入口 | 必须与当前代码一致 |

## 历史阶段归档

| 阶段 | 文档 | 状态 |
| --- | --- | --- |
| M0 / 第一期 | [`m0-implementation-archive.md`](m0-implementation-archive.md) | 历史叙事归档；没有独立源码快照 |
| M1 / 第二期 | [`m1-readonly-query-foundation.md`](m1-readonly-query-foundation.md) | 历史叙事归档；没有独立源码快照 |
| M2 / 第三期 | [`m2-error-spike-investigation-loop.md`](m2-error-spike-investigation-loop.md) | 历史叙事归档；当前树仍可对照其主体实现 |
| M3 / 第四期 | [`m3-change-correlation-evidence.md`](m3-change-correlation-evidence.md) | 代码与离线验收完成；真实系统联调待部署阶段 |
| M4-A / 第五期首个切片 | [`m4-recoverable-query-steps.md`](m4-recoverable-query-steps.md) | 代码与离线验收完成；不代表完整 M4 或生产验收 |
| M5-A / 第六期首个切片 | [`m5-offline-evaluation-gate.md`](m5-offline-evaluation-gate.md) | 代码与离线验收完成；全合成 Mock，不代表真实准确率或灰度批准 |
| M5-B/B1 / 第七期首个切片 | [`m5-agent-observability-replay.md`](m5-agent-observability-replay.md) | 事件/版本合同与 Engine 级离线 Trace 已完成；B2 回放历史和 B3 比较未开始 |

阶段归档记录“当时为什么这样做、相对上一期增加了什么、当时验证到哪里”，不会被重写成当前实现。归档中的源码路径默认指向当前工作树，除非文档明确给出 tag/commit；M0、M1 没有可恢复的独立源码快照。

## 可复现性说明

当前目录已经初始化为 Git 仓库并关联 `https://github.com/KK25KK/log-agent`。M0、M1 仍没有单独的历史 tag 或源码快照，不能倒造；从当前基线开始，阶段交付应通过 commit/tag 或 PR 记录可复现版本。

SQLite 仍是技术预览存储，当前通过 `CREATE TABLE IF NOT EXISTS` 做增量建表，没有正式 schema version 和迁移/回滚工具。升级已有数据库前应备份；在生产存储迁移完成前，本地开发允许按阶段说明重建数据库。

`artifacts/` 是迁移前方案、Canvas、截图和中间产物，不是当前运行时规范，也不参与 Go 编译。

# Log Agent 文档索引

## 当前有效文档

| 文档 | 作用 | 维护规则 |
| --- | --- | --- |
| [`spec.md`](spec.md) | 唯一当前技术与行为契约 | 行为代码变更前先更新 |
| [`roadmap.md`](roadmap.md) | 当前阶段边界、完成状态和后续计划 | 只写可独立验收的阶段 |
| [`../README.md`](../README.md) | 运行、配置和项目入口 | 必须与当前代码一致 |

## 历史阶段归档

| 阶段 | 文档 | 状态 |
| --- | --- | --- |
| M0 / 第一期 | [`m0-implementation-archive.md`](m0-implementation-archive.md) | 历史叙事归档；没有独立源码快照 |
| M1 / 第二期 | [`m1-readonly-query-foundation.md`](m1-readonly-query-foundation.md) | 历史叙事归档；没有独立源码快照 |
| M2 / 第三期 | [`m2-error-spike-investigation-loop.md`](m2-error-spike-investigation-loop.md) | 历史叙事归档；当前树仍可对照其主体实现 |
| M3 / 第四期 | [`m3-change-correlation-evidence.md`](m3-change-correlation-evidence.md) | 代码与离线验收完成；真实系统联调待部署阶段 |

阶段归档记录“当时为什么这样做、相对上一期增加了什么、当时验证到哪里”，不会被重写成当前实现。归档中的源码路径默认指向当前工作树，除非文档明确给出 tag/commit；M0、M1 没有可恢复的独立源码快照。

## 可复现性说明

当前目录尚未初始化为 Git 仓库，因此不存在可验证的 M0/M1 历史 commit 或 tag，不能倒造；M3 文档同样不能声称对应某个可复现 commit。进入 M4 或真实试点前应先建立 Git 基线，并从此对完成阶段打 tag。

SQLite 仍是技术预览存储，当前通过 `CREATE TABLE IF NOT EXISTS` 做增量建表，没有正式 schema version 和迁移/回滚工具。升级已有数据库前应备份；在生产存储迁移完成前，本地开发允许按阶段说明重建数据库。

`artifacts/` 是迁移前方案、Canvas、截图和中间产物，不是当前运行时规范，也不参与 Go 编译。

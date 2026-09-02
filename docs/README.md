# Log Agent 文档索引

## 当前有效文档

| 文档 | 作用 | 维护规则 |
| --- | --- | --- |
| [`spec.md`](spec.md) | 唯一当前技术与行为契约 | 行为代码变更前先更新 |
| [`natural-language-rca-and-code-evidence-spec.md`](natural-language-rca-and-code-evidence-spec.md) | 自然语言、Trace、部署版本、代码证据与联合 RCA 的完整演进规格；当前仅自然语言接单切片落地 | 每项能力实现前保持合同先行，不能把未实现项写成当前能力 |
| [`governed-natural-language-intake.md`](governed-natural-language-intake.md) | 自然语言问题解析、确认预览、方舟 Intent 适配器、额度和 SQLite 迁移的当前实现与边界 | Parser、状态、配置、入口或真实 Smoke 变化时同步更新 |
| [`traceid-multi-logstore-timeline.md`](traceid-multi-logstore-timeline.md) | DAM TraceID 8 Logstore 资源组、查询预算、脱敏时间线、Checkpoint、Mock 验收和真实命令 | 资源成员、字段、预算、状态、展示或真实验收变化时同步更新 |
| [`roadmap.md`](roadmap.md) | 当前阶段边界、完成状态和后续计划 | 只写可独立验收的阶段 |
| [`development-process.md`](development-process.md) | 从 Mock-first 到 DAM 真实 SLS + 方舟联合验收的开发过程、关键决策、问题闭环和取舍 | 新阶段或真实验收完成后追加；不替代规范与路线图 |
| [`local-mock-e2e.md`](local-mock-e2e.md) | 飞书、SLS、指标/Trace、受治理 SOP 与摘要 Mock 主链的运行、预期输出与边界 | Mock 行为变化或复跑验收时同步更新 |
| [`m4-recoverable-query-steps.md`](m4-recoverable-query-steps.md) | 第五期 M4-A 的检查点、结果未知与恢复合同 | M4-A 行为或验收变化时同步更新 |
| [`m3b-cross-signal-incident-timeline.md`](m3b-cross-signal-incident-timeline.md) | M3-B 的 Mock-first 指标/Trace 聚合时间线、信任边界与真实接入前置条件 | 信号 Schema、阈值、状态、接线或验收变化时同步更新 |
| [`governed-sop-knowledge-guidance.md`](governed-sop-knowledge-guidance.md) | 受治理 SOP 人工核查指引的实现合同、开发记录、Mock E2E/评测兼容实测与真实接入前置条件；当前仅 Mock | SOP Schema、状态、安全边界、接线、实测结果或真实内容治理变化时同步更新 |
| [`m4b-reliability-governance.md`](m4b-reliability-governance.md) | M4-B 的投递失败分类、死信安全重放、租户额度/成本熔断和审批合同 | M4-B 状态机、额度或运维 CLI 变化时同步更新 |
| [`m5-offline-evaluation-gate.md`](m5-offline-evaluation-gate.md) | 第六期 M5-A 的全合成离线评测数据、指标、门禁与声明边界 | 数据集、评测规则或 CLI 行为变化时同步更新 |
| [`m5-agent-observability-replay.md`](m5-agent-observability-replay.md) | 第七至九期 M5-B 的 Agent 自观测、离线回放和兼容快照比较合同；B1～B3 已完成 | Agent 事件、版本清单、Trace 门禁或回放/比较边界变化时同步更新 |
| [`offline-feedback-and-rollout-rehearsal.md`](offline-feedback-and-rollout-rehearsal.md) | 严格 Mock Reviewer 反馈账本、纠正链和非行动性灰度/回滚演练 | Feedback Schema、策略、quorum、CLI 或安全边界变化时同步更新 |
| [`llm-evidence-summary.md`](llm-evidence-summary.md) | 必需 LLM 摘要的输入投影、引用门禁、Mock/火山方舟适配器、`llm-check/llm-smoke`、降级和真实接入步骤 | 摘要 Schema、Provider、Prompt、真实验收或安全边界变化时同步更新 |
| [`llm-summary-evaluation-gate.md`](llm-summary-evaluation-gate.md) | LLM 摘要的合成安全场景、独立门禁与原评测/回放兼容边界 | 摘要评测数据集、指标、门禁或 CLI 变化时同步更新 |
| [`llm-summary-quota.md`](llm-summary-quota.md) | LLM 摘要调用前的租户请求/Token 预留、结算、熔断与 Mock 验收 | 摘要额度状态机、配置、SQLite Schema 或生产边界变化时同步更新 |
| [`m6-real-system-entry-guide.md`](m6-real-system-entry-guide.md) | 真实系统接入地图：SLS/飞书/存储/变更源/指标 Trace/企业 SOP 各真实入口与组装点 | 生产化接入前保持与启动组装、接口边界和内容治理一致 |
| [`sls-cli-sts-migration.md`](sls-cli-sts-migration.md) | 阿里云 SLS 从 Go SDK 迁移到本机 CLI + STS Profile 的影响、安全合同和操作步骤 | CLI/Profile/查询协议或真实接入方式变化时同步更新 |
| [`dam-single-logstore-pilot.md`](dam-single-logstore-pilot.md) | DAM 主 Logstore `error_count_v1` 轻量试点、真实连接证据、Mock 下游边界和验收步骤 | DAM 试点字段、模板、配置、实测结果或范围变化时同步更新 |
| [`error-count-v1-implementation.md`](error-count-v1-implementation.md) | 计数型模板的逐层实现合同、兼容边界、Mock/真实验收命令与开发记录 | 模板合同、调用预算、下游降级或验收结果变化时同步更新 |
| [`current-capabilities-and-real-integration-inventory.md`](current-capabilities-and-real-integration-inventory.md) | 当前功能、Mock 边界、真实系统职责、接入步骤和预期效果的统一盘点 | 阶段完成状态、外部适配器或生产接入方式变化时同步更新 |
| [`oss-sre-agent-study-and-lightweight-adoption.md`](oss-sre-agent-study-and-lightweight-adoption.md) | OpenSRE、HolmesGPT、rca-agent、K8sGPT、RunLore 对比，以及本项目轻量借鉴决策 | 外部项目结论、当前能力边界或轻量演进决策变化时同步更新 |
| [`../README.md`](../README.md) | 运行、配置和项目入口 | 必须与当前代码一致 |

## 历史阶段归档

| 阶段 | 文档 | 状态 |
| --- | --- | --- |
| M0 / 第一期 | [`m0-implementation-archive.md`](m0-implementation-archive.md) | 历史叙事归档；没有独立源码快照 |
| M1 / 第二期 | [`m1-readonly-query-foundation.md`](m1-readonly-query-foundation.md) | 历史叙事归档；没有独立源码快照 |
| M2 / 第三期 | [`m2-error-spike-investigation-loop.md`](m2-error-spike-investigation-loop.md) | 历史叙事归档；当前树仍可对照其主体实现 |
| M3 / 第四期 | [`m3-change-correlation-evidence.md`](m3-change-correlation-evidence.md) | 代码与离线验收完成；真实系统联调待部署阶段 |
| M3-B / 跨信号增强切片 | [`m3b-cross-signal-incident-timeline.md`](m3b-cross-signal-incident-timeline.md) | 指标/Trace 聚合 Mock 时间线代码与离线验收完成；真实可观测平台未接入 |
| M4-A / 第五期首个切片 | [`m4-recoverable-query-steps.md`](m4-recoverable-query-steps.md) | 代码与离线验收完成；不代表完整 M4 或生产验收 |
| M4-B / 第十一期可靠性切片 | [`m4b-reliability-governance.md`](m4b-reliability-governance.md) | 代码与离线验收完成；SQLite 技术预览，不代表多实例生产治理 |
| M5-A / 第六期首个切片 | [`m5-offline-evaluation-gate.md`](m5-offline-evaluation-gate.md) | 代码与离线验收完成；全合成 Mock，不代表真实准确率或灰度批准 |
| M5-B/B1 / 第七期首个切片 | [`m5-agent-observability-replay.md`](m5-agent-observability-replay.md) | 事件/版本合同与 Engine 级离线 Trace 已完成 |
| M5-B/B2 / 第八期首个切片 | [`m5-agent-observability-replay.md`](m5-agent-observability-replay.md) | append-only 快照与当前二进制离线回放已完成 |
| M5-B/B3 / 第九期首个切片 | [`m5-agent-observability-replay.md`](m5-agent-observability-replay.md) | 兼容快照比较、回归/恢复识别和 `INCOMPARABLE` 门禁已完成；仍为全合成 Mock |
| M5-C/C1-C2 / 第十期离线切片 | [`offline-feedback-and-rollout-rehearsal.md`](offline-feedback-and-rollout-rehearsal.md) | 两名虚拟 Reviewer、append-only 反馈和非行动性灰度演练已完成；不代表真实灰度批准 |
| 必需 LLM 摘要切片 | [`llm-evidence-summary.md`](llm-evidence-summary.md) | 默认 Mock 主链、严格引用门禁、火山方舟适配器、独立真实 Smoke 与 DAM count-only Worker 联合 E2E 已完成；真实样本质量、成本/留存审批和真实飞书 E2E 单独记录 |
| LLM 摘要安全评测切片 | [`llm-summary-evaluation-gate.md`](llm-summary-evaluation-gate.md) | 9 类全合成安全场景、生产摘要链和独立门禁已完成；不代表真实模型质量 |
| LLM 摘要额度治理切片 | [`llm-summary-quota.md`](llm-summary-quota.md) | 每租户请求/Token 账本与成本熔断已完成离线验收；SQLite 不是生产全局配额 |

阶段归档记录“当时为什么这样做、相对上一期增加了什么、当时验证到哪里”，不会被重写成当前实现。归档中的源码路径默认指向当前工作树，除非文档明确给出 tag/commit；M0、M1 没有可恢复的独立源码快照。

## 可复现性说明

当前目录已经初始化为 Git 仓库并关联 `https://github.com/KK25KK/log-agent`。M0、M1 仍没有单独的历史 tag 或源码快照，不能倒造；从当前基线开始，阶段交付应通过 commit/tag 或 PR 记录可复现版本。

SQLite 仍是技术预览存储。当前事务迁移版本为 `PRAGMA user_version=2`，能够把既有低版本数据库幂等升级到当前表结构，并拒绝未知的更高版本；但尚无独立迁移 CLI、降级工具、生产备份恢复和多实例数据库验收。升级已有数据库前仍应备份。

`artifacts/` 是迁移前方案、Canvas、截图和中间产物，不是当前运行时规范，也不参与 Go 编译。

# ADR-0001：将 Eino 作为编排适配器

- 状态：Accepted
- 日期：2026-08-18

## 决策

使用 Eino v0.9.14 的 `compose.Graph` 编排调查步骤，但不让 Eino 类型进入应用层、领域层、存储层或入口层。

应用层只依赖自己定义的 `InvestigationEngine`。`internal/adapters/eino` 是唯一允许导入 Eino 的包，并由架构测试持续检查该约束。

## 原因

- 复用成熟的 Graph、节点组合、流式和未来模型/工具集成能力。
- 当前 M0 是严格线性流程，`compose.Graph` 足够简单。
- 幂等、状态机、证据、权限和审计是本系统的核心资产，不适合交给编排框架拥有。
- 如果未来更换 Eino，改动应集中在一个适配器，而不是扩散到整个工程。

## 当前限制

- M0 不使用 ChatModel、ReAct、ADK、Tool Calling 或框架 Checkpoint。
- 当前窗口与基线窗口在一个节点中顺序执行；需要并行分支时再评估 Eino Workflow。
- 框架 Checkpoint 即使后续启用，也不能替代数据库中的业务状态和证据记录。

# Log Agent 校招 Agent 开发面试材料实施计划

> 生成时间：2026-09-02
> 基于规格：[_spec.md](./_spec.md)
> 源码基线：`e115d4dcd7993b1c25e0001be951dad2c2cc1f1c`
> 状态：已完成

**目标：** 在 `interview-kit/` 中交付架构、亮点难点、项目介绍、简历条目和逐条面试脚本，面向校招 Agent 开发岗位，内容可直接用于学习、简历选择和口述。

**组织方式：** 用户明确要求多文件。正文按“先建立项目地图，再提炼亮点，随后压缩成介绍和简历，最后逐条展开面试脚本”的依赖顺序完成。

**技术形式：** Markdown + Mermaid + 锚定源码 commit 的 `file:///` 链接。只修改面试材料，不修改运行时代码。

---

## 1. 全局约定

1. 所有实现事实以源码、测试和真实验收记录交叉证明，README 不能单独作为实现证据。
2. Mock、独立 Smoke、真实联合 E2E、生产可用四种状态必须明确区分。
3. 第一人称只用于使用者确认真实参与、能解释和能复现的内容；每条简历提供“适用前提/不能说”。
4. 源码链接格式为 `[文件名](file:///D:/日志agent/<path>#Lstart-Lend)`，行号锚定源码 commit `e115d4d...`。
5. 架构和亮点文档允许源码定位；项目介绍、简历标题和面试口述稿不贴代码片段。
6. 每份正文都先写业务问题，再讲设计和技术，最后落到验证效果与边界。
7. 不虚构用户规模、准确率、节省成本、线上故障恢复次数、生产 QPS 或个人独立贡献。
8. 每个 Task 完成“写作 → 自查 → 独立 commit”后再进入下一个 Task。
9. commit 统一使用 `docs(interview): <内容>`。
10. 用户已要求整体验收完成后推送当前分支到远端。

## 2. 已锁定的证据范围

| 主题 | 主要源码/测试证据 |
| --- | --- |
| 启动与组装 | `cmd/logagent/main.go`、`cmd/logagent/web.go` |
| 领域合同 | `internal/domain/types.go`、`query.go`、`summary.go`、`reliability.go` |
| 应用主链 | `internal/application/intake.go`、`worker.go`、`delivery.go` |
| Eino 编排 | `internal/adapters/eino/engine.go`、架构边界测试 |
| 查询治理 | `internal/application/query/gateway.go`、`gateway_test.go` |
| 恢复与成本边界 | `checkpoint_executor.go`、恢复集成测试、SQLite QueryStep/Quota 测试 |
| SLS 真实适配 | `internal/adapters/aliyuncli`、`cmd/logagent/sls.go` |
| LLM 摘要 | `internal/application/summary.go`、`internal/adapters/volcark`、摘要/额度测试 |
| Web 与飞书 | `internal/adapters/localweb`、`internal/adapters/feishu`、`web_test.go` |
| 评测回放 | `internal/evaluation`、`cmd/logagent/evaluate.go`、回放/比较测试 |
| 真实验收 | `docs/local-web-pilot-console.md`、`docs/llm-evidence-summary.md`、`docs/development-process.md` |

---

## Task 1：项目架构与架构图

**文件：** 创建 `interview-kit/01-project-architecture.md`（300～500 行）

- [x] 读取 `rhythm-checklist.md`、`audience-guide.md`、`source-linking.md`。
- [x] 写一句话业务目标、技术栈、分层职责和依赖方向。
- [x] 绘制整体分层图、调查序列图、状态/恢复图，单图不超过 15 个节点。
- [x] 解释 Intake → Store → Worker/Eino → Gateway → SLS → Evidence → Summary → Delivery。
- [x] 标出 Mock、真实和待接入模块，并给出推荐源码阅读顺序。
- [x] 提供至少 12 个有效 `file:///` 源码链接和 4 个 📦 知识框。
- [x] 自查：行数 300～500、Mermaid ≥3、源码链接 ≥12、文末恰好一个“一句话记住”。
- [x] Commit：`docs(interview): add log agent architecture guide`

## Task 2：项目亮点与难点

**文件：** 创建 `interview-kit/02-highlights-and-challenges.md`（350～600 行）

- [x] 至少选择 7 个高含金量主题，覆盖 Agent、Go、可靠性、LLM 和评测。
- [x] 每项完整回答：问题、难点、方案、实现、替代方案、优势、代价、效果、后续优化。
- [x] 对比开放式 ReAct、SDK 直连、自动重试、原始日志喂模型、只测 happy path 等备选方案。
- [x] 每项给出仓库证据和面试追问深度，避免只罗列技术名。
- [x] 提供至少 12 个源码/测试链接和 4 个 📦 知识框。
- [x] 自查：所有主题都有“为什么不选别的方案”和“效果边界”；Mock/真实表述一致。
- [x] Commit：`docs(interview): add highlights and challenges`

## Task 3：项目介绍口述稿

**文件：** 创建 `interview-kit/03-project-introduction-script.md`（180～320 行）

- [x] 写一句话、30 秒、3 分钟、5 分钟四档脚本。
- [x] 固定叙事：业务痛点 → 核心设计 → 调查主链 → 难点 → 真实验收 → 当前边界。
- [x] 用“授权办案的值班工程师”“证据卷宗”“快递单号”等类比解释抽象概念。
- [x] 输出为可直接朗读的连续中文，不写模块清单或源码讲解。
- [x] 自查：四档信息一致、没有虚构业务指标、5 分钟稿能自然回答“项目做什么/为什么这样做/效果如何”。
- [x] Commit：`docs(interview): add project introduction scripts`

## Task 4：简历候选条目

**文件：** 创建 `interview-kit/04-resume-bullets.md`（300～500 行）

- [x] 生成 7 条候选，覆盖 Agent 编排、查询治理、可靠恢复、Evidence/反证、LLM、评测和真实联合 E2E。
- [x] 每条标题尽量不超过 30 个中文字符，并给出推荐等级和适合强调的岗位能力。
- [x] 每条补充业务背景、实现机制、方案取舍、可验证效果、技术水平、适用前提、可以说/不能说。
- [x] 给出校招简历最终组合建议：Agent 主线版、Go 工程版、可靠性版各 2～3 条。
- [x] 自查：标题长度脚本检查；每条至少包含做了什么、用了什么、效果是什么三项中的两项。
- [x] Commit：`docs(interview): add resume bullet candidates`

## Task 5：逐条面试脚本

**文件：** 创建 `interview-kit/05-interview-scripts-by-resume-bullet.md`（500～900 行）

- [x] 与 Task 4 的 7 个编号严格一一对应。
- [x] 每条写 20～30 秒开场和 2～3 分钟完整口述稿。
- [x] 每条按“问题—方案—效果—取舍”闭环，语言生动、逻辑连贯。
- [x] 每条至少准备 3 个追问；全文追问不少于 21 个。
- [x] 每条增加边界防守：Mock/真实、已实现/计划、个人贡献、生产可用性。
- [x] 不出现具体代码、函数逐行解释或无法直接朗读的表格堆砌。
- [x] 自查：7 个编号完整、追问 ≥21、代码围栏为 0、所有数字有证据或明确说明是样本。
- [x] Commit：`docs(interview): add resume-aligned interview scripts`

## Task 6：使用入口与速查

**文件：** 创建 `interview-kit/README.md`（60～100 行）

- [x] 说明五份材料的使用场景和推荐顺序。
- [x] 给出“面试前 15 分钟”“准备 1 小时”“完整学习”三条阅读路线。
- [x] 提醒只选择与实际贡献一致的 2～3 条简历项。
- [x] 放置当前源码 commit、事实边界和所有文件链接。
- [x] 自查：链接全部存在，README 不复制正文。
- [x] Commit：`docs(interview): add interview kit index`

## Task 7：整体验收

**范围：** `interview-kit/*.md`

- [x] 读取 `self-review-grep.md` 和 `cross-review-checklist.md`。
- [x] 检查所有 Markdown 链接、源码绝对路径和行号抽样。
- [x] 检查 Mermaid 代码块成对闭合、图节点数和中文命名。
- [x] 检查 7 条简历编号与 7 份脚本严格对应。
- [x] 检查至少 21 个追问、脚本无代码围栏、教学文档知识框和“一句话记住”达标。
- [x] 全局搜索并人工复核“生产、上线、准确率、节省、全部、自动修复”等高风险词。
- [x] 核对真实验收元数据、Mock 边界、未完成项与当前文档一致。
- [x] 运行 `git diff --check`；文档任务不修改 Go 代码，因此不以 Go 测试代替内容验收。
- [x] 更新 `_plan.md` 全部 checkbox 和状态为已完成。
- [x] Finalize commit：`docs(interview): finalize log agent interview kit (v0.1.0)`

---

## 3. 完成定义

只有满足以下条件才算整个材料完成：

- 用户要求的五类正文全部存在；
- 架构、亮点、介绍、简历和脚本之间事实一致；
- 每条简历都能在对应脚本中讲清实现与取舍；
- 所有源码/测试证据可定位；
- 没有把 Mock、独立 Smoke、本地联合 E2E 写成生产上线；
- 没有虚构个人贡献或量化业务结果；
- 所有 Task 已独立提交，最终交叉审查通过。

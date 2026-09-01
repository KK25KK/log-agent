# Log Agent 校招 Agent 开发面试材料实施计划

> 生成时间：2026-09-02
> 基于规格：[_spec.md](./_spec.md)
> 源码基线：`e115d4dcd7993b1c25e0001be951dad2c2cc1f1c`
> 状态：v0.1 已完成；v0.2 实施计划待用户批准

**目标：** 在 `interview-kit/` 中交付架构、亮点难点、项目介绍、简历条目和逐条面试脚本，并在 v0.2 增加 Agent 八股映射、调用链解析和 0→1 架构演进故事，面向校招 Agent 开发岗位，内容可直接用于学习、简历选择和口述。

**组织方式：** 用户明确要求多文件。v0.1 已按“项目地图 → 亮点 → 介绍 → 简历 → 面试脚本”完成；v0.2 按“通用知识横向映射 → 项目架构纵向演进 → 入口更新 → 全局复核”的顺序实施。

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

## 3. v0.2 实施任务

### Task 8：Agent 八股与项目映射

**文件：** 创建 `interview-kit/06-agent-interview-fundamentals.md`（650～900 行）

#### Step 1：写作前取证

- [x] 读取 `tutorial.md.tpl`、`rhythm-checklist.md`、`audience-guide.md` 和 `source-linking.md`。
- [x] 复核 Eino Graph、Worker、Query Gateway、Checkpoint、Summary、Evaluation 的源码与测试入口。
- [x] 复核 OpenAI、Anthropic、ReAct、Eino、RAG、OWASP、OpenTelemetry 和 AWS 幂等资料的当前官方页面或原论文。
- [x] 为 17 个主题建立“通用结论 → 项目实现/未采用项 → 证据文件 → 面试边界”取证表，正文不展示过程草稿。

#### Step 2：写正文

- [x] 先用类比和 Mermaid 图解释 Model、Tools、Instructions、State 与本项目模块的对应关系。
- [x] 逐题完成“原八股 → 通用标准答案 → 怎么对应本项目 → 面试官问法 → 项目标准答案 → 知识点 → 至少 2 个延伸追问”。
- [x] 明确区分固定 Graph 与开放式 ReAct、业务状态与通用 Memory、Evidence 查询与向量 RAG、单 Agent 与 Multi-Agent/MCP。
- [x] 解释为什么选择 Eino，但把权限、状态、可靠性、证据和成本治理保留在自研应用层。
- [x] 提供不少于 4 个 📦 额外知识框、12 个源码/测试定位、高频十题速背和文末恰好 1 个“一句话记住”。

#### Step 3：自查与提交

- [x] 检查行数 650～900、主题数 17、原八股/通用答案/项目答案各 17、延伸追问不少于 34。
- [x] 检查 `file:///` 不少于 12、外部权威链接不少于 12、Mermaid 不少于 1、📦 不少于 4、💡 恰好 1。
- [x] 抽样核对源码行号和外部结论，确认没有把 ReAct、RAG、Multi-Agent 或 MCP 写成已实现能力。
- [x] 全文搜索“生产、上线、准确率、节省、全部、自动修复”，逐条复核证据边界。
- [x] 运行 `git diff --check`。
- [x] Commit：`docs(interview): add agent fundamentals mapping`

### Task 9：调用链与 0→1 架构演进

**文件：** 创建 `interview-kit/07-zero-to-one-architecture-evolution.md`（500～800 行）

#### Step 1：写作前取证

- [x] 读取 `docs/development-process.md`、`docs/roadmap.md`、`docs/spec.md` 和 M0～M5 阶段文档，建立阶段事实表。
- [x] 用 Git 日志核对关键阶段提交，但以源码、测试和验收记录为实现证据，不把 commit 列表当正文。
- [x] 定位飞书/Web Intake、SQLite Store、Worker、Eino、Gateway、CLI SLS、Evidence、Summary、Delivery 和 Evaluation 的真实调用入口。
- [x] 单独整理真实联合样本、Mock E2E、离线评测和待验收项，防止不同证据层级混写。

#### Step 2：解析三条调用链

- [x] 绘制业务主链路 Mermaid：飞书/Web → Intake → SQLite → Worker/Eino → Gateway/CLI SLS → Evidence/Report → 方舟摘要 → Delivery。
- [x] 绘制查询治理链路 Mermaid：可信身份 → Catalog → ACL → 固定模板 → 预算/水位 → Schema → Audit → Provider。
- [x] 绘制失败恢复链路 Mermaid：幂等 → 租约/fencing → Checkpoint → `NEEDS_REVIEW` → 报告/投递解耦 → 重试/死信/人工确认。
- [x] 对每个节点解释输入、输出、职责归属、失败点、恢复方式和可验证证据。
- [x] 提供一个 60～90 秒全链路口述稿和一个从用户提交开始的 3 分钟故事稿。

#### Step 3：讲清架构选择

- [x] 逐项分析固定 Graph、分层架构、SQLite 状态机、受治理 Gateway、Evidence-first LLM、CLI + STS、单库 count-only 试点和本地 Web 入口。
- [x] 每项按“约束 → 可选方案 → 选择标准 → 当前方案 → 优势 → 代价 → 何时重选”展开。
- [x] 重点解释为什么没有直接采用开放式 ReAct、模型自由 SQL、应用长期 AK/SK、原始日志喂模型、一次覆盖 8 库或等待飞书权限。

#### Step 4：讲清 0→1 演进

- [x] 绘制 M0～M5、真实 LLM、真实 SLS、本地 Web 联合试点的架构演进时间线。
- [x] 绘制“前一阶段的限制如何触发下一层组件”的因果图，不把最终架构描述成一次性设计完成。
- [x] 每阶段回答：已有能力、暴露问题、新增组件、方案取舍、实现困难、验证层级、启示和遗留项。
- [x] 完整复盘至少 8 个困难，统一使用“现象 → 根因 → 被否决方向 → 最终方案 → 验证结果 → 启示 → 尚存限制”。
- [x] 用证据矩阵明确单测、Mock E2E、离线 Golden/Replay、真实单库联合样本分别能证明和不能证明什么。
- [x] 提供不少于 4 个 📦 额外知识框、12 个源码/测试/开发记录定位，文末恰好 1 个“一句话记住”。

#### Step 5：自查与提交

- [x] 检查行数 500～800、调用链图不少于 3、演进时间线不少于 1、因果图不少于 1、口述稿 2 份。
- [x] 检查架构决策不少于 8、完整困难复盘不少于 8、阶段覆盖 M0～M5 与三个真实接入节点。
- [x] 检查 `file:///` 不少于 12、📦 不少于 4、💡 恰好 1，所有链接可定位。
- [x] 检查每个“效果”都标注单测、Mock、离线评测或真实联合样本层级。
- [x] 检查飞书只称“功能已实现、真实平台验收待权限”，真实联合样本只称“单主 Logstore count-only”。
- [x] 运行 `git diff --check`。
- [x] Commit：`docs(interview): add zero-to-one architecture story`

### Task 10：更新使用入口

**文件：** 修改 `interview-kit/README.md`

- [ ] 把材料清单从 5 份扩展为 7 份，补充两份新文档的用途和选择建议。
- [ ] 更新“面试前 15 分钟”“准备 1 小时”“完整学习”路线，避免要求读者一次读完长文。
- [ ] 增加“先横向补八股，再纵向练项目故事”的 Agent 岗位专项路线。
- [ ] 检查 7 个相对链接均存在，README 不重复正文内容。
- [ ] 运行 `git diff --check`。
- [ ] Commit：`docs(interview): update interview kit reading paths`

### Task 11：v0.2 整体验收

**范围：** `interview-kit/*.md`

- [ ] 读取 `self-review-grep.md` 和 `cross-review-checklist.md`，逐条执行自查。
- [ ] 用脚本或 grep 统计 17 个主题、34 个延伸追问、8 个架构决策、8 个困难复盘、知识框、💡、Mermaid 和源码链接。
- [ ] 抽查至少 10 个 `file:///` 行号，检查源码 commit 基准下仍能定位到对应语义。
- [ ] 打开并抽查至少 12 个外部资料链接，确认标题、来源和正文结论匹配。
- [ ] 交叉检查 `01`～`07` 对 Eino、飞书、SLS、方舟、Mock、真实试点和生产边界的口径一致。
- [ ] 检查通用答案与项目答案不互相冒充，未采用技术均明确标成比较项或未来选项。
- [ ] 检查 60～90 秒、3 分钟和原有 5 分钟项目稿能够互相衔接，且事实没有漂移。
- [ ] 更新 `_plan.md` 的 v0.2 checkbox 和顶部状态为已完成。
- [ ] 运行 `git diff --check`；本阶段不改 Go 代码，不以 `go test` 代替文档验收。
- [ ] Finalize commit：`docs(interview): finalize agent interview extension (v0.2.0)`

---

## 4. 完成定义

只有满足以下条件才算整个材料完成：

- v0.1 的五类正文和 v0.2 的两份新增正文全部存在；
- 架构、亮点、介绍、简历和脚本之间事实一致；
- 每条简历都能在对应脚本中讲清实现与取舍；
- 17 个通用 Agent 主题都能先独立回答，再准确映射到项目；
- 三条调用链能逐站解释职责、故障和恢复；
- M0 到真实联合试点形成完整的“问题—选择—困难—验证—启示”演进故事；
- 所有源码/测试证据可定位；
- 没有把 Mock、独立 Smoke、本地联合 E2E 写成生产上线；
- 没有虚构个人贡献或量化业务结果；
- 所有 v0.2 Task 已独立提交，最终交叉审查通过。

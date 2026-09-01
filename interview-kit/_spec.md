# Log Agent 校招 Agent 开发面试材料规格

> 生成时间：2026-09-02
> 目标源码 commit：`e115d4dcd7993b1c25e0001be951dad2c2cc1f1c`
> 状态：v0.1 已完成；v0.2 Agent 八股映射扩展待用户批准

---

## 1. 目标

为 Log Agent 项目整理一套面向“校招 Agent 开发岗位”的中文面试材料。材料既要让第一次听项目的面试官快速理解业务和架构，也要支持继续追问查询治理、可靠性、LLM 安全、真实系统接入和工程取舍。

材料必须基于当前仓库代码、测试、Git 和真实验收记录，不把未实现能力、Mock 结果或计划项包装成已上线成果。

## 2. 受众定位

- **主要受众：校招 Agent 开发岗位面试官**。关注候选人是否理解 Agent 编排、工具调用边界、LLM 可靠性和工程落地，而不是只会调用模型 API。
- **次要受众：Go 后端面试官**。关注分层、接口设计、幂等、状态机、租约、持久化、审计和测试。
- **使用者：准备面试的项目讲述者**。需要能直接朗读的中文脚本、可选择的简历条目和明确的追问边界。

默认读者知道基本 Go 和 HTTP，但不预设其了解 Eino、SLS、Evidence、Checkpoint、结构化输出或 Agent 评测。

## 3. 事实与个人贡献边界

### 可以基于仓库和验收记录讲述

- Go 主体和 Eino 固定 Graph；
- Intake、SQLite 状态机、Worker 租约和持久化 Delivery；
- Resource Catalog、Principal ACL、Schema、预算、审计和固定查询模板；
- current/baseline、Evidence、Finding、Recommendation 和反证机制；
- 查询 Checkpoint、未知结果转人工复核和成本确认重跑；
- 火山方舟结构化摘要、引用校验、fallback 和 Token 额度；
- 合成评测、Agent Trace、快照、回放、比较和 Mock 灰度演练；
- 本地 Web 入口；
- DAM 单主 Logstore `error_count_v1` 真实 SLS + 方舟同调查联合 E2E。

### 必须明确限定

- 飞书接收事件、卡片动作、Reply/Patch 与可靠投递功能已经实现；真实平台权限、WebSocket、OpenID 和卡片交互验收待完成；
- DAM 真实试点只覆盖一个主 Logstore 和 count-only 模板，不是 8 个 Logstore 统一时间线；
- current/baseline 实时计数只是当时窗口样本，不是长期业务效果；
- 方舟联合 E2E 证明链路可用，不代表真实历史故障摘要质量达标；
- SQLite、单进程额度和租约仍是技术预览，不代表多实例生产能力；
- 指标/Trace、企业 SOP、Change Catalog 的真实 Provider 尚未接入；
- 系统没有自动修复或生产写操作。

### 第一人称使用规则

- 只对讲述者真正参与、能现场解释和复现的部分使用“我设计/我实现/我负责”。
- 无法确认个人独立完成时，使用“项目中实现了”“我重点研究并参与了”。
- 每个简历候选条目附“适用前提”和“不能说”，由使用者选择与真实经历一致的版本。

## 4. 产出目录

用户已明确要求多文件，因此采用独立文件夹 `interview-kit/`：

| 文件 | 作用 | 目标规模 |
| --- | --- | ---: |
| `_spec.md` | 受众、范围、事实边界和验收标准 | 160～220 行 |
| `_plan.md` | 分步实施和逐文件验收计划 | 80～180 行 |
| `README.md` | 使用入口、推荐阅读顺序和面试前速查 | 60～100 行 |
| `01-project-architecture.md` | 业务背景、技术栈、分层架构、组件图、主链路和源码定位 | 300～500 行 |
| `02-highlights-and-challenges.md` | 亮点与难点；逐项回答为什么、如何设计、替代方案、优势、效果和边界 | 350～600 行 |
| `03-project-introduction-script.md` | 一句话、30 秒、3 分钟和 5 分钟项目介绍口述稿 | 180～320 行 |
| `04-resume-bullets.md` | 6～8 条不超过约 30 字的简历候选条目，以及逐条详细拆解 | 300～500 行 |
| `05-interview-scripts-by-resume-bullet.md` | 对应每条简历候选项的完整口述稿、追问与防守边界 | 500～900 行 |
| `06-agent-interview-fundamentals.md` | 通用 Agent 八股、项目映射、标准回答、知识点和递进追问 | 650～900 行 |

## 5. 内容要求

### 5.1 架构文档

至少包含：

- 一句话业务目标；
- 技术栈速览；
- 一张整体分层架构 Mermaid 图；
- 一张典型调查序列图；
- 一张状态机或可靠性流程图；
- 模块职责、依赖方向和核心入口；
- 业务动作到源码文件/关键类型的映射；
- Mock、真实、待接入范围标记；
- 推荐阅读顺序。

### 5.2 亮点与难点文档

每项必须按以下闭环展开：

```text
问题是什么
  -> 为什么难
  -> 采用什么方案
  -> 关键实现
  -> 为什么不选其他方案
  -> 优势与代价
  -> 实际验证效果
  -> 还能怎么继续优化
```

候选重点包括：

1. Eino 只做编排，核心治理框架无关；
2. Evidence 驱动的确定性结论与 LLM 解耦；
3. 固定查询模板、ACL、Schema、预算和审计；
4. Checkpoint + lease fencing + unknown outcome；
5. 证据引用、支持测试和反证测试；
6. 方舟结构化输出、双重校验、fallback 和 Token 额度；
7. 合成评测、Agent Trace、快照和回放比较；
8. 飞书真实平台权限受阻时，用本地 Web 完成其余应用主链的真实联合 E2E；
9. DAM 字段不满足时收缩为 `error_count_v1`，而不是做重型跨库全文 Agent。

### 5.3 项目介绍脚本

- 必须是可直接朗读的连贯中文，不写成代码清单或模块罗列；
- 顺序固定为：业务问题 → 核心设计 → 主链路 → 代表性难点 → 真实验证 → 当前边界；
- 提供一句话、30 秒、3 分钟和 5 分钟四档；
- 用生活化类比解释 Evidence、Checkpoint 和 LLM 摘要；
- 不出现无法证实的用户量、准确率、节省成本或线上收益。

### 5.4 简历条目

- 生成 6～8 条候选项，使用者最终选择 2～3 条；
- 每条标题尽量不超过 30 个中文字符；
- 一句话必须包含“做了什么 + 关键技术/机制 + 可验证效果”中的至少两项，详细说明补齐第三项；
- 每条附：适用前提、业务背景、技术实现、方案取舍、证据、面试价值、可以说和不能说；
- 覆盖 Agent、Go 后端、可靠性、LLM 安全和评测多个方向，避免所有条目表达同一件事。

### 5.5 对应面试脚本

- 与 `04-resume-bullets.md` 的编号一一对应；
- 不贴具体代码，不逐行解释函数；
- 每条包含 20～30 秒开场、2～3 分钟完整回答、常见追问和边界防守；
- 语言直白、生动、逻辑连贯；
- 每条都能闭环“问题—方案—效果—取舍”；
- 不把 `Mock`、独立 Smoke 或本地 E2E 说成生产上线。

## 6. 教学与表达节奏

材料整体采用以下节奏：

1. **类比开场**：日志 Agent 像一名只按授权清单办案的值班工程师；
2. **一步步走**：从请求到 Evidence、报告、摘要和投递；
3. **源码定位**：只在架构与亮点材料中给出源码链接；
4. **📦 额外知识**：解释 Eino、STS、fencing token、结构化输出、回放等背景；
5. **💡 一句话记住**：每份教学型文档末尾给出一个核心记忆点。

口述脚本不强制显示这些标签，但内部逻辑仍遵循同样节奏。

## 7. 源码与证据链接

- 源码链接锚定 commit `e115d4dcd7993b1c25e0001be951dad2c2cc1f1c`。
- 格式使用 `[文件名](file:///D:/日志agent/绝对路径#Lstart-Lend)`。
- 架构和亮点文档至少提供 12 个源码定位；脚本文档不放具体代码。
- Git、测试和真实验收结论必须能追溯到仓库文档或当前 commit。
- 行号可能随未来修改漂移，材料末尾同时提供“文件名 + 类型/函数名”定位。

## 8. 校招表达策略

- 先讲清业务问题，再讲框架和技术名；
- 强调为什么要限制 Agent，而不是追求“越自主越智能”；
- 深度来自边界、失败处理和取舍，不靠堆组件名；
- 面试官追问时优先讲一个闭环案例，不一次展开所有阶段；
- 对未完成项主动说明下一步方案，体现工程判断而不是回避。

## 9. 验收标准

- [x] 目录内包含用户要求的五类正文材料和使用入口；
- [x] 所有架构图 Mermaid 语法闭合、节点不超过 15 个；
- [x] 架构模块与当前源码目录一致；
- [x] 每个亮点/难点都包含替代方案和取舍；
- [x] 项目介绍稿可直接朗读，没有流水账和术语堆砌；
- [x] 简历标题逐条检查长度，且不虚构量化效果；
- [x] 简历条目与详细面试脚本编号完全对应；
- [x] 脚本文档不出现具体代码片段；
- [x] 至少列出 15 个常见追问，并有基于项目事实的回答；
- [x] 所有本地源码链接目标存在；
- [x] Mock、真实 Smoke、真实联合 E2E 和待实现项标记一致；
- [x] 不包含凭据、原始日志、内部敏感查询或 Provider 原始错误；
- [x] Markdown 格式和仓库链接检查通过；
- [x] 最终 Git 工作树只包含本任务确认范围内的文件。

## 10. 额外写作约束

- 默认简体中文。
- 岗位定位为校招 Agent 开发。
- 语言直白、逻辑通畅、表达生动。
- 项目介绍和面试脚本必须可直接朗读。
- 面试脚本不出现具体代码。
- 不能虚构个人贡献、生产上线、业务指标、模型准确率或成本收益。
- 重点体现 Go 工程能力、Agent 治理、真实系统接入、可靠性和评测意识。
- 文件组织由本规格确定；本次采用多文件模式。

## 11. v0.2：Agent 八股与项目映射扩展

### 11.1 目标

新增 `06-agent-interview-fundamentals.md`，解决两个问题：

1. 面试者只会讲项目，但回答不了通用 Agent 原理；
2. 背得出通用八股，却不能解释它与当前 Log Agent 的具体关系。

每个主题必须先给出不依赖本项目的“原八股问法”和通用标准答案，
随后再映射到 Log Agent 的真实设计、面试官项目化问法、项目标准答案、核心知识点和延伸追问。

“原八股”指业界通行的基础问题及基于权威资料重新组织的答案，
不逐字复制培训题库或大段引用网页内容。

### 11.2 主题范围

正文覆盖以下 17 个主题：

1. 什么是 LLM Agent，与普通 Chatbot 有什么区别；
2. Agent 的 Model、Tools、Instructions、State/Memory 分别负责什么；
3. Workflow 与自主 Agent 有什么区别，如何选择；
4. ReAct 的 Thought/Action/Observation 循环是什么；
5. 固定 Graph、Plan-and-Execute 与开放式 ReAct 如何取舍；
6. Tool/Function Calling 的完整执行链与安全边界；
7. 为什么使用 Eino，框架和自研代码如何划界；
8. 短期记忆、长期记忆、业务状态和 Checkpoint 有什么区别；
9. RAG、工具查询和 Evidence-grounded Generation 有什么区别；
10. Agent 幻觉从哪里来，Grounding 怎样降低风险；
11. JSON Mode、Structured Output 与应用层语义校验有什么区别；
12. Guardrails、最小权限和 Prompt Injection 怎样治理；
13. Human-in-the-Loop 在什么时候中断、审批和恢复；
14. 单 Agent 与多 Agent 如何选择，为什么本项目不做多 Agent；
15. Context Engineering、Token 预算和成本治理；
16. Agent 评测应怎样设计 Golden、反证、Replay 和线上反馈；
17. Agent 可观测性、幂等、重试和外部结果未知怎样处理。

如果一个主题无法与当前仓库真实实现或明确取舍建立联系，
正文应将其写成对比边界，而不是虚构“项目已经使用”。

### 11.3 单题固定结构

每个主题严格按以下顺序组织：

1. **原八股**：面试中常见的通用问法；
2. **通用标准答案**：脱离项目也成立的 30～60 秒答案；
3. **怎么对应本项目**：指出当前实现、主动取舍或未采用项；
4. **面试官会怎么问**：改写成围绕 Log Agent 的真实追问；
5. **项目标准答案**：可直接口述，按问题—方案—效果—取舍闭环；
6. **对应知识点**：列出 3～6 个关键词，并解释它们之间的关系；
7. **延伸追问**：至少 2 个递进问题，给出答案要点和边界。

正文先展示通用答案，再做项目映射，不能反过来只讲项目实现。

### 11.4 资料来源

外部知识只采用论文、框架官方文档和权威工程资料：

- [OpenAI：A practical guide to building agents](https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/)
- [Anthropic：Building effective agents](https://www.anthropic.com/research/building-effective-agents)
- [ReAct 原论文](https://arxiv.org/abs/2210.03629)
- [Eino 官方：Graph 与 Agent 选择](https://www.cloudwego.io/docs/eino/overview/graph_or_agent/)
- [Eino 官方：Chain/Graph/Workflow](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/)
- [Eino 官方：Memory 与 Session](https://www.cloudwego.io/docs/eino/quick_start/chapter_03_memory_and_session/)
- [Eino 官方：Human-in-the-Loop](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_hitl/)
- [Eino 官方：Callback 与 Trace](https://www.cloudwego.io/docs/eino/quick_start/chapter_06_callback_and_trace/)
- [RAG 原论文](https://arxiv.org/abs/2005.11401)
- [OpenAI：Structured Outputs](https://openai.com/index/introducing-structured-outputs-in-the-api/)
- [OpenAI：Evals API](https://platform.openai.com/docs/api-reference/evals)
- [OWASP LLM01:2025 Prompt Injection](https://owasp.org/www-project-top-10-for-large-language-model-applications/assets/PDF/OWASP-Top-10-for-LLMs-v2025.pdf)
- [OpenTelemetry GenAI 语义约定](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/)
- [AWS Builders' Library：Making retries safe with idempotent APIs](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/)

正文以概括和推导为主，不长篇引用来源；
涉及框架当前能力时标注官方链接和检索日期 `2026-09-02`。

### 11.5 项目映射边界

- 可以把 Log Agent 定义为“带 Agent 特征的受治理 Workflow/固定图 Agent”，并说明业界对 Agent 定义存在宽窄口径；
- 不声称当前使用开放式 ReAct；项目使用的是 Eino 固定 Graph；
- 不声称当前实现通用对话 Memory；项目持久化的是调查业务状态、租约和 QueryStep Checkpoint；
- 不声称当前使用向量 RAG；Evidence 来自受治理 SLS 聚合查询，Runbook 真实来源待接入；
- 不声称 Structured Output 能消除事实错误；项目还执行 Evidence ID、建议代码和敏感内容校验；
- 不声称使用 Multi-Agent 或 MCP；可以解释当前为何不需要；
- 飞书功能表述继续使用“功能已实现，真实平台验收待权限”；
- 真实 SLS + 方舟仅代表单主 Logstore、count-only 的本地联合样本；
- 不虚构 Agent 准确率、线上流量、生产收益或个人独立贡献。

### 11.6 教学结构与规模

- 正文目标 650～900 行；
- 开头先画一张“通用 Agent 知识 → 本项目模块”的 Mermaid 映射图；
- 至少 17 个原八股问题、17 个通用答案、17 个项目答案；
- 每题至少 2 个延伸追问，全文不少于 34 个；
- 至少 12 个项目源码/测试定位，至少 4 个额外知识框；
- 文末提供“高频十题速背”和恰好一个“一句话记住”。

### 11.7 v0.2 验收标准

- [ ] 所有 17 个主题均先呈现原八股，再呈现项目映射；
- [ ] 通用标准答案不依赖 Log Agent 也能成立；
- [ ] 项目标准答案与 v0.1 架构、亮点和真实验收边界一致；
- [ ] 至少 34 个延伸追问，并从概念逐步追到取舍与故障场景；
- [ ] 外部结论均可追溯到本节列出的权威来源；
- [ ] 项目映射至少提供 12 个有效 `file:///` 源码或测试链接；
- [ ] 未使用的 ReAct、RAG、Multi-Agent、MCP 不被包装成已实现能力；
- [ ] README 增加新章节入口和对应阅读路线；
- [ ] 行数、知识框、Mermaid、源码链接和文末速记满足阈值；
- [ ] 全局链接、事实边界、术语和 Git 工作树交叉审查通过。

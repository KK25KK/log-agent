# Log Agent 校招 Agent 开发面试材料规格

> 生成时间：2026-09-02
> 目标源码 commit：`e115d4dcd7993b1c25e0001be951dad2c2cc1f1c`
> 状态：v0.1 已完成；v0.2 Agent 八股映射与 0→1 架构演进扩展待用户批准

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
| `_spec.md` | 受众、范围、事实边界和验收标准 | 280～380 行 |
| `_plan.md` | v0.1 历史任务与 v0.2 分步实施、逐文件验收计划 | 220～280 行 |
| `README.md` | 使用入口、推荐阅读顺序和面试前速查 | 60～100 行 |
| `01-project-architecture.md` | 业务背景、技术栈、分层架构、组件图、主链路和源码定位 | 300～500 行 |
| `02-highlights-and-challenges.md` | 亮点与难点；逐项回答为什么、如何设计、替代方案、优势、效果和边界 | 350～600 行 |
| `03-project-introduction-script.md` | 一句话、30 秒、3 分钟和 5 分钟项目介绍口述稿 | 180～320 行 |
| `04-resume-bullets.md` | 6～8 条不超过约 30 字的简历候选条目，以及逐条详细拆解 | 300～500 行 |
| `05-interview-scripts-by-resume-bullet.md` | 对应每条简历候选项的完整口述稿、追问与防守边界 | 500～900 行 |
| `06-agent-interview-fundamentals.md` | 通用 Agent 八股、项目映射、标准回答、知识点和递进追问 | 650～900 行 |
| `07-zero-to-one-architecture-evolution.md` | 调用链、架构选择和从 M0 到真实试点的 0→1 演进故事 | 500～800 行 |

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

新增两份互补材料：

- `06-agent-interview-fundamentals.md` 横向串联通用 Agent 知识与项目实现；
- `07-zero-to-one-architecture-evolution.md` 纵向讲清调用链、架构选择和项目从 0 到 1 的演进。

两份材料共同解决三个问题：

1. 面试者只会讲项目，但回答不了通用 Agent 原理；
2. 背得出通用八股，却不能解释它与当前 Log Agent 的具体关系；
3. 能罗列最终模块，却讲不清这些模块为什么按这个顺序出现、解决了什么真实困难。

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

### 11.6 Agent 八股文档的教学结构与规模

- `06-agent-interview-fundamentals.md` 目标 650～900 行；
- 开头先画一张“通用 Agent 知识 → 本项目模块”的 Mermaid 映射图；
- 至少 17 个原八股问题、17 个通用答案、17 个项目答案；
- 每题至少 2 个延伸追问，全文不少于 34 个；
- 至少 12 个项目源码/测试定位，至少 4 个额外知识框；
- 文末提供“高频十题速背”和恰好一个“一句话记住”。

### 11.7 调用链解析要求

`07-zero-to-one-architecture-evolution.md` 不能只放最终架构图，至少从三个视角解释同一次调查：

1. **业务主链路**：飞书或本地 Web 接收请求 → Intake 幂等接单 → SQLite 持久化 → Worker 租约领取 → Eino 固定 Graph → Query Checkpoint → Query Gateway → 阿里云 CLI + STS → Evidence/Report → 火山方舟摘要 → Delivery；
2. **查询治理链路**：服务端可信身份 → Resource Catalog → ACL → 固定查询模板 → 时间/费用/水位门禁 → Schema 校验 → Query Audit → Provider；
3. **失败恢复链路**：重复请求去重 → Worker 续租与接管 → QueryStep 状态转换 → 外部结果未知转 `NEEDS_REVIEW` → 报告生成与消息投递解耦 → 重试、死信或人工确认。

每条链路都必须包含 Mermaid 图，并逐站说明：

- 输入与输出是什么；
- 哪一层负责，为什么职责放在这一层；
- 会失败在哪里；
- 如何恢复或安全停止；
- 可以用什么状态、审计记录或测试证明它发生过。

调用链还要提供两个口述版本：一个 60～90 秒全链路版，一个从“用户点击提交”开始的 3 分钟故事版。

飞书只能按“代码功能链路已实现、真实平台验收待权限”讲述；真实联合验收使用本地 Web 入口，不能把两者混写。

### 11.8 为什么选择当前架构

架构选择必须从业务风险倒推，而不是按技术名词堆砌。正文至少解释以下决策：

| 决策 | 采用方案 | 需要对比的替代方案 | 必须讲清的原因 |
| --- | --- | --- | --- |
| Agent 形态 | 受治理的 Eino 固定 Graph | 开放式 ReAct、完全硬编码流程 | 日志查询有费用和权限风险，需要可预测步骤，同时保留模型总结能力 |
| 分层 | Domain/Application/Ports/Adapters | Handler 直接调用 SDK、全交给框架 | 隔离业务事实与供应商依赖，使 Mock/真实 Provider 和飞书/Web 入口可替换 |
| 状态 | SQLite 状态机、租约、Checkpoint | 纯内存、只靠模型上下文 | 长任务会中断，业务状态必须可审计、可恢复、可去重 |
| 查询 | Catalog + ACL + 固定模板的 Gateway | 模型自由生成查询、用户直传资源名 | 防越权、注入、成本失控和物理资源泄露 |
| 结论 | 先形成 Evidence，再让 LLM 摘要 | 原始日志直接交给 LLM | 确定性事实与语言生成分离，失败可回退且结论可追溯 |
| 接云 | 本机 CLI + SSO/STS Profile | 应用内长期 AK/SK、原 SDK 直连 | 复用企业现有临时凭据流程，应用不保存密钥 |
| 真实试点 | 单 Logstore `error_count_v1` | 假设不存在的字段、立即覆盖 8 个库 | 真实 DAM 字段不满足原模板时先缩小合同，取得可验证的最小闭环 |
| 交互入口 | 保留飞书适配层，同时增加本地 Web | 等待飞书权限后再开发 | 解耦 Agent 核心与入口，在权限未就绪时仍能验收真实 SLS + LLM 主链路 |

每项决策按“约束 → 选项 → 选择标准 → 方案 → 收益 → 代价 → 未来何时重选”展开，避免把当前方案说成任何场景下都最优。

### 11.9 从 0 到 1 的架构演进故事

正文以 `docs/development-process.md`、`docs/roadmap.md`、阶段总结和真实联调记录为事实主线，不按 Git 提交机械复述。必须展现“前一阶段暴露的限制，如何触发后一阶段的架构变化”：

1. **M0 最小骨架**：Go 调查模型、状态机、Mock SLS 和确定性报告，先证明请求能闭环；
2. **M1 查询治理**：加入 Catalog、ACL、Schema、预算和审计，解决用户或模型控制物理资源与查询的问题；
3. **M2 证据化调查**：加入 current/baseline、Evidence、Finding、Recommendation，解决只有结论、无法复核的问题；
4. **M3 相关性克制**：加入变更候选、支持证据和反证，避免把时间接近误判成根因；
5. **M3-B 多信号接口**：预留指标和 Trace 聚合，但明确真实 Provider 未接入；
6. **M4 可靠执行**：加入 Query Checkpoint、未知结果人工复核、投递重试/死信、租户额度和成本熔断；
7. **M5 评测闭环**：加入 Golden、Trace、快照、Replay、Compare、专家反馈账本和离线灰度演练；
8. **真实 LLM 接入**：火山方舟只总结 Evidence，增加结构校验、引用校验、Token 配额和失败回退；
9. **真实 SLS 接入**：从 SDK 改为 CLI + STS；面对 DAM 字段不匹配，收缩为单主 Logstore 的 `error_count_v1`；
10. **真实联合试点**：飞书权限未就绪时增加本地 Web，跑通真实 SLS + SQLite + Eino + 方舟 + Delivery，同时保留飞书代码等待平台验收。

每个阶段都必须回答七个问题：

1. 当时已经有什么；
2. 暴露了什么具体问题；
3. 新增或调整了什么组件；
4. 为什么没有选择更重或更自由的方案；
5. 实现中最困难的点是什么，怎样解决；
6. 用哪一级证据验证效果：单测、Mock E2E、离线评测还是真实联合样本；
7. 留下了什么启示，以及下一阶段仍欠什么。

正文至少提供一张架构演进时间线，以及“最终组件不是一次设计出来的，而是被风险逐层逼出来”的因果图。

### 11.10 困难、解决过程、启示与效果边界

至少完整复盘以下困难，不得只写一句“最终解决”：

- Eino 与自研代码边界：框架负责 Graph 编排，可靠状态、治理和证据语义留在应用层；
- Mock 与真实系统边界：先关闭接口合同，再逐个替换 Provider，并始终标注验证层级；
- SDK 与企业认证流程冲突：改用 CLI + SSO/STS，避免应用长期持有凭据；
- 真实日志字段与 `error_analysis_v2` 不匹配：不伪造 `error_type/instance_id`，收缩到 `env + level` 的 `error_count_v1`；
- LLM 可能改写事实：只给脱敏 Evidence，使用结构化输出、Evidence ID 校验和确定性 fallback；
- 已付费查询遇到进程崩溃：Checkpoint 记录意图、执行和结果，未知结果不盲目重试；
- 飞书权限暂时拿不到：增加本地 Web 适配器验证核心链路，不冒充飞书真实验收；
- 联调中的工程摩擦：遮罩值不等于 API Key、浏览器剪贴板与 Windows 剪贴板隔离、PowerShell 变量冲突和临时凭据清理。

每个困难使用统一模板：**现象 → 根因 → 错误或被否决的方向 → 最终方案 → 验证结果 → 启示 → 尚存限制**。

“效果”必须注明证据层级：

- 单元测试只能证明模块合同；
- Mock E2E 只能证明应用链路和适配器契约；
- 合成 Golden/Replay 只能证明离线回归能力；
- 真实联合样本只能证明当时配置下的单 Logstore count-only 链路可运行；
- 未获得长期流量、线上事故和真实飞书验收前，不给出准确率、节省时长或生产收益数字。

### 11.11 v0.2 验收标准

- [ ] 所有 17 个主题均先呈现原八股，再呈现项目映射；
- [ ] 通用标准答案不依赖 Log Agent 也能成立；
- [ ] 项目标准答案与 v0.1 架构、亮点和真实验收边界一致；
- [ ] 至少 34 个延伸追问，并从概念逐步追到取舍与故障场景；
- [ ] 外部结论均可追溯到本节列出的权威来源；
- [ ] 项目映射至少提供 12 个有效 `file:///` 源码或测试链接；
- [ ] 未使用的 ReAct、RAG、Multi-Agent、MCP 不被包装成已实现能力；
- [ ] 业务主链路、查询治理链路和失败恢复链路均有 Mermaid 图和逐站解释；
- [ ] 0→1 演进覆盖 M0～M5、真实 LLM、真实 SLS 与本地 Web 联合试点；
- [ ] 每个演进阶段均回答“问题—选择—困难—验证—启示—限制”；
- [ ] 至少 8 个困难按统一模板完整复盘；
- [ ] 至少 8 项架构决策对比替代方案，并说明未来何时需要重选；
- [ ] 所有效果均标注单测、Mock、离线评测或真实联合样本的证据层级；
- [ ] `07-zero-to-one-architecture-evolution.md` 至少包含 3 张链路图、1 张演进时间线和 2 个可朗读版本；
- [ ] README 增加两份新文档入口和对应阅读路线；
- [ ] 行数、知识框、Mermaid、源码链接和文末速记满足阈值；
- [ ] 全局链接、事实边界、术语和 Git 工作树交叉审查通过。

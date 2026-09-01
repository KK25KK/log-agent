# Log Agent 的项目亮点与难点：不是“接个模型”，而是管住一条真实调查链

> 面向岗位：校招 Agent 开发 / Go 后端
> 源码基线：`e115d4dcd7993b1c25e0001be951dad2c2cc1f1c`
> 使用方式：每个主题都按“问题—难点—方案—实现—替代方案—优势—代价—效果—下一步”展开，可直接转成面试回答。

## 1. 先判断什么才算这个项目的亮点

如果把 Agent 比作一位刚入职的值班工程师，接入 LLM 只相当于让他“会说话”。
真正困难的是让他知道自己能查什么、一次最多查多少、查到一半进程崩了怎么办、结论依据在哪、模型说错了如何拦住。

因此，本项目的亮点不是堆了多少框架名，而是把真实工具调用放进可审计、可恢复、可降级的工程边界中。
下面七个主题覆盖 Agent 编排、Go 工程、可靠性、安全、LLM 和评测。

## 2. 亮点一：用固定 Eino Graph 约束 Agent 自主性

### 问题

日志排障天然像一个 Agent 任务：系统需要理解目标、规划观测窗口、调用日志工具、比较结果并形成报告。
但 SLS 查询同时涉及权限、费用和敏感数据，不能让模型随意探索。

### 难点难在哪里

开放式 Agent 很容易出现三个问题：

1. 同一个问题每次产生不同查询路径，难以回放；
2. 为了“多找一点证据”不断扩大时间窗，费用不可控；
3. 模型生成查询表达式后，很难证明它没有越权或注入风险。

难点不是让流程“更聪明”，而是在有限自主性和工程确定性之间找平衡。

### 方案

项目选择 Eino，但只用它编排固定调查图：

1. `plan_queries` 生成当前与基线两个标准 QuerySpec；
2. `execute_queries` 调用受治理执行器；
3. `build_report` 用规则生成 Evidence、Finding 和 Recommendation；
4. `correlate_changes` 在满足前置条件时补充变更与运维信号。

节点和边在 [engine.go](file:///D:/日志agent/internal/adapters/eino/engine.go#L90-L159) 中编译一次，
运行时调用已编译 Runner，见
[engine.go](file:///D:/日志agent/internal/adapters/eino/engine.go#L162-L182)。

### 怎么实现

应用层不直接依赖 Eino 类型，只依赖 [ports.go](file:///D:/日志agent/internal/ports/ports.go#L75-L89) 里的 `InvestigationEngine` 接口。
Eino 适配器接收标准化请求，输出领域 Evidence 和 Report。

Graph 还记录固定节点的开始、成功、失败和跳过状态，因此失败位置可以被观测，未运行节点不会被误报成成功。
闭合图的 Trace 行为由
[engine_test.go](file:///D:/日志agent/internal/adapters/eino/engine_test.go#L341-L405) 验证。

### 为什么不采用开放式 ReAct

ReAct 更适合工具低风险、搜索空间开放、结果可快速人工确认的场景。
当前项目的主要目标是先把一条真实日志调查链安全跑通，而不是覆盖所有未知故障类型。

如果第一阶段就让模型自由选择工具和参数，每次行为上界、权限边界和恢复语义都会变得模糊。
所以这里先锁定调查合同，后续再在低风险节点逐步开放策略选择。

### 优势

- 调用次数和成本上界可估算；
- 每个节点都能独立测试和观测；
- 同一输入更容易回放比较；
- LLM 故障不会改变核心查询路径；
- Eino 可替换，业务状态不被框架绑死。

### 代价

- 无法临场发明新的查询维度；
- 新调查类型需要新增模板或图节点；
- 对复杂未知故障的覆盖率低于开放式 Agent。

### 效果与证据边界

自动化测试已经覆盖突增、零基线、不完整证据、治理漂移和跨信号关联。
例如不完整 Evidence 不会生成确定性结论，见
[engine_test.go](file:///D:/日志agent/internal/adapters/eino/engine_test.go#L308-L325)。

这能证明固定图的行为合同，
不能证明它已经覆盖所有真实生产故障。

### 下一步优化

可以把“选择已批准调查模板”开放给策略层，
但模板仍由管理员注册，所有工具调用仍穿过 Query Gateway。
这样可以增加灵活性，又不把查询控制权完全交给模型。

> 📦 **额外知识：Agent 的自主性可以分级**
>
> 自主性不是“有或没有”两档。
> 可以只开放模板选择，也可以开放节点顺序、参数范围，最后才是自由工具调用。
> 高风险场景适合从最低一级开始，用证据逐步放权。

## 3. 亮点二：Query Gateway 把日志查询变成受治理能力

### 问题

一个普通 SLS Client 只关心“查询能不能发出去”。
企业内部 Agent 还必须回答：谁在查、查哪个逻辑服务、为何允许、窗口多大、索引是否满足、花了多少、审计是否落库。

### 难点难在哪里

安全检查如果散落在入口、Worker 和 Adapter 中，新增入口时很容易漏掉某一层。
更棘手的是先查询后审计：一旦审计写失败，敏感访问已经发生，无法撤回。

### 方案

项目建立单一 Query Gateway，所有 SLS 调用必须经过它。
审批路径固定为：

1. 校验 QuerySpec；
2. 用 Service + Environment 解析管理员 Catalog；
3. 校验可信 Principal 的 ACL；
4. 校验资源绑定的固定模板版本；
5. 检查时间窗、摄入水位和模板预算；
6. 获取并发许可和超时 Context；
7. 读取并校验索引 Schema；
8. 写 STARTED 审计后才调用后端；
9. 归一化结果并写终态审计。

核心路径在 [gateway.go](file:///D:/日志agent/internal/application/query/gateway.go#L95-L200)，
预检规则在 [gateway.go](file:///D:/日志agent/internal/application/query/gateway.go#L202-L225)。

### 怎么实现

用户请求只带逻辑范围，物理 Project、Logstore 和字段映射由 Catalog 决定。
Gateway 生成的 `ApprovedQuery` 包含资源、模板、Schema、策略版本和治理指纹。

错误输出也会变成稳定的 Provider 中立原因，避免把查询文本或云端响应体泄露给用户。
授权请求与固定模板测试见
[gateway_test.go](file:///D:/日志agent/internal/application/query/gateway_test.go#L106-L139)，
未授权请求在 Provider 调用前被拒绝，见
[gateway_test.go](file:///D:/日志agent/internal/application/query/gateway_test.go#L247-L264)。

### 为什么不让每个 Adapter 自己校验

Adapter 只理解某个 Provider 的调用细节，不应该决定业务用户能否访问某个服务。
如果授权写进阿里云适配器，未来接入其他日志后端就要复制一套规则，还可能产生两个后端行为不一致的问题。

### 优势

- 所有入口共享一套默认拒绝规则；
- 业务身份与物理资源解耦；
- 查询开始前必须成功落审计；
- 并发、窗口、超时和数据量统一限制；
- Provider 可以替换，治理合同不变。

### 代价

- Catalog、Schema 与模板版本需要管理员维护；
- 新日志结构不能“直接试一下”，要先完成合同配置；
- 严格水位线会牺牲最新几分钟的实时性。

### 效果与证据边界

测试覆盖未授权、超窗、水位未过、Schema 不匹配、审计失败、
Provider 超时后晚返回、并发限制和 Schema 缓存。
例如终态审计失败时不能返回成功，见
[gateway_test.go](file:///D:/日志agent/internal/application/query/gateway_test.go#L535-L566)。

这些是自动化合同证明；
真实环境的 RAM 权限范围仍由企业管理员配置，代码不能替代云端权限审计。

### 下一步优化

将 Catalog 和策略从本地配置升级为带版本审批的配置中心，
并把查询用量导出到可观测平台，形成按租户和模板的长期趋势。

> 📦 **额外知识：水位线为什么重要？**
>
> 日志从应用产生到可查询存在摄入延迟。
> 如果窗口结束时间太靠近现在，“0 条错误”可能只是日志还没到。
> 水位线要求等数据进入稳定区后再下结论，是一种数据完整性保护。

## 4. 亮点三：Checkpoint + 租约解决外部调用恢复难题

### 问题

一次调查至少包含当前窗口和基线窗口两个外部查询。
如果第一个查询成功后进程崩溃，重启后全部重做会浪费费用；但如果请求已发出、结果没来，自动重试又可能重复执行。

### 难点难在哪里

数据库事务无法包住远端 SLS 请求。
系统最多只能知道“准备调用”“收到成功”或“没收到确定结果”，无法获得真正的分布式 Exactly Once。

同时，多 Worker 接管旧任务时，旧进程可能迟到写回结果，造成新旧尝试互相覆盖。

### 方案

项目组合使用三种机制：

- Job Lease：保证某一时刻只有租约持有者推进任务；
- Attempt Fence：旧尝试即使迟到，也不能完成新尝试的任务；
- QueryStep Checkpoint：为 current/baseline 分别记录 STARTED、SUCCEEDED、FAILED、UNKNOWN。

Checkpoint 位于 Eino 外部，
这样更换编排框架也不会丢失恢复语义。
设计接口见
[checkpoint_executor.go](file:///D:/日志agent/internal/application/checkpoint_executor.go#L23-L55)。

### 怎么实现

执行前先解析当前治理指纹，并与 QuerySpec 共同计算输入哈希。
若已有相同哈希、相同治理身份的成功结果则深拷贝复用；旧尝试停在 STARTED 则视为 UNKNOWN；治理变化则拒绝复用。

Checkpoint 执行逻辑在
[checkpoint_executor.go](file:///D:/日志agent/internal/application/checkpoint_executor.go#L55-L137)。
持久化结果跨重启复用由
[query_steps_test.go](file:///D:/日志agent/internal/adapters/sqlite/query_steps_test.go#L22-L64) 验证，
旧租约持有者不能完成任务由
[store_test.go](file:///D:/日志agent/internal/adapters/sqlite/store_test.go#L156-L220) 验证。

### 为什么不采用“失败就重试三次”

重试适合明确未发送、幂等且低成本的操作。
超时只表示客户端不知道结果，不能证明 Provider 没执行；此时重试会把未知状态伪装成普通失败。

项目宁可进入 `NEEDS_REVIEW`，也不在无法证明安全时自动追加一次云查询。

### 优势

- 已完成窗口可以跨进程恢复；
- 治理变化时旧缓存不会被误用；
- 旧 Worker 迟到写入被 fencing 拦截；
- 未知外部结果被诚实建模；
- 恢复机制不依赖 Eino 内存状态。

### 代价

- 状态机和测试明显更复杂；
- `NEEDS_REVIEW` 需要运维人员理解和处理；
- 不能承诺严格的外部 Exactly Once，只能减少重复并失败关闭。

### 效果与证据边界

测试已经覆盖复用、治理漂移、Provider 未知、取消、进程关闭和租约回收。
Worker 将未知结果落为待复核的行为见
[checkpoint_executor_test.go](file:///D:/日志agent/internal/application/checkpoint_executor_test.go#L384-L429)。

当前验证基于 SQLite 单实例技术预览，
不能直接等同于多机生产调度器。

### 下一步优化

生产化时可将 Store 迁移到支持行锁和高可用的数据库，
增加 Operator UI 展示 QueryStep、治理指纹差异与人工确认动作。

> 📦 **额外知识：Exactly Once 为什么常常是错觉？**
>
> 本地事务可以保证数据库写一次，无法证明远端服务只执行一次。
> 工程上更诚实的做法是使用幂等键、Checkpoint、fencing 和人工复核，
> 把“至少一次”带来的重复风险限制在可控范围内。

## 5. 亮点四：Evidence 先于 LLM，模型输出必须可引用

### 问题

LLM 很适合把日志结论翻译成人能快速阅读的摘要，但直接交付原始日志会产生隐私泄露、提示注入、幻觉和结论不可追溯问题。

### 难点难在哪里

只在 Prompt 里写“请勿编造”并不可靠。
模型仍可能引用不存在的证据、提出未批准操作，或在 count-only 数据上猜测错误类型和实例。

### 方案

系统先用确定性规则形成 Evidence、Finding 和 Recommendation，再构造最小 SummaryInput。
LLM 只负责重述现象、引用已有 Evidence ID、选择已有建议代码，并可选择一个已有根因假设。

输入合同位于 [summary.go](file:///D:/日志agent/internal/domain/summary.go#L24-L73)，
摘要服务在 [summary.go](file:///D:/日志agent/internal/application/summary.go#L29-L156)
执行输入安全检查、额度预留、Provider 调用、输出校验和回退。

### 怎么实现

Provider 端使用严格 JSON Schema，关闭服务端存储并限制输出 Token，
实现见 [summarizer.go](file:///D:/日志agent/internal/adapters/volcark/summarizer.go#L131-L228)。

返回后，应用层逐项验证：

- Evidence ID 是否真的存在；
- Recommendation Code 是否在规则报告中；
- 模型是否引入未提供的根因；
- 文本是否包含受限敏感模式；
- Token 用量是否超过预留边界。

只要验证失败，就舍弃模型草稿，保留确定性报告和回退摘要。

### 为什么不把原始日志直接喂给模型

原始日志包含不可控文本，可能带账号、路径、业务数据或恶意指令，还会快速放大 Token 成本。
更关键的是，模型从几条样本推断总体趋势，很容易把“看见”误当“证实”。

本项目让 SLS 做聚合，让规则层做事实判断，让 LLM 做表达优化。

### 优势

- Provider 看不到凭据、身份和物理资源；
- 摘要中的关键陈述能回指 Evidence；
- LLM 失败不拖垮调查成功状态；
- count-only 模板不会越界生成维度结论；
- 可以统计每租户 Token 配额并熔断。

### 代价

- 摘要的发挥空间更小；
- 需要维护严格的输入输出 Schema；
- 规则层必须先提供足够结构化的事实。

### 效果与证据边界

自动化测试验证了只接受 grounded 草稿、拒绝虚构和敏感输出、
敏感输入不调用 Provider，以及 count-only 不产生根因。
证据见
[summary_test.go](file:///D:/日志agent/internal/application/summary_test.go#L22-L155)。

真实方舟联合样本成功生成 MODEL 摘要，
样本元数据是 725 输入 Token、182 输出 Token、总计 907、延迟 1771ms。
这只是一次本地联合验收记录，不是线上 SLA 或平均性能指标。

### 下一步优化

建立脱敏后的专家反馈集，
分别评价证据忠实度、可读性和建议可操作性，
再决定是否升级模型或 Prompt，而不是只凭主观感受调参。

## 6. 亮点五：用 CLI + STS 接入真实 SLS，同时缩小凭据面

### 问题

原设计使用阿里云 SDK，但实际授权路径是 SSO 发放 STS 临时凭据，并由本机阿里云 CLI Profile 管理。
如果 Agent 再保存一份 AK/SK，会扩大秘密分发和轮换范围。

### 难点难在哪里

把 SDK 换成 CLI 并不只是替换一个调用函数。
CLI 带来进程执行、参数注入、环境变量继承、输出爆量和错误泄露风险，也要兼容真实 `data/meta` 响应。

### 方案

项目废弃 SDK 真实路径，
由 Go 适配器调用固定 `aliyun sls` 子命令。
CLI 自己从指定 StsToken Profile 读取临时凭据并完成签名，
Agent 不读取 Profile 文件，也不把 Token 写入配置。

初始化校验见
[backend.go](file:///D:/日志agent/internal/adapters/aliyuncli/backend.go#L33-L76)，
真实查询编排见
[backend.go](file:///D:/日志agent/internal/adapters/aliyuncli/backend.go#L89-L129)。

### 怎么实现

适配器做了多层收口：

- CLI 路径解析为绝对可执行文件；
- Profile、Region、Endpoint、Project 和 Logstore 使用安全字符校验；
- 子命令和参数由代码固定生成，不经过 Shell 拼接；
- 清理可能覆盖凭据的环境变量；
- 限制输出字节数和请求超时；
- 错误只暴露稳定原因，不回显查询和 Provider Body；
- 用查询前后计数检测变化边界，必要时把结果降级为不完整。

固定聚合查询测试见
[backend_test.go](file:///D:/日志agent/internal/adapters/aliyuncli/backend_test.go#L51-L127)，
凭据环境清理和错误脱敏测试见
[backend_test.go](file:///D:/日志agent/internal/adapters/aliyuncli/backend_test.go#L278-L309)。

### 为什么不继续使用 SDK

SDK 的类型安全和进程内性能更好，但在当前环境里需要单独解决凭据注入与轮换。
CLI 已承接 SSO → STS 的标准路径，复用它能更快完成只读试点，并减少 Agent 接触长期凭据的机会。

### 优势

- 复用现有 SSO/STS 运维习惯；
- Agent 不保存 AK/SK/Token；
- CLI 可由用户在终端独立诊断；
- 真实 SLS 与 Mock SLS 仍共享同一端口；
- SDK 被移除后认证路径更单一。

### 代价

- 依赖本机 CLI 和插件版本；
- 进程启动开销高于 SDK；
- 跨平台路径和返回格式需要额外兼容；
- Profile 过期仍需用户重新获取 STS。

### 效果与证据边界

8 个 Logstore 曾通过人工 CLI 连接检查，
Agent 的真实联合 E2E 已在一个主 Logstore 上跑通 `env + level` 的 count-only 调查。
不能把它描述成 Agent 已自动展开 StoreView 或统一分析 8 个库。

### 下一步优化

将 CLI 版本和插件能力加入启动前兼容性检查，
补充可观测的 Provider 调用耗时分位，
再根据实际吞吐决定是否需要受控执行器服务，而不是直接回退到 SDK。

> 📦 **额外知识：CLI 不是天然比 SDK 安全**
>
> CLI 只有在“不经过 Shell 拼接、固定子命令、限制环境和输出”时才可控。
> 如果直接执行用户输入的命令字符串，风险反而可能高于 SDK。
> 安全来自适配器约束，不来自“CLI”三个字。

## 7. 亮点六：调查和投递分离，失败可以各自恢复

### 问题

排障报告已经算完但飞书暂时不可用时，把投递当成调查事务的一部分会把成功调查标成失败；整体重跑又会重复查询 SLS 和消耗 LLM。

### 难点难在哪里

消息发送也存在“已发出但客户端没收到确认”的不确定状态。
不同卡片更新还有顺序：初始回执必须先于进度和终态，旧进度不能覆盖新终态。

### 方案

系统把报告持久化和消息投递拆成两条可靠链：

- Worker 原子保存调查终态并入队 Delivery；
- DeliveryWorker 用独立租约领取消息；
- 临时失败指数退避；
- 永久失败或达到次数后进入 Dead Letter；
- Operator 可审计并安全重放允许的死信。

投递 Worker 见
[delivery.go](file:///D:/日志agent/internal/application/delivery.go#L13-L94)，
SQLite 的领取与失败落库见
[delivery.go](file:///D:/日志agent/internal/adapters/sqlite/delivery.go#L98-L163)
和 [delivery.go](file:///D:/日志agent/internal/adapters/sqlite/delivery.go#L285-L350)。

### 怎么实现

每个 Delivery 带调查 ID、投递类型、序号、尝试次数和卡片绑定信息。
租约与 Attempt Fence 防止两个投递 Worker 同时完成同一条消息。
失败分类决定重试还是死信，退避最大限制为一分钟。

顺序与卡片绑定由
[delivery_test.go](file:///D:/日志agent/internal/adapters/sqlite/delivery_test.go#L13-L93) 验证，
旧进度不能覆盖终态由
[delivery_ops_test.go](file:///D:/日志agent/internal/adapters/sqlite/delivery_ops_test.go#L13-L68) 验证。

### 为什么不在 HTTP 请求里同步发送

同步发送实现简单，但把用户请求时延、调查时延和飞书时延绑在一起。
任何一处超时都会造成调用方重试，重复风险被放大。
独立 Delivery 是更清晰的“Outbox 风格”边界。

### 优势

- 投递失败不会重新调查；
- 可以单独观察重试和死信；
- 终态报告先持久化，不依赖外部平台可用性；
- Web Sender 与飞书 Sender 复用同一 Delivery 合同。

### 代价

- 用户看到结果有短暂最终一致延迟；
- 需要管理投递顺序、死信和重放规则；
- 飞书功能代码已实现，真实平台仍需验证 Reply/Patch 语义。

### 效果与证据边界

飞书接收器、卡片动作、Reply/Patch Sender 与可靠投递功能已经实现，
核心实现可见 [receiver.go](file:///D:/日志agent/internal/adapters/feishu/receiver.go#L77-L159) 和 [sender.go](file:///D:/日志agent/internal/adapters/feishu/sender.go#L38-L149)。
本地 Web 联合链进一步串联真实 SLS 和真实 LLM，验证了飞书之外的应用主链。
当前缺少的是飞书应用权限下的平台验收，不是飞书功能代码本身。

### 下一步优化

获得飞书权限后，
按事件接收、身份映射、初始 Reply、进度 Patch、终态 Patch、按钮回调逐项验收，
并用真实平台 Request ID 补充投递审计。

## 8. 亮点七：评测、快照和反证让 Agent 迭代可比较

### 问题

Agent 修改一个阈值、Prompt 或规则后，单看几个“成功案例”无法判断是否退化。
日志调查尤其危险：模型可能给出更流畅但更不忠实的答案。

### 难点难在哪里

真实日志会随时间变化，直接重跑两个版本不能公平比较；只测 happy path 又发现不了误导性结论、缺证据引用和治理漂移。

### 方案

项目建立三层离线验证：

1. Golden Dataset：固定输入与预期门禁；
2. Snapshot Archive：保存评测版本、输入摘要、输出和 lineage；
3. Replay Compare：不重新访问外部系统，直接比较两个严格快照。

评测入口在 [evaluate.go](file:///D:/日志agent/cmd/logagent/evaluate.go#L190-L223)，
回放比较入口在 [evaluate.go](file:///D:/日志agent/cmd/logagent/evaluate.go#L82-L121)。

### 怎么实现

每次评测生成唯一 Run ID 和稳定版本指纹。
门禁不仅检查是否生成报告，还检查 Evidence 引用、结论是否超出完整性、危险建议和反证场景。
两个快照只有数据集和合同可比较时才输出 Delta，
否则明确标记 incomparable，避免伪精确比较。

Golden 与误导结论失败关闭由
[evaluate_test.go](file:///D:/日志agent/cmd/logagent/evaluate_test.go#L14-L73) 验证；
严格快照、回放 lineage 和不可比较边界由
[evaluate_test.go](file:///D:/日志agent/cmd/logagent/evaluate_test.go#L95-L224) 验证。

### 为什么不只做单元测试或线上 A/B

单元测试能证明局部函数，却难回答整个报告是否更忠实。
线上 A/B 需要真实流量、风险隔离和反馈标签，当前阶段过重。
离线 Golden + Replay 能先用较低成本锁住安全底线。

### 优势

- 版本升级有可追溯比较；
- 不依赖变化中的真实日志重复出现；
- 失败样本和反证被当成一等公民；
- 可先挡住安全退化，再讨论体验提升。

### 代价

- 合成数据不能代表真实故障分布；
- Golden 需要持续由专家反馈更新；
- 离线通过不等于线上效果好。

### 效果与证据边界

当前固定 Golden Set、摘要安全场景和回放比较已有自动化测试，
摘要安全评测入口测试见
[summary_evaluate_test.go](file:///D:/日志agent/cmd/logagent/summary_evaluate_test.go#L13-L47)。

它证明“已定义场景没有回归”，
不能声称准确率达到某个百分比，也不能替代生产灰度。

### 下一步优化

把真实排障后的专家反馈脱敏进入反馈账本，
按模板、环境和失败类型分层抽样，
逐步形成覆盖真实长尾问题的回归集。

> 📦 **额外知识：反证比多一个成功样本更值钱**
>
> 成功样本只能证明系统在某种条件下会工作。
> 反证样本检查系统何时应该保持沉默、降级或拒绝结论，
> 对高风险 Agent 来说，这往往比“回答得更多”更重要。

## 9. 七个亮点如何组合成一条主线

这七个主题不是七个互不相关的功能：

1. 固定 Graph 规定 Agent 可以走哪些调查步骤；
2. Query Gateway 规定每一步可以访问什么数据；
3. Checkpoint 与租约保证步骤中断后不会盲目重做；
4. Evidence 规定结论和 LLM 能说什么；
5. CLI + STS 把规则接到真实 SLS；
6. 独立 Delivery 保证结果投递失败不污染调查；
7. Golden + Replay 保证迭代时能发现安全退化。

面试时不需要一次讲完七个。
建议先讲“固定 Graph + Query Gateway + Evidence-only LLM”作为 Agent 主线，面试官追问工程深度时再展开 Checkpoint、租约和评测。

## 10. 当前效果应该怎样诚实表达

可以说：

- Go 主链、Eino 固定图、SQLite 状态、查询治理和 LLM 安全边界已实现；
- 自动化测试覆盖幂等、租约、治理拒绝、未知结果、摘要回退和回放；
- 本地 Web 已串联真实阿里云 SLS 与真实火山方舟完成一次联合验收；
- 真实试点使用单主 Logstore 的 count-only 模板；
- 飞书接收、卡片动作和可靠投递功能已实现，真实平台验收待应用权限。

不可以说：

- 已经在生产大规模上线；
- 自动覆盖 DAM 全部 8 个 Logstore；
- 已完成飞书真实平台联调或生产上线；
- 根因准确率达到某个百分比；
- 能自动修复或回滚故障；
- 某次样本 Token 和延迟代表稳定 SLA。

## 11. 校招面试中的价值

这个项目适合 Agent 开发岗位，因为它不只展示“会调用模型”，还展示了三层判断：

- 产品判断：先解决高频、边界清楚的日志突增调查；
- Agent 判断：自主性应该受风险约束，Evidence 应先于生成；
- 工程判断：外部调用必须考虑身份、预算、审计、恢复和评测。

它也保留了诚实的不足：SQLite 仍是单实例技术预览，飞书真实平台验收待权限，
跨多日志库、真实 Metrics/Trace、企业 Runbook 和生产灰度仍是后续工作。
能把这些边界讲清楚，本身就是工程成熟度的一部分。

> 💡 **一句话记住**：这个项目真正的亮点，是把“会查日志、会写摘要”的 Agent，约束成一条有门禁、有案卷、有恢复、有反证的真实工程链路。

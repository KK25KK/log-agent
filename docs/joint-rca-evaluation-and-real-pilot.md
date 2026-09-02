# 联合 RCA 离线评测与真实试点手册

## 1. 状态结论

| 项目 | 当前状态 |
| --- | --- |
| `joint-rca-v1` 主体代码 | 已完成 |
| 合成离线评测 | 8/8 Case 通过 |
| 真实 DAM 8 库 Schema | 8/8 READY，零日志读取（既有记录） |
| 真实 DAM TraceID Smoke | 待代表性 TraceID |
| 真实部署 Commit 目录 | 待部署平台事实 |
| 真实代码联合 Smoke | 待上述两项 |
| 真实事故专家评审 | 待脱敏历史 Case 与 Reviewer |
| 生产灰度/自动处置 | 未批准；自动处置不在当前范围 |

离线通过只证明规则和实现稳定，不能写成“真实根因准确率 100%”。

2026-09-02 当前工作树实测：`PASSED`，8/8 Case；Dataset fingerprint 为 `0362e2d15f144fb78bffb611e560b4f0eed0348fa7da0766431f7c4b1ac08d79`。Case/状态/Verdict/引用完整性/确定性重放均为 1，不安全声明、自动动作、外部网络、真实事故和专家标签均为 0。

## 2. 独立离线门禁

执行：

```powershell
Set-Location "D:\日志agent"
go run ./cmd/logagent joint-rca-evaluate
```

固定数据边界：

- `data_source=SYNTHETIC_MOCK`
- `real_incident_count=0`
- `expert_label_count=0`
- `credentials_required=false`
- `external_network_calls=0`
- `production_claim_allowed=false`

固定门禁：Case、状态、Verdict、引用完整性和确定性重放均为 `1`；不安全确定性根因表述、自动动作和网络调用均为 `0`。

8 个场景覆盖：

1. 堆栈直达 + 可信变更重叠；
2. 错误文本命中 + 文件未变化软反证；
3. 代码搜索截断并强制不确定；
4. 完整搜索无匹配；
5. 部署记录冲突；
6. Trace 不完整；
7. 代码 Provider 不可用；
8. 没有安全运行时锚点。

实现位置：`internal/evaluation/jointrca`；Fixture：`internal/evaluation/jointrca/fixtures/synthetic-v1.json`。

## 3. 四类验收必须分开

| Profile | 数据 | 可以证明 | 不可以证明 |
| --- | --- | --- | --- |
| `SYNTHETIC_MOCK` | 仓库内合成 Fixture | 规则、状态、预算、引用和降级合同 | 真实召回率、准确率、MTTR |
| `REDACTED_HISTORICAL` | 脱敏历史事故 + 专家标签 | 离线候选有用性与安全性 | 实时连接、生产稳定性 |
| `REAL_SINGLE_PILOT` | 一次测试环境真实 Trace/部署/代码 | 外部链路和字段契约在该样本可用 | 普遍准确率、生产 SLO |
| `PRODUCTION_APPROVED` | 审批后的灰度流量 | 经治理范围内的运行表现 | 超出审批范围的自动扩权或处置 |

任何报告或面试材料必须同时给出 Profile、真实事故数、专家标签数和生产声明权限。

## 4. 脱敏历史 Case 准备

复制 [`templates/joint-rca-history-case.example.json`](templates/joint-rca-history-case.example.json) 到 Git 忽略的 `data/joint-rca-history/`，每个事故一份。必须：

1. 使用无业务含义 Case ID；
2. 原始 TraceID只计算 SHA-256 指纹，不保存原值；
3. 不保存原始日志和代码正文，只保留受治理 Report/Deployment 指纹及逻辑文件/符号标签；
4. 根因只来自既有复盘事实；没有事实就标 `UNLABELLED`；
5. 独立隐私 Reviewer 完成五项检查；
6. `production_claim_allowed` 始终为 `false`，真实生产批准由独立变更流程管理。

建议第一轮准备 10～20 个测试/预发历史 Case，至少覆盖代码、配置、数据、下游依赖和“未确认根因”。样本数量是建议，不是当前已完成结果。

## 5. 专家评审流程

使用 [`templates/joint-rca-expert-review-template.md`](templates/joint-rca-expert-review-template.md)：

1. 固定同一个 Report fingerprint；
2. 两名 Reviewer 独立评审，至少一名熟悉目标服务；
3. 先检查数据边界，再看候选，避免敏感材料污染；
4. 分别评价代码位置、运行时绑定、变更关系、Verdict 保守性和人工动作；
5. 任一 `UNSAFE_OR_MISLEADING` 或 Reviewer 分歧均阻断扩大试点；
6. 只统计有可信事故复盘标签的根因准确类指标；`UNLABELLED` 只评安全性和缩小范围能力。

建议指标：

- Top-1/Top-3 文件命中率；
- 候选接受率与 `UNSAFE_OR_MISLEADING` 率；
- `INCONCLUSIVE` 是否正确保守；
- 人工动作有用率；
- 从打开报告到定位首个有效代码位置的时间。

在真实样本完成前，上述指标没有当前数值。

## 6. 真实测试环境预检

### 6.1 保持凭据只在本机

阿里云继续由本机 `aliyun` CLI 的 `default + StsToken` Profile 完成签名；火山方舟 Key 只放当前终端环境变量；两者都不能写入 Git、聊天、截图或报告。

### 6.2 检查 Trace 资源与权限（零日志读取）

```powershell
Set-Location "D:\日志agent"
$env:LOG_AGENT_TRACE_MODE = "aliyun"
$env:LOG_AGENT_TRACE_CATALOG = ".\config\trace-resources.json"
go run ./cmd/logagent trace-check
```

验收：全部成员 `READY` 且 `log_reads=0`。失败时先修 Profile、权限、Region、成员或索引，不进入真实 Smoke。

### 6.3 写入真实部署事实

```powershell
Copy-Item .\config\code-resources.example.json .\config\code-resources.json
notepad .\config\code-resources.json
```

只从发布平台/镜像制品映射填写：

- 事故时间生效的完整 `commit_sha`；
- 同一可信来源给出的 `previous_commit_sha`，没有就删除字段；
- 部署时间和可选制品摘要；
- 本地仓库真实顶层路径与允许目录。

禁止执行 `git rev-parse HEAD` 后把结果当成部署版本。

```powershell
$env:LOG_AGENT_CODE_MODE = "localgit"
$env:LOG_AGENT_CODE_CATALOG = ".\config\code-resources.json"
go run ./cmd/logagent code-check dam-server test "<事故结束时间-RFC3339>"
```

验收：唯一部署记录、仓库顶层和 Commit 对象通过；`code_reads=0`。

### 6.4 可选方舟 Intent 预检

```powershell
$env:LOG_AGENT_INTENT_MODE = "volcark"
$env:LOG_AGENT_ARK_API_KEY = "<仅在本机终端输入>"
$env:LOG_AGENT_ARK_MODEL = "<已开通的方舟模型或推理接入点>"
go run ./cmd/logagent intent-check
```

`intent-check` 不查 SLS。不要用命令历史或脚本保存 Key；完成后执行：

```powershell
Remove-Item Env:LOG_AGENT_ARK_API_KEY
```

## 7. 真实单次联合 Smoke

只使用测试环境、已知代表性 TraceID 和最大 30 分钟窗口：

```powershell
Set-Location "D:\日志agent"
$env:LOG_AGENT_TRACE_MODE = "aliyun"
$env:LOG_AGENT_TRACE_CATALOG = ".\config\trace-resources.json"
$env:LOG_AGENT_CODE_MODE = "localgit"
$env:LOG_AGENT_CODE_CATALOG = ".\config\code-resources.json"

go run ./cmd/logagent trace-smoke dam-server test 10m "<真实TraceID>"
```

逐项验收：

- 调查终态是 `SUCCEEDED`，或在不完整/未知情况下按合同保守停止；
- 8 个逻辑成员分别有状态、审计和 Checkpoint；
- 持久化报告中没有原始 TraceID、物理 Logstore、凭据或未脱敏个人数据；
- Anchor 与事件/成员引用一致；
- Deployment 是事故时刻唯一完整 Commit；
- Code Match 来自该 Commit，未提交工作区内容不参与；
- `JointRCA` 只出现候选、反证、缺失项和人工动作，不出现确认根因或自动修复；
- 无命中是有效结果，不自动扩大环境、时间或仓库。

不要把单次成功写成准确率。将输出中的非敏感指纹、状态、计数、版本和 Reviewer 结论填入试点记录，不复制代码正文。

## 8. 放量与回滚门禁

扩大测试环境试点前至少满足：

- 全仓测试、静态检查和三类离线评测通过；
- 真实 `trace-check/code-check` 通过；
- 有代表性的真实单次 Smoke 完成；
- 脱敏历史集达到团队约定规模；
- 每个有效 Case 有双 Reviewer；
- 无安全泄露、伪确定根因或自动处置；
- 查询预算、STS 续期、部署目录维护和回滚负责人明确。

任一条件失效时，关闭自然语言/代码模式或回到结构化 `error_count_v1` 试点，不删除历史 Evidence，不把失败样本重标成成功。

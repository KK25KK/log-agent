# DAM 单 Logstore 轻量试点

## 结论

第一阶段只接入 `tech-center-sha / 2016-hyper-dam-file`，使用新增的 `error_count_v1`。当前日志只需已有的 `env + level` 索引，不要求日志采集侧补充 `error_type` 或 `instance_id`。

试点不开发 StoreView 展开、8 库并发、原始 `msg` 采样、错误指纹归并或自动根因分析。LLM 摘要和飞书投递只使用 Mock；真实系统验收仅覆盖 SLS 的只读 `sls-check` 与 `sls-smoke`。

## 已验证的真实边界

截至 2026-09-01，本机 `default` StsToken Profile、SLS CLI 插件、Project、主 Logstore、Index 元数据和 `env:"test" AND level:"error"` 聚合计数均已只读接通。真实聚合响应的 SQL 行位于 `data`，质量和用量元数据位于 `meta`。

本次针对新增模板的实际验收结果：

- `sls-check` 成功解析 `dam-server-test-count`，确认 Logstore 为 Standard 模式，并读取到模板所需的 4 个索引字段。
- `sls-smoke dam-server test 10m` 经 Catalog、ACL、Schema、预算和查询审计成功完成，Provider `progress=Complete`。
- Smoke 固定执行 2 次计数聚合，返回一致计数，且 Pattern/Instance 上限均为 0；没有读取或保存原始日志。
- 当次观测到的错误计数属于易变的连接验收样本，不归档为故障事实，也不据此宣称排障准确率。

这些结果证明连接、权限和固定计数查询可用，不证明故障结论、生产准确率或飞书到 SLS 的完整真实 E2E。

## 模板合同

`error_count_v1` 每个窗口只执行两次固定聚合：

1. count-before：`env + level` 条件下的错误总数。
2. count-after：重复相同计数，作为跨调用一致性门禁。

两次计数不一致时结果为 `Incomplete`，不能生成确定性突增结论。current 与 baseline 各一个窗口，因此完整调查最多四次 SLS 查询。模板不读取 `msg`，不返回错误类型和实例分布。

下游必须明确表达能力边界：

- 可以输出当前/基线错误数、差值、倍数、完整性、查询用量和证据。
- 错误类型和实例分布显示“本模板不适用”。
- Cause、Timeline、Runbook 均为 `INCONCLUSIVE`，且不调用对应外部数据源。
- LLM Mock 只能改写计数事实和安全建议，不得补造根因。
- 飞书 Mock 只验证卡片渲染和投递状态，不代表真实飞书已经接通。

## 资源配置

示例文件为 `config/sls-resources.dam-pilot.example.json`。资源固定绑定 `error-count-v1`，选择器固定为 `env=test`，错误选择器固定为 `level=error`，不包含 `error_field` 或 `instance_field`。

```powershell
$env:LOG_AGENT_SLS_MODE = "aliyun"
$env:LOG_AGENT_SLS_CATALOG = ".\config\sls-resources.dam-pilot.example.json"
$env:LOG_AGENT_SLS_CLI_PROFILE = "default"
go run ./cmd/logagent sls-check
```

Smoke Principal 必须与示例 Binding 一致：

```powershell
$env:LOG_AGENT_SMOKE_APP_ID = "replace_with_feishu_app_id"
$env:LOG_AGENT_SMOKE_TENANT_KEY = "replace_with_feishu_tenant_key"
$env:LOG_AGENT_SMOKE_USER_ID = "replace_with_feishu_open_id"
go run ./cmd/logagent sls-smoke dam-server test 10m
```

## 试点完成标准

- `sls-check` 只要求并验证 `env` 与 `level` 字段，不要求统计型维度。
- `sls-smoke` 经过 Catalog、ACL、Schema、预算和审计，返回两次一致性计数组成的单窗口结果，不包含原始日志正文。
- Mock E2E 创建 current/baseline Evidence，输出计数突增判断，并以 Mock LLM 和 Mock 飞书完成下游链路。
- 任一权限、STS、字段、完整性、用量元数据或预算门禁失败时安全降级，不生成确定性结论。

上述标准已于 2026-09-01 在真实 SLS 上通过。它只代表 DAM 主 Logstore 测试环境的计数型试点，不代表 DAM 8 个 Logstore 的完整排障覆盖；LLM 和飞书仍为 Mock。

## 后续本地 Web 联合试点

同日新增的 `web` 命令允许在不修改飞书代码的前提下，把本地页面、正式 Worker、DAM 真实 SLS 和火山方舟真实摘要连接到同一次调查。资源目录 Binding 使用服务端固定的 `local-web/local-pilot/operator`，页面仍只能提交 `dam-server/test/10m/error_count_v1` 这类逻辑范围。

本地 Web 的 Mock 联合链路已有自动化测试；真实 SLS + 真实方舟的同调查联合运行仍需执行并单独记录。在它完成前，本节不能把前文的独立 SLS Smoke 和独立方舟 Smoke 合并描述为联合验收。飞书 WebSocket、OpenID、Reply/Patch 和卡片回调继续保持未验收。操作见 [`local-web-pilot-console.md`](local-web-pilot-console.md)。

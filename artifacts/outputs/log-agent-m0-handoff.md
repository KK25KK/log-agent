# Log Agent M0 交付说明

## 项目位置

```text
D:\日志agent
```

入口文档：[README](../../README.md)  
阶段路线：[Roadmap](../../docs/roadmap.md)  
技术契约：[Specification](../../docs/spec.md)  
Eino 决策：[ADR-0001](../../docs/adr/0001-eino-as-orchestration-adapter.md)

## M0 已完成

- Go 命令入口：`demo`、`worker`、`feishu`。
- 飞书 WebSocket 事件解析与 2 秒持久化预算。
- SQLite 原子 Inbox、调查任务、证据和报告。
- 跨连接消息幂等、文件重启恢复。
- Worker 心跳续租、过期重领、取消传播和 attempt fencing token。
- Eino v0.9.14 固定 Graph 与可替换的 `InvestigationEngine`。
- Mock SLS 当前/基线窗口查询。
- 完整性、截断、Query ID、统计一致性和证据引用校验。
- Eino 与飞书 SDK 依赖边界架构测试。

## 本地运行

```powershell
cd D:\日志agent
go run ./cmd/logagent demo
```

预期关键结果：`SUCCEEDED`、`spike_detected`，报告引用两条完整证据。

## 已验证

```text
go test -count=1 ./...                         PASS
go vet ./...                                   PASS
go test -shuffle=on -count=20 \
  ./internal/application \
  ./internal/adapters/sqlite                   PASS
go run ./cmd/logagent demo                     PASS
```

`go test -race ./...` 当前未完成：本机默认关闭 CGO，启用后缺少 `gcc`。需要在带 C 编译器的 Windows 环境或 Linux CI 中补跑。

## 有意保留到后续阶段

- 真实阿里云 SLS 只读接入、Schema 校验、ACL 和查询预算。
- 飞书结果卡片和交互按钮。
- LLM 摘要与解释。
- 瞬时错误分类、步骤幂等键、自动重试和 Outbox。
- 真实飞书应用凭证连通性测试。

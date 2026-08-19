import {
  Button,
  Callout,
  Card,
  CardBody,
  CardHeader,
  Code,
  Divider,
  Grid,
  H1,
  H2,
  H3,
  Link,
  Pill,
  Row,
  Stack,
  Stat,
  Table,
  Text,
  useCanvasAction,
  useCanvasState,
  useHostTheme,
} from "cursor/canvas";

type View = "architecture" | "eino" | "milestones";

const milestones = [
  ["M0", "技术验证", "3–5 天", "飞书、Eino、SLS 链路与 ADR", "未开始"],
  ["M1", "只读查询底座", "1–2 周", "QuerySpec、ACL、预算、Inbox/Outbox", "未开始"],
  ["M2", "错误突增闭环", "1–2 周", "首条端到端调查 Graph", "未开始"],
  ["M3", "证据与反证", "约 2 周", "Evidence Ledger、版本对照", "未开始"],
  ["M4", "长任务与审批", "1–2 周", "故障恢复、幂等、限时 Approval", "未开始"],
  ["M5", "评测与灰度", "1–2 周", "历史回放、安全、成本与质量基线", "未开始"],
];

function Layer({ title, detail, tag }: { title: string; detail: string; tag: string }) {
  const theme = useHostTheme();
  return (
    <div
      style={{
        border: `1px solid ${theme.stroke.tertiary}`,
        borderRadius: 8,
        padding: 14,
        background: theme.bg.elevated,
      }}
    >
      <Row align="center" justify="space-between" gap={12}>
        <Text weight="semibold">{title}</Text>
        <Pill size="sm">{tag}</Pill>
      </Row>
      <Text tone="secondary" size="small" style={{ marginTop: 8, marginBottom: 0 }}>
        {detail}
      </Text>
    </div>
  );
}

function ArchitectureView() {
  const theme = useHostTheme();
  return (
    <Stack gap={18}>
      <Callout tone="success" title="推荐架构">
        Go 模块化单体 + 飞书长连接 + 持久任务 + Eino 确定性 Graph + SLS 类型化工具 + Evidence Ledger。
      </Callout>

      <Grid columns={4} gap={14}>
        <Stat value="1" label="首个垂直场景" tone="info" />
        <Stat value="2" label="运行模式" />
        <Stat value="6" label="可独立验收阶段" />
        <Stat value="只读" label="MVP 生产权限" tone="success" />
      </Grid>

      <Grid columns="minmax(0, 1.45fr) minmax(260px, 0.8fr)" gap={20} align="start">
        <Stack gap={9}>
          <H2>主链路</H2>
          <Layer title="飞书入口" tag="Adapter" detail="长连接、群内必须 @、message_id 去重、3 秒内持久化并结束处理。" />
          <div style={{ textAlign: "center", color: theme.text.tertiary }}>↓</div>
          <Layer title="Durable Inbox / Job / Outbox" tag="Reliability" detail="任务租约、重试、取消、故障恢复和通知幂等；数据库是事实来源。" />
          <div style={{ textAlign: "center", color: theme.text.tertiary }}>↓</div>
          <Layer title="Investigation Worker" tag="Go + Eino" detail="Scope → Baseline → Drilldown → Correlate → Verify → Report。" />
          <div style={{ textAlign: "center", color: theme.text.tertiary }}>↓</div>
          <Layer title="SLS Tool Policy Gateway" tag="Trust boundary" detail="ACL、Schema、QuerySpec、预算、脱敏、完整性检查和审计。" />
          <div style={{ textAlign: "center", color: theme.text.tertiary }}>↓</div>
          <Layer title="阿里云 SLS" tag="Data" detail="生产核心查询优先官方 Go SDK；官方可观测 MCP 用于 PoC 和跨信号扩展。" />
        </Stack>

        <Stack gap={16}>
          <Card>
            <CardHeader trailing={<Pill size="sm" active>边界</Pill>}>Eino 负责什么</CardHeader>
            <CardBody>
              <Stack gap={8}>
                <Text size="small">对话理解和有限工具路由</Text>
                <Text size="small">确定性 Workflow / Graph</Text>
                <Text size="small">Tool Schema、Callback、短期中断恢复</Text>
              </Stack>
            </CardBody>
          </Card>

          <H3>Eino 不负责</H3>
          <Stack gap={7}>
            <Text tone="secondary" size="small">企业权限和云凭证</Text>
            <Text tone="secondary" size="small">飞书消息可靠投递</Text>
            <Text tone="secondary" size="small">长期调查状态和证据</Text>
            <Text tone="secondary" size="small">查询预算、脱敏与 exactly-once</Text>
          </Stack>

          <Divider />

          <Callout tone="warning" title="首版约束">
            不上 Multi-Agent、不执行自由 SQL/SPL、不做自动生产写操作、不把原始日志批量送入模型。
          </Callout>
        </Stack>
      </Grid>
    </Stack>
  );
}

function EinoView() {
  return (
    <Stack gap={18}>
      <Callout tone="success" title="选型结论：建议采用">
        固定 <Code>v0.9.14</Code>，把 Eino 隔离在 <Code>internal/orchestration/eino</Code>，业务层只依赖自有窄接口。
      </Callout>

      <Table
        headers={["采用项", "用途", "生产约束"]}
        rows={[
          ["ADK ChatModelAgent + Runner", "意图理解、有限工具选择、事件流", "只开放受控 Graph Tool"],
          ["compose Workflow / Graph", "固定调查步骤、分支和反证循环", "节点必须可测试、可重放"],
          ["Typed Tool / GraphTool", "SLS、发布、Trace 等能力", "参数先过 Schema、ACL 和预算"],
          ["Callback", "模型、Tool、Graph 的观测", "转换为自有稳定业务事件"],
          ["Interrupt / Checkpoint", "补充输入、后续审批、短期恢复", "不能替代 Investigation 数据库"],
        ]}
        rowTone={["success", "success", "success", "info", "warning"]}
        striped
      />

      <Grid columns={2} gap={18}>
        <Card>
          <CardHeader>采用理由</CardHeader>
          <CardBody>
            <Stack gap={8}>
              <Text size="small">Go 原生、类型化组件和工具契约</Text>
              <Text size="small">Agent 与确定性 Graph 能自然组合</Text>
              <Text size="small">流式、Callback、HITL 和 MCP 生态可复用</Text>
            </Stack>
          </CardBody>
        </Card>
        <Card>
          <CardHeader trailing={<Pill size="sm">0.x</Pill>}>风险控制</CardHeader>
          <CardBody>
            <Stack gap={8}>
              <Text size="small">锁定精确版本，不用 v0.10 alpha</Text>
              <Text size="small">Eino 类型不进入领域和持久化协议</Text>
              <Text size="small">升级前跑历史回放、Tool 契约和恢复测试</Text>
            </Stack>
          </CardBody>
        </Card>
      </Grid>

      <Text tone="secondary" size="small">
        官方依据：<Link href="https://www.cloudwego.io/docs/eino/overview/graph_or_agent/">Agent or Graph</Link> · <Link href="https://github.com/cloudwego/eino/releases">Eino Releases</Link>
      </Text>
    </Stack>
  );
}

function MilestonesView() {
  return (
    <Stack gap={18}>
      <Callout tone="info" title="渐进交付">
        按 2 名 Go 后端、平台与测试兼职估算约 8–12 周。每阶段独立验收；建议 M2 完成后先小范围试用，再决定扩展。
      </Callout>

      <Table
        headers={["阶段", "目标", "周期", "核心交付", "状态"]}
        rows={milestones}
        rowTone={["info", undefined, undefined, undefined, undefined, undefined]}
        striped
      />

      <Grid columns={3} gap={16}>
        <Card>
          <CardHeader>M0 决策门</CardHeader>
          <CardBody>
            <Text size="small">确认 Eino 可替换边界，并用同一批用例比较 SLS SDK 与官方 MCP。</Text>
          </CardBody>
        </Card>
        <Card>
          <CardHeader>M2 价值门</CardHeader>
          <CardBody>
            <Text size="small">只交付“错误突增调查”闭环，真实用户试用后再继续投资。</Text>
          </CardBody>
        </Card>
        <Card>
          <CardHeader>M5 上线门</CardHeader>
          <CardBody>
            <Text size="small">越权为零，并有根因命中率、耗时、成本和用户反馈基线。</Text>
          </CardBody>
        </Card>
      </Grid>

      <H3>亮点能力的推荐顺序</H3>
      <Text tone="secondary">
        发布与配置时间线 → Trace 上下文 → 跨服务传播链 → 相似事故与 Runbook → SLS 告警预调查 → 日志质量医生 → 经审批的低风险处置。
      </Text>
    </Stack>
  );
}

export default function SLSLogAgentArchitecture() {
  const [view, setView] = useCanvasState<View>("selected-view", "architecture");
  const dispatch = useCanvasAction();

  return (
    <Stack gap={22} style={{ maxWidth: 1180, margin: "0 auto", padding: "28px 24px 44px" }}>
      <Stack gap={8}>
        <H1>日志 Agent：Go + 飞书 + Eino 整体方案</H1>
        <Text tone="secondary">
          v0.1 · 首个场景聚焦生产服务错误突增 · 2026-08-18
        </Text>
      </Stack>

      <Row gap={8} wrap>
        <Pill active={view === "architecture"} onClick={() => setView("architecture")}>架构总览</Pill>
        <Pill active={view === "eino"} onClick={() => setView("eino")}>Eino 决策</Pill>
        <Pill active={view === "milestones"} onClick={() => setView("milestones")}>里程碑</Pill>
      </Row>

      {view === "architecture" && <ArchitectureView />}
      {view === "eino" && <EinoView />}
      {view === "milestones" && <MilestonesView />}

      <Divider />
      <Row justify="space-between" align="center" gap={16} wrap>
        <Text tone="tertiary" size="small">主规格包含模块职责、可靠性、安全、数据模型和逐阶段验收条件。</Text>
        <Button
          variant="primary"
          onClick={() => dispatch({ type: "newComposerChat", userPrompt: "请基于这份日志 Agent 架构方案继续评审，并帮我确定 M0 的具体任务。" })}
        >
          继续评审
        </Button>
      </Row>
    </Stack>
  );
}


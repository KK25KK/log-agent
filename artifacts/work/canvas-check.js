// ../canvas/sls-log-agent-architecture.canvas.tsx
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
  useHostTheme
} from "cursor/canvas";
var milestones = [
  ["M0", "\u6280\u672F\u9A8C\u8BC1", "3\u20135 \u5929", "\u98DE\u4E66\u3001Eino\u3001SLS \u94FE\u8DEF\u4E0E ADR", "\u672A\u5F00\u59CB"],
  ["M1", "\u53EA\u8BFB\u67E5\u8BE2\u5E95\u5EA7", "1\u20132 \u5468", "QuerySpec\u3001ACL\u3001\u9884\u7B97\u3001Inbox/Outbox", "\u672A\u5F00\u59CB"],
  ["M2", "\u9519\u8BEF\u7A81\u589E\u95ED\u73AF", "1\u20132 \u5468", "\u9996\u6761\u7AEF\u5230\u7AEF\u8C03\u67E5 Graph", "\u672A\u5F00\u59CB"],
  ["M3", "\u8BC1\u636E\u4E0E\u53CD\u8BC1", "\u7EA6 2 \u5468", "Evidence Ledger\u3001\u7248\u672C\u5BF9\u7167", "\u672A\u5F00\u59CB"],
  ["M4", "\u957F\u4EFB\u52A1\u4E0E\u5BA1\u6279", "1\u20132 \u5468", "\u6545\u969C\u6062\u590D\u3001\u5E42\u7B49\u3001\u9650\u65F6 Approval", "\u672A\u5F00\u59CB"],
  ["M5", "\u8BC4\u6D4B\u4E0E\u7070\u5EA6", "1\u20132 \u5468", "\u5386\u53F2\u56DE\u653E\u3001\u5B89\u5168\u3001\u6210\u672C\u4E0E\u8D28\u91CF\u57FA\u7EBF", "\u672A\u5F00\u59CB"]
];
function Layer({ title, detail, tag }) {
  const theme = useHostTheme();
  return /* @__PURE__ */ React.createElement(
    "div",
    {
      style: {
        border: `1px solid ${theme.stroke.tertiary}`,
        borderRadius: 8,
        padding: 14,
        background: theme.bg.elevated
      }
    },
    /* @__PURE__ */ React.createElement(Row, { align: "center", justify: "space-between", gap: 12 }, /* @__PURE__ */ React.createElement(Text, { weight: "semibold" }, title), /* @__PURE__ */ React.createElement(Pill, { size: "sm" }, tag)),
    /* @__PURE__ */ React.createElement(Text, { tone: "secondary", size: "small", style: { marginTop: 8, marginBottom: 0 } }, detail)
  );
}
function ArchitectureView() {
  const theme = useHostTheme();
  return /* @__PURE__ */ React.createElement(Stack, { gap: 18 }, /* @__PURE__ */ React.createElement(Callout, { tone: "success", title: "\u63A8\u8350\u67B6\u6784" }, "Go \u6A21\u5757\u5316\u5355\u4F53 + \u98DE\u4E66\u957F\u8FDE\u63A5 + \u6301\u4E45\u4EFB\u52A1 + Eino \u786E\u5B9A\u6027 Graph + SLS \u7C7B\u578B\u5316\u5DE5\u5177 + Evidence Ledger\u3002"), /* @__PURE__ */ React.createElement(Grid, { columns: 4, gap: 14 }, /* @__PURE__ */ React.createElement(Stat, { value: "1", label: "\u9996\u4E2A\u5782\u76F4\u573A\u666F", tone: "info" }), /* @__PURE__ */ React.createElement(Stat, { value: "2", label: "\u8FD0\u884C\u6A21\u5F0F" }), /* @__PURE__ */ React.createElement(Stat, { value: "6", label: "\u53EF\u72EC\u7ACB\u9A8C\u6536\u9636\u6BB5" }), /* @__PURE__ */ React.createElement(Stat, { value: "\u53EA\u8BFB", label: "MVP \u751F\u4EA7\u6743\u9650", tone: "success" })), /* @__PURE__ */ React.createElement(Grid, { columns: "minmax(0, 1.45fr) minmax(260px, 0.8fr)", gap: 20, align: "start" }, /* @__PURE__ */ React.createElement(Stack, { gap: 9 }, /* @__PURE__ */ React.createElement(H2, null, "\u4E3B\u94FE\u8DEF"), /* @__PURE__ */ React.createElement(Layer, { title: "\u98DE\u4E66\u5165\u53E3", tag: "Adapter", detail: "\u957F\u8FDE\u63A5\u3001\u7FA4\u5185\u5FC5\u987B @\u3001message_id \u53BB\u91CD\u30013 \u79D2\u5185\u6301\u4E45\u5316\u5E76\u7ED3\u675F\u5904\u7406\u3002" }), /* @__PURE__ */ React.createElement("div", { style: { textAlign: "center", color: theme.text.tertiary } }, "\u2193"), /* @__PURE__ */ React.createElement(Layer, { title: "Durable Inbox / Job / Outbox", tag: "Reliability", detail: "\u4EFB\u52A1\u79DF\u7EA6\u3001\u91CD\u8BD5\u3001\u53D6\u6D88\u3001\u6545\u969C\u6062\u590D\u548C\u901A\u77E5\u5E42\u7B49\uFF1B\u6570\u636E\u5E93\u662F\u4E8B\u5B9E\u6765\u6E90\u3002" }), /* @__PURE__ */ React.createElement("div", { style: { textAlign: "center", color: theme.text.tertiary } }, "\u2193"), /* @__PURE__ */ React.createElement(Layer, { title: "Investigation Worker", tag: "Go + Eino", detail: "Scope \u2192 Baseline \u2192 Drilldown \u2192 Correlate \u2192 Verify \u2192 Report\u3002" }), /* @__PURE__ */ React.createElement("div", { style: { textAlign: "center", color: theme.text.tertiary } }, "\u2193"), /* @__PURE__ */ React.createElement(Layer, { title: "SLS Tool Policy Gateway", tag: "Trust boundary", detail: "ACL\u3001Schema\u3001QuerySpec\u3001\u9884\u7B97\u3001\u8131\u654F\u3001\u5B8C\u6574\u6027\u68C0\u67E5\u548C\u5BA1\u8BA1\u3002" }), /* @__PURE__ */ React.createElement("div", { style: { textAlign: "center", color: theme.text.tertiary } }, "\u2193"), /* @__PURE__ */ React.createElement(Layer, { title: "\u963F\u91CC\u4E91 SLS", tag: "Data", detail: "\u751F\u4EA7\u6838\u5FC3\u67E5\u8BE2\u4F18\u5148\u5B98\u65B9 Go SDK\uFF1B\u5B98\u65B9\u53EF\u89C2\u6D4B MCP \u7528\u4E8E PoC \u548C\u8DE8\u4FE1\u53F7\u6269\u5C55\u3002" })), /* @__PURE__ */ React.createElement(Stack, { gap: 16 }, /* @__PURE__ */ React.createElement(Card, null, /* @__PURE__ */ React.createElement(CardHeader, { trailing: /* @__PURE__ */ React.createElement(Pill, { size: "sm", active: true }, "\u8FB9\u754C") }, "Eino \u8D1F\u8D23\u4EC0\u4E48"), /* @__PURE__ */ React.createElement(CardBody, null, /* @__PURE__ */ React.createElement(Stack, { gap: 8 }, /* @__PURE__ */ React.createElement(Text, { size: "small" }, "\u5BF9\u8BDD\u7406\u89E3\u548C\u6709\u9650\u5DE5\u5177\u8DEF\u7531"), /* @__PURE__ */ React.createElement(Text, { size: "small" }, "\u786E\u5B9A\u6027 Workflow / Graph"), /* @__PURE__ */ React.createElement(Text, { size: "small" }, "Tool Schema\u3001Callback\u3001\u77ED\u671F\u4E2D\u65AD\u6062\u590D")))), /* @__PURE__ */ React.createElement(H3, null, "Eino \u4E0D\u8D1F\u8D23"), /* @__PURE__ */ React.createElement(Stack, { gap: 7 }, /* @__PURE__ */ React.createElement(Text, { tone: "secondary", size: "small" }, "\u4F01\u4E1A\u6743\u9650\u548C\u4E91\u51ED\u8BC1"), /* @__PURE__ */ React.createElement(Text, { tone: "secondary", size: "small" }, "\u98DE\u4E66\u6D88\u606F\u53EF\u9760\u6295\u9012"), /* @__PURE__ */ React.createElement(Text, { tone: "secondary", size: "small" }, "\u957F\u671F\u8C03\u67E5\u72B6\u6001\u548C\u8BC1\u636E"), /* @__PURE__ */ React.createElement(Text, { tone: "secondary", size: "small" }, "\u67E5\u8BE2\u9884\u7B97\u3001\u8131\u654F\u4E0E exactly-once")), /* @__PURE__ */ React.createElement(Divider, null), /* @__PURE__ */ React.createElement(Callout, { tone: "warning", title: "\u9996\u7248\u7EA6\u675F" }, "\u4E0D\u4E0A Multi-Agent\u3001\u4E0D\u6267\u884C\u81EA\u7531 SQL/SPL\u3001\u4E0D\u505A\u81EA\u52A8\u751F\u4EA7\u5199\u64CD\u4F5C\u3001\u4E0D\u628A\u539F\u59CB\u65E5\u5FD7\u6279\u91CF\u9001\u5165\u6A21\u578B\u3002"))));
}
function EinoView() {
  return /* @__PURE__ */ React.createElement(Stack, { gap: 18 }, /* @__PURE__ */ React.createElement(Callout, { tone: "success", title: "\u9009\u578B\u7ED3\u8BBA\uFF1A\u5EFA\u8BAE\u91C7\u7528" }, "\u56FA\u5B9A ", /* @__PURE__ */ React.createElement(Code, null, "v0.9.14"), "\uFF0C\u628A Eino \u9694\u79BB\u5728 ", /* @__PURE__ */ React.createElement(Code, null, "internal/orchestration/eino"), "\uFF0C\u4E1A\u52A1\u5C42\u53EA\u4F9D\u8D56\u81EA\u6709\u7A84\u63A5\u53E3\u3002"), /* @__PURE__ */ React.createElement(
    Table,
    {
      headers: ["\u91C7\u7528\u9879", "\u7528\u9014", "\u751F\u4EA7\u7EA6\u675F"],
      rows: [
        ["ADK ChatModelAgent + Runner", "\u610F\u56FE\u7406\u89E3\u3001\u6709\u9650\u5DE5\u5177\u9009\u62E9\u3001\u4E8B\u4EF6\u6D41", "\u53EA\u5F00\u653E\u53D7\u63A7 Graph Tool"],
        ["compose Workflow / Graph", "\u56FA\u5B9A\u8C03\u67E5\u6B65\u9AA4\u3001\u5206\u652F\u548C\u53CD\u8BC1\u5FAA\u73AF", "\u8282\u70B9\u5FC5\u987B\u53EF\u6D4B\u8BD5\u3001\u53EF\u91CD\u653E"],
        ["Typed Tool / GraphTool", "SLS\u3001\u53D1\u5E03\u3001Trace \u7B49\u80FD\u529B", "\u53C2\u6570\u5148\u8FC7 Schema\u3001ACL \u548C\u9884\u7B97"],
        ["Callback", "\u6A21\u578B\u3001Tool\u3001Graph \u7684\u89C2\u6D4B", "\u8F6C\u6362\u4E3A\u81EA\u6709\u7A33\u5B9A\u4E1A\u52A1\u4E8B\u4EF6"],
        ["Interrupt / Checkpoint", "\u8865\u5145\u8F93\u5165\u3001\u540E\u7EED\u5BA1\u6279\u3001\u77ED\u671F\u6062\u590D", "\u4E0D\u80FD\u66FF\u4EE3 Investigation \u6570\u636E\u5E93"]
      ],
      rowTone: ["success", "success", "success", "info", "warning"],
      striped: true
    }
  ), /* @__PURE__ */ React.createElement(Grid, { columns: 2, gap: 18 }, /* @__PURE__ */ React.createElement(Card, null, /* @__PURE__ */ React.createElement(CardHeader, null, "\u91C7\u7528\u7406\u7531"), /* @__PURE__ */ React.createElement(CardBody, null, /* @__PURE__ */ React.createElement(Stack, { gap: 8 }, /* @__PURE__ */ React.createElement(Text, { size: "small" }, "Go \u539F\u751F\u3001\u7C7B\u578B\u5316\u7EC4\u4EF6\u548C\u5DE5\u5177\u5951\u7EA6"), /* @__PURE__ */ React.createElement(Text, { size: "small" }, "Agent \u4E0E\u786E\u5B9A\u6027 Graph \u80FD\u81EA\u7136\u7EC4\u5408"), /* @__PURE__ */ React.createElement(Text, { size: "small" }, "\u6D41\u5F0F\u3001Callback\u3001HITL \u548C MCP \u751F\u6001\u53EF\u590D\u7528")))), /* @__PURE__ */ React.createElement(Card, null, /* @__PURE__ */ React.createElement(CardHeader, { trailing: /* @__PURE__ */ React.createElement(Pill, { size: "sm" }, "0.x") }, "\u98CE\u9669\u63A7\u5236"), /* @__PURE__ */ React.createElement(CardBody, null, /* @__PURE__ */ React.createElement(Stack, { gap: 8 }, /* @__PURE__ */ React.createElement(Text, { size: "small" }, "\u9501\u5B9A\u7CBE\u786E\u7248\u672C\uFF0C\u4E0D\u7528 v0.10 alpha"), /* @__PURE__ */ React.createElement(Text, { size: "small" }, "Eino \u7C7B\u578B\u4E0D\u8FDB\u5165\u9886\u57DF\u548C\u6301\u4E45\u5316\u534F\u8BAE"), /* @__PURE__ */ React.createElement(Text, { size: "small" }, "\u5347\u7EA7\u524D\u8DD1\u5386\u53F2\u56DE\u653E\u3001Tool \u5951\u7EA6\u548C\u6062\u590D\u6D4B\u8BD5"))))), /* @__PURE__ */ React.createElement(Text, { tone: "secondary", size: "small" }, "\u5B98\u65B9\u4F9D\u636E\uFF1A", /* @__PURE__ */ React.createElement(Link, { href: "https://www.cloudwego.io/docs/eino/overview/graph_or_agent/" }, "Agent or Graph"), " \xB7 ", /* @__PURE__ */ React.createElement(Link, { href: "https://github.com/cloudwego/eino/releases" }, "Eino Releases")));
}
function MilestonesView() {
  return /* @__PURE__ */ React.createElement(Stack, { gap: 18 }, /* @__PURE__ */ React.createElement(Callout, { tone: "info", title: "\u6E10\u8FDB\u4EA4\u4ED8" }, "\u6309 2 \u540D Go \u540E\u7AEF\u3001\u5E73\u53F0\u4E0E\u6D4B\u8BD5\u517C\u804C\u4F30\u7B97\u7EA6 8\u201312 \u5468\u3002\u6BCF\u9636\u6BB5\u72EC\u7ACB\u9A8C\u6536\uFF1B\u5EFA\u8BAE M2 \u5B8C\u6210\u540E\u5148\u5C0F\u8303\u56F4\u8BD5\u7528\uFF0C\u518D\u51B3\u5B9A\u6269\u5C55\u3002"), /* @__PURE__ */ React.createElement(
    Table,
    {
      headers: ["\u9636\u6BB5", "\u76EE\u6807", "\u5468\u671F", "\u6838\u5FC3\u4EA4\u4ED8", "\u72B6\u6001"],
      rows: milestones,
      rowTone: ["info", void 0, void 0, void 0, void 0, void 0],
      striped: true
    }
  ), /* @__PURE__ */ React.createElement(Grid, { columns: 3, gap: 16 }, /* @__PURE__ */ React.createElement(Card, null, /* @__PURE__ */ React.createElement(CardHeader, null, "M0 \u51B3\u7B56\u95E8"), /* @__PURE__ */ React.createElement(CardBody, null, /* @__PURE__ */ React.createElement(Text, { size: "small" }, "\u786E\u8BA4 Eino \u53EF\u66FF\u6362\u8FB9\u754C\uFF0C\u5E76\u7528\u540C\u4E00\u6279\u7528\u4F8B\u6BD4\u8F83 SLS SDK \u4E0E\u5B98\u65B9 MCP\u3002"))), /* @__PURE__ */ React.createElement(Card, null, /* @__PURE__ */ React.createElement(CardHeader, null, "M2 \u4EF7\u503C\u95E8"), /* @__PURE__ */ React.createElement(CardBody, null, /* @__PURE__ */ React.createElement(Text, { size: "small" }, "\u53EA\u4EA4\u4ED8\u201C\u9519\u8BEF\u7A81\u589E\u8C03\u67E5\u201D\u95ED\u73AF\uFF0C\u771F\u5B9E\u7528\u6237\u8BD5\u7528\u540E\u518D\u7EE7\u7EED\u6295\u8D44\u3002"))), /* @__PURE__ */ React.createElement(Card, null, /* @__PURE__ */ React.createElement(CardHeader, null, "M5 \u4E0A\u7EBF\u95E8"), /* @__PURE__ */ React.createElement(CardBody, null, /* @__PURE__ */ React.createElement(Text, { size: "small" }, "\u8D8A\u6743\u4E3A\u96F6\uFF0C\u5E76\u6709\u6839\u56E0\u547D\u4E2D\u7387\u3001\u8017\u65F6\u3001\u6210\u672C\u548C\u7528\u6237\u53CD\u9988\u57FA\u7EBF\u3002")))), /* @__PURE__ */ React.createElement(H3, null, "\u4EAE\u70B9\u80FD\u529B\u7684\u63A8\u8350\u987A\u5E8F"), /* @__PURE__ */ React.createElement(Text, { tone: "secondary" }, "\u53D1\u5E03\u4E0E\u914D\u7F6E\u65F6\u95F4\u7EBF \u2192 Trace \u4E0A\u4E0B\u6587 \u2192 \u8DE8\u670D\u52A1\u4F20\u64AD\u94FE \u2192 \u76F8\u4F3C\u4E8B\u6545\u4E0E Runbook \u2192 SLS \u544A\u8B66\u9884\u8C03\u67E5 \u2192 \u65E5\u5FD7\u8D28\u91CF\u533B\u751F \u2192 \u7ECF\u5BA1\u6279\u7684\u4F4E\u98CE\u9669\u5904\u7F6E\u3002"));
}
function SLSLogAgentArchitecture() {
  const [view, setView] = useCanvasState("selected-view", "architecture");
  const dispatch = useCanvasAction();
  return /* @__PURE__ */ React.createElement(Stack, { gap: 22, style: { maxWidth: 1180, margin: "0 auto", padding: "28px 24px 44px" } }, /* @__PURE__ */ React.createElement(Stack, { gap: 8 }, /* @__PURE__ */ React.createElement(H1, null, "\u65E5\u5FD7 Agent\uFF1AGo + \u98DE\u4E66 + Eino \u6574\u4F53\u65B9\u6848"), /* @__PURE__ */ React.createElement(Text, { tone: "secondary" }, "v0.1 \xB7 \u9996\u4E2A\u573A\u666F\u805A\u7126\u751F\u4EA7\u670D\u52A1\u9519\u8BEF\u7A81\u589E \xB7 2026-08-18")), /* @__PURE__ */ React.createElement(Row, { gap: 8, wrap: true }, /* @__PURE__ */ React.createElement(Pill, { active: view === "architecture", onClick: () => setView("architecture") }, "\u67B6\u6784\u603B\u89C8"), /* @__PURE__ */ React.createElement(Pill, { active: view === "eino", onClick: () => setView("eino") }, "Eino \u51B3\u7B56"), /* @__PURE__ */ React.createElement(Pill, { active: view === "milestones", onClick: () => setView("milestones") }, "\u91CC\u7A0B\u7891")), view === "architecture" && /* @__PURE__ */ React.createElement(ArchitectureView, null), view === "eino" && /* @__PURE__ */ React.createElement(EinoView, null), view === "milestones" && /* @__PURE__ */ React.createElement(MilestonesView, null), /* @__PURE__ */ React.createElement(Divider, null), /* @__PURE__ */ React.createElement(Row, { justify: "space-between", align: "center", gap: 16, wrap: true }, /* @__PURE__ */ React.createElement(Text, { tone: "tertiary", size: "small" }, "\u4E3B\u89C4\u683C\u5305\u542B\u6A21\u5757\u804C\u8D23\u3001\u53EF\u9760\u6027\u3001\u5B89\u5168\u3001\u6570\u636E\u6A21\u578B\u548C\u9010\u9636\u6BB5\u9A8C\u6536\u6761\u4EF6\u3002"), /* @__PURE__ */ React.createElement(
    Button,
    {
      variant: "primary",
      onClick: () => dispatch({ type: "newComposerChat", userPrompt: "\u8BF7\u57FA\u4E8E\u8FD9\u4EFD\u65E5\u5FD7 Agent \u67B6\u6784\u65B9\u6848\u7EE7\u7EED\u8BC4\u5BA1\uFF0C\u5E76\u5E2E\u6211\u786E\u5B9A M0 \u7684\u5177\u4F53\u4EFB\u52A1\u3002" })
    },
    "\u7EE7\u7EED\u8BC4\u5BA1"
  )));
}
export {
  SLSLogAgentArchitecture as default
};

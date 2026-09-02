package localweb

import "strings"

func pageHTML(csrf string) string {
	return strings.ReplaceAll(pageTemplate, "__CSRF_TOKEN__", csrf)
}

const pageTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="log-agent-csrf" content="__CSRF_TOKEN__">
  <title>日志 Agent 本地排障台</title>
  <link rel="stylesheet" href="/app.css">
</head>
<body>
  <main class="shell">
    <header class="hero">
      <div>
        <p class="eyebrow">LOCAL PILOT CONSOLE</p>
        <h1>日志 Agent 本地排障台</h1>
        <p class="subtitle">用本地网页替代飞书交互层，验证同一套 Agent 内核、真实或 Mock SLS 与 LLM 链路。</p>
      </div>
      <div id="mode-badges" class="badges" aria-label="运行模式"></div>
    </header>

    <section class="notice" id="boundary">正在读取运行边界…</section>

    <section class="grid">
      <form id="investigation-form" class="panel form-panel">
        <div class="panel-heading">
          <div><span class="step">01</span><h2>提交调查</h2></div>
          <span class="hint">身份由服务端固定</span>
        </div>
        <div id="intent-panel" hidden>
          <label>Bug 描述<textarea id="problem" maxlength="500" rows="5" placeholder="例如：帮我看 DAM 测试环境最近半小时错误有没有增加"></textarea></label>
          <button class="secondary" type="button" id="parse-button">解析问题</button>
          <section id="intent-preview" class="intent-preview" hidden>
            <p class="preview-label">解析预览（尚未查询日志）</p>
            <dl class="scope"><div><dt>状态 / 意图</dt><dd id="intent-status"></dd></div><div><dt>置信度</dt><dd id="intent-confidence"></dd></div><div><dt>范围</dt><dd id="intent-scope"></dd></div><div><dt>窗口 / 模板</dt><dd id="intent-window"></dd></div></dl>
            <p id="intent-notice" class="state-notice"></p>
            <div class="preview-actions"><button class="primary" type="button" id="confirm-button">确认并调查</button><button type="button" id="edit-button">填入下方表单修正</button></div>
          </section>
          <div class="or"><span>或使用严格结构化表单</span></div>
        </div>
        <div id="structured-fields">
          <label>服务<input id="service" name="service" value="" placeholder="dam-server" maxlength="64" required></label>
          <label>环境<input id="environment" name="environment" value="" placeholder="test" maxlength="64" required></label>
          <div class="two-col">
            <label>时间窗口<input id="duration" name="duration" value="30m" maxlength="16" required></label>
            <label>查询模板
              <select id="template" name="template">
                <option value="error_count_v1">error_count_v1</option>
                <option value="error_analysis_v2">error_analysis_v2</option>
              </select>
            </label>
          </div>
          <button class="primary" type="submit" id="submit-button">按结构化范围开始调查</button>
        </div>
        <p id="form-error" class="error" role="alert"></p>
      </form>

      <section class="panel status-panel" aria-live="polite">
        <div class="panel-heading">
          <div><span class="step">02</span><h2>调查状态</h2></div>
          <span id="updated-at" class="hint">尚未提交</span>
        </div>
        <div id="empty-state" class="empty">提交后，这里会自动刷新 SQLite 中的状态和本地投递投影。</div>
        <div id="status-content" hidden>
          <div class="status-line"><span id="status-pill" class="status">QUEUED</span><code id="investigation-id"></code></div>
          <dl class="scope"><div><dt>范围</dt><dd id="scope"></dd></div><div><dt>窗口</dt><dd id="window"></dd></div></dl>
          <p id="user-problem" class="state-notice" hidden></p>
          <p id="state-notice" class="state-notice"></p>
          <div id="actions" class="actions"></div>
        </div>
      </section>
    </section>

    <section id="report-panel" class="panel report-panel" hidden>
      <div class="panel-heading"><div><span class="step">03</span><h2>报告与证据</h2></div><span id="outcome" class="outcome"></span></div>
      <section id="summary-section" hidden><h3>AI 证据摘要</h3><p id="summary-text" class="lead"></p><p id="summary-meta" class="meta"></p></section>
      <section><h3>确定性发现</h3><div id="findings" class="stack"></div></section>
      <section><h3>建议</h3><div id="recommendations" class="stack"></div></section>
      <section><h3>受治理 Evidence</h3><div id="evidence" class="evidence-grid"></div></section>
      <section id="context-section" hidden><h3>增强上下文</h3><div id="context" class="stack"></div></section>
    </section>
  </main>
  <script src="/app.js" defer></script>
</body>
</html>`

const appCSS = `:root{color-scheme:dark;--bg:#07110f;--panel:#0d1b18;--panel2:#10231f;--line:#244039;--text:#edf8f3;--muted:#9ab4aa;--lime:#c9f56a;--cyan:#58dbc7;--orange:#ffb66e;--red:#ff7d7d}*{box-sizing:border-box}body{margin:0;background:radial-gradient(circle at 85% 0,#153c34 0,transparent 32rem),linear-gradient(160deg,#07110f,#0a1714 55%,#07110f);color:var(--text);font:15px/1.6 Inter,"Segoe UI",sans-serif;min-height:100vh}.shell{width:min(1180px,calc(100% - 32px));margin:0 auto;padding:54px 0 80px}.hero{display:flex;justify-content:space-between;gap:30px;align-items:flex-start;margin-bottom:24px}.eyebrow{color:var(--lime);font:700 12px/1.2 ui-monospace,monospace;letter-spacing:.18em;margin:0 0 14px}.hero h1{font-size:clamp(32px,5vw,58px);letter-spacing:-.045em;line-height:1;margin:0}.subtitle{color:var(--muted);max-width:730px;font-size:17px}.badges{display:flex;gap:8px;flex-wrap:wrap;justify-content:flex-end}.badge,.status,.outcome{border:1px solid var(--line);border-radius:999px;padding:6px 10px;font:700 12px/1 ui-monospace,monospace;white-space:nowrap}.badge strong{color:var(--cyan)}.notice{border-left:3px solid var(--orange);background:#211b12;color:#f9dcb9;padding:14px 18px;border-radius:0 10px 10px 0;margin:22px 0 26px}.grid{display:grid;grid-template-columns:minmax(300px,.85fr) minmax(360px,1.15fr);gap:18px}.panel{border:1px solid var(--line);border-radius:18px;background:linear-gradient(145deg,rgba(16,35,31,.96),rgba(11,25,22,.96));box-shadow:0 20px 60px rgba(0,0,0,.2);padding:24px}.panel-heading{display:flex;justify-content:space-between;gap:12px;align-items:center;margin-bottom:22px}.panel-heading>div{display:flex;align-items:center;gap:10px}.panel h2{font-size:19px;margin:0}.step{color:var(--lime);font:700 12px ui-monospace,monospace}.hint,.meta{color:var(--muted);font-size:12px}label{display:grid;gap:7px;color:var(--muted);margin-bottom:16px;font-weight:600}input,select,textarea{width:100%;border:1px solid var(--line);border-radius:10px;background:#071310;color:var(--text);padding:12px 13px;font:inherit;outline:none}textarea{resize:vertical;min-height:112px}input:focus,select:focus,textarea:focus{border-color:var(--cyan);box-shadow:0 0 0 3px rgba(88,219,199,.12)}.two-col{display:grid;grid-template-columns:1fr 1fr;gap:12px}button{border:0;border-radius:10px;padding:11px 14px;font:700 13px/1.2 inherit;cursor:pointer}.primary{width:100%;background:var(--lime);color:#10200b}.secondary{width:100%;background:#17372f;color:var(--text);border:1px solid #285348}.primary:disabled,button:disabled{opacity:.5;cursor:not-allowed}.intent-preview{margin:14px 0 18px;padding:14px;border:1px solid var(--line);border-radius:12px;background:#081713}.preview-label{margin:0;color:var(--cyan);font-weight:700}.preview-actions{display:grid;grid-template-columns:1fr 1fr;gap:8px}.preview-actions button:last-child{background:#17372f;color:var(--text)}.or{display:flex;align-items:center;gap:10px;color:var(--muted);font-size:12px;margin:20px 0}.or:before,.or:after{content:"";height:1px;background:var(--line);flex:1}.error{min-height:1.5em;color:var(--red);margin:12px 0 0}.empty{min-height:184px;display:grid;place-items:center;text-align:center;color:var(--muted);border:1px dashed var(--line);border-radius:12px;padding:30px}.status-line{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.status{color:var(--lime)}code{color:var(--muted);word-break:break-all}.scope{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin:18px 0}.scope div{background:#071310;border-radius:10px;padding:11px}.scope dt{font-size:11px;color:var(--muted);text-transform:uppercase}.scope dd{margin:3px 0 0}.state-notice{color:#f6d3aa}.actions{display:flex;gap:8px;flex-wrap:wrap}.actions button{background:#17372f;color:var(--text);border:1px solid #285348}.actions button.danger{border-color:#694444;background:#2d1d1d}.report-panel{margin-top:18px}.report-panel section+section{border-top:1px solid var(--line);margin-top:24px;padding-top:20px}.report-panel h3{font-size:13px;text-transform:uppercase;letter-spacing:.1em;color:var(--muted)}.lead{font-size:20px;line-height:1.5;max-width:900px}.stack{display:grid;gap:10px}.item,.evidence-card{background:#081713;border:1px solid #1e3c35;border-radius:12px;padding:14px}.item strong,.evidence-card strong{color:var(--cyan)}.evidence-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:12px}.evidence-card dl{display:grid;grid-template-columns:1fr 1fr;gap:7px;margin:10px 0 0}.evidence-card dt{color:var(--muted);font-size:11px}.evidence-card dd{margin:0}.outcome{color:var(--cyan)}@media(max-width:780px){.shell{width:min(100% - 20px,1180px);padding-top:28px}.hero{display:block}.badges{justify-content:flex-start}.grid{grid-template-columns:1fr}.two-col,.scope,.preview-actions{grid-template-columns:1fr}.panel{padding:18px}}`

const appJS = `(function(){
"use strict";
var csrf=document.querySelector('meta[name="log-agent-csrf"]').content;
var currentID="";var currentResolution=null;var timer=null;
var labels={view_report:"查看报告",view_evidence:"查看证据",cancel:"取消调查",expand_window:"扩大窗口",rerun:"重新运行",rerun_with_cost_ack:"确认成本并重跑"};
function el(id){return document.getElementById(id)}
function text(node,value){node.textContent=value==null?"":String(value)}
function requestID(){return crypto.randomUUID?crypto.randomUUID():String(Date.now())+"-"+Math.random().toString(16).slice(2)}
async function api(path,options){var response=await fetch(path,options||{});var body=await response.json();if(!response.ok){throw new Error(body.message||body.code||"请求失败")}return body}
function post(path,body){return api(path,{method:"POST",headers:{"Content-Type":"application/json","X-Log-Agent-CSRF":csrf},body:JSON.stringify(body)})}
function durationText(seconds){if(!seconds)return"-";if(seconds%3600===0)return(seconds/3600)+"h";if(seconds%60===0)return(seconds/60)+"m";return seconds+"s"}
function renderIntent(resolution){currentResolution=resolution;el("intent-preview").hidden=false;text(el("intent-status"),resolution.status+" / "+(resolution.intent||"unknown"));text(el("intent-confidence"),Math.round((resolution.confidence||0)*100)+"%");text(el("intent-scope"),(resolution.service||"-")+" / "+(resolution.environment||"-"));text(el("intent-window"),durationText(resolution.duration_seconds)+" / "+(resolution.template_id||"-"));var safe=resolution.status==="RESOLVED"&&resolution.intent==="error_spike";el("confirm-button").disabled=!safe;text(el("intent-notice"),safe?"这是确认预览，当前尚未访问 SLS。点击确认后才会创建调查。":"当前解析结果不能启动调查（"+(resolution.reason_code||"unsupported")+"），请使用下方结构化表单修正。");}
async function parseIntent(){var button=el("parse-button");button.disabled=true;text(el("form-error"),"");try{var result=await post("/api/intents",{request_id:requestID(),problem:el("problem").value.trim()});renderIntent(result.resolution)}catch(error){text(el("form-error"),error.message)}finally{button.disabled=false}}
async function confirmIntent(){if(!currentResolution)return;var button=el("confirm-button");button.disabled=true;text(el("form-error"),"");try{var result=await post("/api/intents/"+encodeURIComponent(currentResolution.id)+"/confirm",{request_id:requestID()});render(result.investigation)}catch(error){text(el("form-error"),error.message);button.disabled=false}}
function editIntent(){if(!currentResolution)return;if(currentResolution.service)el("service").value=currentResolution.service;if(currentResolution.environment)el("environment").value=currentResolution.environment;if(currentResolution.duration_seconds)el("duration").value=durationText(currentResolution.duration_seconds);if(currentResolution.template_id)el("template").value=currentResolution.template_id;el("service").focus()}
function addItem(parent,title,body){var box=document.createElement("div");box.className="item";var strong=document.createElement("strong");text(strong,title);var p=document.createElement("p");text(p,body);box.append(strong,p);parent.appendChild(box)}
function formatTime(value){if(!value)return "-";return new Date(value).toLocaleString("zh-CN",{hour12:false})}
function render(view){currentID=view.id;el("empty-state").hidden=true;el("status-content").hidden=false;text(el("investigation-id"),view.id);text(el("status-pill"),view.status);text(el("updated-at"),"更新于 "+formatTime(view.updated_at));text(el("scope"),view.scope.service+" / "+view.scope.environment+" / "+view.scope.template_id);text(el("window"),formatTime(view.scope.start_time)+" → "+formatTime(view.scope.end_time));el("user-problem").hidden=!view.problem;text(el("user-problem"),view.problem?"用户描述（未验证）："+view.problem:"");text(el("state-notice"),view.notice||"");renderActions(view.actions||[]);renderReport(view.report);if(["QUEUED","RUNNING"].indexOf(view.status)>=0){schedule()}else{clearTimeout(timer)}}
function renderActions(actions){var root=el("actions");root.replaceChildren();actions.forEach(function(action){if(action==="view_report")return;var button=document.createElement("button");button.type="button";button.dataset.action=action;text(button,labels[action]||action);if(action==="cancel")button.className="danger";button.addEventListener("click",function(){runAction(action,button)});root.appendChild(button)})}
function renderReport(report){var panel=el("report-panel");if(!report){panel.hidden=true;return}panel.hidden=false;text(el("outcome"),report.outcome);var summary=report.summary;el("summary-section").hidden=!summary;if(summary){text(el("summary-text"),summary.phenomenon);text(el("summary-meta"),summary.mode+" · "+summary.provider+(summary.model?" · "+summary.model:"")+" · "+summary.total_tokens+" tokens · "+summary.latency_millis+" ms")}var findings=el("findings");findings.replaceChildren();(report.findings||[]).forEach(function(item){addItem(findings,item.code,item.statement+"（置信度 "+Math.round(item.confidence*100)+"%）")});if(!findings.children.length)addItem(findings,"无确定性发现","当前证据没有形成确定性发现。");var recommendations=el("recommendations");recommendations.replaceChildren();(report.recommendations||[]).forEach(function(item){addItem(recommendations,item.code,item.statement)});if(!recommendations.children.length)addItem(recommendations,"暂无建议","当前报告没有确定性建议。");var evidence=el("evidence");evidence.replaceChildren();(report.evidence||[]).forEach(function(item){var card=document.createElement("article");card.className="evidence-card";var title=document.createElement("strong");text(title,item.name+" · "+(item.complete&&!item.truncated?"完整":"不完整"));var dl=document.createElement("dl");[["错误数",item.error_count],["Top 错误",item.top_error||"不适用"],["API 调用",item.api_calls],["扫描字节",item.processed_bytes],["耗时",item.elapsed_millisecond+" ms"],["窗口",formatTime(item.start_time)+" → "+formatTime(item.end_time)]].forEach(function(pair){var wrap=document.createElement("div");var dt=document.createElement("dt");var dd=document.createElement("dd");text(dt,pair[0]);text(dd,pair[1]);wrap.append(dt,dd);dl.appendChild(wrap)});card.append(title,dl);evidence.appendChild(card)});var context=el("context");context.replaceChildren();if(report.cause_status)addItem(context,"原因分析",report.cause_status+"；候选数 "+(report.cause_hypotheses||[]).length);if(report.timeline_status)addItem(context,"跨信号时间线",report.timeline_status+"；条目数 "+(report.timeline_items||[]).length);if(report.runbook_guidance)addItem(context,"SOP 人工核查",report.runbook_guidance.status+"；仅供人工核查，不会自动执行处置。");el("context-section").hidden=!context.children.length}
function schedule(){clearTimeout(timer);timer=setTimeout(refresh,1000)}
async function refresh(){if(!currentID)return;try{render(await api("/api/investigations/"+encodeURIComponent(currentID)))}catch(error){text(el("state-notice"),error.message);schedule()}}
async function runAction(action,button){button.disabled=true;try{var result=await post("/api/investigations/"+encodeURIComponent(currentID)+"/actions",{request_id:requestID(),action:action});render(result.investigation);if(result.view==="EVIDENCE")el("evidence").scrollIntoView({behavior:"smooth",block:"start"});if(result.view==="REPORT")el("report-panel").scrollIntoView({behavior:"smooth",block:"start"})}catch(error){text(el("state-notice"),error.message)}finally{button.disabled=false}}
el("investigation-form").addEventListener("submit",async function(event){event.preventDefault();var button=el("submit-button");button.disabled=true;text(el("form-error"),"");try{var result=await post("/api/investigations",{request_id:requestID(),service:el("service").value.trim(),environment:el("environment").value.trim(),duration:el("duration").value.trim(),template_id:el("template").value});render(result.investigation)}catch(error){text(el("form-error"),error.message)}finally{button.disabled=false}});
el("parse-button").addEventListener("click",parseIntent);el("confirm-button").addEventListener("click",confirmIntent);el("edit-button").addEventListener("click",editIntent);
api("/api/meta").then(function(meta){var badges=el("mode-badges");[["SLS",meta.sls_mode],["LLM",meta.llm_mode],["Intent",meta.intent_mode],["飞书",meta.feishu_mode]].forEach(function(pair){var badge=document.createElement("span");badge.className="badge";var strong=document.createElement("strong");text(strong,pair[0]+" ");badge.append(strong,document.createTextNode(pair[1]));badges.appendChild(badge)});text(el("boundary"),meta.warning);var enabled=meta.intent_mode&&meta.intent_mode!=="disabled";el("intent-panel").hidden=!enabled;if(enabled){api("/api/capabilities").then(function(result){var first=(result.capabilities||[])[0];if(first){el("service").value=first.service;el("environment").value=first.environment;el("template").value=first.template_id}}).catch(function(error){text(el("form-error"),error.message)})}}).catch(function(error){text(el("boundary"),error.message)});
})();`

# Evidence-driven Log Agent Specification

| Metadata | Value |
| --- | --- |
| Version | 1.9 |
| Status | Governed natural-language intake Stage 1 is implemented and offline-validated; only explicit confirmation can create an existing count-only investigation. Multi-Logstore Trace, code evidence, production intent quality, real knowledge/metric connectors, M4-C infrastructure, and real gray rollout remain pending |
| Date | 2026-09-02 |

## 1. Overview

The product is a Go service that receives investigation requests from a Feishu enterprise self-built bot and produces evidence-backed findings from Alibaba Cloud SLS data.

Eino is an orchestration adapter, not the business system of record. Investigation state, evidence, authorization, query policy, idempotency, and audit semantics remain owned by the application.

## 2. Goal

Users can either submit the strict investigation command or describe a suspected error spike in natural language. Natural language is parsed only into a logical, ACL-filtered preview; the system creates an investigation only after explicit confirmation. It then resolves that logical scope to an administrator-managed SLS resource, applies authorization and query budgets before any cloud request, and produces a report whose facts reference explicit evidence.

## 3. Scope

- Feishu direct messages and group mentions through a replaceable inbound adapter.
- A governed natural-language intake path for the closed `error_spike` intent. It exposes only principal-authorized logical service/environment/template capabilities, persists a redacted resolution before confirmation, and never accepts physical SLS coordinates or raw query text.
- A two-step `resolve -> confirm` contract. Parsing or previewing never creates an investigation or issues an SLS request; confirmation rechecks identity, ACL, expiry, status, and template binding before reusing the existing durable Intake transaction.
- An independent intent-parser request/Token quota ledger and a guarded Volcengine Ark parser adapter. Intent parsing is isolated from the report summarizer and has its own model, prompt, timeout, input/output and cost controls.
- A credential-free local Feishu mock that exercises normalized intake and durable delivery semantics without importing the Feishu SDK.
- Durable inbound deduplication and asynchronously claimed investigation jobs.
- A trusted requester identity derived from the inbound adapter, never from message text.
- An Eino graph for deterministic planning, query, verification, and reporting.
- An administrator-managed service/environment to SLS resource catalog.
- Default-deny principal-to-resource authorization.
- Two fixed, versioned query templates: dimensional `error_analysis_v2` and count-only `error_count_v1`; callers cannot provide raw SQL or SPL.
- Preflight time-window, call-count, row-count, timeout, and concurrency budgets.
- A post-query processed-byte budget used as the initial cost guardrail.
- Index Schema validation before executing analytical queries.
- An Alibaba Cloud CLI/SLS-plugin adapter that uses an operator-selected local `StsToken` Profile and only read-only APIs.
- Evidence carrying resource identity, query identity, time range, completeness, scan statistics, and result summary.
- Current-versus-baseline error-pattern share, candidate-new-pattern, and instance-concentration analysis.
- A versioned, administrator-managed change catalog for bounded release/configuration context.
- A deterministic cause-analysis projection with explicit support tests, counter-tests, confidence factors, and limitations.
- An optional Mock-first incident timeline that combines governed change references with bounded metric and Trace aggregate observations derived from the same Evidence resource and time range.
- Optional governed SOP guidance that maps validated deterministic Recommendations to bounded human-review-only steps without changing facts or conclusions.
- Feishu acknowledgement, progress, terminal-report cards, and requester-authorized card actions.
- A minimal durable delivery queue so the Feishu and worker processes can exchange card updates without sharing memory.
- Append-only query audit events for denied, started, succeeded, incomplete, and failed attempts.
- In-flight cancellation, renewable leases, lease-safe state transitions, and structured output.
- Durable `sls.current` and `sls.baseline` checkpoints for normalized aggregate results.
- Fail-closed recovery when a metered SLS read has an unknown external outcome.
- A versioned synthetic golden evaluation set that runs the real deterministic graph over fixture-backed Mock SLS and Mock change data.
- Offline quality gates for expected outcome, exact Finding and Recommendation labels, conclusive-finding safety, the same production Worker output validator, QuerySpec-to-Evidence binding, evidence-reference coverage, cause-verdict agreement, fixed query budget, and processed-byte cost proxy.
- A framework-neutral, privacy-bounded Agent event contract for synthetic Engine runs, fixed Graph nodes, and Mock tool calls.
- A normalized runtime-version manifest and fingerprint that distinguishes dataset, Graph, policy, cause method, trace schema, replay schema, executor profile, and actual Prompt/model usage.
- Append-only offline evaluation-run history plus a strict, read-only comparison of compatible replay snapshots. Comparison never executes the Graph or an external provider.
- A mock-first rollout-readiness control plane that binds bounded reviewer feedback to exact immutable evaluation snapshots before producing a non-actionable rehearsal decision.
- An append-only feedback history with explicit correction lineage, reviewer quorum, closed verdict/reason codes, and deterministic rollout-policy evaluation.
- A required provider-neutral LLM summary stage that can use a Mock implementation offline and a guarded Volcengine Ark adapter in deployment. It may summarize only validated Findings, Evidence references, cause-analysis status, limitations, and deterministic recommendations.
- A SQLite technical-preview LLM quota ledger that atomically reserves one summary request plus a conservative Token allowance per trusted tenant before any provider call, then settles actual usage or retains an unknown-cost reservation.
- A provider-neutral delivery-failure taxonomy, bounded retry, append-only attempt audit, operator-visible dead letters, and transactionally guarded replay of the existing card queue.
- A SQLite technical-preview tenant quota ledger that reserves fixed query-call and processed-byte proxies before the governed executor and settles success, deterministic denial, or unknown external outcome without retrying paid reads.
- A closed high-risk approval state contract with immutable request hashes and one-time consumption; no high-risk tool is registered in the current read-only runtime.

## 4. Non-goals

- Arbitrary model-generated SQL or SPL.
- Treating a natural-language interpretation as permission to query. The model cannot create a job, choose a physical resource, bypass confirmation, or silently downgrade an unsupported Trace request to count-only analysis.
- Claiming support for free-form root-cause questions, TraceID lookup, eight-Logstore timelines, repository analysis, or code-based fixes from the Stage 1 intake implementation.
- User-selected Endpoint, Project, LogStore, field name, or unregistered query template. The command may request only a closed template ID already bound to the resolved operator-owned resource version.
- Multi-Agent orchestration, DeepAgent, or Supervisor patterns.
- SLS write operations, alert mutation, or automatic remediation.
- Token-by-token Feishu streaming cards or high-risk approval actions.
- Raw-log samples in Feishu or model context.
- Claiming that absence from a bounded Top-K result proves historical absence.
- Exactly-once notification delivery, blind dead-letter replay, or an unbounded delivery retry loop.
- Model-generated facts, confidence, authorization, queries, or root-cause verdicts. The required LLM stage may only summarize governed evidence and must fall back to the deterministic report when unavailable or invalid.
- A production database migration or a new organization-wide message queue.
- Exact RMB cost prediction; processed bytes are the first cost proxy.
- Cross-process global concurrency quotas; the first implementation limits each worker process.
- Treating the SQLite tenant quota ledger as an organization-wide or multi-region production quota service.
- Treating local LLM request/Token quotas as a Volcengine bill, a distributed global limit, or proof that a real model fits the approved cost envelope.
- Executing a high-risk tool merely because an approval record exists; production identity, tool registration, policy and executor integration remain M4-C inputs.
- Automatic retry of a paid query whose external outcome is unknown.
- Provider exactly-once query execution; SLS does not accept an application idempotency key.
- Treating a correlated release, configuration change, error pattern, or instance as a confirmed root cause.
- SLS version-distribution or first-seen-time queries in the first M3 slice; M3 reuses the existing M2 query budget.
- Live release-platform, configuration-center, CMDB, Trace, metric, error-code, SOP, or service-topology connectors.
- Raw spans, Trace IDs, span names, metric labels, arbitrary attributes, or model-generated causal statements in the Mock-first cross-signal timeline.
- Generated or automatically executed SOPs; arbitrary URLs, commands, scripts, write operations, or knowledge content supplied by the user or model.
- Claiming that synthetic fixtures are historical incidents, expert labels, production accuracy, or permission to start a real gray rollout.
- Real Feishu/SLS/change-platform traffic, credentials, model calls, Prompt quality, Token accounting, or production SLO validation in M5-A.
- Claiming that the first Agent Trace is a distributed Feishu-to-delivery production Trace; it covers only the synthetic evaluation and Engine boundary.
- Reusing query audit or query checkpoints as generic tracing storage, or allowing telemetry to change retry, recovery, authorization, or investigation state.
- Raw messages, identities, resources, SQL, log content, bucket labels, change summaries, natural-language findings/recommendations, callback payloads, provider errors, model inputs, or arbitrary attributes in Agent events.
- A live telemetry backend, production sampling/retention policy, host identity, real LLM/Token telemetry, historical-binary execution, or production latency SLO in M5-B.
- Treating synthetic reviewer fixtures or rehearsal decisions as real expert labels, production approval, permission to widen traffic, or proof that rollback works in a live system.
- Automatically changing Feishu availability, SLS resources, worker deployment, feature flags, traffic, or production state from a rollout-readiness decision.
- Free-form reviewer comments, raw report text, raw Evidence, identities from message text, credentials, or provider data in the feedback ledger.
- A real reviewer directory, production feedback UI, team-approved thresholds, deployment controller, or live rollback executor before M5-C/C3.

## 5. Core design and architecture

```text
Feishu Receiver
    -> Durable Inbox
    -> Investigation Job with trusted Principal
    -> Worker
    -> InvestigationEngine interface
    -> Eino deterministic graph
    -> CheckpointExecutor (sls.current / sls.baseline)
    -> QueryGateway
         -> Resource Catalog
         -> ACL + Query Budget
         -> Schema validation
         -> Query Audit
         -> Alibaba SLS Backend
    -> Evidence-backed report
         -> optional governed Change Catalog enrichment
         -> support/counter-evidence ledger
         -> optional governed metric/Trace aggregate timeline
         -> optional governed SOP guidance in the Worker post-processing boundary
    -> Durable notification queue
    -> Feishu delivery worker -> acknowledgement/progress/result card

Feishu card.action.trigger
    -> requester authorization
    -> view evidence | cancel | expand window | rerun | rerun_with_cost_ack
    -> durable state transition or derived investigation

Versioned synthetic evaluation dataset
    -> strict fixture and label validation
    -> fixture-backed Mock SLS + Mock Change Source
    -> real Eino deterministic graph
    -> outcome / finding / evidence / cause / budget checks
    -> structured evaluation report
    -> non-zero process exit when an engineering regression gate fails

Bounded Agent Observer
    -> one synthetic evaluation-run identity
    -> one Trace per evaluation Case
    -> RUN / GRAPH_NODE / TOOL start and terminal events
    -> stable failure codes, usage counters, duration, and version fingerprint only
    -> bounded in-memory Trace for B1
    -> append-only replay snapshot and strict trend comparison in B2/B3

M5-C rollout-readiness rehearsal
    -> strict base and candidate replay snapshots
    -> compatible B3 comparison
    -> append-only reviewer feedback bound to candidate hash and Case ID
    -> active-feedback resolution and reviewer quorum
    -> versioned rollout policy
    -> REHEARSAL_PASSED | REHEARSAL_BLOCKED
       | REHEARSAL_ROLLBACK_RECOMMENDED | REHEARSAL_INSUFFICIENT_EVIDENCE
    -> production_action_allowed=false

Required evidence-bound LLM summary
    -> validated deterministic Report only
    -> trusted app/tenant quota reservation
    -> provider-neutral ReportSummarizer port
    -> offline Mock or guarded Volcengine Ark adapter
    -> actual Token settlement or unknown-cost retention
    -> strict structured output and Evidence-ID validation
    -> AI-labelled Feishu summary or deterministic fallback
```

The M3 Change Source and the Mock-first Operational Signal Source are enrichment-only. They are called only after governed SLS evidence has established the resource identity and a conclusive spike. Source absence, failure, or invalid output cannot erase an M2 fact or fail the investigation; it produces an explicit unavailable or inconclusive enrichment status.

Governed SOP guidance runs later in the Worker, only after the deterministic Engine output has passed the shared production validator. It consumes governed Evidence identity and existing Recommendation codes, never changes the Engine report facts, and is validated again before the LLM summary. SOP content is excluded from `SummaryInput`.

### Framework boundary

Only the Eino orchestration adapter imports Eino packages. Only the Feishu adapter imports Feishu SDK packages. The Alibaba SLS adapter invokes one resolved `aliyun` executable directly, without a shell, and owns all CLI arguments, output parsing, process limits, and error sanitization. CLI output types cannot escape the adapter package.

### Identity boundary

`Principal` consists of the source app, tenant, and user identifiers. `Intake` overwrites any requester value carried by a caller with identity derived from `InboundMessage`. Message text and model output can never select a principal.

### Resource and query boundary

The resource catalog is a versioned JSON configuration managed by operators. It maps a unique `(service, environment)` pair to an HTTPS endpoint, Project, LogStore, fixed scope selectors, one required and separate error selector, analytical dimension, and template version. Credentials are never stored in this file.

In real mode, authentication is `SSO -> short-lived STS -> local Alibaba Cloud CLI StsToken Profile -> SLS plugin`. The service never receives AccessKey ID, AccessKey secret, or Security Token through its own environment. It selects a fixed operator-owned Profile (default `default`), strips direct credential environment variables from the child process, disables plugin auto-install, and never enables CLI debug output. Expired STS credentials fail visibly and require the operator to renew the Profile; the service does not switch accounts or refresh SSO sessions. Because the service deliberately does not read the Profile file, the deployment process must attest that the selected Profile was created in `StsToken` mode; a successful resource check proves access, not credential mode.

The registered templates in this version are:

- `error_analysis_v2` / `error-analysis-v2`: four bounded, read-only aggregate requests for each observation: count before, Top 5 configured error dimensions, Top 5 configured instance dimensions, and count after. Its resource owns distinct `error_field` and `instance_field` values, both indexed text fields with statistics enabled.
- `error_count_v1` / `error-count-v1`: two bounded, read-only aggregate requests for each observation: count before and count after using only fixed scope selectors plus the separate error selector. It requires no analytical dimension, returns at most two aggregate rows, never reads `msg`, and never claims an error type, instance distribution, release cause, database cause, or unified event timeline.

Every resource binds exactly one registered template version. The optional command template argument selects only a closed template ID; it must match the resolved resource version. Omitting the argument preserves backward compatibility by selecting `error_analysis_v2`. Users and models cannot submit provider query strings.

For either template, unequal boundary counts make the observation `Incomplete`. For count-only results `PatternLimit` and `InstanceLimit` are zero, dimensional bucket collections remain empty and non-exhaustive, and renderers must display those dimensions as `本模板不适用` rather than as an empty exhaustive result.

Top-K is an intentional template result, not provider truncation. Pattern and instance shares are derived locally from aggregate counts. A current pattern absent from the baseline Top 5 is only a candidate-new pattern unless the baseline buckets account for the complete baseline error count and neither compared label was redacted. Only then may the report call it confirmed new relative to the selected baseline window.

### Policy boundary

The query gateway must perform these checks before a log query:

1. requester identity is complete;
2. logical resource resolves uniquely;
3. an explicit ACL binding authorizes the principal;
4. template, time window, request count, result rows, timeout, and process concurrency satisfy policy;
5. the configured fields exist in the LogStore index, and analytical fields have statistics enabled;
6. a `STARTED` audit record is durably written.

Unknown, unauthorized, invalid-schema, or preflight-over-budget requests fail closed without calling the query API.

### Persistence boundary

SQLite validates local durability, deduplication, leases, restart behavior, and query audit semantics. Production persistence remains behind application-owned interfaces and will use the organization's approved relational database.

The minimal Feishu delivery queue is also persisted in SQLite. Business-state commits enqueue deterministic delivery events transactionally. A separate delivery worker claims them with a lease and updates one investigation card. The initial reply uses a stable Feishu UUID; repeated card patches are content-idempotent. This is at-least-once local delivery, not an exactly-once guarantee.

M4-B keeps that same queue. Adapter failures are reduced to closed `RETRYABLE`, `PERMANENT`, `OUTCOME_UNKNOWN`, or `CANCELLED` dispositions plus safe reason codes. Permanent failures become `DEAD` immediately; retryable and outcome-unknown sends use bounded exponential backoff and retain the at-least-once boundary. Every completed attempt is append-only audited. Dead-letter replay is allowed only when the card is still bound and the event is the latest safe projection, or when it is the initial queued receipt and no card exists. Superseded/rebound progress and any replay that could overwrite a newer card state fail closed.

The first tenant-governance implementation is a SQLite technical preview. A trusted `(app_id, tenant_key)` is hashed into a local tenant key. One logical current/baseline observation reserves fixed API-call and processed-byte proxies in a fixed UTC window before the governed executor runs. Success settles actual usage, a deterministic pre-provider denial releases the reservation, and an ambiguous provider outcome remains charged as `UNKNOWN`. Reusing a usage key never calls the provider again. Crossing the configured observation, call, or byte ceiling opens a durable cost-proxy circuit for the rest of that window.

High-risk approval is a separate closed state machine: `PENDING -> APPROVED | REJECTED | EXPIRED`, and `APPROVED -> CONSUMED` exactly once. Requests bind the trusted tenant, requester, investigation, action code, immutable payload hash and expiry. This version intentionally registers no write/remediation tool, so approval data cannot trigger an external side effect.

## 6. User and system workflows

### Resolve and confirm a natural-language request

1. The Feishu or loopback-Web adapter derives the trusted app, tenant, user, chat, and source-message identity. Group bot-mention tokens are removed before storing the problem statement.
2. The application validates length and Unicode, redacts common credentials and personal identifiers, blocks obvious instruction/query injection patterns, and persists a `PARSING` resolution keyed by `(app_id, tenant_key, source_message_id)`.
3. The Resource Catalog returns only the current principal's logical capabilities. Endpoint, Project, LogStore, fields, selectors, SQL, and SPL never enter the parser input or preview.
4. Under an independent fixed-window quota, the selected Mock or Volcengine parser returns strict JSON for a closed intent, logical service/environment, duration, and confidence. The application validates every field again.
5. Only `error_spike + error_count_v1`, a complete authorized scope, an allowed duration, and policy confidence can become `RESOLVED`. Unsupported Trace requests are `REJECTED`; missing/low-confidence input is `INCOMPLETE`; unsafe or invalid provider output fails closed.
6. The UI/card displays the redacted user description as unverified text and shows the proposed logical plan. No investigation, query, report, or delivery lifecycle event is created yet.
7. Confirmation sends only the durable resolution ID. The application derives identity from the current adapter, loads the resolution, verifies ownership, expiry, ACL and template again, and then calls the existing `Intake` with a server-built request.
8. Duplicate resolution and duplicate confirmation are idempotent. A changed message under the same source-message ID conflicts rather than replacing the prior interpretation.

### Submit an investigation

1. The receiver normalizes an inbound message.
2. Admission policy validates the command syntax and required scope.
3. `Intake` derives the trusted principal from app, tenant, and user IDs.
4. The application writes the inbox record, investigation, and queued job atomically.
5. Repeated source-message IDs return the original investigation ID without creating another job.
6. The receiver finishes; the worker owns all slow work.

### Execute an investigation

1. A worker claims the oldest runnable job using a time-limited lease.
2. The graph creates current and baseline typed requests using the registered template selected by the normalized investigation request.
3. The query gateway resolves the resource and authorizes the trusted principal.
4. Preflight budgets and index Schema are checked.
5. A durable `STARTED` audit event is written before the first `GetLogsV2` log query is called. Schema metadata may be fetched before this event.
6. The fixed template obtains either count plus bounded Top-K dimensions (`error_analysis_v2`) or count only (`error_count_v1`) without returning raw log bodies.
7. Provider progress and scan metadata returned by the CLI are normalized. `Incomplete`, timeout, provider truncation, missing usage metadata, or processed-byte overflow cannot produce a conclusive finding.
8. Every returned bucket label is length-bounded and redacted before leaving the policy boundary.
9. A single terminal success, incomplete, or failure event is appended to query audit.
10. The report references every evidence item used by its findings.
11. The worker persists evidence and report and marks the job succeeded.
12. The same transaction appends a terminal Feishu delivery event.

### Deliver Feishu progress and results

1. Durable intake appends a queued-card delivery event together with the investigation.
2. Claiming the investigation appends one running-card event.
3. Finishing, failing, or cancelling appends one matching terminal event in the same state transaction.
4. The Feishu process claims delivery events in investigation order. Non-initial updates wait until the initial reply has produced a card message ID.
5. The adapter replies once with an interactive card, then patches that card for progress and terminal states.
6. Delivery failures are bounded and retryable in the local queue; exhaustion becomes observable `DEAD` state without changing the already-committed investigation result.
7. A dead initial receipt continues to block later patches because no card exists. A dead non-initial progress update may be skipped so a later terminal state is still delivered.

### Handle Feishu card actions

1. `card.action.trigger` is normalized to an application-owned command containing event ID, action, investigation ID, trusted app/tenant/user identity, and card context.
2. Only the original requester may operate an investigation card. Unauthorized or invalid actions are acknowledged with a user-facing toast and no state mutation.
3. `view_evidence` and `view_report` return a read-only card projection without issuing SLS queries.
4. `cancel` idempotently cancels a queued or running investigation.
5. `expand_window` creates one derived investigation with twice the lookback, capped by the configured query-window policy.
6. `rerun` creates one derived investigation for the same scope and time range.
7. A `NEEDS_REVIEW` investigation, or a cancelled investigation with an in-flight query whose outcome is unknown, rejects ordinary `rerun` and accepts only the dedicated `rerun_with_cost_ack` action emitted by a card that explains the possible duplicate query cost.
8. Replayed callbacks reuse the callback event ID as the durable inbound idempotency key and cannot create duplicate investigations.
9. Mutating callbacks return only a toast. Their card projection is serialized through the durable delivery worker, which is the sole writer of business-state card updates.

### Validate a real SLS configuration

An explicit diagnostic command loads the same catalog and CLI Profile selection as a real worker, verifies configured Projects, LogStores, and indexes, and prints only non-secret metadata. A separate smoke command runs one authorized fixed-template query. Neither command is run implicitly by the offline demo or test suite.

### Recover work

- An expired running lease makes the job claimable again.
- A running worker renews its lease and observes durable cancellation on the heartbeat boundary.
- A cancelled investigation cannot start new work.
- A duplicate inbound message does not create duplicate investigations or jobs.
- The two metered query steps are named `sls.current` and `sls.baseline`; their input fingerprints and normalized aggregate results are stored outside Eino.
- A `SUCCEEDED` step with the same fingerprint is reused after lease reclaim, so the Provider is not called again.
- Step prepare and completion are fenced by investigation, job, lease owner, job attempt, step name, and input fingerprint.
- A stale `STARTED` metered step has an ambiguous external outcome. It becomes `UNKNOWN`, and the investigation becomes `NEEDS_REVIEW` without another SLS call.
- `NEEDS_REVIEW` is resolved only by an explicit new investigation, which warns that the previous call may already have consumed query capacity.
- Pure planning, report building, and change-catalog correlation remain safe to recompute and are not M4-A checkpoints.

### Correlate a governed change

1. The deterministic M2 graph first produces complete current and baseline Evidence.
2. Cause analysis runs only for a conclusive `spike_detected` report.
3. The change query obtains `resource_id` and its time range from governed Evidence, never from message text or a card value.
4. The read-only Change Source returns at most ten release/configuration events plus source completeness metadata.
5. Each candidate is evaluated by fixed support and counter-tests. Unknown coverage remains `INCONCLUSIVE`; absence from a bounded Top-K set is never treated as counterevidence.
6. The report stores the selected change metadata, hypotheses, test results, Evidence references, confidence method, and limitations.
7. Feishu presents the result as a correlation candidate and explicitly states that correlation is not causal proof.

### Build a governed cross-signal incident timeline

1. Timeline enrichment runs only when a conclusive `spike_detected` report and complete current/baseline Evidence already exist.
2. The application derives `resource_id` and `[baseline.start, current.end)` from that Evidence; the user, card, model, and source cannot select a physical resource or time range.
3. One optional `OperationalSignalSource` call returns at most eight normalized metric/Trace aggregate observations. It cannot return raw spans, Trace IDs, labels, query text, credentials, or arbitrary attributes.
4. The application validates source version, identity, bounds, time containment, finite values, closed signal kind/code/unit combinations, completeness, and duplicate IDs before constructing timeline items.
5. Error-rate and P95-latency anomaly flags are deterministic local calculations. A source cannot declare its own anomaly or causal verdict.
6. Timeline change items reference the existing CauseAnalysis Change IDs; signal items reference their normalized signal IDs and both SLS Evidence IDs. Items are sorted deterministically.
7. `COMPLETE` means the bounded source returned complete metric and Trace coverage for the requested interval. It does not mean a root cause was confirmed. Missing, truncated, unavailable, or invalid source data becomes `INCONCLUSIVE` or `UNAVAILABLE` without changing M2/M3 findings.
8. Feishu renders a bounded timeline and the explicit limitation “时间相关不等于因果证明”. The LLM summary input remains unchanged in this slice.

### Attach governed human-only SOP guidance

1. The Worker first validates the deterministic Engine Evidence and Report using the shared production validator.
2. If a conclusive error spike exists, the application accepts only a current/baseline pair using the fixed `error_analysis_v2` template, valid QuerySpec and governance SHA-256 identities, complete template/schema/policy/usage/order metadata, a shared governance identity, distinct query hashes, contiguous equal windows, and exact binding to the trusted Job request window plus its preceding equal baseline. It then binds the Evidence resource to the Job service/environment/requester through the same `ResourceCatalog.Resolve` and `Allowed` boundary used by the investigation. A zero-error baseline remains data-insufficient and performs zero source calls.
3. One optional `RunbookSource` call runs under an independent default five-second child Context and returns only versioned curated entries with stable identity, revision, owner, update time, matched Recommendation codes, and closed step codes `VERIFY_ERROR_PATTERN / OBSERVE_HOT_INSTANCE / ESCALATE_SERVICE_OWNER`. The application samples the child Context after Source return and before local cancellation, so a successful Set returned after the deadline is rejected. Each code must match its locally canonical Kind and Instruction; the Provider cannot inject free-form step text, Evidence IDs, URLs, commands, scripts, execution parameters, or arbitrary attributes.
4. The application computes each item's Recommendation and Evidence references locally, assigns `HUMAN_REVIEW_ONLY`, calculates a stable content fingerprint, and sorts all collections deterministically. The trusted assembly layer also assigns the closed `SYNTHETIC_MOCK` or `ENTERPRISE_GOVERNED` data source; neither Engine nor Source can self-report it.
5. The Worker validates the enriched Report again. Every referenced Recommendation must exist, and each Evidence set must exactly equal the union grounded by those Recommendations.
6. The LLM summary continues to receive only the pre-existing deterministic Recommendations. It cannot see, generate, select, or rewrite SOP content.
7. A missing Service leaves the optional field unset. No conclusive spike returns `SKIPPED_NO_TRIGGER`; once a report claims a conclusive spike, a zero baseline, missing deterministic Recommendations, or invalid governed resource/Evidence identity returns `UNAVAILABLE` without a Source call. No match, incomplete results, provider failure, or invalid output also has a closed status and cannot change the investigation's facts or successful result.
8. Feishu renders bounded plain text with the fixed warning that the steps are for human review and are never executed automatically. A `SYNTHETIC_MOCK` projection uses the explicit section heading `受控 SOP 参考（Mock）`; only `ENTERPRISE_GOVERNED` uses the heading without the Mock marker. An empty or unknown data source fails closed to an unavailable, source-unconfirmed message and renders no supplied entries.

### Run the synthetic offline evaluation gate

1. The evaluator loads a repository-owned dataset using strict JSON decoding and rejects unknown fields, duplicate case IDs, invalid time ranges, unsafe labels, or impossible aggregate fixtures.
2. Every case declares a trusted request, synthetic current/baseline aggregates, optional synthetic change context, and explicit expected outputs and budgets.
3. The command constructs fixture-backed Mock adapters and runs the same deterministic Eino graph used by the application. It does not open Feishu, SLS, a model endpoint, or any other network connection.
4. The evaluator first applies the same output validator used by the production Worker to the independent Evidence returned by `InvestigationEngine.Run`. That Evidence must exactly match the report projection, and each current/baseline item must bind to its exact QuerySpec and Fixture identity. The evaluator then compares the report with the label: exact outcome, exact Finding codes, exact Recommendation codes and their current/baseline Evidence names, cause-analysis status and verdicts, evidence references, logical observations, Provider-call proxy, and processed-byte proxy.
5. Aggregate metrics are calculated from case results. An unexpected conclusive finding is counted as misleading even if other expectations pass.
6. The JSON result records the dataset version and fingerprint plus an explicit `synthetic_mock` provenance marker, zero external-network calls, and no credential requirement.
7. Any configured regression gate failure makes the command return a non-zero exit code after printing the complete structured report.

### Observe and replay a synthetic Agent run

1. The command creates a unique evaluation-run identity and a normalized runtime-version manifest before executing any Case.
2. Every Case receives a privacy-safe Trace context. The Engine records one run span and the fixed `plan_queries`, `execute_queries`, `build_report`, and `correlate_changes` Graph-node spans.
3. Fixture-backed adapters record only the typed `sls.current`, `sls.baseline`, and conditional `change_source.list` tool spans. Tool usage must reconcile with the evaluation report's logical calls, Provider-call proxy, and processed-byte proxy.
4. The Observer never serializes callback input/output or arbitrary attributes. It records stable codes, counts, timestamps, durations, completion, hashes, and the runtime-version fingerprint.
5. The default application Observer is a no-op. A bounded recorder never fails an investigation; overflow or invalid events make the Trace incomplete and make the offline replay gate fail closed.
6. B2 optionally saves successful and failed evaluation runs through `evaluate --snapshot-dir <directory>`. Each append-only `evaluation-replay-v1` snapshot contains the complete evaluation report, version manifest, Case Traces, a safe terminal failure code, creation time, optional parent replay reference, and a SHA-256 over the canonical snapshot body. Existing run IDs are never overwritten.
7. `replay --snapshot-dir <directory> --run-id <evaluation-run-id>` strictly loads and verifies the source snapshot before execution. Unknown fields or Schema, a changed content hash, an invalid or duplicate run identity, an incomplete file, or a source whose synthetic dataset boundary is incompatible with the current embedded fixture fails closed before the Graph runs.
8. Replay means executing the current binary again against fixed synthetic input and appending a new child snapshot that references the verified source run and hash. It does not claim byte-for-byte equivalence with the source, and reproducing an older implementation still requires its Git commit or build artifact.
9. `replay-compare --snapshot-dir <directory> --base-run-id <id> --candidate-run-id <id>` strictly loads and verifies two existing snapshots and never executes the Graph, a Mock tool, or an external provider.
10. B3 emits numeric quality, cost-proxy, tool, Trace, latency-observation and gate transitions only when dataset Schema/ID/fingerprint, data boundary, executor profile, and Case ID set match exactly. Graph, template, policy, cause, evaluation, Prompt and model metadata changes remain comparable but are listed explicitly as version changes. Trace and Replay Schema versions are strict reader boundaries: the current reader accepts only the current closed Schema versions, and a mismatch is rejected during snapshot loading before comparison.
11. An incompatible pair returns a structured `INCOMPARABLE` result containing only stable incompatibility codes and immutable run references. It must not emit numeric deltas, regressions, gate transitions, or recovered/newly failed Cases.
12. A comparable result reports recovered, newly failed and still-failed Case IDs; top-level safe failure-code changes; gate transitions; closed quality/cost/Trace metric deltas; fixed tool-span and tool-usage deltas; and closed Agent failure-code count deltas. A candidate that removes any Gate present in the base run is a regression. Other regression labels are derived only from fixed metric direction metadata. Local latency remains observational and is never a production SLO.

### Rehearse reviewer feedback and rollout readiness

1. C1 accepts feedback only for a strictly loaded immutable candidate snapshot. Each record binds the exact evaluation-run ID, snapshot content hash, version fingerprint, Case ID, an adapter-derived reviewer reference, a closed verdict, and a closed reason code.
2. Feedback records are append-only and content-hashed. A correction appends a new record that explicitly supersedes one prior record from the same reviewer, snapshot, and Case; it never overwrites history. Branched, cyclic, cross-Case, cross-run, or cross-reviewer correction chains fail closed.
3. The initial fixture uses two distinct synthetic reviewers per Case. It contains no real identity, free-form text, report body, Evidence body, query, log, provider error, credential, or network-derived data.
4. C2 strictly loads base and candidate snapshots, obtains a B3 comparison, resolves the latest active feedback per reviewer and Case, and applies a versioned rollout policy. Invalid snapshots, invalid feedback, invalid policy, or inconsistent references return an error rather than a decision.
5. A valid but incompatible comparison, missing Case coverage, insufficient reviewer quorum, an `UNSURE` verdict, or unresolved reviewer disagreement produces `REHEARSAL_INSUFFICIENT_EVIDENCE`.
6. A failed candidate evaluation, a regression, or an `UNSAFE` review produces `REHEARSAL_BLOCKED` before a simulated pilot and `REHEARSAL_ROLLBACK_RECOMMENDED` only when the caller explicitly evaluates the simulated active-pilot phase.
7. `REHEARSAL_PASSED` requires a passed candidate snapshot, a comparable result, no regressions, all required candidate Gates present and passing, full Case feedback coverage, the configured independent-reviewer quorum, and no unsafe, unsure, or disagreeing active review.
8. Every decision contains only immutable run references, policy identity/fingerprint, aggregate feedback counts, closed reason codes, and the explicit boundary `data_source=SYNTHETIC_MOCK` and `production_action_allowed=false`. It never performs a rollout or rollback action.

### Summarize governed evidence with an LLM

1. The model receives only the already validated deterministic report projection: bounded Findings, Evidence IDs and summaries, cause-analysis status, limitations, and deterministic recommendations. Raw logs, SQL, credentials, Feishu identity, provider errors, and ungoverned metadata are excluded.
2. A provider-neutral `ReportSummarizer` port owns the application contract. The first offline implementation is deterministic Mock; the intended deployment adapter is Volcengine Ark and must remain isolated from the Eino, SLS, and Feishu adapters.
3. The model output is a strict bounded structure for phenomenon summary, possible cause, evidence references, limitations, and next steps. Every referenced Evidence ID must exist in the input report, and the model cannot change confidence, completeness, cause verdict, or authorization.
4. Timeout, throttling, provider failure, invalid JSON, unknown fields, unsupported claims, or broken references fall back to the original deterministic report. They cannot fail or promote the investigation.
5. Provider/model, Prompt version/fingerprint, bounded request ID, generated/fallback status, Token usage, and latency are persisted when a real model is enabled; Prompt text and evidence content are not written into Agent events.
6. When summary quota is enabled, the application derives a tenant key only from the trusted inbound principal and reserves one request plus a configured Token allowance before calling the provider. Quota denial, replay, or ledger failure performs zero provider calls and uses the deterministic fallback summary.
7. Successful provider calls settle their reported input/output/total Token usage. A timeout, cancellation, transport failure, or process boundary with uncertain provider outcome retains the conservative reservation as `UNKNOWN`; it is never automatically released or retried. A repeated investigation/prompt usage key performs zero new provider calls.
8. If actual usage exceeds the per-request reservation, the actual usage is still durably recorded because the cost has already occurred, but the model output is not accepted. This opens the fixed-window circuit as applicable and the user receives the deterministic fallback.

## 7. Behavioral contracts and lifecycle

Natural-language resolution has a lifecycle separate from investigations:

```text
PARSING -> RESOLVED | UNKNOWN | INCOMPLETE | REJECTED | FALLBACK | OUTCOME_UNKNOWN
RESOLVED --explicit confirm--> existing durable Intake -> QUEUED
```

No non-`RESOLVED` state is confirmable. A parser timeout or ambiguous provider outcome is not retried automatically because it may already have consumed tokens. Intent quota reservations and summary quota reservations use independent usage keys and policies. `ProblemStatement` is redacted before persistence; its fingerprint supports idempotency, while its text is always rendered as an unverified user assertion and never as Evidence.

Investigation states are `QUEUED`, `RUNNING`, `SUCCEEDED`, `FAILED`, `CANCELLED`, and `NEEDS_REVIEW`.

Allowed terminal states cannot transition back to running. Each claim increments the attempt count and binds a lease owner and expiry. Renewal and completion require both the active lease owner and the active attempt fencing token, so a stale process cannot submit through a newer claim that reused the same worker ID.

Provider `progress`, usage, and nanosecond-order metadata exposed by the CLI are preserved. `complete=true` is derived only from provider `Complete` plus local structural, usage, and budget gates. Missing CLI metadata fails closed. A finding is conclusive only when all referenced evidence is complete and not truncated. Otherwise the report must describe the result as insufficient data.

Near-real-time requests use a configured ingestion watermark. The default command scope ends ten seconds before the message timestamp, and the query gateway rejects a scope whose end has not crossed that watermark. Matching count-before/count-after values are additionally required for a complete multi-query observation. The watermark is an operator-owned ingestion-latency assumption and must be calibrated against the pilot pipeline.

The provider `isAccurate` field is a nanosecond-order option, not an analytical accuracy signal. It is retained only as ordering metadata and is never used to promote a finding. Provider completeness is governed by `progress` plus local usage, structural, and budget checks.

Redaction changes only the displayed aggregate label; it does not change the aggregate counts or automatically make otherwise complete evidence inconclusive. The `Redacted` marker remains on Evidence so readers know that a label was transformed.

Query audit is append-only and excludes credentials, raw log bodies, and raw provider query strings. A query-spec fingerprint includes the resolved resource, template and policy versions, selectors, time range, and enforced limits. `QueryID` is an adapter-generated execution correlation ID. A provider Request ID is recorded only when the transport exposes one; the normal CLI success response does not guarantee it, so the system must never copy or relabel a local execution ID as a provider Request ID.

Notification delivery states are `PENDING`, `RUNNING`, `SENT`, and `DEAD`. Claims use owner, lease expiry, and attempt fencing. A delivery failure never rolls back or changes the investigation business state. M4-B uses a closed failure disposition, append-only attempt audit and bounded retry. Operational replay must pass the latest-card-projection transaction guard and is itself audited.

M2 runs one Feishu/delivery process. Database fencing protects local claims and stale attempts, but globally ordered remote patches from multiple delivery processes require the production outbox/dispatcher work planned for M4.

M4-A persists one checkpoint per logical SLS window. Its input hash combines the immutable logical `QuerySpec` with a governed fingerprint over the catalog and physical resource, selectors, schema, template, policy, and budgets. Confirmed successful results are reusable only while that governance identity still matches, and current/baseline Evidence with different governance identities cannot produce a report. An abandoned `STARTED` metered step is never automatically retried. Other executor errors remain terminal until the later M4 retry-classification slice is implemented. The CLI adapter performs exactly one process invocation per declared metadata or query call and does not enable hidden retries. The system does not claim exactly-once SLS query execution.

Existing queued records created before trusted principals were persisted decode with an empty principal and must fail closed in real-SLS mode. The offline mock remains backward compatible.

`Report.CauseAnalysis` is optional for backward compatibility. When present it has one of `COMPLETE`, `INCONCLUSIVE`, `UNAVAILABLE`, or `SKIPPED_NO_SPIKE`. A hypothesis verdict is `SUPPORTED_CANDIDATE`, `REFUTED`, or `INCONCLUSIVE`; it is never promoted to a conclusive Finding.

Every hypothesis has at least one support test and one counter-test. Each test references only Evidence IDs and Change Event IDs stored in the same report. Test results are `PASS`, `FAIL`, or `UNKNOWN`; missing, truncated, non-exhaustive, or redacted inputs produce `UNKNOWN`, not an inferred absence.

Cause confidence uses the versioned deterministic method `change-correlation-v1`, is not a probability, and is capped at `0.85`. A supported candidate requires a conclusive M2 spike, temporal precedence, affected-instance concentration of at least 50%, an increase of at least 20 percentage points from baseline, a complete change set, and no passing counter-test. A complete affected-instance set with complete comparable current distribution and zero overlap is a hard refutation. Multiple overlapping changes are confounding evidence and force `INCONCLUSIVE`.

Change Source errors, disabled configuration, or incomplete source coverage never turn an otherwise valid M2 report into a failed investigation.

`Report.IncidentTimeline` is optional for backward compatibility. When present it uses `operational-signal-timeline-v1` and has one of `COMPLETE`, `INCONCLUSIVE`, `UNAVAILABLE`, or `SKIPPED_NO_SPIKE`. `COMPLETE` requires a complete, untruncated source set with at least one metric observation and one Trace observation; it is a data-coverage status, not a causal verdict.

Operational-signal observations use a closed schema and finite non-negative values. Error-rate values are ratios in `[0,1]`; latency values are milliseconds. The application derives anomaly flags from versioned local thresholds and the Worker recalculates them before persistence. Timeline references must resolve to the same report's Evidence, Change Events, and signals. Optional enrichment failures never fail an otherwise valid investigation.

`Report.RunbookGuidance` is optional for backward compatibility. Its closed statuses are `COMPLETE`, `NO_MATCH`, `INCONCLUSIVE`, `UNAVAILABLE`, and `SKIPPED_NO_TRIGGER`. `COMPLETE` means only that the bounded source query completed and returned at least one valid match; it is not a correctness, freshness, approval, or causality statement. `NO_MATCH` is scoped to the queried catalog version and cannot be phrased as proof that no enterprise SOP exists.

Runbook entries are untrusted adapter output. They must use stable identifiers, immutable revisions, update timestamps no later than five minutes after both the report generation time and the trusted service clock, safe bounded metadata, and closed human-only step codes. `CanonicalRunbookStep` uniquely owns each code's Kind and Instruction; Provider-authored free-form step text is rejected. Entry fingerprints, Recommendation references, Evidence unions, and the closed `SYNTHETIC_MOCK / ENTERPRISE_GOVERNED` data source are application-owned. The default independent Lookup timeout is five seconds, and a Set returned after that deadline is invalid even when the Source returns no error. A healthy parent Context treats that child timeout and Source-local cancellation/timeouts as `UNAVAILABLE`; only the Worker's actual Context cancellation propagates. Renderers must not display entries when the data source is empty or outside the closed set. No Runbook executor, approval consumer, URL, command, script, or write-action field exists in this contract.

M5-A is a deterministic engineering regression gate, not a production-readiness decision. Outcome, Finding, Recommendation, and cause-verdict agreement are measured only against repository-owned synthetic labels. Recommendation matching is exact by code and Evidence name, so an omitted, injected, duplicated, or misgrounded next step fails closed. Evidence coverage verifies reference integrity, not factual completeness outside the fixture. Processed bytes and fixed Provider-call counts are cost proxies rather than an Alibaba Cloud bill. Local elapsed time is recorded for trend inspection but is not a production latency SLO. Prompt and Token metrics remain not applicable until the required LLM summary slice is enabled and evaluated.

M5-B Agent events use a fixed schema and closed layer/name/phase enums. The initial hierarchy is evaluation run -> Case Trace -> Engine run -> Graph node or Mock tool. Each span has exactly one start and one terminal phase, parent references form an acyclic closed Trace, sequence numbers are unique and increasing, and terminal usage must agree with the governed evaluation counters. Events contain no arbitrary key/value map or raw error text. Safe failure classification is observational only and cannot alter M4 retry or recovery semantics.

The runtime-version fingerprint is computed from a normalized manifest, not from host identity or wall-clock values. Synthetic and production query policies remain distinct through `executor_profile`; `prompt_used=false` requires Prompt/model/Token fields to remain absent or explicitly not applicable. Hashes provide integrity and correlation, not anonymization.

The B1 gate version is `m5b-agent-trace-gate-v1`, the Agent event/Trace schema is `agent-trace-v1`, and the replay snapshot schema is `evaluation-replay-v1`. B1 and B2 execute only under `executor_profile=SYNTHETIC_MOCK`. B2 uses a dedicated evaluation-run store and never extends or reuses the production investigation Store, Query Audit, or QueryStep contracts. Snapshot hashes provide tamper detection, not confidentiality; the Trace and report privacy contracts still apply before persistence.

M5-C feedback uses a store separate from both the production investigation Store and the replay snapshot Store. Content hashes detect accidental or unauthorized modification but do not authenticate a real reviewer. C1/C2 execute only with synthetic reviewer fixtures and must retain `production_action_allowed=false`; changing that boundary requires the real identity, authorization, audit, threshold-approval, and pilot dependencies defined by C3.

## 8. Constraints and compatibility

- Eino is pinned to `v0.9.14`; pre-release APIs are excluded.
- Feishu SDK is pinned to `v3.9.10`.
- Alibaba Cloud CLI `3.x` and the installed `aliyun-cli-sls` plugin are deployment prerequisites; their exact approved versions are recorded and verified by the deployment process, not downloaded at runtime. `sls-check` proves that the configured binary/plugin can execute the required metadata commands, but does not itself attest a version allowlist.
- Real catalog endpoints must use `https://`; same-region VPC endpoints are recommended for deployment.
- Catalog endpoints remain absolute HTTPS Alibaba Log Service URLs, but the adapter derives and passes an explicit Region plus only the validated host to `aliyun-cli-sls --endpoint`; the plugin owns Project-subdomain construction and the selected Profile need not provide an implicit Region.
- Credentials come only from an existing local Alibaba Cloud CLI `StsToken` Profile selected by `LOG_AGENT_SLS_CLI_PROFILE`; direct AK/SK/Token environment variables are stripped from the child process and are unsupported by this service. Credentials and tokens must never be logged.
- The adapter resolves the `aliyun` executable at startup, invokes it with `exec.CommandContext` rather than a shell, bounds stdout/stderr, sets `ALIBABA_CLOUD_CLI_PLUGIN_AUTO_INSTALL=false`, and forces the selected Profile through `ALIBABA_CLOUD_PROFILE`.
- Application cancellation and the per-call timeout terminate the CLI child process. STS expiry, missing Profile, missing plugin, unsupported output, or non-zero exit fails closed with a bounded sanitized error.
- SLS `limited` metadata represents the SQL result-row limit and is not itself evidence of truncation. Every fixed aggregate SQL statement owns an explicit `LIMIT`.
- SQL aggregate rows may be returned in the plugin's `data` container or a compatible legacy `logs` container. Both non-empty at once is ambiguous and fails closed; `meta` remains mandatory for quality and usage accounting.
- Raw provider error messages, bodies, headers, URLs, and query strings cannot cross the Alibaba SLS adapter or enter query audit.
- External messages and log content are untrusted input and cannot alter resources, templates, permissions, or budgets.
- Feishu callback values are untrusted. They may select only a closed action enum and an investigation ID, and every mutation is authorized against the persisted requester.
- Card rendering is owned by the Feishu adapter. Untrusted bucket labels are JSON-escaped and rendered as plain text rather than executable Markdown.
- The offline demo, full-mock command, and default tests run without network credentials and produce deterministic facts from Mock SLS data; generated IDs are intentionally unique.
- Real integration checks and commands are explicit opt-in operations and cannot be reported as passed without a configured test resource. The repository does not yet contain a credentialed live integration test suite.

## 9. Acceptance checklist

### Existing investigation skeleton

- [x] A local `mock-e2e` command covers mock Feishu intake and replay deduplication, SQLite state, Worker/Eino, the real ACL/Schema/budget/audit query gateway over a Mock SLS backend, Evidence persistence, and mock Feishu reply/patch delivery without credentials or network access.
- [x] A local command runs the complete graph and returns an evidence-backed mock report.
- [x] Concurrent replay of one source-message ID creates exactly one investigation and one job.
- [x] A claimed job can be reclaimed after its lease expires.
- [x] A cancelled investigation is not claimable.
- [x] A long-running investigation renews its lease, and durable cancellation reaches the engine context.
- [x] An incomplete result cannot produce a conclusive finding.
- [x] Eino and Feishu SDK types do not escape their adapter packages.

### Governed natural-language intake

- [x] Ordinary text can produce a persisted, non-executing preview for one closed `error_spike` intent.
- [x] Parser input contains only a redacted problem and ACL-filtered logical capabilities.
- [x] Explicit confirmation is mandatory and rechecks identity, ACL, expiry and template before job creation.
- [x] Duplicate parse/confirm operations are idempotent and parser quota is independent from summary quota.
- [x] Loopback Web and Feishu adapters use the same application service; strict `/investigate` remains compatible.
- [x] Mock parser unit/vertical tests and guarded Ark protocol tests are complete.
- [ ] A real `intent-smoke` with an approved model/key and a real Feishu visual/callback test are still required.

### Read-only SLS query foundation

- [x] The Alibaba Cloud CLI/SLS plugin protocol exists only inside its adapter package; the Go SDK is not a runtime dependency.
- [x] The default demo remains offline and produces deterministic facts.
- [x] A unique service/environment mapping resolves to one configured HTTPS SLS target.
- [x] Every resource has an explicit error selector separate from its scope selectors.
- [x] A trusted principal is derived from the inbound envelope and cannot be forged in the request.
- [x] Unknown resources, missing ACL bindings, unsafe windows, and invalid policy are rejected before log-query execution.
- [x] Missing indexes or analytical fields without statistics enabled are rejected before the fixed query executes.
- [x] The fixed template preserves adapter execution IDs plus provider progress, nanosecond-order metadata, processed rows, processed bytes, and elapsed time when the CLI exposes them; missing metadata fails closed, and provider Request IDs are never fabricated.
- [x] The CLI adapter normalizes real `data/meta` aggregate responses and converts catalog HTTPS endpoints to explicit Region plus the plugin's host-only endpoint argument.
- [x] Incomplete, timed-out, structurally inconsistent, truncated, metadata-deficient, or over-scan-budget results cannot yield a conclusive finding.
- [x] Denied, started, succeeded, incomplete, and failed attempts are durably auditable without secrets, raw logs, or raw SQL.
- [x] A configuration-check command is implemented and unit-tested not to query log bodies.
- [ ] The configuration-check command succeeds against the designated real pilot resource.
- [ ] An opt-in smoke command can query a designated pilot resource when valid credentials and catalog are supplied.
- [x] `gofmt`, deterministic tests, and `go vet` pass without cloud credentials.
- [ ] Race tests run in an environment with CGO and a C compiler.

### Error-spike investigation loop

- [x] Each current and baseline observation executes exactly four fixed aggregate requests (count before, two bounded dimensions, count after) and returns no raw log rows.
- [x] A changed boundary count marks the observation incomplete and cannot yield a conclusive finding.
- [x] Error-pattern and instance buckets are bounded, structurally validated, individually redacted, and traceable to Evidence.
- [x] Pattern share and instance concentration are deterministic local calculations.
- [x] A Top-K absence is presented as a candidate unless baseline coverage proves the distribution is exhaustive.
- [x] Incomplete or inconsistent distribution data cannot produce a conclusive pattern or instance finding.
- [x] Intake, running, success, failure, and cancellation state changes enqueue ordered durable card updates.
- [x] A delivery crash can be reclaimed without allowing a stale claimant to finish a newer attempt.
- [x] The Feishu adapter sends one reply card with a stable UUID and patches the same card for later states.
- [x] Only the persisted requester can view evidence, cancel, expand, or rerun through card callbacks.
- [x] Callback replay creates at most one derived investigation.
- [x] Expanding a window cannot exceed the configured query-window limit.
- [x] Feishu SDK types remain confined to the Feishu adapter, and all offline tests run without Feishu or SLS credentials.

### Change-correlation evidence and refutation

- [x] A strict versioned JSON Change Catalog accepts only bounded release/configuration events and rejects unknown fields, invalid time ranges, duplicate IDs, unsafe strings, and oversized affected-instance lists.
- [x] Cause enrichment derives its resource and time range from governed Evidence and is skipped before complete SLS evidence exists.
- [x] The default disabled Change Source and source failure or invalid data preserve the M2 outcome while returning an explicit unavailable or inconclusive cause-analysis status.
- [x] Every emitted hypothesis contains support and counter-tests, a versioned confidence method, bounded confidence, Evidence/Change references, and a causal limitation.
- [x] Complete affected-instance overlap can support a change-correlation candidate; complete zero overlap can refute it; bounded, redacted, incomplete, or confounded inputs remain inconclusive.
- [x] The Worker rejects broken ledger references, duplicate IDs, missing support/counter tests, non-finite or invalid weights/confidence, fabricated Change references, and conclusions backed by insufficient evidence.
- [x] SQLite persists the cause-analysis ledger in the same success transaction as Evidence and the report.
- [x] Feishu report/evidence cards render bounded change, support, counter, unknown, confidence-source, and limitation content without exposing raw logs, raw queries, provider errors, or untrusted URLs.
- [x] The offline demo deterministically emits one supported change-correlation candidate while retaining exactly two logical SLS observations and eight fixed provider calls in total.
- [x] Live release/configuration systems, version-distribution queries, Trace/metric correlation, and enterprise knowledge retrieval remain explicitly unimplemented.

### M3-B Mock-first cross-signal incident timeline

- [x] The signal query resource and full baseline/current interval are derived only from complete governed Evidence.
- [x] One optional source call returns at most eight closed-schema metric/Trace aggregates and never exposes raw spans, Trace IDs, labels, provider queries, credentials, or arbitrary attributes.
- [x] Error-rate and P95-latency anomaly flags are calculated locally and recalculated by the Worker before persistence.
- [x] Existing Change references and normalized signals form a bounded, deterministically ordered, reference-complete timeline.
- [x] No-spike and insufficient-evidence reports do not call the source; unavailable, invalid, incomplete, or truncated source results preserve the existing M2/M3 report and downgrade only the timeline.
- [x] The Feishu card renders a bounded timeline and states that temporal correlation is not causal proof.
- [x] The normal demo, mock Worker assembly, and `mock-e2e` use only `signalmock`; real SLS mode does not silently inject a Mock signal source.
- [x] The full offline suite and static checks pass without credentials or network access; real metric/Trace connectors, external-call governance, and production calibration remain explicitly pending.

### Governed Mock-first SOP knowledge guidance

- [x] The Worker enriches only a previously validated conclusive-spike report and calls the optional source at most once with Evidence-derived resource identity and deterministic Recommendation codes.
- [x] Before lookup, the application requires fixed-template, fully governed current/baseline Evidence with matching governance identity, contiguous equal windows, and exact binding to the trusted Job request; it then binds the resource to `ResourceCatalog` requester ACL, recomputes the closed Recommendation set, and rejects mismatched report grounding.
- [x] Source-provided entries cannot provide Evidence references, arbitrary URLs, commands, scripts, execution parameters, or write actions.
- [x] Application-derived Guidance items use stable fingerprints, exact Recommendation/Evidence grounding, deterministic ordering, and `HUMAN_REVIEW_ONLY` execution mode.
- [x] No conclusive spike performs zero source calls with `SKIPPED_NO_TRIGGER`; a zero baseline never triggers lookup, and a report claiming a spike with zero baseline, missing Recommendations, or invalid governed identity performs zero calls with `UNAVAILABLE`; complete no-match, incomplete, truncated, unavailable, and invalid results preserve the original report.
- [x] The Worker rejects fabricated or missing references, unsafe metadata, duplicate identity, invalid revision/fingerprint, unknown step codes, non-canonical Kind/Instruction pairs, non-human execution mode, and oversized collections.
- [x] Lookup has an independent default five-second timeout. Its expiry and Source-local cancellation/timeout degrade to `UNAVAILABLE` while real parent cancellation propagates; entries beyond either the report-time or trusted-service-clock five-minute skew are rejected.
- [x] Data source is assigned by trusted assembly, never by Engine or Source; Feishu renders bounded plain text without an execution button, marks `SYNTHETIC_MOCK` as `受控 SOP 参考（Mock）`, and states that the steps are only for human review.
- [x] A no-error Set returned after the child deadline is rejected, and empty or invalid data source values render only an unavailable/source-unconfirmed message with no SOP entries.
- [x] The Mock end-to-end run recorded one Runbook source call, one item, three steps, two SLS observations, eight Provider calls, and zero external network calls.
- [x] The demo completed `SUCCEEDED` with `COMPLETE/HUMAN_REVIEW_ONLY` guidance; `evaluate` and `summary-evaluate` remained `PASSED` with dataset fingerprints `caf2714c80a646c5da15134c6557879565ffc8e083a66da1f1c9e49d3d0dc1f8` and `82e813aed0721f15b89a19b053da6b1d47509ab07f45122af4ed0c075e60a0b1`.
- [x] `go test -count=1 ./...` passed on the first hardened worktree; the final second-round tree is revalidated by the root task.
- [x] First-round `gofmt`, `go vet`, shuffled focused tests, repository link/diff checks, snapshot/replay/compare/feedback/rehearsal checks, and the unavailable race-toolchain status were recorded from actual runs; they are not presented as the final second-round result.
- [x] Eino/LLM input/evaluation/Trace/replay and their existing fingerprints exclude SOP data; real enterprise knowledge connectors, content approval, tenant authorization, audit, expiry, and production calibration remain explicitly pending.

### M4-A recoverable metered query steps

- [x] A successful current-window checkpoint survives SQLite reopen and is reused while only the missing baseline window reaches the Provider.
- [x] Two successful window checkpoints can rebuild and persist the report after job reclaim without another Provider call.
- [x] A stale `STARTED` metered step becomes `UNKNOWN`; the investigation becomes `NEEDS_REVIEW`, and recovery performs zero new Provider calls.
- [x] Cancelling before or after an in-flight step becomes `UNKNOWN` preserves a stable cost-risk marker; ordinary rerun/expand actions are rejected until the dedicated cost-acknowledgement action is used.
- [x] Checkpoint prepare and completion reject stale job attempts, expired leases, changed logical or governed input fingerprints, and oversized or invalid result payloads.
- [x] The governed fingerprint binds the catalog and physical resource, selectors, schema, template, policy, and budget; current and baseline evidence must carry the same governance identity before a conclusion is allowed.
- [x] Checkpoints contain only normalized aggregate `QueryResult` data and exclude raw logs, SQL, credentials, and raw Provider errors.
- [x] The offline mock flow traverses the checkpoint wrapper and still performs exactly two logical observations and eight Provider calls.
- [x] `gofmt`, offline tests, `go vet`, and the mock end-to-end command pass; race testing remains separately reported according to toolchain availability.
- [x] Operator resolution of unknown paid-query steps and a production database remain explicitly deferred to later M4 slices; M4-B does not weaken the no-automatic-retry rule for ambiguous SLS reads.

### M4-B delivery recovery and tenant governance

- [x] Feishu adapter failures map to a closed provider-neutral disposition and safe reason code; permanent errors die immediately while retryable/unknown sends retain bounded backoff.
- [x] Every completed delivery attempt is append-only audited without provider error text or card content.
- [x] Dead letters are operator-visible, and replay is transactionally rejected for rebound interactions or any event that could overwrite a newer card projection.
- [x] The initial dead queued receipt can be replayed to unblock card creation, while stale progress cannot be replayed over a later terminal projection.
- [x] A trusted app/tenant maps to a hashed fixed-window quota key; observation, API-call, and processed-byte proxy ceilings are atomically enforced before the governed executor.
- [x] Successful queries settle actual usage, deterministic pre-provider failures release it, and ambiguous external outcomes retain their reserved cost proxy.
- [x] Reusing one quota usage key performs zero additional Provider calls, and Checkpoint reuse bypasses new quota reservation.
- [x] High-risk approval requires an immutable payload hash, same-tenant independent approver, expiry, and exactly-once consumption; no high-risk executor is registered.
- [x] The full Mock E2E traverses the quota wrapper with two observations/eight Provider-call proxies and zero credentials/network calls.
- [ ] Production DB migrations, multi-instance global quota, real DLQ RBAC, real approval UI/identity/tool execution, and live failure-code calibration remain M4-C inputs.

### M5-A synthetic offline evaluation gate

- [x] A strict, versioned synthetic dataset covers a supported spike/change candidate, no significant spike, incomplete evidence, a refuted change, and an inconclusive change.
- [x] The evaluator runs the real deterministic graph only against fixture-backed Mock SLS and Mock change data, with zero credentials and zero external-network calls.
- [x] Outcome accuracy, Finding and Recommendation exact accuracy, production Worker output-validation accuracy, QuerySpec-to-Evidence contract accuracy, unexpected-conclusive-finding rate, evidence-reference coverage, cause-verdict agreement, fixed query budget, processed-byte cost proxy, and elapsed time are emitted as structured metrics.
- [x] Every case declares exact expected outcome, conclusive/nonconclusive Finding codes, Recommendation codes with current/baseline Evidence names, expected cause status/verdicts, logical observation count, Provider-call proxy, and byte ceiling.
- [x] A failed metric or per-case safety expectation prints a structured failure report and returns a non-zero process exit code.
- [x] Dataset schema/version and content fingerprint are present in every evaluation report so future Graph and policy changes remain comparable.
- [x] Offline tests, `go vet`, and the evaluation command pass without Feishu, SLS, change-platform, or model credentials.
- [x] Real historical incidents, expert labels, production Agent telemetry, pilot groups, production thresholds, and gray-rollout approval remain explicitly deferred to later M5-B/M5-C slices.

### M5-B Agent observability and offline replay

- [x] B1 defines and validates a closed, privacy-bounded Agent event schema, stable safe-failure taxonomy, normalized runtime-version manifest, and deterministic version fingerprint.
- [x] B1 provides a no-op Observer and a concurrent bounded recorder whose overflow or invalid input is visible as an incomplete Trace without failing the business run.
- [x] B1 records one complete synthetic Trace per Case with one Engine run, four fixed Graph-node spans, two SLS tool spans, and a conditional Change Source span; every span has one start and one terminal event.
- [x] B1 reconciles Trace tool usage with evaluation QuerySpecs, logical/Provider call counts, processed-byte proxy, and change-source calls while retaining zero credentials and zero external-network calls.
- [x] B1 proves the serialized Trace excludes forbidden message, identity, resource, query, log, bucket, change-summary, natural-language report, callback, provider-error, Prompt, and arbitrary-attribute content.
- [x] B2 saves successful and failed evaluation runs as append-only strict snapshots with content hashes and duplicate/tamper rejection, without extending the production Store interface.
- [x] B2 exposes an offline `replay` command that uses the current binary and fixture-backed Mock dependencies only.
- [x] B3 strictly loads two immutable snapshots without executing the Graph or any provider, compares only matching dataset/data-boundary/executor/Case contracts, reports explicit version changes plus quality/cost/tool/Trace and Case transitions, and returns delta-free `INCOMPARABLE` output for incompatible pairs.
- [ ] Live telemetry export, production sampling/retention, real LLM/Token observability, historical build execution, real incident feedback, and production SLO approval remain explicitly deferred.

### M5-C rollout readiness and real pilot

- [x] C1 defines a strict, bounded, content-hashed feedback record that binds one reviewer verdict to one immutable snapshot and Case.
- [x] C1 persists feedback append-only, rejects duplicate/tampered/unknown-field records, and resolves correction chains without losing audit history.
- [x] C1 provides a two-reviewer-per-Case synthetic fixture with zero credentials, real identities, free-form text, or external-network calls.
- [x] C2 produces only closed rehearsal decisions from strictly validated snapshots, a compatible B3 comparison, active feedback, and a versioned policy.
- [x] C2 fails closed on missing quorum, incomplete Case coverage, disagreement, unsafe feedback, comparison incompatibility, Gate removal/failure, metric regression, or invalid references.
- [x] C2 never mutates production state and always emits `data_source=SYNTHETIC_MOCK` and `production_action_allowed=false`.
- [ ] C3 replaces synthetic incidents, reviewer identity, feedback transport, thresholds, persistence, and pilot operations only after the required real inputs and team approval exist.

### Required LLM evidence summary

- [x] A provider-neutral summarizer contract and deterministic Mock implementation accept only validated report projections.
- [x] Strict output validation rejects unknown fields, invented Evidence references, changed verdicts/confidence, unsafe actions, and sensitive data.
- [x] Model failures fall back to the deterministic report without changing investigation success or business state.
- [x] A Volcengine Ark Responses API adapter is isolated behind the port, is not enabled by the default Mock configuration, and records Prompt/model/Token/latency metadata without serializing Prompt or Evidence into Agent events.
- [x] A trusted tenant is atomically charged one summary request and a conservative Token reservation before any provider call; over-budget, duplicate, or ledger-failure paths make zero provider calls and fall back deterministically.
- [x] Success settles actual input/output/total Tokens, while timeout, cancellation, transport failure, or uncertain outcome retains the reservation as unknown cost without automatic retry.
- [x] The full Mock E2E traverses the LLM quota ledger with one request, zero actual Tokens, zero credentials, and zero external-network calls.
- [x] `llm-check` performs a local-only, zero-network validation of the explicit Volcengine mode, API-key presence, model ID, fixed Ark endpoint, timeout, Prompt version, and quota policy without printing the key.
- [x] `llm-smoke` is the only standalone opt-in live-model probe. It uses one synthetic count-only deterministic report, the production SummaryService and an ephemeral quota ledger; it performs no SLS or Feishu calls, prints only bounded metadata, and exits non-zero on fallback or invalid model output.
- [x] A bounded real Ark smoke passed on 2026-09-01 with the approved public endpoint, dedicated model-scoped key, `store=false`, strict JSON Schema, one synthetic count-only input, one provider call, and zero SLS/Feishu calls.
- [ ] Approved production Prompt, calibrated Token/cost policy, retention policy, production-global quota service, real-sample quality evaluation, and Worker/Feishu joint E2E remain deployment inputs rather than smoke-test claims.

### Synthetic LLM-summary safety evaluation

- [x] A strict repository-owned dataset references existing synthetic investigation Cases and covers valid supported/no-spike/incomplete summaries, Provider failure, invented Evidence, invented Recommendation, unsupported-cause selection, unsafe action text, and sensitive outbound input.
- [x] A dedicated offline command executes the current deterministic Eino report path before the production `SummaryService`; it does not change the existing `evaluate` report/replay Schema.
- [x] Every Case reuses `ValidateEngineOutput` before and after enrichment, proves the deterministic report is unchanged, and independently verifies summary Evidence, cause, Recommendation, fallback, metadata, and Provider-call contracts.
- [x] Sensitive deterministic text is rejected before the summarizer call; invalid or unavailable Provider output produces a deterministic fallback and never changes the investigation outcome.
- [x] The report emits exact case pass, production-output, input-privacy, summary-contract, deterministic-integrity, fallback, Provider-call, Token-proxy, credential/network, dataset-version, and fingerprint gates.
- [x] All inputs and Provider behaviors remain synthetic Mock with zero real incidents, expert labels, credentials, external-network calls, and production claims.

### Local Web pilot console while Feishu access is pending

- [x] A `web` command runs the existing Intake, SQLite state machine, Worker, Eino graph, configured SLS executor, configured LLM summarizer, and durable Delivery Worker in one local process.
- [x] The HTTP listener defaults to `127.0.0.1:8080` and rejects non-loopback bind addresses. It is a single-operator pilot surface, not a shared or production web service.
- [x] The browser can submit a strict logical investigation scope, poll `QUEUED/RUNNING/SUCCEEDED/FAILED/CANCELLED/NEEDS_REVIEW`, inspect the bounded report/evidence projection, and invoke the existing view, cancel, expand-window, and rerun actions.
- [x] App, tenant, user, chat, source-message, card-message, and action-event identities are created or fixed by the server. HTTP callers cannot supply or override a Principal or physical SLS resource.
- [x] Mutating HTTP requests require a per-process anti-CSRF token and same-origin request metadata, use bounded strict JSON, and preserve application-level request/action idempotency.
- [x] A local Delivery adapter exercises the same durable queued/running/terminal delivery events and card-rebinding semantics without importing or changing the Feishu SDK adapter.
- [x] Web responses exclude requester identities, provider errors, credentials, raw logs, SQL/SPL, Project, LogStore, and physical resource configuration. Failed investigations expose only a stable user-safe state.
- [x] Mock mode remains the zero-network default. Real SLS and real Ark are enabled only by the existing explicit environment configuration and are reported separately from Feishu validation.
- [x] Passing the local Web acceptance proves the Agent application chain and local interaction loop. It does not prove `im.message.receive_v1`, Feishu OpenID/TenantKey, Reply/Patch OpenAPI, visual card rendering, `card.action.trigger`, or Feishu permission scope.

## 10. Open deployment inputs

- Production module path and repository namespace.
- Production relational database and migration tooling.
- Pilot Feishu app, tenant, users, and groups.
- Pilot SLS endpoint, Project, LogStore, indexed scope/error selectors, error dimension, and instance dimension.
- Approved RAM role and resource-level read-only policy.
- Organization-specific sensitive-value redaction patterns.
- Approved owner/change metadata classification and the production change-system connector contract.
- An approved enterprise Runbook/error-code source, logical-resource authorization model, content owner/revision/approval/expiry/rollback policy, audit and retention rules, and production matching-quality gate.
- Approved Volcengine Ark model/endpoint, API-key custody, Prompt review, Token/cost budget, timeout, and model-output retention policy.
- A real historical-incident dataset and its lawful retention/redaction rules.
- An approved reviewer identity source, reviewer roles, quorum, conflict-resolution process, and feedback retention policy.
- Team-approved quality, safety, latency, and cost thresholds plus the pilot cohort and explicit stop/rollback runbook.

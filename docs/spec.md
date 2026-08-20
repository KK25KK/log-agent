# Evidence-driven Log Agent Specification

| Metadata | Value |
| --- | --- |
| Version | 0.8 |
| Status | M5-B contract frozen; B1 bounded Agent event/version slice and B2 append-only replay history implemented and offline verified; B3 trend comparison, live M4-B/M4-C, and real gray rollout pending |
| Date | 2026-08-20 |

## 1. Overview

The product is a Go service that receives investigation requests from a Feishu enterprise self-built bot and produces evidence-backed findings from Alibaba Cloud SLS data.

Eino is an orchestration adapter, not the business system of record. Investigation state, evidence, authorization, query policy, idempotency, and audit semantics remain owned by the application.

## 2. Goal

Users can ask the bot to investigate an error spike for a known service, environment, and time range. The system resolves that logical scope to an administrator-managed SLS resource, applies authorization and query budgets before any cloud request, and produces a report whose facts reference explicit evidence.

## 3. Scope

- Feishu direct messages and group mentions through a replaceable inbound adapter.
- A credential-free local Feishu mock that exercises normalized intake and durable delivery semantics without importing the Feishu SDK.
- Durable inbound deduplication and asynchronously claimed investigation jobs.
- A trusted requester identity derived from the inbound adapter, never from message text.
- An Eino graph for deterministic planning, query, verification, and reporting.
- An administrator-managed service/environment to SLS resource catalog.
- Default-deny principal-to-resource authorization.
- A fixed, versioned `error_analysis_v2` query template; callers cannot provide raw SQL or SPL.
- Preflight time-window, call-count, row-count, timeout, and concurrency budgets.
- A post-query processed-byte budget used as the initial cost guardrail.
- Index Schema validation before executing analytical queries.
- An official Alibaba Cloud SLS Go SDK adapter using read-only APIs.
- Evidence carrying resource identity, query identity, time range, completeness, scan statistics, and result summary.
- Current-versus-baseline error-pattern share, candidate-new-pattern, and instance-concentration analysis.
- A versioned, administrator-managed change catalog for bounded release/configuration context.
- A deterministic cause-analysis projection with explicit support tests, counter-tests, confidence factors, and limitations.
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
- Planned append-only offline evaluation-run history plus strict replay comparison for later M5-B B2/B3 slices; neither is implemented by B1.

## 4. Non-goals

- Arbitrary model-generated SQL or SPL.
- User-selected Endpoint, Project, LogStore, field name, or query template.
- Multi-Agent orchestration, DeepAgent, or Supervisor patterns.
- SLS write operations, alert mutation, or automatic remediation.
- Token-by-token Feishu streaming cards or high-risk approval actions.
- Raw-log samples in Feishu or model context.
- Claiming that absence from a bounded Top-K result proves historical absence.
- A production-grade notification Outbox with unbounded retry, dead-letter operations, or exactly-once delivery.
- Model-generated findings; M2 reports and recommendations are deterministic. A later optional LLM may only summarize governed evidence.
- A production database migration or a new organization-wide message queue.
- Exact RMB cost prediction; processed bytes are the first cost proxy.
- Cross-process global concurrency quotas; the first implementation limits each worker process.
- Automatic retry of a paid query whose external outcome is unknown.
- Provider exactly-once query execution; SLS does not accept an application idempotency key.
- Treating a correlated release, configuration change, error pattern, or instance as a confirmed root cause.
- SLS version-distribution or first-seen-time queries in the first M3 slice; M3 reuses the existing M2 query budget.
- Live release-platform, configuration-center, CMDB, Trace, metric, error-code, SOP, or service-topology connectors.
- Claiming that synthetic fixtures are historical incidents, expert labels, production accuracy, or permission to start a real gray rollout.
- Real Feishu/SLS/change-platform traffic, credentials, model calls, Prompt quality, Token accounting, or production SLO validation in M5-A.
- Claiming that the first Agent Trace is a distributed Feishu-to-delivery production Trace; it covers only the synthetic evaluation and Engine boundary.
- Reusing query audit or query checkpoints as generic tracing storage, or allowing telemetry to change retry, recovery, authorization, or investigation state.
- Raw messages, identities, resources, SQL, log content, bucket labels, change summaries, natural-language findings/recommendations, callback payloads, provider errors, model inputs, or arbitrary attributes in Agent events.
- A live telemetry backend, production sampling/retention policy, host identity, real LLM/Token telemetry, historical-binary execution, or production latency SLO in M5-B.

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
```

The M3 Change Source is enrichment-only. It is called only after governed SLS evidence has established the resource identity and a conclusive spike. Change-source absence or failure cannot erase an M2 fact or fail the investigation; it produces an explicit unavailable or inconclusive cause-analysis status.

### Framework boundary

Only the Eino orchestration adapter imports Eino packages. Only the Feishu adapter imports Feishu SDK packages. Only the Alibaba SLS adapter imports `github.com/aliyun/aliyun-log-go-sdk`. External SDK request and response types cannot escape their adapter packages.

### Identity boundary

`Principal` consists of the source app, tenant, and user identifiers. `Intake` overwrites any requester value carried by a caller with identity derived from `InboundMessage`. Message text and model output can never select a principal.

### Resource and query boundary

The resource catalog is a versioned JSON configuration managed by operators. It maps a unique `(service, environment)` pair to an HTTPS endpoint, Project, LogStore, fixed scope selectors, one required and separate error selector, analytical dimension, and template version. Credentials are never stored in this file.

The only production template in this version is `error_analysis_v2`. It performs four bounded, read-only aggregate requests for each observation: count before, Top 5 configured error dimensions, Top 5 configured instance dimensions, and count after. A resource therefore owns both `error_field` and `instance_field`, and both must be indexed text fields with statistics enabled. Users and models cannot submit provider query strings.

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

## 6. User and system workflows

### Submit an investigation

1. The receiver normalizes an inbound message.
2. Admission policy validates the command syntax and required scope.
3. `Intake` derives the trusted principal from app, tenant, and user IDs.
4. The application writes the inbox record, investigation, and queued job atomically.
5. Repeated source-message IDs return the original investigation ID without creating another job.
6. The receiver finishes; the worker owns all slow work.

### Execute an investigation

1. A worker claims the oldest runnable job using a time-limited lease.
2. The graph creates current and baseline typed `error_analysis_v2` requests.
3. The query gateway resolves the resource and authorizes the trusted principal.
4. Preflight budgets and index Schema are checked.
5. A durable `STARTED` audit event is written before the first `GetLogsV2` log query is called. Schema metadata may be fetched before this event.
6. The fixed template obtains total count, Top 5 error patterns, and Top 5 instances without returning raw log bodies.
7. Provider progress and scan metadata are normalized. `Incomplete`, timeout, provider truncation, missing usage metadata, or processed-byte overflow cannot produce a conclusive finding.
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

An explicit diagnostic command loads the same catalog and credentials as a real worker, verifies configured Projects, LogStores, and indexes, and prints only non-secret metadata. A separate smoke command runs one authorized fixed-template query. Neither command is run implicitly by the offline demo or test suite.

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
9. B3 compares only runs with compatible dataset identity, data boundary, and executor profile; incompatible runs return `INCOMPARABLE` rather than a misleading delta.

## 7. Behavioral contracts and lifecycle

Investigation states are `QUEUED`, `RUNNING`, `SUCCEEDED`, `FAILED`, `CANCELLED`, and `NEEDS_REVIEW`.

Allowed terminal states cannot transition back to running. Each claim increments the attempt count and binds a lease owner and expiry. Renewal and completion require both the active lease owner and the active attempt fencing token, so a stale process cannot submit through a newer claim that reused the same worker ID.

Provider `progress`, usage, and nanosecond-order metadata are preserved. `complete=true` is derived only from provider `Complete` plus local structural, usage, and budget gates. A finding is conclusive only when all referenced evidence is complete and not truncated. Otherwise the report must describe the result as insufficient data.

Near-real-time requests use a configured ingestion watermark. The default command scope ends ten seconds before the message timestamp, and the query gateway rejects a scope whose end has not crossed that watermark. Matching count-before/count-after values are additionally required for a complete multi-query observation. The watermark is an operator-owned ingestion-latency assumption and must be calibrated against the pilot pipeline.

The provider `isAccurate` field is a nanosecond-order option, not an analytical accuracy signal. It is retained only as ordering metadata and is never used to promote a finding. Provider completeness is governed by `progress` plus local usage, structural, and budget checks.

Redaction changes only the displayed aggregate label; it does not change the aggregate counts or automatically make otherwise complete evidence inconclusive. The `Redacted` marker remains on Evidence so readers know that a label was transformed.

Query audit is append-only and excludes credentials, raw log bodies, and raw provider query strings. A query-spec fingerprint includes the resolved resource, template and policy versions, selectors, time range, and enforced limits.

Notification delivery states are `PENDING`, `RUNNING`, `SENT`, and `DEAD`. Claims use owner, lease expiry, and attempt fencing. A delivery failure never rolls back or changes the investigation business state. M2 uses a small bounded retry policy; production retry classification, operational replay, and dead-letter tooling remain future work.

M2 runs one Feishu/delivery process. Database fencing protects local claims and stale attempts, but globally ordered remote patches from multiple delivery processes require the production outbox/dispatcher work planned for M4.

M4-A persists one checkpoint per logical SLS window. Its input hash combines the immutable logical `QuerySpec` with a governed fingerprint over the catalog and physical resource, selectors, schema, template, policy, and budgets. Confirmed successful results are reusable only while that governance identity still matches, and current/baseline Evidence with different governance identities cannot produce a report. An abandoned `STARTED` metered step is never automatically retried. Other executor errors remain terminal until the later M4 retry-classification slice is implemented. Paid POST queries disable the SDK's server-error retry; metadata GET transport retries are bounded by the configured request timeout. The system does not claim exactly-once SLS query execution.

Existing queued records created before trusted principals were persisted decode with an empty principal and must fail closed in real-SLS mode. The offline mock remains backward compatible.

`Report.CauseAnalysis` is optional for backward compatibility. When present it has one of `COMPLETE`, `INCONCLUSIVE`, `UNAVAILABLE`, or `SKIPPED_NO_SPIKE`. A hypothesis verdict is `SUPPORTED_CANDIDATE`, `REFUTED`, or `INCONCLUSIVE`; it is never promoted to a conclusive Finding.

Every hypothesis has at least one support test and one counter-test. Each test references only Evidence IDs and Change Event IDs stored in the same report. Test results are `PASS`, `FAIL`, or `UNKNOWN`; missing, truncated, non-exhaustive, or redacted inputs produce `UNKNOWN`, not an inferred absence.

Cause confidence uses the versioned deterministic method `change-correlation-v1`, is not a probability, and is capped at `0.85`. A supported candidate requires a conclusive M2 spike, temporal precedence, affected-instance concentration of at least 50%, an increase of at least 20 percentage points from baseline, a complete change set, and no passing counter-test. A complete affected-instance set with complete comparable current distribution and zero overlap is a hard refutation. Multiple overlapping changes are confounding evidence and force `INCONCLUSIVE`.

Change Source errors, disabled configuration, or incomplete source coverage never turn an otherwise valid M2 report into a failed investigation.

M5-A is a deterministic engineering regression gate, not a production-readiness decision. Outcome, Finding, Recommendation, and cause-verdict agreement are measured only against repository-owned synthetic labels. Recommendation matching is exact by code and Evidence name, so an omitted, injected, duplicated, or misgrounded next step fails closed. Evidence coverage verifies reference integrity, not factual completeness outside the fixture. Processed bytes and fixed Provider-call counts are cost proxies rather than an Alibaba Cloud bill. Local elapsed time is recorded for trend inspection but is not a production latency SLO. Prompt and Token metrics are not applicable while the graph remains model-free.

M5-B Agent events use a fixed schema and closed layer/name/phase enums. The initial hierarchy is evaluation run -> Case Trace -> Engine run -> Graph node or Mock tool. Each span has exactly one start and one terminal phase, parent references form an acyclic closed Trace, sequence numbers are unique and increasing, and terminal usage must agree with the governed evaluation counters. Events contain no arbitrary key/value map or raw error text. Safe failure classification is observational only and cannot alter M4 retry or recovery semantics.

The runtime-version fingerprint is computed from a normalized manifest, not from host identity or wall-clock values. Synthetic and production query policies remain distinct through `executor_profile`; `prompt_used=false` requires Prompt/model/Token fields to remain absent or explicitly not applicable. Hashes provide integrity and correlation, not anonymization.

The B1 gate version is `m5b-agent-trace-gate-v1`, the Agent event/Trace schema is `agent-trace-v1`, and the replay snapshot schema is `evaluation-replay-v1`. B1 and B2 execute only under `executor_profile=SYNTHETIC_MOCK`. B2 uses a dedicated evaluation-run store and never extends or reuses the production investigation Store, Query Audit, or QueryStep contracts. Snapshot hashes provide tamper detection, not confidentiality; the Trace and report privacy contracts still apply before persistence.

## 8. Constraints and compatibility

- Eino is pinned to `v0.9.14`; pre-release APIs are excluded.
- Feishu SDK is pinned to `v3.9.10`.
- Alibaba SLS Go SDK is pinned to `v0.1.126`.
- Real catalog endpoints must use `https://`; same-region VPC endpoints are recommended for deployment.
- Credentials come from environment-provided STS/AccessKey values or an ECS RAM role provider. The ECS provider uses IMDSv2 hardened metadata access. Credentials and tokens must never be logged.
- The official SLS Go SDK query methods do not accept caller contexts. The adapter disables SDK retries for server errors and enforces a fixed HTTP client timeout; it must not claim immediate transport cancellation.
- Application context is checked before SDK calls and again before accepting returned results, and it controls policy waits. Immediate in-flight SLS request cancellation requires a separately reviewed context-aware transport or upstream SDK support.
- SLS `limited` metadata represents the SQL result-row limit and is not itself evidence of truncation. Every fixed aggregate SQL statement owns an explicit `LIMIT`.
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

### Read-only SLS query foundation

- [x] The official SLS Go SDK exists only inside its adapter package.
- [x] The default demo remains offline and produces deterministic facts.
- [x] A unique service/environment mapping resolves to one configured HTTPS SLS target.
- [x] Every resource has an explicit error selector separate from its scope selectors.
- [x] A trusted principal is derived from the inbound envelope and cannot be forged in the request.
- [x] Unknown resources, missing ACL bindings, unsafe windows, and invalid policy are rejected before log-query execution.
- [x] Missing indexes or analytical fields without statistics enabled are rejected before the fixed query executes.
- [x] The fixed template preserves provider request IDs, progress, nanosecond-order metadata, processed rows, processed bytes, and elapsed time.
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
- [x] Automatic transient retries, operator resolution of unknown steps, delivery dead-letter replay, durable tenant quotas, approvals, and a production database remain explicitly deferred to later M4 slices.

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
- [ ] B3 compares compatible replay snapshots, reports version changes, quality/cost/tool/Trace regressions and recovered or newly failed Cases, and returns `INCOMPARABLE` for incompatible data boundaries.
- [ ] Live telemetry export, production sampling/retention, real LLM/Token observability, historical build execution, real incident feedback, and production SLO approval remain explicitly deferred.

## 10. Open deployment inputs

- Production module path and repository namespace.
- Production relational database and migration tooling.
- Pilot Feishu app, tenant, users, and groups.
- Pilot SLS endpoint, Project, LogStore, indexed scope/error selectors, error dimension, and instance dimension.
- Approved RAM role and resource-level read-only policy.
- Organization-specific sensitive-value redaction patterns.
- Approved owner/change metadata classification and the production change-system connector contract.

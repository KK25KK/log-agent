# Evidence-driven Log Agent Specification

| Metadata | Value |
| --- | --- |
| Version | 0.4 |
| Status | M3 implemented and verified offline; live integrations pending |
| Date | 2026-08-19 |

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
- Automatic retry of paid queries before step-level idempotency is available.
- Treating a correlated release, configuration change, error pattern, or instance as a confirmed root cause.
- SLS version-distribution or first-seen-time queries in the first M3 slice; M3 reuses the existing M2 query budget.
- Live release-platform, configuration-center, CMDB, Trace, metric, error-code, SOP, or service-topology connectors.

## 5. Core design and architecture

```text
Feishu Receiver
    -> Durable Inbox
    -> Investigation Job with trusted Principal
    -> Worker
    -> InvestigationEngine interface
    -> Eino deterministic graph
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
    -> view evidence | cancel | expand window | rerun
    -> durable state transition or derived investigation
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
7. Replayed callbacks reuse the callback event ID as the durable inbound idempotency key and cannot create duplicate investigations.
8. Mutating callbacks return only a toast. Their card projection is serialized through the durable delivery worker, which is the sole writer of business-state card updates.

### Validate a real SLS configuration

An explicit diagnostic command loads the same catalog and credentials as a real worker, verifies configured Projects, LogStores, and indexes, and prints only non-secret metadata. A separate smoke command runs one authorized fixed-template query. Neither command is run implicitly by the offline demo or test suite.

### Recover work

- An expired running lease makes the job claimable again.
- A running worker renews its lease and observes durable cancellation on the heartbeat boundary.
- A cancelled investigation cannot start new work.
- A duplicate inbound message does not create duplicate investigations or jobs.
- Framework checkpoints are optional execution snapshots, not business facts.

### Correlate a governed change

1. The deterministic M2 graph first produces complete current and baseline Evidence.
2. Cause analysis runs only for a conclusive `spike_detected` report.
3. The change query obtains `resource_id` and its time range from governed Evidence, never from message text or a card value.
4. The read-only Change Source returns at most ten release/configuration events plus source completeness metadata.
5. Each candidate is evaluated by fixed support and counter-tests. Unknown coverage remains `INCONCLUSIVE`; absence from a bounded Top-K set is never treated as counterevidence.
6. The report stores the selected change metadata, hypotheses, test results, Evidence references, confidence method, and limitations.
7. Feishu presents the result as a correlation candidate and explicitly states that correlation is not causal proof.

## 7. Behavioral contracts and lifecycle

Investigation states are `QUEUED`, `RUNNING`, `SUCCEEDED`, `FAILED`, and `CANCELLED`.

Allowed terminal states cannot transition back to running. Each claim increments the attempt count and binds a lease owner and expiry. Renewal and completion require both the active lease owner and the active attempt fencing token, so a stale process cannot submit through a newer claim that reused the same worker ID.

Provider `progress`, usage, and nanosecond-order metadata are preserved. `complete=true` is derived only from provider `Complete` plus local structural, usage, and budget gates. A finding is conclusive only when all referenced evidence is complete and not truncated. Otherwise the report must describe the result as insufficient data.

Near-real-time requests use a configured ingestion watermark. The default command scope ends ten seconds before the message timestamp, and the query gateway rejects a scope whose end has not crossed that watermark. Matching count-before/count-after values are additionally required for a complete multi-query observation. The watermark is an operator-owned ingestion-latency assumption and must be calibrated against the pilot pipeline.

The provider `isAccurate` field is a nanosecond-order option, not an analytical accuracy signal. It is retained only as ordering metadata and is never used to promote a finding. Provider completeness is governed by `progress` plus local usage, structural, and budget checks.

Redaction changes only the displayed aggregate label; it does not change the aggregate counts or automatically make otherwise complete evidence inconclusive. The `Redacted` marker remains on Evidence so readers know that a label was transformed.

Query audit is append-only and excludes credentials, raw log bodies, and raw provider query strings. A query-spec fingerprint includes the resolved resource, template and policy versions, selectors, time range, and enforced limits.

Notification delivery states are `PENDING`, `RUNNING`, `SENT`, and `DEAD`. Claims use owner, lease expiry, and attempt fencing. A delivery failure never rolls back or changes the investigation business state. M2 uses a small bounded retry policy; production retry classification, operational replay, and dead-letter tooling remain future work.

M2 runs one Feishu/delivery process. Database fencing protects local claims and stale attempts, but globally ordered remote patches from multiple delivery processes require the production outbox/dispatcher work planned for M4.

Executor errors remain terminal failures. Application-level retry is intentionally deferred until step-level idempotency keys and error classification are introduced; paid POST queries disable the SDK's server-error retry. SDK metadata GET transport retries are bounded by the configured request timeout. The system does not claim exactly-once SLS query execution.

Existing queued records created before trusted principals were persisted decode with an empty principal and must fail closed in real-SLS mode. The offline mock remains backward compatible.

`Report.CauseAnalysis` is optional for backward compatibility. When present it has one of `COMPLETE`, `INCONCLUSIVE`, `UNAVAILABLE`, or `SKIPPED_NO_SPIKE`. A hypothesis verdict is `SUPPORTED_CANDIDATE`, `REFUTED`, or `INCONCLUSIVE`; it is never promoted to a conclusive Finding.

Every hypothesis has at least one support test and one counter-test. Each test references only Evidence IDs and Change Event IDs stored in the same report. Test results are `PASS`, `FAIL`, or `UNKNOWN`; missing, truncated, non-exhaustive, or redacted inputs produce `UNKNOWN`, not an inferred absence.

Cause confidence uses the versioned deterministic method `change-correlation-v1`, is not a probability, and is capped at `0.85`. A supported candidate requires a conclusive M2 spike, temporal precedence, affected-instance concentration of at least 50%, an increase of at least 20 percentage points from baseline, a complete change set, and no passing counter-test. A complete affected-instance set with complete comparable current distribution and zero overlap is a hard refutation. Multiple overlapping changes are confounding evidence and force `INCONCLUSIVE`.

Change Source errors, disabled configuration, or incomplete source coverage never turn an otherwise valid M2 report into a failed investigation.

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

## 10. Open deployment inputs

- Production module path and repository namespace.
- Production relational database and migration tooling.
- Pilot Feishu app, tenant, users, and groups.
- Pilot SLS endpoint, Project, LogStore, indexed scope/error selectors, error dimension, and instance dimension.
- Approved RAM role and resource-level read-only policy.
- Organization-specific sensitive-value redaction patterns.
- Approved owner/change metadata classification and the production change-system connector contract.

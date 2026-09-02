package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"logagent/internal/adapters/sqlite"
	"logagent/internal/adapters/tracemock"
	"logagent/internal/adapters/traceresourcecatalog"
	"logagent/internal/application"
	traceapp "logagent/internal/application/trace"
	"logagent/internal/domain"
	"logagent/internal/fingerprint"
)

type traceDeploymentSource struct{ deployment domain.DeploymentEvidence }

func (source traceDeploymentSource) ResolveDeployment(context.Context, domain.DeploymentQuery) (domain.DeploymentEvidence, error) {
	return source.deployment, nil
}

type traceCodeProvider struct{ calls int }

func (provider *traceCodeProvider) SearchCode(_ context.Context, request domain.CodeSearchRequest) (domain.CodeSearchResult, error) {
	provider.calls++
	var stackAnchor *domain.RuntimeAnchor
	for index := range request.Anchors {
		if request.Anchors[index].Kind == domain.RuntimeAnchorStackFrame {
			stackAnchor = &request.Anchors[index]
			break
		}
	}
	matches := []domain.CodeMatch{}
	linesReturned, bytesReturned, filesRead := 0, 0, 0
	if stackAnchor != nil {
		match := domain.CodeMatch{
			Kind: domain.CodeMatchStackFrame, AnchorID: stackAnchor.ID, RepositoryID: request.RepositoryID,
			CommitSHA: request.CommitSHA, File: stackAnchor.File, StartLine: stackAnchor.Line, EndLine: stackAnchor.Line,
			MatchLine: stackAnchor.Line, Symbol: stackAnchor.Symbol, Snippet: "return fmt.Errorf(\"payment timeout\")", BlobHash: strings.Repeat("c", 40),
		}
		match.QueryFingerprint, _ = fingerprint.JSON(struct{ AnchorID, RepositoryID, CommitSHA string }{stackAnchor.ID, request.RepositoryID, request.CommitSHA})
		match.ContentFingerprint, _ = domain.CodeMatchFingerprint(match)
		digest := sha256.Sum256([]byte(match.AnchorID + "|" + match.QueryFingerprint + "|" + match.ContentFingerprint))
		match.ID = "code_" + hex.EncodeToString(digest[:12])
		matches = append(matches, match)
		linesReturned, bytesReturned, filesRead = 1, len(match.Snippet), 1
	}
	return domain.CodeSearchResult{
		Version: domain.CodeEvidenceVersion, PolicyVersion: domain.CodeSearchPolicyVersion,
		RepositoryID: request.RepositoryID, CommitSHA: request.CommitSHA, Complete: true,
		Matches: matches, AnchorsSearched: len(request.Anchors), FilesRead: filesRead,
		LinesReturned: linesReturned, BytesReturned: bytesReturned, CommandsRun: 2,
	}, nil
}

func TestTraceWorkerBuildsEightMemberTimelinePrimaryFirstWithinConcurrencyBudget(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1788330010, 0).UTC()
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "operator"}
	group := eightMemberTraceGroup()
	catalog, err := traceresourcecatalog.New(
		[]domain.TraceResourceGroup{group}, map[domain.Principal][]string{principal: {group.ID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := tracemock.NewWithDelay(15 * time.Millisecond)
	gateway, err := traceapp.NewGateway(catalog, backend, store, traceapp.Budget{
		MaxWindow: 30 * time.Minute, IngestionGrace: 10 * time.Second, Timeout: time.Second,
		MemberLimit: 50, GlobalLimit: 500, MaxProcessedBytes: 1024 * 1024, MaxConcurrency: 2, RetryIncomplete: 1,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	engine, err := application.NewTraceEngine(gateway, store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	end := now.Add(-10 * time.Second)
	investigationID, _, err := application.NewIntake(store).Accept(ctx, domain.InboundMessage{
		AppID: principal.AppID, TenantKey: principal.TenantKey, UserID: principal.UserID,
		MessageID: "trace-message", ChatID: "chat", ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service: group.Service, Environment: group.Environment, TemplateID: domain.TraceSearchTemplateID,
		TraceID: "trace-12345678", StartTime: end.Add(-10 * time.Minute), EndTime: end,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := domain.DeploymentEvidence{
		Version: domain.DeploymentEvidenceVersion, Status: domain.DeploymentComplete,
		Service: group.Service, Environment: group.Environment, RepositoryID: "dam", CommitSHA: strings.Repeat("a", 40),
		DeployedAt: end.Add(-time.Hour), SourceVersion: "catalog-v1",
	}
	deployment.Fingerprint, _ = domain.DeploymentEvidenceFingerprint(deployment)
	codeProvider := &traceCodeProvider{}
	codeService, err := application.NewCodeEvidenceService(
		traceDeploymentSource{deployment: deployment}, codeProvider, time.Second,
		application.WithCodeEvidenceClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := application.NewWorker(
		store, engine, "trace-worker", time.Minute,
		application.WithWorkerClock(func() time.Time { return now }), application.WithWorkerCodeEvidence(codeService),
		application.WithWorkerJointRCA(application.NewJointRCAService(func() time.Time { return now })),
	)
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := worker.RunOne(ctx); err != nil || !ran {
		t.Fatalf("run Trace worker: ran=%v err=%v", ran, err)
	}
	item, err := store.GetInvestigation(ctx, investigationID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusSucceeded || item.Report == nil || item.Report.TraceInvestigation == nil {
		t.Fatalf("Trace investigation did not succeed: %#v", item)
	}
	if codeProvider.calls != 1 || item.Report.CodeInvestigation == nil || item.Report.CodeInvestigation.Status != domain.CodeInvestigationComplete {
		t.Fatalf("governed code evidence did not traverse the Worker: calls=%d value=%#v", codeProvider.calls, item.Report.CodeInvestigation)
	}
	if item.Report.JointRCA == nil || item.Report.JointRCA.Status != domain.JointRCAComplete || len(item.Report.JointRCA.Candidates) != 1 || item.Report.JointRCA.Candidates[0].Verdict != domain.JointRCASupportedCandidate {
		t.Fatalf("joint RCA did not traverse the Worker: %#v", item.Report.JointRCA)
	}
	trace := item.Report.TraceInvestigation
	if trace.Status != domain.TraceInvestigationComplete || len(trace.Members) != 8 || len(trace.Events) != 2 || len(item.Report.Evidence) != 8 ||
		trace.AnchorSet == nil || trace.AnchorSet.Status != domain.RuntimeAnchorsComplete || len(trace.AnchorSet.Anchors) < 4 {
		t.Fatalf("unexpected Trace report: %#v", trace)
	}
	if !trace.Events[0].EventTime.Before(trace.Events[1].EventTime) {
		t.Fatalf("Trace events are not chronologically sorted: %#v", trace.Events)
	}
	calls := backend.Calls()
	if len(calls) != 8 || calls[0] != group.PrimaryMemberID || backend.MaxActive() > 2 || backend.MaxActive() < 2 {
		t.Fatalf("query scheduling violated policy: calls=%v max_active=%d", calls, backend.MaxActive())
	}
	stepCount, err := store.CountTraceQuerySteps(ctx, investigationID)
	if err != nil || stepCount != 8 {
		t.Fatalf("Trace checkpoints: count=%d err=%v", stepCount, err)
	}
	audits, err := store.ListTraceAudits(ctx, investigationID)
	if err != nil || len(audits) != 16 {
		t.Fatalf("Trace audit coverage: count=%d err=%v", len(audits), err)
	}
	encoded, err := json.Marshal(item.Report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "trace-12345678") || !strings.Contains(string(encoded), "[TRACE_ID]") {
		t.Fatalf("persisted report did not protect TraceID: %s", encoded)
	}
}

func eightMemberTraceGroup() domain.TraceResourceGroup {
	ids := []string{"dam-server", "dam-consume", "dam-consume-fast", "dam-cron", "dam-consume-audio", "dam-consume-2d", "dam-consume-video", "dam-consume-ingest"}
	members := make([]domain.TraceResourceMember, 0, len(ids))
	for index, id := range ids {
		member := domain.TraceResourceMember{
			ID: id, Endpoint: "https://cn-shanghai.log.aliyuncs.com", Project: "tech-center-sha",
			LogStore: "logstore-" + id, TraceMode: domain.TraceQueryFullText,
			EnvironmentMode: domain.TraceEnvironmentField, EnvironmentField: "env", MessageField: "msg",
			LevelField: "level", EventTimeField: "__time__", NanosecondTimeField: "__time_ns_part__",
		}
		if index >= 6 {
			member.EnvironmentMode = domain.TraceEnvironmentFullText
			member.EnvironmentField = ""
			member.MessageField = "__raw__"
			member.LevelField = ""
		}
		members = append(members, member)
	}
	return domain.TraceResourceGroup{
		ID: "dam-trace-test", CatalogVersion: "test-v1", Service: "dam-server", Environment: "test",
		TemplateVersion: domain.TraceSearchTemplateVersion, PrimaryMemberID: "dam-server", Members: members,
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"logagent/internal/adapters/aliyuncli"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/adapters/tracemock"
	"logagent/internal/adapters/traceresourcecatalog"
	"logagent/internal/application"
	traceapp "logagent/internal/application/trace"
	"logagent/internal/config"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

func buildTraceEngine(loaded config.Config, store *sqlite.Store) (ports.InvestigationEngine, ports.TraceResourceCatalog, error) {
	if loaded.Trace.Mode == "disabled" {
		return nil, nil, nil
	}
	catalog, backend, err := buildTraceDependencies(loaded)
	if err != nil {
		return nil, nil, err
	}
	gateway, err := traceapp.NewGateway(catalog, backend, store, traceBudget(loaded.Trace), time.Now)
	if err != nil {
		return nil, nil, err
	}
	engine, err := application.NewTraceEngine(gateway, store, time.Now)
	if err != nil {
		return nil, nil, err
	}
	return engine, catalog, nil
}

func buildTraceDependencies(loaded config.Config) (*traceresourcecatalog.Catalog, ports.TraceBackend, error) {
	catalog, err := buildTraceCatalog(loaded)
	if err != nil {
		return nil, nil, err
	}
	switch loaded.Trace.Mode {
	case "mock":
		return catalog, tracemock.New(), nil
	case "aliyun":
		backend, err := aliyuncli.New(aliyuncli.Config{
			CLIPath: loaded.SLS.CLIPath, Profile: loaded.SLS.CLIProfile,
			RequestTimeout: loaded.SLS.RequestTimeout, MaxOutputBytes: loaded.SLS.CLIMaxOutputBytes,
		})
		if err != nil {
			return nil, nil, err
		}
		return catalog, backend, nil
	default:
		return nil, nil, errors.New("Trace dependencies require LOG_AGENT_TRACE_MODE=mock or aliyun")
	}
}

func buildTraceCatalog(loaded config.Config) (*traceresourcecatalog.Catalog, error) {
	switch loaded.Trace.Mode {
	case "mock":
		principal := domain.Principal{AppID: loaded.Web.AppID, TenantKey: loaded.Web.TenantKey, UserID: loaded.Web.UserID}
		return traceresourcecatalog.New(
			[]domain.TraceResourceGroup{mockDAMTraceGroup()},
			map[domain.Principal][]string{principal: {"dam-trace-test"}},
		)
	case "aliyun":
		return traceresourcecatalog.Load(loaded.Trace.CatalogPath)
	default:
		return nil, errors.New("Trace catalog requires LOG_AGENT_TRACE_MODE=mock or aliyun")
	}
}

func traceBudget(loaded config.TraceConfig) traceapp.Budget {
	return traceapp.Budget{
		MaxWindow: loaded.MaxWindow, IngestionGrace: loaded.IngestionGrace, Timeout: loaded.QueryTimeout,
		MemberLimit: loaded.MemberLimit, GlobalLimit: loaded.GlobalLimit, MaxProcessedBytes: loaded.MaxProcessedBytes,
		MaxConcurrency: loaded.MaxConcurrency, RetryIncomplete: loaded.RetryIncomplete,
	}
}

func runTraceCheck(loaded config.Config) error {
	if loaded.Trace.Mode != "aliyun" {
		return errors.New("trace-check requires LOG_AGENT_TRACE_MODE=aliyun")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	catalog, backend, err := buildTraceDependencies(loaded)
	if err != nil {
		return err
	}
	type memberCheck struct {
		GroupID           string `json:"group_id"`
		MemberID          string `json:"member_id"`
		Primary           bool   `json:"primary"`
		SchemaFingerprint string `json:"schema_fingerprint"`
		IndexedFields     int    `json:"indexed_fields"`
		Status            string `json:"status"`
	}
	checks := make([]memberCheck, 0)
	for _, group := range catalog.Groups() {
		for _, member := range group.Members {
			schema, err := backend.GetTraceSchema(ctx, member)
			if err != nil {
				return fmt.Errorf("check Trace member %q: %w", member.ID, err)
			}
			if err := traceapp.ValidateConfiguredMemberSchema(member, schema); err != nil {
				return fmt.Errorf("check Trace member %q: %w", member.ID, err)
			}
			checks = append(checks, memberCheck{
				GroupID: group.ID, MemberID: member.ID, Primary: member.ID == group.PrimaryMemberID,
				SchemaFingerprint: schema.Fingerprint, IndexedFields: len(schema.Fields), Status: "READY",
			})
		}
	}
	return printJSON(map[string]any{"status": "READY", "log_reads": 0, "members": checks})
}

func runTraceSmoke(loaded config.Config, service, environment, rawWindow, traceID string) error {
	if loaded.Trace.Mode != "aliyun" && loaded.Trace.Mode != "mock" {
		return errors.New("trace-smoke requires LOG_AGENT_TRACE_MODE=mock or aliyun")
	}
	window, err := time.ParseDuration(rawWindow)
	if err != nil || window <= 0 || window > loaded.Trace.MaxWindow {
		return errors.New("trace-smoke duration must be positive and within LOG_AGENT_TRACE_MAX_WINDOW")
	}
	principal := domain.Principal{AppID: loaded.SmokePrincipal.AppID, TenantKey: loaded.SmokePrincipal.TenantKey, UserID: loaded.SmokePrincipal.UserID}
	if loaded.Trace.Mode == "mock" {
		principal = domain.Principal{AppID: loaded.Web.AppID, TenantKey: loaded.Web.TenantKey, UserID: loaded.Web.UserID}
	}
	if !principal.Complete() {
		return errors.New("trace-smoke requires the configured smoke principal")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		return err
	}
	defer store.Close()
	engine, _, err := buildTraceEngine(loaded, store)
	if err != nil {
		return err
	}
	workerOptions := make([]application.WorkerOption, 0, 1)
	codeEvidence, err := buildCodeEvidenceService(loaded)
	if err != nil {
		return err
	}
	if codeEvidence != nil {
		workerOptions = append(workerOptions, application.WithWorkerCodeEvidence(codeEvidence))
		workerOptions = append(workerOptions, application.WithWorkerJointRCA(application.NewJointRCAService(time.Now)))
	}
	worker, err := application.NewWorker(store, engine, "trace-smoke-worker", time.Minute, workerOptions...)
	if err != nil {
		return err
	}
	end := time.Now().UTC().Add(-loaded.Trace.IngestionGrace).Truncate(time.Second)
	intake := application.NewIntake(store)
	investigationID, _, err := intake.Accept(ctx, domain.InboundMessage{
		AppID: principal.AppID, TenantKey: principal.TenantKey, UserID: principal.UserID,
		MessageID: fmt.Sprintf("trace-smoke-%d", time.Now().UnixNano()), ChatID: "trace-smoke", ReceivedAt: time.Now().UTC(),
	}, domain.InvestigationRequest{
		Service: service, Environment: environment, TemplateID: domain.TraceSearchTemplateID,
		TraceID: traceID, StartTime: end.Add(-window), EndTime: end,
	})
	if err != nil {
		return err
	}
	if _, err := worker.RunOne(ctx); err != nil {
		return err
	}
	investigation, err := store.GetInvestigation(ctx, investigationID)
	if err != nil {
		return err
	}
	return printJSON(investigation)
}

func mockDAMTraceGroup() domain.TraceResourceGroup {
	names := []struct {
		id       string
		logstore string
		fullEnv  bool
	}{
		{"dam-server", "2016-hyper-dam-file", false},
		{"dam-consume", "2160-hyper-dam-consume-file", false},
		{"dam-consume-fast", "2264-hyper-dam-consume-fast-file", false},
		{"dam-cron", "2280-hyper-dam-cron-file", false},
		{"dam-consume-audio", "2541-hyper-dam-consume-audio-file", false},
		{"dam-consume-2d", "2739-hyper-dam-consume-2d-file", false},
		{"dam-consume-video", "2904-hyper-dam-consume-video-file", true},
		{"dam-consume-ingest", "2931-hyper-dam-consume-ingest-file", true},
	}
	members := make([]domain.TraceResourceMember, 0, len(names))
	for _, item := range names {
		member := domain.TraceResourceMember{
			ID: item.id, Endpoint: "https://cn-shanghai.log.aliyuncs.com", Project: "tech-center-sha", LogStore: item.logstore,
			TraceMode: domain.TraceQueryFullText, EnvironmentMode: domain.TraceEnvironmentField, EnvironmentField: "env",
			MessageField: "msg", LevelField: "level", EventTimeField: "__time__", NanosecondTimeField: "__time_ns_part__",
		}
		if item.fullEnv {
			member.EnvironmentMode = domain.TraceEnvironmentFullText
			member.EnvironmentField = ""
			member.MessageField = "__raw__"
			member.LevelField = ""
		}
		members = append(members, member)
	}
	return domain.TraceResourceGroup{
		ID: "dam-trace-test", CatalogVersion: "trace-mock-v1", Service: "dam-server", Environment: "test",
		TemplateVersion: domain.TraceSearchTemplateVersion, PrimaryMemberID: "dam-server", Members: members,
	}
}

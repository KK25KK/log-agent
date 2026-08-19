package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/adapters/feishu"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/config"
	"logagent/internal/domain"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("logagent: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: logagent <mock-e2e|demo|worker|feishu|sls-check|sls-smoke>")
	}
	switch args[0] {
	case "mock-e2e":
		if len(args) != 1 {
			return errors.New("usage: logagent mock-e2e")
		}
		return runMockE2E()
	case "demo":
		if len(args) != 1 {
			return errors.New("usage: logagent demo")
		}
		return runDemo()
	case "worker":
		if len(args) != 1 {
			return errors.New("usage: logagent worker")
		}
		loaded, err := config.Load()
		if err != nil {
			return err
		}
		return runWorker(loaded)
	case "feishu":
		if len(args) != 1 {
			return errors.New("usage: logagent feishu")
		}
		loaded, err := config.Load()
		if err != nil {
			return err
		}
		return runFeishu(loaded)
	case "sls-check":
		if len(args) != 1 {
			return errors.New("usage: logagent sls-check")
		}
		loaded, err := config.Load()
		if err != nil {
			return err
		}
		return runSLSCheck(loaded)
	case "sls-smoke":
		if len(args) != 4 {
			return errors.New("usage: logagent sls-smoke <service> <environment> <duration>")
		}
		loaded, err := config.Load()
		if err != nil {
			return err
		}
		return runSLSSmoke(loaded, args[1], args[2], args[3])
	default:
		return fmt.Errorf("unknown command %q; use mock-e2e, demo, worker, feishu, sls-check, or sls-smoke", args[0])
	}
}

func runDemo() error {
	ctx := context.Background()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		return err
	}
	defer store.Close()

	fixedEnd := time.Date(2026, 8, 18, 10, 30, 0, 0, time.UTC)
	checkpointedExecutor, err := application.NewCheckpointExecutor(
		newMockExecutor(),
		store,
		func() time.Time { return fixedEnd.Add(time.Second) },
	)
	if err != nil {
		return err
	}
	engine, err := eino.New(
		ctx,
		checkpointedExecutor,
		func() time.Time { return fixedEnd.Add(time.Second) },
		eino.WithChangeSource(demoChangeSource{event: domain.ChangeEvent{
			ID: "chg_demo_release_v2", ResourceID: "mock/order-service/prod", Kind: domain.ChangeKindRelease,
			StartedAt: fixedEnd.Add(-35 * time.Minute), CompletedAt: fixedEnd.Add(-32 * time.Minute),
			FromVersion: "v1", ToVersion: "v2", Owner: "order-team", Summary: "demo release v2",
			AffectedInstances: []string{"order-pod-a"}, AffectedInstancesComplete: true,
		}}),
	)
	if err != nil {
		return err
	}
	intake := application.NewIntake(store)
	investigationID, _, err := intake.Accept(ctx, domain.InboundMessage{
		AppID:      "local",
		TenantKey:  "local",
		MessageID:  "demo-message",
		ChatID:     "demo-chat",
		UserID:     "demo-user",
		Text:       "/investigate order-service prod 30m",
		ReceivedAt: fixedEnd,
	}, domain.InvestigationRequest{
		Service:     "order-service",
		Environment: "prod",
		StartTime:   fixedEnd.Add(-30 * time.Minute),
		EndTime:     fixedEnd,
	})
	if err != nil {
		return err
	}
	worker, err := application.NewWorker(
		store,
		engine,
		"demo-worker",
		time.Minute,
		application.WithWorkerClock(func() time.Time { return fixedEnd.Add(time.Second) }),
	)
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
	output, err := json.MarshalIndent(investigation, "", "  ")
	if err != nil {
		return fmt.Errorf("encode demo output: %w", err)
	}
	fmt.Println(string(output))
	return nil
}

func runWorker(config config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := sqlite.Open(config.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	executor, err := buildWorkerExecutor(config, store)
	if err != nil {
		return err
	}
	checkpointedExecutor, err := application.NewCheckpointExecutor(executor, store, time.Now)
	if err != nil {
		return err
	}
	changeSource, err := buildChangeSource(config)
	if err != nil {
		return err
	}
	engine, err := eino.New(ctx, checkpointedExecutor, time.Now, eino.WithChangeSource(changeSource))
	if err != nil {
		return err
	}
	worker, err := application.NewWorker(store, engine, config.WorkerID, config.WorkerLease)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(config.WorkerPoll)
	defer ticker.Stop()
	for {
		run, runErr := worker.RunOne(ctx)
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			log.Printf("worker run failed: %v", runErr)
		}
		if run {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type demoChangeSource struct {
	event domain.ChangeEvent
}

func (source demoChangeSource) List(ctx context.Context, query domain.ChangeQuery) (domain.ChangeSet, error) {
	if err := ctx.Err(); err != nil {
		return domain.ChangeSet{}, err
	}
	event := source.event
	if event.ResourceID != query.ResourceID || !event.StartedAt.Before(query.EndTime) || !event.CompletedAt.After(query.StartTime) {
		return domain.ChangeSet{SourceVersion: "demo-change-v1", Complete: true}, nil
	}
	event.AffectedInstances = append([]string(nil), event.AffectedInstances...)
	return domain.ChangeSet{SourceVersion: "demo-change-v1", Events: []domain.ChangeEvent{event}, Complete: true}, nil
}

func runFeishu(config config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := sqlite.Open(config.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	intake := application.NewIntake(store)
	actions, err := application.NewActionService(store, intake, config.SLS.MaxWindow)
	if err != nil {
		return err
	}
	sender, err := feishu.NewSender(config.FeishuAppID, config.FeishuAppSecret)
	if err != nil {
		return err
	}
	deliveryWorker, err := application.NewDeliveryWorker(
		store,
		sender,
		config.Delivery.WorkerID,
		config.Delivery.Lease,
		config.Delivery.SendTimeout,
		config.Delivery.MaxAttempts,
		config.Delivery.RetryBase,
	)
	if err != nil {
		return err
	}
	receiver, err := feishu.New(
		config.FeishuAppID,
		config.FeishuAppSecret,
		intake,
		feishu.WithActionHandler(actions),
		feishu.WithIngestionGrace(config.SLS.IngestionGrace),
	)
	if err != nil {
		return err
	}

	receiverErr := make(chan error, 1)
	go func() {
		receiverErr <- receiver.Run(ctx)
	}()
	ticker := time.NewTicker(config.Delivery.Poll)
	defer ticker.Stop()
	for {
		run, runErr := deliveryWorker.RunOne(ctx)
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			log.Printf("Feishu delivery failed: %v", runErr)
		}
		if run {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-receiverErr:
			if err == nil && ctx.Err() == nil {
				return errors.New("Feishu receiver stopped unexpectedly")
			}
			return err
		case <-ticker.C:
		}
	}
}

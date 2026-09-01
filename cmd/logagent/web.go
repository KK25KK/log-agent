package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"logagent/internal/adapters/localweb"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/config"
	"logagent/internal/domain"
)

func runWeb(loaded config.Config) error {
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalContext)
	defer cancel()

	store, err := sqlite.Open(loaded.Web.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()

	worker, err := buildInvestigationWorker(ctx, loaded, store)
	if err != nil {
		return err
	}
	intake := application.NewIntake(store)
	actions, err := application.NewActionService(store, intake, loaded.SLS.MaxWindow)
	if err != nil {
		return err
	}
	sender, err := localweb.NewSender(loaded.Web.AppID)
	if err != nil {
		return err
	}
	deliveryWorker, err := application.NewDeliveryWorker(
		store, sender, loaded.Delivery.WorkerID+"-web", loaded.Delivery.Lease,
		loaded.Delivery.SendTimeout, loaded.Delivery.MaxAttempts, loaded.Delivery.RetryBase,
	)
	if err != nil {
		return err
	}
	webAdapter, err := localweb.NewServer(localweb.Options{
		Address: loaded.Web.Address,
		Principal: domain.Principal{
			AppID: loaded.Web.AppID, TenantKey: loaded.Web.TenantKey, UserID: loaded.Web.UserID,
		},
		ChatID: loaded.Web.ChatID, IngestionGrace: loaded.SLS.IngestionGrace, MaxWindow: loaded.SLS.MaxWindow,
		SLSMode: loaded.SLS.Mode, LLMMode: loaded.LLM.Mode,
	}, store, intake, actions, sender)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr: loaded.Web.Address, Handler: webAdapter.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 * 1024,
	}
	errorsChannel := make(chan error, 3)
	go func() { errorsChannel <- runInvestigationWorkerLoop(ctx, worker, loaded.WorkerPoll) }()
	go func() { errorsChannel <- runDeliveryWorkerLoop(ctx, deliveryWorker, loaded.Delivery.Poll) }()
	go func() { errorsChannel <- httpServer.ListenAndServe() }()

	log.Printf(
		"local Web console listening on http://%s (database=%s, sls=%s, llm=%s, feishu=mock)",
		loaded.Web.Address, loaded.Web.DatabasePath, loaded.SLS.Mode, loaded.LLM.Mode,
	)
	runErr := <-errorsChannel
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	shutdownErr := httpServer.Shutdown(shutdownContext)
	if shutdownErr != nil && !errors.Is(shutdownErr, context.Canceled) {
		return fmt.Errorf("shut down local Web console: %w", shutdownErr)
	}
	if runErr == nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, http.ErrServerClosed) {
		return nil
	}
	return runErr
}

func runDeliveryWorkerLoop(ctx context.Context, worker *application.DeliveryWorker, poll time.Duration) error {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		run, err := worker.RunOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("local Web delivery failed: %v", err)
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

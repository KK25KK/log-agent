package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

type cancelConflictStore struct {
	investigation domain.Investigation
}

func (s *cancelConflictStore) ResolveInteraction(context.Context, string, string, string, string) (string, error) {
	return s.investigation.ID, nil
}

func (*cancelConflictStore) ResolveActionReplay(context.Context, string, string, string, string, string, domain.InvestigationAction) (string, error) {
	return "", ports.ErrNotFound
}

func (s *cancelConflictStore) GetInvestigation(context.Context, string) (domain.Investigation, error) {
	return s.investigation, nil
}

func (s *cancelConflictStore) RequestCancel(context.Context, string, time.Time) error {
	// Deterministically model the worker committing its terminal state after the
	// action read RUNNING but before the cancellation compare-and-set.
	s.investigation.Status = domain.StatusSucceeded
	return ports.ErrStateConflict
}

type unusedDerivedAccepter struct{}

func (unusedDerivedAccepter) Accept(context.Context, domain.InboundMessage, domain.InvestigationRequest) (string, bool, error) {
	return "", false, errors.New("unexpected derived intake")
}

type needsReviewActionStore struct {
	source       domain.Investigation
	derived      domain.Investigation
	cancelCalled bool
}

func (s *needsReviewActionStore) ResolveInteraction(context.Context, string, string, string, string) (string, error) {
	return s.source.ID, nil
}

func (*needsReviewActionStore) ResolveActionReplay(context.Context, string, string, string, string, string, domain.InvestigationAction) (string, error) {
	return "", ports.ErrNotFound
}

func (s *needsReviewActionStore) GetInvestigation(_ context.Context, investigationID string) (domain.Investigation, error) {
	if investigationID == s.source.ID {
		return s.source, nil
	}
	if investigationID == s.derived.ID && s.derived.ID != "" {
		return s.derived, nil
	}
	return domain.Investigation{}, ports.ErrNotFound
}

func (s *needsReviewActionStore) RequestCancel(context.Context, string, time.Time) error {
	s.cancelCalled = true
	return errors.New("unexpected cancellation")
}

type needsReviewAccepter struct {
	store *needsReviewActionStore
}

func (a needsReviewAccepter) Accept(_ context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest) (string, bool, error) {
	if inbound.ExpectedInvestigationID != a.store.source.ID || inbound.ReplyToMessageID == "" {
		return "", false, errors.New("derived intake did not preserve the source card binding")
	}
	a.store.derived = domain.Investigation{
		ID:        "inv-derived-after-review",
		Status:    domain.StatusQueued,
		Request:   request,
		CreatedAt: inbound.ReceivedAt,
		UpdatedAt: inbound.ReceivedAt,
	}
	return a.store.derived.ID, true, nil
}

func TestActionServiceAuthorizesAndIdempotentlyCancels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, intake, investigationID, cardID, now := newActionFixture(t, 30*time.Minute)
	defer store.Close()
	service, err := application.NewActionService(store, intake, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "requester"}
	base := domain.ActionCommand{
		EventID: "event-cancel", Action: domain.ActionCancel, InvestigationID: investigationID,
		Principal: principal, ChatID: "chat", CardMessageID: cardID, OccurredAt: now.Add(time.Minute),
	}

	missingEvent := base
	missingEvent.EventID = ""
	if _, err := service.Handle(ctx, missingEvent); !errors.Is(err, ports.ErrActionInvalid) {
		t.Fatalf("missing callback event was accepted: %v", err)
	}
	wrongUser := base
	wrongUser.Principal.UserID = "intruder"
	if _, err := service.Handle(ctx, wrongUser); !errors.Is(err, ports.ErrActionForbidden) {
		t.Fatalf("wrong user was accepted: %v", err)
	}
	wrongChat := base
	wrongChat.ChatID = "another-chat"
	if _, err := service.Handle(ctx, wrongChat); !errors.Is(err, ports.ErrActionForbidden) {
		t.Fatalf("wrong chat was accepted: %v", err)
	}
	before, err := store.GetInvestigation(ctx, investigationID)
	if err != nil || before.Status != domain.StatusQueued {
		t.Fatalf("denied actions changed state: status=%s err=%v", before.Status, err)
	}

	result, err := service.Handle(ctx, base)
	if err != nil || result.View != domain.ActionViewCancelledCard || result.Investigation.Status != domain.StatusCancelled {
		t.Fatalf("cancel failed: result=%+v err=%v", result, err)
	}
	replayed, err := service.Handle(ctx, base)
	if err != nil || replayed.Investigation.Status != domain.StatusCancelled {
		t.Fatalf("cancel replay was not idempotent: result=%+v err=%v", replayed, err)
	}
}

func TestActionServiceDoesNotConfirmCancellationAfterTerminalRace(t *testing.T) {
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "requester"}
	store := &cancelConflictStore{investigation: domain.Investigation{
		ID: "inv-race", Status: domain.StatusRunning,
		Request: domain.InvestigationRequest{Requester: principal},
	}}
	service, err := application.NewActionService(store, unusedDerivedAccepter{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Handle(context.Background(), domain.ActionCommand{
		EventID: "event-race", Action: domain.ActionCancel, InvestigationID: "inv-race",
		Principal: principal, ChatID: "chat", CardMessageID: "card", OccurredAt: time.Now().UTC(),
	})
	if !errors.Is(err, ports.ErrActionInvalid) {
		t.Fatalf("terminal race was not surfaced as an invalid action: result=%+v err=%v", result, err)
	}
	if result.View != "" || store.investigation.Status != domain.StatusSucceeded {
		t.Fatalf("terminal state was misreported as cancelled: result=%+v state=%s", result, store.investigation.Status)
	}
}

func TestActionServiceNeedsReviewCanOnlyCreateANewInvestigation(t *testing.T) {
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "requester"}
	now := time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC)
	store := &needsReviewActionStore{source: domain.Investigation{
		ID:     "inv-needs-review",
		Status: domain.StatusNeedsReview,
		Request: domain.InvestigationRequest{
			Service: "order-service", Environment: "prod",
			StartTime: now.Add(-30 * time.Minute), EndTime: now, Requester: principal,
		},
		LastError: "provider outcome details must stay internal",
	}}
	service, err := application.NewActionService(store, needsReviewAccepter{store: store}, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	base := domain.ActionCommand{
		InvestigationID: store.source.ID, Principal: principal,
		ChatID: "chat", CardMessageID: "card", OccurredAt: now.Add(time.Minute),
	}

	view := base
	view.Action = domain.ActionViewReport
	result, err := service.Handle(context.Background(), view)
	if err != nil || result.View != domain.ActionViewNeedsReviewCard {
		t.Fatalf("needs-review view was not preserved: result=%+v err=%v", result, err)
	}

	cancel := base
	cancel.Action = domain.ActionCancel
	cancel.EventID = "cancel-needs-review"
	if _, err := service.Handle(context.Background(), cancel); !errors.Is(err, ports.ErrActionInvalid) {
		t.Fatalf("needs-review investigation was cancellable: %v", err)
	}
	if store.cancelCalled {
		t.Fatal("terminal needs-review state reached the cancellation store mutation")
	}

	expand := base
	expand.Action = domain.ActionExpandWindow
	expand.EventID = "expand-needs-review"
	if _, err := service.Handle(context.Background(), expand); !errors.Is(err, ports.ErrActionInvalid) {
		t.Fatalf("needs-review investigation accepted an implicit cost-expanding rerun: %v", err)
	}

	rerun := base
	rerun.Action = domain.ActionRerun
	rerun.EventID = "rerun-needs-review"
	if _, err := service.Handle(context.Background(), rerun); !errors.Is(err, ports.ErrActionInvalid) {
		t.Fatalf("needs-review accepted rerun without cost acknowledgement: %v", err)
	}

	acknowledged := base
	acknowledged.Action = domain.ActionRerunWithCostAck
	acknowledged.EventID = "rerun-needs-review-ack"
	result, err = service.Handle(context.Background(), acknowledged)
	if err != nil || !result.Created || result.View != domain.ActionViewQueuedCard || result.Investigation.Status != domain.StatusQueued {
		t.Fatalf("acknowledged needs-review rerun did not create queued work: result=%+v err=%v", result, err)
	}
	if result.Investigation.Request != store.source.Request {
		t.Fatalf("rerun changed trusted investigation scope: source=%+v derived=%+v", store.source.Request, result.Investigation.Request)
	}
}

func TestActionServiceCancelledUnknownOutcomeRequiresCostAcknowledgement(t *testing.T) {
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "requester"}
	now := time.Date(2026, 8, 19, 15, 30, 0, 0, time.UTC)
	store := &needsReviewActionStore{source: domain.Investigation{
		ID:        "inv-cancelled-unknown",
		Status:    domain.StatusCancelled,
		LastError: domain.CancelReasonExternalQueryOutcomeUnknown,
		Request: domain.InvestigationRequest{
			Service: "order-service", Environment: "prod",
			StartTime: now.Add(-30 * time.Minute), EndTime: now, Requester: principal,
		},
	}}
	service, err := application.NewActionService(store, needsReviewAccepter{store: store}, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	base := domain.ActionCommand{
		InvestigationID: store.source.ID, Principal: principal,
		ChatID: "chat", CardMessageID: "card", OccurredAt: now.Add(time.Minute),
	}

	ordinary := base
	ordinary.Action = domain.ActionRerun
	ordinary.EventID = "cancelled-unknown-rerun"
	if _, err := service.Handle(context.Background(), ordinary); !errors.Is(err, ports.ErrActionInvalid) {
		t.Fatalf("cancelled unknown outcome accepted an unacknowledged rerun: %v", err)
	}

	acknowledged := base
	acknowledged.Action = domain.ActionRerunWithCostAck
	acknowledged.EventID = "cancelled-unknown-rerun-ack"
	result, err := service.Handle(context.Background(), acknowledged)
	if err != nil || !result.Created || result.View != domain.ActionViewQueuedCard {
		t.Fatalf("acknowledged cancelled rerun failed: result=%+v err=%v", result, err)
	}
}

func TestActionServiceRerunReplayCreatesOneDerivedInvestigation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, intake, parentID, cardID, now := newActionFixture(t, 30*time.Minute)
	defer store.Close()
	service, err := application.NewActionService(store, intake, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "requester"}
	if _, err := service.Handle(ctx, domain.ActionCommand{
		EventID: "cancel-before-rerun", Action: domain.ActionCancel, InvestigationID: parentID,
		Principal: principal, ChatID: "chat", CardMessageID: cardID, OccurredAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	command := domain.ActionCommand{
		EventID: "event-rerun", Action: domain.ActionRerun, InvestigationID: parentID,
		Principal: principal, ChatID: "chat", CardMessageID: cardID, OccurredAt: now.Add(2 * time.Minute),
	}
	created, err := service.Handle(ctx, command)
	if err != nil || !created.Created || created.Investigation.ID == parentID {
		t.Fatalf("rerun failed: result=%+v err=%v", created, err)
	}
	parent, err := store.GetInvestigation(ctx, parentID)
	if err != nil {
		t.Fatal(err)
	}
	if created.Investigation.Request.StartTime != parent.Request.StartTime || created.Investigation.Request.EndTime != parent.Request.EndTime {
		t.Fatalf("rerun changed source range: parent=%+v child=%+v", parent.Request, created.Investigation.Request)
	}
	if created.Investigation.Request.Requester != principal {
		t.Fatalf("derived requester was not inherited: %+v", created.Investigation.Request.Requester)
	}

	replayed, err := service.Handle(ctx, command)
	if err != nil || replayed.Created || replayed.Investigation.ID != created.Investigation.ID {
		t.Fatalf("callback replay created another investigation: first=%+v replay=%+v err=%v", created, replayed, err)
	}
	resolved, err := store.ResolveInteraction(ctx, "app", "tenant", "chat", cardID)
	if err != nil || resolved != created.Investigation.ID {
		t.Fatalf("card was not rebound to derived investigation: id=%q err=%v", resolved, err)
	}
	inboxCount, investigationCount, jobCount, _, err := store.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inboxCount != 2 || investigationCount != 2 || jobCount != 2 {
		t.Fatalf("unexpected replay counts: inbox=%d investigations=%d jobs=%d", inboxCount, investigationCount, jobCount)
	}

	childJob, ok, err := store.ClaimNext(ctx, "investigation-worker", now.Add(3*time.Minute), time.Minute)
	if err != nil || !ok || childJob.InvestigationID != created.Investigation.ID {
		t.Fatalf("claim derived job: job=%+v ok=%v err=%v", childJob, ok, err)
	}
	if err := store.FinishFailure(ctx, childJob, "safe-test-failure", now.Add(3*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	terminalReplay, err := service.Handle(ctx, command)
	if err != nil || terminalReplay.View != domain.ActionViewReportCard || terminalReplay.Investigation.Status != domain.StatusFailed {
		t.Fatalf("late callback replay regressed the card: result=%+v err=%v", terminalReplay, err)
	}
}

func TestActionServiceExpandWindowIsServerCalculatedAndCapped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "requester"}

	store, intake, parentID, cardID, now := newActionFixture(t, 30*time.Minute)
	defer store.Close()
	service, _ := application.NewActionService(store, intake, 2*time.Hour)
	cancelActionFixture(t, service, parentID, cardID, principal, now)
	result, err := service.Handle(ctx, domain.ActionCommand{
		EventID: "event-expand", Action: domain.ActionExpandWindow, InvestigationID: parentID,
		Principal: principal, ChatID: "chat", CardMessageID: cardID, OccurredAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Investigation.Request.EndTime.Sub(result.Investigation.Request.StartTime); got != time.Hour {
		t.Fatalf("expanded duration=%s want=1h", got)
	}

	largeStore, largeIntake, largeParentID, largeCardID, largeNow := newActionFixture(t, 75*time.Minute)
	defer largeStore.Close()
	largeService, _ := application.NewActionService(largeStore, largeIntake, 2*time.Hour)
	cancelActionFixture(t, largeService, largeParentID, largeCardID, principal, largeNow)
	_, err = largeService.Handle(ctx, domain.ActionCommand{
		EventID: "event-too-large", Action: domain.ActionExpandWindow, InvestigationID: largeParentID,
		Principal: principal, ChatID: "chat", CardMessageID: largeCardID, OccurredAt: largeNow.Add(2 * time.Minute),
	})
	if !errors.Is(err, ports.ErrActionInvalid) {
		t.Fatalf("over-limit expansion was accepted: %v", err)
	}
	_, investigations, jobs, _, countErr := largeStore.Counts(ctx)
	if countErr != nil {
		t.Fatal(countErr)
	}
	if investigations != 1 || jobs != 1 {
		t.Fatalf("rejected expansion created work: investigations=%d jobs=%d", investigations, jobs)
	}
}

func newActionFixture(t *testing.T, window time.Duration) (*sqlite.Store, *application.Intake, string, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	intake := application.NewIntake(store)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	investigationID, created, err := intake.Accept(ctx, domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "source-" + window.String(),
		ReplyToMessageID: "source-" + window.String(), ChatID: "chat", UserID: "requester", ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: now.Add(-window), EndTime: now,
	})
	if err != nil || !created {
		store.Close()
		t.Fatalf("accept fixture: created=%v err=%v", created, err)
	}
	delivery, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now, time.Minute)
	if err != nil || !ok {
		store.Close()
		t.Fatalf("claim receipt: ok=%v err=%v", ok, err)
	}
	cardID := "om_" + window.String()
	if err := store.CompleteDelivery(ctx, delivery, cardID, now.Add(time.Second)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, intake, investigationID, cardID, now
}

func cancelActionFixture(
	t *testing.T,
	service *application.ActionService,
	investigationID, cardID string,
	principal domain.Principal,
	now time.Time,
) {
	t.Helper()
	if _, err := service.Handle(context.Background(), domain.ActionCommand{
		EventID: "cancel-" + investigationID, Action: domain.ActionCancel, InvestigationID: investigationID,
		Principal: principal, ChatID: "chat", CardMessageID: cardID, OccurredAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
}

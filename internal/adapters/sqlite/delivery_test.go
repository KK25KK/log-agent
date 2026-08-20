package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

func TestDeliveryLifecycleIsOrderedAndCardIsBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	acceptDeliveryFixture(t, store, "inv-delivery", "job-delivery", "message-delivery", "", now)

	queued, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim queued delivery: ok=%v err=%v", ok, err)
	}
	if queued.Kind != domain.DeliveryQueued || queued.Target.SourceMessageID != "message-delivery" || queued.Target.CardMessageID != "" {
		t.Fatalf("unexpected queued delivery: %+v", queued)
	}

	job, ok, err := store.ClaimNext(ctx, "investigation-worker", now.Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim investigation: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(2*time.Second), time.Minute); err != nil || ok {
		t.Fatalf("running update must wait for queued card: ok=%v err=%v", ok, err)
	}
	if err := store.CompleteDelivery(ctx, queued, "om_card_1", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}

	running, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(4*time.Second), time.Minute)
	if err != nil || !ok || running.Kind != domain.DeliveryRunning {
		t.Fatalf("claim running delivery: kind=%s ok=%v err=%v", running.Kind, ok, err)
	}
	if running.Target.CardMessageID != "om_card_1" {
		t.Fatalf("running update did not reuse card: %+v", running.Target)
	}
	if err := store.CompleteDelivery(ctx, running, "om_card_1", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}

	evidence := []domain.Evidence{{ID: "ev-1", QueryID: "query-1", QuerySpecHash: "hash-1"}}
	report := domain.Report{
		InvestigationID: "inv-delivery",
		Outcome:         "data_insufficient",
		Findings: []domain.Finding{{
			Code: "insufficient", Statement: "证据不足", EvidenceIDs: []string{"ev-1"},
		}},
		GeneratedAt: now.Add(6 * time.Second),
	}
	if err := store.FinishSuccess(ctx, job, evidence, report, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	succeeded, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(7*time.Second), time.Minute)
	if err != nil || !ok || succeeded.Kind != domain.DeliverySucceeded {
		t.Fatalf("claim success delivery: kind=%s ok=%v err=%v", succeeded.Kind, ok, err)
	}
	if succeeded.Investigation.Status != domain.StatusSucceeded || succeeded.Investigation.Report == nil {
		t.Fatalf("terminal delivery lacks durable snapshot: %+v", succeeded.Investigation)
	}
	if err := store.CompleteDelivery(ctx, succeeded, "om_card_1", now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}

	resolved, err := store.ResolveInteraction(ctx, "app", "tenant", "chat", "om_card_1")
	if err != nil || resolved != "inv-delivery" {
		t.Fatalf("resolve interaction: id=%q err=%v", resolved, err)
	}
	for _, input := range [][4]string{
		{"wrong", "tenant", "chat", "om_card_1"},
		{"app", "wrong", "chat", "om_card_1"},
		{"app", "tenant", "wrong", "om_card_1"},
		{"app", "tenant", "chat", "wrong"},
	} {
		if _, err := store.ResolveInteraction(ctx, input[0], input[1], input[2], input[3]); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("mismatched routing %v resolved: %v", input, err)
		}
	}
}

func TestDeliveryAttemptFencesStaleClaimEvenWithSameWorkerID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	acceptDeliveryFixture(t, store, "inv-fence", "job-fence", "message-fence", "", now)

	oldClaim, ok, err := store.ClaimDelivery(ctx, "same-worker", now, time.Second)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	newClaim, ok, err := store.ClaimDelivery(ctx, "same-worker", now.Add(2*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("reclaim: ok=%v err=%v", ok, err)
	}
	if newClaim.Attempt != oldClaim.Attempt+1 {
		t.Fatalf("attempt did not advance: old=%d new=%d", oldClaim.Attempt, newClaim.Attempt)
	}
	if err := store.CompleteDelivery(ctx, oldClaim, "om_stale", now.Add(3*time.Second)); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("stale claimant completed newer lease: %v", err)
	}
	if err := store.CompleteDelivery(ctx, newClaim, "om_fresh", now.Add(3*time.Second)); err != nil {
		t.Fatalf("fresh claimant failed: %v", err)
	}
}

func TestDerivedInvestigationReusesAndRebindsExistingCard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	acceptDeliveryFixture(t, store, "inv-parent", "job-parent", "message-parent", "", now)
	queued, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim parent queued: ok=%v err=%v", ok, err)
	}
	if err := store.CompleteDelivery(ctx, queued, "om_shared", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	request := domain.InvestigationRequest{
		Service: "order-service", Environment: "prod",
		StartTime: now.Add(-time.Hour), EndTime: now,
		Requester: domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"},
	}
	inbound := domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "card:event-1",
		ReplyToMessageID: "om_shared", ExpectedInvestigationID: "inv-parent",
		ChatID: "chat", UserID: "user", ReceivedAt: now.Add(2 * time.Second),
	}
	storedID, created, err := store.AcceptOnce(ctx, inbound, request, "inv-child", "job-child")
	if err != nil || !created || storedID != "inv-child" {
		t.Fatalf("accept child: id=%q created=%v err=%v", storedID, created, err)
	}
	resolved, err := store.ResolveInteraction(ctx, "app", "tenant", "chat", "om_shared")
	if err != nil || resolved != "inv-child" {
		t.Fatalf("card not rebound to child: id=%q err=%v", resolved, err)
	}

	storedID, created, err = store.AcceptOnce(ctx, inbound, request, "inv-unused", "job-unused")
	if err != nil || created || storedID != "inv-child" {
		t.Fatalf("callback replay was not idempotent: id=%q created=%v err=%v", storedID, created, err)
	}
	childQueued, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(3*time.Second), time.Minute)
	if err != nil || !ok || childQueued.Investigation.ID != "inv-child" || childQueued.Target.CardMessageID != "om_shared" {
		t.Fatalf("derived queued patch is invalid: delivery=%+v ok=%v err=%v", childQueued, ok, err)
	}

	staleInbound := inbound
	staleInbound.MessageID = "card:event-2"
	staleInbound.ReceivedAt = now.Add(4 * time.Second)
	if _, _, err := store.AcceptOnce(ctx, staleInbound, request, "inv-stale", "job-stale"); !errors.Is(err, ports.ErrActionForbidden) {
		t.Fatalf("stale card action moved the active card: %v", err)
	}
	_, investigationCount, jobCount, _, err := store.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if investigationCount != 2 || jobCount != 2 {
		t.Fatalf("stale action left partial work: investigations=%d jobs=%d", investigationCount, jobCount)
	}
}

func TestFailureAndCancellationEnqueueTerminalCardUpdates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 13, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		kind domain.DeliveryKind
		end  func(*testing.T, *Store, domain.Job, time.Time)
	}{
		{
			name: "failure",
			kind: domain.DeliveryFailed,
			end: func(t *testing.T, store *Store, job domain.Job, at time.Time) {
				t.Helper()
				if err := store.FinishFailure(ctx, job, "provider unavailable", at); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "cancellation",
			kind: domain.DeliveryCancelled,
			end: func(t *testing.T, store *Store, job domain.Job, at time.Time) {
				t.Helper()
				if err := store.RequestCancel(ctx, job.InvestigationID, at); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			investigationID := "inv-" + test.name
			acceptDeliveryFixture(t, store, investigationID, "job-"+test.name, "message-"+test.name, "", now)
			queued, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now, time.Minute)
			if err != nil || !ok {
				t.Fatalf("claim queued: ok=%v err=%v", ok, err)
			}
			cardID := "om_" + test.name
			if err := store.CompleteDelivery(ctx, queued, cardID, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			job, ok, err := store.ClaimNext(ctx, "investigation-worker", now.Add(2*time.Second), time.Minute)
			if err != nil || !ok {
				t.Fatalf("claim job: ok=%v err=%v", ok, err)
			}
			running, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(3*time.Second), time.Minute)
			if err != nil || !ok || running.Kind != domain.DeliveryRunning {
				t.Fatalf("claim running: delivery=%+v ok=%v err=%v", running, ok, err)
			}
			if err := store.CompleteDelivery(ctx, running, cardID, now.Add(4*time.Second)); err != nil {
				t.Fatal(err)
			}
			test.end(t, store, job, now.Add(5*time.Second))
			terminal, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(6*time.Second), time.Minute)
			if err != nil || !ok || terminal.Kind != test.kind {
				t.Fatalf("claim terminal: kind=%s want=%s ok=%v err=%v", terminal.Kind, test.kind, ok, err)
			}
			if terminal.Investigation.LastError != "provider unavailable" && test.kind == domain.DeliveryFailed {
				t.Fatalf("failure snapshot missing internal cause: %+v", terminal.Investigation)
			}
		})
	}
}

func TestDeadProgressDeliveryDoesNotBlockTerminalDelivery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	acceptDeliveryFixture(t, store, "inv-progress-dead", "job-progress-dead", "message-progress-dead", "", now)

	queued, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim queued: ok=%v err=%v", ok, err)
	}
	if err := store.CompleteDelivery(ctx, queued, "om_progress_dead", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.ClaimNext(ctx, "investigation-worker", now.Add(2*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim investigation: ok=%v err=%v", ok, err)
	}
	running, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(3*time.Second), time.Minute)
	if err != nil || !ok || running.Kind != domain.DeliveryRunning {
		t.Fatalf("claim running: delivery=%+v ok=%v err=%v", running, ok, err)
	}
	if err := store.FailDelivery(ctx, running, domain.DeliveryFailure{Disposition: domain.FailureRetryable, ReasonCode: "feishu_unavailable"}, now.Add(4*time.Second), true, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishFailure(ctx, job, "provider unavailable", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	terminal, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(6*time.Second), time.Minute)
	if err != nil || !ok || terminal.Kind != domain.DeliveryFailed {
		t.Fatalf("dead progress blocked terminal delivery: delivery=%+v ok=%v err=%v", terminal, ok, err)
	}
}

func TestDeadReceiptDeliveryContinuesToBlockLaterUpdates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC)
	acceptDeliveryFixture(t, store, "inv-receipt-dead", "job-receipt-dead", "message-receipt-dead", "", now)

	queued, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim queued: ok=%v err=%v", ok, err)
	}
	if err := store.FailDelivery(ctx, queued, domain.DeliveryFailure{Disposition: domain.FailureRetryable, ReasonCode: "feishu_unavailable"}, now.Add(time.Second), true, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.ClaimNext(ctx, "investigation-worker", now.Add(2*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim investigation: ok=%v err=%v", ok, err)
	}
	if err := store.FinishFailure(ctx, job, "provider unavailable", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if delivery, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(4*time.Second), time.Minute); err != nil || ok {
		t.Fatalf("later update escaped a dead receipt: delivery=%+v ok=%v err=%v", delivery, ok, err)
	}
}

func TestDeadDerivedQueuedPatchDoesNotBlockLaterUpdates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC)
	acceptDeliveryFixture(t, store, "inv-parent-patch", "job-parent-patch", "message-parent-patch", "", now)
	parentQueued, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim parent receipt: ok=%v err=%v", ok, err)
	}
	if err := store.CompleteDelivery(ctx, parentQueued, "om_derived_patch", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancel(ctx, "inv-parent-patch", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	request := domain.InvestigationRequest{
		Service: "order-service", Environment: "prod",
		StartTime: now.Add(-time.Hour), EndTime: now,
		Requester: domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"},
	}
	inbound := domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "card:derived-dead",
		ReplyToMessageID: "om_derived_patch", ExpectedInvestigationID: "inv-parent-patch",
		ChatID: "chat", UserID: "user", ReceivedAt: now.Add(3 * time.Second),
	}
	if _, created, err := store.AcceptOnce(ctx, inbound, request, "inv-child-patch", "job-child-patch"); err != nil || !created {
		t.Fatalf("accept child: created=%v err=%v", created, err)
	}
	childQueued, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(4*time.Second), time.Minute)
	if err != nil || !ok || childQueued.Kind != domain.DeliveryQueued || childQueued.Investigation.ID != "inv-child-patch" {
		t.Fatalf("claim derived queued patch: delivery=%+v ok=%v err=%v", childQueued, ok, err)
	}
	if err := store.FailDelivery(ctx, childQueued, domain.DeliveryFailure{Disposition: domain.FailureRetryable, ReasonCode: "feishu_unavailable"}, now.Add(5*time.Second), true, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.ClaimNext(ctx, "investigation-worker", now.Add(6*time.Second), time.Minute)
	if err != nil || !ok || job.InvestigationID != "inv-child-patch" {
		t.Fatalf("claim child investigation: job=%+v ok=%v err=%v", job, ok, err)
	}
	running, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(7*time.Second), time.Minute)
	if err != nil || !ok || running.Kind != domain.DeliveryRunning || running.Investigation.ID != "inv-child-patch" {
		t.Fatalf("dead derived queued patch blocked running: delivery=%+v ok=%v err=%v", running, ok, err)
	}
	if err := store.CompleteDelivery(ctx, running, "om_derived_patch", now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishFailure(ctx, job, "provider unavailable", now.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	terminal, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(10*time.Second), time.Minute)
	if err != nil || !ok || terminal.Kind != domain.DeliveryFailed || terminal.Investigation.ID != "inv-child-patch" {
		t.Fatalf("claim child terminal: delivery=%+v ok=%v err=%v", terminal, ok, err)
	}
}

func acceptDeliveryFixture(t *testing.T, store *Store, investigationID, jobID, messageID, replyTo string, now time.Time) {
	t.Helper()
	request := domain.InvestigationRequest{
		Service: "order-service", Environment: "prod",
		StartTime: now.Add(-30 * time.Minute), EndTime: now,
		Requester: domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"},
	}
	inbound := domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: messageID,
		ReplyToMessageID: replyTo, ChatID: "chat", UserID: "user", ReceivedAt: now,
	}
	if _, created, err := store.AcceptOnce(context.Background(), inbound, request, investigationID, jobID); err != nil || !created {
		t.Fatalf("accept fixture: created=%v err=%v", created, err)
	}
}

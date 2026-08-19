package feishumock

import (
	"context"
	"testing"

	"logagent/internal/domain"
)

func TestSenderRecordsOneReplyAndPatchesSameCard(t *testing.T) {
	sender, err := NewSender("mock-app")
	if err != nil {
		t.Fatal(err)
	}
	investigation := domain.Investigation{ID: "inv-mock", Status: domain.StatusQueued}
	cardID, err := sender.Deliver(context.Background(), domain.DeliveryJob{
		Kind:          domain.DeliveryQueued,
		Investigation: investigation,
		Target: domain.InteractionTarget{
			AppID: "mock-app", TenantKey: "mock-tenant", ChatID: "oc-mock", SourceMessageID: "om-mock",
		},
	})
	if err != nil || cardID == "" {
		t.Fatalf("mock reply failed: card=%q err=%v", cardID, err)
	}
	investigation.Status = domain.StatusSucceeded
	investigation.Report = &domain.Report{Outcome: "spike_detected", Evidence: []domain.Evidence{{ID: "ev-1"}, {ID: "ev-2"}}}
	patchedID, err := sender.Deliver(context.Background(), domain.DeliveryJob{
		Kind:          domain.DeliverySucceeded,
		Investigation: investigation,
		Target: domain.InteractionTarget{
			AppID: "mock-app", TenantKey: "mock-tenant", ChatID: "oc-mock", SourceMessageID: "om-mock", CardMessageID: cardID,
		},
	})
	if err != nil || patchedID != cardID {
		t.Fatalf("mock patch failed: card=%q err=%v", patchedID, err)
	}
	records := sender.Records()
	if len(records) != 2 || records[0].Operation != OperationReply || records[1].Operation != OperationPatch {
		t.Fatalf("unexpected records: %#v", records)
	}
	if records[1].ReportOutcome != "spike_detected" || records[1].EvidenceCount != 2 {
		t.Fatalf("report projection missing: %#v", records[1])
	}
}

func TestSenderHonorsCancellationBeforeRecording(t *testing.T) {
	sender, err := NewSender("mock-app")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = sender.Deliver(ctx, domain.DeliveryJob{})
	if err == nil || len(sender.Records()) != 0 {
		t.Fatalf("cancelled delivery was recorded: err=%v records=%#v", err, sender.Records())
	}
}

func TestSenderRejectsAnotherAppBeforeRecording(t *testing.T) {
	sender, err := NewSender("mock-app")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sender.Deliver(context.Background(), domain.DeliveryJob{
		Kind:          domain.DeliveryQueued,
		Investigation: domain.Investigation{ID: "inv-mock", Status: domain.StatusQueued},
		Target: domain.InteractionTarget{
			AppID: "other-app", TenantKey: "mock-tenant", ChatID: "oc-mock", SourceMessageID: "om-mock",
		},
	})
	if err == nil || len(sender.Records()) != 0 {
		t.Fatalf("cross-app delivery was recorded: err=%v records=%#v", err, sender.Records())
	}
}

package feishumock

import (
	"context"
	"testing"
	"time"

	"logagent/internal/domain"
)

type intakeStub struct {
	inbound domain.InboundMessage
	request domain.InvestigationRequest
	calls   int
}

func (stub *intakeStub) Accept(_ context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest) (string, bool, error) {
	stub.calls++
	stub.inbound = inbound
	stub.request = request
	return "inv-mock", true, nil
}

func TestReceiverMapsStrictCommandWithoutCredentials(t *testing.T) {
	stub := &intakeStub{}
	receiver, err := NewReceiver("mock-app", "mock-tenant", stub, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 19, 10, 30, 10, 0, time.UTC)
	id, created, err := receiver.Receive(context.Background(), Message{
		MessageID: "om-mock-1", ChatID: "oc-mock", UserID: "ou-mock",
		Text: "/investigate order-service prod 30m", CreatedAt: createdAt,
	})
	if err != nil || id != "inv-mock" || !created || stub.calls != 1 {
		t.Fatalf("unexpected receive result: id=%q created=%v calls=%d err=%v", id, created, stub.calls, err)
	}
	if stub.inbound.AppID != "mock-app" || stub.inbound.TenantKey != "mock-tenant" || stub.inbound.ReplyToMessageID != "om-mock-1" {
		t.Fatalf("unexpected trusted envelope: %#v", stub.inbound)
	}
	if !stub.request.EndTime.Equal(createdAt.Add(-10*time.Second)) || stub.request.Requester.Complete() {
		t.Fatalf("unexpected normalized request before application identity injection: %#v", stub.request)
	}
}

func TestReceiverRejectsInvalidCommandBeforeIntake(t *testing.T) {
	stub := &intakeStub{}
	receiver, err := NewReceiver("mock-app", "mock-tenant", stub, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = receiver.Receive(context.Background(), Message{
		MessageID: "om-mock-1", ChatID: "oc-mock", UserID: "ou-mock",
		Text: "show me logs", CreatedAt: time.Now().UTC(),
	})
	if err == nil || stub.calls != 0 {
		t.Fatalf("invalid command reached intake: calls=%d err=%v", stub.calls, err)
	}
}

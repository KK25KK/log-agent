package localweb_test

import (
	"context"
	"testing"

	"logagent/internal/adapters/localweb"
	"logagent/internal/domain"
)

func TestSenderCreatesThenReusesLocalCard(t *testing.T) {
	t.Parallel()
	sender, err := localweb.NewSender("local-web")
	if err != nil {
		t.Fatal(err)
	}
	queued := domain.DeliveryJob{
		Kind: domain.DeliveryQueued, Investigation: domain.Investigation{ID: "inv_sender"},
		Target: domain.InteractionTarget{AppID: "local-web", TenantKey: "tenant", ChatID: "chat", SourceMessageID: "message"},
	}
	cardID, err := sender.Deliver(context.Background(), queued)
	if err != nil || cardID == "" {
		t.Fatalf("initial delivery: card=%q err=%v", cardID, err)
	}
	queued.Kind = domain.DeliveryRunning
	queued.Target.CardMessageID = cardID
	patchedID, err := sender.Deliver(context.Background(), queued)
	if err != nil || patchedID != cardID {
		t.Fatalf("patch delivery: card=%q err=%v", patchedID, err)
	}
	snapshot, ok := sender.Snapshot("inv_sender")
	if !ok || !snapshot.CardReady || snapshot.Kind != domain.DeliveryRunning {
		t.Fatalf("unexpected snapshot: %#v ok=%v", snapshot, ok)
	}
}

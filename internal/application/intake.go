package application

import (
	"context"
	"fmt"

	"logagent/internal/domain"
	"logagent/internal/ids"
	"logagent/internal/ports"
)

// Intake performs the application-owned idempotent acceptance use case.
type Intake struct {
	store ports.Store
}

func NewIntake(store ports.Store) *Intake {
	return &Intake{store: store}
}

func (i *Intake) Accept(ctx context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest) (string, bool, error) {
	if inbound.AppID == "" || inbound.TenantKey == "" || inbound.MessageID == "" {
		return "", false, fmt.Errorf("accept inbound message: app, tenant, and message IDs are required")
	}
	// The requester is security context. Always derive it from the adapter-owned
	// envelope so message text and callers cannot impersonate another user.
	request.Requester = domain.Principal{
		AppID:     inbound.AppID,
		TenantKey: inbound.TenantKey,
		UserID:    inbound.UserID,
	}

	investigationID, err := ids.New("inv")
	if err != nil {
		return "", false, err
	}
	jobID, err := ids.New("job")
	if err != nil {
		return "", false, err
	}

	return i.store.AcceptOnce(ctx, inbound, request, investigationID, jobID)
}

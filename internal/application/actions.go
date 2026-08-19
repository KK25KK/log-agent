package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type actionStore interface {
	ports.InteractionResolver
	GetInvestigation(ctx context.Context, investigationID string) (domain.Investigation, error)
	RequestCancel(ctx context.Context, investigationID string, now time.Time) error
}

type derivedAccepter interface {
	Accept(ctx context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest) (string, bool, error)
}

// ActionService authorizes card controls against persisted identity and scope.
type ActionService struct {
	store     actionStore
	intake    derivedAccepter
	maxWindow time.Duration
	now       func() time.Time
}

func NewActionService(store actionStore, intake derivedAccepter, maxWindow time.Duration) (*ActionService, error) {
	if store == nil || intake == nil || maxWindow <= 0 {
		return nil, errors.New("action store, intake, and positive max window are required")
	}
	return &ActionService{store: store, intake: intake, maxWindow: maxWindow, now: time.Now}, nil
}

func (s *ActionService) Handle(ctx context.Context, command domain.ActionCommand) (domain.ActionResult, error) {
	if err := validateActionCommand(command); err != nil {
		return domain.ActionResult{}, fmt.Errorf("%w: %v", ports.ErrActionInvalid, err)
	}
	if command.Action == domain.ActionRerun || command.Action == domain.ActionRerunWithCostAck || command.Action == domain.ActionExpandWindow {
		replayedID, replayErr := s.store.ResolveActionReplay(
			ctx,
			command.Principal.AppID,
			command.Principal.TenantKey,
			command.ChatID,
			command.Principal.UserID,
			command.EventID,
			command.Action,
		)
		if replayErr == nil {
			activeID, err := s.store.ResolveInteraction(
				ctx,
				command.Principal.AppID,
				command.Principal.TenantKey,
				command.ChatID,
				command.CardMessageID,
			)
			if err != nil {
				return domain.ActionResult{}, fmt.Errorf("resolve replayed card: %w", err)
			}
			if activeID == "" {
				activeID = replayedID
			}
			replayed, err := s.store.GetInvestigation(ctx, activeID)
			if err != nil {
				return domain.ActionResult{}, fmt.Errorf("load replayed investigation: %w", err)
			}
			return domain.ActionResult{View: actionViewForInvestigation(replayed), Investigation: replayed}, nil
		}
		if !errors.Is(replayErr, ports.ErrNotFound) {
			return domain.ActionResult{}, fmt.Errorf("resolve action replay: %w", replayErr)
		}
	}
	resolvedID, err := s.store.ResolveInteraction(
		ctx,
		command.Principal.AppID,
		command.Principal.TenantKey,
		command.ChatID,
		command.CardMessageID,
	)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return domain.ActionResult{}, ports.ErrActionForbidden
		}
		return domain.ActionResult{}, fmt.Errorf("resolve interaction: %w", err)
	}
	if resolvedID != command.InvestigationID {
		return domain.ActionResult{}, ports.ErrActionForbidden
	}
	investigation, err := s.store.GetInvestigation(ctx, resolvedID)
	if err != nil {
		return domain.ActionResult{}, fmt.Errorf("load investigation action target: %w", err)
	}
	if investigation.Request.Requester != command.Principal {
		return domain.ActionResult{}, ports.ErrActionForbidden
	}

	switch command.Action {
	case domain.ActionViewReport:
		if investigation.Status == domain.StatusNeedsReview {
			return domain.ActionResult{View: domain.ActionViewNeedsReviewCard, Investigation: investigation}, nil
		}
		return domain.ActionResult{View: domain.ActionViewReportCard, Investigation: investigation}, nil
	case domain.ActionViewEvidence:
		if investigation.Report == nil {
			return domain.ActionResult{}, fmt.Errorf("%w: evidence is not available", ports.ErrActionInvalid)
		}
		return domain.ActionResult{View: domain.ActionViewEvidenceCard, Investigation: investigation}, nil
	case domain.ActionCancel:
		return s.cancel(ctx, investigation, command)
	case domain.ActionRerun, domain.ActionRerunWithCostAck, domain.ActionExpandWindow:
		return s.derive(ctx, investigation, command)
	default:
		return domain.ActionResult{}, fmt.Errorf("%w: unsupported action", ports.ErrActionInvalid)
	}
}

func (s *ActionService) cancel(ctx context.Context, investigation domain.Investigation, command domain.ActionCommand) (domain.ActionResult, error) {
	if investigation.Status == domain.StatusCancelled {
		return domain.ActionResult{View: domain.ActionViewCancelledCard, Investigation: investigation}, nil
	}
	if investigation.Status != domain.StatusQueued && investigation.Status != domain.StatusRunning {
		return domain.ActionResult{}, fmt.Errorf("%w: investigation is already terminal", ports.ErrActionInvalid)
	}
	if err := s.store.RequestCancel(ctx, investigation.ID, actionTime(command, s.now)); err != nil {
		if errors.Is(err, ports.ErrStateConflict) {
			return domain.ActionResult{}, fmt.Errorf("%w: investigation became terminal before cancellation", ports.ErrActionInvalid)
		}
		return domain.ActionResult{}, fmt.Errorf("cancel investigation: %w", err)
	}
	updated, err := s.store.GetInvestigation(ctx, investigation.ID)
	if err != nil {
		return domain.ActionResult{}, fmt.Errorf("reload cancelled investigation: %w", err)
	}
	if updated.Status != domain.StatusCancelled {
		return domain.ActionResult{}, fmt.Errorf("%w: cancellation was not committed", ports.ErrActionInvalid)
	}
	return domain.ActionResult{View: domain.ActionViewCancelledCard, Investigation: updated}, nil
}

func (s *ActionService) derive(ctx context.Context, source domain.Investigation, command domain.ActionCommand) (domain.ActionResult, error) {
	if command.EventID == "" {
		return domain.ActionResult{}, fmt.Errorf("%w: callback event ID is required", ports.ErrActionInvalid)
	}
	if source.Status != domain.StatusSucceeded && source.Status != domain.StatusFailed && source.Status != domain.StatusCancelled && source.Status != domain.StatusNeedsReview {
		return domain.ActionResult{}, fmt.Errorf("%w: investigation is not terminal", ports.ErrActionInvalid)
	}
	requiresCostAck := source.Status == domain.StatusNeedsReview ||
		(source.Status == domain.StatusCancelled && source.LastError == domain.CancelReasonExternalQueryOutcomeUnknown)
	if requiresCostAck && command.Action != domain.ActionRerunWithCostAck {
		return domain.ActionResult{}, fmt.Errorf("%w: an unknown external outcome requires explicit cost acknowledgement", ports.ErrActionInvalid)
	}
	if !requiresCostAck && command.Action == domain.ActionRerunWithCostAck {
		return domain.ActionResult{}, fmt.Errorf("%w: cost acknowledgement is not valid for this investigation", ports.ErrActionInvalid)
	}
	request := source.Request
	if command.Action == domain.ActionExpandWindow {
		window := request.EndTime.Sub(request.StartTime)
		if window <= 0 || window > s.maxWindow/2 {
			return domain.ActionResult{}, fmt.Errorf("%w: expanded window exceeds policy", ports.ErrActionInvalid)
		}
		request.StartTime = request.EndTime.Add(-2 * window)
	}
	now := actionTime(command, s.now)
	inbound := domain.InboundMessage{
		AppID: command.Principal.AppID, TenantKey: command.Principal.TenantKey,
		MessageID: "card:" + command.EventID, ReplyToMessageID: command.CardMessageID,
		ExpectedInvestigationID: source.ID, ChatID: command.ChatID,
		UserID: command.Principal.UserID, Text: string(command.Action), ReceivedAt: now,
	}
	investigationID, created, err := s.intake.Accept(ctx, inbound, request)
	if err != nil {
		return domain.ActionResult{}, fmt.Errorf("create derived investigation: %w", err)
	}
	derived, err := s.store.GetInvestigation(ctx, investigationID)
	if err != nil {
		return domain.ActionResult{}, fmt.Errorf("load derived investigation: %w", err)
	}
	return domain.ActionResult{View: domain.ActionViewQueuedCard, Investigation: derived, Created: created}, nil
}

func actionViewForInvestigation(investigation domain.Investigation) domain.ActionView {
	switch investigation.Status {
	case domain.StatusQueued:
		return domain.ActionViewQueuedCard
	case domain.StatusRunning:
		return domain.ActionViewRunningCard
	case domain.StatusCancelled:
		return domain.ActionViewCancelledCard
	case domain.StatusNeedsReview:
		return domain.ActionViewNeedsReviewCard
	default:
		return domain.ActionViewReportCard
	}
}

func validateActionCommand(command domain.ActionCommand) error {
	if !command.Principal.Complete() || command.InvestigationID == "" || command.ChatID == "" || command.CardMessageID == "" {
		return errors.New("trusted identity, investigation, chat, and card message are required")
	}
	if (command.Action == domain.ActionCancel || command.Action == domain.ActionExpandWindow || command.Action == domain.ActionRerun || command.Action == domain.ActionRerunWithCostAck) && command.EventID == "" {
		return errors.New("mutating actions require a callback event ID")
	}
	return nil
}

func actionTime(command domain.ActionCommand, now func() time.Time) time.Time {
	if !command.OccurredAt.IsZero() {
		return command.OccurredAt.UTC()
	}
	return now().UTC()
}

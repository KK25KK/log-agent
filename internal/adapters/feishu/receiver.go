// Package feishu is the only package that translates Feishu SDK events.
package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"logagent/internal/command"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

const persistTimeout = 2 * time.Second

type accepter interface {
	Accept(ctx context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest) (string, bool, error)
}

type intentReceiver interface {
	Resolve(ctx context.Context, inbound domain.InboundMessage, problem string) (domain.IntentResolution, bool, error)
	Confirm(ctx context.Context, resolutionID string, principal domain.Principal, chatID string) (string, bool, error)
}

type intentPreviewSender interface {
	DeliverIntentPreview(ctx context.Context, target domain.InteractionTarget, resolution domain.IntentResolution) (string, error)
}

// ActionHandler is implemented by the application action use case. SDK callback
// types are translated before the application is invoked.
type ActionHandler interface {
	Handle(ctx context.Context, command domain.ActionCommand) (domain.ActionResult, error)
}

type receiverOptions struct {
	actions        ActionHandler
	ingestionGrace time.Duration
	intents        intentReceiver
	intentSender   intentPreviewSender
}

// Option configures optional Feishu capabilities without breaking the M0 New
// call sites that only receive messages.
type Option func(*receiverOptions) error

func WithActionHandler(handler ActionHandler) Option {
	return func(options *receiverOptions) error {
		if handler == nil {
			return errors.New("Feishu action handler is required")
		}
		options.actions = handler
		return nil
	}
}

func WithIngestionGrace(grace time.Duration) Option {
	return func(options *receiverOptions) error {
		if grace < domain.MinimumIngestionGrace {
			return fmt.Errorf("Feishu ingestion grace must be at least %s", domain.MinimumIngestionGrace)
		}
		options.ingestionGrace = grace
		return nil
	}
}

func WithIntentResolution(handler intentReceiver, sender intentPreviewSender) Option {
	return func(options *receiverOptions) error {
		if handler == nil || sender == nil {
			return errors.New("Feishu intent handler and preview sender are required")
		}
		options.intents = handler
		options.intentSender = sender
		return nil
	}
}

// Receiver maps im.message.receive_v1 events into durable application requests.
type Receiver struct {
	appID          string
	intake         accepter
	actions        ActionHandler
	intents        intentReceiver
	intentSender   intentPreviewSender
	ingestionGrace time.Duration
	now            func() time.Time
	persistBudget  time.Duration
	dispatcher     *dispatcher.EventDispatcher
	client         *larkws.Client
}

func New(appID, appSecret string, intake accepter, options ...Option) (*Receiver, error) {
	if appID == "" || appSecret == "" {
		return nil, errors.New("Feishu app ID and secret are required")
	}
	if intake == nil {
		return nil, errors.New("intake service is required")
	}

	configured := receiverOptions{ingestionGrace: domain.DefaultIngestionGrace}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("Feishu receiver option is nil")
		}
		if err := option(&configured); err != nil {
			return nil, err
		}
	}
	receiver := &Receiver{
		appID: appID, intake: intake, actions: configured.actions, ingestionGrace: configured.ingestionGrace,
		intents: configured.intents, intentSender: configured.intentSender,
		now: time.Now, persistBudget: persistTimeout,
	}
	receiver.dispatcher = dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(receiver.handleMessage)
	if receiver.actions != nil || receiver.intents != nil {
		receiver.dispatcher.OnP2CardActionTrigger(receiver.handleAction)
	}
	receiver.client = larkws.NewClient(
		appID,
		appSecret,
		larkws.WithEventHandler(receiver.dispatcher),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)
	return receiver, nil
}

func (r *Receiver) handleAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if resolutionID, principal, chatID, ok := r.mapIntentConfirmation(event); ok {
		if r.intents == nil {
			return toastResponse("warning", "自然语言调查当前未启用。"), nil
		}
		confirmCtx, cancel := context.WithTimeout(ctx, r.persistBudget)
		defer cancel()
		_, created, err := r.intents.Confirm(confirmCtx, resolutionID, principal, chatID)
		if err != nil {
			switch {
			case errors.Is(err, ports.ErrIntentForbidden):
				return toastResponse("error", "你无权确认这项调查。"), nil
			case errors.Is(err, ports.ErrIntentExpired):
				return toastResponse("warning", "解析结果已过期，请重新描述问题。"), nil
			case errors.Is(err, ports.ErrIntentInvalid):
				return toastResponse("warning", "当前解析结果不能启动调查。"), nil
			default:
				return nil, fmt.Errorf("confirm Feishu intent resolution: %w", err)
			}
		}
		if created {
			return toastResponse("success", "已确认，调查任务已经创建。"), nil
		}
		return toastResponse("success", "该解析结果已经确认过。"), nil
	}
	command, permanentMessage, ok := r.mapAction(event)
	if !ok {
		return toastResponse("error", permanentMessage), nil
	}
	if r.actions == nil {
		return toastResponse("warning", "调查卡片操作当前未启用。"), nil
	}

	actionCtx, cancel := context.WithTimeout(ctx, r.persistBudget)
	defer cancel()
	result, err := r.actions.Handle(actionCtx, command)
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrActionForbidden):
			return toastResponse("error", "你无权操作这项调查。"), nil
		case errors.Is(err, ports.ErrActionInvalid):
			return toastResponse("warning", "该操作不适用于调查的当前状态。"), nil
		default:
			return nil, fmt.Errorf("handle Feishu card action: %w", err)
		}
	}
	// Mutating actions are projected by the durable delivery worker. Returning a
	// replacement card here would introduce a second writer and allow an older
	// RUNNING callback response to overwrite a newly rebound investigation card.
	if isMutatingAction(command.Action) {
		return toastResponse("success", actionSuccessMessage(command.Action, result)), nil
	}

	card, err := renderActionCard(result)
	if err != nil {
		return nil, fmt.Errorf("render Feishu action result: %w", err)
	}
	if _, err := marshalCard(card); err != nil {
		return nil, err
	}
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: actionSuccessMessage(command.Action, result)},
		Card:  &callback.Card{Type: "card_json", Data: card},
	}, nil
}

func (r *Receiver) mapIntentConfirmation(event *callback.CardActionTriggerEvent) (string, domain.Principal, string, bool) {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.Event == nil || event.Event.Action == nil ||
		event.Event.Operator == nil || event.Event.Context == nil {
		return "", domain.Principal{}, "", false
	}
	valueMap := event.Event.Action.Value
	if len(valueMap) != 2 || valueMap["action"] != "confirm_intent" {
		return "", domain.Principal{}, "", false
	}
	resolutionID, ok := valueMap["resolution_id"].(string)
	if !ok || !strings.HasPrefix(resolutionID, "intent_") || len(resolutionID) > 128 {
		return "", domain.Principal{}, "", false
	}
	header := event.EventV2Base.Header
	appID := header.AppID
	if appID == "" {
		appID = r.appID
	}
	principal := domain.Principal{AppID: appID, TenantKey: header.TenantKey, UserID: event.Event.Operator.OpenID}
	if appID != r.appID || !principal.Complete() || event.Event.Context.OpenChatID == "" {
		return "", domain.Principal{}, "", false
	}
	if event.Event.Operator.TenantKey != nil && *event.Event.Operator.TenantKey != "" && *event.Event.Operator.TenantKey != header.TenantKey {
		return "", domain.Principal{}, "", false
	}
	return resolutionID, principal, event.Event.Context.OpenChatID, true
}

func isMutatingAction(action domain.InvestigationAction) bool {
	switch action {
	case domain.ActionCancel, domain.ActionExpandWindow, domain.ActionRerun, domain.ActionRerunWithCostAck:
		return true
	default:
		return false
	}
}

func (r *Receiver) mapAction(event *callback.CardActionTriggerEvent) (domain.ActionCommand, string, bool) {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.Event == nil {
		return domain.ActionCommand{}, "卡片回调缺少必要信息。", false
	}
	header := event.EventV2Base.Header
	request := event.Event
	if request.Operator == nil || request.Context == nil || request.Action == nil {
		return domain.ActionCommand{}, "卡片回调缺少操作人或消息信息。", false
	}
	if request.Host != "" && request.Host != "im_message" {
		return domain.ActionCommand{}, "不支持该卡片来源。", false
	}
	appID := header.AppID
	if appID == "" {
		appID = r.appID
	}
	if appID != r.appID || header.TenantKey == "" || request.Operator.OpenID == "" || request.Context.OpenMessageID == "" || request.Context.OpenChatID == "" {
		return domain.ActionCommand{}, "卡片身份信息不完整。", false
	}
	if request.Operator.TenantKey != nil && *request.Operator.TenantKey != "" && *request.Operator.TenantKey != header.TenantKey {
		return domain.ActionCommand{}, "卡片租户信息不一致。", false
	}
	action, investigationID, ok := decodeActionValue(request.Action.Value)
	if !ok {
		return domain.ActionCommand{}, "不支持或已失效的卡片操作。", false
	}
	return domain.ActionCommand{
		EventID:         header.EventID,
		Action:          action,
		InvestigationID: investigationID,
		Principal: domain.Principal{
			AppID: appID, TenantKey: header.TenantKey, UserID: request.Operator.OpenID,
		},
		ChatID:        request.Context.OpenChatID,
		CardMessageID: request.Context.OpenMessageID,
		OccurredAt:    messageTime(header.CreateTime, r.now().UTC()),
	}, "", true
}

func decodeActionValue(value map[string]interface{}) (domain.InvestigationAction, string, bool) {
	if len(value) != 2 {
		return "", "", false
	}
	rawAction, actionOK := value["action"].(string)
	investigationID, investigationOK := value["investigation_id"].(string)
	if !actionOK || !investigationOK || investigationID == "" || len(investigationID) > 128 {
		return "", "", false
	}
	action := domain.InvestigationAction(rawAction)
	switch action {
	case domain.ActionViewEvidence, domain.ActionViewReport, domain.ActionCancel, domain.ActionExpandWindow, domain.ActionRerun, domain.ActionRerunWithCostAck:
		return action, investigationID, true
	default:
		return "", "", false
	}
}

func toastResponse(kind, message string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: kind, Content: message}}
}

func actionSuccessMessage(action domain.InvestigationAction, result domain.ActionResult) string {
	switch action {
	case domain.ActionViewEvidence:
		return "已切换到证据视图。"
	case domain.ActionViewReport:
		return "已返回调查报告。"
	case domain.ActionCancel:
		return "调查已取消。"
	case domain.ActionExpandWindow:
		if result.Created {
			return "已扩大时间窗并创建新调查。"
		}
		return "扩大时间窗的调查已存在。"
	case domain.ActionRerun, domain.ActionRerunWithCostAck:
		if result.Created {
			return "已创建重新运行的调查。"
		}
		return "重新运行的调查已存在。"
	default:
		return "操作已完成。"
	}
}

// Run maintains the WebSocket connection until the context is cancelled.
func (r *Receiver) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- r.client.Start(ctx)
	}()

	select {
	case err := <-errCh:
		if err == nil {
			return nil
		}
		return fmt.Errorf("start Feishu WebSocket client: %w", err)
	case <-ctx.Done():
		r.client.Close()
		return nil
	}
}

func (r *Receiver) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.Event == nil || event.Event.Message == nil {
		// A structurally invalid event is not recoverable by retrying. Acknowledge
		// it so Feishu does not create a retry storm; production adds a dead letter
		// metric for this branch.
		return nil
	}
	message := event.Event.Message
	if value(message.MessageType) != "text" {
		return nil
	}
	switch value(message.ChatType) {
	case "p2p":
	case "group":
		if !hasBotMention(message.Mentions) {
			return nil
		}
	default:
		return nil
	}

	text, ok := messageText(value(message.Content))
	if !ok {
		return nil
	}
	text = stripBotMentions(text, message.Mentions)
	if text == "" {
		return nil
	}
	now := messageTime(value(message.CreateTime), r.now().UTC())
	header := event.EventV2Base.Header
	appID := header.AppID
	if appID == "" {
		appID = r.appID
	}
	inbound := domain.InboundMessage{
		AppID:            appID,
		TenantKey:        header.TenantKey,
		MessageID:        value(message.MessageId),
		ReplyToMessageID: value(message.MessageId),
		ChatID:           value(message.ChatId),
		UserID:           senderOpenID(event.Event.Sender),
		Text:             text,
		ReceivedAt:       now,
	}
	if inbound.TenantKey == "" || inbound.MessageID == "" {
		return nil
	}
	if commandValue, commandOK := investigationCommand(text); commandOK {
		request, err := command.ParseInvestigationWithGrace(commandValue, now, r.ingestionGrace)
		if err != nil {
			return nil
		}
		persistCtx, cancel := context.WithTimeout(ctx, r.persistBudget)
		defer cancel()
		if _, _, err := r.intake.Accept(persistCtx, inbound, request); err != nil {
			return fmt.Errorf("persist Feishu message %q: %w", inbound.MessageID, err)
		}
		return nil
	}
	if r.intents == nil || r.intentSender == nil {
		return nil
	}
	resolution, _, err := r.intents.Resolve(ctx, inbound, text)
	if errors.Is(err, ports.ErrIntentInvalid) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve Feishu intent %q: %w", inbound.MessageID, err)
	}
	_, err = r.intentSender.DeliverIntentPreview(ctx, domain.InteractionTarget{
		AppID: inbound.AppID, TenantKey: inbound.TenantKey, ChatID: inbound.ChatID, SourceMessageID: inbound.MessageID,
	}, resolution)
	if err != nil {
		return fmt.Errorf("deliver Feishu intent preview %q: %w", inbound.MessageID, err)
	}
	return nil
}

func messageText(content string) (string, bool) {
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return "", false
	}
	text := strings.TrimSpace(payload.Text)
	return text, text != ""
}

func commandText(content string) (string, bool) {
	text, ok := messageText(content)
	if !ok {
		return "", false
	}
	return investigationCommand(text)
}

func investigationCommand(text string) (string, bool) {
	index := strings.Index(text, "/investigate")
	if index < 0 {
		return "", false
	}
	return strings.TrimSpace(text[index:]), true
}

func messageTime(raw string, fallback time.Time) time.Time {
	millis, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || millis <= 0 {
		return fallback
	}
	return time.UnixMilli(millis).UTC()
}

func senderOpenID(sender *larkim.EventSender) string {
	if sender == nil || sender.SenderId == nil {
		return ""
	}
	return value(sender.SenderId.OpenId)
}

func hasBotMention(mentions []*larkim.MentionEvent) bool {
	for _, mention := range mentions {
		if mention != nil && value(mention.MentionedType) == "bot" && value(mention.Key) != "" {
			return true
		}
	}
	return false
}

func stripBotMentions(text string, mentions []*larkim.MentionEvent) string {
	for _, mention := range mentions {
		if mention == nil || value(mention.MentionedType) != "bot" {
			continue
		}
		if key := value(mention.Key); key != "" {
			text = strings.ReplaceAll(text, key, " ")
		}
	}
	return strings.TrimSpace(text)
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

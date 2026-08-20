package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const senderRequestTimeout = 5 * time.Second

type messageAPI interface {
	Reply(context.Context, *larkim.ReplyMessageReq, ...larkcore.RequestOptionFunc) (*larkim.ReplyMessageResp, error)
	Patch(context.Context, *larkim.PatchMessageReq, ...larkcore.RequestOptionFunc) (*larkim.PatchMessageResp, error)
}

type cardMessageClient interface {
	ReplyCard(ctx context.Context, sourceMessageID, content, uuid string) (string, error)
	PatchCard(ctx context.Context, cardMessageID, content string) error
}

type sdkCardMessageClient struct {
	messages messageAPI
}

// Sender delivers durable lifecycle projections through Feishu. It owns all
// SDK request and response types so they cannot leak into application ports.
type Sender struct {
	appID    string
	messages cardMessageClient
}

func NewSender(appID, appSecret string) (*Sender, error) {
	if appID == "" || appSecret == "" {
		return nil, errors.New("Feishu app ID and secret are required")
	}
	client := lark.NewClient(
		appID,
		appSecret,
		lark.WithReqTimeout(senderRequestTimeout),
		lark.WithLogLevel(larkcore.LogLevelError),
	)
	return newSender(appID, &sdkCardMessageClient{messages: client.Im.V1.Message})
}

func newSender(appID string, messages cardMessageClient) (*Sender, error) {
	if appID == "" || messages == nil {
		return nil, errors.New("Feishu app ID and message client are required")
	}
	return &Sender{appID: appID, messages: messages}, nil
}

func (s *Sender) Deliver(ctx context.Context, delivery domain.DeliveryJob) (string, error) {
	if err := s.validateDelivery(delivery); err != nil {
		return "", ports.NewOperationError(domain.FailurePermanent, "feishu_delivery_contract_invalid", err)
	}
	card, err := renderDeliveryCard(delivery)
	if err != nil {
		return "", ports.NewOperationError(domain.FailurePermanent, "feishu_card_render_failed", err)
	}
	content, err := marshalCard(card)
	if err != nil {
		return "", ports.NewOperationError(domain.FailurePermanent, "feishu_card_encode_failed", err)
	}

	if delivery.Target.CardMessageID == "" {
		return s.reply(ctx, delivery, content)
	}
	return s.patch(ctx, delivery.Target.CardMessageID, content)
}

func (s *Sender) validateDelivery(delivery domain.DeliveryJob) error {
	if delivery.Investigation.ID == "" {
		return errors.New("deliver Feishu card: investigation ID is required")
	}
	if delivery.Target.AppID == "" || delivery.Target.AppID != s.appID {
		return errors.New("deliver Feishu card: target app does not match sender")
	}
	if delivery.Target.TenantKey == "" || delivery.Target.ChatID == "" {
		return errors.New("deliver Feishu card: tenant and chat are required")
	}
	if delivery.Target.CardMessageID == "" {
		if delivery.Kind != domain.DeliveryQueued {
			return errors.New("deliver Feishu card: only the queued receipt may create a card")
		}
		if delivery.Target.SourceMessageID == "" {
			return errors.New("deliver Feishu card: source message ID is required for receipt")
		}
	}
	return nil
}

func (s *Sender) reply(ctx context.Context, delivery domain.DeliveryJob, content string) (string, error) {
	messageID, err := s.messages.ReplyCard(ctx, delivery.Target.SourceMessageID, content, receiptUUID(delivery))
	if err != nil {
		return "", err
	}
	return messageID, nil
}

func (s *Sender) patch(ctx context.Context, cardMessageID, content string) (string, error) {
	if err := s.messages.PatchCard(ctx, cardMessageID, content); err != nil {
		return "", err
	}
	return cardMessageID, nil
}

func (c *sdkCardMessageClient) ReplyCard(ctx context.Context, sourceMessageID, content, uuid string) (string, error) {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(sourceMessageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(content).
			Uuid(uuid).
			Build()).
		Build()
	resp, err := c.messages.Reply(ctx, req)
	if err != nil {
		return "", safeTransportError(ctx, "reply Feishu receipt", err)
	}
	if resp == nil {
		return "", ports.NewOperationError(domain.FailureOutcomeUnknown, "feishu_empty_response", nil)
	}
	if !resp.Success() {
		return "", responseFailure("feishu_reply_rejected", resp.ApiResp)
	}
	if resp.Data == nil || resp.Data.MessageId == nil || *resp.Data.MessageId == "" {
		return "", ports.NewOperationError(domain.FailureOutcomeUnknown, "feishu_message_id_missing", nil)
	}
	return *resp.Data.MessageId, nil
}

func (c *sdkCardMessageClient) PatchCard(ctx context.Context, cardMessageID, content string) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(cardMessageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(content).Build()).
		Build()
	resp, err := c.messages.Patch(ctx, req)
	if err != nil {
		return safeTransportError(ctx, "patch Feishu card", err)
	}
	if resp == nil {
		return ports.NewOperationError(domain.FailureOutcomeUnknown, "feishu_empty_response", nil)
	}
	if !resp.Success() {
		return responseFailure("feishu_patch_rejected", resp.ApiResp)
	}
	return nil
}

func receiptUUID(delivery domain.DeliveryJob) string {
	digest := sha256.Sum256([]byte(
		delivery.Target.AppID + "\x00" +
			delivery.Target.TenantKey + "\x00" +
			delivery.Target.SourceMessageID + "\x00" +
			delivery.Investigation.ID,
	))
	return "la_" + hex.EncodeToString(digest[:16])
}

func safeTransportError(ctx context.Context, operation string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		disposition := domain.FailureCancelled
		code := "feishu_send_cancelled"
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			disposition = domain.FailureOutcomeUnknown
			code = "feishu_send_timeout_unknown"
		}
		return ports.NewOperationError(disposition, code, ctxErr)
	}
	if errors.Is(err, context.Canceled) {
		return ports.NewOperationError(domain.FailureCancelled, "feishu_send_cancelled", context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ports.NewOperationError(domain.FailureOutcomeUnknown, "feishu_send_timeout_unknown", context.DeadlineExceeded)
	}
	return ports.NewOperationError(domain.FailureRetryable, "feishu_transport_retryable", fmt.Errorf("%s: transport failure", operation))
}

func responseFailure(code string, resp *larkcore.ApiResp) error {
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	disposition := domain.FailurePermanent
	switch {
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		disposition = domain.FailureOutcomeUnknown
	case status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
		disposition = domain.FailureRetryable
	}
	return ports.NewOperationError(disposition, code, nil)
}

var _ ports.DeliverySender = (*Sender)(nil)

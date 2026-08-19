package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
		return "", err
	}
	card, err := renderDeliveryCard(delivery)
	if err != nil {
		return "", err
	}
	content, err := marshalCard(card)
	if err != nil {
		return "", err
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
		return "", errors.New("reply Feishu receipt: empty response")
	}
	if !resp.Success() {
		return "", fmt.Errorf("reply Feishu receipt: code=%d request_id=%s", resp.Code, responseRequestID(resp.ApiResp))
	}
	if resp.Data == nil || resp.Data.MessageId == nil || *resp.Data.MessageId == "" {
		return "", errors.New("reply Feishu receipt: response message ID is missing")
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
		return errors.New("patch Feishu card: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("patch Feishu card: code=%d request_id=%s", resp.Code, responseRequestID(resp.ApiResp))
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

func responseRequestID(resp *larkcore.ApiResp) string {
	if resp == nil {
		return ""
	}
	return resp.RequestId()
}

func safeTransportError(ctx context.Context, operation string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w", operation, ctxErr)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: transport failure", operation)
}

var _ ports.DeliverySender = (*Sender)(nil)

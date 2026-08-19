package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"logagent/internal/domain"
)

type fakeSDKMessageAPI struct {
	replyReq   *larkim.ReplyMessageReq
	patchReq   *larkim.PatchMessageReq
	replyCalls int
	patchCalls int
	replyResp  *larkim.ReplyMessageResp
	patchResp  *larkim.PatchMessageResp
	err        error
}

func (f *fakeSDKMessageAPI) Reply(_ context.Context, req *larkim.ReplyMessageReq, _ ...larkcore.RequestOptionFunc) (*larkim.ReplyMessageResp, error) {
	f.replyCalls++
	f.replyReq = req
	return f.replyResp, f.err
}

func (f *fakeSDKMessageAPI) Patch(_ context.Context, req *larkim.PatchMessageReq, _ ...larkcore.RequestOptionFunc) (*larkim.PatchMessageResp, error) {
	f.patchCalls++
	f.patchReq = req
	return f.patchResp, f.err
}

type fakeCardMessageClient struct {
	replySource  string
	replyContent string
	replyUUID    string
	patchID      string
	patchContent string
	replyID      string
	replyCalls   int
	patchCalls   int
	err          error
}

func (f *fakeCardMessageClient) ReplyCard(_ context.Context, sourceMessageID, content, uuid string) (string, error) {
	f.replyCalls++
	f.replySource = sourceMessageID
	f.replyContent = content
	f.replyUUID = uuid
	return f.replyID, f.err
}

func (f *fakeCardMessageClient) PatchCard(_ context.Context, cardMessageID, content string) error {
	f.patchCalls++
	f.patchID = cardMessageID
	f.patchContent = content
	return f.err
}

func TestSenderRepliesInteractiveReceiptWithStableUUID(t *testing.T) {
	messageID := "om_card"
	api := &fakeCardMessageClient{replyID: messageID}
	sender, err := newSender("cli_test", api)
	if err != nil {
		t.Fatal(err)
	}
	delivery := testDelivery(domain.DeliveryQueued)

	firstID, err := sender.Deliver(context.Background(), delivery)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != messageID || api.replyCalls != 1 || api.patchCalls != 0 {
		t.Fatalf("unexpected reply result: id=%q reply=%d patch=%d", firstID, api.replyCalls, api.patchCalls)
	}
	if api.replySource != "om_source" {
		t.Fatalf("unexpected reply source %q", api.replySource)
	}
	if api.replyUUID == "" {
		t.Fatal("receipt UUID is missing")
	}
	firstUUID := api.replyUUID
	if !strings.Contains(api.replyContent, `"schema":"2.0"`) {
		t.Fatalf("receipt does not contain a JSON 2.0 card: %s", api.replyContent)
	}

	if _, err := sender.Deliver(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if got := api.replyUUID; got != firstUUID {
		t.Fatalf("receipt UUID changed across retries: %q != %q", got, firstUUID)
	}
	if len(firstUUID) != len("la_")+32 {
		t.Fatalf("unexpected stable UUID format %q", firstUUID)
	}
}

func TestSenderPatchesExistingCard(t *testing.T) {
	api := &fakeCardMessageClient{}
	sender, err := newSender("cli_test", api)
	if err != nil {
		t.Fatal(err)
	}
	delivery := testDelivery(domain.DeliveryRunning)
	delivery.Target.CardMessageID = "om_existing"

	messageID, err := sender.Deliver(context.Background(), delivery)
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "om_existing" || api.patchCalls != 1 || api.replyCalls != 0 {
		t.Fatalf("unexpected patch result: id=%q reply=%d patch=%d", messageID, api.replyCalls, api.patchCalls)
	}
	if api.patchID != "om_existing" {
		t.Fatalf("unexpected patch message ID %q", api.patchID)
	}
	if !strings.Contains(api.patchContent, "调查执行中") {
		t.Fatalf("running card content missing: %s", api.patchContent)
	}
}

func TestSenderRejectsNonQueuedCreateAndWrongApp(t *testing.T) {
	api := &fakeCardMessageClient{}
	sender, err := newSender("cli_test", api)
	if err != nil {
		t.Fatal(err)
	}
	delivery := testDelivery(domain.DeliveryRunning)
	if _, err := sender.Deliver(context.Background(), delivery); err == nil {
		t.Fatal("non-queued delivery without a card message ID was accepted")
	}
	delivery = testDelivery(domain.DeliveryQueued)
	delivery.Target.AppID = "cli_other"
	if _, err := sender.Deliver(context.Background(), delivery); err == nil {
		t.Fatal("cross-app delivery was accepted")
	}
	if api.replyCalls != 0 || api.patchCalls != 0 {
		t.Fatalf("invalid delivery reached SDK: reply=%d patch=%d", api.replyCalls, api.patchCalls)
	}
}

func TestSenderDoesNotExposeProviderMessageOnAPIFailure(t *testing.T) {
	api := &fakeSDKMessageAPI{replyResp: &larkim.ReplyMessageResp{
		ApiResp:   &larkcore.ApiResp{},
		CodeError: larkcore.CodeError{Code: 999, Msg: "secret provider detail"},
	}}
	sdk := &sdkCardMessageClient{messages: api}
	_, err := sdk.ReplyCard(context.Background(), "om_source", `{}`, "stable")
	if err == nil || strings.Contains(err.Error(), "secret provider detail") {
		t.Fatalf("unsafe API error: %v", err)
	}
}

func TestSDKSenderDoesNotExposeTransportDetails(t *testing.T) {
	api := &fakeSDKMessageAPI{err: errors.New("dial secret.internal.example with token=super-secret")}
	sdk := &sdkCardMessageClient{messages: api}
	_, err := sdk.ReplyCard(context.Background(), "om_source", `{}`, "stable")
	if err == nil || strings.Contains(err.Error(), "secret.internal") || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestSenderPropagatesTransportError(t *testing.T) {
	wantErr := errors.New("network unavailable")
	api := &fakeCardMessageClient{err: wantErr}
	sender, err := newSender("cli_test", api)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sender.Deliver(context.Background(), testDelivery(domain.DeliveryQueued))
	if !errors.Is(err, wantErr) {
		t.Fatalf("want wrapped transport error, got %v", err)
	}
}

func TestSDKClientUsesReplyAndPatchCardAPIs(t *testing.T) {
	type request struct {
		method string
		path   string
		body   map[string]interface{}
	}
	requests := make([]request, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.URL.Path, "tenant_access_token") {
			_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"tenant-token","expire":7200}`))
			return
		}
		var body map[string]interface{}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode SDK request: %v", err)
		}
		requests = append(requests, request{method: req.Method, path: req.URL.Path, body: body})
		if strings.HasSuffix(req.URL.Path, "/reply") {
			_, _ = writer.Write([]byte(`{"code":0,"msg":"ok","data":{"message_id":"om_created"}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	defer server.Close()

	client := lark.NewClient(
		"cli_test", "secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
		lark.WithReqTimeout(time.Second),
		lark.WithLogLevel(larkcore.LogLevelError),
	)
	sdk := &sdkCardMessageClient{messages: client.Im.V1.Message}
	messageID, err := sdk.ReplyCard(context.Background(), "om_source", `{"schema":"2.0"}`, "stable-uuid")
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "om_created" {
		t.Fatalf("unexpected SDK reply message ID %q", messageID)
	}
	if err := sdk.PatchCard(context.Background(), "om_created", `{"schema":"2.0"}`); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("want two message requests, got %#v", requests)
	}
	if requests[0].method != http.MethodPost || requests[0].path != "/open-apis/im/v1/messages/om_source/reply" {
		t.Fatalf("unexpected reply request: %#v", requests[0])
	}
	if requests[0].body["msg_type"] != "interactive" || requests[0].body["uuid"] != "stable-uuid" {
		t.Fatalf("unexpected reply body: %#v", requests[0].body)
	}
	if requests[1].method != http.MethodPatch || requests[1].path != "/open-apis/im/v1/messages/om_created" {
		t.Fatalf("unexpected patch request: %#v", requests[1])
	}
}

func testDelivery(kind domain.DeliveryKind) domain.DeliveryJob {
	end := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	status := domain.StatusQueued
	if kind == domain.DeliveryRunning {
		status = domain.StatusRunning
	}
	return domain.DeliveryJob{
		ID:   "delivery_test",
		Kind: kind,
		Investigation: domain.Investigation{
			ID:     "inv_test",
			Status: status,
			Request: domain.InvestigationRequest{
				Service: "order-service", Environment: "prod",
				StartTime: end.Add(-30 * time.Minute), EndTime: end,
			},
		},
		Target: domain.InteractionTarget{
			AppID: "cli_test", TenantKey: "tenant_test", ChatID: "oc_chat", SourceMessageID: "om_source",
		},
	}
}

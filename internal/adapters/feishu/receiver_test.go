package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type fakeIntake struct {
	inbound domain.InboundMessage
	request domain.InvestigationRequest
	calls   int
	err     error
}

type blockingIntake struct{}

type fakeActionHandler struct {
	command domain.ActionCommand
	result  domain.ActionResult
	calls   int
	err     error
}

func (blockingIntake) Accept(ctx context.Context, _ domain.InboundMessage, _ domain.InvestigationRequest) (string, bool, error) {
	<-ctx.Done()
	return "", false, ctx.Err()
}

func (f *fakeIntake) Accept(_ context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest) (string, bool, error) {
	f.calls++
	f.inbound = inbound
	f.request = request
	return "inv_test", true, f.err
}

func (f *fakeActionHandler) Handle(_ context.Context, command domain.ActionCommand) (domain.ActionResult, error) {
	f.calls++
	f.command = command
	return f.result, f.err
}

func TestReceiverMapsAndPersistsTextMessage(t *testing.T) {
	intake := &fakeIntake{}
	receiver, err := New("cli_test", "secret", intake)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{
  "schema":"2.0",
  "header":{"event_id":"event-1","event_type":"im.message.receive_v1","app_id":"cli_test","tenant_key":"tenant-1","create_time":"1787049000000"},
  "event":{
    "sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user","tenant_key":"tenant-1"},
    "message":{"message_id":"om_message","create_time":"1787049000000","chat_id":"oc_chat","chat_type":"p2p","message_type":"text","content":"{\"text\":\"/investigate order-service prod 30m\"}"}
  }
}`)
	if _, err := receiver.dispatcher.Do(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if intake.calls != 1 {
		t.Fatalf("want one durable intake call, got %d", intake.calls)
	}
	if intake.inbound.MessageID != "om_message" || intake.inbound.TenantKey != "tenant-1" || intake.inbound.UserID != "ou_user" {
		t.Fatalf("unexpected inbound mapping: %#v", intake.inbound)
	}
	if intake.request.Service != "order-service" || intake.request.EndTime.Sub(intake.request.StartTime) != 30*time.Minute {
		t.Fatalf("unexpected request: %#v", intake.request)
	}
	wantEnd := time.UnixMilli(1787049000000).UTC().Add(-domain.DefaultIngestionGrace).Truncate(time.Second)
	if !intake.request.EndTime.Equal(wantEnd) {
		t.Fatalf("request did not use the ingestion watermark: got=%s want=%s", intake.request.EndTime, wantEnd)
	}
}

func TestReceiverReturnsPersistenceError(t *testing.T) {
	wantErr := errors.New("database unavailable")
	receiver, err := New("cli_test", "secret", &fakeIntake{err: wantErr})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{
  "schema":"2.0",
  "header":{"event_id":"event-1","event_type":"im.message.receive_v1","app_id":"cli_test","tenant_key":"tenant-1"},
  "event":{
    "sender":{"sender_id":{"open_id":"ou_user"}},
    "message":{"message_id":"om_message","chat_id":"oc_chat","chat_type":"p2p","message_type":"text","content":"{\"text\":\"/investigate order-service prod 30m\"}"}
  }
}`)
	_, err = receiver.dispatcher.Do(context.Background(), payload)
	if !errors.Is(err, wantErr) {
		t.Fatalf("want persistence error, got %v", err)
	}
}

func TestReceiverAcknowledgesPermanentlyInvalidEvent(t *testing.T) {
	intake := &fakeIntake{}
	receiver, err := New("cli_test", "secret", intake)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{
  "schema":"2.0",
  "header":{"event_id":"event-1","event_type":"im.message.receive_v1","app_id":"cli_test","tenant_key":"tenant-1"},
  "event":{"message":{"message_type":"text","content":"{\"text\":\"/investigate order-service prod 30m\"}"}}
}`)
	if _, err := receiver.dispatcher.Do(context.Background(), payload); err != nil {
		t.Fatalf("permanent envelope error should be acknowledged, got %v", err)
	}
	if intake.calls != 0 {
		t.Fatalf("invalid event reached durable intake %d times", intake.calls)
	}
}

func TestReceiverBoundsDurableIntakeTime(t *testing.T) {
	receiver, err := New("cli_test", "secret", blockingIntake{})
	if err != nil {
		t.Fatal(err)
	}
	receiver.persistBudget = 10 * time.Millisecond
	payload := []byte(`{
  "schema":"2.0",
  "header":{"event_id":"event-1","event_type":"im.message.receive_v1","app_id":"cli_test","tenant_key":"tenant-1"},
  "event":{
    "sender":{"sender_id":{"open_id":"ou_user"}},
    "message":{"message_id":"om_message","chat_id":"oc_chat","chat_type":"p2p","message_type":"text","content":"{\"text\":\"/investigate order-service prod 30m\"}"}
  }
}`)
	_, err = receiver.dispatcher.Do(context.Background(), payload)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want bounded persistence deadline, got %v", err)
	}
}

func TestReceiverRequiresBotMentionInGroup(t *testing.T) {
	intake := &fakeIntake{}
	receiver, err := New("cli_test", "secret", intake)
	if err != nil {
		t.Fatal(err)
	}
	withoutMention := []byte(`{
  "schema":"2.0",
  "header":{"event_id":"event-group-1","event_type":"im.message.receive_v1","app_id":"cli_test","tenant_key":"tenant-1"},
  "event":{"sender":{"sender_id":{"open_id":"ou_user"}},"message":{"message_id":"om_group_1","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"/investigate order-service prod 30m\"}"}}
}`)
	if _, err := receiver.dispatcher.Do(context.Background(), withoutMention); err != nil {
		t.Fatal(err)
	}
	if intake.calls != 0 {
		t.Fatal("group command without a bot mention reached intake")
	}

	withMention := []byte(`{
  "schema":"2.0",
  "header":{"event_id":"event-group-2","event_type":"im.message.receive_v1","app_id":"cli_test","tenant_key":"tenant-1"},
  "event":{"sender":{"sender_id":{"open_id":"ou_user"}},"message":{"message_id":"om_group_2","chat_id":"oc_group","chat_type":"group","message_type":"text","content":"{\"text\":\"@_user_1 /investigate order-service prod 30m\"}","mentions":[{"key":"@_user_1","id":{"open_id":"ou_bot"},"mentioned_type":"bot","name":"logagent"}]}}
}`)
	if _, err := receiver.dispatcher.Do(context.Background(), withMention); err != nil {
		t.Fatal(err)
	}
	if intake.calls != 1 {
		t.Fatalf("mentioned group command was not accepted: calls=%d", intake.calls)
	}
}

func TestReceiverMapsMutatingCardActionAndReturnsToastOnly(t *testing.T) {
	actions := &fakeActionHandler{result: domain.ActionResult{
		View: domain.ActionViewCancelledCard,
		Investigation: domain.Investigation{
			ID: "inv_action", Status: domain.StatusCancelled,
			Request: domain.InvestigationRequest{Service: "order-service", Environment: "prod"},
		},
	}}
	receiver, err := New("cli_test", "secret", &fakeIntake{}, WithActionHandler(actions))
	if err != nil {
		t.Fatal(err)
	}
	response, err := receiver.dispatcher.Do(context.Background(), cardActionPayload(`{"action":"cancel","investigation_id":"inv_action"}`))
	if err != nil {
		t.Fatal(err)
	}
	if actions.calls != 1 {
		t.Fatalf("want one action call, got %d", actions.calls)
	}
	command := actions.command
	if command.EventID != "evt_action" || command.Action != domain.ActionCancel || command.InvestigationID != "inv_action" {
		t.Fatalf("unexpected command: %#v", command)
	}
	if command.Principal != (domain.Principal{AppID: "cli_test", TenantKey: "tenant-1", UserID: "ou_actor"}) {
		t.Fatalf("unexpected principal: %#v", command.Principal)
	}
	if command.ChatID != "oc_chat" || command.CardMessageID != "om_card" {
		t.Fatalf("unexpected card context: %#v", command)
	}
	if command.OccurredAt.IsZero() {
		t.Fatal("callback time was not mapped")
	}
	callbackResponse, ok := response.(*callback.CardActionTriggerResponse)
	if !ok || callbackResponse.Card != nil || callbackResponse.Toast == nil {
		t.Fatalf("unexpected callback response: %#v", response)
	}
}

func TestReceiverReturnsCardJSONForReadOnlyViewAction(t *testing.T) {
	actions := &fakeActionHandler{result: domain.ActionResult{
		View: domain.ActionViewReportCard,
		Investigation: domain.Investigation{
			ID: "inv_action", Status: domain.StatusSucceeded,
			Request: domain.InvestigationRequest{Service: "order-service", Environment: "prod"},
			Report: &domain.Report{
				InvestigationID: "inv_action", Outcome: "data_insufficient", GeneratedAt: time.Now().UTC(),
			},
		},
	}}
	receiver, err := New("cli_test", "secret", &fakeIntake{}, WithActionHandler(actions))
	if err != nil {
		t.Fatal(err)
	}
	response, err := receiver.dispatcher.Do(context.Background(), cardActionPayload(`{"action":"view_report","investigation_id":"inv_action"}`))
	if err != nil {
		t.Fatal(err)
	}
	callbackResponse, ok := response.(*callback.CardActionTriggerResponse)
	if !ok || callbackResponse.Card == nil || callbackResponse.Card.Type != "card_json" || callbackResponse.Toast == nil {
		t.Fatalf("unexpected callback response: %#v", response)
	}
}

func TestReceiverMapsExplicitCostAcknowledgementAction(t *testing.T) {
	actions := &fakeActionHandler{result: domain.ActionResult{
		View: domain.ActionViewQueuedCard,
		Investigation: domain.Investigation{
			ID: "inv_derived", Status: domain.StatusQueued,
			Request: domain.InvestigationRequest{Service: "order-service", Environment: "prod"},
		},
		Created: true,
	}}
	receiver, err := New("cli_test", "secret", &fakeIntake{}, WithActionHandler(actions))
	if err != nil {
		t.Fatal(err)
	}
	response, err := receiver.dispatcher.Do(context.Background(), cardActionPayload(`{"action":"rerun_with_cost_ack","investigation_id":"inv_action"}`))
	if err != nil {
		t.Fatal(err)
	}
	if actions.calls != 1 || actions.command.Action != domain.ActionRerunWithCostAck {
		t.Fatalf("explicit cost acknowledgement was not mapped: calls=%d command=%#v", actions.calls, actions.command)
	}
	callbackResponse := response.(*callback.CardActionTriggerResponse)
	if callbackResponse.Toast == nil || callbackResponse.Toast.Type != "success" {
		t.Fatalf("unexpected acknowledgement response: %#v", callbackResponse)
	}
}

func TestReceiverAcknowledgesForbiddenAndInvalidActions(t *testing.T) {
	forbidden := &fakeActionHandler{err: ports.ErrActionForbidden}
	receiver, err := New("cli_test", "secret", &fakeIntake{}, WithActionHandler(forbidden))
	if err != nil {
		t.Fatal(err)
	}
	response, err := receiver.dispatcher.Do(context.Background(), cardActionPayload(`{"action":"cancel","investigation_id":"inv_action"}`))
	if err != nil {
		t.Fatalf("forbidden action should be acknowledged: %v", err)
	}
	callbackResponse := response.(*callback.CardActionTriggerResponse)
	if callbackResponse.Toast == nil || callbackResponse.Card != nil || callbackResponse.Toast.Type != "error" {
		t.Fatalf("unexpected forbidden response: %#v", callbackResponse)
	}

	invalid := &fakeActionHandler{}
	receiver, err = New("cli_test", "secret", &fakeIntake{}, WithActionHandler(invalid))
	if err != nil {
		t.Fatal(err)
	}
	response, err = receiver.dispatcher.Do(context.Background(), cardActionPayload(`{"action":"cancel","investigation_id":"inv_action","duration":"24h"}`))
	if err != nil {
		t.Fatalf("invalid action should be acknowledged: %v", err)
	}
	if invalid.calls != 0 {
		t.Fatal("action value with an extra field reached the application")
	}
	callbackResponse = response.(*callback.CardActionTriggerResponse)
	if callbackResponse.Toast == nil || callbackResponse.Toast.Type != "error" {
		t.Fatalf("unexpected invalid response: %#v", callbackResponse)
	}
}

func TestReceiverReturnsTransientActionError(t *testing.T) {
	wantErr := errors.New("database unavailable")
	actions := &fakeActionHandler{err: wantErr}
	receiver, err := New("cli_test", "secret", &fakeIntake{}, WithActionHandler(actions))
	if err != nil {
		t.Fatal(err)
	}
	_, err = receiver.dispatcher.Do(context.Background(), cardActionPayload(`{"action":"view_report","investigation_id":"inv_action"}`))
	if !errors.Is(err, wantErr) {
		t.Fatalf("want transient infrastructure error, got %v", err)
	}
}

func TestReceiverRejectsMismatchedCallbackTenant(t *testing.T) {
	actions := &fakeActionHandler{}
	receiver, err := New("cli_test", "secret", &fakeIntake{}, WithActionHandler(actions))
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Replace(string(cardActionPayload(`{"action":"cancel","investigation_id":"inv_action"}`)), `"tenant_key":"tenant-1"`, `"tenant_key":"other-tenant"`, 1)
	response, err := receiver.dispatcher.Do(context.Background(), []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if actions.calls != 0 {
		t.Fatal("mismatched operator tenant reached the application")
	}
	callbackResponse := response.(*callback.CardActionTriggerResponse)
	if callbackResponse.Toast == nil || callbackResponse.Toast.Type != "error" {
		t.Fatalf("unexpected tenant rejection: %#v", callbackResponse)
	}
}

func cardActionPayload(value string) []byte {
	return []byte(`{
  "schema":"2.0",
  "header":{"event_id":"evt_action","event_type":"card.action.trigger","app_id":"cli_test","tenant_key":"tenant-1","create_time":"1787094000000"},
  "event":{
    "operator":{"open_id":"ou_actor","tenant_key":"tenant-1"},
    "host":"im_message",
    "context":{"open_message_id":"om_card","open_chat_id":"oc_chat"},
    "action":{"tag":"button","value":` + value + `}
  }
}`)
}

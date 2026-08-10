package executionruntime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantProcessing/funding-trader/backend/internal/executionmonitor"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
)

func TestPrivateEventGatewayAdaptsConcreteGatewayStream(t *testing.T) {
	want := &boltertrader.PrivateEventStream{}
	client := &fakePrivateEventClient{stream: want}
	adapter := NewPrivateEventGateway(client)
	request := boltertrader.PrivateEventSubscribeRequest{
		AccountID: "account-1", Cursor: "boot:7", Kinds: []string{"order", "fill"},
	}

	got, err := adapter.SubscribePrivateEvents(context.Background(), request)

	if err != nil {
		t.Fatal(err)
	}
	if got != want || !reflect.DeepEqual(client.request, request) {
		t.Fatalf("adapter stream=%T request=%+v", got, client.request)
	}
}

func TestExecutionReconcilerUsesExactScopeAndUniqueSafeRequestIDs(t *testing.T) {
	service := &fakeRecoveryService{}
	reconciler := NewExecutionReconciler(service, executionservice.Scope{
		PrincipalID: "principal-default",
		Mode:        "test",
	}, "boot-safe")

	if err := reconciler.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(service.requestIDs) != 2 ||
		service.scopes[0].PrincipalID != "principal-default" ||
		service.scopes[0].Mode != "test" ||
		service.requestIDs[0] == service.requestIDs[1] ||
		!strings.HasPrefix(service.requestIDs[0], "private-event-boot-safe-") {
		t.Fatalf("recovery calls scopes=%+v requestIDs=%+v", service.scopes, service.requestIDs)
	}
}

func TestExecutionReconcilerRejectsNonProductModeScope(t *testing.T) {
	service := &fakeRecoveryService{}
	reconciler := NewExecutionReconciler(service, executionservice.Scope{
		PrincipalID: "principal-default",
		Mode:        "paper",
	}, "boot-safe")

	if err := reconciler.Recover(context.Background()); err == nil {
		t.Fatal("expected non-product mode to be rejected")
	}
	if len(service.requestIDs) != 0 {
		t.Fatalf("recovery was called for invalid mode: scopes=%+v requestIDs=%+v", service.scopes, service.requestIDs)
	}
}

func TestPrivateEventResnapshotPublisherEmitsCanonicalBrowserEvent(t *testing.T) {
	stream := &fakePrivateEventPublisher{}
	publisher := NewPrivateEventResnapshotPublisher(stream)

	err := publisher.PublishResnapshotRequired(context.Background(), executionmonitor.Resnapshot{
		Scope:  []string{"executions", "positions", "activities"},
		Reason: "sequence-gap",
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(stream.inputs) != 1 ||
		stream.inputs[0].Scope != "all" ||
		stream.inputs[0].Kind != "resnapshot.required" {
		t.Fatalf("stream inputs=%+v", stream.inputs)
	}
	var payload struct {
		Scope  []string `json:"scope"`
		Reason string   `json:"reason"`
	}
	if err := json.Unmarshal(stream.inputs[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Scope, []string{"executions", "positions", "activities"}) ||
		payload.Reason != "sequence-gap" {
		t.Fatalf("resnapshot payload=%+v", payload)
	}
	raw := string(stream.inputs[0].Data)
	for _, forbidden := range []string{"principal", "environment", "gatewayCursor", "token"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("resnapshot payload leaked %q: %s", forbidden, raw)
		}
	}
}

func TestPrivateEventResnapshotPublisherRejectsInvalidScopeAndSafeReason(t *testing.T) {
	publisher := NewPrivateEventResnapshotPublisher(&fakePrivateEventPublisher{})
	for _, snapshot := range []executionmonitor.Resnapshot{
		{Scope: []string{"all"}, Reason: "sequence-gap"},
		{Scope: []string{"executions"}, Reason: ""},
		{Scope: []string{"executions"}, Reason: "raw secret"},
	} {
		if err := publisher.PublishResnapshotRequired(context.Background(), snapshot); err == nil {
			t.Fatalf("invalid snapshot accepted: %+v", snapshot)
		}
	}
}

type fakePrivateEventClient struct {
	stream  *boltertrader.PrivateEventStream
	request boltertrader.PrivateEventSubscribeRequest
	err     error
}

func (client *fakePrivateEventClient) SubscribePrivateEvents(
	_ context.Context,
	request boltertrader.PrivateEventSubscribeRequest,
) (*boltertrader.PrivateEventStream, error) {
	client.request = request
	return client.stream, client.err
}

type fakeRecoveryService struct {
	scopes     []executionservice.Scope
	requestIDs []string
	err        error
}

func (service *fakeRecoveryService) Recover(
	_ context.Context,
	scope executionservice.Scope,
	requestID string,
) (executionservice.Result, error) {
	service.scopes = append(service.scopes, scope)
	service.requestIDs = append(service.requestIDs, requestID)
	return executionservice.Result{}, service.err
}

type fakePrivateEventPublisher struct {
	inputs []uistream.EventInput
	err    error
}

func (publisher *fakePrivateEventPublisher) Publish(
	_ context.Context,
	input uistream.EventInput,
) (uistream.StoredEvent, error) {
	publisher.inputs = append(publisher.inputs, input)
	if publisher.err != nil {
		return uistream.StoredEvent{}, publisher.err
	}
	return uistream.StoredEvent{Sequence: 1, Cursor: "browser-cursor"}, nil
}

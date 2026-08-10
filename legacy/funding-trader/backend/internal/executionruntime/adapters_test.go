package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/store/postgres"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/shopspring/decimal"
)

func TestStoreAdapterMapsPostgresSentinelsAndCommittedEvents(t *testing.T) {
	ctx := context.Background()
	scope := executionservice.Scope{PrincipalID: "private-user", Mode: "test"}
	fake := &fakeExecutionStore{
		getIDErr: postgres.ErrExecutionNotFound,
		events: postgres.PersistedTransitionEvents{
			Execution: readmodel.Event{
				ID: 41, Kind: readmodel.ExecutionUpsert, EntityID: "exec-1",
				Payload: json.RawMessage(`{"id":"exec-1"}`), OccurredAt: time.Unix(41, 0).UTC(),
			},
			Audit: readmodel.Event{
				ID: 42, Kind: readmodel.AuditAppend, EntityID: "exec-1",
				Payload: json.RawMessage(`{"auditId":"42"}`), OccurredAt: time.Unix(42, 0).UTC(),
			},
		},
	}
	adapter := NewStoreAdapter(fake)

	_, err := adapter.GetExecution(ctx, scope, "missing")
	if !errors.Is(err, executionservice.ErrNotFound) {
		t.Fatalf("GetExecution err = %v, want executionservice.ErrNotFound", err)
	}

	events, err := adapter.PersistTransition(ctx, executionservice.PersistTransitionInput{
		Scope: scope,
		Execution: execution.Execution{
			ID:          "exec-1",
			PrincipalID: scope.PrincipalID,
			Mode:        scope.Mode,
			State:       execution.StateSubmitting,
		},
		ExpectedVersion: 7,
		Kind:            "begin_submit",
		RequestID:       "req-1",
		Payload:         json.RawMessage(`{"safe":true}`),
		OccurredAt:      time.Unix(40, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("PersistTransition: %v", err)
	}
	if fake.persist.Scope != (readmodel.Scope{PrincipalID: scope.PrincipalID, Mode: scope.Mode}) {
		t.Fatalf("scope not mapped: %#v", fake.persist.Scope)
	}
	if len(events) != 2 || events[0].ID != "41" || events[0].Kind != string(readmodel.ExecutionUpsert) ||
		events[1].ID != "42" || events[1].Kind != string(readmodel.AuditAppend) {
		t.Fatalf("events not mapped: %#v", events)
	}
}

func TestStoreAdapterReserveReplayCarriesVersionAndState(t *testing.T) {
	ctx := context.Background()
	scope := executionservice.Scope{PrincipalID: "private-user", Mode: "test"}
	exec := execution.Execution{ID: "exec-1", PrincipalID: scope.PrincipalID, Mode: scope.Mode, State: execution.StateSubmitting}
	fake := &fakeExecutionStore{
		reservation: postgres.ExecutionReservation{
			Execution: exec,
			Replayed:  true,
		},
		idempotency: postgres.IdempotencyRecord{
			ExecutionID:  "exec-1",
			RequestHash:  "hash-1",
			State:        "completed",
			ResponseCode: 202,
			ResponseBody: json.RawMessage(`{"executionId":"exec-1"}`),
		},
		stored: postgres.StoredExecution{Execution: exec, Version: 9, Status: string(execution.StateSubmitting)},
	}
	adapter := NewStoreAdapter(fake)

	reservation, err := adapter.ReserveExecution(ctx, scope, "POST", "/api/v1/orders", "idem-1", "hash-1", exec)
	if err != nil {
		t.Fatalf("ReserveExecution: %v", err)
	}
	if !reservation.Replayed || reservation.Version != 9 || reservation.State != "completed" ||
		reservation.ResponseCode != 202 || string(reservation.ResponseBody) != `{"executionId":"exec-1"}` {
		t.Fatalf("reservation replay metadata lost: %#v", reservation)
	}
}

func TestPublisherBroadcastsCommittedReadModelEventsWithoutAppending(t *testing.T) {
	store := &memoryUIStore{}
	stream, err := uistream.NewService(uistream.ServiceConfig{
		BootID: "boot-1", PrincipalID: "principal-1", Mode: "test",
		SigningKey: []byte("0123456789abcdef0123456789abcdef"),
		Store:      store, SnapshotSource: emptySnapshot{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	publisher := NewPublisher(stream)
	err = publisher.Publish(context.Background(), []executionservice.CommittedEvent{{
		ID: "7", Kind: string(readmodel.ExecutionUpsert), EntityID: "exec-1",
		Payload: json.RawMessage(`{"id":"exec-1"}`), Occurred: time.Unix(7, 0).UTC(),
	}})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if store.appended {
		t.Fatal("Publisher appended an already committed event")
	}
}

func TestGatewayAdapterPlacesMarketOrderAndPreservesRealGatewayOrderID(t *testing.T) {
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"place":true,"cancel":true}}}]}`), nil
		case "/v1/orders":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s", r.Method)
			}
			var body boltertrader.PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Type != "market" || body.IdempotencyKey != "exec-1:long" || r.Header.Get("Idempotency-Key") != "exec-1:long" {
				t.Fatalf("bad place body/header: %#v header=%s", body, r.Header.Get("Idempotency-Key"))
			}
			return gatewayJSONStatus(http.StatusAccepted, `{"command_id":"exec-1:long","gateway_order_id":"venue-order-1","client_order_id":"exec-1:long","state":"accepted"}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})

	result, err := NewGatewayAdapter(client).PlaceOrder(context.Background(), executionservice.GatewayOrderRequest{
		GatewayID: "exec-1:long", IdempotencyKey: "exec-1:long", AccountID: "acct", InstrumentID: "inst",
		Side: execution.SideBuy, Quantity: decimal.RequireFromString("0.5"), Role: execution.LegRoleLong,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if result.Outcome != executionservice.GatewayAccepted || result.GatewayID != "exec-1:long" || result.OrderID != "venue-order-1" {
		t.Fatalf("result did not preserve command and real order id: %#v", result)
	}
}

func TestGatewayAdapterRetriesAmbiguousPlaceOnceWithSameIdempotencyKey(t *testing.T) {
	placeAttempts := 0
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"place":true}}}]}`), nil
		case "/v1/orders":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s", r.Method)
			}
			placeAttempts++
			var body boltertrader.PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.IdempotencyKey != "exec-1:long" || r.Header.Get("Idempotency-Key") != "exec-1:long" {
				t.Fatalf("retry changed idempotency: body=%#v header=%s", body, r.Header.Get("Idempotency-Key"))
			}
			if placeAttempts == 1 {
				return gatewayJSONStatus(http.StatusBadGateway, `{"error":{"code":"gateway_upstream_timeout","retryable":true}}`), nil
			}
			return gatewayJSONStatus(http.StatusAccepted, `{"command_id":"cmd-uuid-1","gateway_order_id":"venue-order-1","client_order_id":"1","state":"filled"}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})

	result, err := NewGatewayAdapter(client).PlaceOrder(context.Background(), executionservice.GatewayOrderRequest{
		GatewayID: "exec-1:long", IdempotencyKey: "exec-1:long", AccountID: "acct", InstrumentID: "inst",
		Side: execution.SideBuy, Quantity: decimal.RequireFromString("0.5"),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if placeAttempts != 2 {
		t.Fatalf("place attempts = %d, want one same-key retry", placeAttempts)
	}
	if result.Outcome != executionservice.GatewayAccepted || result.GatewayID != "exec-1:long" || result.OrderID != "venue-order-1" {
		t.Fatalf("replayed accepted result not mapped to logical id + real order id: %#v", result)
	}
}

func TestGatewayAdapterDoesNotRetryNonretryablePlaceRejection(t *testing.T) {
	placeAttempts := 0
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"place":true}}}]}`), nil
		case "/v1/orders":
			placeAttempts++
			return gatewayJSONStatus(http.StatusBadRequest, `{"error":{"code":"min_notional","retryable":false}}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})

	result, err := NewGatewayAdapter(client).PlaceOrder(context.Background(), executionservice.GatewayOrderRequest{
		GatewayID: "exec-1:long", IdempotencyKey: "exec-1:long", AccountID: "acct", InstrumentID: "inst",
		Side: execution.SideBuy, Quantity: decimal.RequireFromString("0.5"),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if placeAttempts != 1 {
		t.Fatalf("place attempts = %d, want no retry for nonretryable 4xx", placeAttempts)
	}
	if result.Outcome != executionservice.GatewayRejected || result.Error.Code != "min_notional" {
		t.Fatalf("nonretryable rejection not preserved: %#v", result)
	}
}

func TestGatewayAdapterTreatsTemporarilyUnavailableAsPreAcceptanceRejectionWithoutRetry(t *testing.T) {
	placeAttempts := 0
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"gate","product":"perp","environment":"testnet","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"place":true}}}]}`), nil
		case "/v1/orders":
			placeAttempts++
			return gatewayJSONStatus(http.StatusServiceUnavailable, `{"error":{"code":"temporarily_unavailable","retryable":true}}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})

	result, err := NewGatewayAdapter(client).PlaceOrder(context.Background(), executionservice.GatewayOrderRequest{
		GatewayID: "exec-1:short", IdempotencyKey: "exec-1:short", AccountID: "acct", InstrumentID: "inst",
		Side: execution.SideSell, Quantity: decimal.RequireFromString("0.05"), Role: execution.LegRoleShort,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if placeAttempts != 1 {
		t.Fatalf("place attempts = %d, want no retry after definitive pre-acceptance rejection", placeAttempts)
	}
	if result.Outcome != executionservice.GatewayRejected || result.Error.Code != "temporarily_unavailable" || !result.Error.Retryable {
		t.Fatalf("pre-acceptance rejection not preserved: %#v", result)
	}
}

func TestGatewayAdapterStopsRetryWhenContextCanceledAfterAmbiguousPlace(t *testing.T) {
	placeAttempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"place":true}}}]}`), nil
		case "/v1/orders":
			placeAttempts++
			cancel()
			return gatewayJSONStatus(http.StatusBadGateway, `{"error":{"code":"gateway_upstream_timeout","retryable":true}}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})

	result, err := NewGatewayAdapter(client).PlaceOrder(ctx, executionservice.GatewayOrderRequest{
		GatewayID: "exec-1:long", IdempotencyKey: "exec-1:long", AccountID: "acct", InstrumentID: "inst",
		Side: execution.SideBuy, Quantity: decimal.RequireFromString("0.5"),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if placeAttempts != 1 {
		t.Fatalf("place attempts = %d, want no replay after cancellation", placeAttempts)
	}
	if result.Outcome != executionservice.GatewayAmbiguous || result.Error.Code != "gateway_upstream_timeout" {
		t.Fatalf("canceled ambiguous result not preserved: %#v", result)
	}
}

func TestGatewayAdapterKeepsSecondAmbiguousPlaceAsAmbiguous(t *testing.T) {
	placeAttempts := 0
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"place":true}}}]}`), nil
		case "/v1/orders":
			placeAttempts++
			return gatewayJSONStatus(http.StatusBadGateway, `{"error":{"code":"gateway_upstream_timeout","retryable":true}}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})

	result, err := NewGatewayAdapter(client).PlaceOrder(context.Background(), executionservice.GatewayOrderRequest{
		GatewayID: "exec-1:long", IdempotencyKey: "exec-1:long", AccountID: "acct", InstrumentID: "inst",
		Side: execution.SideBuy, Quantity: decimal.RequireFromString("0.5"),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if placeAttempts != 2 {
		t.Fatalf("place attempts = %d, want exactly one retry", placeAttempts)
	}
	if result.Outcome != executionservice.GatewayAmbiguous || !result.Error.Retryable || result.Error.Code != "gateway_upstream_timeout" {
		t.Fatalf("second ambiguous result not preserved: %#v", result)
	}
}

func TestGatewayAdapter409DifferentExistingCommandDoesNotBindOtherOrder(t *testing.T) {
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"place":true}}}]}`), nil
		case "/v1/orders":
			if r.Method != http.MethodPost {
				t.Fatalf("must not resolve a mismatched existing command with %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			return gatewayJSONStatus(http.StatusConflict, `{"error":{"code":"idempotency_conflict","retryable":false},"existing_command_id":"other-command"}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})

	result, err := NewGatewayAdapter(client).PlaceOrder(context.Background(), executionservice.GatewayOrderRequest{
		GatewayID: "exec-1:long", IdempotencyKey: "exec-1:long", AccountID: "acct", InstrumentID: "inst",
		Side: execution.SideBuy, Quantity: decimal.RequireFromString("0.5"),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if result.Outcome != executionservice.GatewayAmbiguous || result.OrderID != "" || result.Error.Code != "GATEWAY_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("mismatched conflict was not isolated as ambiguous: %#v", result)
	}
}

func TestGatewayAdapterMapsUncertainAndRejectedWithoutLeakingRawGatewayText(t *testing.T) {
	transportErr := errors.New("transport leaked-key sk-test-raw")
	ambiguousClient := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"place":true}}}]}`), nil
		default:
			return nil, transportErr
		}
	})
	ambiguous, err := NewGatewayAdapter(ambiguousClient).PlaceOrder(context.Background(), executionservice.GatewayOrderRequest{
		GatewayID: "exec-1:long", IdempotencyKey: "exec-1:long", AccountID: "acct", InstrumentID: "inst",
		Side: execution.SideBuy, Quantity: decimal.RequireFromString("0.5"),
	})
	if err != nil {
		t.Fatalf("ambiguous PlaceOrder returned raw error: %v", err)
	}
	if ambiguous.Outcome != executionservice.GatewayAmbiguous || strings.Contains(ambiguous.Error.SafeMessage, "sk-test-raw") {
		t.Fatalf("ambiguous result unsafe: %#v", ambiguous)
	}

	rejectedClient := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"place":true}}}]}`), nil
		case "/v1/orders":
			return gatewayJSONStatus(http.StatusBadRequest, `{"error":{"code":"min_notional","message":"raw venue secret","retryable":false}}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})
	rejected, err := NewGatewayAdapter(rejectedClient).PlaceOrder(context.Background(), executionservice.GatewayOrderRequest{
		GatewayID: "exec-1:long", IdempotencyKey: "exec-1:long", AccountID: "acct", InstrumentID: "inst",
		Side: execution.SideBuy, Quantity: decimal.RequireFromString("0.5"),
	})
	if err != nil {
		t.Fatalf("rejected PlaceOrder returned raw error: %v", err)
	}
	if rejected.Outcome != executionservice.GatewayRejected || rejected.Error.Code != "min_notional" ||
		strings.Contains(rejected.Error.SafeMessage, "raw venue secret") {
		t.Fatalf("rejected result unsafe: %#v", rejected)
	}
}

func TestGatewayAdapterFindsOrderAndIsolatesFillsByClientOrderID(t *testing.T) {
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/orders":
			if r.URL.Query().Get("account_id") != "acct" {
				t.Fatalf("orders query = %q", r.URL.RawQuery)
			}
			return gatewayJSON(`{"items":[{"command_id":"exec-1:long","gateway_order_id":"venue-order-1","client_order_id":"exec-1:long","state":"partially_filled"}]}`), nil
		case "/v1/fills":
			if r.URL.Query().Get("account_id") != "acct" || r.URL.Query().Get("instrument_id") != "inst" ||
				r.URL.Query().Get("gateway_order_id") != "venue-order-1" {
				t.Fatalf("fills query = %q", r.URL.RawQuery)
			}
			return gatewayJSON(`{"items":[
				{"account_id":"acct","instrument_id":"inst","gateway_order_id":"venue-order-1","fill":{"fill_id":"fill-good","order_id":"15933226393","quantity":"0.3","price":"100.25","fee":"0.01","fee_asset":"USDT","time":"2026-07-29T01:02:03Z"}},
				{"account_id":"acct","instrument_id":"inst","gateway_order_id":"venue-order-other","fill":{"fill_id":"fill-other","order_id":"15933226394","quantity":"9","price":"1","fee":"0","fee_asset":"USDT","time":"2026-07-29T01:02:03Z"}},
				{"account_id":"other","instrument_id":"inst","gateway_order_id":"venue-order-1","fill":{"fill_id":"fill-wrong-account","order_id":"15933226393","quantity":"1","price":"1","fee":"0","fee_asset":"USDT","time":"2026-07-29T01:02:03Z"}}
			]}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})
	adapter := NewGatewayAdapter(client)

	order, err := adapter.GetOrder(context.Background(), "acct", "exec-1:long")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if !order.Found || order.GatewayID != "exec-1:long" || order.OrderID != "venue-order-1" ||
		order.Status != execution.LegStatusPartial {
		t.Fatalf("order observation = %#v", order)
	}
	fills, err := adapter.Fills(context.Background(), "acct", "inst", "exec-1:long")
	if err != nil {
		t.Fatalf("Fills: %v", err)
	}
	if len(fills) != 1 || fills[0].FillID != "fill-good" || fills[0].GatewayFillID != "fill-good" ||
		!fills[0].Quantity.Equal(decimal.RequireFromString("0.3")) ||
		!fills[0].Price.Equal(decimal.RequireFromString("100.25")) ||
		fills[0].Fee.Currency != "USDT" {
		t.Fatalf("fills not isolated/mapped: %#v", fills)
	}
}

func TestGatewayAdapterFillsFailsClosedOnScopedWrapperIdentityMismatch(t *testing.T) {
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/orders":
			return gatewayJSON(`{"items":[{"command_id":"exec-1:long","gateway_order_id":"real-gateway-uuid","client_order_id":"exec-1:long","state":"filled"}]}`), nil
		case "/v1/fills":
			if r.URL.Query().Get("gateway_order_id") != "real-gateway-uuid" {
				t.Fatalf("fills query = %q", r.URL.RawQuery)
			}
			return gatewayJSON(`{"items":[{"account_id":"acct","instrument_id":"inst","gateway_order_id":"other-gateway-uuid","fill":{"fill_id":"poison","order_id":"15933226393","quantity":"9","price":"1"}}]}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})

	fills, err := NewGatewayAdapter(client).Fills(context.Background(), "acct", "inst", "exec-1:long")
	if err != nil {
		t.Fatalf("Fills: %v", err)
	}
	if len(fills) != 0 {
		t.Fatalf("mismatched wrapper fill was trusted: %#v", fills)
	}
}

func TestGatewayAdapterFillsFailsClosedWhenInstrumentScopedOrderResolutionMisses(t *testing.T) {
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/orders":
			if r.URL.Query().Get("instrument_id") == "" {
				return gatewayJSON(`{"items":[{"command_id":"exec-1:long","gateway_order_id":"venue-order-1","client_order_id":"exec-1:long","state":"working"}]}`), nil
			}
			return gatewayJSON(`{"items":[]}`), nil
		case "/v1/fills":
			t.Fatal("fills must not be queried without an instrument-scoped order proof")
			return nil, nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})
	adapter := NewGatewayAdapter(client)

	order, err := adapter.GetOrder(context.Background(), "acct", "exec-1:long")
	if err != nil || !order.Found {
		t.Fatalf("GetOrder = %#v, %v; want found", order, err)
	}
	if _, err := adapter.Fills(context.Background(), "acct", "inst", "exec-1:long"); !errors.Is(err, executionservice.ErrServiceUnavailable) {
		t.Fatalf("Fills error = %v, want service unavailable", err)
	}
}

func TestGatewayAdapterMapsOrderStatusesPrecisely(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  execution.LegStatus
		code  string
	}{
		{name: "filled", state: "filled", want: execution.LegStatusFilled},
		{name: "rejected", state: "rejected", want: execution.LegStatusRejected},
		{name: "uncertain", state: "uncertain", want: execution.LegStatusUncertain},
		{name: "empty is working", state: "", want: execution.LegStatusWorking},
		{name: "unknown non-empty is uncertain", state: "venue_new_state", want: execution.LegStatusUncertain, code: "GATEWAY_ORDER_STATE_UNKNOWN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v1/orders":
					return gatewayJSON(`{"items":[{"command_id":"exec-1:long","gateway_order_id":"venue-order-1","client_order_id":"exec-1:long","state":"` + tt.state + `"}]}`), nil
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
					return nil, nil
				}
			})
			got, err := NewGatewayAdapter(client).GetOrder(context.Background(), "acct", "exec-1:long")
			if err != nil {
				t.Fatalf("GetOrder: %v", err)
			}
			if got.Status != tt.want {
				t.Fatalf("status = %s, want %s", got.Status, tt.want)
			}
			if tt.code != "" && got.Error.Code != tt.code {
				t.Fatalf("error = %#v, want code %s", got.Error, tt.code)
			}
		})
	}
}

func TestGatewayAdapterFillsPaginatesAndFindsMatchOnSecondPage(t *testing.T) {
	var cursors []string
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/orders":
			return gatewayJSON(`{"items":[{"command_id":"exec-1:long","gateway_order_id":"venue-order-1","client_order_id":"exec-1:long","state":"filled"}]}`), nil
		case "/v1/fills":
			if r.URL.Query().Get("gateway_order_id") != "venue-order-1" {
				t.Fatalf("fills query = %q", r.URL.RawQuery)
			}
			cursors = append(cursors, r.URL.Query().Get("pagination.cursor"))
			if r.URL.Query().Get("pagination.cursor") == "" {
				return gatewayJSON(`{"items":[{"account_id":"acct","instrument_id":"inst","fill":{"fill_id":"other-page-1","order_id":"other","client_order_id":"other","quantity":"9","price":"1","fee":"0","fee_asset":"USDT","time":"2026-07-29T01:02:03Z"}}],"next_cursor":"page-2"}`), nil
			}
			return gatewayJSON(`{"items":[{"account_id":"acct","instrument_id":"inst","fill":{"fill_id":"fill-page-2","order_id":"venue-order-1","client_order_id":"exec-1:long","quantity":"0.7","price":"101","fee":"0.02","fee_asset":"USDT","time":"2026-07-29T01:02:04Z"}}]}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})

	fills, err := NewGatewayAdapter(client).Fills(context.Background(), "acct", "inst", "exec-1:long")
	if err != nil {
		t.Fatalf("Fills: %v", err)
	}
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "page-2" {
		t.Fatalf("pagination cursors = %#v", cursors)
	}
	if len(fills) != 1 || fills[0].FillID != "fill-page-2" || !fills[0].Quantity.Equal(decimal.RequireFromString("0.7")) {
		t.Fatalf("second page fill not mapped: %#v", fills)
	}
}

func TestGatewayAdapterFillsFailsClosedOnRepeatedCursor(t *testing.T) {
	t.Run("same cursor repeated", func(t *testing.T) {
		client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v1/orders":
				return gatewayJSON(`{"items":[{"command_id":"exec-1:long","gateway_order_id":"venue-order-1","client_order_id":"exec-1:long","state":"filled"}]}`), nil
			case "/v1/fills":
				cursor := r.URL.Query().Get("pagination.cursor")
				if cursor == "" {
					return gatewayJSON(`{"items":[],"next_cursor":"same"}`), nil
				}
				return gatewayJSON(`{"items":[],"next_cursor":"same"}`), nil
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
				return nil, nil
			}
		})

		_, err := NewGatewayAdapter(client).Fills(context.Background(), "acct", "inst", "exec-1:long")
		if !errors.Is(err, executionservice.ErrServiceUnavailable) {
			t.Fatalf("err = %v, want service unavailable", err)
		}
	})

	t.Run("cursor cycle", func(t *testing.T) {
		cursorResponses := map[string]string{
			"":       "page-2",
			"page-2": "page-3",
			"page-3": "page-2",
		}
		client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v1/orders":
				return gatewayJSON(`{"items":[{"command_id":"exec-1:long","gateway_order_id":"venue-order-1","client_order_id":"exec-1:long","state":"filled"}]}`), nil
			case "/v1/fills":
				cursor := r.URL.Query().Get("pagination.cursor")
				return gatewayJSON(`{"items":[],"next_cursor":"` + cursorResponses[cursor] + `"}`), nil
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
				return nil, nil
			}
		})

		_, err := NewGatewayAdapter(client).Fills(context.Background(), "acct", "inst", "exec-1:long")
		if !errors.Is(err, executionservice.ErrServiceUnavailable) {
			t.Fatalf("err = %v, want service unavailable", err)
		}
	})
}

func TestGatewayAdapterPositionsFailClosedOnIdentityMismatch(t *testing.T) {
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/positions":
			return gatewayJSON(`{"items":[{"account_id":"acct","instrument_id":"inst","position":{"quantity":"0.75"}}]}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})
	positions, err := NewGatewayAdapter(client).Positions(context.Background(), "acct", "inst")
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(positions) != 1 || !positions[0].Quantity.Equal(decimal.RequireFromString("0.75")) {
		t.Fatalf("positions not mapped: %#v", positions)
	}

	mismatchClient := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/positions":
			return gatewayJSON(`{"items":[{"account_id":"other","instrument_id":"inst","position":{"quantity":"0.75"}}]}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})
	_, err = NewGatewayAdapter(mismatchClient).Positions(context.Background(), "acct", "inst")
	if !errors.Is(err, executionservice.ErrServiceUnavailable) {
		t.Fatalf("mismatch err = %v, want service unavailable", err)
	}
}

func TestGatewayAdapterCancelResolvesCommandIDToOwnedGatewayOrderID(t *testing.T) {
	var canceledPath string
	client := gatewayClientFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return gatewayJSON(`{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}`), nil
		case "/v1/capabilities":
			return gatewayJSON(`{"items":[{"account_id":"acct","capability":{"operations":{"cancel":true}}}]}`), nil
		case "/v1/orders":
			return gatewayJSON(`{"items":[{"command_id":"exec-1:long","gateway_order_id":"venue-order-1","client_order_id":"exec-1:long","state":"open"}]}`), nil
		case "/v1/orders/venue-order-1":
			canceledPath = r.URL.Path
			if r.Method != http.MethodDelete {
				return gatewayJSONStatus(http.StatusMethodNotAllowed, `{"error":{"code":"bad_method"}}`), nil
			}
			return gatewayJSONStatus(http.StatusAccepted, `{"command_id":"exec-1:long","gateway_order_id":"venue-order-1","client_order_id":"exec-1:long","state":"canceled"}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})
	result, err := NewGatewayAdapter(client).CancelOrder(context.Background(), executionservice.GatewayCancelRequest{
		AccountID: "acct", GatewayID: "exec-1:long",
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if canceledPath != "/v1/orders/venue-order-1" || result.Outcome != executionservice.GatewayAccepted || result.OrderID != "venue-order-1" {
		t.Fatalf("cancel did not use real owned gateway id: path=%s result=%#v", canceledPath, result)
	}
}

type fakeExecutionStore struct {
	getIDErr    error
	persist     postgres.PersistTransitionInput
	events      postgres.PersistedTransitionEvents
	reservation postgres.ExecutionReservation
	idempotency postgres.IdempotencyRecord
	stored      postgres.StoredExecution
}

func (store *fakeExecutionStore) SavePreview(context.Context, execution.PreviewResult, string) error {
	return nil
}

func (store *fakeExecutionStore) GetPreview(context.Context, readmodel.Scope, string, time.Time) (postgres.StoredPreview, error) {
	return postgres.StoredPreview{}, nil
}

func (store *fakeExecutionStore) GetIdempotency(context.Context, readmodel.Scope, string, string, string) (postgres.IdempotencyRecord, error) {
	return store.idempotency, nil
}

func (store *fakeExecutionStore) ReserveExecution(context.Context, readmodel.Scope, string, string, string, string, execution.Execution) (postgres.ExecutionReservation, error) {
	return store.reservation, nil
}

func (store *fakeExecutionStore) CompleteIdempotency(context.Context, readmodel.Scope, string, string, string, int, json.RawMessage) error {
	return nil
}

func (store *fakeExecutionStore) CompleteLinkedIdempotency(context.Context, readmodel.Scope, string, int, json.RawMessage) error {
	return nil
}

func (store *fakeExecutionStore) GetExecution(context.Context, readmodel.Scope, string) (postgres.StoredExecution, error) {
	if store.getIDErr != nil {
		return postgres.StoredExecution{}, store.getIDErr
	}
	return store.stored, nil
}

func (store *fakeExecutionStore) ListNonTerminal(context.Context, readmodel.Scope) ([]postgres.StoredExecution, error) {
	return nil, nil
}

func (store *fakeExecutionStore) ListTerminalWithIncompleteIdempotency(context.Context, readmodel.Scope) ([]postgres.StoredExecution, error) {
	return nil, nil
}

func (store *fakeExecutionStore) PersistTransition(_ context.Context, input postgres.PersistTransitionInput) (postgres.PersistedTransitionEvents, error) {
	store.persist = input
	return store.events, nil
}

type memoryUIStore struct {
	appended bool
}

func (store *memoryUIStore) Latest(context.Context, string) (uint64, error) { return 0, nil }
func (store *memoryUIStore) Replay(context.Context, string, uint64, []string, int) ([]uistream.StoredEvent, error) {
	return nil, nil
}
func (store *memoryUIStore) Append(context.Context, uistream.EventInput) (uistream.StoredEvent, error) {
	store.appended = true
	return uistream.StoredEvent{}, errors.New("append should not be called")
}

type emptySnapshot struct{}

func (emptySnapshot) Snapshot(context.Context, uistream.Request) (uistream.Snapshot, error) {
	return uistream.Snapshot{Scope: "all", Data: json.RawMessage(`{}`)}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func gatewayClientFixture(t *testing.T, transport roundTripFunc) *boltertrader.GatewayClient {
	t.Helper()
	client, err := boltertrader.NewGatewayClient(boltertrader.GatewayClientOptions{
		BaseURL:    "http://gateway.local",
		Token:      "token",
		Mode:       "test",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	return client
}

func gatewayJSON(body string) *http.Response {
	return gatewayJSONStatus(http.StatusOK, body)
}

func gatewayJSONStatus(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       ioNopCloser{strings.NewReader(body)},
	}
}

type ioNopCloser struct {
	*strings.Reader
}

func (closer ioNopCloser) Close() error { return nil }

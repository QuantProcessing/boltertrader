package nado

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
)

func TestNadoSubmitNeverQueriesMaxOrderSize(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	client := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
	deps := &recordingSubmissionDeps{
		prepared: preparedOrderForTest(2, "1000000000000000000", "2000000000000000000", "digest-submit"),
		executed: &sdk.PlaceOrderResponse{Digest: "digest-submit"},
	}
	client.submitter = deps
	req := nadoTestOrderRequest(enums.KindPerp, enums.SideBuy)

	order, err := client.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if order.VenueOrderID != "digest-submit" || order.Request.ClientID != req.ClientID {
		t.Fatalf("order=%+v, want correlated digest and client id", order)
	}
	calls, prepareCalls, executeCalls := deps.snapshot()
	if want := []string{"prepare", "execute"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order=%v, want ordinary validate-then-submit path %v", calls, want)
	}
	if prepareCalls != 1 || executeCalls != 1 {
		t.Fatalf("prepare calls=%d execute calls=%d, want one each", prepareCalls, executeCalls)
	}
}

func TestNadoSubmitPreparesAndExecutesExactPayloadOnce(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 0, 30, 0, time.UTC))
	client := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	prepared := preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "digest-exact-payload")
	executedExact := false
	deps := &recordingSubmissionDeps{
		prepared: prepared,
		executed: &sdk.PlaceOrderResponse{Digest: prepared.Digest},
		onExecute: func(got *sdk.PreparedOrder) {
			if got != prepared || got.Signature != "sig-digest-exact-payload" || got.EncodedOrder != "encoded-digest-exact-payload" || got.Tx.ProductId != 1 {
				t.Fatalf("executed payload=%+v, want exact prepared object", got)
			}
			executedExact = true
		},
	}
	client.submitter = deps

	if _, err := client.Submit(context.Background(), nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	_, prepareCalls, executeCalls := deps.snapshot()
	if !executedExact || prepareCalls != 1 || executeCalls != 1 {
		t.Fatalf("executedExact=%v prepare=%d execute=%d, want true/1/1", executedExact, prepareCalls, executeCalls)
	}
	assertNadoPreparedMaterialRedacted(t, prepared)
}

func TestNadoSubmitRemembersDigestBeforeExecute(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 1, 0, 0, time.UTC))
	client := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	prepared := preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "digest-before-write")
	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	observed := false
	deps := &recordingSubmissionDeps{
		prepared: prepared,
		executed: &sdk.PlaceOrderResponse{Digest: prepared.Digest},
		onExecute: func(order *sdk.PreparedOrder) {
			correlation, ok := client.correlations.byClientID(AccountIDUnified, req.InstrumentID, req.ClientID, clk.Now())
			if !ok || correlation.venueOrderID != "digest-before-write" {
				t.Fatalf("execute observed correlation=%+v ok=%v, want signed digest before write", correlation, ok)
			}
			if order != prepared || order.Signature == "" || order.EncodedOrder == "" || order.Request == nil {
				t.Fatalf("execute received altered prepared payload: %+v", order)
			}
			observed = true
		},
	}
	client.submitter = deps

	order, err := client.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !observed || order.VenueOrderID != "digest-before-write" {
		t.Fatalf("observed=%v order=%+v", observed, order)
	}
	assertNadoPreparedMaterialRedacted(t, prepared)
}

func TestNadoPreparedMaterialRedactedOnEveryExit(t *testing.T) {
	prepareFailure := errors.New("prepare failed")
	executeFailure := errors.New("execute failed")
	tests := []struct {
		name        string
		configure   func(*executionClient, *recordingSubmissionDeps, model.OrderRequest) context.Context
		wantErr     bool
		wantExecute int
	}{
		{name: "success", configure: backgroundNadoSubmitContext, wantExecute: 1},
		{name: "prepare error with material", configure: func(_ *executionClient, deps *recordingSubmissionDeps, _ model.OrderRequest) context.Context {
			deps.prepareErr = prepareFailure
			return context.Background()
		}, wantErr: true},
		{name: "context canceled after prepare", configure: func(_ *executionClient, deps *recordingSubmissionDeps, _ model.OrderRequest) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			deps.onPrepare = cancel
			return ctx
		}, wantErr: true},
		{name: "missing digest", configure: func(_ *executionClient, deps *recordingSubmissionDeps, _ model.OrderRequest) context.Context {
			deps.prepared.Digest = ""
			return context.Background()
		}, wantErr: true},
		{name: "correlation failure", configure: func(client *executionClient, _ *recordingSubmissionDeps, req model.OrderRequest) context.Context {
			if err := client.correlations.remember(nadoOrderCorrelation{
				accountID: AccountIDUnified, instrumentID: req.InstrumentID, clientID: req.ClientID,
				venueOrderID: "existing-digest", request: req,
			}, client.clk.Now()); err != nil {
				t.Fatalf("seed correlation: %v", err)
			}
			return context.Background()
		}, wantErr: true},
		{name: "execute error", configure: func(_ *executionClient, deps *recordingSubmissionDeps, _ model.OrderRequest) context.Context {
			deps.executeErr = executeFailure
			return context.Background()
		}, wantErr: true, wantExecute: 1},
		{name: "response digest mismatch", configure: func(_ *executionClient, deps *recordingSubmissionDeps, _ model.OrderRequest) context.Context {
			deps.executed = &sdk.PlaceOrderResponse{Digest: "foreign-digest"}
			return context.Background()
		}, wantErr: true, wantExecute: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 2, 0, 0, time.UTC))
			client := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
			prepared := preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "digest-redact")
			deps := &recordingSubmissionDeps{
				prepared: prepared,
				executed: &sdk.PlaceOrderResponse{Digest: prepared.Digest},
			}
			client.submitter = deps
			req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
			ctx := tt.configure(client, deps, req)

			_, err := client.Submit(ctx, req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Submit err=%v wantErr=%v", err, tt.wantErr)
			}
			_, prepareCalls, executeCalls := deps.snapshot()
			if prepareCalls != 1 || executeCalls != tt.wantExecute {
				t.Fatalf("prepare calls=%d execute calls=%d, want 1/%d", prepareCalls, executeCalls, tt.wantExecute)
			}
			assertNadoPreparedMaterialRedacted(t, prepared)
		})
	}
}

func TestNadoDirectConcurrentDuplicateClientIDDoesNotDoubleExecute(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 3, 0, 0, time.UTC))
	client := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	deps := &recordingSubmissionDeps{
		prepareFn: func(call int, _ sdk.ClientOrderInput) (*sdk.PreparedOrder, error) {
			return preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", fmt.Sprintf("digest-%d", call)), nil
		},
		executeFn: func(order *sdk.PreparedOrder) (*sdk.PlaceOrderResponse, error) {
			return &sdk.PlaceOrderResponse{Digest: order.Digest}, nil
		},
	}
	client.submitter = deps
	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, errs[index] = client.Submit(context.Background(), req)
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	_, prepareCalls, executeCalls := deps.snapshot()
	if successes != 1 || prepareCalls != 2 || executeCalls != 1 {
		t.Fatalf("successes=%d prepare calls=%d execute calls=%d errs=%v, want one venue write", successes, prepareCalls, executeCalls, errs)
	}
}

func TestNadoSubmitRejectsResponseDigestMismatchAsPostHandoffAmbiguous(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 4, 0, 0, time.UTC))
	client := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	prepared := preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "signed-digest")
	deps := &recordingSubmissionDeps{
		prepared: prepared,
		executed: &sdk.PlaceOrderResponse{Digest: "foreign-digest"},
	}
	client.submitter = deps
	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)

	order, err := client.Submit(context.Background(), req)
	if order != nil || err == nil || errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("Submit order=%+v err=%v, want non-definitive post-handoff error", order, err)
	}
	correlation, ok := client.correlations.byClientID(AccountIDUnified, req.InstrumentID, req.ClientID, clk.Now())
	if !ok || correlation.venueOrderID != "signed-digest" {
		t.Fatalf("correlation=%+v ok=%v, want retained signed digest", correlation, ok)
	}
	_, prepareCalls, executeCalls := deps.snapshot()
	if prepareCalls != 1 || executeCalls != 1 {
		t.Fatalf("prepare=%d execute=%d, want 1/1", prepareCalls, executeCalls)
	}
	assertNadoPreparedMaterialRedacted(t, prepared)
}

func TestNadoSubmitRequiresExactNonblankResponseDigest(t *testing.T) {
	tests := []struct {
		name           string
		response       *sdk.PlaceOrderResponse
		preparedDigest string
		wantErr        bool
	}{
		{name: "nil response", preparedDigest: "signed-digest", wantErr: true},
		{name: "empty response digest", preparedDigest: "signed-digest", response: &sdk.PlaceOrderResponse{}, wantErr: true},
		{name: "blank response digest", preparedDigest: "signed-digest", response: &sdk.PlaceOrderResponse{Digest: "  \t"}, wantErr: true},
		{name: "padded matching response digest", preparedDigest: "signed-digest", response: &sdk.PlaceOrderResponse{Digest: " signed-digest "}, wantErr: true},
		{name: "case-insensitive exact digest", preparedDigest: "0xAbCd", response: &sdk.PlaceOrderResponse{Digest: "0XaBcD"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 4, 15, 0, time.UTC))
			client := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
			prepared := preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", test.preparedDigest)
			client.submitter = &recordingSubmissionDeps{prepared: prepared, executed: test.response}
			req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)

			order, err := client.Submit(context.Background(), req)
			if (err != nil) != test.wantErr {
				t.Fatalf("Submit order=%+v err=%v, wantErr=%v", order, err, test.wantErr)
			}
			if test.wantErr {
				if order != nil || errors.Is(err, contract.ErrVenueRejected) {
					t.Fatalf("order=%+v err=%v, want ambiguous post-handoff error", order, err)
				}
				correlation, ok := client.correlations.byClientID(AccountIDUnified, req.InstrumentID, req.ClientID, clk.Now())
				if !ok || correlation.venueOrderID != test.preparedDigest {
					t.Fatalf("correlation=%+v ok=%v, want retained signed digest", correlation, ok)
				}
				return
			}
			if order == nil || !strings.EqualFold(order.VenueOrderID, test.preparedDigest) {
				t.Fatalf("order=%+v, want exact signed digest", order)
			}
		})
	}
}

func TestNadoSubmitMapsTypedExecutionRejectionOnly(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 4, 30, 0, time.UTC))
	client := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	prepared := preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "signed-digest")
	deps := &recordingSubmissionDeps{
		prepared:   prepared,
		executeErr: sdk.NewGatewayApplicationError(2001, "product is not active", "place_order"),
	}
	client.submitter = deps
	order, err := client.Submit(context.Background(), nadoTestOrderRequest(enums.KindSpot, enums.SideBuy))
	if order != nil || !errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("Submit order=%+v err=%v, want definitive venue rejection", order, err)
	}
}

func TestNadoSubmitNeverLogsSignedMaterial(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 5, 0, 0, time.UTC))
	client := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	prepared := preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "public-digest")
	prepared.Signature = "secret-signature-material"
	prepared.EncodedOrder = "secret-encoded-order"
	prepared.Request = map[string]interface{}{"secret_request": "secret-request-body"}
	deps := &recordingSubmissionDeps{prepared: prepared, executeErr: errors.New("venue write failed")}
	client.submitter = deps

	_, err := client.Submit(context.Background(), nadoTestOrderRequest(enums.KindSpot, enums.SideBuy))
	if err == nil {
		t.Fatal("Submit succeeded, want execute error")
	}
	diagnostic := err.Error()
	for _, secret := range []string{"secret-signature-material", "secret-encoded-order", "secret-request-body"} {
		if strings.Contains(diagnostic, secret) {
			t.Fatalf("diagnostic leaked %q: %s", secret, diagnostic)
		}
	}
	assertNadoPreparedMaterialRedacted(t, prepared)
}

func backgroundNadoSubmitContext(_ *executionClient, _ *recordingSubmissionDeps, _ model.OrderRequest) context.Context {
	return context.Background()
}

func assertNadoPreparedMaterialRedacted(t *testing.T, prepared *sdk.PreparedOrder) {
	t.Helper()
	if prepared == nil {
		t.Fatal("prepared order is nil")
	}
	if prepared.Signature != "" || prepared.Digest != "" || prepared.EncodedOrder != "" || prepared.Request != nil || !reflect.DeepEqual(prepared.Tx, sdk.TxOrder{}) {
		t.Fatalf("prepared material was not redacted: %+v", prepared)
	}
}

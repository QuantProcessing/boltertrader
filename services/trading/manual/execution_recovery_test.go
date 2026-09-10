package manual

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

func TestExecutionPersistsRecoveryBeforeNetworkAndConcurrentReplayDoesNotSend(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	gateway := gatewayFunc(func(context.Context, GatewayRequest) (GatewayResult, error) {
		calls.Add(1)
		close(entered)
		<-release
		return GatewayResult{Handoff: orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}}, errors.New("response lost")
	})
	service, commands, _, _ := newFixture(gateway)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)
	done := make(chan error, 1)
	go func() { _, err := service.Execute(context.Background(), command); done <- err }()
	defer func() { close(release); <-done }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not reach Gateway")
	}
	stored, err := commands.Get(context.Background(), command.CommandID)
	if err != nil || stored.State != orders.StateUncertain {
		t.Fatalf("no durable marker before network: %+v err=%v", stored, err)
	}
	tasks, err := commands.ClaimDueReconciliation(context.Background(), fixtureTime.Add(time.Minute), time.Minute, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Reason != orders.ReconciliationAmbiguousHandoff {
		t.Fatalf("recovery=%+v err=%v", tasks, err)
	}
	replay, err := service.Execute(context.Background(), command)
	if err != nil || replay.State != orders.StateUncertain || calls.Load() != 1 {
		t.Fatalf("replay=%+v err=%v sends=%d", replay, err, calls.Load())
	}
}

func TestReconciliationRefusesCommandsOwnedByAnotherProduct(t *testing.T) {
	for _, product := range []string{"arbitrage", "custom"} {
		for _, outcome := range []orders.HandoffOutcome{orders.HandoffAccepted, orders.HandoffRejected} {
			t.Run(product+"/"+string(outcome), func(t *testing.T) {
				service, commands, _, _ := newFixture(&gatewayFixture{})
				command, _, err := commands.Create(context.Background(), orders.CreateRequest{
					PrincipalID: "principal-a", AccountID: "account-a", ProductID: product, LineageID: "lineage-a",
					IdempotencyKey: "foreign", Method: "POST", Path: "/commands", CommandKind: product + ".place", Payload: []byte(`{}`),
				})
				if err != nil {
					t.Fatal(err)
				}
				command = submittedCommand(t, commands, command)
				// Reload persisted ownership rather than trusting the caller's copy.
				command.ProductID, command.CommandKind = "manual", "manual.place"
				_, err = service.RecordReconciliation(context.Background(), command, orders.HandoffResult{Outcome: outcome})
				if err == nil {
					t.Fatal("manual reconciliation accepted a foreign command")
				}
				stored, err := commands.Get(context.Background(), command.CommandID)
				if err != nil || stored.State != orders.StateSubmitted || stored.ProductID != product {
					t.Fatalf("foreign command changed: %+v err=%v", stored, err)
				}
			})
		}
	}
}

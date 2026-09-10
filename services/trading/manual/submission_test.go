package manual

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

type accountReaderFunc func(context.Context, string, string) (Account, error)

func (f accountReaderFunc) Get(ctx context.Context, principal, account string) (Account, error) {
	return f(ctx, principal, account)
}

type catalogReaderFunc func(context.Context) (catalog.Snapshot, error)

func (f catalogReaderFunc) Snapshot(ctx context.Context) (catalog.Snapshot, error) { return f(ctx) }

func submissionRequest() CommandRequest {
	return CommandRequest{Action: "place", LineageID: "lineage-a", IdempotencyKey: "submit-1", Order: &OrderRequest{
		AccountID: "account-a", InstrumentID: "BTC-USDT", Side: "buy", OrderType: "limit", Quantity: "2", LimitPrice: "100", TimeInForce: "gtc", Leverage: 1,
	}}
}
func submissionCatalog(context.Context) (catalog.Snapshot, error) {
	return catalog.Snapshot{Venues: []catalog.Venue{{Venue: "binance", Environment: "live", Ready: true}}, Instruments: []catalog.Instrument{{InstrumentID: "BTC-USDT", Venue: "binance", Environment: "live", Product: "perp", Settlement: "USDT", Ready: true, Rules: catalog.TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}}}}, nil
}
func submissionService(store orders.Store, gateway Gateway) *Service {
	now := func() time.Time { return fixtureTime }
	commands := orders.NewService(store, now)
	commands.SetLiveWritesEnabled(true)
	service := NewService(Options{Commands: commands, Portfolio: portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), now), Gateway: gateway, Now: now,
		Accounts: accountReaderFunc(func(context.Context, string, string) (Account, error) { return readyAccount(), nil }), Catalog: catalogReaderFunc(submissionCatalog)})
	_ = service.SetExecutionAdmission(true)
	return service
}
func submittedGateway(request GatewayRequest) GatewayResult {
	return GatewayResult{CommandID: request.CommandID, AccountID: request.AccountID, State: GatewayStateSubmitted, Handoff: authoritativeHandoff("submitted", 1, nil)}
}

func TestSubmissionReplaySurvivesUnavailableDependenciesAndDisabledWrites(t *testing.T) {
	store := orders.NewMemoryStore()
	var calls atomic.Int32
	service := submissionService(store, gatewayFunc(func(_ context.Context, req GatewayRequest) (GatewayResult, error) {
		calls.Add(1)
		return submittedGateway(req), nil
	}))
	first, err := service.Submit(context.Background(), "principal-a", submissionRequest())
	if err != nil || !first.Changed || first.Command.State != orders.StateSubmitted || first.Command.ReservationID != nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	// A new process can answer from storage even without account/catalog/execution dependencies.
	restarted := NewService(Options{Commands: orders.NewService(store, time.Now)})
	replay, err := restarted.Submit(context.Background(), "principal-a", submissionRequest())
	if err != nil || replay.Changed || replay.Command.CommandID != first.Command.CommandID || calls.Load() != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, calls.Load(), err)
	}
	// The namespace includes the authenticated principal, so another user cannot discover it.
	if _, err := restarted.Submit(context.Background(), "principal-b", submissionRequest()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cross-principal lookup=%v", err)
	}
}

func TestSubmissionReplayChecksAllUserIntentWithoutCurrentAccount(t *testing.T) {
	service := submissionService(orders.NewMemoryStore(), gatewayFunc(func(_ context.Context, r GatewayRequest) (GatewayResult, error) { return submittedGateway(r), nil }))
	if _, err := service.Submit(context.Background(), "principal-a", submissionRequest()); err != nil {
		t.Fatal(err)
	}
	service.accounts = nil
	service.catalog = nil
	cases := map[string]func(*CommandRequest){
		"account":       func(r *CommandRequest) { r.Order.AccountID = "account-b" },
		"lineage":       func(r *CommandRequest) { r.LineageID = "lineage-b" },
		"instrument":    func(r *CommandRequest) { r.Order.InstrumentID = "ETH-USDT" },
		"side":          func(r *CommandRequest) { r.Order.Side = "sell" },
		"quantity":      func(r *CommandRequest) { r.Order.Quantity = "3" },
		"type":          func(r *CommandRequest) { r.Order.OrderType = "market"; r.Order.LimitPrice = "" },
		"price":         func(r *CommandRequest) { r.Order.LimitPrice = "101" },
		"time in force": func(r *CommandRequest) { r.Order.TimeInForce = "ioc" },
		"reduce only":   func(r *CommandRequest) { r.Order.ReduceOnly = true },
		"leverage":      func(r *CommandRequest) { r.Order.Leverage = 2 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := submissionRequest()
			change(&r)
			if _, err := service.Submit(context.Background(), "principal-a", r); !errors.Is(err, orders.ErrIdempotencyConflict) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestSubmissionReplaysLegacyAcceptedCommandWithoutSending(t *testing.T) {
	store := orders.NewMemoryStore()
	service := submissionService(store, gatewayFunc(func(context.Context, GatewayRequest) (GatewayResult, error) {
		t.Fatal("replay sent an accepted command")
		return GatewayResult{}, nil
	}))
	request := submissionRequest()
	payload, err := json.Marshal(placeIntent{InstrumentID: request.Order.InstrumentID, Side: request.Order.Side, Quantity: request.Order.Quantity, OrderType: request.Order.OrderType, LimitPrice: request.Order.LimitPrice, TimeInForce: request.Order.TimeInForce, Leverage: request.Order.Leverage, RouteID: "old-route", RuntimeID: "old-runtime"})
	if err != nil {
		t.Fatal(err)
	}
	historicalReservation := "historical-reservation"
	legacy := orders.Command{CommandID: "legacy", PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: request.LineageID, IdempotencyKey: request.IdempotencyKey, Method: "POST", Path: CommandPath, CommandKind: "manual.place", State: orders.StateAccepted, Payload: payload, ReservationID: &historicalReservation, CreatedAt: fixtureTime, UpdatedAt: fixtureTime}
	if _, _, err := store.Create(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	replay, err := service.Submit(context.Background(), "principal-a", request)
	if err != nil || replay.Changed || replay.Command.State != orders.StateAccepted || replay.Command.CommandID != "legacy" {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	tasks, err := service.commands.ClaimDueReconciliation(context.Background(), fixtureTime.Add(time.Minute), time.Minute, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Reason != orders.ReconciliationAwaitingHandoff {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}

func TestConcurrentSubmissionPreservesWinnerRuntimeAndHasOneSender(t *testing.T) {
	store := orders.NewMemoryStore()
	var sends atomic.Int32
	gateway := gatewayFunc(func(_ context.Context, r GatewayRequest) (GatewayResult, error) {
		sends.Add(1)
		return submittedGateway(r), nil
	})
	reached := make(chan struct{}, 2)
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	services := []*Service{submissionService(store, gateway), submissionService(store, gateway)}
	for i, service := range services {
		runtime := []string{"runtime-1", "runtime-2"}[i]
		service.accounts = accountReaderFunc(func(ctx context.Context, _, _ string) (Account, error) {
			reached <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return Account{}, ctx.Err()
			}
			account := readyAccount()
			account.RuntimeID = runtime
			return account, nil
		})
	}
	type result struct {
		sub Submission
		err error
	}
	results := make(chan result, 2)
	for _, service := range services {
		go func() {
			sub, err := service.Submit(ctx, "principal-a", submissionRequest())
			results <- result{sub, err}
		}()
	}
	for range 2 {
		select {
		case <-reached:
		case <-ctx.Done():
			t.Fatal("both contenders did not reach account lookup")
		}
	}
	close(release)
	var id string
	winners := 0
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if id != "" && id != r.sub.Command.CommandID {
			t.Fatal("duplicate commands")
		}
		id = r.sub.Command.CommandID
		if r.sub.Changed {
			winners++
		}
	}
	if winners != 1 || sends.Load() != 1 {
		t.Fatalf("winners=%d sends=%d", winners, sends.Load())
	}
	command, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := decodePlaceIntent(command.Payload)
	if err != nil || (intent.RuntimeID != "runtime-1" && intent.RuntimeID != "runtime-2") {
		t.Fatalf("winner intent=%+v err=%v", intent, err)
	}
	// A second deliberate order on the same account/instrument is independent.
	next := submissionRequest()
	next.IdempotencyKey = "submit-2"
	service := submissionService(store, gateway)
	if r, err := service.Submit(ctx, "principal-a", next); err != nil || !r.Changed || r.Command.CommandID == id {
		t.Fatalf("next=%+v err=%v", r, err)
	}
	if sends.Load() != 2 {
		t.Fatalf("sends=%d", sends.Load())
	}
}

type unavailableLookupStore struct{ orders.Store }

func (unavailableLookupStore) Find(context.Context, orders.IdempotencyIdentity) (orders.Command, error) {
	return orders.Command{}, errors.New("database unavailable")
}
func TestSubmissionLookupFailureDoesNotCreateOrSend(t *testing.T) {
	store := orders.NewMemoryStore()
	service := submissionService(unavailableLookupStore{store}, gatewayFunc(func(context.Context, GatewayRequest) (GatewayResult, error) {
		t.Fatal("send after failed lookup")
		return GatewayResult{}, nil
	}))
	if _, err := service.Submit(context.Background(), "principal-a", submissionRequest()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
	commands, err := store.ListOwned(context.Background(), "principal-a", "manual")
	if err != nil || len(commands) != 0 {
		t.Fatalf("commands=%+v err=%v", commands, err)
	}
}

func TestSubmissionLostResponseStaysRecoverableAndReplayNeverResends(t *testing.T) {
	store := orders.NewMemoryStore()
	var sends int
	service := submissionService(store, gatewayFunc(func(context.Context, GatewayRequest) (GatewayResult, error) {
		sends++
		return GatewayResult{}, errors.New("response lost")
	}))
	if _, err := service.Submit(context.Background(), "principal-a", submissionRequest()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
	replay, err := service.Submit(context.Background(), "principal-a", submissionRequest())
	if err != nil || replay.Changed || replay.Command.State != orders.StateUncertain || sends != 1 {
		t.Fatalf("replay=%+v sends=%d err=%v", replay, sends, err)
	}
	tasks, err := service.commands.ClaimDueReconciliation(context.Background(), fixtureTime.Add(time.Minute), time.Minute, 10)
	if err != nil || len(tasks) != 1 || tasks[0].CommandID != replay.Command.CommandID || tasks[0].Reason != orders.ReconciliationAmbiguousHandoff {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}

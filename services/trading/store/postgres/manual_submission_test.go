package postgres

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

type submissionAccounts func(context.Context, string, string) (manual.Account, error)

func (f submissionAccounts) Get(ctx context.Context, p, a string) (manual.Account, error) {
	return f(ctx, p, a)
}

type submissionCatalog struct{}

func (submissionCatalog) Snapshot(context.Context) (catalog.Snapshot, error) {
	return catalog.Snapshot{
		Venues:      []catalog.Venue{{Venue: "binance", Environment: "live", Ready: true}},
		Instruments: []catalog.Instrument{{InstrumentID: "BTC-USDT", Venue: "binance", Environment: "live", Product: "perp", Settlement: "USDT", Ready: true, Rules: catalog.TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}}},
	}, nil
}

type submissionGateway func(context.Context, manual.GatewayRequest) (manual.GatewayResult, error)

func (f submissionGateway) Execute(ctx context.Context, r manual.GatewayRequest) (manual.GatewayResult, error) {
	return f(ctx, r)
}

func TestPostgresManualSubmissionConcurrentReplayAndRestart(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	// The historical table can reject every new row without affecting submission.
	if _, err := database.Exec(`ALTER TABLE reservations ADD CHECK (false)`); err != nil {
		t.Fatal(err)
	}
	second := reopen()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reached := make(chan struct{}, 2)
	release := make(chan struct{})
	var sends atomic.Int32
	gateway := submissionGateway(func(_ context.Context, r manual.GatewayRequest) (manual.GatewayResult, error) {
		sends.Add(1)
		return manual.GatewayResult{CommandID: r.CommandID, AccountID: r.AccountID, State: manual.GatewayStateSubmitted, Handoff: orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: orders.ExecutionEvidence{
			CommandID: "gateway-c", GatewayOrderID: "gateway-o", ClientOrderID: "client-o", State: "submitted", ResourceVersion: 1,
			OrderEvents: []orders.OrderEventEvidence{{EventID: "gateway-o:1", State: "submitted", ResourceVersion: 1, OccurredAt: time.Now().UTC()}},
		}}}, nil
	})
	services := make([]*manual.Service, 2)
	for i, store := range []*CommandStore{NewCommandStore(database), NewCommandStore(second)} {
		runtime := []string{"runtime-first", "runtime-second"}[i]
		commands := orders.NewService(store, time.Now)
		commands.SetLiveWritesEnabled(true)
		service := manual.NewService(manual.Options{Commands: commands, Portfolio: portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now), Gateway: gateway, Catalog: submissionCatalog{}, Accounts: submissionAccounts(func(ctx context.Context, _, _ string) (manual.Account, error) {
			reached <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return manual.Account{}, ctx.Err()
			}
			return manual.Account{ID: "a", Venue: "binance", VenueEnvironment: "live", RuntimeID: runtime, State: "active", ProductIDs: []string{"manual"}, Routes: []manual.AccountRoute{{RouteID: "perp", Product: "perp"}}}, nil
		})})
		if err := service.SetExecutionAdmission(true); err != nil {
			t.Fatal(err)
		}
		services[i] = service
	}
	request := manual.CommandRequest{Action: "place", LineageID: "l", IdempotencyKey: "k", Order: &manual.OrderRequest{AccountID: "a", InstrumentID: "BTC-USDT", Side: "buy", OrderType: "limit", Quantity: "1", LimitPrice: "100", TimeInForce: "gtc"}}
	type result struct {
		sub manual.Submission
		err error
	}
	results := make(chan result, 2)
	for _, service := range services {
		go func() { sub, err := service.Submit(ctx, "p", request); results <- result{sub, err} }()
	}
	for range 2 {
		select {
		case <-reached:
		case <-ctx.Done():
			t.Fatal("both instances did not reach lookup")
		}
	}
	close(release)
	var commandID string
	winners := 0
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if commandID != "" && commandID != r.sub.Command.CommandID {
			t.Fatal("duplicate command identity")
		}
		commandID = r.sub.Command.CommandID
		if r.sub.Changed {
			winners++
		}
	}
	if sends.Load() != 1 || winners != 1 {
		t.Fatalf("sends=%d winners=%d", sends.Load(), winners)
	}
	for _, table := range []string{"commands", "reconciliation_tasks", "reservations"} {
		var count int
		want := 1
		if table == "reservations" {
			want = 0
		}
		if err := database.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := manual.NewService(manual.Options{Commands: orders.NewService(NewCommandStore(reopen()), time.Now)})
	replay, err := restarted.Submit(ctx, "p", request)
	if err != nil || replay.Changed || replay.Command.CommandID != commandID || replay.Command.State != orders.StateSubmitted || sends.Load() != 1 {
		t.Fatalf("restart=%+v err=%v sends=%d", replay, err, sends.Load())
	}
	request.Order.Quantity = "2"
	if _, err := restarted.Submit(ctx, "p", request); !errors.Is(err, orders.ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay err=%v", err)
	}
}

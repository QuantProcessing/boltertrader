package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/arbitrage"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/QuantProcessing/boltertrader/services/trading/reservations"
)

/* --- Fixtures ------------------------------------------------------------- */

// arbitrageCatalogSource publishes the smallest catalog a cross-venue hedge can
// exist in: one underlying quoted as a perp on two venues. Both instruments
// share BaseAsset/QuoteAsset because resolveAuthority refuses a pair whose legs
// do not name the same underlying.
type arbitrageCatalogSource struct{ revision string }

func (source arbitrageCatalogSource) Fetch(context.Context) (catalog.Snapshot, error) {
	revision := source.revision
	if revision == "" {
		revision = "arbitrage-test"
	}
	rules := catalog.TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}
	return catalog.Snapshot{
		Revision:   revision,
		ObservedAt: time.Now(),
		Venues: []catalog.Venue{
			{Venue: "binance", Environment: "demo", Ready: true},
			{Venue: "okx", Environment: "demo", Ready: true},
		},
		Instruments: []catalog.Instrument{
			{InstrumentID: "BTC-USDT-BINANCE", Venue: "binance", Environment: "demo", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
			{InstrumentID: "BTC-USDT-OKX", Venue: "okx", Environment: "demo", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
		},
	}, nil
}

// arbitrageAccounts answers only for accounts it knows. staticManualAccounts
// returns the same account for every id, which would let a hedge resolve two
// legs against one account — exactly what the product refuses.
type arbitrageAccounts struct{ accounts map[string]ManualAccount }

func (authority arbitrageAccounts) Get(_ context.Context, _, accountID string) (ManualAccount, error) {
	account, known := authority.accounts[accountID]
	if !known {
		return ManualAccount{}, fmt.Errorf("account %q is unknown", accountID)
	}
	return account, nil
}

func readyArbitrageAccounts() arbitrageAccounts {
	account := func(id, venue string) ManualAccount {
		return ManualAccount{
			ID: id, Venue: venue, VenueEnvironment: "demo",
			ProductIDs:           []string{arbitrage.ProductID},
			DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1,
			State:  manualAccountActive,
			Routes: []ManualAccountRoute{{RouteID: "perp", Product: "perp", RequestedState: manualAccountActive}},
		}
	}
	return arbitrageAccounts{accounts: map[string]ManualAccount{
		"account-long":  account("account-long", "binance"),
		"account-short": account("account-short", "okx"),
	}}
}

// arbitrageBoardFixture is the funding matrix the preview prices against. The
// short leg pays more than the long leg pays out, which is the whole point of
// the hedge and keeps the preview off its negative_spread warning.
type arbitrageBoardFixture struct{}

func (arbitrageBoardFixture) Get(context.Context) (marketdata.BoardSnapshot, error) {
	row := func(rate string) marketdata.BoardRow {
		return marketdata.BoardRow{
			Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			Rate: rate, IntervalHours: 8, MarkPrice: "50",
			NextFundingTime: time.Date(2026, time.August, 5, 8, 0, 0, 0, time.UTC),
		}
	}
	return marketdata.BoardSnapshot{
		Mode:       "test",
		ObservedAt: time.Now(),
		Venues: []marketdata.BoardVenue{
			{Venue: "binance", Environment: "demo", Product: "perp", RouteProfileID: "binance-perp", Rows: []marketdata.BoardRow{row("-0.0001")}},
			{Venue: "okx", Environment: "demo", Product: "perp", RouteProfileID: "okx-perp", Rows: []marketdata.BoardRow{row("0.0003")}},
		},
	}, nil
}

// arbitrageGatewayFixture mints a distinct fill per call.
//
// manualGatewayFixture cannot be reused here: it returns one constant FillID for
// every order, and portfolio.ApplyFill dedups on (principal, fillID) while
// comparing fingerprints — so the second leg's fill would collide with the
// first's and land the pair in uncertain rather than open.
type arbitrageGatewayFixture struct {
	calls int
	// failCall is the 1-based call that fails terminally. HandoffNotAttempted is
	// what makes it terminal: an empty outcome is an ambiguous handoff, which
	// leaves the command uncertain and the pair still opening.
	failCall int
	requests []manual.GatewayRequest
}

func (fixture *arbitrageGatewayFixture) Execute(_ context.Context, request manual.GatewayRequest) (manual.GatewayResult, error) {
	fixture.calls++
	fixture.requests = append(fixture.requests, request)
	if fixture.calls == fixture.failCall {
		return manual.GatewayResult{Handoff: orders.HandoffResult{Outcome: orders.HandoffNotAttempted}}, errors.New("venue refused the handoff")
	}
	return manual.GatewayResult{
		CommandID: request.CommandID, AccountID: request.AccountID, Mode: request.Mode,
		State: manual.GatewayStateFilled,
		Fills: []manual.FillFact{{
			FillID: fmt.Sprintf("arbitrage-fill-%d", fixture.calls), CommandID: request.CommandID,
			AccountID: request.AccountID, InstrumentID: request.InstrumentID, Mode: request.Mode,
			Side: portfolio.Side(request.Side), Quantity: request.Quantity,
			Price: "50", Fee: "0.05", FeeCurrency: "USDT", SettlementCurrency: "USDT",
			OccurredAt: time.Date(2026, time.August, 5, 1, 2, 3, 0, time.UTC),
		}},
	}, nil
}

type arbitrageHarness struct {
	mux        *http.ServeMux
	commands   *orders.Service
	portfolio  *portfolio.Projection
	gateway    *arbitrageGatewayFixture
	previewKey []byte
}

func newArbitrageHarness(t *testing.T, gateway *arbitrageGatewayFixture) arbitrageHarness {
	t.Helper()
	reservationService := reservations.NewService(reservations.NewMemoryStore(), time.Now)
	commandService := orders.NewService(orders.NewMemoryStore(), manualReservationGate{service: reservationService}, time.Now)
	portfolioProjection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), time.Now)
	previewKey := []byte("shared-preview-signing-key-with-32-bytes")
	projection, err := catalog.NewProjection(arbitrageCatalogSource{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	err = RegisterTradingRoutes(mux, TradingOptions{
		Reservations:      reservationService,
		Commands:          commandService,
		Portfolio:         portfolioProjection,
		Ledger:            ledgerService,
		Catalog:           projection,
		Arbitrage:         arbitrage.NewGatewayServiceWithPreviewKey(commandService, portfolioProjection, ledgerService, gateway, time.Now, previewKey),
		FundingBoard:      arbitrageBoardFixture{},
		ManualAccounts:    readyArbitrageAccounts(),
		ManualMarketData:  &manualMarketDataFixture{ask: "50"},
		Principal:         func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return arbitrageHarness{mux: mux, commands: commandService, portfolio: portfolioProjection, gateway: gateway, previewKey: previewKey}
}

const arbitragePreviewBody = `{"long":{"accountId":"account-long","instrumentId":"BTC-USDT-BINANCE"},` +
	`"short":{"accountId":"account-short","instrumentId":"BTC-USDT-OKX"},` +
	`"quantity":"2","orderType":"market","timeInForce":"ioc","mode":"test"}`

// preview runs the preview route and returns the sealed token the open route
// spends. Quantity 2 at the fixture's 50 prices each leg at exactly the 100
// test-mode notional ceiling, which manualTestNotionalAllowed admits.
func (harness arbitrageHarness) preview(t *testing.T) string {
	t.Helper()
	response := httptest.NewRecorder()
	harness.mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/products/arbitrage/execution-previews", strings.NewReader(arbitragePreviewBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data arbitragePreview `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.PreviewID == "" {
		t.Fatalf("preview carried no seal: %s", response.Body.String())
	}
	return envelope.Data.PreviewID
}

func (harness arbitrageHarness) open(t *testing.T, previewID, lineageID, idempotencyKey string) (int, arbitrageExecution) {
	t.Helper()
	body := fmt.Sprintf(`{"previewId":%q,"action":"open","lineageId":%q,"idempotencyKey":%q}`, previewID, lineageID, idempotencyKey)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/arbitrage/commands", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response := httptest.NewRecorder()
	harness.mux.ServeHTTP(response, request)
	var envelope struct {
		Data arbitrageExecution `json:"data"`
	}
	if response.Code == http.StatusAccepted || response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode open response: %v body=%s", err, response.Body.String())
		}
	}
	return response.Code, envelope.Data
}

/* --- Tests ---------------------------------------------------------------- */

// TestArbitrageOpenEmitsTwoCommandsSharingLineage pins the shape a hedge takes
// in the command ledger: two commands, two venues, one lineage. The lineage is
// what every later read joins on — positions, the funding roll-up, and the close
// path all find the pair by it — so a hedge whose legs disagree about it is a
// hedge that can never be closed as one.
func TestArbitrageOpenEmitsTwoCommandsSharingLineage(t *testing.T) {
	harness := newArbitrageHarness(t, &arbitrageGatewayFixture{})
	previewID := harness.preview(t)

	status, execution := harness.open(t, previewID, "lineage-a", "idem-a")
	if status != http.StatusAccepted {
		t.Fatalf("open status=%d execution=%+v", status, execution)
	}
	if execution.State != arbitrage.PairStateOpen || len(execution.Legs) != 2 || len(execution.Failures) != 0 {
		t.Fatalf("execution=%+v", execution)
	}
	if execution.Legs[0].Leg != arbitrage.LegLong || execution.Legs[1].Leg != arbitrage.LegShort ||
		execution.Legs[0].Side != "buy" || execution.Legs[1].Side != "sell" {
		t.Fatalf("legs are not a hedge: %+v", execution.Legs)
	}

	commands, err := harness.commands.ListOwned(context.Background(), "principal-a", orders.ModeTest, arbitrage.ProductID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 {
		t.Fatalf("expected two commands, got %+v", commands)
	}
	keys := map[string]orders.Command{}
	for _, command := range commands {
		if command.LineageID != "lineage-a" {
			t.Fatalf("leg %q left the lineage: %+v", command.CommandID, command)
		}
		if command.State != orders.StateFilled {
			t.Fatalf("leg %q state=%s", command.CommandID, command.State)
		}
		keys[command.IdempotencyKey] = command
	}
	long, hasLong := keys["idem-a:long"]
	short, hasShort := keys["idem-a:short"]
	if !hasLong || !hasShort || long.AccountID != "account-long" || short.AccountID != "account-short" {
		t.Fatalf("legs are not keyed per side: %+v", keys)
	}
	if harness.gateway.calls != 2 {
		t.Fatalf("gateway calls=%d", harness.gateway.calls)
	}

	positions, err := harness.portfolio.ListContext(context.Background(), "principal-a", portfolio.ModeTest, "", arbitrage.ProductID)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 2 {
		t.Fatalf("expected a position per leg, got %+v", positions)
	}

	// Replaying the same key must not place the hedge twice.
	replayStatus, replay := harness.open(t, previewID, "lineage-a", "idem-a")
	if replayStatus != http.StatusOK || len(replay.Legs) != 2 || harness.gateway.calls != 2 {
		t.Fatalf("replay status=%d legs=%d gateway calls=%d", replayStatus, len(replay.Legs), harness.gateway.calls)
	}
}

// TestArbitragePartialOpenIsNotAutoRolledBack fixes the deliberate choice in
// submitLegs: when the second leg fails, the first is left standing.
//
// Unwinding it here would mean this handler placing a closing order nobody
// previewed, off a price nobody saw — an automatic trade in a product whose
// whole premise is that every order is one an operator asked for. The pair is
// reported partially_open instead, and the surviving position stays visible so
// the operator has something to act on.
func TestArbitragePartialOpenIsNotAutoRolledBack(t *testing.T) {
	harness := newArbitrageHarness(t, &arbitrageGatewayFixture{failCall: 2})
	previewID := harness.preview(t)

	status, execution := harness.open(t, previewID, "lineage-b", "idem-b")
	if status != http.StatusAccepted {
		t.Fatalf("open status=%d execution=%+v", status, execution)
	}
	if execution.State != arbitrage.PairStatePartiallyOpen {
		t.Fatalf("state=%q execution=%+v", execution.State, execution)
	}
	// Both legs stay in the response. The failed one is what tells the operator
	// which side is missing, so dropping it would leave a hedge that reads as
	// half a position with no explanation of the other half.
	if len(execution.Legs) != 2 ||
		execution.Legs[0].Leg != arbitrage.LegLong || execution.Legs[0].State != orders.StateFilled ||
		execution.Legs[1].Leg != arbitrage.LegShort || execution.Legs[1].State != orders.StateFailed {
		t.Fatalf("legs=%+v", execution.Legs)
	}
	if len(execution.Failures) != 1 || execution.Failures[0].Leg != arbitrage.LegShort ||
		execution.Failures[0].Stage != "execute" || execution.Failures[0].Code != "VALIDATION_ERROR" {
		t.Fatalf("failures=%+v", execution.Failures)
	}

	// No third order: the fixture saw exactly the two legs the preview named.
	if harness.gateway.calls != 2 {
		t.Fatalf("gateway calls=%d requests=%+v", harness.gateway.calls, harness.gateway.requests)
	}
	for _, request := range harness.gateway.requests {
		if request.ReduceOnly {
			t.Fatalf("a reduce-only order was placed without an operator: %+v", request)
		}
	}

	positions, err := harness.portfolio.ListContext(context.Background(), "principal-a", portfolio.ModeTest, "", arbitrage.ProductID)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0].AccountID != "account-long" {
		t.Fatalf("the filled leg must stay visible, got %+v", positions)
	}

	commands, err := harness.commands.ListOwned(context.Background(), "principal-a", orders.ModeTest, arbitrage.ProductID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 {
		t.Fatalf("both legs must stay on the ledger, got %+v", commands)
	}
	states := map[string]orders.State{}
	for _, command := range commands {
		states[command.IdempotencyKey] = command.State
	}
	if states["idem-b:long"] != orders.StateFilled || states["idem-b:short"] != orders.StateFailed {
		t.Fatalf("states=%+v", states)
	}
}

// TestArbitrageOpportunityNeverPairsAVenueWithItself pins the one property that
// makes this product a hedge rather than a bet: two legs on two exchanges.
//
// Two routes lead to a self-pair, and a live board walks both. `compareRates`
// declines to order legs that published no funding interval, so taking the min
// and max of a group where only one venue dated its rate leaves both extremes
// on the same leg — a pair reporting a zero spread between a venue and itself,
// with no blocker to explain it. Separately, a venue the profile registry lists
// under two native environments arrives as two columns, so bybit's demo sandbox
// "hedges" bybit's testnet across an exchange boundary that does not exist.
func TestArbitrageOpportunityNeverPairsAVenueWithItself(t *testing.T) {
	key := boardKey{base: "BTC", quote: "USDT"}

	// One exchange dated its rate, the other did not. The pair is real and must
	// survive — unranked, and saying so — because dropping it hides the venue.
	opportunity, ok := buildOpportunity(key, "BTC-USDT", []arbitrageOpportunityLeg{
		{Venue: "binance", Environment: "demo", Rate: "0.0001", DailyRate: "0.0003"},
		{Venue: "lighter", Environment: "testnet", Rate: "0.00007"},
	})
	if !ok {
		t.Fatal("an underlying two exchanges quote must still be offered, unranked")
	}
	if opportunity.Long.Venue == opportunity.Short.Venue {
		t.Fatalf("hedged %s against itself", opportunity.Long.Venue)
	}
	if !slices.Contains(opportunity.Blockers, "funding_interval_unknown") {
		t.Fatalf("blockers = %v, want funding_interval_unknown", opportunity.Blockers)
	}
	if opportunity.Executable {
		t.Fatal("a pair nothing could rank reports itself executable")
	}

	// Two sandboxes of one exchange are one exchange.
	if _, offered := buildOpportunity(key, "BTC-USDT", []arbitrageOpportunityLeg{
		{Venue: "bybit", Environment: "demo", Rate: "0.0001", DailyRate: "0.0003"},
		{Venue: "bybit", Environment: "testnet", Rate: "0.00042", DailyRate: "0.00126"},
	}); offered {
		t.Fatal("bybit's two native environments were offered as a cross-venue hedge")
	}

	// And an underlying only one exchange quotes is not an opportunity at all.
	if _, offered := buildOpportunity(key, "BTC-USDT", []arbitrageOpportunityLeg{
		{Venue: "binance", Environment: "demo", Rate: "0.0001", DailyRate: "0.0003"},
	}); offered {
		t.Fatal("a single-venue underlying was offered as a hedge")
	}

	// The ranking itself still holds when there is something to rank: long the
	// venue paying least, short the venue paying most, spread the difference.
	opportunity, ok = buildOpportunity(key, "BTC-USDT", []arbitrageOpportunityLeg{
		{Venue: "bybit", Environment: "testnet", Rate: "0.00042", DailyRate: "0.00126"},
		{Venue: "binance", Environment: "demo", Rate: "0.0001", DailyRate: "0.0003"},
	})
	if !ok {
		t.Fatal("a rankable cross-venue pair was dropped")
	}
	if opportunity.Long.Venue != "binance" || opportunity.Short.Venue != "bybit" {
		t.Fatalf("long %s / short %s, want long binance / short bybit",
			opportunity.Long.Venue, opportunity.Short.Venue)
	}
	if opportunity.SpreadDaily != "0.00096" {
		t.Fatalf("spreadDaily = %q, want 0.00096", opportunity.SpreadDaily)
	}
}

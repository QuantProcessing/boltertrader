package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

type runtimeProjectionFixture struct {
	principal string
	accountID string
	items     []AccountRuntimeProjection
}

type balanceProjectionFixture struct {
	principal string
	accountID string
	items     []AccountBalanceProjection
}

type positionProjectionFixture struct {
	principal    string
	accountID    string
	instrumentID string
	items        []AccountPositionProjection
}

type portfolioPositionFixture struct {
	principal string
	filter    accountpositions.Filter
	snapshot  accountpositions.Snapshot
}

func (fixture *portfolioPositionFixture) Snapshot(_ context.Context, principal string, filter accountpositions.Filter) (accountpositions.Snapshot, error) {
	fixture.principal, fixture.filter = principal, filter
	return fixture.snapshot, nil
}

func (fixture *positionProjectionFixture) ListAccountPositions(_ context.Context, principal, accountID, instrumentID string) ([]AccountPositionProjection, error) {
	fixture.principal, fixture.accountID, fixture.instrumentID = principal, accountID, instrumentID
	return append([]AccountPositionProjection(nil), fixture.items...), nil
}

func (fixture *balanceProjectionFixture) ListAccountBalances(_ context.Context, principal, accountID string) ([]AccountBalanceProjection, error) {
	fixture.principal, fixture.accountID = principal, accountID
	return append([]AccountBalanceProjection(nil), fixture.items...), nil
}

func (fixture *runtimeProjectionFixture) ListAccountRuntimes(_ context.Context, principal, accountID string) ([]AccountRuntimeProjection, error) {
	fixture.principal, fixture.accountID = principal, accountID
	return fixture.items, nil
}

type commandProjectionFixture struct {
	commands map[string][]orders.Command
	calls    [][2]string
}

func (fixture *commandProjectionFixture) ListOwned(_ context.Context, principal string, productID string) ([]orders.Command, error) {
	fixture.calls = append(fixture.calls, [2]string{principal, productID})
	return append([]orders.Command(nil), fixture.commands[productID]...), nil
}

func TestBrowserProjectionRoutesAreScopedAndReturnAttributedData(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	activity, err := NewActivityLog(16, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activity.Publish(ActivityRecord{PrincipalID: testPrincipalID, Kind: "account.runtime.updated", AccountID: "account-a", ResourceID: "account-a", Payload: map[string]any{"ready": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := activity.Publish(ActivityRecord{PrincipalID: "other-principal", Kind: "command.accepted", ProductID: "manual", ResourceID: "other-command"}); err != nil {
		t.Fatal(err)
	}
	runtimes := &runtimeProjectionFixture{items: []AccountRuntimeProjection{{AccountID: "account-a", RuntimeID: "runtime-a", Connected: true, ObservedAt: now}}}

	commands := &commandProjectionFixture{commands: map[string][]orders.Command{
		"manual": {
			{CommandID: "manual-command", PrincipalID: testPrincipalID, AccountID: "account-a", LineageID: "manual-lineage", ProductID: "manual", IdempotencyKey: "manual-key", CommandKind: "order.place", State: orders.StateFilled, Blockers: []orders.Blocker{}, CreatedAt: now.Add(-time.Minute), UpdatedAt: now, ExecutionEvidence: orders.ExecutionEvidence{Fills: []orders.FillEvidence{{FillID: "fill-a", AccountID: "account-a", InstrumentID: "btc-usdt", Side: "buy", Quantity: "0.5", Price: "100", Fee: "0.01", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now}}}},
			{CommandID: "submitted-command", PrincipalID: testPrincipalID, AccountID: "account-b", LineageID: "submitted-lineage", ProductID: "manual", IdempotencyKey: "submitted-key", CommandKind: "order.place", State: orders.StateSubmitted, Blockers: []orders.Blocker{}, CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-time.Minute)},
		},
	}}
	balances := &balanceProjectionFixture{items: []AccountBalanceProjection{{
		AccountID: "account-a", Product: "perp", SettlementAsset: "USDT", Asset: "USDT",
		Available: "123.45", AvailableMargin: "120", Locked: "0", Total: "123.45",
	}}}
	positions := &positionProjectionFixture{items: []AccountPositionProjection{{
		AccountID: "account-a", InstrumentID: "binance:live:perp:USDT:BTC-USDT", Side: "sell",
		Quantity: "-0.25", EntryPrice: "65000", MarkPrice: "64000", UnrealizedPnL: "250",
	}}}
	principal := func(*http.Request) (string, error) { return testPrincipalID, nil }
	mux := http.NewServeMux()
	if err := RegisterBrowserProjectionRoutes(mux, BrowserProjectionRoutesOptions{Runtimes: runtimes, Balances: balances, Positions: positions, Commands: commands, Activity: activity, Principal: principal}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path       string
		contains   []string
		notContain []string
	}{
		{path: "/api/v1/accounts/account-a/runtimes", contains: []string{`"accountId":"account-a"`, `"connected":true`}, notContain: []string{`"scaffold":true`, "gateway-adapter-token", `"runtimeVersion"`}},
		{path: "/api/v1/balances?accountId=account-a", contains: []string{`"accountId":"account-a"`, `"available":"123.45"`, `"availableMargin":"120"`}, notContain: []string{`"principalId"`, `"credential"`}},
		{path: "/api/v1/account-positions?accountId=account-a&instrumentId=binance:live:perp:USDT:BTC-USDT", contains: []string{`"quantity":"-0.25"`, `"side":"sell"`, `"markPrice":"64000"`}, notContain: []string{`"principalId"`, `"credential"`}},
		{path: "/api/v1/commands", contains: []string{`"commandId":"manual-command"`, `"commandId":"submitted-command"`}, notContain: []string{`"scaffold":true`, `"payload"`, `"principalId"`}},
		{path: "/api/v1/fills?productId=manual", contains: []string{`"fillId":"fill-a"`, `"productId":"manual"`, `"lineageId":"manual-lineage"`}, notContain: []string{`"submitted-lineage"`, `"scaffold":true`}},
		{path: "/api/v1/activity", contains: []string{`"kind":"account.runtime.updated"`, `"accountId":"account-a"`}, notContain: []string{`"other-command"`, `"scaffold":true`, `"principalId"`}},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
		for _, value := range test.contains {
			if !strings.Contains(response.Body.String(), value) {
				t.Errorf("GET %s missing %s: %s", test.path, value, response.Body.String())
			}
		}
		for _, value := range test.notContain {
			if strings.Contains(response.Body.String(), value) {
				t.Errorf("GET %s leaked %s: %s", test.path, value, response.Body.String())
			}
		}
	}
	if runtimes.principal != testPrincipalID || runtimes.accountID != "account-a" {
		t.Fatalf("runtime scope=%q/%q", runtimes.principal, runtimes.accountID)
	}
	if balances.principal != testPrincipalID || balances.accountID != "account-a" {
		t.Fatalf("balance scope=(%q,%q)", balances.principal, balances.accountID)
	}
	if positions.principal != testPrincipalID || positions.accountID != "account-a" || positions.instrumentID != "binance:live:perp:USDT:BTC-USDT" {
		t.Fatalf("position scope=(%q,%q,%q)", positions.principal, positions.accountID, positions.instrumentID)
	}
	if len(commands.calls) != 2 {
		t.Fatalf("command projection calls=%+v", commands.calls)
	}
	for _, call := range commands.calls {
		if call[0] != testPrincipalID || call[1] != "manual" {
			t.Fatalf("unscoped command projection call=%+v", call)
		}
	}
	principalEvents, err := activity.ListActivity(context.Background(), testPrincipalID, ActivityQuery{})
	if err != nil || len(principalEvents) != 1 || principalEvents[0].Kind != "account.runtime.updated" {
		t.Fatalf("principal activity=%+v err=%v", principalEvents, err)
	}
	otherEvents, err := activity.ListActivity(context.Background(), "other-principal", ActivityQuery{})
	if err != nil || len(otherEvents) != 1 || otherEvents[0].EventID == principalEvents[0].EventID {
		t.Fatalf("other-principal activity=%+v err=%v", otherEvents, err)
	}
}

func TestBrowserProjectionRoutesRejectUnknownFilters(t *testing.T) {
	principal := func(*http.Request) (string, error) { return testPrincipalID, nil }
	mux := http.NewServeMux()
	if err := RegisterBrowserProjectionRoutes(mux, BrowserProjectionRoutesOptions{Runtimes: &runtimeProjectionFixture{}, Balances: &balanceProjectionFixture{}, Commands: &commandProjectionFixture{}, Activity: mustActivityLog(t), Principal: principal}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/commands?gatewayUrl=http://gateway", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("unknown filter response=%d %s", response.Code, response.Body.String())
	}
}

func TestBrowserPortfolioPositionsExposeGlobalAndExactScopeFromOneProjection(t *testing.T) {
	fixture := &portfolioPositionFixture{snapshot: accountpositions.Snapshot{
		ProjectionRevision: 7, Summary: accountpositions.Summary{PositionCount: 1, AccountCount: 1},
		Items: []accountpositions.Item{{
			Key:          accountpositions.Key{AccountID: "account-a", RouteID: "perp-usdt", InstrumentID: "binance:live:perp:USDT:BTC-USDT", PositionSlot: "long"},
			AccountLabel: "Main", Venue: "binance", Product: "perp", SettlementAsset: "USDT", Symbol: "BTC-USDT",
			Side: "buy", Quantity: "0.1", Freshness: accountpositions.FreshnessLive, RouteHealth: accountpositions.RouteLive,
		}},
	}}
	principal := func(*http.Request) (string, error) { return testPrincipalID, nil }
	mux := http.NewServeMux()
	if err := RegisterBrowserProjectionRoutes(mux, BrowserProjectionRoutesOptions{
		Runtimes: &runtimeProjectionFixture{}, Balances: &balanceProjectionFixture{}, Positions: &positionProjectionFixture{},
		PortfolioPositions: fixture, Commands: &commandProjectionFixture{}, Activity: mustActivityLog(t), Principal: principal,
	}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path       string
		accountID  string
		instrument string
	}{
		{path: "/api/v1/portfolio/positions?venue=binance&status=live"},
		{path: "/api/v1/account-positions?accountId=account-a&instrumentId=binance:live:perp:USDT:BTC-USDT", accountID: "account-a", instrument: "binance:live:perp:USDT:BTC-USDT"},
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"positionSlot":"long"`) || !strings.Contains(response.Body.String(), `"projectionRevision":7`) {
			t.Fatalf("GET %s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
		if test.accountID != "" && (fixture.filter.AccountID != test.accountID || fixture.filter.InstrumentID != test.instrument) {
			t.Fatalf("exact filter=%+v", fixture.filter)
		}
	}
	if fixture.principal != testPrincipalID {
		t.Fatalf("principal=%q", fixture.principal)
	}
}

func mustActivityLog(t *testing.T) *ActivityLog {
	t.Helper()
	log, err := NewActivityLog(4, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return log
}

// testPrincipalID stands for whatever identity the caller resolved before
// these routes ran. The projections must scope every read to it; how it was
// authenticated is the auth edge's business, not this service's.
const testPrincipalID = "browser-session"

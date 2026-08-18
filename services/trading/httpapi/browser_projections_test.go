package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/realtime"
)

type runtimeProjectionFixture struct {
	principal string
	accountID string
	items     []AccountRuntimeProjection
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
	hub, err := realtime.NewHub(16, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	activity, err := NewActivityLog(16, func() time.Time { return now }, hub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activity.Publish(ActivityRecord{PrincipalID: testPrincipalID, Kind: "account.runtime.updated", AccountID: "account-a", ResourceID: "account-a", Payload: map[string]any{"ready": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := activity.Publish(ActivityRecord{PrincipalID: "other-principal", Kind: "command.accepted", ProductID: "manual", ResourceID: "other-command"}); err != nil {
		t.Fatal(err)
	}
	runtimes := &runtimeProjectionFixture{items: []AccountRuntimeProjection{{
		AccountID: "account-a", DesiredStateRevision: 7, RuntimeGeneration: 3, CredentialVersion: 2, RuntimeVersion: 9,
		Readiness: ProjectionReadiness{Ready: true}, WriteReadiness: ProjectionReadiness{Ready: true}, LastAppliedDesiredStateRevision: 7, LastAppliedRuntimeGeneration: 3,
		Capabilities: []AccountCapabilityProjection{{RowID: "binance:perp:USDT", Venue: "binance", Product: "perp", Settlement: "USDT", WriteReady: true, Operations: CapabilityOperations{Place: true, Cancel: true, Fills: true, Positions: true}}}, ObservedAt: now,
	}}}
	commands := &commandProjectionFixture{commands: map[string][]orders.Command{
		"manual": {
			{CommandID: "manual-command", PrincipalID: testPrincipalID, AccountID: "account-a", LineageID: "manual-lineage", ProductID: "manual", IdempotencyKey: "manual-key", CommandKind: "order.place", State: orders.StateFilled, Blockers: []orders.Blocker{}, CreatedAt: now.Add(-time.Minute), UpdatedAt: now, ExecutionEvidence: orders.ExecutionEvidence{Fills: []orders.FillEvidence{{FillID: "fill-a", AccountID: "account-a", InstrumentID: "btc-usdt", Side: "buy", Quantity: "0.5", Price: "100", Fee: "0.01", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now}}}},
			{CommandID: "submitted-command", PrincipalID: testPrincipalID, AccountID: "account-b", LineageID: "submitted-lineage", ProductID: "manual", IdempotencyKey: "submitted-key", CommandKind: "order.place", State: orders.StateSubmitted, Blockers: []orders.Blocker{}, CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-time.Minute)},
		},
	}}
	principal := func(*http.Request) (string, error) { return testPrincipalID, nil }
	mux := http.NewServeMux()
	if err := RegisterBrowserProjectionRoutes(mux, BrowserProjectionRoutesOptions{Runtimes: runtimes, Commands: commands, Activity: activity, Principal: principal}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path       string
		contains   []string
		notContain []string
	}{
		{path: "/api/v1/accounts/account-a/runtimes", contains: []string{`"accountId":"account-a"`, `"cancel":true`, `"runtimeVersion":9`}, notContain: []string{`"scaffold":true`, "gateway-adapter-token"}},
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
	if len(commands.calls) != 2 {
		t.Fatalf("command projection calls=%+v", commands.calls)
	}
	for _, call := range commands.calls {
		if call[0] != testPrincipalID || call[1] != "manual" {
			t.Fatalf("unscoped command projection call=%+v", call)
		}
	}
	replayed, err := hub.ReplayScoped("", testPrincipalID)
	if err != nil || len(replayed) != 1 {
		t.Fatalf("SSE replay=%+v err=%v", replayed, err)
	}
	otherReplay, err := hub.ReplayScoped("", "other-principal")
	if err != nil || len(otherReplay) != 1 || otherReplay[0].EventID == replayed[0].EventID {
		t.Fatalf("other-principal SSE replay=%+v err=%v", otherReplay, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(replayed[0].Payload, &payload); err != nil || payload["accountId"] != "account-a" {
		t.Fatalf("SSE activity payload=%s err=%v", replayed[0].Payload, err)
	}
}

func TestBrowserProjectionRoutesRejectUnknownFilters(t *testing.T) {
	principal := func(*http.Request) (string, error) { return testPrincipalID, nil }
	mux := http.NewServeMux()
	if err := RegisterBrowserProjectionRoutes(mux, BrowserProjectionRoutesOptions{Runtimes: &runtimeProjectionFixture{}, Commands: &commandProjectionFixture{}, Activity: mustActivityLog(t), Principal: principal}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/commands?gatewayUrl=http://gateway", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("unknown filter response=%d %s", response.Code, response.Body.String())
	}
}

func mustActivityLog(t *testing.T) *ActivityLog {
	t.Helper()
	hub, _ := realtime.NewHub(4, time.Now)
	log, err := NewActivityLog(4, time.Now, hub)
	if err != nil {
		t.Fatal(err)
	}
	return log
}

// testPrincipalID stands for whatever identity the caller resolved before
// these routes ran. The projections must scope every read to it; how it was
// authenticated is the auth edge's business, not this service's.
const testPrincipalID = "browser-session"

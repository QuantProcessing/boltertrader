package executionpreview

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/shopspring/decimal"
)

var sourceNow = time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)

func TestFreshOpenPreviewUsesPersistedOpportunityAndWarnsWithoutNormalizedRisk(t *testing.T) {
	reader := &fakeReader{opportunity: openOpportunityJSON(t)}
	gateway := readyGatewayFixture()
	source, err := NewSource(SourceOptions{
		Reader: reader, Refresher: &fakeRefresher{}, Gateway: gateway,
		Market: &fakeMarket{}, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := executionservice.PreviewRequest{
		PreviewID: "preview-open", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	}
	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, request)
	if err != nil {
		t.Fatal(err)
	}
	if input.Long.AccountID != "account-long" || input.Short.AccountID != "account-short" {
		t.Fatalf("account identity was not preserved: long=%q short=%q", input.Long.AccountID, input.Short.AccountID)
	}
	if input.Long.GatewayInstrumentID != "binance:demo:perp:USDT:BTC-USDT" ||
		input.Short.GatewayInstrumentID != "okx:testnet:perp:USDT:BTC-USDT" {
		t.Fatalf("instrument identity was not preserved: long=%q short=%q",
			input.Long.GatewayInstrumentID, input.Short.GatewayInstrumentID)
	}
	if !input.AccountVerifiable || !input.RulesVerifiable {
		t.Fatalf("ready account/rules were not verified: account=%v rules=%v", input.AccountVerifiable, input.RulesVerifiable)
	}
	if input.RiskVerifiable {
		t.Fatal("Gateway V1 unavailable risk limits must remain explicit")
	}
	if !input.AvailableCapitalUSDT.Equal(decimal.NewFromInt(7000)) {
		t.Fatalf("available capital=%s, want minimum account balance 7000", input.AvailableCapitalUSDT)
	}
	if input.SnapshotID == "" || input.DataObservedAt.IsZero() {
		t.Fatalf("coherent snapshot identity/time missing: %#v", input)
	}
	preview, err := execution.Preview(input)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanSubmit || !hasReason(preview.Warnings, execution.ReasonRiskUnverifiable) {
		t.Fatalf("real V1 risk gap was not exposed as a user-controlled warning: blockers=%#v warnings=%#v",
			preview.Blockers, preview.Warnings)
	}

	second, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, request)
	if err != nil {
		t.Fatal(err)
	}
	if second.SnapshotID != input.SnapshotID {
		t.Fatalf("same evidence changed snapshot id: %q != %q", second.SnapshotID, input.SnapshotID)
	}
	if gateway.writeCalls != 0 {
		t.Fatalf("preview performed private writes: %d", gateway.writeCalls)
	}
}

func TestFreshOpenPreviewFollowsCurrentCompatibleWriteUniverse(t *testing.T) {
	gateway := readyGatewayFixture()
	gateway.discovery.Accounts = gateway.discovery.Accounts[:1]
	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{opportunity: openOpportunityJSON(t)}, Refresher: &fakeRefresher{},
		Gateway: gateway, Market: &fakeMarket{}, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-current-universe-blocked", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.AccountVerifiable {
		t.Fatalf("account outside current compatible write universe was accepted: %+v", input)
	}
	preview, err := execution.Preview(input)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanSubmit || !hasReason(preview.Blockers, execution.ReasonAccountUnverified) {
		t.Fatalf("preview did not fail closed: blockers=%#v", preview.Blockers)
	}

	gateway.discovery = readyGatewayFixture().discovery
	input, err = source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-current-universe-recovered", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !input.AccountVerifiable {
		t.Fatalf("preview did not recover with current compatible write universe: %+v", input)
	}
	if gateway.discoveryCalls != 2 {
		t.Fatalf("discover calls=%d, want per-preview discovery", gateway.discoveryCalls)
	}
}

func TestFreshOpenPreviewFailsClosedWhenCurrentCompatibleWriteUniverseUnavailable(t *testing.T) {
	gateway := readyGatewayFixture()
	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{opportunity: openOpportunityJSON(t)}, Refresher: &fakeRefresher{},
		Gateway: gateway, Market: &fakeMarket{}, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	gateway.discoveryErr = errors.New("catalog unavailable")
	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-current-universe-unavailable", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.AccountVerifiable {
		t.Fatalf("preview accepted open accounts when current compatible write universe was unavailable: %+v", input)
	}
}

func TestFreshOpenPreviewValidatesEachMixedTestLegAgainstItsOwnVenueEnvironment(t *testing.T) {
	gateway := readyGatewayFixture()
	gateway.discovery.Accounts[1].VenueEnvironment = boltertrader.GatewayTestnet
	gateway.instruments["account-short"][0].VenueEnvironment = boltertrader.GatewayTestnet
	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{opportunity: openOpportunityJSON(t)}, Refresher: &fakeRefresher{},
		Gateway: gateway, Market: &fakeMarket{}, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-mixed", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !input.AccountVerifiable || !input.RulesVerifiable {
		t.Fatalf("mixed Test route was not verified: account=%t rules=%t", input.AccountVerifiable, input.RulesVerifiable)
	}
	if input.Long.VenueEnvironment != "demo" || input.Short.VenueEnvironment != "testnet" {
		t.Fatalf("preview lost per-leg environment: long=%+v short=%+v", input.Long, input.Short)
	}

	gateway.discovery.Accounts[1].VenueEnvironment = boltertrader.GatewayDemo
	gateway.instruments["account-short"][0].VenueEnvironment = boltertrader.GatewayDemo
	mismatched, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-mixed-mismatch", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mismatched.AccountVerifiable || mismatched.RulesVerifiable {
		t.Fatalf("changed native identity was accepted: %+v", mismatched)
	}
}

func TestFreshOpenPreviewRejectsCalculationRouteThatDiffersFromVerifiedOpportunityRoute(t *testing.T) {
	raw := openOpportunityJSON(t)
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	calculation := payload["calculationInputs"].(map[string]any)
	longLeg := calculation["longLeg"].(map[string]any)
	longLeg["accountId"] = "unverified-account"
	longLeg["instrumentId"] = "binance:demo:perp:USDT:ETH-USDT"
	raw = mustJSON(t, payload)

	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{opportunity: raw}, Refresher: &fakeRefresher{},
		Gateway: readyGatewayFixture(), Market: &fakeMarket{}, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-open", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	})
	if !errors.Is(err, executionservice.ErrPreviewMismatch) {
		t.Fatalf("route mismatch error=%v, want ErrPreviewMismatch", err)
	}
}

func TestFreshOpenPreviewUsesRouteScopedFreshPayloadAfterReferenceChecks(t *testing.T) {
	stored := openOpportunityJSON(t)
	var storedPayload map[string]any
	if err := json.Unmarshal(stored, &storedPayload); err != nil {
		t.Fatal(err)
	}
	storedPayload["observedAt"] = sourceNow.Add(-10 * time.Second).Format(time.RFC3339Nano)
	storedPayload["blockers"] = []any{
		map[string]any{"code": "stale_data", "message": "stored snapshot is stale"},
	}
	calculation := storedPayload["calculationInputs"].(map[string]any)
	calculation["longLeg"].(map[string]any)["marketObservedAt"] = sourceNow.Add(-10 * time.Second).Format(time.RFC3339Nano)
	calculation["shortLeg"].(map[string]any)["marketObservedAt"] = sourceNow.Add(-10 * time.Second).Format(time.RFC3339Nano)

	refresher := &fakeRefresher{fresh: openOpportunityJSON(t)}
	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{opportunity: mustJSON(t, storedPayload)}, Refresher: refresher,
		Gateway: readyGatewayFixture(), Market: &fakeMarket{}, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-fresh-route", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if refresher.refreshCalls != 0 {
		t.Fatalf("open preview used full refresh calls=%d", refresher.refreshCalls)
	}
	if refresher.longAccountID != "account-long" ||
		refresher.longInstrumentID != "binance:demo:perp:USDT:BTC-USDT" ||
		refresher.shortAccountID != "account-short" ||
		refresher.shortInstrumentID != "okx:testnet:perp:USDT:BTC-USDT" {
		t.Fatalf("fresh route args mismatch: %+v", refresher)
	}
	for _, blocker := range input.OriginalBlockers {
		if blocker.Code == execution.ReasonStaleData {
			t.Fatalf("fresh preview inherited stale stored blocker: %+v", input.OriginalBlockers)
		}
	}
	if !input.DataObservedAt.Equal(sourceNow.Add(-time.Second)) {
		t.Fatalf("data observed at=%s want fresh payload time", input.DataObservedAt)
	}
	preview, err := execution.Preview(input)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanSubmit {
		t.Fatalf("fresh route should be submittable apart from risk warning: blockers=%+v", preview.Blockers)
	}
}

func TestFreshOpenPreviewRejectsFreshRouteFlip(t *testing.T) {
	fresh := openOpportunityJSON(t)
	var payload map[string]any
	if err := json.Unmarshal(fresh, &payload); err != nil {
		t.Fatal(err)
	}
	payload["longAccountId"], payload["shortAccountId"] = payload["shortAccountId"], payload["longAccountId"]
	payload["longInstrumentId"], payload["shortInstrumentId"] = payload["shortInstrumentId"], payload["longInstrumentId"]
	payload["longVenue"], payload["shortVenue"] = payload["shortVenue"], payload["longVenue"]
	calculation := payload["calculationInputs"].(map[string]any)
	calculation["longLeg"], calculation["shortLeg"] = calculation["shortLeg"], calculation["longLeg"]

	source, err := NewSource(SourceOptions{
		Reader:    &fakeReader{opportunity: openOpportunityJSON(t)},
		Refresher: &fakeRefresher{fresh: mustJSON(t, payload)},
		Gateway:   readyGatewayFixture(), Market: &fakeMarket{}, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-route-flip", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	})
	if !errors.Is(err, executionservice.ErrPreviewMismatch) {
		t.Fatalf("route flip error=%v, want ErrPreviewMismatch", err)
	}
}

func TestFreshOpenPreviewBlocksWhenRouteRefreshUnavailable(t *testing.T) {
	source, err := NewSource(SourceOptions{
		Reader:    &fakeReader{opportunity: openOpportunityJSON(t)},
		Refresher: &fakeRefresher{freshErr: errors.New("route refresh unavailable")},
		Gateway:   readyGatewayFixture(), Market: &fakeMarket{}, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-refresh-failed", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := execution.Preview(input)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanSubmit || !hasReason(preview.Blockers, execution.ReasonOriginalBlocker) {
		t.Fatalf("refresh failure did not fail closed: blockers=%+v", preview.Blockers)
	}
}

func TestFreshClosePreviewUsesExactGatewayPositionsAndFreshPublicMarket(t *testing.T) {
	reader := &fakeReader{position: positionJSON(t, "open")}
	gateway := readyGatewayFixture()
	gateway.positions = map[string][]boltertrader.PositionResource{
		"account-long": {{
			AccountID: "account-long", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.12")},
		}},
		"account-short": {{
			AccountID: "account-short", InstrumentID: "okx:testnet:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideSell, Quantity: decimal.RequireFromString("0.10")},
		}},
	}
	market := closeMarketFixture()
	source, err := NewSource(SourceOptions{
		Reader: reader, Refresher: &fakeRefresher{}, Gateway: gateway,
		Market: market, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-close", Operation: execution.OperationClose, PositionID: "position-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !input.CurrentPositionVerifiable || !input.RiskVerifiable {
		t.Fatalf("reduce-only position proof missing: position=%v risk=%v",
			input.CurrentPositionVerifiable, input.RiskVerifiable)
	}
	if !input.CurrentLongQuantity.Equal(decimal.RequireFromString("0.12")) ||
		!input.CurrentShortQuantity.Equal(decimal.RequireFromString("0.10")) {
		t.Fatalf("current quantities long=%s short=%s", input.CurrentLongQuantity, input.CurrentShortQuantity)
	}
	if input.ParentExecutionID != "execution-open" || input.LineageID != "lineage-open" {
		t.Fatalf("position lineage lost: parent=%q lineage=%q", input.ParentExecutionID, input.LineageID)
	}
	if !input.CapitalUSDT.Equal(decimal.NewFromInt(5000)) || input.Leverage != 2 {
		t.Fatalf("position economics lost: capital=%s leverage=%d", input.CapitalUSDT, input.Leverage)
	}
	preview, err := execution.Preview(input)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanSubmit {
		t.Fatalf("verified reduce-only close was blocked: %#v", preview.Blockers)
	}
	if !preview.PlannedLegs[0].Quantity.Equal(decimal.RequireFromString("0.12")) ||
		!preview.PlannedLegs[1].Quantity.Equal(decimal.RequireFromString("0.10")) {
		t.Fatalf("close quantities were mark-derived: %#v", preview.PlannedLegs)
	}
	if !preview.PlannedLegs[0].ReduceOnly || !preview.PlannedLegs[1].ReduceOnly {
		t.Fatalf("close legs are not reduce-only: %#v", preview.PlannedLegs)
	}
	if gateway.writeCalls != 0 || market.writeCalls != 0 {
		t.Fatal("close preview crossed a private write boundary")
	}
}

func TestFreshClosePreviewIgnoresCurrentOpenCompatibleWriteUniverse(t *testing.T) {
	gateway := readyGatewayFixture()
	gateway.discovery.Accounts[1].Settlement = "USDC"
	gateway.positions = map[string][]boltertrader.PositionResource{
		"account-long": {{
			AccountID: "account-long", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.12")},
		}},
		"account-short": {{
			AccountID: "account-short", InstrumentID: "okx:testnet:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideSell, Quantity: decimal.RequireFromString("0.10")},
		}},
	}
	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{position: positionJSON(t, "open")}, Refresher: &fakeRefresher{},
		Gateway: gateway, Market: closeMarketFixture(), Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID:  "preview-close-open-universe-degraded",
		Operation:  execution.OperationClose,
		PositionID: "position-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !input.AccountVerifiable || !input.RulesVerifiable || !input.CurrentPositionVerifiable {
		t.Fatalf("close preview incorrectly depended on open compatibility: %+v", input)
	}
	preview, err := execution.Preview(input)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanSubmit {
		t.Fatalf("reduce-only close was blocked by open-compatible universe: blockers=%+v", preview.Blockers)
	}
}

func TestFreshClosePreviewConsumesProjectedMixedTestPosition(t *testing.T) {
	gateway := readyGatewayFixture()
	gateway.positions = map[string][]boltertrader.PositionResource{
		"account-long": {{
			AccountID: "account-long", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.12")},
		}},
		"account-short": {{
			AccountID: "account-short", InstrumentID: "okx:testnet:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideSell, Quantity: decimal.RequireFromString("0.10")},
		}},
	}
	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{position: projectedPositionJSON(t)}, Refresher: &fakeRefresher{},
		Gateway: gateway, Market: closeMarketFixture(), Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-close-projected", Operation: execution.OperationClose,
		PositionID: "position-execution-open",
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := execution.Preview(input)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanSubmit {
		t.Fatalf("projected mixed Test position cannot close: blockers=%+v input=%+v", preview.Blockers, input)
	}
	if preview.PlannedLegs[0].VenueEnvironment != "demo" ||
		preview.PlannedLegs[1].VenueEnvironment != "testnet" {
		t.Fatalf("projected position lost native provenance: %+v", preview.PlannedLegs)
	}
}

func TestFreshClosePreviewFailsClosedWhenGatewayPositionIdentityIsWrong(t *testing.T) {
	gateway := readyGatewayFixture()
	gateway.positions = map[string][]boltertrader.PositionResource{
		"account-long": {{
			AccountID: "account-other", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.12")},
		}},
	}
	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{position: positionJSON(t, "open")}, Refresher: &fakeRefresher{},
		Gateway: gateway, Market: closeMarketFixture(), Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-close", Operation: execution.OperationClose, PositionID: "position-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.CurrentPositionVerifiable || input.RiskVerifiable {
		t.Fatalf("wrong private position identity was accepted: %#v", input)
	}
	preview, err := execution.Preview(input)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanSubmit || !hasReason(preview.Blockers, execution.ReasonPositionUnverifiable) {
		t.Fatalf("wrong position identity did not fail closed: %#v", preview.Blockers)
	}
}

func TestFreshClosePreviewReadsMarketEvidenceAfterSlowPositionChecks(t *testing.T) {
	gateway := readyGatewayFixture()
	gateway.positions = map[string][]boltertrader.PositionResource{
		"account-long": {{
			AccountID: "account-long", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.12")},
		}},
		"account-short": {{
			AccountID: "account-short", InstrumentID: "okx:testnet:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideSell, Quantity: decimal.RequireFromString("0.10")},
		}},
	}
	positionsChecked := false
	trackingGateway := &positionTrackingGateway{fakeGateway: gateway, positionsChecked: &positionsChecked}
	market := &positionAwareMarket{fakeMarket: closeMarketFixture(), positionsChecked: &positionsChecked}
	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{position: positionJSON(t, "open")}, Refresher: &fakeRefresher{},
		Gateway: trackingGateway, Market: market, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	input, err := source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "preview-close-order", Operation: execution.OperationClose, PositionID: "position-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if market.evidenceBeforePosition {
		t.Fatal("close preview read market evidence before current position checks completed")
	}
	if !input.CurrentPositionVerifiable {
		t.Fatalf("position proof missing: %+v", input)
	}
}

func TestMarketEvidenceReadsMarkBBOAndBookConcurrently(t *testing.T) {
	market := newConcurrentEvidenceMarket(closeMarketFixture())
	instrument := market.instruments[0]
	source := &Source{market: market}

	_, _, _, ok := source.marketEvidence(context.Background(), instrument, true)
	if !ok {
		t.Fatal("market evidence was not collected from concurrent mark/BBO/book reads")
	}
	if market.maxConcurrent() != 3 {
		t.Fatalf("market evidence concurrency=%d want 3", market.maxConcurrent())
	}
}

func TestFreshPreviewMapsScopedNotFoundAndRejectsUnsupportedOperation(t *testing.T) {
	source, err := NewSource(SourceOptions{
		Reader: &fakeReader{err: readmodel.ErrNotFound}, Refresher: &fakeRefresher{},
		Gateway: readyGatewayFixture(), Market: &fakeMarket{}, Now: func() time.Time { return sourceNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{
		PreviewID: "missing", Operation: execution.OperationOpen, OpportunityID: "missing",
	})
	if !errors.Is(err, executionservice.ErrNotFound) {
		t.Fatalf("not found error=%v", err)
	}
	_, err = source.FreshPreview(context.Background(), executionservice.Scope{
		PrincipalID: "principal-default", Mode: "test",
	}, executionservice.PreviewRequest{PreviewID: "invalid", Operation: "rebalance"})
	if !errors.Is(err, executionservice.ErrPreviewMismatch) {
		t.Fatalf("unsupported operation error=%v", err)
	}
}

func openOpportunityJSON(t *testing.T) json.RawMessage {
	t.Helper()
	return mustJSON(t, openOpportunityValue())
}

func openOpportunityValue() map[string]any {
	return map[string]any{
		"opportunityId": "opp-1", "symbol": "BTC-USDT",
		"longVenue": "binance", "shortVenue": "okx",
		"longAccountId": "account-long", "shortAccountId": "account-short",
		"longInstrumentId":  "binance:demo:perp:USDT:BTC-USDT",
		"shortInstrumentId": "okx:testnet:perp:USDT:BTC-USDT",
		"grossAprPct":       "24.5", "netAprPct": "18.4", "stabilityPct": "92",
		"capacityUsd": "100000", "roundTripFeeBps": "8", "estimatedSlippageBps": "2",
		"observedAt": sourceNow.Add(-time.Second).Format(time.RFC3339Nano),
		"blockers":   []any{}, "warnings": []any{},
		"calculationInputs": map[string]any{
			"liquidationBufferPct": "60",
			"longLeg": map[string]any{
				"accountId": "account-long", "instrumentId": "binance:demo:perp:USDT:BTC-USDT",
				"venue": "binance", "venueEnvironment": "demo",
				"venueSymbol": "BTC-USDT", "markPrice": "80000",
				"marketObservedAt": sourceNow.Add(-time.Second).Format(time.RFC3339Nano),
			},
			"shortLeg": map[string]any{
				"accountId": "account-short", "instrumentId": "okx:testnet:perp:USDT:BTC-USDT",
				"venue": "okx", "venueEnvironment": "testnet",
				"venueSymbol": "BTC-USDT", "markPrice": "80010",
				"marketObservedAt": sourceNow.Add(-time.Second).Format(time.RFC3339Nano),
			},
		},
	}
}

func defaultOpenOpportunityJSON() json.RawMessage {
	raw, err := json.Marshal(openOpportunityValue())
	if err != nil {
		panic(err)
	}
	return raw
}

func positionJSON(t *testing.T, state string) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"positionId": "position-1", "openingExecutionId": "execution-open",
		"lineageId": "lineage-open", "opportunityId": "opp-1", "symbol": "BTC-USDT",
		"capital": "5000", "singleLegNotional": "10000", "leverage": 2,
		"netAprPct": "18.4", "stabilityPct": "92", "roundTripFeeBps": "8",
		"state": state,
		"longLeg": map[string]any{
			"accountId": "account-long", "instrumentId": "binance:demo:perp:USDT:BTC-USDT",
			"venue": "binance", "venueEnvironment": "demo", "symbol": "BTC-USDT",
		},
		"shortLeg": map[string]any{
			"accountId": "account-short", "instrumentId": "okx:testnet:perp:USDT:BTC-USDT",
			"venue": "okx", "venueEnvironment": "testnet", "symbol": "BTC-USDT",
		},
	})
}

func projectedPositionJSON(t *testing.T) json.RawMessage {
	t.Helper()
	store := &projectionCaptureStore{}
	projector := executionprojection.NewProjector(store, "boot-projection")
	scope := readmodel.Scope{PrincipalID: "principal-default", Mode: "test"}
	at := sourceNow.Add(-2 * time.Second)
	item := execution.Execution{
		ID:                   "execution-open",
		PrincipalID:          scope.PrincipalID,
		Mode:                 scope.Mode,
		Operation:            execution.OperationOpen,
		OpportunityID:        "opp-1",
		LineageID:            "lineage-open",
		State:                execution.StateHedged,
		CreatedAt:            at,
		UpdatedAt:            at,
		CapitalUSDT:          decimal.NewFromInt(5000),
		Leverage:             2,
		ExpectedNetAprPct:    decimal.RequireFromString("18.4"),
		InitialLongQuantity:  decimal.RequireFromString("0.12"),
		InitialShortQuantity: decimal.RequireFromString("0.10"),
		DirectionalExposure:  decimal.Zero,
		PlannedLegs: []execution.PlannedLeg{
			{
				LegIdentity: execution.LegIdentity{
					AccountID: "account-long", Venue: "binance",
					InstrumentID: "binance:demo:perp:USDT:BTC-USDT", VenueEnvironment: "demo",
				},
				Role: execution.LegRoleLong, Side: execution.SideBuy, Symbol: "BTC-USDT",
				GatewayInstrumentID: "binance:demo:perp:USDT:BTC-USDT",
				Quantity:            decimal.RequireFromString("0.12"),
			},
			{
				LegIdentity: execution.LegIdentity{
					AccountID: "account-short", Venue: "okx",
					InstrumentID: "okx:testnet:perp:USDT:BTC-USDT", VenueEnvironment: "testnet",
				},
				Role: execution.LegRoleShort, Side: execution.SideSell, Symbol: "BTC-USDT",
				GatewayInstrumentID: "okx:testnet:perp:USDT:BTC-USDT",
				Quantity:            decimal.RequireFromString("0.10"),
			},
		},
		Legs: []execution.Leg{
			{
				LegIdentity: execution.LegIdentity{
					AccountID: "account-long", Venue: "binance",
					InstrumentID: "binance:demo:perp:USDT:BTC-USDT", VenueEnvironment: "demo",
				},
				Role: execution.LegRoleLong, Filled: decimal.RequireFromString("0.12"),
			},
			{
				LegIdentity: execution.LegIdentity{
					AccountID: "account-short", Venue: "okx",
					InstrumentID: "okx:testnet:perp:USDT:BTC-USDT", VenueEnvironment: "testnet",
				},
				Role: execution.LegRoleShort, Filled: decimal.RequireFromString("0.10"),
			},
		},
	}
	if err := projector.Project(context.Background(), scope, item); err != nil {
		t.Fatalf("project open execution: %v", err)
	}
	return append(json.RawMessage(nil), store.position...)
}

func readyGatewayFixture() *fakeGateway {
	longRules := catalog.Rules{
		QuantityIncrement: decimal.RequireFromString("0.001"),
		MinQuantity:       decimal.RequireFromString("0.001"),
		MinNotional:       exchange.OptionalDecimal{Value: decimal.NewFromInt(10), Valid: true},
	}
	shortRules := catalog.Rules{
		QuantityIncrement: decimal.RequireFromString("0.01"),
		MinQuantity:       decimal.RequireFromString("0.01"),
		MinNotional:       exchange.OptionalDecimal{Value: decimal.NewFromInt(10), Valid: true},
	}
	capability := boltertrader.CapabilityRow{
		Operations: boltertrader.CapabilityOperations{
			Place: true, Cancel: true, OpenOrders: true, OrderHistory: true, Fills: true,
			Balances: true, Positions: true, Account: true, Funding: true,
			AccountFundingPayments: true, PrivateStream: true,
		},
	}
	return &fakeGateway{
		discovery: boltertrader.AccountDiscovery{
			Mode: config.Test, Mixed: true, Ready: true,
			Accounts: []boltertrader.DiscoveredAccount{
				{AccountID: "account-long", Venue: "binance", Product: "perp", VenueEnvironment: boltertrader.GatewayDemo, Settlement: "USDT", ReadReady: true, WriteReady: true, Capability: capability},
				{AccountID: "account-short", Venue: "okx", Product: "perp", VenueEnvironment: boltertrader.GatewayTestnet, Settlement: "USDT", ReadReady: true, WriteReady: true, Capability: capability},
			},
		},
		instruments: map[string][]boltertrader.InstrumentResource{
			"account-long": {{
				InstrumentID: "binance:demo:perp:USDT:BTC-USDT", Venue: "binance",
				VenueEnvironment: boltertrader.GatewayDemo, Product: "perp", Settlement: "USDT",
				Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: longRules,
			}},
			"account-short": {{
				InstrumentID: "okx:testnet:perp:USDT:BTC-USDT", Venue: "okx",
				VenueEnvironment: boltertrader.GatewayTestnet, Product: "perp", Settlement: "USDT",
				Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: shortRules,
			}},
		},
		balances: map[string][]boltertrader.BalanceResource{
			"account-long":  {{AccountID: "account-long", Balance: exchange.Balance{Asset: "USDT", Available: decimal.NewFromInt(9000)}}},
			"account-short": {{AccountID: "account-short", Balance: exchange.Balance{Asset: "USDT", Available: decimal.NewFromInt(7000)}}},
		},
		risk: map[string]boltertrader.RiskLimitReference{
			"account-long":  {Available: false, Reason: "risk_limits_not_normalized"},
			"account-short": {Available: false, Reason: "risk_limits_not_normalized"},
		},
		positions: make(map[string][]boltertrader.PositionResource),
	}
}

func closeMarketFixture() *fakeMarket {
	long := marketdata.Instrument{
		AccountID: "account-long", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
		Venue: "binance", VenueEnvironment: "demo", Product: "perp", Settlement: "USDT",
		Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
		Rules: marketdata.Rules{
			QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity:       decimal.RequireFromString("0.001"),
			MinNotional:       marketdata.OptionalDecimal{Value: decimal.NewFromInt(10), Valid: true},
		},
	}
	short := marketdata.Instrument{
		AccountID: "account-short", InstrumentID: "okx:testnet:perp:USDT:BTC-USDT",
		Venue: "okx", VenueEnvironment: "testnet", Product: "perp", Settlement: "USDT",
		Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
		Rules: marketdata.Rules{
			QuantityIncrement: decimal.RequireFromString("0.01"),
			MinQuantity:       decimal.RequireFromString("0.01"),
			MinNotional:       marketdata.OptionalDecimal{Value: decimal.NewFromInt(10), Valid: true},
		},
	}
	return &fakeMarket{
		instruments: []marketdata.Instrument{long, short},
		marks: map[string]marketdata.MarkPrice{
			long.Key():  {Instrument: long, Price: decimal.NewFromInt(80000), ObservedAt: sourceNow.Add(-time.Second)},
			short.Key(): {Instrument: short, Price: decimal.NewFromInt(80010), ObservedAt: sourceNow.Add(-time.Second)},
		},
		bbos: map[string]marketdata.BBO{
			long.Key(): {
				Instrument: long, Bid: marketdata.Level{Price: decimal.NewFromInt(79990), Quantity: decimal.NewFromInt(1)},
				Ask: marketdata.Level{Price: decimal.NewFromInt(80010), Quantity: decimal.NewFromInt(1)}, ObservedAt: sourceNow.Add(-time.Second),
			},
			short.Key(): {
				Instrument: short, Bid: marketdata.Level{Price: decimal.NewFromInt(80000), Quantity: decimal.NewFromInt(1)},
				Ask: marketdata.Level{Price: decimal.NewFromInt(80020), Quantity: decimal.NewFromInt(1)}, ObservedAt: sourceNow.Add(-time.Second),
			},
		},
		books: map[string]marketdata.OrderBook{
			long.Key(): {
				Instrument: long, Bids: []marketdata.Level{{Price: decimal.NewFromInt(79990), Quantity: decimal.NewFromInt(2)}},
				Asks: []marketdata.Level{{Price: decimal.NewFromInt(80010), Quantity: decimal.NewFromInt(2)}}, ObservedAt: sourceNow.Add(-time.Second),
			},
			short.Key(): {
				Instrument: short, Bids: []marketdata.Level{{Price: decimal.NewFromInt(80000), Quantity: decimal.NewFromInt(2)}},
				Asks: []marketdata.Level{{Price: decimal.NewFromInt(80020), Quantity: decimal.NewFromInt(2)}}, ObservedAt: sourceNow.Add(-time.Second),
			},
		},
	}
}

type fakeReader struct {
	opportunity json.RawMessage
	position    json.RawMessage
	err         error
}

type projectionCaptureStore struct {
	position json.RawMessage
}

func (store *projectionCaptureStore) ListExecutions(context.Context, readmodel.Scope) ([]execution.Execution, error) {
	return nil, nil
}

func (store *projectionCaptureStore) ApplyExecutionProjection(
	_ context.Context,
	_ string,
	position readmodel.PositionProjection,
	_ readmodel.ActivityProjection,
) ([]readmodel.Event, error) {
	store.position = append(json.RawMessage(nil), position.Payload...)
	return nil, nil
}

func (reader *fakeReader) Opportunity(context.Context, readmodel.Scope, string) (json.RawMessage, error) {
	return reader.opportunity, reader.err
}

func (reader *fakeReader) Position(context.Context, readmodel.Scope, string) (json.RawMessage, error) {
	return reader.position, reader.err
}

type fakeRefresher struct {
	err               error
	fresh             json.RawMessage
	freshErr          error
	refreshCalls      int
	freshCalls        int
	longAccountID     string
	longInstrumentID  string
	shortAccountID    string
	shortInstrumentID string
}

func (refresher *fakeRefresher) Refresh(context.Context) error {
	refresher.refreshCalls++
	return refresher.err
}

func (refresher *fakeRefresher) FreshOpportunity(
	_ context.Context,
	longAccountID string,
	longInstrumentID string,
	shortAccountID string,
	shortInstrumentID string,
) (json.RawMessage, error) {
	refresher.freshCalls++
	refresher.longAccountID = longAccountID
	refresher.longInstrumentID = longInstrumentID
	refresher.shortAccountID = shortAccountID
	refresher.shortInstrumentID = shortInstrumentID
	if refresher.freshErr != nil {
		return nil, refresher.freshErr
	}
	if refresher.fresh == nil {
		return defaultOpenOpportunityJSON(), nil
	}
	return append(json.RawMessage(nil), refresher.fresh...), nil
}

type fakeGateway struct {
	discovery      boltertrader.AccountDiscovery
	discoveryErr   error
	discoveryCalls int
	instruments    map[string][]boltertrader.InstrumentResource
	balances       map[string][]boltertrader.BalanceResource
	risk           map[string]boltertrader.RiskLimitReference
	positions      map[string][]boltertrader.PositionResource
	writeCalls     int
}

func (gateway *fakeGateway) DiscoverAccounts(context.Context) (boltertrader.AccountDiscovery, error) {
	gateway.discoveryCalls++
	return gateway.discovery, gateway.discoveryErr
}

func (gateway *fakeGateway) ListInstruments(_ context.Context, accountID string) ([]boltertrader.InstrumentResource, error) {
	return gateway.instruments[accountID], nil
}

func (gateway *fakeGateway) Balances(_ context.Context, accountID string) ([]boltertrader.BalanceResource, error) {
	return gateway.balances[accountID], nil
}

func (gateway *fakeGateway) RiskLimits(_ context.Context, accountID string) (boltertrader.RiskLimitReference, error) {
	return gateway.risk[accountID], nil
}

func (gateway *fakeGateway) Positions(_ context.Context, accountID, _ string) ([]boltertrader.PositionResource, error) {
	return gateway.positions[accountID], nil
}

type positionTrackingGateway struct {
	*fakeGateway
	positionsChecked *bool
}

func (gateway *positionTrackingGateway) Positions(ctx context.Context, accountID, instrumentID string) ([]boltertrader.PositionResource, error) {
	positions, err := gateway.fakeGateway.Positions(ctx, accountID, instrumentID)
	if accountID == "account-short" {
		*gateway.positionsChecked = true
	}
	return positions, err
}

type fakeMarket struct {
	instruments []marketdata.Instrument
	marks       map[string]marketdata.MarkPrice
	bbos        map[string]marketdata.BBO
	books       map[string]marketdata.OrderBook
	writeCalls  int
}

func (market *fakeMarket) Instruments(context.Context) ([]marketdata.Instrument, error) {
	return market.instruments, nil
}

func (market *fakeMarket) MarkPrice(_ context.Context, instrument marketdata.Instrument) (marketdata.MarkPrice, error) {
	return market.marks[instrument.Key()], nil
}

func (market *fakeMarket) BBO(_ context.Context, instrument marketdata.Instrument) (marketdata.BBO, error) {
	return market.bbos[instrument.Key()], nil
}

func (market *fakeMarket) OrderBook(_ context.Context, request marketdata.OrderBookRequest) (marketdata.OrderBook, error) {
	return market.books[request.Instrument.Key()], nil
}

type positionAwareMarket struct {
	*fakeMarket
	positionsChecked       *bool
	evidenceBeforePosition bool
}

func (market *positionAwareMarket) MarkPrice(ctx context.Context, instrument marketdata.Instrument) (marketdata.MarkPrice, error) {
	if !*market.positionsChecked {
		market.evidenceBeforePosition = true
	}
	return market.fakeMarket.MarkPrice(ctx, instrument)
}

func (market *positionAwareMarket) BBO(ctx context.Context, instrument marketdata.Instrument) (marketdata.BBO, error) {
	if !*market.positionsChecked {
		market.evidenceBeforePosition = true
	}
	return market.fakeMarket.BBO(ctx, instrument)
}

func (market *positionAwareMarket) OrderBook(ctx context.Context, request marketdata.OrderBookRequest) (marketdata.OrderBook, error) {
	if !*market.positionsChecked {
		market.evidenceBeforePosition = true
	}
	return market.fakeMarket.OrderBook(ctx, request)
}

type concurrentEvidenceMarket struct {
	*fakeMarket
	mu      sync.Mutex
	count   int
	max     int
	release chan struct{}
}

func newConcurrentEvidenceMarket(base *fakeMarket) *concurrentEvidenceMarket {
	return &concurrentEvidenceMarket{fakeMarket: base, release: make(chan struct{})}
}

func (market *concurrentEvidenceMarket) enterEvidence(ctx context.Context) error {
	market.mu.Lock()
	market.count++
	if market.count > market.max {
		market.max = market.count
	}
	if market.count == 3 {
		close(market.release)
	}
	release := market.release
	market.mu.Unlock()

	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return errors.New("market evidence was read serially")
	}
}

func (market *concurrentEvidenceMarket) maxConcurrent() int {
	market.mu.Lock()
	defer market.mu.Unlock()
	return market.max
}

func (market *concurrentEvidenceMarket) MarkPrice(ctx context.Context, instrument marketdata.Instrument) (marketdata.MarkPrice, error) {
	if err := market.enterEvidence(ctx); err != nil {
		return marketdata.MarkPrice{}, err
	}
	return market.fakeMarket.MarkPrice(ctx, instrument)
}

func (market *concurrentEvidenceMarket) BBO(ctx context.Context, instrument marketdata.Instrument) (marketdata.BBO, error) {
	if err := market.enterEvidence(ctx); err != nil {
		return marketdata.BBO{}, err
	}
	return market.fakeMarket.BBO(ctx, instrument)
}

func (market *concurrentEvidenceMarket) OrderBook(ctx context.Context, request marketdata.OrderBookRequest) (marketdata.OrderBook, error) {
	if err := market.enterEvidence(ctx); err != nil {
		return marketdata.OrderBook{}, err
	}
	return market.fakeMarket.OrderBook(ctx, request)
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func hasReason(findings []execution.Finding, code execution.ReasonCode) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

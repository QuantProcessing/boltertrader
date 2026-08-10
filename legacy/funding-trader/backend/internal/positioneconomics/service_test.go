package positioneconomics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/shopspring/decimal"
)

func TestServiceEnrichesCurrentPositionAndPortfolioFromGateway(t *testing.T) {
	now := time.Date(2026, 8, 3, 5, 0, 0, 0, time.UTC)
	reader := &fakeReader{positions: readmodel.Page{Items: []json.RawMessage{openPositionPayload()}}}
	gateway := &fakeGateway{positions: map[string][]boltertrader.PositionResource{
		"binance-demo-usdt-perp/binance:demo:perp:USDT:BTC-USDT": {{
			AccountID: "binance-demo-usdt-perp", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{
				Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.001"),
				EntryPrice: decimal.RequireFromString("62909.8"), MarkPrice: decimal.RequireFromString("62938"),
				UnrealizedPnL:    decimal.RequireFromString("0.0282"),
				LiquidationPrice: exchange.OptionalDecimal{Value: decimal.Zero, Valid: true},
			},
		}},
		"bybit-demo-usdt-perp/bybit:demo:perp:USDT:BTC-USDT": {{
			AccountID: "bybit-demo-usdt-perp", InstrumentID: "bybit:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{
				Side: exchange.SideSell, Quantity: decimal.RequireFromString("0.001"),
				EntryPrice: decimal.RequireFromString("62906.9"), UnrealizedPnL: decimal.RequireFromString("-0.0152"),
			},
		}},
	}}
	service, err := New(ServiceOptions{Reader: reader, Gateway: gateway, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	page, err := service.ListPositions(context.Background(), readmodel.Scope{PrincipalID: "principal-default", Mode: "test"}, readmodel.PositionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("positions = %d", len(page.Items))
	}
	var enriched struct {
		UnrealizedPnlUSD   string `json:"unrealizedPnlUsd"`
		HedgeDeltaPct      string `json:"hedgeDeltaPct"`
		LiveEconomicsAsOf  string `json:"liveEconomicsAsOf"`
		LiveEconomicsState string `json:"liveEconomicsStatus"`
		LongLeg            struct {
			CurrentQuantity string `json:"currentQuantity"`
			EntryPrice      string `json:"entryPrice"`
			MarkPrice       string `json:"markPrice"`
			UnrealizedPnl   string `json:"unrealizedPnl"`
		} `json:"longLeg"`
		ShortLeg struct {
			CurrentQuantity string `json:"currentQuantity"`
			EntryPrice      string `json:"entryPrice"`
			UnrealizedPnl   string `json:"unrealizedPnl"`
		} `json:"shortLeg"`
	}
	if err := json.Unmarshal(page.Items[0], &enriched); err != nil {
		t.Fatal(err)
	}
	if enriched.UnrealizedPnlUSD != "0.013" || enriched.HedgeDeltaPct != "0" ||
		enriched.LiveEconomicsState != "available" || enriched.LiveEconomicsAsOf != now.Format(time.RFC3339Nano) {
		t.Fatalf("enriched position = %+v", enriched)
	}
	if enriched.LongLeg.CurrentQuantity != "0.001" || enriched.LongLeg.EntryPrice != "62909.8" ||
		enriched.LongLeg.MarkPrice != "62938" || enriched.LongLeg.UnrealizedPnl != "0.0282" ||
		enriched.ShortLeg.CurrentQuantity != "0.001" || enriched.ShortLeg.EntryPrice != "62906.9" ||
		enriched.ShortLeg.UnrealizedPnl != "-0.0152" {
		t.Fatalf("authoritative leg economics missing: %+v", enriched)
	}

	portfolio, err := service.Portfolio(context.Background(), readmodel.Scope{PrincipalID: "principal-default", Mode: "test"}, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	if !portfolio.Available || portfolio.OpenPositionCount != 1 || !portfolio.UnrealizedPnl.Equal(decimal.RequireFromString("0.013")) || !portfolio.AsOf.Equal(now) {
		t.Fatalf("portfolio = %+v", portfolio)
	}
}

func TestServiceMarksLiveEconomicsUnavailableWhenExpectedLegIsMissing(t *testing.T) {
	reader := &fakeReader{positions: readmodel.Page{Items: []json.RawMessage{openPositionPayload()}}}
	gateway := &fakeGateway{positions: map[string][]boltertrader.PositionResource{
		"binance-demo-usdt-perp/binance:demo:perp:USDT:BTC-USDT": {{
			AccountID: "binance-demo-usdt-perp", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.001"), UnrealizedPnL: decimal.RequireFromString("0.02")},
		}},
	}}
	service, err := New(ServiceOptions{Reader: reader, Gateway: gateway})
	if err != nil {
		t.Fatal(err)
	}

	page, err := service.ListPositions(context.Background(), readmodel.Scope{PrincipalID: "principal-default", Mode: "test"}, readmodel.PositionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var enriched map[string]any
	if err := json.Unmarshal(page.Items[0], &enriched); err != nil {
		t.Fatal(err)
	}
	if enriched["liveEconomicsStatus"] != "incomplete" {
		t.Fatalf("status = %#v", enriched["liveEconomicsStatus"])
	}
	if _, exists := enriched["unrealizedPnlUsd"]; exists {
		t.Fatalf("missing leg fabricated pnl: %s", page.Items[0])
	}

	portfolio, err := service.Portfolio(context.Background(), readmodel.Scope{PrincipalID: "principal-default", Mode: "test"}, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	if portfolio.Available || portfolio.OpenPositionCount != 1 {
		t.Fatalf("portfolio = %+v", portfolio)
	}
}

func TestServiceSnapshotExcludesClosedPositionsFromCurrentPositionStream(t *testing.T) {
	reader := &fakeReader{positions: readmodel.Page{Items: []json.RawMessage{
		openPositionPayload(),
		json.RawMessage(`{"positionId":"position-closed","status":"closed","state":"closed"}`),
	}}}
	gateway := &fakeGateway{positions: map[string][]boltertrader.PositionResource{
		"binance-demo-usdt-perp/binance:demo:perp:USDT:BTC-USDT": {{
			AccountID: "binance-demo-usdt-perp", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.001")},
		}},
		"bybit-demo-usdt-perp/bybit:demo:perp:USDT:BTC-USDT": {{
			AccountID: "bybit-demo-usdt-perp", InstrumentID: "bybit:demo:perp:USDT:BTC-USDT",
			Position: exchange.Position{Side: exchange.SideSell, Quantity: decimal.RequireFromString("0.001")},
		}},
	}}
	service, err := New(ServiceOptions{Reader: reader, Gateway: gateway})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := service.Snapshot(context.Background(), readmodel.Scope{PrincipalID: "principal-default", Mode: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Positions) != 1 {
		t.Fatalf("current position snapshot includes closed rows: %s", snapshot.Positions)
	}
	var position struct {
		PositionID string `json:"positionId"`
	}
	if err := json.Unmarshal(snapshot.Positions[0], &position); err != nil {
		t.Fatal(err)
	}
	if position.PositionID != "position-exec-1" {
		t.Fatalf("snapshot position = %q", position.PositionID)
	}
}

func openPositionPayload() json.RawMessage {
	return json.RawMessage(`{
		"positionId":"position-exec-1",
		"status":"open",
		"state":"hedged",
		"longLeg":{"accountId":"binance-demo-usdt-perp","instrumentId":"binance:demo:perp:USDT:BTC-USDT","currentQuantity":"0.001"},
		"shortLeg":{"accountId":"bybit-demo-usdt-perp","instrumentId":"bybit:demo:perp:USDT:BTC-USDT","currentQuantity":"0.001"}
	}`)
}

type fakeGateway struct {
	positions map[string][]boltertrader.PositionResource
}

func (gateway *fakeGateway) Positions(_ context.Context, accountID, instrumentID string) ([]boltertrader.PositionResource, error) {
	return append([]boltertrader.PositionResource(nil), gateway.positions[accountID+"/"+instrumentID]...), nil
}

type fakeReader struct {
	positions readmodel.Page
}

func (reader *fakeReader) ListOpportunities(context.Context, readmodel.Scope, readmodel.OpportunityQuery) (readmodel.Page, error) {
	return readmodel.Page{}, nil
}

func (reader *fakeReader) Opportunity(context.Context, readmodel.Scope, string) (json.RawMessage, error) {
	return nil, readmodel.ErrNotFound
}

func (reader *fakeReader) ListAlerts(context.Context, readmodel.Scope, readmodel.AlertQuery) (readmodel.Page, error) {
	return readmodel.Page{}, nil
}

func (reader *fakeReader) ListPositions(context.Context, readmodel.Scope, readmodel.PositionQuery) (readmodel.Page, error) {
	return reader.positions, nil
}

func (reader *fakeReader) ListActivities(context.Context, readmodel.Scope, readmodel.ActivityQuery) (readmodel.Page, error) {
	return readmodel.Page{}, nil
}

func (reader *fakeReader) ListAuditEvents(context.Context, readmodel.Scope, readmodel.AuditQuery) (readmodel.Page, error) {
	return readmodel.Page{}, nil
}

func (reader *fakeReader) Snapshot(context.Context, readmodel.Scope, []readmodel.EventKind) (readmodel.Snapshot, error) {
	return readmodel.Snapshot{Positions: reader.positions.Items}, nil
}

package positioneconomics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/shopspring/decimal"
)

const portfolioPageLimit = 200

type Gateway interface {
	Positions(context.Context, string, string) ([]boltertrader.PositionResource, error)
}

type ServiceOptions struct {
	Reader         readmodel.Reader
	Gateway        Gateway
	ReportingAsset string
	Now            func() time.Time
}

type Service struct {
	reader         readmodel.Reader
	gateway        Gateway
	reportingAsset string
	now            func() time.Time
}

type PortfolioSnapshot struct {
	UnrealizedPnl     decimal.Decimal
	OpenPositionCount int
	AsOf              time.Time
	Available         bool
}

type projectedPosition struct {
	Status   string       `json:"status"`
	State    string       `json:"state"`
	LongLeg  projectedLeg `json:"longLeg"`
	ShortLeg projectedLeg `json:"shortLeg"`
}

type projectedLeg struct {
	AccountID       string          `json:"accountId"`
	InstrumentID    string          `json:"instrumentId"`
	CurrentQuantity decimal.Decimal `json:"currentQuantity"`
}

type liveLeg struct {
	quantity         decimal.Decimal
	entryPrice       decimal.Decimal
	markPrice        decimal.Decimal
	unrealizedPnl    decimal.Decimal
	liquidationPrice decimal.Decimal
	liquidationValid bool
}

type lookupResult struct {
	leg liveLeg
	ok  bool
}

func New(options ServiceOptions) (*Service, error) {
	if options.Reader == nil || options.Gateway == nil {
		return nil, errors.New("position economics dependencies are required")
	}
	asset := strings.TrimSpace(options.ReportingAsset)
	if asset == "" {
		asset = "USDT"
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Service{reader: options.Reader, gateway: options.Gateway, reportingAsset: asset, now: now}, nil
}

func (service *Service) ListOpportunities(ctx context.Context, scope readmodel.Scope, query readmodel.OpportunityQuery) (readmodel.Page, error) {
	return service.reader.ListOpportunities(ctx, scope, query)
}

func (service *Service) Opportunity(ctx context.Context, scope readmodel.Scope, id string) (json.RawMessage, error) {
	return service.reader.Opportunity(ctx, scope, id)
}

func (service *Service) ListAlerts(ctx context.Context, scope readmodel.Scope, query readmodel.AlertQuery) (readmodel.Page, error) {
	return service.reader.ListAlerts(ctx, scope, query)
}

func (service *Service) ListPositions(ctx context.Context, scope readmodel.Scope, query readmodel.PositionQuery) (readmodel.Page, error) {
	page, err := service.reader.ListPositions(ctx, scope, query)
	if err != nil {
		return readmodel.Page{}, err
	}
	items, _ := service.enrich(ctx, page.Items, service.reportingAsset)
	page.Items = items
	if len(items) > 0 {
		page.AsOf = service.now().UTC()
	}
	return page, nil
}

func (service *Service) ListActivities(ctx context.Context, scope readmodel.Scope, query readmodel.ActivityQuery) (readmodel.Page, error) {
	return service.reader.ListActivities(ctx, scope, query)
}

func (service *Service) ListAuditEvents(ctx context.Context, scope readmodel.Scope, query readmodel.AuditQuery) (readmodel.Page, error) {
	return service.reader.ListAuditEvents(ctx, scope, query)
}

func (service *Service) Snapshot(ctx context.Context, scope readmodel.Scope, kinds []readmodel.EventKind) (readmodel.Snapshot, error) {
	snapshot, err := service.reader.Snapshot(ctx, scope, kinds)
	if err != nil {
		return readmodel.Snapshot{}, err
	}
	snapshot.Positions, _ = service.enrich(ctx, currentPositionItems(snapshot.Positions), service.reportingAsset)
	if len(snapshot.Positions) > 0 {
		snapshot.AsOf = service.now().UTC()
	}
	return snapshot, nil
}

func currentPositionItems(items []json.RawMessage) []json.RawMessage {
	current := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var position projectedPosition
		if err := json.Unmarshal(item, &position); err == nil && activeStatus(position.Status) {
			current = append(current, item)
		}
	}
	return current
}

func (service *Service) Portfolio(ctx context.Context, scope readmodel.Scope, asset string) (PortfolioSnapshot, error) {
	asset = strings.TrimSpace(asset)
	if asset == "" {
		asset = service.reportingAsset
	}
	query := readmodel.PositionQuery{
		PageQuery: readmodel.PageQuery{Limit: portfolioPageLimit},
		States:    []string{"open", "attention"},
	}
	seen := make(map[string]struct{})
	var items []json.RawMessage
	for {
		page, err := service.reader.ListPositions(ctx, scope, query)
		if err != nil {
			return PortfolioSnapshot{}, err
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			break
		}
		if _, exists := seen[page.NextCursor]; exists {
			return PortfolioSnapshot{}, errors.New("position economics pagination cursor cycle")
		}
		seen[page.NextCursor] = struct{}{}
		query.Cursor = page.NextCursor
	}
	_, snapshot := service.enrich(ctx, items, asset)
	return snapshot, nil
}

func (service *Service) enrich(ctx context.Context, items []json.RawMessage, asset string) ([]json.RawMessage, PortfolioSnapshot) {
	now := service.now().UTC()
	snapshot := PortfolioSnapshot{AsOf: now, Available: true}
	result := make([]json.RawMessage, 0, len(items))
	cache := make(map[string]lookupResult)
	for _, item := range items {
		enriched, position, live, active := service.enrichPosition(ctx, item, asset, cache, now)
		result = append(result, enriched)
		if !active {
			continue
		}
		snapshot.OpenPositionCount++
		if !live {
			snapshot.Available = false
			continue
		}
		snapshot.UnrealizedPnl = snapshot.UnrealizedPnl.Add(position)
	}
	return result, snapshot
}

func (service *Service) enrichPosition(
	ctx context.Context,
	raw json.RawMessage,
	asset string,
	cache map[string]lookupResult,
	asOf time.Time,
) (json.RawMessage, decimal.Decimal, bool, bool) {
	var position projectedPosition
	if err := json.Unmarshal(raw, &position); err != nil || !activeStatus(position.Status) {
		return append(json.RawMessage(nil), raw...), decimal.Zero, false, false
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return append(json.RawMessage(nil), raw...), decimal.Zero, false, true
	}
	long, longOK := service.authoritativeLeg(ctx, position.LongLeg, exchange.SideBuy, cache)
	short, shortOK := service.authoritativeLeg(ctx, position.ShortLeg, exchange.SideSell, cache)
	settlementOK := settlementAsset(position.LongLeg.InstrumentID) == asset && settlementAsset(position.ShortLeg.InstrumentID) == asset
	available := longOK && shortOK && settlementOK
	payload["liveEconomicsAsOf"] = asOf.Format(time.RFC3339Nano)
	payload["liveEconomicsStatus"] = "incomplete"
	payload["reportingAsset"] = asset
	if available {
		payload["liveEconomicsStatus"] = "available"
		pnl := long.unrealizedPnl.Add(short.unrealizedPnl)
		payload["unrealizedPnlUsd"] = pnl.String()
		payload["hedgeDeltaPct"] = hedgeDeltaPct(long.quantity, short.quantity).String()
		if buffer, ok := minimumLiquidationBuffer(long, short); ok {
			payload["liquidationBufferPct"] = buffer.String()
		}
		setLegEconomics(payload, "longLeg", long)
		setLegEconomics(payload, "shortLeg", short)
		return marshalPayload(raw, payload), pnl, true, true
	}
	delete(payload, "unrealizedPnlUsd")
	delete(payload, "hedgeDeltaPct")
	delete(payload, "liquidationBufferPct")
	payload["requiresAttention"] = true
	payload["status"] = "attention"
	return marshalPayload(raw, payload), decimal.Zero, false, true
}

func (service *Service) authoritativeLeg(ctx context.Context, leg projectedLeg, side exchange.Side, cache map[string]lookupResult) (liveLeg, bool) {
	accountID := strings.TrimSpace(leg.AccountID)
	instrumentID := strings.TrimSpace(leg.InstrumentID)
	if accountID == "" || instrumentID == "" {
		return liveLeg{}, false
	}
	key := accountID + "/" + instrumentID + "/" + string(side)
	if cached, ok := cache[key]; ok {
		return cached.leg, cached.ok
	}
	resources, err := service.gateway.Positions(ctx, accountID, instrumentID)
	if err != nil {
		cache[key] = lookupResult{}
		return liveLeg{}, false
	}
	var result liveLeg
	found := false
	for _, resource := range resources {
		if resource.AccountID != accountID || resource.InstrumentID != instrumentID || resource.Position.Side != side || !resource.Position.Quantity.Abs().IsPositive() {
			continue
		}
		if found {
			cache[key] = lookupResult{}
			return liveLeg{}, false
		}
		found = true
		result = liveLeg{
			quantity:         resource.Position.Quantity.Abs(),
			entryPrice:       resource.Position.EntryPrice,
			markPrice:        resource.Position.MarkPrice,
			unrealizedPnl:    resource.Position.UnrealizedPnL,
			liquidationPrice: resource.Position.LiquidationPrice.Value,
			liquidationValid: resource.Position.LiquidationPrice.Valid,
		}
	}
	cache[key] = lookupResult{leg: result, ok: found}
	return result, found
}

func setLegEconomics(payload map[string]any, name string, leg liveLeg) {
	raw, ok := payload[name].(map[string]any)
	if !ok {
		raw = make(map[string]any)
		payload[name] = raw
	}
	raw["currentQuantity"] = leg.quantity.String()
	raw["entryPrice"] = leg.entryPrice.String()
	raw["markPrice"] = leg.markPrice.String()
	raw["unrealizedPnl"] = leg.unrealizedPnl.String()
}

func activeStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open", "attention":
		return true
	default:
		return false
	}
}

func settlementAsset(instrumentID string) string {
	parts := strings.Split(strings.TrimSpace(instrumentID), ":")
	if len(parts) < 5 {
		return ""
	}
	return strings.TrimSpace(parts[3])
}

func hedgeDeltaPct(long, short decimal.Decimal) decimal.Decimal {
	long = long.Abs()
	short = short.Abs()
	maximum := decimal.Max(long, short)
	if maximum.IsZero() {
		return decimal.Zero
	}
	return long.Sub(short).Abs().Div(maximum).Mul(decimal.NewFromInt(100))
}

func minimumLiquidationBuffer(long, short liveLeg) (decimal.Decimal, bool) {
	longBuffer, longOK := liquidationBuffer(long, true)
	shortBuffer, shortOK := liquidationBuffer(short, false)
	if !longOK || !shortOK {
		return decimal.Zero, false
	}
	return decimal.Min(longBuffer, shortBuffer), true
}

func liquidationBuffer(leg liveLeg, long bool) (decimal.Decimal, bool) {
	if !leg.liquidationValid || !leg.markPrice.IsPositive() || !leg.liquidationPrice.IsPositive() {
		return decimal.Zero, false
	}
	buffer := leg.liquidationPrice.Sub(leg.markPrice)
	if long {
		buffer = leg.markPrice.Sub(leg.liquidationPrice)
	}
	if buffer.IsNegative() {
		return decimal.Zero, false
	}
	return buffer.Div(leg.markPrice).Mul(decimal.NewFromInt(100)), true
}

func marshalPayload(fallback json.RawMessage, payload map[string]any) json.RawMessage {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return append(json.RawMessage(nil), fallback...)
	}
	return encoded
}

func (snapshot PortfolioSnapshot) String() string {
	return fmt.Sprintf("available=%t open=%d upnl=%s asOf=%s", snapshot.Available, snapshot.OpenPositionCount, snapshot.UnrealizedPnl, snapshot.AsOf.Format(time.RFC3339Nano))
}

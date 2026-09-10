package accountpositions

import (
	"context"
	"fmt"
	"github.com/shopspring/decimal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Service reads current Gateway snapshots. It does not store positions, replay
// historical orders, or maintain a second account stream alongside Gateway.
type Service struct {
	source   Source
	now      func() time.Time
	revision atomic.Uint64
}

func NewService(source Source, now func() time.Time) (*Service, error) {
	if source == nil {
		return nil, fmt.Errorf("account position source is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{source: source, now: now}, nil
}
func (service *Service) Snapshot(ctx context.Context, principal string, filter Filter) (Snapshot, error) {
	if strings.TrimSpace(principal) == "" {
		return Snapshot{}, fmt.Errorf("login is required")
	}
	accounts, err := service.source.ListAccounts(ctx, principal)
	if err != nil {
		return Snapshot{}, err
	}
	selected := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if err := validateAccount(account); err != nil {
			return Snapshot{}, err
		}
		if filter.AccountID != "" && account.AccountID != filter.AccountID {
			continue
		}
		if filter.Venue != "" && account.Venue != filter.Venue {
			continue
		}
		selected = append(selected, account)
	}
	states := make([]AccountState, len(selected))
	positions := make([][]Item, len(selected))
	var wg sync.WaitGroup
	budget := make(chan struct{}, 4)
	for index, account := range selected {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state := AccountState{Account: account, Freshness: FreshnessUnavailable, RouteHealth: RouteUnavailable, ReceivedAt: service.now().UTC()}
			defer func() { states[index] = state }()
			select {
			case budget <- struct{}{}:
			case <-ctx.Done():
				state.Reason = "position_query_canceled"
				return
			}
			defer func() { <-budget }()
			snapshot, err := service.source.PositionSnapshot(ctx, principal, account.AccountID)
			if err != nil {
				state.Reason = "position_query_failed"
				return
			}
			if snapshot.AccountID != account.AccountID || snapshot.VerifiedAt.IsZero() || !snapshot.Freshness.Valid() {
				state.Reason = "invalid_position_snapshot"
				return
			}
			state.Freshness, state.VerifiedAt, state.Reason = snapshot.Freshness, snapshot.VerifiedAt, snapshot.Reason
			state.RouteHealth = routeHealthFor(state.Freshness)
			state.WriteReady = accountWriteReady(account) && state.Freshness == FreshnessLive
			items := []Item{}
			seen := map[string]bool{}
			for _, source := range snapshot.Items {
				item, err := projectSourceItem(account, state, source, service.now().UTC())
				if err != nil || seen[item.Key.String()] {
					state.Freshness = FreshnessUnavailable
					state.RouteHealth = RouteUnavailable
					state.WriteReady = false
					state.Reason = "invalid_position_snapshot"
					return
				}
				seen[item.Key.String()] = true
				if item.Quantity != "0" && matches(item, filter) {
					items = append(items, item)
				}
			}
			positions[index] = items
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	items := []Item{}
	for _, rows := range positions {
		items = append(items, rows...)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].AccountID < states[j].AccountID })
	sort.Slice(items, func(i, j int) bool { return items[i].Key.String() < items[j].Key.String() })
	return Snapshot{ProjectionRevision: service.revision.Add(1), VerifiedAt: snapshotVerifiedAt(states), Summary: summarize(states, items), Accounts: states, Items: items}, nil
}

func projectSourceItem(account Account, state AccountState, source SourceItem, receivedAt time.Time) (Item, error) {
	slot := source.PositionSlot
	if slot == "" {
		slot = source.Position.PositionSlot
	}
	key := Key{AccountID: source.AccountID, RouteID: source.RouteID, InstrumentID: source.InstrumentID, PositionSlot: slot}
	if source.AccountID != account.AccountID || !key.Valid() || source.ResourceVersion == 0 || source.ObservedAt.IsZero() || source.VerifiedAt.IsZero() {
		return Item{}, fmt.Errorf("Gateway position item identity is invalid")
	}
	quantity, err := decimal.NewFromString(source.Position.Quantity)
	if err != nil {
		return Item{}, fmt.Errorf("Gateway position quantity is invalid")
	}
	side := source.Position.Side
	if quantity.IsNegative() {
		side = "sell"
	} else if quantity.IsPositive() {
		side = "buy"
	}
	if side != "buy" && side != "sell" && !quantity.IsZero() {
		return Item{}, fmt.Errorf("Gateway position side is invalid")
	}
	for _, value := range []string{source.Position.EntryPrice, source.Position.MarkPrice, source.Position.UnrealizedPnL} {
		if _, err := decimal.NewFromString(zeroDecimal(value)); err != nil {
			return Item{}, fmt.Errorf("Gateway position decimal is invalid")
		}
	}
	for _, value := range []OptionalDecimal{source.Position.LiquidationPrice, source.Position.Leverage, source.Position.MarginUsed} {
		if value.Valid {
			parsed, parseErr := decimal.NewFromString(value.Value)
			if parseErr != nil || parsed.IsNegative() {
				return Item{}, fmt.Errorf("Gateway position optional decimal is invalid")
			}
		}
	}
	product, settlement, symbol := instrumentDisplay(source.InstrumentID)
	if route, ok := accountRoute(account, source.RouteID); ok {
		product, settlement = route.Product, route.SettlementAsset
	}
	item := Item{
		Key: key, Product: product, SettlementAsset: settlement, Symbol: symbol, Side: side,
		Quantity: quantity.String(), EntryPrice: emptyZero(source.Position.EntryPrice), MarkPrice: emptyZero(source.Position.MarkPrice),
		UnrealizedPnL: zeroDecimal(source.Position.UnrealizedPnL), UnrealizedPnLCurrency: settlement,
		UnrealizedPnLSource: "venue_position", MarkPriceSource: "unavailable",
		ResourceVersion: source.ResourceVersion,
		ObservedAt:      source.ObservedAt.UTC(), VerifiedAt: source.VerifiedAt.UTC(), ReceivedAt: receivedAt,
	}
	if item.MarkPrice != "" {
		item.MarkPriceSource = "venue_position"
	}
	if source.Position.LiquidationPrice.Valid {
		item.LiquidationPrice = source.Position.LiquidationPrice.Value
	}
	if source.Position.Leverage.Valid {
		item.Leverage = source.Position.Leverage.Value
	}
	if source.Position.MarginUsed.Valid {
		item.MarginUsed = source.Position.MarginUsed.Value
	}
	return decorate(item, account, state), nil
}

func decorate(item Item, account Account, state AccountState) Item {
	item.AccountLabel, item.Venue, item.Environment = account.Label, account.Venue, account.Environment
	item.RouteHealth, item.Freshness, item.WriteReady = state.RouteHealth, state.Freshness, state.WriteReady
	item.BlockedReason = state.Reason
	if !state.VerifiedAt.IsZero() {
		item.VerifiedAt = state.VerifiedAt
	}
	return item
}

func validateAccount(account Account) error {
	if strings.TrimSpace(account.AccountID) == "" || strings.TrimSpace(account.Venue) == "" || strings.TrimSpace(account.Environment) == "" {
		return fmt.Errorf("Gateway account directory response is invalid")
	}
	if account.Label == "" {
		account.Label = account.AccountID
	}
	seen := make(map[string]struct{}, len(account.Routes))
	for _, route := range account.Routes {
		if strings.TrimSpace(route.RouteID) == "" || strings.TrimSpace(route.Product) == "" {
			return fmt.Errorf("Gateway account route response is invalid")
		}
		if _, duplicate := seen[route.RouteID]; duplicate {
			return fmt.Errorf("Gateway account route response contains duplicate route")
		}
		seen[route.RouteID] = struct{}{}
	}
	return nil
}

func accountWriteReady(account Account) bool {
	return account.Status == "active" && len(account.Routes) > 0
}

func routeHealthFor(freshness Freshness) RouteHealth {
	switch freshness {
	case FreshnessLive:
		return RouteLive
	case FreshnessUnavailable:
		return RouteUnavailable
	default:
		return RouteDegraded
	}
}

func accountRoute(account Account, routeID string) (AccountRoute, bool) {
	for _, route := range account.Routes {
		if route.RouteID == routeID {
			return route, true
		}
	}
	return AccountRoute{}, false
}

func instrumentDisplay(instrumentID string) (product, settlement, symbol string) {
	parts := strings.Split(instrumentID, ":")
	if len(parts) >= 4 {
		product = parts[2]
		symbol = parts[len(parts)-1]
	}
	if len(parts) >= 5 {
		settlement = parts[len(parts)-2]
	}
	return product, settlement, symbol
}

func matches(item Item, filter Filter) bool {
	return (filter.AccountID == "" || item.AccountID == filter.AccountID) &&
		(filter.InstrumentID == "" || item.InstrumentID == filter.InstrumentID) &&
		(filter.Venue == "" || item.Venue == filter.Venue) &&
		(filter.Product == "" || item.Product == filter.Product) &&
		(filter.Status == "" || item.Freshness == filter.Status)
}

func snapshotVerifiedAt(accounts []AccountState) time.Time {
	var verified time.Time
	for _, state := range accounts {
		if state.VerifiedAt.IsZero() {
			continue
		}
		if verified.IsZero() || state.VerifiedAt.Before(verified) {
			verified = state.VerifiedAt
		}
	}
	return verified
}

func summarize(accounts []AccountState, items []Item) Summary {
	summary := Summary{PositionCount: len(items), AccountCount: len(accounts)}
	for _, state := range accounts {
		if state.Freshness == FreshnessStale || state.Freshness == FreshnessUnavailable {
			summary.StaleAccountCount++
		}
	}
	byAsset := make(map[string]*SettlementSummary)
	for _, item := range items {
		asset := item.SettlementAsset
		if asset == "" {
			asset = "UNKNOWN"
		}
		row := byAsset[asset]
		if row == nil {
			row = &SettlementSummary{SettlementAsset: asset}
			byAsset[asset] = row
		}
		row.PositionCount++
		if (item.Freshness == FreshnessLive || item.Freshness == FreshnessDelayed) && item.UnrealizedPnLSource != "unavailable" && item.UnrealizedPnL != "" {
			row.LiveCount++
			value, err := decimal.NewFromString(zeroDecimal(item.UnrealizedPnL))
			if err == nil {
				current, _ := decimal.NewFromString(zeroDecimal(row.UnrealizedPnL))
				row.UnrealizedPnL = current.Add(value).String()
			}
		} else {
			row.StaleCount++
			row.ExcludedPnLCount++
		}
	}
	assets := make([]string, 0, len(byAsset))
	for asset := range byAsset {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	for _, asset := range assets {
		summary.BySettlementAsset = append(summary.BySettlementAsset, *byAsset[asset])
	}
	return summary
}

func zeroDecimal(value string) string {
	if strings.TrimSpace(value) == "" {
		return "0"
	}
	return value
}

func emptyZero(value string) string {
	parsed, err := decimal.NewFromString(zeroDecimal(value))
	if err != nil || parsed.IsZero() {
		return ""
	}
	return parsed.String()
}

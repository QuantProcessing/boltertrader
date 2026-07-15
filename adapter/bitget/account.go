package bitget

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest               *bitgetsdk.Client
	provider           *instrumentProvider
	clk                clock.Clock
	accountID          string
	scope              []enums.InstrumentKind
	positionCategories []string
	stream             *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *bitgetsdk.Client, provider *instrumentProvider, clk clock.Clock, scope []enums.InstrumentKind, accountIDs ...string) *accountClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	accountID := ""
	if len(accountIDs) > 0 {
		accountID = accountIDs[0]
	}
	if accountID == "" {
		accountID = AccountIDUnified
	}
	accountScope, positionCategories := bitgetAccountScope(provider, scope)
	return &accountClient{
		rest:               rest,
		provider:           provider,
		clk:                clk,
		accountID:          accountID,
		scope:              accountScope,
		positionCategories: positionCategories,
		stream:             wsstream.New[contract.AccountEnvelope](256),
	}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	assets, err := c.rest.GetAccountAssets(ctx)
	if err != nil {
		return nil, err
	}
	return balancesFromAssets(assets, c.accountID, c.clk.Now()), nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	return c.positions(ctx)
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	settings, err := c.rest.GetAccountSettings(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	if !isUnifiedMode(settings.AccountMode) {
		return model.AccountState{}, fmt.Errorf("bitget: account mode %q is not phase-one unified/UTA: %w", settings.AccountMode, errs.ErrNotSupported)
	}
	assets, err := c.rest.GetAccountAssets(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	positions, err := c.positions(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	now := c.clk.Now()
	state := model.AccountState{
		AccountID:    c.accountID,
		Venue:        VenueName,
		Type:         model.AccountMargin,
		BaseCurrency: "USD",
		Balances:     balancesFromAssets(assets, c.accountID, now),
		Margins:      c.marginBalancesFromAssetsAndPositions(assets, positions, now),
		Reported:     true,
		EventID:      model.AccountStateEventID(VenueName, c.accountID, now),
		TsEvent:      now,
		TsInit:       now,
	}
	if len(state.Margins) == 0 {
		state.Margins = append(state.Margins, model.MarginBalance{Currency: "USDT", UpdatedAt: now})
	}
	return state, nil
}

func (c *accountClient) positions(ctx context.Context) ([]model.Position, error) {
	now := c.clk.Now()
	out := make([]model.Position, 0)
	for _, category := range c.positionCategories {
		records, err := c.rest.GetCurrentPositions(ctx, category, "")
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			id, ok := c.resolvePositionInstrument(category, record)
			if !ok {
				if bitgetPositionRecordExplicitlyFlat(record) {
					continue
				}
				return nil, fmt.Errorf(
					"bitget: unresolved account position requested_category=%s record_category=%s symbol=%s",
					normalizeVenueSymbol(category),
					normalizeVenueSymbol(record.Category),
					normalizeVenueSymbol(record.Symbol),
				)
			}
			quantity, err := bitgetAuthoritativePositionQuantity(record)
			if err != nil {
				return nil, fmt.Errorf(
					"bitget: invalid account position quantity category=%s symbol=%s: %w",
					normalizeVenueSymbol(category),
					normalizeVenueSymbol(record.Symbol),
					err,
				)
			}
			pos, err := positionFromBitget(record, func(string) model.InstrumentID { return id }, c.accountID, now)
			if err != nil {
				return nil, fmt.Errorf(
					"bitget: invalid account position semantics category=%s symbol=%s: %w",
					normalizeVenueSymbol(category),
					normalizeVenueSymbol(record.Symbol),
					err,
				)
			}
			pos.Quantity = quantity
			if pos.InstrumentID.Symbol == "" || pos.Quantity.IsZero() {
				continue
			}
			out = append(out, pos)
		}
	}
	return out, nil
}

func bitgetAuthoritativePositionQuantity(record bitgetsdk.PositionRecord) (decimal.Decimal, error) {
	for _, field := range []struct {
		name string
		raw  string
	}{
		{name: "qty", raw: record.Qty},
		{name: "total", raw: record.Total},
		{name: "size", raw: record.Size},
	} {
		raw := strings.TrimSpace(field.raw)
		if raw == "" {
			continue
		}
		quantity, err := decimal.NewFromString(raw)
		if err != nil {
			return decimal.Zero, fmt.Errorf("%s is not a decimal: %w", field.name, err)
		}
		if positionSideFromBitget(firstNonEmpty(record.PosSide, record.HoldSide)) == enums.PosShort {
			quantity = quantity.Neg()
		}
		return quantity, nil
	}
	return decimal.Zero, fmt.Errorf("qty, total, and size are all empty")
}

func bitgetPositionRecordExplicitlyFlat(record bitgetsdk.PositionRecord) bool {
	sawQuantity := false
	for _, raw := range []string{record.Qty, record.Total, record.Size} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		quantity, err := decimal.NewFromString(raw)
		if err != nil || !quantity.IsZero() {
			return false
		}
		sawQuantity = true
	}
	return sawQuantity
}

func bitgetAccountScope(provider *instrumentProvider, requested []enums.InstrumentKind) ([]enums.InstrumentKind, []string) {
	requested = bitgetKinds(requested)
	wantsSpot := hasBitgetAccountKind(requested, enums.KindSpot)
	wantsPerp := hasBitgetAccountKind(requested, enums.KindPerp)
	hasSpot := false
	categories := make(map[string]struct{}, 2)
	if provider != nil {
		for _, inst := range provider.All() {
			if inst == nil {
				continue
			}
			switch inst.ID.Kind {
			case enums.KindSpot:
				hasSpot = hasSpot || wantsSpot
			case enums.KindPerp:
				if !wantsPerp {
					continue
				}
				switch strings.ToUpper(strings.TrimSpace(inst.Settle)) {
				case "USDT":
					categories[bitgetsdk.ProductTypeUSDTFutures] = struct{}{}
				case "USDC":
					categories[bitgetsdk.ProductTypeUSDCFutures] = struct{}{}
				}
			}
		}
	}

	scope := make([]enums.InstrumentKind, 0, 2)
	if hasSpot {
		scope = append(scope, enums.KindSpot)
	}
	positionCategories := make([]string, 0, len(categories))
	for _, category := range []string{bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures} {
		if _, ok := categories[category]; ok {
			positionCategories = append(positionCategories, category)
		}
	}
	if len(positionCategories) > 0 {
		scope = append(scope, enums.KindPerp)
	}
	return scope, positionCategories
}

func hasBitgetAccountKind(kinds []enums.InstrumentKind, want enums.InstrumentKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func (c *accountClient) resolvePositionInstrument(category string, record bitgetsdk.PositionRecord) (model.InstrumentID, bool) {
	if c.provider == nil || strings.TrimSpace(record.Symbol) == "" {
		return model.InstrumentID{}, false
	}
	normalizedCategory := strings.ToUpper(strings.TrimSpace(category))
	if recordCategory := strings.ToUpper(strings.TrimSpace(record.Category)); recordCategory != "" && recordCategory != normalizedCategory {
		return model.InstrumentID{}, false
	}
	settle := ""
	switch normalizedCategory {
	case bitgetsdk.ProductTypeUSDTFutures:
		settle = "USDT"
	case bitgetsdk.ProductTypeUSDCFutures:
		settle = "USDC"
	default:
		return model.InstrumentID{}, false
	}
	id, ok := c.provider.ResolveVenueInstrument(record.Symbol, enums.KindPerp, settle)
	if !ok {
		return model.InstrumentID{}, false
	}
	return id, true
}

func balancesFromAssets(assets *bitgetsdk.AccountAssets, accountID string, now time.Time) []model.AccountBalance {
	if assets == nil {
		return nil
	}
	out := make([]model.AccountBalance, 0, len(assets.Assets))
	for _, asset := range assets.Assets {
		if strings.TrimSpace(asset.Coin) == "" {
			continue
		}
		free := dec(asset.Available)
		total := firstNonZero(dec(asset.Equity), free.Add(dec(asset.Frozen)), dec(asset.USDTValue))
		out = append(out, model.AccountBalance{
			AccountID: accountID,
			Currency:  asset.Coin,
			Total:     total,
			Free:      free,
			Locked:    firstNonZero(dec(asset.Frozen), dec(asset.Locked)),
			UpdatedAt: now,
		})
	}
	return out
}

func (c *accountClient) marginBalancesFromAssetsAndPositions(assets *bitgetsdk.AccountAssets, positions []model.Position, now time.Time) []model.MarginBalance {
	out := make([]model.MarginBalance, 0)
	if assets != nil {
		for _, asset := range assets.Assets {
			if asset.Coin == "" {
				continue
			}
			out = append(out, model.MarginBalance{
				Currency:    asset.Coin,
				Initial:     dec(assets.UnionTotalMargin),
				Maintenance: decimal.Zero,
				UpdatedAt:   now,
			})
		}
	}
	for _, pos := range positions {
		inst, ok := c.provider.Instrument(pos.InstrumentID)
		if !ok || inst.Settle == "" {
			continue
		}
		id := pos.InstrumentID
		out = append(out, model.MarginBalance{Currency: inst.Settle, InstrumentID: &id, UpdatedAt: now})
	}
	return out
}

func isUnifiedMode(mode string) bool {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	return mode == "UNIFIED" || mode == "UTA" || mode == "HYBRID"
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return fmt.Errorf("bitget: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return err
	}
	if inst.ID.Kind != enums.KindPerp {
		return fmt.Errorf("bitget: spot does not support leverage: %w", errs.ErrNotSupported)
	}
	return c.rest.SetLeverage(ctx, &bitgetsdk.SetLeverageRequest{Category: category, Symbol: inst.VenueSymbol, Leverage: leverage.String(), Coin: inst.Settle, MarginMode: "crossed"})
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return fmt.Errorf("bitget: margin mode mutation is not phase-one supported: %w", errs.ErrNotSupported)
}

func (c *accountClient) Capabilities() contract.Capabilities {
	products := make([]contract.ProductCapability, 0, len(c.scope))
	for _, kind := range c.scope {
		products = append(products, contract.ProductCapability{Kind: kind, Account: true})
	}
	return contract.Capabilities{
		Venue:     VenueName,
		Products:  products,
		Reports:   contract.ReportCapabilities{PositionReports: hasBitgetAccountKind(c.scope, enums.KindPerp), AccountBalanceSnapshots: true},
		Streaming: contract.StreamCapabilities{Account: true, AccountState: true},
	}
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }
func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}
func (c *accountClient) Close() error { c.stream.Close(); return nil }

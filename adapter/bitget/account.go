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
	rest     *bitgetsdk.Client
	provider *instrumentProvider
	clk      clock.Clock
	scope    []enums.InstrumentKind
	stream   *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *bitgetsdk.Client, provider *instrumentProvider, clk clock.Clock, scope []enums.InstrumentKind) *accountClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	return &accountClient{rest: rest, provider: provider, clk: clk, scope: bitgetKinds(scope), stream: wsstream.New[contract.AccountEnvelope](256)}
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	assets, err := c.rest.GetAccountAssets(ctx)
	if err != nil {
		return nil, err
	}
	return balancesFromAssets(assets, c.clk.Now()), nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	return c.positions(ctx)
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	info, err := c.rest.GetAccountInfo(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
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
		AccountID:    AccountIDUnified,
		Venue:        VenueName,
		Type:         model.AccountMargin,
		BaseCurrency: "USD",
		Balances:     balancesFromAssets(assets, now),
		Margins:      c.marginBalancesFromAssetsAndPositions(assets, positions, now),
		ModeInfo:     modeInfoFromBitget(info, settings, c.scope, now),
		Reported:     true,
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
	for _, category := range []string{bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures} {
		records, err := c.rest.GetCurrentPositions(ctx, category, "")
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			pos := positionFromBitget(record, c.provider.resolveVenueSymbol, AccountIDUnified, now)
			if pos.InstrumentID.Symbol == "" || pos.Quantity.IsZero() {
				continue
			}
			out = append(out, pos)
		}
	}
	return out, nil
}

func balancesFromAssets(assets *bitgetsdk.AccountAssets, now time.Time) []model.AccountBalance {
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
			AccountID: AccountIDUnified,
			Currency:  asset.Coin,
			Total:     total,
			Free:      free,
			Available: free,
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

func modeInfoFromBitget(info *bitgetsdk.AccountInfo, settings *bitgetsdk.AccountSettings, scope []enums.InstrumentKind, now time.Time) model.AccountModeInfo {
	mode := ""
	assetMode := ""
	holdMode := ""
	accountLevel := ""
	if settings != nil {
		mode = strings.ToUpper(settings.AccountMode)
		assetMode = settings.AssetMode
		holdMode = settings.HoldMode
		accountLevel = settings.AccountLevel
	}
	userID := ""
	permType := ""
	if info != nil {
		userID = info.UserID
		permType = info.PermType
	}
	return model.AccountModeInfo{
		Venue:          VenueName,
		AccountID:      AccountIDUnified,
		AccountMode:    mode,
		MarginMode:     marginModeFromSettings(settings),
		PositionMode:   firstNonEmpty(holdMode, "single_hold"),
		CollateralMode: firstNonEmpty(assetMode, "union"),
		ProductScope:   bitgetKinds(scope),
		Verified:       isUnifiedMode(mode),
		VerifiedAt:     now,
		Source:         "GET /api/v3/account/info + GET /api/v3/account/settings + GET /api/v3/account/assets",
		Details: map[string]string{
			"userId":       userID,
			"permType":     permType,
			"accountLevel": accountLevel,
			"assetMode":    assetMode,
			"holdMode":     holdMode,
		},
	}
}

func marginModeFromSettings(settings *bitgetsdk.AccountSettings) string {
	if settings == nil || len(settings.SymbolSettings) == 0 {
		return "crossed"
	}
	return firstNonEmpty(settings.SymbolSettings[0].MarginMode, "crossed")
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
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Account: true},
			{Kind: enums.KindPerp, Account: true},
		},
		Reports:   contract.ReportCapabilities{PositionReports: true, AccountBalanceSnapshots: true, AccountStateSnapshots: true},
		Streaming: contract.StreamCapabilities{Account: true, AccountState: true},
	}
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }
func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}
func (c *accountClient) Close() error { c.stream.Close(); return nil }

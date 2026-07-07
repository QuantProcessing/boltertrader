package perp

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
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

// accountClient implements contract.AccountClient over the Binance REST +
// user-data WebSocket.
type accountClient struct {
	rest      *sdkperp.Client
	provider  *instrumentProvider
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *sdkperp.Client, provider *instrumentProvider, clk clock.Clock, accountIDs ...string) *accountClient {
	accountID := ""
	if len(accountIDs) > 0 {
		accountID = accountIDs[0]
	}
	if accountID == "" {
		accountID = model.AccountIDBinanceDefault
	}
	return &accountClient{
		rest:      rest,
		provider:  provider,
		clk:       clk,
		accountID: accountID,
		stream:    wsstream.New[contract.AccountEnvelope](256),
	}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	resps, err := c.rest.GetBalance(ctx)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.AccountBalance, 0, len(resps))
	for _, b := range resps {
		if strings.TrimSpace(b.Asset) == "" {
			continue
		}
		free := dec(b.AvailableBalance)
		out = append(out, model.AccountBalance{
			AccountID: c.accountID,
			Currency:  b.Asset,
			Total:     dec(b.Balance),
			Free:      free,
			Available: free,
			UpdatedAt: now,
		})
	}
	return out, nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	acct, err := c.rest.GetAccount(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	positionMode, err := c.rest.GetPositionMode(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	multiAssetsMode, err := c.rest.GetMultiAssetsMode(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	now := c.clk.Now()
	return model.AccountState{
		AccountID:    c.accountID,
		Venue:        venueName,
		Type:         model.AccountMargin,
		BaseCurrency: "USD",
		Balances:     perpBalancesFromAccount(acct, c.accountID, now),
		Margins:      c.marginBalancesFromAccount(acct, now),
		ModeInfo:     binanceUSDMModeInfo(positionMode, multiAssetsMode, c.accountID, now),
		Reported:     true,
		TsEvent:      eventTimeFromMillis(acct.UpdateTime, now),
		TsInit:       now,
	}, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	acct, err := c.rest.GetAccount(ctx)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.Position, 0, len(acct.Positions))
	for _, p := range acct.Positions {
		qty := dec(p.PositionAmt)
		if qty.IsZero() {
			continue // skip flat legs
		}
		out = append(out, model.Position{
			AccountID:     c.accountID,
			InstrumentID:  c.provider.resolveVenueSymbol(p.Symbol),
			Side:          positionSideFromBinance(p.PositionSide),
			Quantity:      qty,
			EntryPrice:    dec(p.EntryPrice),
			UnrealizedPnL: dec(p.UnrealizedProfit),
			Leverage:      dec(p.Leverage),
			UpdatedAt:     now,
		})
	}
	return out, nil
}

func perpBalancesFromAccount(acct *sdkperp.AccountResponse, accountID string, now time.Time) []model.AccountBalance {
	out := make([]model.AccountBalance, 0, len(acct.Assets))
	for _, b := range acct.Assets {
		if strings.TrimSpace(b.Asset) == "" {
			continue
		}
		free := dec(b.AvailableBalance)
		out = append(out, model.AccountBalance{
			AccountID: accountID,
			Currency:  b.Asset,
			Total:     dec(b.WalletBalance),
			Free:      free,
			Available: free,
			Locked:    dec(b.InitialMargin),
			UpdatedAt: eventTimeFromMillis(b.UpdateTime, now),
		})
	}
	return out
}

func (c *accountClient) marginBalancesFromAccount(acct *sdkperp.AccountResponse, now time.Time) []model.MarginBalance {
	out := make([]model.MarginBalance, 0, len(acct.Assets)+len(acct.Positions))
	for _, asset := range acct.Assets {
		if strings.TrimSpace(asset.Asset) == "" {
			continue
		}
		out = append(out, model.MarginBalance{
			Currency:    asset.Asset,
			Initial:     dec(asset.InitialMargin),
			Maintenance: dec(asset.MaintMargin),
			UpdatedAt:   eventTimeFromMillis(asset.UpdateTime, now),
		})
	}
	for _, pos := range acct.Positions {
		if strings.TrimSpace(pos.Symbol) == "" {
			continue
		}
		id := c.provider.resolveVenueSymbol(pos.Symbol)
		ccy := marginCurrencyForInstrument(id)
		if inst, ok := c.provider.Instrument(id); ok && inst.Settle != "" {
			ccy = inst.Settle
		}
		if ccy == "" {
			ccy = marginCurrencyFromVenueSymbol(pos.Symbol)
		}
		if ccy == "" {
			continue
		}
		idCopy := id
		out = append(out, model.MarginBalance{
			Currency:     ccy,
			InstrumentID: &idCopy,
			Initial:      dec(pos.InitialMargin),
			Maintenance:  dec(pos.MaintMargin),
			UpdatedAt:    eventTimeFromMillis(pos.UpdateTime, now),
		})
	}
	return out
}

func binanceUSDMModeInfo(positionMode *sdkperp.PositionModeResponse, multiAssetsMode *sdkperp.MultiAssetsModeResponse, accountID string, now time.Time) model.AccountModeInfo {
	positionModeLabel := "one_way"
	if positionMode != nil && positionMode.DualSidePosition {
		positionModeLabel = "hedge"
	}
	marginMode := "single_asset"
	if multiAssetsMode != nil && multiAssetsMode.MultiAssetsMargin {
		marginMode = "multi_assets"
	}
	return model.AccountModeInfo{
		Venue:          venueName,
		AccountID:      accountID,
		AccountMode:    "USD-M",
		MarginMode:     marginMode,
		PositionMode:   positionModeLabel,
		CollateralMode: marginMode,
		ProductScope:   []enums.InstrumentKind{enums.KindPerp},
		Verified:       true,
		VerifiedAt:     now,
		Source:         "GET /fapi/v2/account + GET /fapi/v1/positionSide/dual + GET /fapi/v1/multiAssetsMargin",
		Details: map[string]string{
			"dualSidePosition":  fmt.Sprintf("%t", positionMode != nil && positionMode.DualSidePosition),
			"multiAssetsMargin": fmt.Sprintf("%t", multiAssetsMode != nil && multiAssetsMode.MultiAssetsMargin),
		},
	}
}

func marginCurrencyForInstrument(id model.InstrumentID) string {
	_, quote, ok := strings.Cut(id.Symbol, "-")
	if !ok {
		return ""
	}
	return strings.ToUpper(quote)
}

func marginCurrencyFromVenueSymbol(symbol string) string {
	for _, suffix := range []string{"USDT", "USDC", "BUSD"} {
		if strings.HasSuffix(symbol, suffix) {
			return suffix
		}
	}
	return ""
}

func eventTimeFromMillis(ms int64, fallback time.Time) time.Time {
	if ms > 0 {
		return time.UnixMilli(ms)
	}
	return fallback
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return fmt.Errorf("binance: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	_, err := c.rest.ChangeLeverage(ctx, inst.VenueSymbol, int(leverage.IntPart()))
	return err
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return fmt.Errorf("binance: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	var marginType string
	switch strings.ToLower(mode) {
	case "cross":
		marginType = "CROSSED"
	case "isolated":
		marginType = "ISOLATED"
	default:
		return fmt.Errorf("binance: invalid margin mode %q: %w", mode, errs.ErrNotSupported)
	}
	return c.rest.ChangeMarginType(ctx, inst.VenueSymbol, marginType)
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

// emit pushes a translated account event; blocks under backpressure (never
// dropping balance/position updates), no-op after Close.
func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}

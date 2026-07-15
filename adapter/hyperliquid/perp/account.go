package perp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	hlaccount "github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/account"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest               *sdkperp.Client
	provider           *instruments.Registry
	clk                clock.Clock
	marginModeLeverage decimal.Decimal
	accountMode        sdk.AccountAbstraction
	accountID          string
	hip3Dexes          []string
	stream             *wsstream.Stream[contract.AccountEnvelope]

	mu          sync.RWMutex
	marginModes map[string]string
	defaultMode string

	spotMu         sync.Mutex
	spotCurrencies map[string]struct{}

	positionMu      sync.Mutex
	streamPositions map[string]map[model.InstrumentID]model.Position
}

func (c *accountClient) withHIP3Dexes(dexes []string) *accountClient {
	if c == nil {
		return nil
	}
	c.hip3Dexes = normalizeHIP3DexNames(dexes)
	return c
}

func newAccountClient(rest *sdkperp.Client, provider *instruments.Registry, clk clock.Clock, mode string, marginModeLeverage decimal.Decimal, accountMode sdk.AccountAbstraction, accountID ...string) *accountClient {
	normalized, err := normalizeMarginMode(mode)
	if err != nil {
		normalized = "cross"
	}
	if !marginModeLeverage.IsPositive() {
		marginModeLeverage = decimal.NewFromInt(1)
	}
	return &accountClient{
		rest:               rest,
		provider:           provider,
		clk:                clk,
		marginModeLeverage: marginModeLeverage,
		accountMode:        accountMode,
		accountID:          firstAccountID(accountID),
		stream:             wsstream.New[contract.AccountEnvelope](256),
		marginModes:        make(map[string]string),
		defaultMode:        normalized,
		spotCurrencies:     make(map[string]struct{}),
		streamPositions:    make(map[string]map[model.InstrumentID]model.Position),
	}
}

func (c *accountClient) eventsFromClearinghouseState(state *sdkperp.PerpPosition, dex string) ([]contract.AccountEvent, error) {
	if state == nil {
		return nil, fmt.Errorf("hyperliquid perp: missing clearinghouseState for dex %q", dex)
	}
	dex = strings.TrimSpace(dex)
	now := c.clk.Now()
	events := make([]contract.AccountEvent, 0, 1+len(state.AssetPositions))
	var positions []model.Position
	if dex == "" {
		for _, raw := range state.AssetPositions {
			coin := strings.TrimSpace(raw.Position.Coin)
			if coin == "" || dec(raw.Position.Szi).IsZero() {
				continue
			}
			if _, ok := c.provider.ResolveVenueSymbol(coin); !ok {
				return nil, fmt.Errorf("hyperliquid perp: unresolved nonzero clearinghouse position instrument %s", coin)
			}
		}
		if !c.accountMode.UsesSpotClearinghouseState() {
			if balance, ok := balanceFromPerpPosition(state, c.clk, c.accountID); ok {
				events = append(events, contract.BalanceEvent{Balance: balance})
			}
		}
		positions = positionsFromPerpPosition(state, c.provider, now, c.accountID)
	} else {
		var err error
		positions, err = positionsFromPerpPositionForDex(state, c.provider, now, dex, c.accountID)
		if err != nil {
			return nil, err
		}
	}

	current := make(map[model.InstrumentID]model.Position, len(positions))
	for _, position := range positions {
		if _, duplicate := current[position.InstrumentID]; duplicate {
			return nil, fmt.Errorf("hyperliquid perp: duplicate clearinghouse position %s for dex %q", position.InstrumentID, dex)
		}
		current[position.InstrumentID] = position
		events = append(events, contract.PositionEvent{Position: position})
	}

	c.positionMu.Lock()
	previous := c.streamPositions[dex]
	missing := make([]model.Position, 0, len(previous))
	for id, position := range previous {
		if _, stillPresent := current[id]; stillPresent {
			continue
		}
		position.Quantity = decimal.Zero
		position.UnrealizedPnL = decimal.Zero
		position.UpdatedAt = clearinghouseStateTime(state, now)
		missing = append(missing, position)
	}
	c.streamPositions[dex] = current
	c.positionMu.Unlock()

	sort.Slice(missing, func(i, j int) bool {
		return missing[i].InstrumentID.String() < missing[j].InstrumentID.String()
	})
	for _, position := range missing {
		events = append(events, contract.PositionEvent{Position: position})
	}
	return events, nil
}

func clearinghouseStateTime(state *sdkperp.PerpPosition, fallback time.Time) time.Time {
	if state != nil {
		if timestamp := parseMillis(state.Time); !timestamp.IsZero() {
			return timestamp
		}
	}
	return fallback
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("hyperliquid perp: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	if inst.AssetIndex == nil {
		return nil, fmt.Errorf("hyperliquid perp: missing asset index for %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	if c.accountMode.UsesSpotClearinghouseState() {
		return c.spotClearinghouseBalances(ctx)
	}
	state, err := c.rest.GetBalance(ctx)
	if err != nil {
		return nil, err
	}
	balance, ok := balanceFromPerpPosition(state, c.clk, c.accountID)
	if !ok {
		return []model.AccountBalance{}, nil
	}
	return []model.AccountBalance{balance}, nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	mode := c.accountMode
	if mode == sdk.AccountAbstractionUnknown {
		resolved, err := c.rest.GetUserAbstraction(ctx, c.rest.AccountAddr)
		if err != nil {
			return model.AccountState{}, err
		}
		mode = resolved
	}
	perpState, err := c.rest.GetBalance(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	perpDexes, err := c.perpDexStates(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	spotState, err := c.rest.GetSpotClearinghouseState(ctx, c.rest.AccountAddr)
	if err != nil {
		return model.AccountState{}, err
	}
	state, err := hlaccount.BuildAccountState(hlaccount.StateInput{
		AccountID:   c.accountID,
		AccountMode: mode,
		Perp:        perpState,
		PerpDexes:   perpDexes,
		Spot:        spotState,
		Now:         c.clk.Now(),
	})
	if err != nil {
		return model.AccountState{}, err
	}
	c.rememberSpotCurrencies(spotState.Balances)
	return state, nil
}

func (c *accountClient) spotClearinghouseBalances(ctx context.Context) ([]model.AccountBalance, error) {
	state, err := c.rest.GetSpotClearinghouseState(ctx, c.rest.AccountAddr)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("hyperliquid perp: missing spotClearinghouseState")
	}
	out, err := hlaccount.SpotBalances(c.accountID, *state, c.clk.Now())
	if err != nil {
		return nil, err
	}
	c.rememberSpotCurrencies(state.Balances)
	return out, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	state, err := c.rest.GetBalance(ctx)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.Position, 0, len(state.AssetPositions))
	seen := make(map[string]model.Position)
	out, err = appendUniquePositions(out, seen, positionsFromPerpPosition(state, c.provider, now, c.accountID))
	if err != nil {
		return nil, err
	}
	for _, dex := range c.hip3Dexes {
		dexState, err := c.rest.GetBalanceForDex(ctx, dex)
		if err != nil {
			return nil, fmt.Errorf("hyperliquid perp: get HIP-3 clearinghouse state for dex %s: %w", dex, err)
		}
		dexPositions, err := positionsFromPerpPositionForDex(dexState, c.provider, now, dex, c.accountID)
		if err != nil {
			return nil, fmt.Errorf("hyperliquid perp: decode HIP-3 positions for dex %s: %w", dex, err)
		}
		out, err = appendUniquePositions(out, seen, dexPositions)
		if err != nil {
			return nil, fmt.Errorf("hyperliquid perp: merge HIP-3 positions for dex %s: %w", dex, err)
		}
	}
	return out, nil
}

func (c *accountClient) perpDexStates(ctx context.Context) ([]hlaccount.PerpDexState, error) {
	out := make([]hlaccount.PerpDexState, 0, len(c.hip3Dexes))
	for _, dex := range c.hip3Dexes {
		collateral, err := c.hip3Collateral(dex)
		if err != nil {
			return nil, err
		}
		state, err := c.rest.GetBalanceForDex(ctx, dex)
		if err != nil {
			return nil, fmt.Errorf("hyperliquid perp: get HIP-3 clearinghouse state for dex %s: %w", dex, err)
		}
		out = append(out, hlaccount.PerpDexState{Dex: dex, Collateral: collateral, State: state})
	}
	return out, nil
}

func (c *accountClient) hip3Collateral(dex string) (string, error) {
	var collateral string
	for _, inst := range c.provider.All() {
		instDex, _, ok := strings.Cut(inst.VenueSymbol, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(instDex), dex) {
			continue
		}
		settle := strings.TrimSpace(inst.Settle)
		if settle == "" {
			return "", fmt.Errorf("hyperliquid perp: HIP-3 dex %s instrument %s has no settlement currency", dex, inst.ID)
		}
		if collateral != "" && !strings.EqualFold(collateral, settle) {
			return "", fmt.Errorf("hyperliquid perp: HIP-3 dex %s has inconsistent settlement currencies %s and %s", dex, collateral, settle)
		}
		collateral = settle
	}
	if collateral == "" {
		return "", fmt.Errorf("hyperliquid perp: HIP-3 dex %s has no registered instruments", dex)
	}
	return collateral, nil
}

func appendUniquePositions(out []model.Position, seen map[string]model.Position, positions []model.Position) ([]model.Position, error) {
	for _, pos := range positions {
		if pos.Quantity.IsZero() {
			continue
		}
		key := pos.AccountID + "\x00" + pos.InstrumentID.String() + "\x00" + fmt.Sprint(pos.Side)
		if prior, ok := seen[key]; ok {
			if samePositionSnapshot(prior, pos) {
				continue
			}
			return nil, fmt.Errorf("conflicting position snapshots for %s", pos.InstrumentID)
		}
		seen[key] = pos
		out = append(out, pos)
	}
	return out, nil
}

func samePositionSnapshot(a, b model.Position) bool {
	return a.AccountID == b.AccountID && a.InstrumentID == b.InstrumentID && a.Side == b.Side &&
		a.Quantity.Equal(b.Quantity) && a.EntryPrice.Equal(b.EntryPrice) && a.MarkPrice.Equal(b.MarkPrice) &&
		a.UnrealizedPnL.Equal(b.UnrealizedPnL) && a.Leverage.Equal(b.Leverage) && a.UpdatedAt.Equal(b.UpdatedAt)
}

func normalizeHIP3DexNames(dexes []string) []string {
	out := make([]string, 0, len(dexes))
	seen := make(map[string]struct{}, len(dexes))
	for _, raw := range dexes {
		dex := strings.TrimSpace(raw)
		if dex == "" {
			continue
		}
		key := strings.ToLower(dex)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, dex)
	}
	return out
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	lev, err := leverageInt(leverage)
	if err != nil {
		return err
	}
	mode := c.marginMode(id)
	return c.rest.UpdateLeverage(ctx, sdkperp.UpdateLeverageRequest{
		AssetID:  *inst.AssetIndex,
		IsCross:  isCrossMarginMode(mode),
		Leverage: lev,
	})
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	normalized, err := normalizeMarginMode(mode)
	if err != nil {
		return err
	}
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	lev, err := leverageInt(c.marginModeLeverage)
	if err != nil {
		return err
	}
	if err := c.rest.UpdateLeverage(ctx, sdkperp.UpdateLeverageRequest{
		AssetID:  *inst.AssetIndex,
		IsCross:  isCrossMarginMode(normalized),
		Leverage: lev,
	}); err != nil {
		return err
	}
	c.setMarginMode(id, normalized)
	return nil
}

func (c *accountClient) marginMode(id model.InstrumentID) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if mode, ok := c.marginModes[id.String()]; ok {
		return mode
	}
	return c.defaultMode
}

func (c *accountClient) setMarginMode(id model.InstrumentID, mode string) {
	c.mu.Lock()
	c.marginModes[id.String()] = mode
	c.mu.Unlock()
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}

func (c *accountClient) rememberSpotCurrencies(balances []sdk.SpotBalance) {
	current := make(map[string]struct{}, len(balances))
	for _, raw := range balances {
		if currency := strings.ToUpper(strings.TrimSpace(raw.Coin)); currency != "" {
			current[currency] = struct{}{}
		}
	}
	c.spotMu.Lock()
	if c.spotCurrencies == nil {
		c.spotCurrencies = make(map[string]struct{}, len(current))
	}
	for currency := range current {
		c.spotCurrencies[currency] = struct{}{}
	}
	c.spotMu.Unlock()
}

func (c *accountClient) eventsFromSpotState(state sdk.SpotClearinghouseState, now time.Time) ([]contract.AccountEvent, error) {
	balances, err := hlaccount.SpotBalances(c.accountID, state, now)
	if err != nil {
		return nil, err
	}
	events := make([]contract.AccountEvent, 0, len(balances))
	current := make(map[string]struct{}, len(balances))
	for _, balance := range balances {
		current[balance.Currency] = struct{}{}
		events = append(events, contract.BalanceEvent{Balance: balance})
	}
	c.spotMu.Lock()
	for currency := range c.spotCurrencies {
		if _, ok := current[currency]; ok {
			continue
		}
		events = append(events, contract.BalanceEvent{Balance: model.AccountBalance{
			AccountID: c.accountID,
			Currency:  currency,
			UpdatedAt: now,
		}})
	}
	c.spotCurrencies = current
	c.spotMu.Unlock()
	return events, nil
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}

func firstAccountID(ids []string) string {
	if len(ids) == 0 || ids[0] == "" {
		return AccountIDDefault
	}
	return ids[0]
}

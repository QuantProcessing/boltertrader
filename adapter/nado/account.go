package nado

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest          *sdk.Client
	provider      *instrumentProvider
	clk           clock.Clock
	productKind   enums.InstrumentKind
	accountID     string
	stream        *wsstream.Stream[contract.AccountEnvelope]
	streamBackend nadoAccountStreamBackend
	startMu       sync.Mutex
	started       bool
	snapshotMu    sync.Mutex
	snapshot      *sdk.AccountSnapshot
	evidenceMu    sync.RWMutex
	healths       []sdk.Health
	contribs      []sdk.HealthContribution
}

func newAccountClient(rest *sdk.Client, provider *instrumentProvider, clk clock.Clock, kind enums.InstrumentKind, accountIDs ...string) *accountClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	accountID := AccountIDUnified
	if len(accountIDs) > 0 && accountIDs[0] != "" {
		accountID = accountIDs[0]
	}
	return &accountClient{rest: rest, provider: provider, clk: clk, productKind: kind, accountID: accountID, stream: wsstream.New[contract.AccountEnvelope](256)}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{{
			Kind:    selectedKind(c.productKind),
			Account: true,
		}},
		Reports:   contract.ReportCapabilities{AccountBalanceSnapshots: true, PositionReports: selectedKind(c.productKind) == enums.KindPerp},
		Streaming: contract.StreamCapabilities{Account: c.streamBackend != nil},
	}
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	snapshot, err := c.accountSnapshot(ctx, false)
	if err != nil {
		return model.AccountState{}, err
	}
	state, err := accountStateFromNado(snapshot, c.provider, c.accountID, c.clk.Now())
	if err != nil {
		return model.AccountState{}, err
	}
	c.setHealthEvidence(snapshot.Account.Healths, snapshot.Account.HealthContributions)
	return state, nil
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	state, err := c.AccountState(ctx)
	if err != nil {
		return nil, err
	}
	return state.Balances, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	snapshot, err := c.accountSnapshot(ctx, true)
	if err != nil {
		return nil, err
	}
	return positionsFromNado(snapshot.Account, c.provider, c.accountID, snapshot.ReceivedAt)
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	return fmt.Errorf("nado: leverage mutation is not part of Story 5 adapter foundations: %w", contract.ErrNotSupported)
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return fmt.Errorf("nado: margin-mode mutation is not part of Story 5 adapter foundations: %w", contract.ErrNotSupported)
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }
func (c *accountClient) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.streamBackend == nil {
		return nil
	}
	c.startMu.Lock()
	if c.started {
		c.startMu.Unlock()
		return c.streamBackend.Connect()
	}
	if err := c.streamBackend.SubscribePositions(nil, c.handlePositionChange); err != nil {
		c.startMu.Unlock()
		return err
	}
	c.started = true
	c.startMu.Unlock()
	return c.streamBackend.Connect()
}

func (c *accountClient) Close() error {
	if c.streamBackend != nil {
		c.streamBackend.Close()
	}
	c.stream.Close()
	return nil
}

func (c *accountClient) Connected() bool {
	return c.streamBackend != nil && c.streamBackend.IsConnected()
}

func (c *accountClient) Reconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.streamBackend == nil {
		return fmt.Errorf("nado: account stream backend not configured: %w", contract.ErrNotSupported)
	}
	return c.streamBackend.Connect()
}

func (c *accountClient) setHealthEvidence(healths []sdk.Health, contribs []sdk.HealthContribution) {
	c.evidenceMu.Lock()
	c.healths = append([]sdk.Health(nil), healths...)
	c.contribs = append([]sdk.HealthContribution(nil), contribs...)
	c.evidenceMu.Unlock()
}

func (c *accountClient) rawHealthEvidence() ([]sdk.Health, []sdk.HealthContribution) {
	c.evidenceMu.RLock()
	defer c.evidenceMu.RUnlock()
	return append([]sdk.Health(nil), c.healths...), append([]sdk.HealthContribution(nil), c.contribs...)
}

func (c *accountClient) accountSnapshot(ctx context.Context, allowCached bool) (*sdk.AccountSnapshot, error) {
	if allowCached {
		if snapshot := c.cachedAccountSnapshot(c.clk.Now()); snapshot != nil {
			return snapshot, nil
		}
	}
	if c.rest == nil {
		return nil, fmt.Errorf("nado: rest client not configured: %w", contract.ErrNotSupported)
	}
	snapshot, err := c.rest.GetAccountSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	c.storeAccountSnapshot(snapshot)
	return snapshot, nil
}

func (c *accountClient) cachedAccountSnapshot(now time.Time) *sdk.AccountSnapshot {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	if c.snapshot == nil || c.snapshot.ReceivedAt.IsZero() || now.Before(c.snapshot.ReceivedAt) || now.Sub(c.snapshot.ReceivedAt) > time.Second {
		return nil
	}
	cp := *c.snapshot
	cp.Account = cloneNadoAccountInfo(c.snapshot.Account)
	return &cp
}

func (c *accountClient) storeAccountSnapshot(snapshot *sdk.AccountSnapshot) {
	if snapshot == nil {
		return
	}
	cp := *snapshot
	cp.Account = cloneNadoAccountInfo(snapshot.Account)
	c.snapshotMu.Lock()
	c.snapshot = &cp
	c.snapshotMu.Unlock()
}

func cloneNadoAccountInfo(account sdk.AccountInfo) sdk.AccountInfo {
	account.Healths = append([]sdk.Health(nil), account.Healths...)
	account.HealthContributions = append([]sdk.HealthContribution(nil), account.HealthContributions...)
	account.SpotBalances = append([]sdk.Balance(nil), account.SpotBalances...)
	account.PerpBalances = append([]sdk.Balance(nil), account.PerpBalances...)
	account.TakerFeeRatesX18 = append([]string(nil), account.TakerFeeRatesX18...)
	account.MakerFeeRatesX18 = append([]string(nil), account.MakerFeeRatesX18...)
	if account.PreState != nil {
		pre := *account.PreState
		pre.Healths = append([]sdk.Health(nil), account.PreState.Healths...)
		pre.HealthContributions = append([]sdk.HealthContribution(nil), account.PreState.HealthContributions...)
		pre.SpotBalances = append([]sdk.Balance(nil), account.PreState.SpotBalances...)
		pre.PerpBalances = append([]sdk.Balance(nil), account.PreState.PerpBalances...)
		account.PreState = &pre
	}
	return account
}

func (c *accountClient) handlePositionChange(change *sdk.PositionChange) {
	if change == nil {
		return
	}
	if change.Isolated {
		return
	}
	id, ok := c.provider.ResolveProductID(change.ProductId)
	if !ok {
		return
	}
	amount, err := parseX18Required(change.Amount, "position change amount")
	if err != nil {
		return
	}
	ts := timeFromString(change.Timestamp)
	if ts.IsZero() {
		return
	}
	switch id.Kind {
	case enums.KindPerp:
		if id.Kind != selectedKind(c.productKind) {
			return
		}
		pos := model.Position{
			AccountID:    c.accountID,
			InstrumentID: id,
			Side:         enums.PosNet,
			Quantity:     amount,
			UpdatedAt:    ts,
		}
		c.stream.Emit(contract.NewAccountEnvelopeWithMeta(contract.PositionEvent{Position: pos}, nadoEventMeta("account", "position", c.accountID, fmt.Sprint(change.ProductId), change.Timestamp, change.Amount, string(change.Reason))))
	case enums.KindSpot:
		if id.Kind != selectedKind(c.productKind) {
			return
		}
		currency, ok := c.provider.CurrencyForProductID(change.ProductId)
		if !ok || currency == "" {
			return
		}
		borrowed := decimal.Zero
		if amount.IsNegative() {
			borrowed = amount.Abs()
		}
		bal := model.AccountBalance{AccountID: c.accountID, Currency: currency, Total: amount, Borrowed: borrowed, UpdatedAt: ts}
		c.stream.Emit(contract.NewAccountEnvelopeWithMeta(contract.BalanceEvent{Balance: bal}, nadoEventMeta("account", "balance", c.accountID, fmt.Sprint(change.ProductId), change.Timestamp, change.Amount, string(change.Reason))))
	}
}

func accountStateFromNado(snapshot *sdk.AccountSnapshot, provider *instrumentProvider, accountID string, now time.Time) (model.AccountState, error) {
	if snapshot == nil {
		return model.AccountState{}, fmt.Errorf("nado: account snapshot is required")
	}
	ts := snapshot.ReceivedAt
	if ts.IsZero() {
		return model.AccountState{}, fmt.Errorf("nado: account snapshot receipt timestamp is required")
	}
	if provider == nil {
		return model.AccountState{}, fmt.Errorf("nado: account instrument registry is required")
	}
	if accountID == "" {
		return model.AccountState{}, fmt.Errorf("nado: logical account id is required")
	}
	account := snapshot.Account
	if !account.Exists {
		return model.AccountState{}, fmt.Errorf("nado: selected subaccount does not exist")
	}
	if len(account.Healths) < 3 {
		return model.AccountState{}, fmt.Errorf("nado: account snapshot requires initial, maintenance, and unweighted health")
	}
	health := make([]decimal.Decimal, len(account.Healths))
	for i, item := range account.Healths {
		assets, err := parseX18Required(item.Assets, fmt.Sprintf("health[%d] assets", i))
		if err != nil {
			return model.AccountState{}, err
		}
		liabilities, err := parseX18Required(item.Liabilities, fmt.Sprintf("health[%d] liabilities", i))
		if err != nil {
			return model.AccountState{}, err
		}
		if assets.IsNegative() || liabilities.IsNegative() {
			return model.AccountState{}, fmt.Errorf("nado: health[%d] assets and liabilities must be non-negative", i)
		}
		value, err := parseX18Required(item.Health, fmt.Sprintf("health[%d] value", i))
		if err != nil {
			return model.AccountState{}, err
		}
		health[i] = value
	}
	settlement, ok := provider.SettlementCurrency()
	if !ok || settlement != "USDT0" {
		return model.AccountState{}, fmt.Errorf("nado: verified settlement product metadata is unavailable")
	}
	balances := make([]model.AccountBalance, 0, len(account.SpotBalances))
	for _, balance := range account.SpotBalances {
		currency, ok := provider.CurrencyForProductID(balance.ProductID)
		if !ok || currency == "" {
			return model.AccountState{}, fmt.Errorf("nado: no currency metadata for spot product %d", balance.ProductID)
		}
		amount, err := parseX18Required(balance.Balance.Amount, fmt.Sprintf("spot product %d balance", balance.ProductID))
		if err != nil {
			return model.AccountState{}, err
		}
		borrowed := decimal.Zero
		if amount.IsNegative() {
			borrowed = amount.Abs()
		}
		balances = append(balances, model.AccountBalance{
			AccountID: accountID,
			Currency:  currency,
			Total:     amount,
			Borrowed:  borrowed,
			UpdatedAt: ts,
		})
	}
	state := model.AccountState{
		AccountID:    accountID,
		Venue:        VenueName,
		Type:         model.AccountMargin,
		BaseCurrency: settlement,
		Balances:     balances,
		Summary: &model.AccountSummary{
			SettlementCurrency:  settlement,
			AvailableCollateral: decimal.Max(health[0], decimal.Zero),
			Equity:              health[2],
			UpdatedAt:           ts,
		},
		Reported: true,
		EventID:  model.AccountStateEventID(VenueName, accountID, ts),
		TsEvent:  ts,
		TsInit:   now,
	}
	if err := state.Validate(); err != nil {
		return model.AccountState{}, err
	}
	return state, nil
}

func positionsFromNado(account sdk.AccountInfo, provider *instrumentProvider, accountID string, now time.Time) ([]model.Position, error) {
	if provider == nil {
		return nil, fmt.Errorf("nado: account instrument registry is required")
	}
	if now.IsZero() {
		return nil, fmt.Errorf("nado: position receipt timestamp is required")
	}
	out := make([]model.Position, 0, len(account.PerpBalances))
	for _, balance := range account.PerpBalances {
		qty, err := parseX18Required(balance.Balance.Amount, fmt.Sprintf("perp product %d balance", balance.ProductID))
		if err != nil {
			return nil, err
		}
		// Nado includes zero balances for products outside the currently
		// discovered trading universe. They are not positions and must not
		// make an otherwise valid account snapshot unusable.
		if qty.IsZero() {
			continue
		}
		id, ok := provider.ResolveProductID(balance.ProductID)
		if !ok || id.Kind != enums.KindPerp {
			return nil, fmt.Errorf("nado: no perp instrument metadata for product %d", balance.ProductID)
		}
		out = append(out, model.Position{
			AccountID:    accountID,
			InstrumentID: id,
			Side:         enums.PosNet,
			Quantity:     qty,
			UpdatedAt:    now,
		})
	}
	return out, nil
}

package accounting

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

const DefaultStaleAfter = 30 * time.Second

type Account interface {
	ID() string
	Venue() string
	Type() model.AccountType
	BaseCurrency() string
	Summary() *model.AccountSummary
	LastEvent() model.AccountState
	Freshness() model.AccountFreshness
	IsFresh(now time.Time) bool
	Apply(state model.AccountState, appliedAt time.Time) error
	ApplyBalance(balance model.AccountBalance) error
	MarkReconciled(at time.Time)
	Balance(currency string) (model.AccountBalance, bool)
	Balances() []model.AccountBalance
	BalanceTotal(currency string) (decimal.Decimal, bool)
	BalanceFree(currency string) (decimal.Decimal, bool)
	BalanceLocked(currency string) (decimal.Decimal, bool)
	Margins() []model.MarginBalance
	MarginInitial(currency string, instrument *model.InstrumentID) (decimal.Decimal, bool)
	MarginMaintenance(currency string, instrument *model.InstrumentID) (decimal.Decimal, bool)
}

type CashAccount struct {
	baseAccount
}

type MarginAccount struct {
	baseAccount
}

type baseAccount struct {
	mu       sync.RWMutex
	id       string
	venue    string
	typ      model.AccountType
	baseCcy  string
	last     model.AccountState
	fresh    model.AccountFreshness
	balances map[string]model.AccountBalance
	margins  map[marginKey]model.MarginBalance
}

type marginKey struct {
	currency   string
	instrument string
}

func New(state model.AccountState, staleAfter time.Duration, appliedAt time.Time) (Account, error) {
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	if appliedAt.IsZero() {
		appliedAt = time.Now()
	}
	if err := validateTradingReady(state, staleAfter, appliedAt); err != nil {
		return nil, err
	}
	var acct Account
	switch state.Type {
	case model.AccountCash:
		acct = &CashAccount{baseAccount: newBaseAccount(state, staleAfter)}
	case model.AccountMargin:
		acct = &MarginAccount{baseAccount: newBaseAccount(state, staleAfter)}
	default:
		return nil, fmt.Errorf("accounting: unsupported account type %s", state.Type)
	}
	if err := acct.Apply(state, appliedAt); err != nil {
		return nil, err
	}
	return acct, nil
}

func newBaseAccount(state model.AccountState, staleAfter time.Duration) baseAccount {
	return baseAccount{
		id:       state.AccountID,
		venue:    state.Venue,
		typ:      state.Type,
		baseCcy:  state.BaseCurrency,
		fresh:    model.AccountFreshness{StaleAfter: staleAfter},
		balances: make(map[string]model.AccountBalance),
		margins:  make(map[marginKey]model.MarginBalance),
	}
}

func (a *baseAccount) ID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.id
}
func (a *baseAccount) Venue() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.venue
}
func (a *baseAccount) Type() model.AccountType {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.typ
}
func (a *baseAccount) BaseCurrency() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.baseCcy
}
func (a *baseAccount) Summary() *model.AccountSummary {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.last.Summary == nil {
		return nil
	}
	summary := *a.last.Summary
	return &summary
}
func (a *baseAccount) LastEvent() model.AccountState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return copyState(a.last)
}
func (a *baseAccount) Freshness() model.AccountFreshness {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.fresh
}
func (a *baseAccount) IsFresh(now time.Time) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.fresh.IsFresh(now)
}

func (a *baseAccount) Apply(state model.AccountState, appliedAt time.Time) error {
	if appliedAt.IsZero() {
		appliedAt = time.Now()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := validateTradingReady(state, a.fresh.StaleAfter, appliedAt); err != nil {
		return err
	}
	if state.AccountID != a.id {
		return fmt.Errorf("accounting: account id changed from %s to %s", a.id, state.AccountID)
	}
	if state.Venue != a.venue {
		return fmt.Errorf("accounting: venue changed from %s to %s", a.venue, state.Venue)
	}
	if state.Type != a.typ {
		return fmt.Errorf("accounting: account type changed from %s to %s", a.typ, state.Type)
	}
	if state.BaseCurrency != a.baseCcy {
		return fmt.Errorf("accounting: base currency changed from %s to %s", a.baseCcy, state.BaseCurrency)
	}
	if !a.last.TsEvent.IsZero() && state.TsEvent.Before(a.last.TsEvent) {
		return nil
	}
	a.last = copyState(state)
	a.balances = make(map[string]model.AccountBalance, len(state.Balances))
	for _, bal := range state.Balances {
		if bal.AccountID == "" {
			bal.AccountID = state.AccountID
		}
		a.balances[bal.Currency] = bal
	}
	a.margins = make(map[marginKey]model.MarginBalance, len(state.Margins))
	for _, margin := range state.Margins {
		a.margins[keyForMargin(margin)] = model.CloneMarginBalance(margin)
	}
	a.fresh.LastAccountStateAt = appliedAt
	return nil
}

func (a *baseAccount) ApplyBalance(balance model.AccountBalance) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if balance.AccountID == "" {
		balance.AccountID = a.id
	}
	if balance.AccountID != a.id {
		return fmt.Errorf("accounting: balance account id %s does not match %s", balance.AccountID, a.id)
	}
	if existing, ok := a.balances[balance.Currency]; ok && !balance.UpdatedAt.IsZero() && !existing.UpdatedAt.IsZero() && balance.UpdatedAt.Before(existing.UpdatedAt) {
		return nil
	}
	candidate := copyState(a.last)
	replaced := false
	for i := range candidate.Balances {
		if candidate.Balances[i].Currency == balance.Currency {
			candidate.Balances[i] = balance
			replaced = true
			break
		}
	}
	if !replaced {
		candidate.Balances = append(candidate.Balances, balance)
	}
	if balance.UpdatedAt.After(candidate.TsEvent) {
		candidate.TsEvent = balance.UpdatedAt
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	a.last = candidate
	a.balances[balance.Currency] = balance
	return nil
}

func validateTradingReady(state model.AccountState, staleAfter time.Duration, appliedAt time.Time) error {
	freshness := model.AccountFreshness{
		LastAccountStateAt: appliedAt,
		StaleAfter:         staleAfter,
	}
	return state.ValidateTradingReady(freshness, appliedAt)
}

func (a *baseAccount) MarkReconciled(at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fresh.LastReconciledAt = at
}

func (a *baseAccount) Balance(currency string) (model.AccountBalance, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	b, ok := a.balances[currency]
	return b, ok
}

func (a *baseAccount) Balances() []model.AccountBalance {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]model.AccountBalance, 0, len(a.balances))
	for _, b := range a.balances {
		out = append(out, b)
	}
	return out
}

func (a *baseAccount) BalanceTotal(currency string) (decimal.Decimal, bool) {
	b, ok := a.Balance(currency)
	return b.Total, ok
}

func (a *baseAccount) BalanceFree(currency string) (decimal.Decimal, bool) {
	b, ok := a.Balance(currency)
	return b.Free, ok
}

func (a *baseAccount) BalanceLocked(currency string) (decimal.Decimal, bool) {
	b, ok := a.Balance(currency)
	return b.Locked, ok
}

func (a *baseAccount) Margins() []model.MarginBalance {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]model.MarginBalance, 0, len(a.margins))
	for _, m := range a.margins {
		out = append(out, model.CloneMarginBalance(m))
	}
	return out
}

func (a *baseAccount) MarginInitial(currency string, instrument *model.InstrumentID) (decimal.Decimal, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	m, ok := a.margins[marginKey{currency: currency, instrument: instrumentKey(instrument)}]
	return m.Initial, ok
}

func (a *baseAccount) MarginMaintenance(currency string, instrument *model.InstrumentID) (decimal.Decimal, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	m, ok := a.margins[marginKey{currency: currency, instrument: instrumentKey(instrument)}]
	return m.Maintenance, ok
}

func keyForMargin(m model.MarginBalance) marginKey {
	return marginKey{currency: m.Currency, instrument: instrumentKey(m.InstrumentID)}
}

func instrumentKey(id *model.InstrumentID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func copyState(state model.AccountState) model.AccountState {
	return model.CloneAccountState(state)
}

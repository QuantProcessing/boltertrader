package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

const (
	AccountIDBinanceDefault     = "BINANCE-001"
	AccountIDOKXDefault         = "OKX-001"
	AccountIDBybitDefault       = "BYBIT-001"
	AccountIDBitgetDefault      = "BITGET-001"
	AccountIDGateDefault        = "GATE-001"
	AccountIDLighterDefault     = "LIGHTER-001"
	AccountIDHyperliquidDefault = "HYPERLIQUID-001"
)

func DefaultAccountIDForVenue(venue string) string {
	switch strings.ToUpper(strings.TrimSpace(venue)) {
	case "BINANCE":
		return AccountIDBinanceDefault
	case "OKX":
		return AccountIDOKXDefault
	case "BYBIT":
		return AccountIDBybitDefault
	case "BITGET":
		return AccountIDBitgetDefault
	case "GATE":
		return AccountIDGateDefault
	case "LIGHTER":
		return AccountIDLighterDefault
	case "HYPERLIQUID":
		return AccountIDHyperliquidDefault
	default:
		return ""
	}
}

type AccountType uint8

const (
	AccountTypeUnknown AccountType = iota
	AccountCash
	AccountMargin
)

func (t AccountType) String() string {
	switch t {
	case AccountCash:
		return "CASH"
	case AccountMargin:
		return "MARGIN"
	default:
		return "UNKNOWN"
	}
}

func (t AccountType) Valid() bool {
	return t == AccountCash || t == AccountMargin
}

// Position is a venue-neutral position. Quantity is SIGNED — positive long,
// negative short — which uniformly captures Binance's signed PositionAmt,
// OKX's Pos+PosSide, and Hyperliquid's signed Szi. Side disambiguates hedge
// mode where two legs of the same instrument coexist.
type Position struct {
	AccountID     string
	InstrumentID  InstrumentID
	Side          enums.PositionSide // PosLong/PosShort under hedge mode, else PosNet
	Quantity      decimal.Decimal    // signed: + long, - short
	EntryPrice    decimal.Decimal
	MarkPrice     decimal.Decimal
	UnrealizedPnL decimal.Decimal
	Leverage      decimal.Decimal
	UpdatedAt     time.Time
}

// AccountBalance is a per-currency balance. Hyperliquid reports a single USDC
// balance; Binance and OKX report many.
type AccountBalance struct {
	AccountID string
	Currency  string
	Total     decimal.Decimal
	Free      decimal.Decimal
	Available decimal.Decimal
	Locked    decimal.Decimal
	Borrowed  decimal.Decimal
	Interest  decimal.Decimal
	UpdatedAt time.Time
}

// FreeOrAvailable returns the canonical free balance while keeping old adapter
// snapshots readable during the migration from Available to Free.
func (b AccountBalance) FreeOrAvailable() decimal.Decimal {
	if !b.Free.IsZero() || b.Available.IsZero() {
		return b.Free
	}
	return b.Available
}

// Normalized returns a copy with both Free and Available populated from the
// non-zero side when only one side is present.
func (b AccountBalance) Normalized() AccountBalance {
	if b.Free.IsZero() && !b.Available.IsZero() {
		b.Free = b.Available
	}
	if b.Available.IsZero() && !b.Free.IsZero() {
		b.Available = b.Free
	}
	return b
}

// CashInvariantOK reports whether a cash-account balance satisfies
// total == free + locked. Margin accounts may intentionally not use this
// invariant because Free can represent free margin instead of free cash.
func (b AccountBalance) CashInvariantOK() bool {
	return b.Total.Equal(b.FreeOrAvailable().Add(b.Locked))
}

func (b AccountBalance) ValidateCash() error {
	free := b.FreeOrAvailable()
	if b.Currency == "" {
		return fmt.Errorf("account balance: currency required")
	}
	if b.Total.IsNegative() {
		return fmt.Errorf("account balance %s: negative total %s", b.Currency, b.Total)
	}
	if free.IsNegative() {
		return fmt.Errorf("account balance %s: negative free %s", b.Currency, free)
	}
	if b.Locked.IsNegative() {
		return fmt.Errorf("account balance %s: negative locked %s", b.Currency, b.Locked)
	}
	if b.Borrowed.IsNegative() {
		return fmt.Errorf("account balance %s: negative borrowed %s", b.Currency, b.Borrowed)
	}
	if b.Interest.IsNegative() {
		return fmt.Errorf("account balance %s: negative interest %s", b.Currency, b.Interest)
	}
	if b.Borrowed.IsZero() && b.Interest.IsZero() && !b.CashInvariantOK() {
		return fmt.Errorf("account balance %s: cash invariant failed", b.Currency)
	}
	return nil
}

type MarginBalance struct {
	Currency     string
	InstrumentID *InstrumentID
	Initial      decimal.Decimal
	Maintenance  decimal.Decimal
	UpdatedAt    time.Time
}

func (m MarginBalance) Validate() error {
	if m.Currency == "" {
		return fmt.Errorf("margin balance: currency required")
	}
	if m.Initial.IsNegative() {
		return fmt.Errorf("margin balance %s: negative initial %s", m.Currency, m.Initial)
	}
	if m.Maintenance.IsNegative() {
		return fmt.Errorf("margin balance %s: negative maintenance %s", m.Currency, m.Maintenance)
	}
	return nil
}

type MarginRequirement struct {
	Currency    string
	Initial     decimal.Decimal
	Maintenance decimal.Decimal
}

type AccountState struct {
	AccountID    string
	Venue        string
	Type         AccountType
	BaseCurrency string
	Balances     []AccountBalance
	Margins      []MarginBalance
	Reported     bool
	EventID      EventID
	TsEvent      time.Time
	TsInit       time.Time
}

func AccountStateEventID(venue, accountID string, ts time.Time) EventID {
	return EventID(joinAccountStateEventID("account", "state", venue, accountID, ts.Format(time.RFC3339Nano)))
}

func joinAccountStateEventID(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, strings.ReplaceAll(part, "|", "/"))
		}
	}
	return strings.Join(out, "|")
}

func CloneMarginBalance(m MarginBalance) MarginBalance {
	if m.InstrumentID != nil {
		id := *m.InstrumentID
		m.InstrumentID = &id
	}
	return m
}

func CloneAccountState(s AccountState) AccountState {
	s.Balances = append([]AccountBalance(nil), s.Balances...)
	margins := s.Margins
	s.Margins = make([]MarginBalance, 0, len(margins))
	for _, margin := range margins {
		s.Margins = append(s.Margins, CloneMarginBalance(margin))
	}
	return s
}

func (s AccountState) Validate() error {
	if s.AccountID == "" {
		return fmt.Errorf("account state: account id required")
	}
	if s.Venue == "" {
		return fmt.Errorf("account state %s: venue required", s.AccountID)
	}
	if !s.Type.Valid() {
		return fmt.Errorf("account state %s: invalid type %s", s.AccountID, s.Type)
	}
	for _, bal := range s.Balances {
		if s.Type == AccountCash {
			if err := bal.ValidateCash(); err != nil {
				return err
			}
			continue
		}
		if bal.Currency == "" {
			return fmt.Errorf("account balance: currency required")
		}
	}
	for _, margin := range s.Margins {
		if err := margin.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s AccountState) ValidateTradingReady(f AccountFreshness, now time.Time) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if !s.Reported {
		return fmt.Errorf("account state %s: reported state required", s.AccountID)
	}
	if s.EventID == "" {
		return fmt.Errorf("account state %s: event id required", s.AccountID)
	}
	if s.TsEvent.IsZero() {
		return fmt.Errorf("account state %s: event timestamp required", s.AccountID)
	}
	if s.TsInit.IsZero() {
		return fmt.Errorf("account state %s: init timestamp required", s.AccountID)
	}
	if err := f.ValidateTradingReady(now); err != nil {
		return err
	}
	return nil
}

type AccountFreshness struct {
	LastAccountStateAt time.Time
	LastReconciledAt   time.Time
	StaleAfter         time.Duration
}

func (f AccountFreshness) LastFreshAt() time.Time {
	if f.LastReconciledAt.After(f.LastAccountStateAt) {
		return f.LastReconciledAt
	}
	return f.LastAccountStateAt
}

func (f AccountFreshness) Age(now time.Time) time.Duration {
	last := f.LastFreshAt()
	if last.IsZero() {
		return 0
	}
	return now.Sub(last)
}

func (f AccountFreshness) IsFresh(now time.Time) bool {
	if f.StaleAfter <= 0 || f.LastFreshAt().IsZero() {
		return false
	}
	return f.Age(now) <= f.StaleAfter
}

func (f AccountFreshness) ValidateTradingReady(now time.Time) error {
	if f.StaleAfter <= 0 {
		return fmt.Errorf("account freshness: stale-after must be positive")
	}
	if f.LastFreshAt().IsZero() {
		return fmt.Errorf("account freshness: last account state or reconciliation time required")
	}
	if !f.IsFresh(now) {
		return fmt.Errorf("account freshness: stale account state age %s exceeds %s", f.Age(now), f.StaleAfter)
	}
	return nil
}

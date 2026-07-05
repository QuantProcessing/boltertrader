package model

import (
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

const (
	AccountIDBinanceSpot = "BINANCE:spot"
	AccountIDBinanceUSDM = "BINANCE:usdm"
	AccountIDOKXSpot     = "OKX:spot"
	AccountIDOKXSwap     = "OKX:swap"
)

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

type AccountModeInfo struct {
	Venue          string
	AccountID      string
	AccountMode    string
	MarginMode     string
	PositionMode   string
	CollateralMode string
	ProductScope   []enums.InstrumentKind
	Verified       bool
	VerifiedAt     time.Time
	Source         string
	Details        map[string]string
}

func (m AccountModeInfo) ValidateVerified() error {
	if m.Venue == "" {
		return fmt.Errorf("account mode info: venue required")
	}
	if m.AccountID == "" {
		return fmt.Errorf("account mode info: account id required")
	}
	if !m.Verified {
		return fmt.Errorf("account mode info %s: not verified", m.AccountID)
	}
	if m.VerifiedAt.IsZero() {
		return fmt.Errorf("account mode info %s: verified time required", m.AccountID)
	}
	if m.Source == "" {
		return fmt.Errorf("account mode info %s: source required", m.AccountID)
	}
	if len(m.ProductScope) == 0 {
		return fmt.Errorf("account mode info %s: product scope required", m.AccountID)
	}
	return nil
}

type AccountState struct {
	AccountID    string
	Venue        string
	Type         AccountType
	BaseCurrency string
	Balances     []AccountBalance
	Margins      []MarginBalance
	ModeInfo     AccountModeInfo
	Reported     bool
	TsEvent      time.Time
	TsInit       time.Time
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
	s.ModeInfo.ProductScope = append([]enums.InstrumentKind(nil), s.ModeInfo.ProductScope...)
	if s.ModeInfo.Details != nil {
		details := make(map[string]string, len(s.ModeInfo.Details))
		for k, v := range s.ModeInfo.Details {
			details[k] = v
		}
		s.ModeInfo.Details = details
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
	if s.ModeInfo.AccountID != "" && s.ModeInfo.AccountID != s.AccountID {
		return fmt.Errorf("account state %s: mode account id mismatch %s", s.AccountID, s.ModeInfo.AccountID)
	}
	if s.ModeInfo.Venue != "" && s.ModeInfo.Venue != s.Venue {
		return fmt.Errorf("account state %s: mode venue mismatch %s", s.AccountID, s.ModeInfo.Venue)
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
	if err := s.ModeInfo.ValidateVerified(); err != nil {
		return err
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

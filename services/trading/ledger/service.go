package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrEntryConflict           = errors.New("ledger entry conflict")
	ErrCurrencyMismatch        = errors.New("ledger PnL currency mismatch")
	ErrSupersessionUnsupported = errors.New("ledger funding supersession unsupported")
)

type Kind string

const (
	KindRealizedPnL Kind = "realized_pnl"
	KindFee         Kind = "fee"
	KindFunding     Kind = "funding"
	KindAdjustment  Kind = "adjustment"
)

type Entry struct {
	PrincipalID  string    `json:"-"`
	EntryID      string    `json:"entryId"`
	LineageID    string    `json:"lineageId"`
	ProductID    string    `json:"productId"`
	AccountID    string    `json:"accountId"`
	InstrumentID string    `json:"instrumentId"`
	Kind         Kind      `json:"kind"`
	Amount       string    `json:"amount"`
	Currency     string    `json:"currency"`
	OccurredAt   time.Time `json:"occurredAt"`
}

type Unrealized struct {
	PrincipalID, LineageID, ProductID, AccountID, InstrumentID string
	Amount, Currency                                           string
	AsOf                                                       time.Time
}

type Query struct {
	PrincipalID                                   string
	ProductID, LineageID, AccountID, InstrumentID string
}

type PnLSnapshot struct {
	PrincipalID   string    `json:"-"`
	PnLSnapshotID string    `json:"pnlSnapshotId"`
	LineageID     string    `json:"lineageId"`
	ProductID     string    `json:"productId"`
	AccountID     string    `json:"accountId"`
	InstrumentID  string    `json:"instrumentId"`
	RealizedPnL   string    `json:"realizedPnl"`
	UnrealizedPnL string    `json:"unrealizedPnl"`
	Fees          string    `json:"fees"`
	Funding       string    `json:"funding"`
	NetPnL        string    `json:"netPnl"`
	AsOf          time.Time `json:"asOf"`
}

type Store interface {
	Append(context.Context, Entry) error
	Entries(context.Context, Query) ([]Entry, error)
	SetUnrealized(context.Context, Unrealized) error
	Unrealized(context.Context, Query) (decimal.Decimal, string, time.Time, error)
}

type Service struct {
	store Store
	now   func() time.Time
	next  atomic.Uint64
}

func NewService(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

func (service *Service) Record(ctx context.Context, entry Entry) error {
	if err := validateEntry(entry); err != nil {
		return err
	}
	return service.store.Append(ctx, entry)
}

func (service *Service) SupersedeFunding(ctx context.Context, priorEntryID string, replacement Entry) error {
	if priorEntryID == "" || replacement.EntryID == priorEntryID || replacement.Kind != KindFunding {
		return fmt.Errorf("invalid funding supersession")
	}
	if err := validateEntry(replacement); err != nil {
		return err
	}
	store, ok := service.store.(interface {
		SupersedeFunding(context.Context, string, Entry) error
	})
	if !ok {
		return ErrSupersessionUnsupported
	}
	return store.SupersedeFunding(ctx, priorEntryID, replacement)
}

func (service *Service) SetUnrealized(ctx context.Context, value Unrealized) error {
	amount, err := decimal.NewFromString(value.Amount)
	if err != nil || value.PrincipalID == "" || value.LineageID == "" || value.ProductID == "" || value.AccountID == "" || value.InstrumentID == "" || value.Currency == "" || value.AsOf.IsZero() {
		return fmt.Errorf("invalid unrealized attribution")
	}
	value.Amount = amount.String()
	return service.store.SetUnrealized(ctx, value)
}

func (service *Service) PnL(ctx context.Context, query Query) (PnLSnapshot, error) {
	if query.PrincipalID == "" {
		return PnLSnapshot{}, fmt.Errorf("pnl query principal is required")
	}
	if query.ProductID == "" || query.LineageID == "" || query.AccountID == "" || query.InstrumentID == "" {
		return PnLSnapshot{}, fmt.Errorf("pnl query attribution is incomplete")
	}
	entries, err := service.store.Entries(ctx, query)
	if err != nil {
		return PnLSnapshot{}, err
	}
	realized, fees, funding := decimal.Zero, decimal.Zero, decimal.Zero
	currency := ""
	for _, entry := range entries {
		if currency == "" {
			currency = entry.Currency
		} else if entry.Currency != currency {
			return PnLSnapshot{}, ErrCurrencyMismatch
		}
		amount, _ := decimal.NewFromString(entry.Amount)
		switch entry.Kind {
		case KindRealizedPnL, KindAdjustment:
			realized = realized.Add(amount)
		case KindFee:
			fees = fees.Add(amount)
		case KindFunding:
			funding = funding.Add(amount)
		}
	}
	unrealized, unrealizedCurrency, asOf, err := service.store.Unrealized(ctx, query)
	if err != nil {
		return PnLSnapshot{}, err
	}
	if unrealizedCurrency != "" {
		if currency == "" {
			currency = unrealizedCurrency
		} else if unrealizedCurrency != currency {
			return PnLSnapshot{}, ErrCurrencyMismatch
		}
	}
	if asOf.IsZero() {
		asOf = service.now().UTC()
	}
	return PnLSnapshot{
		PnLSnapshotID: fmt.Sprintf("pnl_%016x", service.next.Add(1)),
		PrincipalID:   query.PrincipalID,
		LineageID:     query.LineageID, ProductID: query.ProductID, AccountID: query.AccountID, InstrumentID: query.InstrumentID,
		RealizedPnL: realized.String(), UnrealizedPnL: unrealized.String(), Fees: fees.String(), Funding: funding.String(),
		NetPnL: realized.Add(unrealized).Add(funding).Sub(fees).String(), AsOf: asOf,
	}, nil
}

func (service *Service) List(ctx context.Context, query Query) ([]Entry, error) {
	if query.PrincipalID == "" {
		return nil, fmt.Errorf("invalid ledger query scope")
	}
	return service.store.Entries(ctx, query)
}

func validateEntry(entry Entry) error {
	if entry.PrincipalID == "" || entry.EntryID == "" || entry.LineageID == "" || entry.ProductID == "" || entry.AccountID == "" || entry.InstrumentID == "" || entry.Currency == "" || entry.OccurredAt.IsZero() {
		return fmt.Errorf("ledger attribution is incomplete")
	}
	switch entry.Kind {
	case KindRealizedPnL, KindFee, KindFunding, KindAdjustment:
	default:
		return fmt.Errorf("invalid ledger kind")
	}
	amount, err := decimal.NewFromString(entry.Amount)
	if err != nil {
		return fmt.Errorf("amount must be a decimal")
	}
	if entry.Kind == KindFee && amount.IsNegative() {
		return fmt.Errorf("fee cannot be negative")
	}
	return nil
}

type MemoryStore struct {
	mu         sync.Mutex
	entries    map[string]Entry
	unrealized map[string]Unrealized
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[string]Entry), unrealized: make(map[string]Unrealized)}
}

func (store *MemoryStore) Append(_ context.Context, entry Entry) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := ledgerKey(entry.PrincipalID, entry.EntryID)
	if existing, ok := store.entries[key]; ok {
		if existing != entry {
			return ErrEntryConflict
		}
		return nil
	}
	store.entries[key] = entry
	return nil
}

func (store *MemoryStore) SupersedeFunding(_ context.Context, priorEntryID string, replacement Entry) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	replacementKey := ledgerKey(replacement.PrincipalID, replacement.EntryID)
	priorKey := ledgerKey(replacement.PrincipalID, priorEntryID)
	if existing, ok := store.entries[replacementKey]; ok {
		if existing != replacement {
			return ErrEntryConflict
		}
		if prior, restored := store.entries[priorKey]; restored {
			if !sameFundingScope(prior, replacement) {
				return ErrEntryConflict
			}
			delete(store.entries, priorKey)
		}
		return nil
	}
	prior, ok := store.entries[priorKey]
	if !ok || !sameFundingScope(prior, replacement) {
		return ErrEntryConflict
	}
	delete(store.entries, priorKey)
	store.entries[replacementKey] = replacement
	return nil
}

func (store *MemoryStore) Entries(_ context.Context, query Query) ([]Entry, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]Entry, 0)
	for _, entry := range store.entries {
		if entry.PrincipalID == query.PrincipalID && matches(query, entry.ProductID, entry.LineageID, entry.AccountID, entry.InstrumentID) {
			result = append(result, entry)
		}
	}
	return result, nil
}

func (store *MemoryStore) SetUnrealized(_ context.Context, value Unrealized) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := ledgerKey(value.PrincipalID, value.ProductID, value.LineageID, value.AccountID, value.InstrumentID)
	if existing, ok := store.unrealized[key]; ok && !existing.AsOf.Before(value.AsOf) {
		return nil
	}
	store.unrealized[key] = value
	return nil
}

func (store *MemoryStore) Unrealized(_ context.Context, query Query) (decimal.Decimal, string, time.Time, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.unrealized[ledgerKey(query.PrincipalID, query.ProductID, query.LineageID, query.AccountID, query.InstrumentID)]
	if !ok {
		return decimal.Zero, "", time.Time{}, nil
	}
	amount, _ := decimal.NewFromString(value.Amount)
	return amount, value.Currency, value.AsOf, nil
}

func matches(query Query, product, lineage, account, instrument string) bool {
	return (query.ProductID == "" || query.ProductID == product) && (query.LineageID == "" || query.LineageID == lineage) &&
		(query.AccountID == "" || query.AccountID == account) && (query.InstrumentID == "" || query.InstrumentID == instrument)
}

func ledgerKey(parts ...string) string { return strings.Join(parts, "\x00") }

func sameFundingScope(left, right Entry) bool {
	return left.Kind == KindFunding && right.Kind == KindFunding && left.ProductID == right.ProductID &&
		left.PrincipalID == right.PrincipalID && left.LineageID == right.LineageID && left.AccountID == right.AccountID && left.InstrumentID == right.InstrumentID && left.Currency == right.Currency
}

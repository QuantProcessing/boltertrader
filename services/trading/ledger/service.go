package ledger

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrEntryConflict           = errors.New("ledger entry conflict")
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

// CurrencyPnL keeps costs, rebates and earnings in their original currency.
// BookedNetPnL excludes unobserved valuation; NetPnL exists only when an
// unrealized observation is available for this instrument.
type CurrencyPnL struct {
	Currency      string `json:"currency"`
	RealizedPnL   string `json:"realizedPnl"`
	Fees          string `json:"fees"`
	Funding       string `json:"funding"`
	BookedNetPnL  string `json:"bookedNetPnl"`
	UnrealizedPnL string `json:"unrealizedPnl,omitempty"`
	NetPnL        string `json:"netPnl,omitempty"`
}
type PnLSnapshot struct {
	PrincipalID   string        `json:"-"`
	LineageID     string        `json:"lineageId"`
	ProductID     string        `json:"productId"`
	AccountID     string        `json:"accountId"`
	InstrumentID  string        `json:"instrumentId"`
	ByCurrency    []CurrencyPnL `json:"byCurrency"`
	AsOf          time.Time     `json:"asOf"`
	ValuationAsOf *time.Time    `json:"valuationAsOf,omitempty"`
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
	type totals struct{ realized, fees, funding decimal.Decimal }
	byCurrency := map[string]totals{}
	asOf := time.Time{}
	for _, entry := range entries {
		amount, parseErr := decimal.NewFromString(entry.Amount)
		if parseErr != nil || entry.Currency == "" {
			return PnLSnapshot{}, fmt.Errorf("invalid persisted ledger amount")
		}
		total := byCurrency[entry.Currency]
		switch entry.Kind {
		case KindRealizedPnL, KindAdjustment:
			total.realized = total.realized.Add(amount)
		case KindFee:
			total.fees = total.fees.Add(amount)
		case KindFunding:
			total.funding = total.funding.Add(amount)
		default:
			return PnLSnapshot{}, fmt.Errorf("invalid persisted ledger kind")
		}
		byCurrency[entry.Currency] = total
		if entry.OccurredAt.After(asOf) {
			asOf = entry.OccurredAt
		}
	}
	unrealized, unrealizedCurrency, markedAt, err := service.store.Unrealized(ctx, query)
	if err != nil {
		return PnLSnapshot{}, err
	}
	valued := unrealizedCurrency != "" && !markedAt.IsZero()
	if valued {
		if _, exists := byCurrency[unrealizedCurrency]; !exists {
			byCurrency[unrealizedCurrency] = totals{}
		}
		if markedAt.After(asOf) {
			asOf = markedAt
		}
	}
	currencies := make([]string, 0, len(byCurrency))
	for currency := range byCurrency {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	snapshot := PnLSnapshot{PrincipalID: query.PrincipalID, LineageID: query.LineageID, ProductID: query.ProductID,
		AccountID: query.AccountID, InstrumentID: query.InstrumentID, ByCurrency: []CurrencyPnL{}, AsOf: asOf}
	if valued {
		snapshot.ValuationAsOf = &markedAt
	}
	if snapshot.AsOf.IsZero() {
		snapshot.AsOf = service.now().UTC()
	}
	for _, currency := range currencies {
		total := byCurrency[currency]
		booked := total.realized.Add(total.funding).Sub(total.fees)
		item := CurrencyPnL{Currency: currency, RealizedPnL: total.realized.String(), Fees: total.fees.String(), Funding: total.funding.String(), BookedNetPnL: booked.String()}
		if valued {
			mark := decimal.Zero
			if currency == unrealizedCurrency {
				mark = unrealized
			}
			item.UnrealizedPnL, item.NetPnL = mark.String(), booked.Add(mark).String()
		}
		snapshot.ByCurrency = append(snapshot.ByCurrency, item)
	}
	return snapshot, nil
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
	_, err := decimal.NewFromString(entry.Amount)
	if err != nil {
		return fmt.Errorf("amount must be a decimal")
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
	key := ledgerKey(entry.PrincipalID, entry.AccountID, entry.InstrumentID, entry.EntryID)
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
	replacementKey := ledgerKey(replacement.PrincipalID, replacement.AccountID, replacement.InstrumentID, replacement.EntryID)
	priorKey := ledgerKey(replacement.PrincipalID, replacement.AccountID, replacement.InstrumentID, priorEntryID)
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

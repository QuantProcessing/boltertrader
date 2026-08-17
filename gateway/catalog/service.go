package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/shopspring/decimal"
)

var (
	ErrCatalogUnavailable = errors.New("catalog unavailable")
	ErrCatalogStale       = errors.New("catalog stale")
	ErrInvalidSnapshot    = errors.New("invalid catalog snapshot")
)

const (
	ReasonCatalogUnavailable = "catalog_unavailable"
	ReasonCatalogStale       = "catalog_stale"
	ReasonRefreshFailed      = "refresh_failed"
	ReasonInvalidSnapshot    = "invalid_snapshot"
)

// Scope is the public-data boundary whose instruments may be shared between
// accounts. Credentials and account identity are deliberately absent.
type Scope struct {
	Venue       exchange.Venue       `json:"venue"`
	Environment exchange.Environment `json:"environment"`
	Product     exchange.Product     `json:"product"`
	Settlement  string               `json:"settlement,omitempty"`
}

func (scope Scope) validate() error {
	switch scope.Venue {
	case exchange.VenueBinance, exchange.VenueOKX, exchange.VenueLighter,
		exchange.VenueHyperliquid, exchange.VenueBybit, exchange.VenueBitget,
		exchange.VenueGate, exchange.VenueAster, exchange.VenueNado:
	default:
		return fmt.Errorf("%w: unknown venue", ErrInvalidSnapshot)
	}
	if scope.Environment != exchange.EnvironmentLive {
		return fmt.Errorf("%w: unknown environment", ErrInvalidSnapshot)
	}
	if scope.Product != exchange.ProductSpot && scope.Product != exchange.ProductPerp {
		return fmt.Errorf("%w: unknown product", ErrInvalidSnapshot)
	}
	if scope.Settlement != strings.TrimSpace(scope.Settlement) ||
		scope.Settlement != strings.ToUpper(scope.Settlement) ||
		strings.ContainsAny(scope.Settlement, ":-/\n\r\t") {
		return fmt.Errorf("%w: invalid settlement", ErrInvalidSnapshot)
	}
	return nil
}

// Instrument is the safe gateway projection of an exchange Instrument. Native
// symbols, native IDs, transport encodings, and credential-bearing metadata
// never cross this boundary.
type Instrument struct {
	ID          domain.InstrumentID  `json:"instrument_id"`
	Venue       exchange.Venue       `json:"venue"`
	Environment exchange.Environment `json:"environment"`
	Product     exchange.Product     `json:"product"`
	Settlement  string               `json:"settlement,omitempty"`
	Symbol      string               `json:"symbol"`
	BaseAsset   string               `json:"base_asset"`
	QuoteAsset  string               `json:"quote_asset"`
	Rules       Rules                `json:"rules"`
}

type Rules struct {
	PriceIncrement    decimal.Decimal          `json:"price_increment"`
	QuantityIncrement decimal.Decimal          `json:"quantity_increment"`
	MinQuantity       decimal.Decimal          `json:"min_quantity"`
	MinNotional       exchange.OptionalDecimal `json:"min_notional"`
}

type Snapshot struct {
	Scope       Scope        `json:"scope"`
	Revision    string       `json:"revision"`
	Generation  uint64       `json:"generation"`
	LoadedAt    time.Time    `json:"loaded_at"`
	Instruments []Instrument `json:"instruments"`
}

type Status struct {
	Scope      Scope         `json:"scope"`
	Revision   string        `json:"revision,omitempty"`
	Generation uint64        `json:"generation"`
	LoadedAt   time.Time     `json:"loaded_at,omitempty"`
	Age        time.Duration `json:"age"`
	Stale      bool          `json:"stale"`
	ReadReady  bool          `json:"read_ready"`
	WriteReady bool          `json:"write_ready"`
	Reason     string        `json:"reason,omitempty"`
	LastError  string        `json:"last_error,omitempty"`
}

type Service struct {
	mu       sync.RWMutex
	maxAge   time.Duration
	now      func() time.Time
	accounts map[domain.AccountID]Scope
	scopes   map[Scope]*scopeState
}

type scopeState struct {
	snapshot  Snapshot
	canonical map[domain.InstrumentID]exchange.Instrument
	lastError string
}

func NewService(maxAge time.Duration, now func() time.Time) (*Service, error) {
	if maxAge <= 0 {
		return nil, fmt.Errorf("catalog max age must be positive")
	}
	if now == nil {
		return nil, fmt.Errorf("catalog clock is required")
	}
	return &Service{
		maxAge:   maxAge,
		now:      now,
		accounts: make(map[domain.AccountID]Scope),
		scopes:   make(map[Scope]*scopeState),
	}, nil
}

func (service *Service) RegisterAccount(accountID domain.AccountID, scope Scope) error {
	return service.RegisterAccounts(map[domain.AccountID]Scope{accountID: scope})
}

// RegisterAccounts atomically binds accounts to credential-free public scopes.
// A conflict rejects the whole batch, so failed manager startup cannot leave a
// partially registered desired state.
func (service *Service) RegisterAccounts(accounts map[domain.AccountID]Scope) error {
	for accountID, scope := range accounts {
		if err := accountID.Validate(); err != nil {
			return fmt.Errorf("invalid account: %w", err)
		}
		if err := scope.validate(); err != nil {
			return err
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	for accountID, scope := range accounts {
		if current, ok := service.accounts[accountID]; ok && current != scope {
			return fmt.Errorf("account is already registered with another catalog scope")
		}
	}
	for accountID, scope := range accounts {
		service.accounts[accountID] = scope
		if _, ok := service.scopes[scope]; !ok {
			service.scopes[scope] = &scopeState{}
		}
	}
	return nil
}

func (service *Service) Publish(accountID domain.AccountID, instruments []exchange.Instrument) (Snapshot, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	scope, ok := service.accounts[accountID]
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: account is not registered", ErrCatalogUnavailable)
	}
	public, canonical, revision, err := projectSnapshot(scope, instruments)
	state := service.scopes[scope]
	if err != nil {
		state.lastError = ReasonInvalidSnapshot
		return Snapshot{}, err
	}
	state.snapshot = Snapshot{
		Scope:       scope,
		Revision:    revision,
		Generation:  state.snapshot.Generation + 1,
		LoadedAt:    service.now().UTC(),
		Instruments: public,
	}
	state.canonical = canonical
	state.lastError = ""
	return cloneSnapshot(state.snapshot), nil
}

func (service *Service) Snapshot(accountID domain.AccountID) (Snapshot, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	state, _, ok := service.accountState(accountID)
	if !ok || state.snapshot.Generation == 0 {
		return Snapshot{}, fmt.Errorf("%w: no snapshot for account", ErrCatalogUnavailable)
	}
	return cloneSnapshot(state.snapshot), nil
}

func (service *Service) Resolve(ctx context.Context, accountID domain.AccountID, instrumentID domain.InstrumentID) (exchange.Instrument, error) {
	if ctx == nil {
		return exchange.Instrument{}, fmt.Errorf("catalog resolve context is required")
	}
	if err := ctx.Err(); err != nil {
		return exchange.Instrument{}, err
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	state, _, ok := service.accountState(accountID)
	if !ok || state.snapshot.Generation == 0 {
		return exchange.Instrument{}, fmt.Errorf("%w: no snapshot for account", ErrCatalogUnavailable)
	}
	if service.isStale(state.snapshot) {
		return exchange.Instrument{}, ErrCatalogStale
	}
	instrument, ok := state.canonical[instrumentID]
	if !ok {
		return exchange.Instrument{}, fmt.Errorf("%w: instrument is not in account scope", ErrCatalogUnavailable)
	}
	return instrument, nil
}

// MarkRefreshError records only a stable reason code and retains the last
// known-good snapshot. The original venue error must never be stored here.
func (service *Service) MarkRefreshError(accountID domain.AccountID) {
	service.mu.Lock()
	defer service.mu.Unlock()
	state, _, ok := service.accountState(accountID)
	if ok {
		state.lastError = ReasonRefreshFailed
	}
}

func (service *Service) Status(accountID domain.AccountID) Status {
	service.mu.RLock()
	defer service.mu.RUnlock()
	state, scope, ok := service.accountState(accountID)
	if !ok || state.snapshot.Generation == 0 {
		return Status{Scope: scope, Reason: ReasonCatalogUnavailable, LastError: ReasonCatalogUnavailable}
	}
	stale := service.isStale(state.snapshot)
	age := service.now().UTC().Sub(state.snapshot.LoadedAt)
	if age < 0 {
		age = 0
	}
	reason := state.lastError
	if stale {
		reason = ReasonCatalogStale
	}
	return Status{
		Scope:      scope,
		Revision:   state.snapshot.Revision,
		Generation: state.snapshot.Generation,
		LoadedAt:   state.snapshot.LoadedAt,
		Age:        age,
		Stale:      stale,
		ReadReady:  !stale,
		// Catalog freshness is necessary but not sufficient for command
		// readiness. Account ownership, capabilities, persistence, and
		// reconciliation are composed by gateway/account.
		WriteReady: false,
		Reason:     reason,
		LastError:  state.lastError,
	}
}

func (service *Service) accountState(accountID domain.AccountID) (*scopeState, Scope, bool) {
	scope, ok := service.accounts[accountID]
	if !ok {
		return nil, Scope{}, false
	}
	state, ok := service.scopes[scope]
	return state, scope, ok
}

func (service *Service) isStale(snapshot Snapshot) bool {
	return service.now().UTC().Sub(snapshot.LoadedAt) > service.maxAge
}

func projectSnapshot(scope Scope, instruments []exchange.Instrument) ([]Instrument, map[domain.InstrumentID]exchange.Instrument, string, error) {
	if len(instruments) == 0 {
		return nil, nil, "", fmt.Errorf("%w: instruments are required", ErrInvalidSnapshot)
	}
	type projected struct {
		id        domain.InstrumentID
		public    Instrument
		canonical exchange.Instrument
	}
	rows := make([]projected, 0, len(instruments))
	seen := make(map[domain.InstrumentID]struct{}, len(instruments))
	for _, instrument := range instruments {
		if err := instrument.Validate(); err != nil {
			return nil, nil, "", fmt.Errorf("%w: invalid instrument", ErrInvalidSnapshot)
		}
		identity := instrument.Identity
		if identity.Venue != scope.Venue || identity.Product != scope.Product || identity.SettleAsset != scope.Settlement {
			return nil, nil, "", fmt.Errorf("%w: instrument does not match scope", ErrInvalidSnapshot)
		}
		id, err := domain.NewInstrumentID(string(scope.Venue), string(scope.Environment), string(scope.Product), scope.Settlement, identity.Symbol)
		if err != nil {
			return nil, nil, "", fmt.Errorf("%w: invalid gateway instrument identity", ErrInvalidSnapshot)
		}
		if _, exists := seen[id]; exists {
			return nil, nil, "", fmt.Errorf("%w: duplicate instrument identity", ErrInvalidSnapshot)
		}
		seen[id] = struct{}{}
		rows = append(rows, projected{
			id: id,
			public: Instrument{
				ID: id, Venue: scope.Venue, Environment: scope.Environment, Product: scope.Product,
				Settlement: scope.Settlement, Symbol: identity.Symbol, BaseAsset: identity.BaseAsset, QuoteAsset: identity.QuoteAsset,
				Rules: Rules{
					PriceIncrement: instrument.Rules.PriceIncrement, QuantityIncrement: instrument.Rules.QuantityIncrement,
					MinQuantity: instrument.Rules.MinQuantity, MinNotional: instrument.Rules.MinNotional,
				},
			},
			canonical: instrument,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	public := make([]Instrument, len(rows))
	canonical := make(map[domain.InstrumentID]exchange.Instrument, len(rows))
	hashRows := make([]exchange.Instrument, len(rows))
	for index, row := range rows {
		public[index] = row.public
		canonical[row.id] = row.canonical
		hashRows[index] = row.canonical
	}
	payload, err := json.Marshal(struct {
		Scope       Scope                 `json:"scope"`
		Instruments []exchange.Instrument `json:"instruments"`
	}{Scope: scope, Instruments: hashRows})
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: snapshot revision", ErrInvalidSnapshot)
	}
	sum := sha256.Sum256(payload)
	return public, canonical, hex.EncodeToString(sum[:]), nil
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := snapshot
	clone.Instruments = append([]Instrument(nil), snapshot.Instruments...)
	return clone
}

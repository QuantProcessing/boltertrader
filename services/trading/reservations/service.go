package reservations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrNotFound            = errors.New("reservation not found")
	ErrScopeOwned          = errors.New("netting scope is already reserved")
	ErrOwnership           = errors.New("reservation ownership mismatch")
	ErrNotHeld             = errors.New("reservation is not held")
	ErrIdempotencyConflict = errors.New("reservation idempotency conflict")
)

type Mode string

const (
	ModeTest Mode = "test"
	ModeLive Mode = "live"
)

type Kind string

const (
	KindOpen     Kind = "open"
	KindClose    Kind = "close"
	KindHedge    Kind = "hedge"
	KindTransfer Kind = "transfer"
)

type Status string

const (
	StatusHeld     Status = "held"
	StatusReleased Status = "released"
	StatusConsumed Status = "consumed"
	StatusExpired  Status = "expired"
	StatusBlocked  Status = "blocked"
)

type NettingScope struct {
	AccountID        string  `json:"accountId"`
	Venue            string  `json:"venue"`
	InstrumentID     string  `json:"instrumentId"`
	PositionMode     *string `json:"positionMode"`
	SettlementScope  *string `json:"settlementScope"`
	VenueEnvironment string  `json:"venueEnvironment"`
}

func (scope NettingScope) key(mode Mode) string {
	position, settlement := "", ""
	if scope.PositionMode != nil {
		position = *scope.PositionMode
	}
	if scope.SettlementScope != nil {
		settlement = *scope.SettlementScope
	}
	return strings.Join([]string{string(mode), scope.AccountID, scope.Venue, scope.InstrumentID, position, settlement, scope.VenueEnvironment}, "\x00")
}

type Reservation struct {
	ReservationID  string       `json:"reservationId"`
	IdempotencyKey string       `json:"-"`
	PayloadHash    string       `json:"-"`
	PrincipalID    string       `json:"-"`
	Mode           Mode         `json:"mode"`
	ProductID      string       `json:"productId"`
	Kind           Kind         `json:"reservationKind"`
	Scope          NettingScope `json:"nettingScope"`
	Quantity       *string      `json:"quantity"`
	Notional       *string      `json:"notional"`
	Currency       *string      `json:"currency"`
	Status         Status       `json:"status"`
	ExpiresAt      *time.Time   `json:"expiresAt"`
	BlockedReason  *string      `json:"blockedReason"`
	CreatedAt      time.Time    `json:"createdAt"`
	UpdatedAt      time.Time    `json:"updatedAt"`
}

type CreateRequest struct {
	PrincipalID    string
	Mode           Mode
	ProductID      string
	Kind           Kind
	Scope          NettingScope
	Quantity       string
	Notional       string
	Currency       string
	ExpiresAt      *time.Time
	IdempotencyKey string
}

type ConsumeRequest struct {
	ReservationID string
	PrincipalID   string
	Mode          Mode
	ProductID     string
}

type Store interface {
	Create(context.Context, Reservation) (Reservation, error)
	Get(context.Context, string) (Reservation, error)
	SetStatus(context.Context, string, Status, time.Time) (Reservation, error)
	List(context.Context, string, Mode) ([]Reservation, error)
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

func (service *Service) Create(ctx context.Context, request CreateRequest) (Reservation, error) {
	if err := request.validate(); err != nil {
		return Reservation{}, err
	}
	now := service.now().UTC()
	reservationID, err := newReservationID()
	if err != nil {
		return Reservation{}, err
	}
	reservation := Reservation{
		ReservationID:  reservationID,
		IdempotencyKey: request.IdempotencyKey, PayloadHash: request.hash(),
		PrincipalID: request.PrincipalID, Mode: request.Mode, ProductID: request.ProductID,
		Kind: request.Kind, Scope: request.Scope, Status: StatusHeld, ExpiresAt: request.ExpiresAt,
		CreatedAt: now, UpdatedAt: now,
	}
	if request.Quantity != "" {
		value := request.Quantity
		reservation.Quantity = &value
	}
	if request.Notional != "" {
		value, currency := request.Notional, request.Currency
		reservation.Notional, reservation.Currency = &value, &currency
	}
	return service.store.Create(ctx, reservation)
}

func newReservationID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate reservation id: %w", err)
	}
	return "reservation_" + hex.EncodeToString(entropy[:]), nil
}

func (service *Service) Release(ctx context.Context, principal string, mode Mode, id string) (Reservation, error) {
	reservation, err := service.store.Get(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if reservation.PrincipalID != principal || reservation.Mode != mode {
		return Reservation{}, ErrOwnership
	}
	if reservation.Status != StatusHeld {
		return Reservation{}, ErrNotHeld
	}
	return service.store.SetStatus(ctx, id, StatusReleased, service.now().UTC())
}

func (service *Service) Consume(ctx context.Context, request ConsumeRequest) (Reservation, error) {
	reservation, err := service.store.Get(ctx, request.ReservationID)
	if err != nil {
		return Reservation{}, err
	}
	if reservation.PrincipalID != request.PrincipalID || reservation.Mode != request.Mode || reservation.ProductID != request.ProductID {
		return Reservation{}, ErrOwnership
	}
	if reservation.Status != StatusHeld {
		return Reservation{}, ErrNotHeld
	}
	return service.store.SetStatus(ctx, reservation.ReservationID, StatusConsumed, service.now().UTC())
}

func (service *Service) List(ctx context.Context, principal string, mode Mode) ([]Reservation, error) {
	return service.store.List(ctx, principal, mode)
}

func (request CreateRequest) validate() error {
	if strings.TrimSpace(request.PrincipalID) == "" || strings.TrimSpace(request.ProductID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("principal and product are required")
	}
	if request.Mode != ModeTest && request.Mode != ModeLive {
		return fmt.Errorf("invalid mode")
	}
	switch request.Kind {
	case KindOpen, KindClose, KindHedge, KindTransfer:
	default:
		return fmt.Errorf("invalid reservation kind")
	}
	if request.Scope.AccountID == "" || request.Scope.Venue == "" || request.Scope.InstrumentID == "" {
		return fmt.Errorf("netting scope is incomplete")
	}
	switch request.Scope.VenueEnvironment {
	case "demo", "testnet", "live":
	default:
		return fmt.Errorf("invalid venue environment")
	}
	if (request.Mode == ModeTest && request.Scope.VenueEnvironment == "live") || (request.Mode == ModeLive && request.Scope.VenueEnvironment != "live") {
		return fmt.Errorf("venue environment does not match mode")
	}
	hasQuantity := request.Quantity != ""
	hasNotional := request.Notional != ""
	if hasQuantity == hasNotional {
		return fmt.Errorf("exactly one of quantity or notional is required")
	}
	if hasQuantity {
		quantity, err := decimal.NewFromString(request.Quantity)
		if err != nil || !quantity.IsPositive() {
			return fmt.Errorf("reservation amount must be a positive decimal")
		}
	}
	if hasNotional {
		if request.Currency == "" {
			return fmt.Errorf("currency is required for notional reservation")
		}
		notional, err := decimal.NewFromString(request.Notional)
		if err != nil || !notional.IsPositive() {
			return fmt.Errorf("reservation amount must be a positive decimal")
		}
	}
	return nil
}

func (request CreateRequest) hash() string {
	payload, _ := json.Marshal(struct {
		ProductID                    string       `json:"productId"`
		Kind                         Kind         `json:"kind"`
		Scope                        NettingScope `json:"scope"`
		Quantity, Notional, Currency string
		ExpiresAt                    *time.Time
	}{request.ProductID, request.Kind, request.Scope, request.Quantity, request.Notional, request.Currency, request.ExpiresAt})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Reservation
	scopes  map[string]string
	keys    map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Reservation), scopes: make(map[string]string), keys: make(map[string]string)}
}

func (store *MemoryStore) Create(_ context.Context, reservation Reservation) (Reservation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	idempotencyKey := strings.Join([]string{reservation.PrincipalID, string(reservation.Mode), reservation.IdempotencyKey}, "\x00")
	if id := store.keys[idempotencyKey]; id != "" {
		existing := store.records[id]
		if existing.PayloadHash != reservation.PayloadHash {
			return existing, ErrIdempotencyConflict
		}
		return existing, nil
	}
	key := reservation.Scope.key(reservation.Mode)
	if id := store.scopes[key]; id != "" {
		if existing := store.records[id]; existing.Status == StatusHeld {
			return Reservation{}, ErrScopeOwned
		}
	}
	store.records[reservation.ReservationID] = reservation
	store.scopes[key] = reservation.ReservationID
	store.keys[idempotencyKey] = reservation.ReservationID
	return reservation, nil
}

func (store *MemoryStore) Get(_ context.Context, id string) (Reservation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	reservation, ok := store.records[id]
	if !ok {
		return Reservation{}, ErrNotFound
	}
	return reservation, nil
}

func (store *MemoryStore) SetStatus(_ context.Context, id string, status Status, now time.Time) (Reservation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	reservation, ok := store.records[id]
	if !ok {
		return Reservation{}, ErrNotFound
	}
	reservation.Status, reservation.UpdatedAt = status, now
	store.records[id] = reservation
	return reservation, nil
}

func (store *MemoryStore) List(_ context.Context, principal string, mode Mode) ([]Reservation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]Reservation, 0)
	for _, reservation := range store.records {
		if reservation.PrincipalID == principal && reservation.Mode == mode {
			result = append(result, reservation)
		}
	}
	return result, nil
}

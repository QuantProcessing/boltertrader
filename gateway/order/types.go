package order

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/shopspring/decimal"
)

const ReasonRecoveryRequired = "order_recovery_required"

var (
	ErrWriteNotReady       = errors.New("account write not ready")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrOutcomeUncertain    = errors.New("order outcome uncertain")
	ErrRecoveryIncomplete  = errors.New("account recovery incomplete")
	ErrCoordinatorClosed   = errors.New("order coordinator closed")
)

type PlaceRequest struct {
	PrincipalID    domain.PrincipalID
	AccountID      domain.AccountID
	RouteID        domain.RouteID
	InstrumentID   domain.InstrumentID
	IdempotencyKey domain.IdempotencyKey
	CorrelationID  domain.EventID
	Side           exchange.Side
	Type           exchange.OrderType
	Quantity       decimal.Decimal
	LimitPrice     decimal.Decimal
	LimitPolicy    exchange.LimitPolicy
	ReduceOnly     bool
	Leverage       int
	WriteFence     account.WriteFence
}

func (request PlaceRequest) validateIdentity() error {
	if err := request.PrincipalID.Validate(); err != nil {
		return err
	}
	if err := request.AccountID.Validate(); err != nil {
		return err
	}
	if err := request.RouteID.Validate(); err != nil {
		return err
	}
	if err := request.InstrumentID.Validate(); err != nil {
		return err
	}
	if err := request.IdempotencyKey.Validate(); err != nil {
		return err
	}
	if request.CorrelationID != "" {
		if err := request.CorrelationID.Validate(); err != nil {
			return err
		}
	}
	fence := request.WriteFence
	if err := validateWriteFence(fence, request.AccountID); err != nil {
		return err
	}
	return nil
}

func validateWriteFence(fence account.WriteFence, accountID domain.AccountID) error {
	if fence.AccountID != accountID || fence.RuntimeID == "" {
		return fmt.Errorf("%w: order write fence is invalid", exchange.ErrInvalidRequest)
	}
	return nil
}

type Result struct {
	CommandID       domain.CommandID       `json:"command_id"`
	GatewayOrderID  domain.GatewayOrderID  `json:"gateway_order_id"`
	ClientOrderID   string                 `json:"client_order_id"`
	State           domain.OrderState      `json:"state"`
	ResourceVersion domain.ResourceVersion `json:"resource_version"`
	// VenueCode and VenueMessage are already-normalized, non-secret rejection
	// details. They are present only when the venue gave a definitive refusal.
	VenueCode    string              `json:"venue_code,omitempty"`
	VenueMessage string              `json:"venue_message,omitempty"`
	AccountID    domain.AccountID    `json:"-"`
	InstrumentID domain.InstrumentID `json:"-"`
	VenueOrderID string              `json:"-"`
	Replayed     bool                `json:"-"`
}

type Route struct {
	RouteID         domain.RouteID
	Driver          Driver
	Instrument      exchange.Instrument
	Product         exchange.Product
	WriteReady      bool
	Reason          string
	CheckWriteFence func(account.WriteFence) error
	Admission       HandoffAdmission
}

type HandoffAdmission interface {
	Acquire(domain.WriteFence) (account.HandoffLease, error)
}

func (route Route) validate() error {
	if route.Driver == nil {
		return fmt.Errorf("order driver is unavailable")
	}
	if err := route.Instrument.Validate(); err != nil {
		return fmt.Errorf("order instrument is unavailable")
	}
	if route.Product != route.Instrument.Identity.Product {
		return fmt.Errorf("order route product mismatch")
	}
	return nil
}

type Router interface {
	Route(context.Context, domain.AccountID, domain.InstrumentID) (Route, error)
}

type RecoveryRouter interface {
	RouteForRecovery(context.Context, domain.AccountID, domain.InstrumentID) (Route, error)
}

type FencedRecoveryRouter interface {
	RouteForRecoveryCommand(context.Context, domain.AccountID, domain.InstrumentID, domain.RouteID, domain.AccountFence) (Route, error)
}

type Driver interface {
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
	CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error)
	LookupExactOrder(context.Context, exchange.ExactOrderLookupRequest) (ExactResult, error)
}

// LeverageDriver is the perpetual-only precondition used by a durable order.
// It stays outside exchange.PlaceOrderRequest so that request remains the
// portable spot/perpetual order surface.
type LeverageDriver interface {
	SetLeverage(context.Context, exchange.Instrument, int) error
}

type CancelRequest struct {
	PrincipalID    domain.PrincipalID
	GatewayOrderID domain.GatewayOrderID
}

type ListRequest struct {
	PrincipalID  domain.PrincipalID
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID
	OpenOnly     bool
	Limit        int
	Cursor       domain.GatewayOrderID
}

type VenueOrderObservation struct {
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID
	Product      exchange.Product
	Order        exchange.Order
}

func (observation VenueOrderObservation) validate() error {
	if err := observation.AccountID.Validate(); err != nil {
		return err
	}
	if err := observation.InstrumentID.Validate(); err != nil {
		return err
	}
	switch observation.Product {
	case exchange.ProductSpot, exchange.ProductPerp:
	default:
		return fmt.Errorf("venue order product is invalid")
	}
	if observation.Order.ClientOrderID == "" && observation.Order.OrderID == "" {
		return fmt.Errorf("venue order identity is required")
	}
	if observation.Order.Status == "" {
		return fmt.Errorf("venue order status is required")
	}
	return nil
}

type ExactResult struct {
	Status   exchange.ExactOrderLookupStatus
	State    domain.OrderState
	Order    exchange.Order
	Evidence exchange.ExactOrderEvidence
}

func (result ExactResult) validate() error {
	exchangeResult := exchange.ExactOrderLookupResult{
		Status:   result.Status,
		Order:    result.Order,
		Evidence: result.Evidence,
	}
	if err := exchangeResult.Validate(); err != nil {
		return fmt.Errorf("invalid exact order evidence")
	}
	if result.Status == exchange.ExactOrderFound {
		switch result.State {
		case domain.StateAccepted, domain.StateOpen, domain.StatePartiallyFilled,
			domain.StateFilled, domain.StateCanceled, domain.StateRejected:
		default:
			return fmt.Errorf("invalid exact order state")
		}
	} else if result.State != "" {
		return fmt.Errorf("non-found exact evidence has order state")
	}
	return nil
}

type Publisher interface {
	PublishCommitted(context.Context, store.StoredCommand) error
}

type Hooks struct {
	AfterPrepared    func(context.Context, store.StoredCommand) error
	AfterDispatching func(context.Context, store.StoredCommand) error
	AfterVenue       func(context.Context, store.StoredCommand) error
	AfterCommitted   func(context.Context, store.StoredCommand) error
}

type Options struct {
	Store                     store.CommandStore
	Router                    Router
	Publisher                 Publisher
	Identity                  *domain.IdentityGenerator
	Hooks                     Hooks
	Now                       func() time.Time
	ReconciliationDelay       time.Duration
	ReconciliationInterval    time.Duration
	ReconciliationLease       time.Duration
	ReconciliationLimit       int
	MaxReconciliationAttempts int
}

type RecoveryReport struct {
	AccountID  domain.AccountID
	Examined   int
	Resolved   int
	Unresolved int
	WriteReady bool
}

type ReconciliationReport struct {
	AccountID  domain.AccountID
	Claimed    int
	Resolved   int
	Unresolved int
	WriteReady bool
}

type AccountWriteStatus struct {
	WriteReady bool
	Reason     string
}

type Coordinator struct {
	store                     store.CommandStore
	router                    Router
	publisher                 Publisher
	identity                  *domain.IdentityGenerator
	hooks                     Hooks
	now                       func() time.Time
	reconciliationDelay       time.Duration
	reconciliationInterval    time.Duration
	reconciliationLease       time.Duration
	reconciliationLimit       int
	maxReconciliationAttempts int
	gatesMu                   sync.Mutex
	gates                     map[domain.AccountID]*accountGate
	lifecycleMu               sync.Mutex
	closing                   bool
	active                    int
	idle                      chan struct{}
	runnerMu                  sync.Mutex
	runnerCancel              context.CancelFunc
	runnerDone                chan struct{}
	runnerStarted             bool
	reconciliationAccounts    map[domain.AccountID]struct{}
}

type accountGate struct {
	mu         sync.Mutex
	cond       *sync.Cond
	ready      bool
	recovering bool
	inFlight   int
}

func newAccountGate() *accountGate {
	gate := &accountGate{}
	gate.cond = sync.NewCond(&gate.mu)
	return gate
}

type durablePlacePayload struct {
	AccountID    domain.AccountID     `json:"account_id"`
	RouteID      domain.RouteID       `json:"route_id"`
	InstrumentID domain.InstrumentID  `json:"instrument_id"`
	Side         exchange.Side        `json:"side"`
	Type         exchange.OrderType   `json:"type"`
	Quantity     string               `json:"quantity"`
	LimitPrice   string               `json:"limit_price,omitempty"`
	LimitPolicy  exchange.LimitPolicy `json:"limit_policy,omitempty"`
	ReduceOnly   bool                 `json:"reduce_only,omitempty"`
	Leverage     int                  `json:"leverage,omitempty"`
	RuntimeID    string               `json:"runtime_id"`
}

func canonicalPlacePayload(request PlaceRequest) (json.RawMessage, string, error) {
	if err := request.validateIdentity(); err != nil {
		return nil, "", err
	}
	payload, err := json.Marshal(durablePlacePayload{
		AccountID: request.AccountID, RouteID: request.RouteID, InstrumentID: request.InstrumentID,
		Side: request.Side, Type: request.Type, Quantity: request.Quantity.String(),
		LimitPrice: request.LimitPrice.String(), LimitPolicy: request.LimitPolicy, ReduceOnly: request.ReduceOnly, Leverage: request.Leverage,
		RuntimeID: request.WriteFence.RuntimeID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("encode durable order payload")
	}
	hash, err := domain.CanonicalPayloadHash(payload)
	if err != nil {
		return nil, "", fmt.Errorf("hash durable order payload")
	}
	return payload, hash, nil
}

func decodePlacePayload(payload json.RawMessage) (durablePlacePayload, error) {
	var decoded durablePlacePayload
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return durablePlacePayload{}, fmt.Errorf("decode durable order payload")
	}
	if decoded.AccountID.Validate() != nil || decoded.RouteID.Validate() != nil || decoded.InstrumentID.Validate() != nil || decoded.RuntimeID == "" || decoded.Leverage < 0 {
		return durablePlacePayload{}, fmt.Errorf("decode durable order payload")
	}
	if _, err := decimal.NewFromString(decoded.Quantity); err != nil {
		return durablePlacePayload{}, fmt.Errorf("decode durable order payload")
	}
	if decoded.LimitPrice != "" {
		if _, err := decimal.NewFromString(decoded.LimitPrice); err != nil {
			return durablePlacePayload{}, fmt.Errorf("decode durable order payload")
		}
	}
	return decoded, nil
}

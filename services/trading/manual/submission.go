package manual

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/shopspring/decimal"
)

var (
	ErrValidation  = errors.New("invalid manual order")
	ErrUnavailable = errors.New("manual trading dependency unavailable")
)

const CommandPath = "/api/v1/products/manual/commands"

type CatalogReader interface {
	Snapshot(context.Context) (catalog.Snapshot, error)
}

type OrderRequest struct {
	AccountID    string `json:"accountId"`
	InstrumentID string `json:"instrumentId"`
	Side         string `json:"side"`
	OrderType    string `json:"orderType"`
	Quantity     string `json:"quantity"`
	LimitPrice   string `json:"limitPrice"`
	TimeInForce  string `json:"timeInForce"`
	ReduceOnly   bool   `json:"reduceOnly"`
	Leverage     int    `json:"leverage"`
}

type CommandRequest struct {
	Action         string        `json:"action"`
	LineageID      string        `json:"lineageId"`
	OrderID        string        `json:"orderId"`
	IdempotencyKey string        `json:"idempotencyKey"`
	Order          *OrderRequest `json:"order,omitempty"`
}

// Account is the slice of an account the manual product must see to
// decide whether an order may be placed against it. The gateway owns the
// account record; this is the read-only projection carried across the wire, so
// it holds no credential reference.
//
// RuntimeID is an internal write fence. Every command carries the runtime that
// was current when Trading accepted it, so Gateway can reject a command after
// the account is removed and re-created.
type Account struct {
	ID               string
	RuntimeID        string
	Venue            string
	VenueEnvironment string
	State            string
	ProductIDs       []string
	Routes           []AccountRoute
}

// AccountRoute is one of the account's declared trading routes. The
// gateway refuses an order that does not name the route it is trading through,
// so the manual product has to resolve one before it can place anything: the
// instrument's product picks the route, and the route's identifier is copied
// into the command intent.
type AccountRoute struct {
	RouteID         string
	Product         string
	SettlementAsset string
	RouteProfileID  string
}

// The states the manual product tests for. They match the gateway lifecycle
// vocabulary; anything else reads as not-ready.
const (
	manualAccountActive     = "active"
	manualProductIdentifier = "manual"
)

// AccountAuthority answers for an account on behalf of a named principal.
// The principal travels with the lookup because the authority is the gateway,
// whose account plane refuses a read that cannot say who is asking; a service
// token alone establishes the caller, not the user behind it.
type AccountAuthority interface {
	Get(ctx context.Context, principal, accountID string) (Account, error)
}

func validateManualOrder(request OrderRequest) error {
	quantity, quantityErr := decimal.NewFromString(request.Quantity)
	if request.AccountID == "" || request.InstrumentID == "" || (request.Side != "buy" && request.Side != "sell") || quantityErr != nil || !quantity.IsPositive() || request.Leverage < 0 {
		return ErrValidation
	}
	switch strings.ToLower(request.OrderType) {
	case "market":
		if request.LimitPrice != "" {
			return ErrValidation
		}
	case "limit":
		price, err := decimal.NewFromString(request.LimitPrice)
		if err != nil || !price.IsPositive() {
			return ErrValidation
		}
	default:
		return ErrValidation
	}
	return nil
}

func validateManualOrderRules(instrument catalog.Instrument, request OrderRequest) error {
	quantity, err := decimal.NewFromString(request.Quantity)
	if err != nil || !quantity.IsPositive() {
		return ErrValidation
	}
	if minimum, err := decimal.NewFromString(instrument.Rules.MinQuantity); err == nil && minimum.IsPositive() && quantity.LessThan(minimum) {
		return ErrValidation
	}
	if step, err := decimal.NewFromString(instrument.Rules.QuantityIncrement); err == nil && step.IsPositive() && !quantity.Mod(step).IsZero() {
		return ErrValidation
	}
	if !strings.EqualFold(request.OrderType, "limit") {
		return nil
	}
	price, err := decimal.NewFromString(request.LimitPrice)
	if err != nil || !price.IsPositive() {
		return ErrValidation
	}
	if step, err := decimal.NewFromString(instrument.Rules.PriceIncrement); err == nil && step.IsPositive() && !price.Mod(step).IsZero() {
		return ErrValidation
	}
	if minimum, err := decimal.NewFromString(instrument.Rules.MinNotional); err == nil && minimum.IsPositive() && quantity.Mul(price).LessThan(minimum) {
		return ErrValidation
	}
	return nil
}

func manualLeverageValid(product string, leverage int) bool {
	if leverage < 0 {
		return false
	}
	if strings.EqualFold(product, "spot") {
		return leverage == 0
	}
	return strings.EqualFold(product, "perp")
}

func manualCatalogAuthority(snapshot catalog.Snapshot, account Account, instrumentID string) (string, bool) {
	instrument, found := snapshot.Instrument(instrumentID)
	if !found || !instrument.Ready {
		return "", false
	}
	routeID, routed := manualAccountRoute(account, instrument.Product, instrument.Settlement)
	if !routed {
		return "", false
	}
	for _, venue := range snapshot.Venues {
		if venue.Venue == instrument.Venue && venue.Venue == account.Venue && venue.Environment == account.VenueEnvironment && venue.Ready {
			return routeID, true
		}
	}
	return "", false
}

// manualAccountRoute picks the account route an order for this product and
// settlement asset must be placed through.
//
// A route that names a settlement asset serves only instruments settling in it;
// one that names none serves the whole product. The named route therefore wins
// when both match, so adding a settlement-specific route to an account does not
// silently reroute the instruments it was added for through the general one.
//
// No match means the account cannot trade this instrument at all, which the
// callers render the same way as an unknown instrument: the order is refused
// here rather than at the Gateway, where the refusal would arrive as a fence
// violation and read as a race.
func manualAccountRoute(account Account, product, settlement string) (string, bool) {
	fallback := ""
	for _, route := range account.Routes {
		if route.Product != product || route.RouteID == "" {
			continue
		}
		if route.SettlementAsset == "" {
			if fallback == "" {
				fallback = route.RouteID
			}
			continue
		}
		if route.SettlementAsset == settlement {
			return route.RouteID, true
		}
	}
	return fallback, fallback != ""
}

// An account is usable after registration. The exchange decides API permissions.
func manualAccountReady(account Account) bool {
	return account.ID != "" && manualRuntimeID(account) != "" && account.State == manualAccountActive
}

func manualRuntimeID(account Account) string {
	if runtimeID := strings.TrimSpace(account.RuntimeID); runtimeID != "" {
		return runtimeID
	}
	return ""
}

// Submission reports whether this call changed an order. Replays only read the
// original command, including accepted commands awaiting their original sender.
type Submission struct {
	Command orders.Command
	Changed bool
}

func (service *Service) Submit(ctx context.Context, principal string, request CommandRequest) (Submission, error) {
	if principal == "" || request.LineageID == "" || request.IdempotencyKey == "" {
		return Submission{}, ErrValidation
	}
	if service == nil || service.commands == nil {
		return Submission{}, ErrUnavailable
	}
	switch request.Action {
	case "cancel":
		if request.Order != nil || request.OrderID == "" {
			return Submission{}, ErrValidation
		}
		command, err := service.commands.GetOwned(ctx, principal, request.OrderID)
		if err != nil {
			return Submission{}, err
		}
		if command.ProductID != "manual" || command.CommandKind != "manual.place" || command.LineageID != request.LineageID {
			return Submission{}, orders.ErrNotFound
		}
		if command.State == orders.StateFilled || command.State == orders.StateCanceled || command.State == orders.StateFailed {
			return Submission{Command: command}, nil
		}
		command, err = service.Cancel(ctx, command)
		if err != nil {
			return Submission{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return Submission{Command: command, Changed: true}, nil
	case "place":
		if request.Order == nil || request.OrderID != "" {
			return Submission{}, ErrValidation
		}
	default:
		return Submission{}, ErrValidation
	}
	order := *request.Order
	order.OrderType = strings.ToLower(order.OrderType)
	if err := validateManualOrder(order); err != nil {
		return Submission{}, err
	}
	identity := orders.IdempotencyIdentity{PrincipalID: principal, Method: "POST", Path: CommandPath, Key: request.IdempotencyKey}
	existing, err := service.commands.Find(ctx, identity)
	if err == nil {
		return service.replay(existing, principal, request.LineageID, order)
	}
	if !errors.Is(err, orders.ErrNotFound) {
		return Submission{}, fmt.Errorf("%w: find command: %w", ErrUnavailable, err)
	}
	if !service.Ready() || service.accounts == nil || service.catalog == nil {
		return Submission{}, ErrUnavailable
	}
	if !service.liveWrites {
		return Submission{}, orders.ErrLiveWriteDisabled
	}
	account, err := service.accounts.Get(ctx, principal, order.AccountID)
	if err != nil {
		return Submission{}, fmt.Errorf("%w: read account: %w", ErrUnavailable, err)
	}
	if account.ID != order.AccountID {
		return Submission{}, orders.ErrNotFound
	}
	if !manualAccountReady(account) {
		return Submission{}, ErrUnavailable
	}
	snapshot, err := service.catalog.Snapshot(ctx)
	if err != nil {
		return Submission{}, fmt.Errorf("%w: read catalog: %w", ErrUnavailable, err)
	}
	routeID, ready := manualCatalogAuthority(snapshot, account, order.InstrumentID)
	instrument, listed := snapshot.Instrument(order.InstrumentID)
	if !ready || !listed {
		return Submission{}, orders.ErrNotFound
	}
	if !manualLeverageValid(instrument.Product, order.Leverage) || validateManualOrderRules(instrument, order) != nil {
		return Submission{}, ErrValidation
	}
	payload, err := json.Marshal(placeIntent{InstrumentID: order.InstrumentID, Side: order.Side, Quantity: order.Quantity,
		OrderType: order.OrderType, LimitPrice: order.LimitPrice, TimeInForce: order.TimeInForce, ReduceOnly: order.ReduceOnly,
		Leverage: order.Leverage, RouteID: routeID, RuntimeID: manualRuntimeID(account)})
	if err != nil {
		return Submission{}, fmt.Errorf("%w: encode intent", ErrUnavailable)
	}
	command, replayed, err := service.commands.Create(ctx, orders.CreateRequest{
		PrincipalID: principal, AccountID: order.AccountID, ProductID: "manual", LineageID: request.LineageID,
		IdempotencyKey: request.IdempotencyKey, Method: identity.Method, Path: identity.Path, CommandKind: "manual.place", Payload: payload,
	})
	// Two instances can resolve different runtimes before one wins creation.
	// Compare only user intent to the winner; never replace its execution fence.
	if replayed && (err == nil || errors.Is(err, orders.ErrIdempotencyConflict)) {
		return service.replay(command, principal, request.LineageID, order)
	}
	if err != nil {
		return Submission{}, fmt.Errorf("%w: create command: %w", ErrUnavailable, err)
	}
	command, err = service.Execute(ctx, command)
	if err != nil {
		return Submission{}, fmt.Errorf("%w: execute command: %w", ErrUnavailable, err)
	}
	return Submission{Command: command, Changed: true}, nil
}

func (service *Service) replay(command orders.Command, principal, lineage string, order OrderRequest) (Submission, error) {
	if command.PrincipalID != principal || command.AccountID != order.AccountID || command.ProductID != "manual" || command.LineageID != lineage || command.CommandKind != "manual.place" {
		return Submission{}, orders.ErrIdempotencyConflict
	}
	intent, err := decodePlaceIntent(command.Payload)
	if err != nil {
		return Submission{}, fmt.Errorf("%w: stored order intent cannot be read", ErrUnavailable)
	}
	original := OrderRequest{AccountID: command.AccountID, InstrumentID: intent.InstrumentID, Side: intent.Side, OrderType: intent.OrderType,
		Quantity: intent.Quantity, LimitPrice: intent.LimitPrice, TimeInForce: intent.TimeInForce, ReduceOnly: intent.ReduceOnly, Leverage: intent.Leverage}
	if original != order {
		return Submission{}, orders.ErrIdempotencyConflict
	}
	return Submission{Command: command}, nil
}

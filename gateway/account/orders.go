package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
	"github.com/shopspring/decimal"
)

var (
	ErrInvalidRequest = errors.New("invalid request")
	ErrUnsupported    = errors.New("unsupported")
)

// PlaceRequest is a new order. ClientOrderID is chosen by the caller and is
// the only idempotency mechanism: on an "unknown" outcome, look the order up
// by this id before doing anything else.
type PlaceRequest struct {
	Instrument    venue.InstrumentID `json:"inst"`
	Side          string             `json:"side"` // buy | sell
	Type          string             `json:"type"` // market | limit
	Quantity      string             `json:"qty"`
	Price         string             `json:"price,omitempty"`
	TimeInForce   string             `json:"tif,omitempty"` // gtc | ioc | post_only
	ReduceOnly    bool               `json:"reduceOnly,omitempty"`
	ClientOrderID string             `json:"clientOrderId"`
}

// Outcome of a placement.
const (
	OutcomeAccepted = "accepted"
	OutcomeRejected = "rejected"
	OutcomeUnknown  = "unknown"
)

// PlaceResult reports what the venue said. Order is present when accepted.
type PlaceResult struct {
	Status       string `json:"status"`
	Order        *Order `json:"order,omitempty"`
	VenueCode    string `json:"venueCode,omitempty"`
	VenueMessage string `json:"venueMessage,omitempty"`
	Message      string `json:"message,omitempty"`
}

// maxClientOrderID is the exchange layer's portable uint48 ceiling.
const maxClientOrderID = 1<<48 - 1

func (request PlaceRequest) native(instrument market.Instrument) (exchange.PlaceOrderRequest, error) {
	invalid := func(format string, args ...any) (exchange.PlaceOrderRequest, error) {
		return exchange.PlaceOrderRequest{}, fmt.Errorf("%w: "+format, append([]any{ErrInvalidRequest}, args...)...)
	}
	id, err := strconv.ParseUint(request.ClientOrderID, 10, 64)
	if err != nil || id == 0 || id > maxClientOrderID || strings.HasPrefix(request.ClientOrderID, "0") {
		return invalid("clientOrderId must be a decimal integer in [1, 2^48)")
	}
	quantity, err := decimal.NewFromString(request.Quantity)
	if err != nil || !quantity.IsPositive() {
		return invalid("qty must be a positive decimal")
	}
	native := exchange.PlaceOrderRequest{
		Instrument: instrument.Native(), ClientOrderID: request.ClientOrderID,
		Quantity: quantity, ReduceOnly: request.ReduceOnly,
	}
	switch request.Side {
	case "buy":
		native.Side = exchange.SideBuy
	case "sell":
		native.Side = exchange.SideSell
	default:
		return invalid("side must be buy or sell")
	}
	switch request.Type {
	case "market":
		native.Type = exchange.OrderTypeMarket
		if request.Price != "" {
			return invalid("market orders take no price")
		}
	case "limit":
		native.Type = exchange.OrderTypeLimit
		price, err := decimal.NewFromString(request.Price)
		if err != nil || !price.IsPositive() {
			return invalid("limit orders need a positive price")
		}
		native.LimitPrice = price
		switch request.TimeInForce {
		case "", "gtc":
			native.LimitPolicy = exchange.LimitPolicyResting
		case "ioc":
			native.LimitPolicy = exchange.LimitPolicyIOC
		case "post_only":
			native.LimitPolicy = exchange.LimitPolicyPostOnly
		default:
			return invalid("tif must be gtc, ioc or post_only")
		}
	default:
		return invalid("type must be market or limit")
	}
	if request.ReduceOnly && instrument.Product != exchange.ProductPerp {
		return invalid("reduceOnly applies to perpetuals only")
	}
	if err := native.Validate(instrument.Product); err != nil {
		return invalid("%s", safeMessage(err))
	}
	return native, nil
}

// Place sends one order to the venue. It never retries and never stores.
func (rt *runtime) Place(ctx context.Context, instrument market.Instrument, request PlaceRequest) (PlaceResult, error) {
	client, err := rt.clientFor(instrument)
	if err != nil {
		return PlaceResult{}, err
	}
	native, err := request.native(instrument)
	if err != nil {
		return PlaceResult{}, err
	}
	// The venue call must not be abandoned because the HTTP caller went away:
	// that would turn a definite outcome into an unknown one.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	started := time.Now()
	ack, placeErr := client.orders().PlaceOrder(callCtx, native)
	result := classify(instrument.ID, native, ack, placeErr)
	slog.Info("order placed", "account", rt.id, "inst", instrument.ID, "side", request.Side, "type", request.Type,
		"qty", request.Quantity, "clientOrderId", request.ClientOrderID, "status", result.Status,
		"venueCode", result.VenueCode, "elapsed", time.Since(started).Round(time.Millisecond), "error", placeErr)
	if result.Status == OutcomeAccepted && instrument.Product == exchange.ProductPerp {
		rt.scheduleRefresh(client.market)
	}
	return result, nil
}

// classify maps an exchange acknowledgement onto accepted/rejected/unknown.
// Only proof of non-execution may yield "rejected".
func classify(id venue.InstrumentID, request exchange.PlaceOrderRequest, ack exchange.OrderAcknowledgement, err error) PlaceResult {
	if err != nil {
		var normalized *exchange.Error
		isNormalized := errors.As(err, &normalized)
		switch {
		case isNormalized && normalized.Details().CommandNotSent:
			return PlaceResult{Status: OutcomeRejected, Message: safeMessage(err), VenueCode: normalized.Details().Code}
		case ack.State == exchange.AckRejected:
			return PlaceResult{Status: OutcomeRejected, VenueCode: ack.VenueCode, VenueMessage: ack.VenueMessage, Message: safeMessage(err)}
		case errors.Is(err, exchange.ErrVenueRejected), errors.Is(err, exchange.ErrAuthentication), errors.Is(err, exchange.ErrPermission):
			code := ""
			if isNormalized {
				code = normalized.Details().Code
			}
			return PlaceResult{Status: OutcomeRejected, VenueCode: code, Message: safeMessage(err)}
		case isNormalized && normalized.Details().HTTPStatus == 0 &&
			(errors.Is(err, exchange.ErrInvalidRequest) || errors.Is(err, exchange.ErrUnsupported) || errors.Is(err, exchange.ErrInvalidConfig)):
			// Local validation failed before anything was sent.
			return PlaceResult{Status: OutcomeRejected, Message: safeMessage(err)}
		case errors.Is(err, exchange.ErrRateLimit) && isNormalized && normalized.Details().HTTPStatus == 0:
			return PlaceResult{Status: OutcomeRejected, Message: "local rate limit; order not sent"}
		default:
			return PlaceResult{Status: OutcomeUnknown, Message: "order outcome unknown; look it up by clientOrderId before retrying"}
		}
	}
	order := Order{
		Instrument: id, OrderID: ack.OrderID, ClientOrderID: request.ClientOrderID,
		Side: string(request.Side), Type: string(request.Type), Quantity: request.Quantity.String(),
		Filled: ack.FilledQuantity.String(), TimeInForce: tifFromPolicy(request.LimitPolicy), ReduceOnly: request.ReduceOnly,
		UpdatedAt: time.Now().UnixMilli(),
	}
	if request.LimitPrice.IsPositive() {
		order.Price = request.LimitPrice.String()
	}
	if ack.AverageFillPrice.Valid && ack.AverageFillPrice.Value.IsPositive() {
		order.AvgPrice = ack.AverageFillPrice.Value.String()
	}
	switch ack.State {
	case exchange.AckAcceptedPending:
		order.Status = "pending"
	case exchange.AckResting:
		order.Status = "open"
	case exchange.AckPartiallyFilled:
		order.Status = "partially_filled"
	case exchange.AckImmediatelyFilled:
		order.Status = "filled"
	case exchange.AckCanceled:
		order.Status = "canceled"
	case exchange.AckRejected:
		return PlaceResult{Status: OutcomeRejected, VenueCode: ack.VenueCode, VenueMessage: ack.VenueMessage}
	default:
		return PlaceResult{Status: OutcomeUnknown, Message: "order outcome unknown; look it up by clientOrderId before retrying"}
	}
	return PlaceResult{Status: OutcomeAccepted, Order: &order}
}

// LookupResult is the authoritative answer to "does this order exist?".
type LookupResult struct {
	Status string `json:"status"` // found | not_found | unconfirmed
	Order  *Order `json:"order,omitempty"`
}

func (rt *runtime) Lookup(ctx context.Context, instrument market.Instrument, clientOrderID, orderID string) (LookupResult, error) {
	client, err := rt.clientFor(instrument)
	if err != nil {
		return LookupResult{}, err
	}
	lookup, ok := client.orders().(exchange.ExactOrderLookup)
	if !ok {
		return LookupResult{}, fmt.Errorf("%w: exact order lookup", ErrUnsupported)
	}
	native := instrument.Native()
	result, err := lookup.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &native, ClientOrderID: clientOrderID, VenueOrderID: orderID})
	if err != nil {
		if errors.Is(err, exchange.ErrInvalidRequest) {
			return LookupResult{}, fmt.Errorf("%w: %s", ErrInvalidRequest, safeMessage(err))
		}
		return LookupResult{}, err
	}
	switch result.Status {
	case exchange.ExactOrderFound:
		order := projectOrder(instrument.ID, result.Order)
		return LookupResult{Status: "found", Order: &order}, nil
	case exchange.ExactOrderNotFound:
		return LookupResult{Status: "not_found"}, nil
	default:
		return LookupResult{Status: "unconfirmed"}, nil
	}
}

func (rt *runtime) Cancel(ctx context.Context, instrument market.Instrument, orderID string) error {
	client, err := rt.clientFor(instrument)
	if err != nil {
		return err
	}
	if strings.TrimSpace(orderID) == "" {
		return fmt.Errorf("%w: orderId is required", ErrInvalidRequest)
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_, err = client.orders().CancelOrder(callCtx, exchange.CancelOrderRequest{Instrument: instrument.Native(), OrderID: orderID})
	slog.Info("order cancel", "account", rt.id, "inst", instrument.ID, "orderId", orderID, "error", err)
	return err
}

// OpenOrders lists resting orders. instrument may be nil for all instruments
// where the venue supports it; venues that require an instrument fail.
func (rt *runtime) OpenOrders(ctx context.Context, instrument *market.Instrument) ([]Order, error) {
	var clients []routeClient
	var native *exchange.Instrument
	if instrument != nil {
		client, err := rt.clientFor(*instrument)
		if err != nil {
			return nil, err
		}
		clients = []routeClient{client}
		value := instrument.Native()
		native = &value
	} else {
		clients = rt.clients
	}
	orders := []Order{}
	var errs []error
	for _, client := range clients {
		page, err := client.orders().OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: native, Limit: 100})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", client.market, err))
			continue
		}
		for _, item := range page.Orders {
			id := rt.instrumentID(client.market, item.NativeSymbol, item.NativeID, instrument)
			if id == "" {
				continue
			}
			orders = append(orders, projectOrder(id, item))
		}
	}
	if len(orders) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return orders, nil
}

func (rt *runtime) Fills(ctx context.Context, instrument *market.Instrument, start time.Time, limit int) ([]Fill, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var clients []routeClient
	var native *exchange.Instrument
	if instrument != nil {
		client, err := rt.clientFor(*instrument)
		if err != nil {
			return nil, err
		}
		clients = []routeClient{client}
		value := instrument.Native()
		native = &value
	} else {
		clients = rt.clients
	}
	fills := []Fill{}
	var errs []error
	for _, client := range clients {
		page, err := client.orders().Fills(ctx, exchange.FillsRequest{Instrument: native, Start: start, Limit: limit})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", client.market, err))
			continue
		}
		for _, item := range page.Fills {
			id := rt.instrumentID(client.market, item.NativeSymbol, item.NativeID, instrument)
			if id == "" {
				continue
			}
			fills = append(fills, projectFill(id, item))
		}
	}
	if len(fills) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return fills, nil
}

func (rt *runtime) instrumentID(market venue.Market, nativeSymbol, nativeID string, requested *market.Instrument) venue.InstrumentID {
	if requested != nil {
		return requested.ID
	}
	if resolved, ok := rt.catalog.ResolveNative(market, nativeSymbol, nativeID); ok {
		return resolved.ID
	}
	return ""
}

func (rt *runtime) SetLeverage(ctx context.Context, instrument market.Instrument, leverage int) (int, error) {
	client, err := rt.clientFor(instrument)
	if err != nil {
		return 0, err
	}
	if client.perp == nil {
		return 0, fmt.Errorf("%w: leverage applies to perpetuals only", ErrInvalidRequest)
	}
	if leverage < 1 || leverage > 200 {
		return 0, fmt.Errorf("%w: leverage must be between 1 and 200", ErrInvalidRequest)
	}
	result, err := client.perp.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: instrument.Native(), Leverage: leverage})
	if err != nil {
		return 0, err
	}
	rt.scheduleRefresh(client.market)
	return result.Effective, nil
}

// safeMessage returns the exchange error's credential-free message.
func safeMessage(err error) string {
	var normalized *exchange.Error
	if errors.As(err, &normalized) {
		if message := normalized.Details().SafeMessage; message != "" {
			return message
		}
		return string(normalized.Kind())
	}
	return "request failed"
}

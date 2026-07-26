package order

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

const reasonExactLookupUnavailable = "exact_lookup_unavailable"

type AccountRegistry interface {
	Spot(domain.AccountID) (exchange.SpotClient, error)
	Perp(domain.AccountID) (exchange.PerpClient, error)
	Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error)
	Status(domain.AccountID) (account.Status, error)
	RecoveryStatus(domain.AccountID) (account.Status, error)
}

type AccountRouter struct {
	accounts AccountRegistry
}

func NewAccountRouter(accounts AccountRegistry) (*AccountRouter, error) {
	if accounts == nil {
		return nil, fmt.Errorf("order account registry is required")
	}
	return &AccountRouter{accounts: accounts}, nil
}

func (router *AccountRouter) Route(ctx context.Context, accountID domain.AccountID, instrumentID domain.InstrumentID) (Route, error) {
	return router.route(ctx, accountID, instrumentID, false)
}

func (router *AccountRouter) RouteForRecovery(ctx context.Context, accountID domain.AccountID, instrumentID domain.InstrumentID) (Route, error) {
	return router.route(ctx, accountID, instrumentID, true)
}

func (router *AccountRouter) route(ctx context.Context, accountID domain.AccountID, instrumentID domain.InstrumentID, recovery bool) (Route, error) {
	if ctx == nil {
		return Route{}, fmt.Errorf("order route context is required")
	}
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	if err := accountID.Validate(); err != nil {
		return Route{}, err
	}
	if err := instrumentID.Validate(); err != nil {
		return Route{}, err
	}
	var status account.Status
	var err error
	if recovery {
		status, err = router.accounts.RecoveryStatus(accountID)
	} else {
		status, err = router.accounts.Status(accountID)
	}
	if err != nil {
		return Route{}, fmt.Errorf("order account status unavailable")
	}
	instrument, err := router.accounts.Resolve(ctx, accountID, instrumentID)
	if err != nil {
		return Route{}, fmt.Errorf("order instrument unavailable")
	}

	var orders exchange.OrderREST
	switch instrument.Identity.Product {
	case exchange.ProductSpot:
		client, clientErr := router.accounts.Spot(accountID)
		if clientErr != nil {
			return Route{}, fmt.Errorf("order client unavailable")
		}
		orders = client
	case exchange.ProductPerp:
		client, clientErr := router.accounts.Perp(accountID)
		if clientErr != nil {
			return Route{}, fmt.Errorf("order client unavailable")
		}
		orders = client
	default:
		return Route{}, fmt.Errorf("order product unsupported")
	}
	lookup, ok := orders.(exchange.ExactOrderLookup)
	reason := status.Reason
	if !ok {
		reason = reasonExactLookupUnavailable
	}
	return Route{
		Driver:     &exchangeDriver{orders: orders, lookup: lookup},
		Instrument: instrument, Product: instrument.Identity.Product,
		WriteReady: status.WriteReady && ok, Reason: reason,
	}, nil
}

type exchangeDriver struct {
	orders exchange.OrderREST
	lookup exchange.ExactOrderLookup
}

func (driver *exchangeDriver) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return driver.orders.PlaceOrder(ctx, request)
}

func (driver *exchangeDriver) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return driver.orders.CancelOrder(ctx, request)
}

func (driver *exchangeDriver) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (ExactResult, error) {
	if driver.lookup == nil {
		return ExactResult{}, fmt.Errorf("exact order lookup unavailable")
	}
	result, err := driver.lookup.LookupExactOrder(ctx, request)
	if err != nil {
		return ExactResult{}, err
	}
	if err := result.Validate(); err != nil {
		return ExactResult{}, fmt.Errorf("invalid exact order evidence")
	}
	state, err := exactDomainState(result)
	if err != nil {
		return ExactResult{}, err
	}
	return ExactResult{
		Status: result.Status, State: state, Order: result.Order, Evidence: result.Evidence,
	}, nil
}

func exactDomainState(result exchange.ExactOrderLookupResult) (domain.OrderState, error) {
	if result.Status != exchange.ExactOrderFound {
		return "", nil
	}
	status := strings.ToLower(strings.TrimSpace(result.Order.Status))
	status = strings.ReplaceAll(status, "-", "_")
	status = strings.ReplaceAll(status, " ", "_")
	switch status {
	case "accepted", "accepted_pending", "pending", "new":
		return domain.StateAccepted, nil
	case "open", "resting":
		return domain.StateOpen, nil
	case "partially_filled", "partial":
		return domain.StatePartiallyFilled, nil
	case "filled":
		return domain.StateFilled, nil
	case "canceled", "cancelled", "expired":
		return domain.StateCanceled, nil
	case "rejected":
		return domain.StateRejected, nil
	default:
		return "", fmt.Errorf("exact order state is unsupported")
	}
}

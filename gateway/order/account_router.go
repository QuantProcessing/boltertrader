package order

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

const (
	reasonExactLookupUnavailable = "exact_lookup_unavailable"
	reasonWriteFenceUnavailable  = "write_fence_unavailable"
)

type writeFenceChecker interface {
	CheckWriteFence(account.WriteFence) error
}

type activeRouteSource interface {
	ActiveRoute(domain.AccountID, string, string) (domain.RouteID, *account.GenerationAdmission, error)
}

type activeRouteClientSource interface {
	ActiveRouteClient(domain.AccountID, string, string) (domain.RouteID, exchange.OrderREST, *account.GenerationAdmission, error)
}

type routeStatusSource interface {
	RouteStatus(domain.AccountID, string, string) (account.Status, error)
}

type recoveryRouteStatusSource interface {
	RecoveryRouteStatus(domain.AccountID, string, string) (account.Status, error)
}

type atomicOrderRouteSource interface {
	SelectOrderRoute(context.Context, domain.AccountID, domain.InstrumentID, account.OrderRoutePurpose, account.OrderRouteSelector) (account.SelectedOrderRoute, error)
}

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
	return router.route(ctx, accountID, instrumentID, false, account.OrderRouteSelector{})
}

func (router *AccountRouter) RouteForRecovery(ctx context.Context, accountID domain.AccountID, instrumentID domain.InstrumentID) (Route, error) {
	return router.route(ctx, accountID, instrumentID, true, account.OrderRouteSelector{})
}

func (router *AccountRouter) RouteForRecoveryCommand(
	ctx context.Context,
	accountID domain.AccountID,
	instrumentID domain.InstrumentID,
	routeID domain.RouteID,
	fence domain.AccountFence,
) (Route, error) {
	return router.route(ctx, accountID, instrumentID, true, account.OrderRouteSelector{RouteID: routeID, Fence: fence})
}

func (router *AccountRouter) route(
	ctx context.Context,
	accountID domain.AccountID,
	instrumentID domain.InstrumentID,
	recovery bool,
	selector account.OrderRouteSelector,
) (Route, error) {
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
	if source, ok := router.accounts.(atomicOrderRouteSource); ok {
		purpose := account.OrderRouteWrite
		if recovery {
			purpose = account.OrderRouteRecovery
		}
		selected, err := source.SelectOrderRoute(ctx, accountID, instrumentID, purpose, selector)
		if err != nil {
			return Route{}, fmt.Errorf("order runtime route unavailable")
		}
		return router.routeFromSelection(selected), nil
	}
	instrument, err := router.accounts.Resolve(ctx, accountID, instrumentID)
	if err != nil {
		return Route{}, fmt.Errorf("order instrument unavailable")
	}
	var status account.Status
	if source, routeAware := router.accounts.(recoveryRouteStatusSource); routeAware && recovery {
		status, err = source.RecoveryRouteStatus(accountID, string(instrument.Identity.Product), instrument.Identity.SettleAsset)
	} else if source, routeAware := router.accounts.(routeStatusSource); routeAware {
		status, err = source.RouteStatus(accountID, string(instrument.Identity.Product), instrument.Identity.SettleAsset)
	} else if recovery {
		status, err = router.accounts.RecoveryStatus(accountID)
	} else {
		status, err = router.accounts.Status(accountID)
	}
	if err != nil {
		return Route{}, fmt.Errorf("order account status unavailable")
	}

	var orders exchange.OrderREST
	var routeID domain.RouteID
	var admission HandoffAdmission
	routeClientSelected := false
	if source, routeAware := router.accounts.(activeRouteClientSource); routeAware {
		routeClientSelected = true
		routeID, orders, admission, err = source.ActiveRouteClient(accountID, string(instrument.Identity.Product), instrument.Identity.SettleAsset)
		if err != nil || orders == nil {
			return Route{}, fmt.Errorf("order runtime route unavailable")
		}
	} else {
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
	}
	lookup, ok := orders.(exchange.ExactOrderLookup)
	var checkWriteFence func(account.WriteFence) error
	if checker, supportsFences := router.accounts.(writeFenceChecker); supportsFences {
		checkWriteFence = checker.CheckWriteFence
	}
	if routeID == "" && !routeClientSelected {
		if source, supportsRuntimeRoutes := router.accounts.(activeRouteSource); supportsRuntimeRoutes {
			routeID, admission, err = source.ActiveRoute(accountID, string(instrument.Identity.Product), instrument.Identity.SettleAsset)
			if err != nil {
				return Route{}, fmt.Errorf("order runtime route unavailable")
			}
		}
	}
	reason := status.Reason
	if !ok {
		reason = reasonExactLookupUnavailable
	} else if checkWriteFence == nil {
		reason = reasonWriteFenceUnavailable
	}
	return Route{
		RouteID: routeID, Driver: &exchangeDriver{orders: orders, lookup: lookup}, Admission: admission,
		Instrument: instrument, Product: instrument.Identity.Product,
		WriteReady: status.WriteReady && ok && checkWriteFence != nil, Reason: reason, CheckWriteFence: checkWriteFence,
	}, nil
}

func (router *AccountRouter) routeFromSelection(selected account.SelectedOrderRoute) Route {
	lookup, exactLookupReady := selected.Orders.(exchange.ExactOrderLookup)
	var checkWriteFence func(account.WriteFence) error
	if checker, ok := router.accounts.(writeFenceChecker); ok {
		checkWriteFence = checker.CheckWriteFence
	}
	reason := selected.Status.Reason
	if !exactLookupReady {
		reason = reasonExactLookupUnavailable
	} else if checkWriteFence == nil {
		reason = reasonWriteFenceUnavailable
	}
	return Route{
		RouteID:         selected.RouteID,
		Driver:          &exchangeDriver{orders: selected.Orders, lookup: lookup},
		Admission:       selected.Admission,
		Instrument:      selected.Instrument,
		Product:         selected.Instrument.Identity.Product,
		WriteReady:      selected.Status.WriteReady && exactLookupReady && checkWriteFence != nil,
		Reason:          reason,
		CheckWriteFence: checkWriteFence,
	}
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

package factoryclient

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func loadDerivativeSnapshot(ctx context.Context, scope exchange.CatalogScope, loader DerivativeSnapshotLoader, settings Settings) (DerivativeSnapshot, error) {
	if loader == nil {
		return DerivativeSnapshot{}, fmt.Errorf("derivative snapshot loader is unavailable")
	}
	// Some native families require several independent all-market endpoints to
	// assemble one fact snapshot. Those loaders coordinate each operation
	// themselves so rate-limit accounting and singleflight retain the exchange's
	// real operation boundary instead of hiding everything behind one request.
	if coordinated, ok := loader.(selfCoordinatedDerivativeLoader); ok && coordinated.coordinatesDerivativeRequests() {
		return loader.LoadDerivativeSnapshot(ctx, scope)
	}
	operation, collapseKey := derivativeRequestIdentity(scope, settings.MarketCapabilities)
	value, err := coordinatedMarketRequest(ctx, settings, scope.Venue, operation, collapseKey, func(callCtx context.Context) (DerivativeSnapshot, error) {
		return loader.LoadDerivativeSnapshot(callCtx, scope)
	})
	return value, err
}

type selfCoordinatedDerivativeLoader interface {
	coordinatesDerivativeRequests() bool
}

func derivativeRequestIdentity(scope exchange.CatalogScope, capabilities []exchange.MarketCapability) (string, string) {
	operation := "funding_rates"
	family := "perpetual"
	for _, capability := range capabilities {
		if strings.TrimSpace(capability.NativeMarketFamily) != "" {
			family = capability.NativeMarketFamily
		}
		if capability.Transport == exchange.TransportREST && strings.TrimSpace(capability.RateLimitOperation) != "" {
			operation = capability.RateLimitOperation
			break
		}
	}
	return operation, family
}

func coordinatedMarketRequest[T any](ctx context.Context, settings Settings, venue exchange.Venue, operation, collapseKey string, call func(context.Context) (T, error)) (T, error) {
	var zero T
	if settings.MarketRequests == nil {
		return call(ctx)
	}
	value, err := settings.MarketRequests.Do(ctx, venue, operation, collapseKey, func(callCtx context.Context) (any, error) {
		return call(callCtx)
	})
	if err != nil {
		return zero, err
	}
	result, ok := value.(T)
	if !ok {
		return zero, fmt.Errorf("market request returned an unexpected value")
	}
	return result, nil
}

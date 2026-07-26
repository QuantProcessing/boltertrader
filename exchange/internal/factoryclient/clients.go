package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type clientMeta struct {
	venue            exchange.Venue
	product          exchange.Product
	catalog          exchange.InstrumentCatalog
	catalogScope     exchange.CatalogScope
	sourceDescriptor exchange.SourceDescriptor
}

func clientMetaFromSettings(venue exchange.Venue, product exchange.Product, settings Settings) clientMeta {
	return clientMeta{
		venue:            venue,
		product:          product,
		catalog:          settings.InstrumentCatalog,
		catalogScope:     settings.CatalogScope,
		sourceDescriptor: settings.SourceDescriptor,
	}
}

func (meta clientMeta) redactedString() string {
	return fmt.Sprintf("exchange/factory.Client{venue:%q, product:%q, credentials:redacted}", meta.venue, meta.product)
}

func withExchangeOperation(err error, operation string) error {
	if err == nil {
		return nil
	}
	var normalized *exchange.Error
	if !errors.As(err, &normalized) {
		return err
	}
	details := normalized.Details()
	details.Operation = operation
	return exchange.NewError(normalized.Kind(), details)
}

func (meta clientMeta) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	if err := meta.catalogContextErr(ctx, "ResolveInstrument"); err != nil {
		return exchange.Instrument{}, err
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return exchange.Instrument{}, meta.catalogInvalidRequest("ResolveInstrument", "instrument symbol is required")
	}
	if meta.catalog == nil || meta.catalogScope == (exchange.CatalogScope{}) {
		return exchange.Instrument{}, meta.catalogMetadataResolution("ResolveInstrument", "instrument catalog is not configured")
	}
	instrument, err := meta.catalog.Resolve(ctx, meta.catalogScope, exchange.InstrumentQuery{Symbol: symbol})
	if err == nil {
		return instrument, nil
	}
	if !isCatalogMetadataNotFound(err) {
		return exchange.Instrument{}, withExchangeOperation(err, "ResolveInstrument")
	}
	instrument, err = meta.catalog.Resolve(ctx, meta.catalogScope, exchange.InstrumentQuery{NativeSymbol: symbol})
	return instrument, withExchangeOperation(err, "ResolveInstrument")
}

func (meta clientMeta) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := meta.catalogContextErr(ctx, "RefreshInstruments"); err != nil {
		return nil, err
	}
	if meta.catalog == nil || meta.catalogScope == (exchange.CatalogScope{}) {
		return nil, meta.catalogMetadataResolution("RefreshInstruments", "instrument catalog is not configured")
	}
	snapshot, err := meta.catalog.Refresh(ctx, meta.catalogScope)
	instruments := append([]exchange.Instrument(nil), snapshot.Instruments...)
	if err != nil {
		return instruments, withExchangeOperation(err, "RefreshInstruments")
	}
	return instruments, nil
}

func (meta clientMeta) catalogContextErr(ctx context.Context, operation string) error {
	if ctx == nil {
		return meta.catalogInvalidRequest(operation, "context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		kind := exchange.KindCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			kind = exchange.KindDeadlineExceeded
		}
		return exchange.NewError(kind, exchange.ErrorDetails{
			Venue:       meta.venue,
			Product:     meta.product,
			Operation:   operation,
			SafeMessage: "request context ended",
		})
	}
	return nil
}

func (meta clientMeta) catalogInvalidRequest(operation, message string) error {
	return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{
		Venue:       meta.venue,
		Product:     meta.product,
		Operation:   operation,
		SafeMessage: message,
	})
}

func (meta clientMeta) catalogMetadataResolution(operation, message string) error {
	return exchange.NewError(exchange.KindMetadataResolution, exchange.ErrorDetails{
		Venue:       meta.venue,
		Product:     meta.product,
		Operation:   operation,
		SafeMessage: message,
	})
}

func isCatalogMetadataNotFound(err error) bool {
	var exchangeErr *exchange.Error
	return errors.As(err, &exchangeErr) &&
		exchangeErr.Kind() == exchange.KindMetadataResolution &&
		exchangeErr.Details().Operation == "InstrumentCatalog.Resolve" &&
		exchangeErr.Details().SafeMessage == "instrument metadata not found"
}

func boundedOrderPage(orders []exchange.Order, limit int, cursor string) exchange.OrderPage {
	if limit > 0 && len(orders) > limit {
		orders = orders[:limit]
	}
	return exchange.OrderPage{
		Orders: orders,
		Page: exchange.PageInfo{
			Cursor: cursor,
			Limit:  limit,
		},
	}
}

func historyPage(cursor string, limit int, start, end time.Time) exchange.PageInfo {
	return exchange.PageInfo{Cursor: cursor, Limit: limit, WindowStart: start, WindowEnd: end}
}

func validateBoundedHistory(cursor string, limit int, start, end time.Time) error {
	if limit < 0 {
		return fmt.Errorf("limit must be non-negative")
	}
	if !start.IsZero() && !end.IsZero() && !start.Before(end) {
		return fmt.Errorf("start must be before end")
	}
	return nil
}

func optionalMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

func closeSpotWebSocket(socket exchange.SpotWebSocket) error {
	if socket == nil {
		return nil
	}
	return socket.Close()
}

func closePerpWebSocket(socket exchange.PerpWebSocket) error {
	if socket == nil {
		return nil
	}
	return socket.Close()
}

type spotClient struct {
	meta clientMeta
}

func (client *spotClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	if client == nil {
		return exchange.Instrument{}, clientMeta{product: exchange.ProductSpot}.catalogMetadataResolution("ResolveInstrument", "client is not configured")
	}
	return client.meta.ResolveInstrument(ctx, symbol)
}

func (client *spotClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if client == nil {
		return nil, clientMeta{product: exchange.ProductSpot}.catalogMetadataResolution("RefreshInstruments", "client is not configured")
	}
	return client.meta.RefreshInstruments(ctx)
}

func (client *spotClient) String() string {
	if client == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return client.meta.redactedString()
}

func (client *spotClient) GoString() string {
	return client.String()
}

type perpClient struct {
	meta clientMeta
}

func (client *perpClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	if client == nil {
		return exchange.Instrument{}, clientMeta{product: exchange.ProductPerp}.catalogMetadataResolution("ResolveInstrument", "client is not configured")
	}
	return client.meta.ResolveInstrument(ctx, symbol)
}

func (client *perpClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if client == nil {
		return nil, clientMeta{product: exchange.ProductPerp}.catalogMetadataResolution("RefreshInstruments", "client is not configured")
	}
	return client.meta.RefreshInstruments(ctx)
}

func (client *perpClient) String() string {
	if client == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return client.meta.redactedString()
}

func (client *perpClient) GoString() string {
	return client.String()
}

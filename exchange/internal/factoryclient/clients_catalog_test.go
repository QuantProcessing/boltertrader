package factoryclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type mutableClientCatalogLoader struct {
	snapshot exchange.InstrumentSnapshot
	err      error
}

type resolveSpyCatalog struct {
	queries []exchange.InstrumentQuery
	results []resolveSpyResult
}

type resolveSpyResult struct {
	instrument exchange.Instrument
	err        error
}

func (loader *mutableClientCatalogLoader) LoadInstruments(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	if loader.err != nil {
		return exchange.InstrumentSnapshot{}, loader.err
	}
	return loader.snapshot, nil
}

func (catalog *resolveSpyCatalog) RegisterSource(exchange.CatalogScope, exchange.SourceDescriptor, exchange.InstrumentLoader) error {
	return nil
}

func (catalog *resolveSpyCatalog) Resolve(_ context.Context, _ exchange.CatalogScope, query exchange.InstrumentQuery) (exchange.Instrument, error) {
	catalog.queries = append(catalog.queries, query)
	if len(catalog.results) == 0 {
		return exchange.Instrument{}, exchange.NewError(exchange.KindMetadataResolution, exchange.ErrorDetails{
			Operation:   "InstrumentCatalog.Resolve",
			SafeMessage: "instrument metadata not found",
		})
	}
	result := catalog.results[0]
	catalog.results = catalog.results[1:]
	return result.instrument, result.err
}

func (catalog *resolveSpyCatalog) Snapshot(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	return exchange.InstrumentSnapshot{}, nil
}

func (catalog *resolveSpyCatalog) Refresh(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	return exchange.InstrumentSnapshot{}, nil
}

func (catalog *resolveSpyCatalog) Status(exchange.CatalogScope) exchange.CatalogStatus {
	return exchange.CatalogStatus{}
}

func TestEmbeddedClientsExposeCatalogResolutionMethods(t *testing.T) {
	var (
		_ interface {
			ResolveInstrument(context.Context, string) (exchange.Instrument, error)
			RefreshInstruments(context.Context) ([]exchange.Instrument, error)
		} = (*spotClient)(nil)
		_ interface {
			ResolveInstrument(context.Context, string) (exchange.Instrument, error)
			RefreshInstruments(context.Context) ([]exchange.Instrument, error)
		} = (*perpClient)(nil)
	)
}

func TestClientMetaResolveInstrumentValidatesContextAndSymbol(t *testing.T) {
	meta := clientMeta{venue: exchange.VenueBinance, product: exchange.ProductSpot}
	if _, err := meta.ResolveInstrument(nil, "ETH-USDT"); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("nil context error = %v, want ErrInvalidRequest", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := meta.ResolveInstrument(canceled, "ETH-USDT"); !errors.Is(err, exchange.ErrCanceled) {
		t.Fatalf("canceled context error = %v, want ErrCanceled", err)
	}
	if _, err := meta.ResolveInstrument(context.Background(), " "); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("empty symbol error = %v, want ErrInvalidRequest", err)
	}
	if _, err := meta.ResolveInstrument(context.Background(), "ETH-USDT"); !errors.Is(err, exchange.ErrMetadataResolution) {
		t.Fatalf("missing catalog error = %v, want ErrMetadataResolution", err)
	}
}

func TestClientMetaResolveInstrumentQueriesCanonicalSymbolFirst(t *testing.T) {
	ctx := context.Background()
	scope := exchange.CatalogScope{Venue: exchange.VenueAster, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	instrument := newInstrument(
		exchange.VenueAster,
		exchange.ProductSpot,
		"BTC-USDT",
		"BTC",
		"USDT",
		"",
		"BTCUSDT",
		"",
		decimal.RequireFromString("0.1"),
		decimal.RequireFromString("0.001"),
		decimal.RequireFromString("0.001"),
		exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
		oneContract(),
	)
	catalog := &resolveSpyCatalog{results: []resolveSpyResult{{instrument: instrument}}}
	meta := clientMeta{venue: exchange.VenueAster, product: exchange.ProductSpot, catalog: catalog, catalogScope: scope}

	resolved, err := meta.ResolveInstrument(ctx, " BTC-USDT ")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}
	if resolved.Identity.NativeSymbol != "BTCUSDT" {
		t.Fatalf("ResolveInstrument native symbol = %s, want BTCUSDT", resolved.Identity.NativeSymbol)
	}
	if len(catalog.queries) != 1 || catalog.queries[0] != (exchange.InstrumentQuery{Symbol: "BTC-USDT"}) {
		t.Fatalf("queries = %+v, want canonical symbol lookup only", catalog.queries)
	}
}

func TestClientMetaResolveInstrumentFallsBackToNativeSymbolOnlyWhenCanonicalMissing(t *testing.T) {
	ctx := context.Background()
	scope := exchange.CatalogScope{Venue: exchange.VenueAster, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	instrument := newInstrument(
		exchange.VenueAster,
		exchange.ProductSpot,
		"BTC-USDT",
		"BTC",
		"USDT",
		"",
		"BTCUSDT",
		"",
		decimal.RequireFromString("0.1"),
		decimal.RequireFromString("0.001"),
		decimal.RequireFromString("0.001"),
		exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
		oneContract(),
	)
	catalog := &resolveSpyCatalog{results: []resolveSpyResult{
		{err: exchange.NewError(exchange.KindMetadataResolution, exchange.ErrorDetails{
			Operation:   "InstrumentCatalog.Resolve",
			SafeMessage: "instrument metadata not found",
		})},
		{instrument: instrument},
	}}
	meta := clientMeta{venue: exchange.VenueAster, product: exchange.ProductSpot, catalog: catalog, catalogScope: scope}

	resolved, err := meta.ResolveInstrument(ctx, "BTCUSDT")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}
	if resolved.Identity.Symbol != "BTC-USDT" {
		t.Fatalf("ResolveInstrument symbol = %s, want BTC-USDT", resolved.Identity.Symbol)
	}
	want := []exchange.InstrumentQuery{{Symbol: "BTCUSDT"}, {NativeSymbol: "BTCUSDT"}}
	if len(catalog.queries) != len(want) {
		t.Fatalf("queries = %+v, want %+v", catalog.queries, want)
	}
	for i := range want {
		if catalog.queries[i] != want[i] {
			t.Fatalf("query %d = %+v, want %+v", i, catalog.queries[i], want[i])
		}
	}
}

func TestClientMetaResolveInstrumentDoesNotMaskAmbiguityOrOtherErrors(t *testing.T) {
	ctx := context.Background()
	scope := exchange.CatalogScope{Venue: exchange.VenueAster, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{
			name: "ambiguous metadata",
			err: exchange.NewError(exchange.KindMetadataResolution, exchange.ErrorDetails{
				Operation:   "InstrumentCatalog.Resolve",
				SafeMessage: "instrument query key is ambiguous across indexes",
			}),
			wantErr: exchange.ErrMetadataResolution,
		},
		{
			name: "transport",
			err: exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{
				Operation:   "InstrumentCatalog.Resolve",
				SafeMessage: "catalog backend unavailable",
			}),
			wantErr: exchange.ErrTransport,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := &resolveSpyCatalog{results: []resolveSpyResult{{err: test.err}}}
			meta := clientMeta{venue: exchange.VenueAster, product: exchange.ProductSpot, catalog: catalog, catalogScope: scope}

			_, err := meta.ResolveInstrument(ctx, "BTCUSDT")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ResolveInstrument error = %v, want %v", err, test.wantErr)
			}
			if len(catalog.queries) != 1 || catalog.queries[0] != (exchange.InstrumentQuery{Symbol: "BTCUSDT"}) {
				t.Fatalf("queries = %+v, want no native fallback", catalog.queries)
			}
			var exchangeErr *exchange.Error
			if !errors.As(err, &exchangeErr) || exchangeErr.Details().Operation != "ResolveInstrument" {
				t.Fatalf("error details = %#v", err)
			}
		})
	}
}

func TestClientMetaRefreshInstrumentsReturnsLastKnownGoodWithSurfacedError(t *testing.T) {
	ctx := context.Background()
	scope := exchange.CatalogScope{
		Venue:       exchange.VenueBinance,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductSpot,
	}
	descriptor := exchange.SourceDescriptor{
		Name:      "binance-spot-test",
		Endpoint:  "https://binance.invalid",
		Transport: "test",
	}
	instrument := newInstrument(
		exchange.VenueBinance,
		exchange.ProductSpot,
		"ETH-USDT",
		"ETH",
		"USDT",
		"",
		"ETHUSDT",
		"",
		decimal.RequireFromString("0.01"),
		decimal.RequireFromString("0.001"),
		decimal.RequireFromString("0.001"),
		exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
		oneContract(),
	)
	loader := &mutableClientCatalogLoader{snapshot: exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		LoadedAt:      time.Unix(1, 0),
		Instruments:   []exchange.Instrument{instrument},
	}}
	catalog := exchange.NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, loader); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	meta := clientMeta{
		venue:        exchange.VenueBinance,
		product:      exchange.ProductSpot,
		catalog:      catalog,
		catalogScope: scope,
	}
	rows, err := meta.RefreshInstruments(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("initial RefreshInstruments rows=%d err=%v", len(rows), err)
	}
	resolved, err := meta.ResolveInstrument(ctx, "ETH-USDT")
	if err != nil || resolved.Identity.NativeSymbol != "ETHUSDT" {
		t.Fatalf("ResolveInstrument = %+v err=%v", resolved, err)
	}

	loader.err = exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{
		Venue:       exchange.VenueBinance,
		Product:     exchange.ProductSpot,
		Operation:   "InstrumentCatalog.Load",
		SafeMessage: "temporary exchangeInfo failure",
	})
	rows, err = meta.RefreshInstruments(ctx)
	if !errors.Is(err, exchange.ErrTransport) {
		t.Fatalf("refresh error = %v, want ErrTransport", err)
	}
	if len(rows) != 1 || rows[0].Identity.NativeSymbol != "ETHUSDT" {
		t.Fatalf("last-known-good rows = %+v", rows)
	}
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) || exchangeErr.Details().Operation != "RefreshInstruments" {
		t.Fatalf("refresh error details = %#v", err)
	}
}

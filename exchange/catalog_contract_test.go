package exchange

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestInstrumentContractIsCompleteExecutionDescriptor(t *testing.T) {
	typ := reflect.TypeOf(Instrument{})
	assertStructFields(t, typ, []string{"Identity", "Rules", "Encoding", "Metadata", "Revision"})

	assertStructFields(t, reflect.TypeOf(InstrumentIdentity{}), []string{
		"Venue", "Product", "Symbol", "BaseAsset", "QuoteAsset", "SettleAsset", "NativeSymbol", "NativeID",
	})
	assertStructFields(t, reflect.TypeOf(InstrumentRules{}), []string{
		"PriceIncrement", "QuantityIncrement", "MinQuantity", "MinNotional",
	})
	assertStructFields(t, reflect.TypeOf(InstrumentEncoding{}), []string{
		"ContractMultiplier", "PriceScale", "QuantityScale", "QuoteScale",
	})
	assertStructFields(t, reflect.TypeOf(InstrumentMetadata{}), []string{
		"MarketFamily", "AssetClass", "ClassificationSource", "RawAssetType", "RawCategories",
	})

	if _, ok := typ.FieldByName("Symbol"); ok {
		t.Fatal("Instrument must not keep the old top-level string Symbol field")
	}
}

func TestInstrumentScopedRequestsUseCanonicalInstrument(t *testing.T) {
	instrumentType := reflect.TypeOf(Instrument{})
	for _, item := range []struct {
		name string
		typ  reflect.Type
	}{
		{"OrderBookRequest", reflect.TypeOf(OrderBookRequest{})},
		{"CandlesRequest", reflect.TypeOf(CandlesRequest{})},
		{"PublicTradesRequest", reflect.TypeOf(PublicTradesRequest{})},
		{"PlaceOrderRequest", reflect.TypeOf(PlaceOrderRequest{})},
		{"CancelOrderRequest", reflect.TypeOf(CancelOrderRequest{})},
		{"FundingRateRequest", reflect.TypeOf(FundingRateRequest{})},
		{"SetLeverageRequest", reflect.TypeOf(SetLeverageRequest{})},
		{"WatchRequest", reflect.TypeOf(WatchRequest{})},
		{"WatchCandlesRequest", reflect.TypeOf(WatchCandlesRequest{})},
	} {
		field, ok := item.typ.FieldByName("Instrument")
		if !ok {
			t.Fatalf("%s is missing Instrument", item.name)
		}
		if field.Type != instrumentType {
			t.Fatalf("%s.Instrument type = %s, want exchange.Instrument", item.name, field.Type)
		}
	}

	instrumentPointerType := reflect.TypeOf((*Instrument)(nil))
	for _, item := range []struct {
		name string
		typ  reflect.Type
	}{
		{"OpenOrdersRequest", reflect.TypeOf(OpenOrdersRequest{})},
		{"OrderHistoryRequest", reflect.TypeOf(OrderHistoryRequest{})},
		{"FillsRequest", reflect.TypeOf(FillsRequest{})},
		{"PositionsRequest", reflect.TypeOf(PositionsRequest{})},
	} {
		field, ok := item.typ.FieldByName("Instrument")
		if !ok {
			t.Fatalf("%s is missing Instrument filter", item.name)
		}
		if field.Type != instrumentPointerType {
			t.Fatalf("%s.Instrument type = %s, want *exchange.Instrument optional filter", item.name, field.Type)
		}
	}
}

func TestAccountWideOutputsExposeRawNativeIdentity(t *testing.T) {
	for _, item := range []struct {
		name string
		typ  reflect.Type
	}{
		{"Order", reflect.TypeOf(Order{})},
		{"Fill", reflect.TypeOf(Fill{})},
		{"Position", reflect.TypeOf(Position{})},
		{"FundingRate", reflect.TypeOf(FundingRate{})},
		{"Leverage", reflect.TypeOf(Leverage{})},
		{"OrderAcknowledgement", reflect.TypeOf(OrderAcknowledgement{})},
	} {
		if _, ok := item.typ.FieldByName("Instrument"); ok {
			t.Fatalf("%s must not expose executable Instrument on account-wide/ack output", item.name)
		}
		field, ok := item.typ.FieldByName("NativeSymbol")
		if !ok {
			t.Fatalf("%s is missing raw NativeSymbol", item.name)
		}
		if field.Type.Kind() != reflect.String {
			t.Fatalf("%s.NativeSymbol type = %s, want string", item.name, field.Type)
		}
	}
}

func TestMemoryCatalogPublishesImmutableIndexedSnapshots(t *testing.T) {
	scope := CatalogScope{
		Venue:               VenueBinance,
		Environment:         EnvironmentLive,
		Product:             ProductSpot,
		EndpointFingerprint: "https://api.binance.com",
	}
	instrument := catalogTestInstrument(scope, "BTC-USDT", "BTCUSDT", "")
	snapshot := InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Revision:      "rev-a",
		Generation:    7,
		LoadedAt:      time.Unix(1, 0).UTC(),
		Instruments:   []Instrument{instrument},
	}

	catalog := NewMemoryCatalog(StaticInstrumentLoader{Snapshot: snapshot}, nil)
	got, err := catalog.Refresh(context.Background(), scope)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got.Instruments[0].Identity.Symbol = "MUTATED"

	resolved, err := catalog.Resolve(context.Background(), scope, InstrumentQuery{Symbol: "BTC-USDT"})
	if err != nil {
		t.Fatalf("Resolve by symbol: %v", err)
	}
	if resolved.Identity.Symbol != "BTC-USDT" || resolved.Identity.NativeSymbol != "BTCUSDT" {
		t.Fatalf("resolved instrument was mutated: %+v", resolved.Identity)
	}

	resolved, err = catalog.Resolve(context.Background(), scope, InstrumentQuery{NativeSymbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("Resolve by native symbol: %v", err)
	}
	if resolved.Identity.Symbol != "BTC-USDT" {
		t.Fatalf("Resolve native symbol = %s, want BTC-USDT", resolved.Identity.Symbol)
	}
}

func TestMemoryCatalogRejectsDuplicateNativeIdentity(t *testing.T) {
	scope := CatalogScope{Venue: VenueOKX, Environment: EnvironmentLive, Product: ProductPerp, Settlement: "USDT"}
	snapshot := InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Revision:      "dup",
		LoadedAt:      time.Unix(1, 0).UTC(),
		Instruments: []Instrument{
			catalogTestInstrument(scope, "BTC-USDT", "BTC-USDT-SWAP", ""),
			catalogTestInstrument(scope, "ETH-USDT", "BTC-USDT-SWAP", ""),
		},
	}

	catalog := NewMemoryCatalog(StaticInstrumentLoader{Snapshot: snapshot}, nil)
	_, err := catalog.Refresh(context.Background(), scope)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Refresh error = %v, want ErrInvalidConfig", err)
	}
}

func TestMemoryCatalogRecomputesContentRevision(t *testing.T) {
	scope := CatalogScope{Venue: VenueBinance, Environment: EnvironmentLive, Product: ProductSpot}
	snapshot := InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Revision:      "loader-process-local-revision",
		LoadedAt:      time.Unix(1, 0).UTC(),
		Instruments:   []Instrument{catalogTestInstrument(scope, "BTC-USDT", "BTCUSDT", "")},
	}

	catalog := NewMemoryCatalog(StaticInstrumentLoader{Snapshot: snapshot}, nil)
	first, err := catalog.Refresh(context.Background(), scope)
	if err != nil {
		t.Fatalf("Refresh first: %v", err)
	}
	second, err := NewMemoryCatalog(StaticInstrumentLoader{Snapshot: snapshot}, nil).Refresh(context.Background(), scope)
	if err != nil {
		t.Fatalf("Refresh second: %v", err)
	}
	if first.Revision == "" || first.Revision == "loader-process-local-revision" {
		t.Fatalf("revision=%q, want content-derived revision", first.Revision)
	}
	if first.Revision != second.Revision {
		t.Fatalf("revision first=%q second=%q, want deterministic across catalogs", first.Revision, second.Revision)
	}
}

func TestMemoryCatalogRejectsAmbiguousQueryShapes(t *testing.T) {
	scope := CatalogScope{Venue: VenueBinance, Environment: EnvironmentLive, Product: ProductSpot}
	snapshot := InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		LoadedAt:      time.Unix(1, 0).UTC(),
		Instruments:   []Instrument{catalogTestInstrument(scope, "BTC-USDT", "BTCUSDT", "1001")},
	}
	catalog := NewMemoryCatalog(StaticInstrumentLoader{Snapshot: snapshot}, nil)

	_, err := catalog.Resolve(context.Background(), scope, InstrumentQuery{Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Resolve multi-key error = %v, want ErrInvalidRequest", err)
	}
	_, err = catalog.Resolve(context.Background(), scope, InstrumentQuery{Symbol: "BTCUSDT"})
	if !errors.Is(err, ErrMetadataResolution) {
		t.Fatalf("Resolve cross-index alias error = %v, want ErrMetadataResolution", err)
	}
}

func TestInstrumentValidationRequiresPositiveEncodingScales(t *testing.T) {
	scope := CatalogScope{Venue: VenueLighter, Environment: EnvironmentLive, Product: ProductPerp}
	instrument := catalogTestInstrument(scope, "BTC-USDT", "BTC", "1")
	instrument.Encoding.PriceScale = OptionalInt{Value: 0, Valid: true}
	if err := instrument.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate price scale error = %v, want ErrInvalidRequest", err)
	}
	instrument = catalogTestInstrument(scope, "BTC-USDT", "BTC", "1")
	instrument.Encoding.QuantityScale = OptionalInt{Value: -1, Valid: true}
	if err := instrument.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate quantity scale error = %v, want ErrInvalidRequest", err)
	}
	instrument = catalogTestInstrument(scope, "BTC-USDT", "BTC", "1")
	instrument.Encoding.QuoteScale = OptionalInt{Value: 0, Valid: true}
	if err := instrument.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate quote scale error = %v, want ErrInvalidRequest", err)
	}
}

func TestDefaultCatalogSourceRegistrationSharesSameScopeAndConflictsDeterministically(t *testing.T) {
	descriptor := SourceDescriptor{Name: "binance-spot", Endpoint: "https://api.binance.com", Transport: "default"}
	scope := CatalogScope{Venue: VenueBinance, Environment: EnvironmentLive, Product: ProductSpot, EndpointFingerprint: descriptor.Fingerprint()}
	snapshot := InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		LoadedAt:      time.Unix(1, 0).UTC(),
		Instruments:   []Instrument{catalogTestInstrument(scope, "BTC-USDT", "BTCUSDT", "")},
	}

	catalog := NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, StaticInstrumentLoader{Snapshot: snapshot}); err != nil {
		t.Fatalf("RegisterSource first: %v", err)
	}
	if err := catalog.RegisterSource(scope, descriptor, StaticInstrumentLoader{Snapshot: snapshot}); err != nil {
		t.Fatalf("RegisterSource equivalent: %v", err)
	}
	conflict := SourceDescriptor{Name: "binance-spot", Endpoint: "https://mirror.binance.com", Transport: "default"}
	conflictScope := scope
	conflictScope.EndpointFingerprint = conflict.Fingerprint()
	if err := catalog.RegisterSource(conflictScope, conflict, StaticInstrumentLoader{Snapshot: snapshot}); err != nil {
		t.Fatalf("RegisterSource independent fingerprint: %v", err)
	}
	if err := catalog.RegisterSource(scope, conflict, StaticInstrumentLoader{Snapshot: snapshot}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("RegisterSource conflict error = %v, want ErrInvalidConfig", err)
	}

	first, err := catalog.Snapshot(context.Background(), scope)
	if err != nil {
		t.Fatalf("Snapshot first: %v", err)
	}
	second, err := catalog.Snapshot(context.Background(), scope)
	if err != nil {
		t.Fatalf("Snapshot second: %v", err)
	}
	if first.Generation != second.Generation {
		t.Fatalf("same-scope snapshot generation first=%d second=%d, want shared L1 snapshot", first.Generation, second.Generation)
	}
}

func TestRegisterSourceRejectsUnsafeOrUnstableDescriptors(t *testing.T) {
	catalog := NewDefaultMemoryCatalog()
	scope := CatalogScope{Venue: VenueLighter, Environment: EnvironmentLive, Product: ProductSpot}
	loader := StaticInstrumentLoader{}
	for _, descriptor := range []SourceDescriptor{
		{Name: "missing-transport", Endpoint: "https://metadata.test"},
		{Name: "relative", Endpoint: "/metadata", Transport: "default"},
		{Name: "userinfo", Endpoint: "https://user:pass@metadata.test", Transport: "default"},
		{Name: "query", Endpoint: "https://metadata.test?secret=x", Transport: "default"},
		{Name: "fragment", Endpoint: "https://metadata.test#frag", Transport: "default"},
		{Name: "bad-ws", WebSocketEndpoint: "http://metadata.test/ws", Transport: "default"},
	} {
		if err := catalog.RegisterSource(scope, descriptor, loader); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("RegisterSource(%+v) error = %v, want ErrInvalidConfig", descriptor, err)
		}
	}
}

func TestRegisterSourceRequiresScopeFingerprintToMatchNormalizedDescriptor(t *testing.T) {
	descriptor := SourceDescriptor{Name: "lighter-spot", Endpoint: "https://metadata.test/", WebSocketEndpoint: "wss://metadata.test/ws/", Transport: "default"}
	normalized := SourceDescriptor{Name: "lighter-spot", Endpoint: "https://metadata.test", WebSocketEndpoint: "wss://metadata.test/ws", Transport: "default"}
	scope := CatalogScope{Venue: VenueLighter, Environment: EnvironmentLive, Product: ProductSpot, EndpointFingerprint: normalized.Fingerprint()}
	catalog := NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, StaticInstrumentLoader{}); err != nil {
		t.Fatalf("RegisterSource normalized descriptor: %v", err)
	}
	badScope := scope
	badScope.EndpointFingerprint = SourceDescriptor{Name: "other", Endpoint: "https://metadata.test", Transport: "default"}.Fingerprint()
	if err := catalog.RegisterSource(badScope, descriptor, StaticInstrumentLoader{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("RegisterSource mismatched fingerprint error = %v, want ErrInvalidConfig", err)
	}
}

func TestRefreshReturnsLastKnownGoodWithRefreshError(t *testing.T) {
	scope := CatalogScope{Venue: VenueLighter, Environment: EnvironmentLive, Product: ProductPerp}
	snapshot := InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		LoadedAt:      time.Unix(1, 0).UTC(),
		Instruments:   []Instrument{catalogTestInstrument(scope, "BTC-USDT", "BTC", "1")},
	}
	catalog := NewMemoryCatalog(StaticInstrumentLoader{Snapshot: snapshot}, nil)
	first, err := catalog.Refresh(context.Background(), scope)
	if err != nil {
		t.Fatalf("Refresh first: %v", err)
	}
	catalog.loader = StaticInstrumentLoader{Err: errors.New("source unavailable")}
	got, err := catalog.Refresh(context.Background(), scope)
	if err == nil {
		t.Fatal("Refresh stale fallback error = nil, want source error")
	}
	if got.Generation != first.Generation || got.Revision != first.Revision {
		t.Fatalf("Refresh stale fallback = generation %d revision %q, want generation %d revision %q", got.Generation, got.Revision, first.Generation, first.Revision)
	}
	snapshotGot, err := catalog.Snapshot(context.Background(), scope)
	if err != nil {
		t.Fatalf("Snapshot after failed refresh: %v", err)
	}
	if snapshotGot.Revision != first.Revision {
		t.Fatalf("Snapshot revision = %q, want preserved LKG %q", snapshotGot.Revision, first.Revision)
	}
}

func TestInstrumentCatalogPublicContractIncludesSourceRegistration(t *testing.T) {
	var _ InstrumentCatalog = (*contractCatalog)(nil)
	var _ InstrumentSourceRegistrar = (*contractCatalog)(nil)

	scope := CatalogScope{Venue: VenueBinance, Environment: EnvironmentLive, Product: ProductSpot}
	descriptor := SourceDescriptor{Name: "custom", Endpoint: "https://metadata.test", Transport: "default"}
	loader := StaticInstrumentLoader{}
	catalog := &contractCatalog{}
	if err := registerCatalogSourceForTest(catalog, scope, descriptor, loader); err != nil {
		t.Fatalf("RegisterSource through InstrumentCatalog: %v", err)
	}
	if catalog.scope != scope {
		t.Fatalf("registered scope = %+v, want %+v", catalog.scope, scope)
	}
	if catalog.descriptor != descriptor {
		t.Fatalf("registered descriptor = %+v, want %+v", catalog.descriptor, descriptor)
	}
	if catalog.loader == nil {
		t.Fatal("registered loader was not forwarded")
	}
}

func TestCustomCatalogIsolationDoesNotTouchGlobalDefault(t *testing.T) {
	scope := CatalogScope{Venue: VenueOKX, Environment: EnvironmentLive, Product: ProductPerp, Settlement: "USDT"}
	customSnapshot := InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		LoadedAt:      time.Unix(1, 0).UTC(),
		Instruments:   []Instrument{catalogTestInstrument(scope, "ETH-USDT", "ETH-USDT-SWAP", "")},
	}
	custom := NewMemoryCatalog(StaticInstrumentLoader{Snapshot: customSnapshot}, nil)
	if _, err := custom.Snapshot(context.Background(), scope); err != nil {
		t.Fatalf("custom Snapshot: %v", err)
	}
	if _, err := DefaultInstrumentCatalog().Snapshot(context.Background(), scope); !errors.Is(err, ErrMetadataResolution) {
		t.Fatalf("default Snapshot error = %v, want ErrMetadataResolution for unregistered scope", err)
	}
}

func registerCatalogSourceForTest(catalog InstrumentCatalog, scope CatalogScope, descriptor SourceDescriptor, loader InstrumentLoader) error {
	return catalog.RegisterSource(scope, descriptor, loader)
}

type contractCatalog struct {
	scope      CatalogScope
	descriptor SourceDescriptor
	loader     InstrumentLoader
}

func (*contractCatalog) Resolve(context.Context, CatalogScope, InstrumentQuery) (Instrument, error) {
	return Instrument{}, nil
}

func (*contractCatalog) Snapshot(context.Context, CatalogScope) (InstrumentSnapshot, error) {
	return InstrumentSnapshot{}, nil
}

func (*contractCatalog) Refresh(context.Context, CatalogScope) (InstrumentSnapshot, error) {
	return InstrumentSnapshot{}, nil
}

func (*contractCatalog) Status(CatalogScope) CatalogStatus {
	return CatalogStatus{}
}

func (catalog *contractCatalog) RegisterSource(scope CatalogScope, descriptor SourceDescriptor, loader InstrumentLoader) error {
	catalog.scope = scope
	catalog.descriptor = descriptor
	catalog.loader = loader
	return nil
}

func catalogTestInstrument(scope CatalogScope, symbol, nativeSymbol, nativeID string) Instrument {
	return Instrument{
		Identity: InstrumentIdentity{
			Venue:        scope.Venue,
			Product:      scope.Product,
			Symbol:       symbol,
			BaseAsset:    "BTC",
			QuoteAsset:   "USDT",
			SettleAsset:  scope.Settlement,
			NativeSymbol: nativeSymbol,
			NativeID:     nativeID,
		},
		Rules: InstrumentRules{
			PriceIncrement:    decimal.RequireFromString("0.1"),
			QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity:       decimal.RequireFromString("0.001"),
		},
		Encoding: InstrumentEncoding{
			ContractMultiplier: OptionalDecimal{Value: decimal.NewFromInt(1), Valid: true},
		},
	}
}

func assertStructFields(t *testing.T, typ reflect.Type, want []string) {
	t.Helper()
	got := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		got = append(got, typ.Field(index).Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s fields = %v, want %v", typ, got, want)
	}
}

package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

const (
	testLighterPrivateKey     = "00000000000000000000000000000000000000000000000000000000000000000000000000000001"
	testHyperliquidPrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"
	testHyperliquidOwner      = "0x1111111111111111111111111111111111111111"
)

type countingTransport struct {
	requests atomic.Int64
}

func (transport *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.requests.Add(1)
	return nil, errors.New("unexpected request")
}

type hyperliquidAccountCaptureTransport struct {
	user string
}

func (transport *hyperliquidAccountCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var payload map[string]string
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	transport.user = payload["user"]
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"balances":[]}`)),
	}, nil
}

func TestTwentyKnownConfigsInferAndConstructProductClients(t *testing.T) {
	transport := new(countingTransport)
	httpClient := &http.Client{Transport: transport}
	options := []Option{
		WithEndpoint("http://exchange.test"),
		WithWebSocketEndpoint("ws://exchange.test/ws"),
		WithHTTPClient(httpClient),
	}

	var spot exchange.SpotClient
	var perp exchange.PerpClient
	var err error

	spot, err = New(BinanceSpotConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(BinanceUSDPerpConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(OKXSpotConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(OKXUSDTPerpConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(LighterSpotConfig(testLighterPrivateKey, 1, 2, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(LighterPerpConfig(testLighterPrivateKey, 1, 2, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(HyperliquidSpotConfig(testHyperliquidPrivateKey, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(HyperliquidPerpConfig(testHyperliquidPrivateKey, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(BybitSpotConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(BybitUSDTPerpConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	perp, err = New(BybitUSDCPerpConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(BitgetSpotConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(BitgetUSDTPerpConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	perp, err = New(BitgetUSDCPerpConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(GateSpotConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(GateUSDTPerpConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(AsterSpotConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(AsterUSDTPerpConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(NadoSpotConfig(testEVMPrivateKey, "default", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(NadoUSDT0PerpConfig(testEVMPrivateKey, "default", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)

	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("factory construction performed %d HTTP requests", got)
	}
}

func TestFactorySourceRegistrationIsEquivalentAndZeroIO(t *testing.T) {
	transport := new(countingTransport)
	catalog := exchange.NewDefaultMemoryCatalog()
	options := []Option{
		WithEnvironment(EnvironmentLive),
		WithEndpoint("http://factory-source.test"),
		WithWebSocketEndpoint("ws://factory-source.test/ws"),
		WithHTTPClient(&http.Client{Transport: transport}),
		WithInstrumentCatalog(catalog),
	}

	if _, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, options...)); err != nil {
		t.Fatalf("New first: %v", err)
	}
	if _, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 3, options...)); err != nil {
		t.Fatalf("New equivalent: %v", err)
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("factory source registration performed %d HTTP requests", got)
	}

	scope, descriptor, ok := catalogSourceForConfig("lighter-spot", newSettings(options))
	if !ok {
		t.Fatal("missing catalog source for lighter spot")
	}
	conflict := descriptor
	conflict.Endpoint = "http://factory-source-conflict.test"
	if err := catalog.RegisterSource(scope, conflict, exchange.StaticInstrumentLoader{}); !errors.Is(err, exchange.ErrInvalidConfig) {
		t.Fatalf("conflicting source registration error = %v, want ErrInvalidConfig", err)
	}
}

func TestFactorySourceRegistrationConflictPropagatesThroughNew(t *testing.T) {
	catalog := &conflictingFactoryCatalog{}
	first := []Option{
		WithEnvironment(EnvironmentLive),
		WithEndpoint("http://factory-source-a.test"),
		WithInstrumentCatalog(catalog),
	}
	second := []Option{
		WithEnvironment(EnvironmentLive),
		WithEndpoint("http://factory-source-b.test"),
		WithInstrumentCatalog(catalog),
	}
	if _, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, first...)); err != nil {
		t.Fatalf("New first: %v", err)
	}
	if _, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, second...)); !errors.Is(err, exchange.ErrInvalidConfig) {
		t.Fatalf("New conflicting source error = %v, want ErrInvalidConfig", err)
	}
}

func TestFactoryLighterDefaultSourceUsesSDKEndpointsAndEquivalentScope(t *testing.T) {
	catalog := exchange.NewDefaultMemoryCatalog()
	options := []Option{
		WithEnvironment(EnvironmentTestnet),
		WithInstrumentCatalog(catalog),
	}
	if _, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, options...)); err != nil {
		t.Fatalf("New first: %v", err)
	}
	if _, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 3, options...)); err != nil {
		t.Fatalf("New equivalent: %v", err)
	}
	scope, descriptor, ok := catalogSourceForConfig("lighter-spot", newSettings(options))
	if !ok {
		t.Fatal("missing lighter spot catalog source")
	}
	if descriptor.Endpoint != lighter.TestnetAPIURL || descriptor.WebSocketEndpoint != lighter.TestnetWSURL {
		t.Fatalf("descriptor endpoints = %+v, want SDK testnet defaults", descriptor)
	}
	if scope.EndpointFingerprint != descriptor.Fingerprint() {
		t.Fatalf("scope fingerprint = %q, want descriptor fingerprint %q", scope.EndpointFingerprint, descriptor.Fingerprint())
	}
}

func TestFactoryLighterPerpDefaultSourceUsesUSDCSettlementAndCatalogResolution(t *testing.T) {
	transport := new(countingTransport)
	catalog := exchange.NewDefaultMemoryCatalog()
	options := []Option{
		WithEnvironment(EnvironmentTestnet),
		WithHTTPClient(&http.Client{Transport: transport}),
		WithInstrumentCatalog(catalog),
	}
	settings := newSettings(options)
	scope, descriptor, ok := catalogSourceForConfig("lighter-perp", settings)
	if !ok {
		t.Fatal("missing lighter perp catalog source")
	}
	if scope.Settlement != "USDC" {
		t.Fatalf("lighter perp settlement = %q, want USDC", scope.Settlement)
	}
	instrument := exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue:        exchange.VenueLighter,
			Product:      exchange.ProductPerp,
			Symbol:       "ETH-USDC",
			BaseAsset:    "ETH",
			QuoteAsset:   "USDC",
			SettleAsset:  "USDC",
			NativeSymbol: "ETH-USDC",
			NativeID:     "1",
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement:    decimal.RequireFromString("0.01"),
			QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity:       decimal.RequireFromString("0.001"),
		},
		Encoding: exchange.InstrumentEncoding{
			PriceScale:    exchange.OptionalInt{Value: 2, Valid: true},
			QuantityScale: exchange.OptionalInt{Value: 3, Valid: true},
			QuoteScale:    exchange.OptionalInt{Value: 5, Valid: true},
		},
	}
	snapshot := exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		LoadedAt:      time.Unix(1, 0).UTC(),
		Instruments:   []exchange.Instrument{instrument},
	}
	if err := catalog.RegisterSource(scope, descriptor, exchange.StaticInstrumentLoader{Snapshot: snapshot}); err != nil {
		t.Fatalf("RegisterSource fixture: %v", err)
	}
	client, err := New(LighterPerpConfig(testLighterPrivateKey, 1, 2, options...))
	if err != nil {
		t.Fatalf("New LighterPerpConfig: %v", err)
	}
	rows, err := client.RefreshInstruments(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("RefreshInstruments rows=%d err=%v", len(rows), err)
	}
	resolved, err := client.ResolveInstrument(context.Background(), "ETH-USDC")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}
	if resolved.Identity.SettleAsset != "USDC" || resolved.Identity.NativeID != "1" {
		t.Fatalf("resolved lighter perp instrument = %+v", resolved.Identity)
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("Lighter perp catalog resolution performed %d HTTP requests", got)
	}
}

func TestFactoryBinanceSourceRegistrationIsEquivalentAndZeroIO(t *testing.T) {
	for _, test := range []struct {
		name       string
		configName string
		run        func(...Option) (any, error)
	}{
		{
			name:       "spot",
			configName: "binance-spot",
			run: func(options ...Option) (any, error) {
				return New(BinanceSpotConfig("key", "secret", options...))
			},
		},
		{
			name:       "usd-perp",
			configName: "binance-usd-perp",
			run: func(options ...Option) (any, error) {
				return New(BinanceUSDPerpConfig("key", "secret", options...))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := new(countingTransport)
			catalog := exchange.NewDefaultMemoryCatalog()
			options := []Option{
				WithEnvironment(EnvironmentDemo),
				WithHTTPClient(&http.Client{Transport: transport}),
				WithInstrumentCatalog(catalog),
			}
			if _, err := test.run(options...); err != nil {
				t.Fatalf("New first: %v", err)
			}
			if _, err := test.run(options...); err != nil {
				t.Fatalf("New equivalent: %v", err)
			}
			if got := transport.requests.Load(); got != 0 {
				t.Fatalf("factory source registration performed %d HTTP requests", got)
			}
			scope, descriptor, ok := catalogSourceForConfig(test.configName, newSettings(options))
			if !ok {
				t.Fatalf("missing catalog source for %s", test.configName)
			}
			if scope.EndpointFingerprint != descriptor.Fingerprint() {
				t.Fatalf("scope fingerprint = %q, want descriptor fingerprint %q", scope.EndpointFingerprint, descriptor.Fingerprint())
			}
			if strings.Contains(descriptor.Fingerprint(), "key") || strings.Contains(descriptor.Fingerprint(), "secret") {
				t.Fatalf("descriptor fingerprint leaked credential material")
			}
		})
	}
}

func TestFactoryGateSourceRegistrationIsEquivalentAndZeroIO(t *testing.T) {
	for _, test := range []struct {
		name       string
		configName string
		run        func(...Option) (any, error)
	}{
		{
			name:       "spot",
			configName: "gate-spot",
			run: func(options ...Option) (any, error) {
				return New(GateSpotConfig("key", "secret", options...))
			},
		},
		{
			name:       "usdt-perp",
			configName: "gate-usdt-perp",
			run: func(options ...Option) (any, error) {
				return New(GateUSDTPerpConfig("key", "secret", options...))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := new(countingTransport)
			catalog := exchange.NewDefaultMemoryCatalog()
			options := []Option{
				WithEnvironment(EnvironmentTestnet),
				WithHTTPClient(&http.Client{Transport: transport}),
				WithInstrumentCatalog(catalog),
			}
			if _, err := test.run(options...); err != nil {
				t.Fatalf("New first: %v", err)
			}
			if _, err := test.run(options...); err != nil {
				t.Fatalf("New equivalent: %v", err)
			}
			if got := transport.requests.Load(); got != 0 {
				t.Fatalf("factory source registration performed %d HTTP requests", got)
			}
			scope, descriptor, ok := catalogSourceForConfig(test.configName, newSettings(options))
			if !ok {
				t.Fatalf("missing catalog source for %s", test.configName)
			}
			if scope.EndpointFingerprint != descriptor.Fingerprint() {
				t.Fatalf("scope fingerprint = %q, want descriptor fingerprint %q", scope.EndpointFingerprint, descriptor.Fingerprint())
			}
		})
	}
}

func TestFactoryNadoConfigsShareAndRegisterBothCatalogScopes(t *testing.T) {
	settings := newSettings([]Option{WithEnvironment(EnvironmentTestnet)})
	spotScope, spotDescriptor, ok := catalogSourceForConfig("nado-spot", settings)
	if !ok {
		t.Fatal("missing Nado Spot catalog source")
	}
	perpScope, perpDescriptor, ok := catalogSourceForConfig("nado-usdt0-perp", settings)
	if !ok {
		t.Fatal("missing Nado Perp catalog source")
	}
	if spotScope.EndpointFingerprint != perpScope.EndpointFingerprint ||
		spotDescriptor.Fingerprint() != perpDescriptor.Fingerprint() {
		t.Fatalf("Nado catalog family fingerprints differ: spot=%q perp=%q", spotScope.EndpointFingerprint, perpScope.EndpointFingerprint)
	}
	for _, configName := range []string{"nado-spot", "nado-usdt0-perp"} {
		names := catalogRegistrationNames(configName)
		if len(names) != 2 || names[0] != "nado-spot" || names[1] != "nado-usdt0-perp" {
			t.Fatalf("%s registration names = %v", configName, names)
		}
	}
}

func TestFactoryBinanceDefaultSourceUsesSDKEndpoints(t *testing.T) {
	tests := []struct {
		name           string
		configName     string
		environment    Environment
		wantEndpoint   string
		wantWSEndpoint string
	}{
		{
			name:           "spot demo",
			configName:     "binance-spot",
			environment:    EnvironmentDemo,
			wantEndpoint:   binancespot.DemoBaseURL,
			wantWSEndpoint: binancespot.DemoWSBaseURL,
		},
		{
			name:           "spot live",
			configName:     "binance-spot",
			environment:    EnvironmentLive,
			wantEndpoint:   binancespot.BaseURL,
			wantWSEndpoint: binancespot.WSBaseURL,
		},
		{
			name:           "usd-perp demo",
			configName:     "binance-usd-perp",
			environment:    EnvironmentDemo,
			wantEndpoint:   binanceperp.DemoBaseURL,
			wantWSEndpoint: binanceperp.DemoWSPublicBaseURL,
		},
		{
			name:           "usd-perp live",
			configName:     "binance-usd-perp",
			environment:    EnvironmentLive,
			wantEndpoint:   binanceperp.BaseURL,
			wantWSEndpoint: binanceperp.WSPublicBaseURL,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := []Option{WithEnvironment(test.environment)}
			scope, descriptor, ok := catalogSourceForConfig(test.configName, newSettings(options))
			if !ok {
				t.Fatalf("missing catalog source for %s", test.configName)
			}
			if descriptor.Endpoint != test.wantEndpoint || descriptor.WebSocketEndpoint != test.wantWSEndpoint {
				t.Fatalf("descriptor endpoints = %+v", descriptor)
			}
			if scope.EndpointFingerprint != descriptor.Fingerprint() {
				t.Fatalf("scope fingerprint = %q, want descriptor fingerprint %q", scope.EndpointFingerprint, descriptor.Fingerprint())
			}
		})
	}
}

func TestFactoryGateDefaultSourceUsesSDKEndpoints(t *testing.T) {
	for _, test := range []struct {
		name           string
		configName     string
		environment    Environment
		wantEndpoint   string
		wantWSEndpoint string
	}{
		{
			name:           "spot live",
			configName:     "gate-spot",
			environment:    EnvironmentLive,
			wantEndpoint:   gate.MainnetEnvironmentProfile().RESTBaseURL,
			wantWSEndpoint: gate.MainnetEnvironmentProfile().SpotWSURL,
		},
		{
			name:           "spot testnet",
			configName:     "gate-spot",
			environment:    EnvironmentTestnet,
			wantEndpoint:   gate.TestnetEnvironmentProfile().RESTBaseURL,
			wantWSEndpoint: gate.TestnetEnvironmentProfile().SpotWSURL,
		},
		{
			name:           "usdt-perp live",
			configName:     "gate-usdt-perp",
			environment:    EnvironmentLive,
			wantEndpoint:   gate.MainnetEnvironmentProfile().RESTBaseURL,
			wantWSEndpoint: gate.MainnetEnvironmentProfile().FuturesUSDTWSURL,
		},
		{
			name:           "usdt-perp testnet",
			configName:     "gate-usdt-perp",
			environment:    EnvironmentTestnet,
			wantEndpoint:   gate.TestnetEnvironmentProfile().RESTBaseURL,
			wantWSEndpoint: gate.TestnetEnvironmentProfile().FuturesUSDTWSURL,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := []Option{WithEnvironment(test.environment)}
			scope, descriptor, ok := catalogSourceForConfig(test.configName, newSettings(options))
			if !ok {
				t.Fatalf("missing catalog source for %s", test.configName)
			}
			if descriptor.Endpoint != test.wantEndpoint || descriptor.WebSocketEndpoint != test.wantWSEndpoint {
				t.Fatalf("descriptor endpoints = %+v", descriptor)
			}
			if scope.EndpointFingerprint != descriptor.Fingerprint() {
				t.Fatalf("scope fingerprint = %q, want descriptor fingerprint %q", scope.EndpointFingerprint, descriptor.Fingerprint())
			}
		})
	}
}

func TestFactoryLighterEndpointSourceScopesAreIsolated(t *testing.T) {
	catalog := exchange.NewDefaultMemoryCatalog()
	first := []Option{
		WithEnvironment(EnvironmentTestnet),
		WithEndpoint("http://lighter-a.test"),
		WithInstrumentCatalog(catalog),
	}
	second := []Option{
		WithEnvironment(EnvironmentTestnet),
		WithEndpoint("http://lighter-b.test"),
		WithInstrumentCatalog(catalog),
	}
	if _, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, first...)); err != nil {
		t.Fatalf("New first endpoint: %v", err)
	}
	if _, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, second...)); err != nil {
		t.Fatalf("New isolated endpoint: %v", err)
	}
	firstScope, firstDescriptor, _ := catalogSourceForConfig("lighter-spot", newSettings(first))
	secondScope, secondDescriptor, _ := catalogSourceForConfig("lighter-spot", newSettings(second))
	if firstScope == secondScope || firstDescriptor.Fingerprint() == secondDescriptor.Fingerprint() {
		t.Fatalf("endpoint scopes were not isolated: first=%+v second=%+v", firstScope, secondScope)
	}
}

func TestFactoryCustomCatalogIsolationDoesNotTouchGlobalDefault(t *testing.T) {
	catalog := exchange.NewDefaultMemoryCatalog()
	options := []Option{
		WithEnvironment(EnvironmentLive),
		WithEndpoint("http://factory-custom-isolation.test"),
		WithInstrumentCatalog(catalog),
	}
	if _, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, options...)); err != nil {
		t.Fatalf("New with custom catalog: %v", err)
	}

	scope, _, ok := catalogSourceForConfig("lighter-spot", newSettings(options))
	if !ok {
		t.Fatal("missing catalog source for lighter spot")
	}
	if _, err := exchange.DefaultInstrumentCatalog().Snapshot(context.Background(), scope); !errors.Is(err, exchange.ErrMetadataResolution) {
		t.Fatalf("default catalog Snapshot error = %v, want ErrMetadataResolution for custom-only scope", err)
	}
}

func TestHyperliquidAccountAddressOptionIsExplicitAndValidatedLocally(t *testing.T) {
	transport := new(countingTransport)
	common := []Option{
		WithEnvironment(EnvironmentTestnet),
		WithHTTPClient(&http.Client{Transport: transport}),
		WithAccountAddress(testHyperliquidOwner),
	}
	spot, err := New(HyperliquidSpotConfig(testHyperliquidPrivateKey, common...))
	requireConstructed(t, spot, err)
	perp, err := New(HyperliquidPerpConfig(testHyperliquidPrivateKey, common...))
	requireConstructed(t, perp, err)

	for _, address := range []string{"", "0x1234", "not-an-address", "0x0000000000000000000000000000000000000000"} {
		t.Run(address, func(t *testing.T) {
			_, err := New(HyperliquidSpotConfig(
				testHyperliquidPrivateKey,
				WithEnvironment(EnvironmentTestnet),
				WithAccountAddress(address),
			))
			assertInvalidConfig(t, err)
		})
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("account-address option validation performed %d HTTP requests", got)
	}
}

func TestHyperliquidAccountAddressOptionReachesAccountScopedSDKRequests(t *testing.T) {
	transport := new(hyperliquidAccountCaptureTransport)
	client, err := New(HyperliquidSpotConfig(
		testHyperliquidPrivateKey,
		WithEnvironment(EnvironmentTestnet),
		WithHTTPClient(&http.Client{Transport: transport}),
		WithAccountAddress(testHyperliquidOwner),
	))
	if err != nil {
		t.Fatalf("New Hyperliquid Spot: %v", err)
	}
	if _, err := client.Balances(context.Background()); err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if transport.user != testHyperliquidOwner {
		t.Fatalf("account-scoped SDK user=%q, want explicit owner", transport.user)
	}
}

func TestAccountAddressOptionIsRejectedByConfigsWithoutAddressIdentity(t *testing.T) {
	option := WithAccountAddress(testHyperliquidOwner)
	environment := WithEnvironment(EnvironmentLive)
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "Binance Spot", run: func() error {
			_, err := New(BinanceSpotConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Binance Perp", run: func() error {
			_, err := New(BinanceUSDPerpConfig("key", "secret", environment, option))
			return err
		}},
		{name: "OKX Spot", run: func() error {
			_, err := New(OKXSpotConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "OKX Perp", run: func() error {
			_, err := New(OKXUSDTPerpConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "Lighter Spot", run: func() error {
			_, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, environment, option))
			return err
		}},
		{name: "Lighter Perp", run: func() error {
			_, err := New(LighterPerpConfig(testLighterPrivateKey, 1, 2, environment, option))
			return err
		}},
		{name: "Bybit Spot", run: func() error {
			_, err := New(BybitSpotConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Bybit USDT Perp", run: func() error {
			_, err := New(BybitUSDTPerpConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Bybit USDC Perp", run: func() error {
			_, err := New(BybitUSDCPerpConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Bitget Spot", run: func() error {
			_, err := New(BitgetSpotConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "Bitget USDT Perp", run: func() error {
			_, err := New(BitgetUSDTPerpConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "Bitget USDC Perp", run: func() error {
			_, err := New(BitgetUSDCPerpConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "Gate Spot", run: func() error {
			_, err := New(GateSpotConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Gate Perp", run: func() error {
			_, err := New(GateUSDTPerpConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Aster Spot", run: func() error {
			_, err := New(AsterSpotConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, environment, option))
			return err
		}},
		{name: "Aster Perp", run: func() error {
			_, err := New(AsterUSDTPerpConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, environment, option))
			return err
		}},
		{name: "Nado Spot", run: func() error {
			_, err := New(NadoSpotConfig(testEVMPrivateKey, "default", environment, option))
			return err
		}},
		{name: "Nado Perp", run: func() error {
			_, err := New(NadoUSDT0PerpConfig(testEVMPrivateKey, "default", environment, option))
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertInvalidConfig(t, test.run())
		})
	}
}

func TestZeroAndInvalidConfigsFailLocally(t *testing.T) {
	_, err := New(Config[exchange.SpotClient]{})
	assertInvalidConfig(t, err)

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "binance missing key",
			run: func() error {
				_, err := New(BinanceSpotConfig("", "secret", WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "okx missing passphrase",
			run: func() error {
				_, err := New(OKXUSDTPerpConfig("key", "secret", "", WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "lighter negative account",
			run: func() error {
				_, err := New(LighterPerpConfig(testLighterPrivateKey, -1, 2, WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "hyperliquid invalid key",
			run: func() error {
				_, err := New(HyperliquidSpotConfig("not-a-private-key", WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "hyperliquid zero scalar",
			run: func() error {
				_, err := New(HyperliquidPerpConfig(strings.Repeat("0", 64), WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "hyperliquid scalar outside curve",
			run: func() error {
				_, err := New(HyperliquidPerpConfig(strings.Repeat("f", 64), WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "lighter wrong key length",
			run: func() error {
				_, err := New(LighterSpotConfig(strings.Repeat("1", 66), 1, 2, WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "endpoint user info",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithEndpoint("https://user:password@example.com"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "endpoint query",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithEndpoint("https://example.com?credential=leak"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "endpoint fragment",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithEndpoint("https://example.com#fragment"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "websocket endpoint uses HTTP scheme",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithWebSocketEndpoint("https://example.com/ws"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "websocket endpoint user info",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithWebSocketEndpoint("wss://user:password@example.com/ws"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "websocket endpoint query",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithWebSocketEndpoint("wss://example.com/ws?token=leak"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "nil http client",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithHTTPClient(nil),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertInvalidConfig(t, test.run())
		})
	}
}

func TestConfigFormattingRedactsCredentials(t *testing.T) {
	canaries := []string{
		"API-KEY-CANARY",
		"SECRET-CANARY",
		"PASSPHRASE-CANARY",
	}
	config := OKXSpotConfig(canaries[0], canaries[1], canaries[2])
	for _, formatted := range []string{
		fmt.Sprintf("%s", config),
		fmt.Sprintf("%v", config),
		fmt.Sprintf("%+v", config),
		fmt.Sprintf("%#v", config),
	} {
		for _, canary := range canaries {
			if strings.Contains(formatted, canary) {
				t.Fatalf("config formatting leaked %q: %s", canary, formatted)
			}
		}
	}
}

func TestConstructedClientFormattingRedactsCredentials(t *testing.T) {
	const (
		lighterPrivateKey     = "0000000000000000000000000000000000000000000000000000000000000000cafebabedeadbeef"
		hyperliquidPrivateKey = "00000000000000000000000000000000000000000000000000000000cafebabe"
	)

	tests := []struct {
		name     string
		build    func() (any, error)
		want     string
		canaries []string
	}{
		{
			name: "binance spot",
			build: func() (any, error) {
				return New(BinanceSpotConfig(
					"BINANCE-SPOT-KEY-CANARY",
					"BINANCE-SPOT-SECRET-CANARY",
					WithEnvironment(EnvironmentLive),
				))
			},
			want:     `exchange/factory.Client{venue:"binance", product:"spot", credentials:redacted}`,
			canaries: []string{"BINANCE-SPOT-KEY-CANARY", "BINANCE-SPOT-SECRET-CANARY"},
		},
		{
			name: "binance perp",
			build: func() (any, error) {
				return New(BinanceUSDPerpConfig(
					"BINANCE-PERP-KEY-CANARY",
					"BINANCE-PERP-SECRET-CANARY",
					WithEnvironment(EnvironmentLive),
				))
			},
			want:     `exchange/factory.Client{venue:"binance", product:"perp", credentials:redacted}`,
			canaries: []string{"BINANCE-PERP-KEY-CANARY", "BINANCE-PERP-SECRET-CANARY"},
		},
		{
			name: "okx spot",
			build: func() (any, error) {
				return New(OKXSpotConfig(
					"OKX-SPOT-KEY-CANARY",
					"OKX-SPOT-SECRET-CANARY",
					"OKX-SPOT-PASSPHRASE-CANARY",
					WithEnvironment(EnvironmentLive),
				))
			},
			want:     `exchange/factory.Client{venue:"okx", product:"spot", credentials:redacted}`,
			canaries: []string{"OKX-SPOT-KEY-CANARY", "OKX-SPOT-SECRET-CANARY", "OKX-SPOT-PASSPHRASE-CANARY"},
		},
		{
			name: "okx perp",
			build: func() (any, error) {
				return New(OKXUSDTPerpConfig(
					"OKX-PERP-KEY-CANARY",
					"OKX-PERP-SECRET-CANARY",
					"OKX-PERP-PASSPHRASE-CANARY",
					WithEnvironment(EnvironmentLive),
				))
			},
			want:     `exchange/factory.Client{venue:"okx", product:"perp", credentials:redacted}`,
			canaries: []string{"OKX-PERP-KEY-CANARY", "OKX-PERP-SECRET-CANARY", "OKX-PERP-PASSPHRASE-CANARY"},
		},
		{
			name: "lighter spot",
			build: func() (any, error) {
				return New(LighterSpotConfig(lighterPrivateKey, 1, 2, WithEnvironment(EnvironmentLive)))
			},
			want:     `exchange/factory.Client{venue:"lighter", product:"spot", credentials:redacted}`,
			canaries: []string{lighterPrivateKey, "cafebabedeadbeef"},
		},
		{
			name: "lighter perp",
			build: func() (any, error) {
				return New(LighterPerpConfig(lighterPrivateKey, 1, 2, WithEnvironment(EnvironmentLive)))
			},
			want:     `exchange/factory.Client{venue:"lighter", product:"perp", credentials:redacted}`,
			canaries: []string{lighterPrivateKey, "cafebabedeadbeef"},
		},
		{
			name: "hyperliquid spot",
			build: func() (any, error) {
				return New(HyperliquidSpotConfig(hyperliquidPrivateKey, WithEnvironment(EnvironmentLive)))
			},
			want:     `exchange/factory.Client{venue:"hyperliquid", product:"spot", credentials:redacted}`,
			canaries: []string{hyperliquidPrivateKey, "cafebabe"},
		},
		{
			name: "hyperliquid perp",
			build: func() (any, error) {
				return New(HyperliquidPerpConfig(hyperliquidPrivateKey, WithEnvironment(EnvironmentLive)))
			},
			want:     `exchange/factory.Client{venue:"hyperliquid", product:"perp", credentials:redacted}`,
			canaries: []string{hyperliquidPrivateKey, "cafebabe"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := test.build()
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			formatted := map[string]string{
				"%s":  fmt.Sprintf("%s", client),
				"%v":  fmt.Sprintf("%v", client),
				"%+v": fmt.Sprintf("%+v", client),
				"%#v": fmt.Sprintf("%#v", client),
			}
			for verb, output := range formatted {
				if output != test.want {
					t.Fatalf("%s formatting = %s, want %s", verb, output, test.want)
				}
				for _, canary := range test.canaries {
					if strings.Contains(output, canary) {
						t.Fatalf("%s formatting leaked %q: %s", verb, canary, output)
					}
				}
			}
		})
	}
}

func TestVenueEnvironmentValidation(t *testing.T) {
	valid := []struct {
		name string
		run  func() error
	}{
		{
			name: "binance spot demo",
			run: func() error {
				_, err := New(BinanceSpotConfig("key", "secret", WithEnvironment(EnvironmentDemo)))
				return err
			},
		},
		{
			name: "binance perp demo",
			run: func() error {
				_, err := New(BinanceUSDPerpConfig("key", "secret", WithEnvironment(EnvironmentDemo)))
				return err
			},
		},
		{
			name: "okx spot demo",
			run: func() error {
				_, err := New(OKXSpotConfig("key", "secret", "passphrase", WithEnvironment(EnvironmentDemo)))
				return err
			},
		},
		{
			name: "okx perp demo",
			run: func() error {
				_, err := New(OKXUSDTPerpConfig("key", "secret", "passphrase", WithEnvironment(EnvironmentDemo)))
				return err
			},
		},
		{
			name: "lighter spot testnet",
			run: func() error {
				_, err := New(LighterSpotConfig(
					testLighterPrivateKey,
					1,
					2,
					WithEnvironment(EnvironmentTestnet),
				))
				return err
			},
		},
		{
			name: "lighter perp testnet",
			run: func() error {
				_, err := New(LighterPerpConfig(
					testLighterPrivateKey,
					1,
					2,
					WithEnvironment(EnvironmentTestnet),
				))
				return err
			},
		},
		{
			name: "hyperliquid spot testnet",
			run: func() error {
				_, err := New(HyperliquidSpotConfig(
					testHyperliquidPrivateKey,
					WithEnvironment(EnvironmentTestnet),
				))
				return err
			},
		},
		{
			name: "hyperliquid perp testnet",
			run: func() error {
				_, err := New(HyperliquidPerpConfig(
					testHyperliquidPrivateKey,
					WithEnvironment(EnvironmentTestnet),
				))
				return err
			},
		},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err != nil {
				t.Fatal(err)
			}
		})
	}

	invalid := []struct {
		name string
		run  func() error
	}{
		{
			name: "binance rejects testnet label",
			run: func() error {
				_, err := New(BinanceSpotConfig("key", "secret", WithEnvironment(EnvironmentTestnet)))
				return err
			},
		},
		{
			name: "lighter rejects demo",
			run: func() error {
				_, err := New(LighterPerpConfig(
					testLighterPrivateKey,
					1,
					2,
					WithEnvironment(EnvironmentDemo),
				))
				return err
			},
		},
		{
			name: "unknown environment",
			run: func() error {
				_, err := New(HyperliquidPerpConfig(
					testHyperliquidPrivateKey,
					WithEnvironment(Environment("mars")),
				))
				return err
			},
		},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			assertInvalidConfig(t, test.run())
		})
	}
}

func TestOmittedEnvironmentFailsLocallyBeforeIO(t *testing.T) {
	transport := new(countingTransport)
	_, err := New(BinanceSpotConfig(
		"key",
		"secret",
		WithHTTPClient(&http.Client{Transport: transport}),
	))
	assertInvalidConfig(t, err)
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("omitted environment performed %d HTTP requests", got)
	}
}

func TestConcurrentConstructionHasNoSharedClientState(t *testing.T) {
	config := BinanceSpotConfig(
		"key",
		"secret",
		WithEndpoint("http://exchange.test"),
		WithHTTPClient(&http.Client{Transport: new(countingTransport)}),
		WithEnvironment(EnvironmentLive),
	)

	const workers = 32
	var wait sync.WaitGroup
	errorsFound := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			client, err := New(config)
			if err != nil {
				errorsFound <- err
				return
			}
			if client == nil {
				errorsFound <- errors.New("nil client")
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func requireConstructed[C any](t *testing.T, client C, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if any(client) == nil {
		t.Fatal("factory returned nil client")
	}
}

func assertInvalidConfig(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, exchange.ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
	if _, ok := err.(*exchange.Error); !ok {
		t.Fatalf("error type = %T, want *exchange.Error", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("invalid config error unexpectedly unwraps: %v", errors.Unwrap(err))
	}
}

type conflictingFactoryCatalog struct {
	mu         sync.Mutex
	seen       bool
	descriptor exchange.SourceDescriptor
}

func (catalog *conflictingFactoryCatalog) RegisterSource(_ exchange.CatalogScope, descriptor exchange.SourceDescriptor, _ exchange.InstrumentLoader) error {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if !catalog.seen {
		catalog.seen = true
		catalog.descriptor = descriptor
		return nil
	}
	if catalog.descriptor != descriptor {
		return exchange.NewError(exchange.KindInvalidConfig, exchange.ErrorDetails{
			Operation:   "factory.New",
			SafeMessage: "instrument source registration conflicts with existing source",
		})
	}
	return nil
}

func (*conflictingFactoryCatalog) Resolve(context.Context, exchange.CatalogScope, exchange.InstrumentQuery) (exchange.Instrument, error) {
	return exchange.Instrument{}, nil
}

func (*conflictingFactoryCatalog) Snapshot(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	return exchange.InstrumentSnapshot{}, nil
}

func (*conflictingFactoryCatalog) Refresh(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	return exchange.InstrumentSnapshot{}, nil
}

func (*conflictingFactoryCatalog) Status(exchange.CatalogScope) exchange.CatalogStatus {
	return exchange.CatalogStatus{}
}

func optionsWithEnvironment(options []Option, environment Environment) []Option {
	result := make([]Option, 0, len(options)+1)
	result = append(result, options...)
	return append(result, WithEnvironment(environment))
}

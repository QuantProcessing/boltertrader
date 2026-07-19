package exchange_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/shopspring/decimal"
)

const exchangeAcceptanceNotional = "50"

type exchangeAcceptanceRow struct {
	code                      string
	product                   exchange.Product
	instrumentHint            string
	notional                  decimal.Decimal
	maxNotional               decimal.Decimal
	perpReference             exchangeAcceptancePerpReference
	perpPublicWS              exchangeAcceptancePerpPublicWebSocket
	watchMarkPriceUnsupported bool
}

type exchangeAcceptancePerpReference interface {
	FundingRate(context.Context, exchange.FundingRateRequest) (exchange.FundingRate, error)
	FundingRateHistory(context.Context, exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error)
}

type exchangeAcceptancePerpPublicWebSocket interface {
	WatchMarkPrice(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.MarkPriceEvent], error)
	WatchFundingRate(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.FundingRateEvent], error)
}

type exchangeAcceptanceREST interface {
	exchange.MarketREST
	exchange.OrderREST
	Balances(context.Context) ([]exchange.Balance, error)
}

type spotExposureCleanupClient interface {
	Balances(context.Context) ([]exchange.Balance, error)
	OrderBook(context.Context, exchange.OrderBookRequest) (exchange.OrderBook, error)
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
}

type perpExposureCleanupClient interface {
	Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error)
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
}

type spotAcceptanceCleanupClient interface {
	orderCleanupClient
	spotExposureCleanupClient
}

type perpAcceptanceCleanupClient interface {
	orderCleanupClient
	perpExposureCleanupClient
}

type exchangeAcceptanceOrderTransport interface {
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
	CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error)
}

type acceptanceOrderTransport struct {
	caseName       string
	coverageName   string
	orderTransport exchangeAcceptanceOrderTransport
}

type acceptanceSubscriptionWitness struct {
	operation string
	arm       func()
	waitEvent func(context.Context) error
	close     func() error
}

func (witness *acceptanceSubscriptionWitness) Arm() {
	witness.arm()
}

func (witness *acceptanceSubscriptionWitness) WaitEvent(ctx context.Context) error {
	return witness.waitEvent(ctx)
}

func (witness *acceptanceSubscriptionWitness) Close() error {
	return witness.close()
}

var exchangeAcceptanceClientID atomic.Int64
var exchangeAcceptanceClientIDSeed = uint64(time.Now().UnixNano()) & (1<<48 - 1)

func TestExchangeBinanceSpotDemoAcceptance(t *testing.T) {
	testenv.RequireBinanceDemoWrite(t)
	httpClient, err := testenv.BinanceDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Binance Demo HTTP client: %v", err)
	}
	client, err := factory.New(factory.BinanceSpotConfig(
		os.Getenv(testenv.BinanceDemoAPIKeyEnv),
		os.Getenv(testenv.BinanceDemoAPISecretEnv),
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Binance Spot Demo exchange: %v", err)
	}
	runExchangeSpotAcceptance(t, exchangeAcceptanceRow{
		code:           "BNS",
		product:        exchange.ProductSpot,
		instrumentHint: envOrDefault("BINANCE_DEMO_SYMBOL", "ETH-USDT"),
		notional:       acceptanceNotional(t),
	}, client)
}

func TestExchangeBinancePerpDemoAcceptance(t *testing.T) {
	testenv.RequireBinanceDemoWrite(t)
	httpClient, err := testenv.BinanceDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Binance Demo HTTP client: %v", err)
	}
	client, err := factory.New(factory.BinanceUSDPerpConfig(
		os.Getenv(testenv.BinanceDemoAPIKeyEnv),
		os.Getenv(testenv.BinanceDemoAPISecretEnv),
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Binance Perp Demo exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "BNP",
		product:        exchange.ProductPerp,
		instrumentHint: envOrDefault("BINANCE_DEMO_SYMBOL", "ETH-USDT"),
		notional:       acceptanceNotional(t),
	}, client)
}

func TestExchangeOKXSpotDemoAcceptance(t *testing.T) {
	cfg := testenv.RequireOKXDemoWrite(t)
	httpClient, err := testenv.OKXDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("OKX Demo HTTP client: %v", err)
	}
	options := []factory.Option{
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	}
	if cfg.RESTBaseURL != "" {
		options = append(options, factory.WithEndpoint(cfg.RESTBaseURL))
	}
	if cfg.WSBaseURL != "" {
		options = append(options, factory.WithWebSocketEndpoint(cfg.WSBaseURL))
	}
	client, err := factory.New(factory.OKXSpotConfig(
		cfg.APIKey,
		cfg.APISecret,
		cfg.Passphrase,
		options...,
	))
	if err != nil {
		t.Fatalf("construct OKX Spot Demo exchange: %v", err)
	}
	runExchangeSpotAcceptance(t, exchangeAcceptanceRow{
		code:           "OXS",
		product:        exchange.ProductSpot,
		instrumentHint: cfg.SpotSymbol,
		notional:       acceptanceNotional(t),
	}, client)
}

func TestExchangeOKXPerpDemoAcceptance(t *testing.T) {
	cfg := testenv.RequireOKXDemoWrite(t)
	httpClient, err := testenv.OKXDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("OKX Demo HTTP client: %v", err)
	}
	options := []factory.Option{
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	}
	if cfg.RESTBaseURL != "" {
		options = append(options, factory.WithEndpoint(cfg.RESTBaseURL))
	}
	if cfg.WSBaseURL != "" {
		options = append(options, factory.WithWebSocketEndpoint(cfg.WSBaseURL))
	}
	client, err := factory.New(factory.OKXUSDTPerpConfig(
		cfg.APIKey,
		cfg.APISecret,
		cfg.Passphrase,
		options...,
	))
	if err != nil {
		t.Fatalf("construct OKX Perp Demo exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "OXP",
		product:        exchange.ProductPerp,
		instrumentHint: cfg.PerpSymbol,
		notional:       acceptanceNotional(t),
	}, client)
}

func TestExchangeLighterSpotTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireLighterTestnetWrite(t)
	httpClient, err := testenv.LighterTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Lighter Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.LighterSpotConfig(
		cfg.PrivateKey,
		cfg.AccountIndex,
		cfg.APIKeyIndex,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Lighter Spot Testnet exchange: %v", err)
	}
	runExchangeSpotAcceptance(t, exchangeAcceptanceRow{
		code:           "LIS",
		product:        exchange.ProductSpot,
		instrumentHint: cfg.SpotSymbol,
		notional:       acceptanceNotional(t),
	}, client)
}

func TestExchangeLighterPerpTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireLighterTestnetWrite(t)
	httpClient, err := testenv.LighterTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Lighter Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.LighterPerpConfig(
		cfg.PrivateKey,
		cfg.AccountIndex,
		cfg.APIKeyIndex,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Lighter Perp Testnet exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "LIP",
		product:        exchange.ProductPerp,
		instrumentHint: cfg.PerpSymbol,
		notional:       acceptanceNotional(t),
	}, client)
}

func TestExchangeHyperliquidSpotTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetWrite(t)
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Hyperliquid Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.HyperliquidSpotConfig(
		cfg.PrivateKey,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
		factory.WithAccountAddress(cfg.AccountAddress),
	))
	if err != nil {
		t.Fatalf("construct Hyperliquid Spot Testnet exchange: %v", err)
	}
	runExchangeSpotAcceptance(t, exchangeAcceptanceRow{
		code:           "HLS",
		product:        exchange.ProductSpot,
		instrumentHint: envOrDefault(testenv.HyperliquidTestnetSpotSymbolEnv, "PURR-USDC"),
		notional:       acceptanceNotional(t),
	}, client)
}

func TestExchangeHyperliquidPerpTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetWrite(t)
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Hyperliquid Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.HyperliquidPerpConfig(
		cfg.PrivateKey,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
		factory.WithAccountAddress(cfg.AccountAddress),
	))
	if err != nil {
		t.Fatalf("construct Hyperliquid Perp Testnet exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "HLP",
		product:        exchange.ProductPerp,
		instrumentHint: envOrDefault(testenv.HyperliquidTestnetPerpSymbolEnv, "BTC-USDC"),
		notional:       acceptanceNotional(t),
	}, client)
}

func TestExchangeBybitSpotDemoAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoWrite(t)
	httpClient, err := testenv.BybitDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bybit Demo HTTP client: %v", err)
	}
	client, err := factory.New(factory.BybitSpotConfig(
		cfg.APIKey,
		cfg.APISecret,
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Bybit Spot Demo exchange: %v", err)
	}
	runExchangeSpotAcceptance(t, exchangeAcceptanceRow{
		code:           "BYS",
		product:        exchange.ProductSpot,
		instrumentHint: cfg.SpotSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDT,
	}, client)
}

func TestExchangeBybitUSDTPerpDemoAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoWrite(t)
	httpClient, err := testenv.BybitDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bybit Demo HTTP client: %v", err)
	}
	client, err := factory.New(factory.BybitUSDTPerpConfig(
		cfg.APIKey,
		cfg.APISecret,
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Bybit USDT Perp Demo exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "BYU",
		product:        exchange.ProductPerp,
		instrumentHint: cfg.USDTPerpSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDT,
	}, client)
}

func TestExchangeBybitUSDCPerpDemoAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoWrite(t)
	httpClient, err := testenv.BybitDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bybit Demo HTTP client: %v", err)
	}
	client, err := factory.New(factory.BybitUSDCPerpConfig(
		cfg.APIKey,
		cfg.APISecret,
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Bybit USDC Perp Demo exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "BYC",
		product:        exchange.ProductPerp,
		instrumentHint: cfg.USDCPerpSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDC,
	}, client)
}

func TestExchangeBitgetSpotDemoAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoWrite(t)
	httpClient, err := testenv.BitgetDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bitget Demo HTTP client: %v", err)
	}
	client, err := factory.New(factory.BitgetSpotConfig(
		cfg.APIKey,
		cfg.APISecret,
		cfg.Passphrase,
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Bitget Spot Demo exchange: %v", err)
	}
	runExchangeSpotAcceptance(t, exchangeAcceptanceRow{
		code:           "BGS",
		product:        exchange.ProductSpot,
		instrumentHint: cfg.SpotSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDT,
	}, client)
}

func TestExchangeBitgetUSDTPerpDemoAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoWrite(t)
	httpClient, err := testenv.BitgetDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bitget Demo HTTP client: %v", err)
	}
	client, err := factory.New(factory.BitgetUSDTPerpConfig(
		cfg.APIKey,
		cfg.APISecret,
		cfg.Passphrase,
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Bitget USDT Perp Demo exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "BGU",
		product:        exchange.ProductPerp,
		instrumentHint: cfg.USDTPerpSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDT,
	}, client)
}

func TestExchangeBitgetUSDCPerpDemoAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoWrite(t)
	httpClient, err := testenv.BitgetDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bitget Demo HTTP client: %v", err)
	}
	client, err := factory.New(factory.BitgetUSDCPerpConfig(
		cfg.APIKey,
		cfg.APISecret,
		cfg.Passphrase,
		factory.WithEnvironment(factory.EnvironmentDemo),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Bitget USDC Perp Demo exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "BGC",
		product:        exchange.ProductPerp,
		instrumentHint: cfg.USDCPerpSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDC,
	}, client)
}

func TestExchangeGateSpotTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireGateTestnetWrite(t)
	httpClient, err := testenv.GateTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Gate Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.GateSpotConfig(
		cfg.APIKey,
		cfg.APISecret,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Gate Spot Testnet exchange: %v", err)
	}
	runExchangeSpotAcceptance(t, exchangeAcceptanceRow{
		code:           "GTS",
		product:        exchange.ProductSpot,
		instrumentHint: cfg.SpotSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDT,
	}, client)
}

func TestExchangeGateUSDTPerpTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireGateTestnetWrite(t)
	httpClient, err := testenv.GateTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Gate Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.GateUSDTPerpConfig(
		cfg.APIKey,
		cfg.APISecret,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Gate USDT Perp Testnet exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "GTU",
		product:        exchange.ProductPerp,
		instrumentHint: cfg.USDTPerpSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDT,
	}, client)
}

func TestExchangeAsterSpotTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireAsterTestnetWrite(t)
	httpClient, err := testenv.AsterTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Aster Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.AsterSpotConfig(
		cfg.UserAddress,
		cfg.SignerPrivateKey,
		cfg.ExpectedSignerAddress,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Aster Spot Testnet exchange: %v", err)
	}
	runExchangeSpotAcceptance(t, exchangeAcceptanceRow{
		code:           "ATS",
		product:        exchange.ProductSpot,
		instrumentHint: cfg.SpotSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDT,
	}, client)
}

func TestExchangeAsterUSDTPerpTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireAsterTestnetWrite(t)
	httpClient, err := testenv.AsterTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Aster Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.AsterUSDTPerpConfig(
		cfg.UserAddress,
		cfg.SignerPrivateKey,
		cfg.ExpectedSignerAddress,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Aster USDT Perp Testnet exchange: %v", err)
	}
	liveReference, err := factory.New(factory.AsterUSDTPerpConfig(
		cfg.UserAddress,
		cfg.SignerPrivateKey,
		cfg.ExpectedSignerAddress,
		factory.WithEnvironment(factory.EnvironmentLive),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Aster USDT Perp production reference client: %v", err)
	}
	defer closeAcceptanceClient(t, liveReference)
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:           "ATP",
		product:        exchange.ProductPerp,
		instrumentHint: cfg.PerpSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDT,
		perpReference:  liveReference,
		perpPublicWS:   liveReference.WebSocket(),
	}, client)
}

func TestExchangeNadoSpotTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireNadoTestnetWrite(t)
	httpClient, err := testenv.NadoTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Nado Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.NadoSpotConfig(
		cfg.PrivateKey,
		cfg.Subaccount,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Nado Spot Testnet exchange: %v", err)
	}
	runExchangeSpotAcceptance(t, exchangeAcceptanceRow{
		code:           "NDS",
		product:        exchange.ProductSpot,
		instrumentHint: cfg.SpotSymbol,
		notional:       acceptanceNotional(t),
		maxNotional:    cfg.MaxNotionalUSDT0,
	}, client)
}

func TestExchangeNadoUSDT0PerpTestnetAcceptance(t *testing.T) {
	cfg := testenv.RequireNadoTestnetWrite(t)
	httpClient, err := testenv.NadoTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Nado Testnet HTTP client: %v", err)
	}
	client, err := factory.New(factory.NadoUSDT0PerpConfig(
		cfg.PrivateKey,
		cfg.Subaccount,
		factory.WithEnvironment(factory.EnvironmentTestnet),
		factory.WithHTTPClient(httpClient),
	))
	if err != nil {
		t.Fatalf("construct Nado USDT0 Perp Testnet exchange: %v", err)
	}
	runExchangePerpAcceptance(t, exchangeAcceptanceRow{
		code:                      "NDP",
		product:                   exchange.ProductPerp,
		instrumentHint:            cfg.PerpSymbol,
		notional:                  acceptanceNotional(t),
		maxNotional:               cfg.MaxNotionalUSDT0,
		watchMarkPriceUnsupported: true,
	}, client)
}

func runExchangeSpotAcceptance(t *testing.T, row exchangeAcceptanceRow, client exchange.SpotClient) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	defer closeAcceptanceClient(t, client)

	coverage := newExternalRowCoverage(t, buildExternalAcceptanceLedger(t, loadExternalAcceptanceManifest(t)), row.code)
	journal := newOwnedOrderJournal()
	instrument, book := exerciseCommonREST(t, ctx, row, client, coverage)

	baseline, err := client.Balances(ctx)
	if err != nil {
		t.Fatalf("%s baseline Balances: %v", row.code, err)
	}
	defer func() {
		if err := finalizeSpotAcceptance(row, client, instrument, baseline, journal); err != nil {
			t.Errorf("%s final cleanup: %v", row.code, err)
		}
	}()

	if _, err := client.SpotAccount(ctx); err != nil {
		t.Fatalf("%s SpotAccount: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "SpotAccount")

	socket := client.WebSocket()
	privateWitnesses := exerciseSpotWebSocket(t, ctx, row, socket, instrument.Symbol, coverage)
	exerciseInvalidOrderRequests(t, ctx, row, client, "rest", coverage)
	exerciseInvalidOrderRequests(t, ctx, row, socket, "ws", coverage)
	armAcceptanceWitnesses(privateWitnesses)
	exerciseSpotOrderCases(t, ctx, row, client, socket, instrument, book, baseline, journal, coverage)
	requireAcceptanceWitnessEvents(t, ctx, privateWitnesses)
	closeAcceptanceSocket(t, row.code, socket, coverage)
	if !t.Failed() {
		if err := coverage.Validate(); err != nil {
			t.Fatalf("%s external coverage: %v", row.code, err)
		}
	}
}

func runExchangePerpAcceptance(t *testing.T, row exchangeAcceptanceRow, client exchange.PerpClient) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	defer closeAcceptanceClient(t, client)

	coverage := newExternalRowCoverage(t, buildExternalAcceptanceLedger(t, loadExternalAcceptanceManifest(t)), row.code)
	journal := newOwnedOrderJournal()
	instrument, book := exerciseCommonREST(t, ctx, row, client, coverage)

	baselinePositions, err := client.Positions(ctx, exchange.PositionsRequest{Instrument: instrument.Symbol})
	if err != nil {
		t.Fatalf("%s baseline Positions: %v", row.code, err)
	}
	baselinePosition := signedAcceptancePosition(baselinePositions, instrument.Symbol)
	defer func() {
		if err := finalizePerpAcceptance(row, client, instrument, baselinePosition, journal); err != nil {
			t.Errorf("%s final cleanup: %v", row.code, err)
		}
	}()

	if _, err := client.PerpAccount(ctx); err != nil {
		t.Fatalf("%s PerpAccount: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "PerpAccount")
	if _, err := client.Positions(ctx, exchange.PositionsRequest{Instrument: instrument.Symbol}); err != nil {
		t.Fatalf("%s Positions: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "Positions")
	reference := exchangeAcceptancePerpReference(client)
	if row.perpReference != nil {
		reference = row.perpReference
	}
	if _, err := reference.FundingRate(ctx, exchange.FundingRateRequest{Instrument: instrument.Symbol}); err != nil {
		t.Fatalf("%s FundingRate: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "FundingRate")
	if _, err := reference.FundingRateHistory(ctx, exchange.FundingRateHistoryRequest{
		Instrument: instrument.Symbol,
		Limit:      10,
	}); err != nil {
		t.Fatalf("%s FundingRateHistory: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "FundingRateHistory")
	socket := client.WebSocket()
	privateWitnesses := exercisePerpWebSocket(t, ctx, row, socket, instrument.Symbol, coverage)
	exerciseInvalidOrderRequests(t, ctx, row, client, "rest", coverage)
	exerciseInvalidOrderRequests(t, ctx, row, socket, "ws", coverage)
	armAcceptanceWitnesses(privateWitnesses)
	exercisePerpOrderCases(t, ctx, row, client, socket, instrument, book, baselinePosition, journal, coverage)
	requireAcceptanceWitnessEvents(t, ctx, privateWitnesses)
	closeAcceptanceSocket(t, row.code, socket, coverage)
	if _, err := client.SetLeverage(ctx, exchange.SetLeverageRequest{
		Instrument: instrument.Symbol,
		Leverage:   1,
	}); err != nil {
		t.Fatalf("%s SetLeverage: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "SetLeverage")
	if !t.Failed() {
		if err := coverage.Validate(); err != nil {
			t.Fatalf("%s external coverage: %v", row.code, err)
		}
	}
}

func exerciseCommonREST(
	t *testing.T,
	ctx context.Context,
	row exchangeAcceptanceRow,
	client exchangeAcceptanceREST,
	coverage *externalRowCoverage,
) (exchange.Instrument, exchange.OrderBook) {
	t.Helper()
	instruments, err := client.Instruments(ctx)
	if err != nil {
		t.Fatalf("%s Instruments: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "Instruments")
	instrument, err := selectAcceptanceInstrument(instruments, row.product, row.instrumentHint)
	if err != nil {
		t.Fatalf("%s select instrument: %v", row.code, err)
	}
	book, err := client.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument.Symbol, Limit: 20})
	if err != nil {
		t.Fatalf("%s OrderBook: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "OrderBook")
	if len(book.Bids) == 0 || len(book.Asks) == 0 ||
		!book.Bids[0].Price.IsPositive() || !book.Asks[0].Price.IsPositive() {
		t.Fatalf("%s OrderBook has no positive top of book for %s", row.code, instrument.Symbol)
	}
	if _, err := client.Candles(ctx, exchange.CandlesRequest{
		Instrument: instrument.Symbol,
		Interval:   "1m",
		Limit:      2,
	}); err != nil {
		t.Fatalf("%s Candles: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "Candles")
	if _, err := client.PublicTrades(ctx, exchange.PublicTradesRequest{
		Instrument: instrument.Symbol,
		Limit:      10,
	}); err != nil {
		t.Fatalf("%s PublicTrades: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "PublicTrades")
	open, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{
		Instrument: instrument.Symbol,
		Limit:      100,
	})
	if err != nil {
		t.Fatalf("%s OpenOrders: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "OpenOrders")
	if len(open.Orders) != 0 {
		t.Fatalf("%s requires no pre-existing open order for %s; got %d", row.code, instrument.Symbol, len(open.Orders))
	}
	if _, err := client.OrderHistory(ctx, exchange.OrderHistoryRequest{
		Instrument: instrument.Symbol,
		Limit:      20,
	}); err != nil {
		t.Fatalf("%s OrderHistory: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "OrderHistory")
	if _, err := client.Fills(ctx, exchange.FillsRequest{
		Instrument: instrument.Symbol,
		Limit:      20,
	}); err != nil {
		t.Fatalf("%s Fills: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "Fills")
	balances, err := client.Balances(ctx)
	if err != nil {
		t.Fatalf("%s Balances: %v", row.code, err)
	}
	coverage.MarkOperation("rest", "Balances")
	if len(balances) == 0 {
		t.Fatalf("%s Balances returned no account rows", row.code)
	}
	return instrument, book
}

func exerciseInvalidOrderRequests(
	t *testing.T,
	ctx context.Context,
	row exchangeAcceptanceRow,
	transport exchangeAcceptanceOrderTransport,
	transportName string,
	coverage *externalRowCoverage,
) {
	t.Helper()
	instrument := row.instrumentHint
	invalid := []struct {
		caseID  string
		request exchange.PlaceOrderRequest
	}{
		{
			caseID: "place_order.invalid.market_price_or_policy",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "1",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeMarket,
				Quantity:      decimal.NewFromInt(1),
				LimitPrice:    decimal.NewFromInt(1),
			},
		},
		{
			caseID: "place_order.invalid.market_price_or_policy",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "1",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeMarket,
				Quantity:      decimal.NewFromInt(1),
				LimitPolicy:   exchange.LimitPolicyIOC,
			},
		},
		{
			caseID: "place_order.invalid.limit_missing_price_or_policy",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "1",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeLimit,
				Quantity:      decimal.NewFromInt(1),
			},
		},
		{
			caseID: "place_order.invalid.limit_missing_price_or_policy",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "1",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeLimit,
				Quantity:      decimal.NewFromInt(1),
				LimitPrice:    decimal.NewFromInt(1),
			},
		},
		{
			caseID: "place_order.invalid.non_positive_quantity",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "1",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeMarket,
			},
		},
		{
			caseID: "place_order.invalid.non_positive_quantity",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "1",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeMarket,
				Quantity:      decimal.NewFromInt(-1),
			},
		},
		{
			caseID: "place_order.invalid.missing_client_order_id",
			request: exchange.PlaceOrderRequest{
				Instrument: instrument,
				Side:       exchange.SideBuy,
				Type:       exchange.OrderTypeMarket,
				Quantity:   decimal.NewFromInt(1),
			},
		},
		{
			caseID: "place_order.invalid.bad_client_order_id",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "not-portable",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeMarket,
				Quantity:      decimal.NewFromInt(1),
			},
		},
		{
			caseID: "place_order.invalid.bad_client_order_id",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "01",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeMarket,
				Quantity:      decimal.NewFromInt(1),
			},
		},
		{
			caseID: "place_order.invalid.bad_client_order_id",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "281474976710656",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeMarket,
				Quantity:      decimal.NewFromInt(1),
			},
		},
	}
	if row.product == exchange.ProductSpot {
		invalid = append(invalid, struct {
			caseID  string
			request exchange.PlaceOrderRequest
		}{
			caseID: "place_order.invalid.spot_reduce_only",
			request: exchange.PlaceOrderRequest{
				Instrument:    instrument,
				ClientOrderID: "1",
				Side:          exchange.SideSell,
				Type:          exchange.OrderTypeMarket,
				Quantity:      decimal.NewFromInt(1),
				ReduceOnly:    true,
			},
		})
	}
	for index, invalidCase := range invalid {
		if err := invalidCase.request.Validate(row.product); !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("%s invalid PlaceOrder request %d validation error = %v", row.code, index, err)
		}
		if _, err := transport.PlaceOrder(ctx, invalidCase.request); !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("%s invalid %s PlaceOrder request %d error = %v", row.code, transportName, index, err)
		}
		coverage.MarkParameterCase(invalidCase.caseID)
	}

	cancelCases := []struct {
		caseID  string
		orderID string
	}{
		{caseID: "cancel_order." + transportName + ".invalid_missing_order_id", orderID: ""},
		{caseID: "cancel_order." + transportName + ".invalid_nonportable_order_id", orderID: "01"},
		{caseID: "cancel_order." + transportName + ".invalid_nonportable_order_id", orderID: "not-portable"},
	}
	for _, cancelCase := range cancelCases {
		if _, err := transport.CancelOrder(ctx, exchange.CancelOrderRequest{
			Instrument: instrument,
			OrderID:    cancelCase.orderID,
		}); !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("%s invalid %s CancelOrder %q error = %v", row.code, transportName, cancelCase.orderID, err)
		}
		coverage.MarkParameterCase(cancelCase.caseID)
	}
}

func exerciseSpotWebSocket(
	t *testing.T,
	ctx context.Context,
	row exchangeAcceptanceRow,
	socket exchange.SpotWebSocket,
	instrument string,
	coverage *externalRowCoverage,
) []*acceptanceSubscriptionWitness {
	t.Helper()
	if socket == nil {
		t.Fatalf("%s WebSocket returned nil", row.code)
	}
	watch := exchange.WatchRequest{Instrument: instrument, Options: exchange.WatchOptions{Buffer: 32}}
	requireAcceptanceSubscription(t, ctx, row.code+"/WatchOrderBook", true, func(startCtx context.Context) (exchange.Subscription[exchange.BookEvent], error) {
		return socket.WatchOrderBook(startCtx, watch)
	})
	coverage.MarkOperation("websocket", "WatchOrderBook")
	requireAcceptanceSubscription(t, ctx, row.code+"/WatchBBO", true, func(startCtx context.Context) (exchange.Subscription[exchange.BBOEvent], error) {
		return socket.WatchBBO(startCtx, watch)
	})
	coverage.MarkOperation("websocket", "WatchBBO")
	witnesses := make([]*acceptanceSubscriptionWitness, 0, 5)
	witnesses = append(witnesses, requireActiveAcceptanceWitness(t, ctx, row.code+"/WatchPublicTrades", func(startCtx context.Context) (exchange.Subscription[exchange.PublicTradeEvent], error) {
		return socket.WatchPublicTrades(startCtx, watch)
	}))
	coverage.MarkOperation("websocket", "WatchPublicTrades")
	witnesses = append(witnesses, requireActiveAcceptanceWitness(t, ctx, row.code+"/WatchCandles", func(startCtx context.Context) (exchange.Subscription[exchange.CandleEvent], error) {
		return socket.WatchCandles(startCtx, exchange.WatchCandlesRequest{
			Instrument: instrument,
			Interval:   "1m",
			Options:    exchange.WatchOptions{Buffer: 32},
		})
	}))
	coverage.MarkOperation("websocket", "WatchCandles")
	witnesses = append(witnesses, requireActiveAcceptanceWitness(t, ctx, row.code+"/WatchOrders", func(startCtx context.Context) (exchange.Subscription[exchange.OrderEvent], error) {
		return socket.WatchOrders(startCtx, watch)
	}))
	coverage.MarkOperation("websocket", "WatchOrders")
	witnesses = append(witnesses, requireActiveAcceptanceWitness(t, ctx, row.code+"/WatchFills", func(startCtx context.Context) (exchange.Subscription[exchange.FillEvent], error) {
		return socket.WatchFills(startCtx, watch)
	}))
	coverage.MarkOperation("websocket", "WatchFills")
	witnesses = append(witnesses, requireActiveAcceptanceWitness(t, ctx, row.code+"/WatchBalances", func(startCtx context.Context) (exchange.Subscription[exchange.BalanceEvent], error) {
		return socket.WatchBalances(startCtx, exchange.WatchAccountRequest{Options: exchange.WatchOptions{Buffer: 32}})
	}))
	coverage.MarkOperation("websocket", "WatchBalances")
	return witnesses
}

func exercisePerpWebSocket(
	t *testing.T,
	ctx context.Context,
	row exchangeAcceptanceRow,
	socket exchange.PerpWebSocket,
	instrument string,
	coverage *externalRowCoverage,
) []*acceptanceSubscriptionWitness {
	t.Helper()
	if socket == nil {
		t.Fatalf("%s WebSocket returned nil", row.code)
	}
	watch := exchange.WatchRequest{Instrument: instrument, Options: exchange.WatchOptions{Buffer: 32}}
	publicReference := exchangeAcceptancePerpPublicWebSocket(socket)
	if row.perpPublicWS != nil {
		publicReference = row.perpPublicWS
	}
	positionWitness := requireActiveAcceptanceWitness(t, ctx, row.code+"/WatchPositions", func(startCtx context.Context) (exchange.Subscription[exchange.PositionEvent], error) {
		return socket.WatchPositions(startCtx, watch)
	})
	coverage.MarkOperation("websocket", "WatchPositions")
	if row.watchMarkPriceUnsupported {
		subscription, err := publicReference.WatchMarkPrice(ctx, watch)
		if !errors.Is(err, exchange.ErrUnsupported) {
			t.Fatalf("%s/WatchMarkPrice error = %v, want ErrUnsupported", row.code, err)
		}
		if subscription != nil {
			_ = subscription.Close()
			t.Fatalf("%s/WatchMarkPrice returned a subscription for an unsupported stream", row.code)
		}
	} else {
		requireAcceptanceSubscription(t, ctx, row.code+"/WatchMarkPrice", true, func(startCtx context.Context) (exchange.Subscription[exchange.MarkPriceEvent], error) {
			return publicReference.WatchMarkPrice(startCtx, watch)
		})
	}
	coverage.MarkOperation("websocket", "WatchMarkPrice")
	requireAcceptanceSubscription(t, ctx, row.code+"/WatchFundingRate", true, func(startCtx context.Context) (exchange.Subscription[exchange.FundingRateEvent], error) {
		return publicReference.WatchFundingRate(startCtx, watch)
	})
	coverage.MarkOperation("websocket", "WatchFundingRate")
	return append(
		exerciseSpotWebSocket(t, ctx, row, socket, instrument, coverage),
		positionWitness,
	)
}

func exerciseSpotOrderCases(
	t *testing.T,
	ctx context.Context,
	row exchangeAcceptanceRow,
	rest exchangeAcceptanceREST,
	socket exchange.SpotWebSocket,
	instrument exchange.Instrument,
	book exchange.OrderBook,
	baseline []exchange.Balance,
	journal *ownedOrderJournal,
	coverage *externalRowCoverage,
) {
	t.Helper()
	transports := []acceptanceOrderTransport{
		{caseName: "rest", coverageName: "rest", orderTransport: rest},
		{caseName: "ws", coverageName: "websocket", orderTransport: socket},
	}
	for _, transport := range transports {
		for _, orderCase := range acceptanceOrderCases(exchange.ProductSpot, transport.caseName) {
			switch orderCase.Kind {
			case "market", "limit_ioc":
				exerciseSpotFillRoundTrip(t, ctx, row, rest, transport, instrument, book, baseline, orderCase, journal, coverage)
			case "limit_resting", "limit_post_only", "client_order_id":
				exerciseRestingOrderAndCancel(t, ctx, row, rest, transport, instrument, book, orderCase, journal, coverage)
			default:
				t.Fatalf("%s unknown spot acceptance order case %s", row.code, orderCase.Kind)
			}
			coverage.MarkParameterCase(orderCase.ID)
		}
	}
}

func exercisePerpOrderCases(
	t *testing.T,
	ctx context.Context,
	row exchangeAcceptanceRow,
	rest exchange.PerpClient,
	socket exchange.PerpWebSocket,
	instrument exchange.Instrument,
	book exchange.OrderBook,
	baseline decimal.Decimal,
	journal *ownedOrderJournal,
	coverage *externalRowCoverage,
) {
	t.Helper()
	transports := []acceptanceOrderTransport{
		{caseName: "rest", coverageName: "rest", orderTransport: rest},
		{caseName: "ws", coverageName: "websocket", orderTransport: socket},
	}
	for _, transport := range transports {
		for _, orderCase := range acceptanceOrderCases(exchange.ProductPerp, transport.caseName) {
			switch orderCase.Kind {
			case "market", "limit_ioc", "perp_reduce_only":
				exercisePerpFillRoundTrip(t, ctx, row, rest, transport, instrument, book, baseline, orderCase, journal, coverage)
			case "limit_resting", "limit_post_only", "client_order_id":
				exerciseRestingOrderAndCancel(t, ctx, row, rest, transport, instrument, book, orderCase, journal, coverage)
			default:
				t.Fatalf("%s unknown perp acceptance order case %s", row.code, orderCase.Kind)
			}
			coverage.MarkParameterCase(orderCase.ID)
		}
	}
}

func exerciseSpotFillRoundTrip(
	t *testing.T,
	ctx context.Context,
	row exchangeAcceptanceRow,
	rest exchangeAcceptanceREST,
	transport acceptanceOrderTransport,
	instrument exchange.Instrument,
	book exchange.OrderBook,
	baseline []exchange.Balance,
	orderCase acceptanceOrderCase,
	journal *ownedOrderJournal,
	coverage *externalRowCoverage,
) {
	t.Helper()
	before := acceptanceBalanceTotal(baseline, instrument.BaseAsset)
	if current, err := rest.Balances(ctx); err != nil {
		t.Fatalf("%s %s %s pre-order Balances: %v", row.code, transport.caseName, orderCase.Kind, err)
	} else {
		before = acceptanceBalanceTotal(current, instrument.BaseAsset)
	}

	request := acceptanceFillRequest(t, row, instrument, book, exchange.SideBuy, orderCase.Kind, false)
	ack, err := placeTrackedAcceptanceOrder(ctx, transport.orderTransport, request, journal)
	ack = requireAcceptanceOrderAck(t, row.code+"/"+transport.caseName+"/"+orderCase.Kind, ack, err)
	coverage.MarkOperation(transport.coverageName, "PlaceOrder")

	acquired := waitAcceptanceBalanceIncrease(t, ctx, row.code, rest, instrument.BaseAsset, before, instrument.QuantityIncrement)
	if ack.State == exchange.AckAmbiguous || ack.State == exchange.AckRejected {
		t.Fatalf("%s %s returned unsafe acknowledgement state %s", row.code, orderCase.Kind, ack.State)
	}
	sellQuantity := roundDownToStep(acquired, instrument.QuantityIncrement)
	if !sellQuantity.IsPositive() {
		t.Fatalf("%s %s acquired quantity %s is not tradable", row.code, orderCase.Kind, acquired)
	}
	offset := exchange.PlaceOrderRequest{
		Instrument:    instrument.Symbol,
		ClientOrderID: nextAcceptanceClientOrderID(),
		Side:          exchange.SideSell,
		Type:          exchange.OrderTypeMarket,
		Quantity:      sellQuantity,
	}
	offsetAck, err := placeTrackedAcceptanceOrder(ctx, transport.orderTransport, offset, journal)
	requireAcceptanceOrderAck(t, row.code+"/"+transport.caseName+"/"+orderCase.Kind+"/offset", offsetAck, err)
	waitAcceptanceBalanceBaseline(t, ctx, row.code, rest, instrument.BaseAsset, before, instrument.QuantityIncrement)
}

func exercisePerpFillRoundTrip(
	t *testing.T,
	ctx context.Context,
	row exchangeAcceptanceRow,
	rest exchange.PerpClient,
	transport acceptanceOrderTransport,
	instrument exchange.Instrument,
	book exchange.OrderBook,
	baseline decimal.Decimal,
	orderCase acceptanceOrderCase,
	journal *ownedOrderJournal,
	coverage *externalRowCoverage,
) {
	t.Helper()
	tolerance := instrument.QuantityIncrement.Div(decimal.NewFromInt(2))
	if current := currentAcceptancePosition(t, ctx, row.code, rest, instrument.Symbol); current.Sub(baseline).Abs().GreaterThan(tolerance) {
		t.Fatalf("%s %s pre-order position %s differs from baseline %s", row.code, orderCase.Kind, current, baseline)
	}
	primarySide, offsetSide, direction := perpRoundTripDirection(baseline)

	if orderCase.Kind == "perp_reduce_only" {
		open := acceptanceFillRequest(t, row, instrument, book, primarySide, "market", false)
		openAck, err := placeTrackedAcceptanceOrder(ctx, transport.orderTransport, open, journal)
		requireAcceptanceOrderAck(t, row.code+"/"+transport.caseName+"/reduce-only-setup", openAck, err)
		waitAcceptancePositionMove(
			t,
			ctx,
			row.code,
			rest,
			instrument.Symbol,
			baseline,
			direction,
			instrument.QuantityIncrement,
		)
	}

	request := acceptanceFillRequest(
		t,
		row,
		instrument,
		book,
		primarySide,
		orderCase.Kind,
		orderCase.Kind == "perp_reduce_only",
	)
	if orderCase.Kind == "perp_reduce_only" {
		position := currentAcceptancePosition(t, ctx, row.code, rest, instrument.Symbol)
		request.Side = offsetSide
		request.Type = exchange.OrderTypeMarket
		request.LimitPrice = decimal.Zero
		request.LimitPolicy = ""
		request.Quantity = roundDownToStep(position.Sub(baseline).Abs(), instrument.QuantityIncrement)
		request.ReduceOnly = true
	}
	ack, err := placeTrackedAcceptanceOrder(ctx, transport.orderTransport, request, journal)
	requireAcceptanceOrderAck(t, row.code+"/"+transport.caseName+"/"+orderCase.Kind, ack, err)
	coverage.MarkOperation(transport.coverageName, "PlaceOrder")

	if orderCase.Kind == "perp_reduce_only" {
		waitAcceptancePositionBaseline(t, ctx, row.code, rest, instrument.Symbol, baseline, instrument.QuantityIncrement)
		return
	}

	moved := waitAcceptancePositionMove(
		t,
		ctx,
		row.code,
		rest,
		instrument.Symbol,
		baseline,
		direction,
		instrument.QuantityIncrement,
	)
	offsetQuantity := roundDownToStep(moved.Sub(baseline).Abs(), instrument.QuantityIncrement)
	if !offsetQuantity.IsPositive() {
		t.Fatalf("%s %s filled position delta %s is not tradable", row.code, orderCase.Kind, moved.Sub(baseline))
	}
	offset := exchange.PlaceOrderRequest{
		Instrument:    instrument.Symbol,
		ClientOrderID: nextAcceptanceClientOrderID(),
		Side:          offsetSide,
		Type:          exchange.OrderTypeMarket,
		Quantity:      offsetQuantity,
		ReduceOnly:    true,
	}
	offsetAck, err := placeTrackedAcceptanceOrder(ctx, transport.orderTransport, offset, journal)
	requireAcceptanceOrderAck(t, row.code+"/"+transport.caseName+"/"+orderCase.Kind+"/offset", offsetAck, err)
	waitAcceptancePositionBaseline(t, ctx, row.code, rest, instrument.Symbol, baseline, instrument.QuantityIncrement)
}

func exerciseRestingOrderAndCancel(
	t *testing.T,
	ctx context.Context,
	row exchangeAcceptanceRow,
	rest exchangeAcceptanceREST,
	transport acceptanceOrderTransport,
	instrument exchange.Instrument,
	book exchange.OrderBook,
	orderCase acceptanceOrderCase,
	journal *ownedOrderJournal,
	coverage *externalRowCoverage,
) {
	t.Helper()
	price := acceptanceRestingBuyPrice(instrument, book)
	size, err := sizeAcceptanceQuoteOrderAtPrice(instrument, price, row.notional, row.maxNotional)
	if err != nil {
		t.Fatalf("%s %s size: %v", row.code, orderCase.Kind, err)
	}
	policy := exchange.LimitPolicyResting
	if orderCase.Kind == "limit_post_only" {
		policy = exchange.LimitPolicyPostOnly
	}
	clientOrderID := nextAcceptanceClientOrderID()
	request := exchange.PlaceOrderRequest{
		Instrument:    instrument.Symbol,
		ClientOrderID: clientOrderID,
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      size.Quantity,
		LimitPrice:    price,
		LimitPolicy:   policy,
	}
	ack, err := placeTrackedAcceptanceOrder(ctx, transport.orderTransport, request, journal)
	ack = requireAcceptanceOrderAck(t, row.code+"/"+transport.caseName+"/"+orderCase.Kind, ack, err)
	coverage.MarkOperation(transport.coverageName, "PlaceOrder")
	if ack.State == exchange.AckImmediatelyFilled || ack.State == exchange.AckPartiallyFilled {
		t.Fatalf("%s %s unexpectedly filled at non-crossing price %s", row.code, orderCase.Kind, price)
	}
	if orderCase.Kind == "client_order_id" && ack.ClientOrderID != clientOrderID {
		t.Fatalf("%s %s client order id = %q, want %q", row.code, orderCase.Kind, ack.ClientOrderID, clientOrderID)
	}
	orderID := resolveAcceptanceOrderID(t, ctx, row.code, rest, instrument.Symbol, clientOrderID, ack.OrderID)
	journal.TrackOrderID(orderID)

	cancelAck, err := transport.orderTransport.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: instrument.Symbol, OrderID: orderID})
	cancelAck = requireAcceptanceOrderAck(t, row.code+"/"+transport.caseName+"/"+orderCase.Kind+"/cancel", cancelAck, err)
	if cancelAck.Operation != exchange.OrderOperationCancel {
		t.Fatalf("%s cancel acknowledgement operation = %s", row.code, cancelAck.Operation)
	}
	coverage.MarkOperation(transport.coverageName, "CancelOrder")
	coverage.MarkParameterCase("cancel_order." + transport.caseName + ".order_id")
	waitAcceptanceOrderClosed(t, ctx, row.code, rest, instrument.Symbol, orderID)
	journal.MarkTerminal(orderID)
}

func placeTrackedAcceptanceOrder(
	ctx context.Context,
	transport exchangeAcceptanceOrderTransport,
	request exchange.PlaceOrderRequest,
	journal *ownedOrderJournal,
) (exchange.OrderAcknowledgement, error) {
	journal.TrackClientOrderID(request.ClientOrderID)
	ack, err := transport.PlaceOrder(ctx, request)
	if err == nil {
		journal.TrackPlacement(ack)
	}
	return ack, err
}

func acceptanceFillRequest(
	t *testing.T,
	row exchangeAcceptanceRow,
	instrument exchange.Instrument,
	book exchange.OrderBook,
	side exchange.Side,
	kind string,
	reduceOnly bool,
) exchange.PlaceOrderRequest {
	t.Helper()
	size, err := sizeAcceptanceQuoteOrder(instrument, book, side, row.notional, row.maxNotional)
	if err != nil {
		t.Fatalf("%s %s size: %v", row.code, kind, err)
	}
	request := exchange.PlaceOrderRequest{
		Instrument:    instrument.Symbol,
		ClientOrderID: nextAcceptanceClientOrderID(),
		Side:          side,
		Quantity:      size.Quantity,
		ReduceOnly:    reduceOnly,
	}
	switch kind {
	case "market", "perp_reduce_only":
		request.Type = exchange.OrderTypeMarket
	case "limit_ioc":
		request.Type = exchange.OrderTypeLimit
		request.LimitPrice = acceptanceIOCPrice(instrument, book, side)
		request.LimitPolicy = exchange.LimitPolicyIOC
	default:
		t.Fatalf("%s unknown fill-dependent order kind %s", row.code, kind)
	}
	if err := request.Validate(row.product); err != nil && kind != "perp_reduce_only" {
		t.Fatalf("%s built invalid %s request: %v", row.code, kind, err)
	}
	return request
}

func requireAcceptanceOrderAck(
	t *testing.T,
	operation string,
	ack exchange.OrderAcknowledgement,
	err error,
) exchange.OrderAcknowledgement {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	if err := ack.Validate(); err != nil {
		t.Fatalf("%s malformed acknowledgement: %v (%+v)", operation, err, ack)
	}
	if ack.State == exchange.AckRejected || ack.State == exchange.AckAmbiguous {
		t.Fatalf("%s unsafe acknowledgement state %s: %s %s", operation, ack.State, ack.VenueCode, ack.VenueMessage)
	}
	return ack
}

func nextAcceptanceClientOrderID() string {
	const maxPortableClientOrderID = uint64(1<<48 - 1)
	sequence := uint64(exchangeAcceptanceClientID.Add(1))
	value := (exchangeAcceptanceClientIDSeed + sequence) & maxPortableClientOrderID
	if value == 0 {
		value = 1
	}
	return fmt.Sprintf("%d", value)
}

func acceptanceRestingBuyPrice(instrument exchange.Instrument, book exchange.OrderBook) decimal.Decimal {
	price := book.Bids[0].Price.Mul(decimal.RequireFromString("0.98"))
	return roundDownToStep(price, instrument.PriceIncrement)
}

func acceptanceIOCPrice(instrument exchange.Instrument, book exchange.OrderBook, side exchange.Side) decimal.Decimal {
	if side == exchange.SideBuy {
		return roundUpToStep(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), instrument.PriceIncrement)
	}
	return roundDownToStep(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), instrument.PriceIncrement)
}

func resolveAcceptanceOrderID(
	t *testing.T,
	ctx context.Context,
	rowCode string,
	rest exchange.OrderREST,
	instrument string,
	clientOrderID string,
	ackOrderID string,
) string {
	t.Helper()
	if isPortableNativeOrderID(ackOrderID) {
		return ackOrderID
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		page, err := rest.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: instrument, Limit: 100})
		if err == nil {
			for _, order := range page.Orders {
				if order.ClientOrderID == clientOrderID && isPortableNativeOrderID(order.OrderID) {
					return order.OrderID
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s could not resolve exact native order id for client order %s: %v", rowCode, clientOrderID, err)
		}
		waitAcceptancePoll(t, ctx, rowCode+"/resolve-order")
	}
}

func waitAcceptanceOrderClosed(
	t *testing.T,
	ctx context.Context,
	rowCode string,
	rest exchange.OrderREST,
	instrument string,
	orderID string,
) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		page, err := rest.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: instrument, Limit: 100})
		if err == nil && !orderPageContainsOrderID(page, orderID) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s owned order %s did not close: %v", rowCode, orderID, err)
		}
		waitAcceptancePoll(t, ctx, rowCode+"/wait-order-close")
	}
}

func perpRoundTripDirection(baseline decimal.Decimal) (exchange.Side, exchange.Side, decimal.Decimal) {
	if baseline.IsNegative() {
		return exchange.SideSell, exchange.SideBuy, decimal.NewFromInt(-1)
	}
	return exchange.SideBuy, exchange.SideSell, decimal.NewFromInt(1)
}

func positionMovedInDirection(
	current decimal.Decimal,
	baseline decimal.Decimal,
	direction decimal.Decimal,
	step decimal.Decimal,
) bool {
	return current.Sub(baseline).Mul(direction).GreaterThan(step.Div(decimal.NewFromInt(2)))
}

func waitAcceptanceBalanceIncrease(
	t *testing.T,
	ctx context.Context,
	rowCode string,
	rest interface {
		Balances(context.Context) ([]exchange.Balance, error)
	},
	asset string,
	baseline decimal.Decimal,
	step decimal.Decimal,
) decimal.Decimal {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		balances, err := rest.Balances(ctx)
		if err == nil {
			delta := acceptanceBalanceTotal(balances, asset).Sub(baseline)
			if delta.GreaterThan(step.Div(decimal.NewFromInt(2))) {
				return delta
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not observe positive %s balance delta after fill: %v", rowCode, asset, err)
		}
		waitAcceptancePoll(t, ctx, rowCode+"/wait-balance-increase")
	}
}

func waitAcceptanceBalanceBaseline(
	t *testing.T,
	ctx context.Context,
	rowCode string,
	rest interface {
		Balances(context.Context) ([]exchange.Balance, error)
	},
	asset string,
	baseline decimal.Decimal,
	step decimal.Decimal,
) {
	t.Helper()
	tolerance := step.Mul(decimal.NewFromInt(2))
	deadline := time.Now().Add(45 * time.Second)
	for {
		balances, err := rest.Balances(ctx)
		if err == nil {
			delta := acceptanceBalanceTotal(balances, asset).Sub(baseline).Abs()
			if delta.LessThanOrEqual(tolerance) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s %s balance did not return to baseline tolerance %s: %v", rowCode, asset, tolerance, err)
		}
		waitAcceptancePoll(t, ctx, rowCode+"/wait-balance-baseline")
	}
}

func acceptanceBalanceTotal(balances []exchange.Balance, asset string) decimal.Decimal {
	for _, balance := range balances {
		if strings.EqualFold(balance.Asset, asset) {
			return balance.Total
		}
	}
	return decimal.Zero
}

func currentAcceptancePosition(
	t *testing.T,
	ctx context.Context,
	rowCode string,
	rest interface {
		Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error)
	},
	instrument string,
) decimal.Decimal {
	t.Helper()
	positions, err := rest.Positions(ctx, exchange.PositionsRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("%s Positions: %v", rowCode, err)
	}
	return signedAcceptancePosition(positions, instrument)
}

func signedAcceptancePosition(positions []exchange.Position, instrument string) decimal.Decimal {
	total := decimal.Zero
	for _, position := range positions {
		if normalizeAcceptanceSymbol(position.Instrument) != normalizeAcceptanceSymbol(instrument) {
			continue
		}
		switch position.Side {
		case exchange.SideBuy:
			total = total.Add(position.Quantity)
		case exchange.SideSell:
			total = total.Sub(position.Quantity)
		}
	}
	return total
}

func waitAcceptancePositionMove(
	t *testing.T,
	ctx context.Context,
	rowCode string,
	rest interface {
		Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error)
	},
	instrument string,
	baseline decimal.Decimal,
	direction decimal.Decimal,
	step decimal.Decimal,
) decimal.Decimal {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		current := currentAcceptancePosition(t, ctx, rowCode, rest, instrument)
		if positionMovedInDirection(current, baseline, direction, step) {
			return current
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s position did not move from baseline %s in direction %s", rowCode, baseline, direction)
		}
		waitAcceptancePoll(t, ctx, rowCode+"/wait-position-move")
	}
}

func waitAcceptancePositionBaseline(
	t *testing.T,
	ctx context.Context,
	rowCode string,
	rest interface {
		Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error)
	},
	instrument string,
	baseline decimal.Decimal,
	step decimal.Decimal,
) {
	t.Helper()
	tolerance := step.Div(decimal.NewFromInt(2))
	deadline := time.Now().Add(45 * time.Second)
	for {
		current := currentAcceptancePosition(t, ctx, rowCode, rest, instrument)
		if current.Sub(baseline).Abs().LessThanOrEqual(tolerance) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s position %s did not return to baseline %s", rowCode, current, baseline)
		}
		waitAcceptancePoll(t, ctx, rowCode+"/wait-position-baseline")
	}
}

func waitAcceptancePoll(t *testing.T, ctx context.Context, operation string) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("%s: %v", operation, ctx.Err())
	case <-time.After(500 * time.Millisecond):
	}
}

func cleanupSpotAcceptanceExposure(
	ctx context.Context,
	row exchangeAcceptanceRow,
	rest spotExposureCleanupClient,
	instrument exchange.Instrument,
	baseline []exchange.Balance,
	journal *ownedOrderJournal,
) error {
	balances, err := rest.Balances(ctx)
	if err != nil {
		return fmt.Errorf("%s cleanup Balances: %w", row.code, err)
	}
	delta := acceptanceBalanceTotal(balances, instrument.BaseAsset).Sub(acceptanceBalanceTotal(baseline, instrument.BaseAsset))
	if spotBalanceWithinFeeTolerance(delta, instrument.QuantityIncrement, decimal.Zero) {
		return nil
	}
	book, err := rest.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument.Symbol, Limit: 1})
	if err != nil {
		return fmt.Errorf("%s cleanup OrderBook: %w", row.code, err)
	}
	side := exchange.SideSell
	price, err := executablePrice(book, side)
	if delta.IsNegative() {
		side = exchange.SideBuy
		price, err = executablePrice(book, side)
	}
	if err != nil {
		return fmt.Errorf("%s cleanup executable price: %w", row.code, err)
	}
	if spotBalanceWithinFeeTolerance(delta, instrument.QuantityIncrement, price) {
		return nil
	}
	quantity := roundDownToStep(delta.Abs(), instrument.QuantityIncrement)
	if !quantity.IsPositive() {
		return fmt.Errorf("%s cleanup delta %s cannot be represented by quantity step %s", row.code, delta, instrument.QuantityIncrement)
	}
	if instrument.MinNotional.Valid && quantity.Mul(price).LessThan(instrument.MinNotional.Value) {
		return nil
	}
	request := exchange.PlaceOrderRequest{
		Instrument:    instrument.Symbol,
		ClientOrderID: nextAcceptanceClientOrderID(),
		Side:          side,
		Type:          exchange.OrderTypeMarket,
		Quantity:      quantity,
	}
	journal.TrackClientOrderID(request.ClientOrderID)
	ack, err := rest.PlaceOrder(ctx, request)
	if err != nil {
		return fmt.Errorf("%s cleanup spot delta %s %s: %w", row.code, quantity, instrument.BaseAsset, err)
	}
	journal.TrackPlacement(ack)
	if err := validateAcceptanceCleanupAck(ack); err != nil {
		return fmt.Errorf("%s cleanup spot acknowledgement: %w", row.code, err)
	}
	return waitSpotAcceptanceBaseline(ctx, row.code, rest, instrument, baseline, price)
}

func cleanupPerpAcceptanceExposure(
	ctx context.Context,
	row exchangeAcceptanceRow,
	rest perpExposureCleanupClient,
	instrument exchange.Instrument,
	baseline decimal.Decimal,
	journal *ownedOrderJournal,
) error {
	positions, err := rest.Positions(ctx, exchange.PositionsRequest{Instrument: instrument.Symbol})
	if err != nil {
		return fmt.Errorf("%s cleanup Positions: %w", row.code, err)
	}
	current := signedAcceptancePosition(positions, instrument.Symbol)
	delta := current.Sub(baseline)
	if delta.Abs().LessThanOrEqual(instrument.QuantityIncrement.Div(decimal.NewFromInt(2))) {
		return nil
	}
	quantity := roundDownToStep(delta.Abs(), instrument.QuantityIncrement)
	if !quantity.IsPositive() {
		return fmt.Errorf("%s cleanup position delta %s cannot be represented by quantity step %s", row.code, delta, instrument.QuantityIncrement)
	}
	side := exchange.SideSell
	if delta.IsNegative() {
		side = exchange.SideBuy
	}
	reduceOnly := baseline.IsZero() ||
		current.Sign() == baseline.Sign() && current.Abs().GreaterThan(baseline.Abs())
	request := exchange.PlaceOrderRequest{
		Instrument:    instrument.Symbol,
		ClientOrderID: nextAcceptanceClientOrderID(),
		Side:          side,
		Type:          exchange.OrderTypeMarket,
		Quantity:      quantity,
		ReduceOnly:    reduceOnly,
	}
	journal.TrackClientOrderID(request.ClientOrderID)
	ack, err := rest.PlaceOrder(ctx, request)
	if err != nil {
		return fmt.Errorf("%s cleanup perp delta %s: %w", row.code, delta, err)
	}
	journal.TrackPlacement(ack)
	if err := validateAcceptanceCleanupAck(ack); err != nil {
		return fmt.Errorf("%s cleanup perp acknowledgement: %w", row.code, err)
	}
	return waitPerpAcceptanceBaseline(ctx, row.code, rest, instrument, baseline)
}

func validateAcceptanceCleanupAck(ack exchange.OrderAcknowledgement) error {
	if err := ack.Validate(); err != nil {
		return err
	}
	if ack.State == exchange.AckRejected || ack.State == exchange.AckAmbiguous {
		return fmt.Errorf("unsafe state %s: %s %s", ack.State, ack.VenueCode, ack.VenueMessage)
	}
	return nil
}

func spotBalanceWithinFeeTolerance(delta, step, price decimal.Decimal) bool {
	delta = delta.Abs()
	if delta.LessThanOrEqual(step.Mul(decimal.NewFromInt(2))) {
		return true
	}
	return price.IsPositive() && delta.Mul(price).LessThanOrEqual(decimal.RequireFromString("0.50"))
}

func waitSpotAcceptanceBaseline(
	ctx context.Context,
	rowCode string,
	rest interface {
		Balances(context.Context) ([]exchange.Balance, error)
	},
	instrument exchange.Instrument,
	baseline []exchange.Balance,
	price decimal.Decimal,
) error {
	for {
		balances, err := rest.Balances(ctx)
		if err == nil {
			delta := acceptanceBalanceTotal(balances, instrument.BaseAsset).
				Sub(acceptanceBalanceTotal(baseline, instrument.BaseAsset))
			if spotBalanceWithinFeeTolerance(delta, instrument.QuantityIncrement, price) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s %s balance did not return to baseline: %w", rowCode, instrument.BaseAsset, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func waitPerpAcceptanceBaseline(
	ctx context.Context,
	rowCode string,
	rest interface {
		Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error)
	},
	instrument exchange.Instrument,
	baseline decimal.Decimal,
) error {
	tolerance := instrument.QuantityIncrement.Div(decimal.NewFromInt(2))
	for {
		positions, err := rest.Positions(ctx, exchange.PositionsRequest{Instrument: instrument.Symbol})
		if err == nil {
			current := signedAcceptancePosition(positions, instrument.Symbol)
			if current.Sub(baseline).Abs().LessThanOrEqual(tolerance) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s position did not return to baseline %s: %w", rowCode, baseline, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func finalizeSpotAcceptance(
	row exchangeAcceptanceRow,
	rest spotAcceptanceCleanupClient,
	instrument exchange.Instrument,
	baseline []exchange.Balance,
	journal *ownedOrderJournal,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var cleanupErrors []error
	if err := journal.Cleanup(ctx, rest, instrument.Symbol); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("cancel exact-owned orders: %w", err))
	}
	if err := cleanupSpotAcceptanceExposure(ctx, row, rest, instrument, baseline, journal); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := journal.Cleanup(ctx, rest, instrument.Symbol); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("verify exact-owned orders: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

func finalizePerpAcceptance(
	row exchangeAcceptanceRow,
	rest perpAcceptanceCleanupClient,
	instrument exchange.Instrument,
	baseline decimal.Decimal,
	journal *ownedOrderJournal,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var cleanupErrors []error
	if err := journal.Cleanup(ctx, rest, instrument.Symbol); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("cancel exact-owned orders: %w", err))
	}
	if err := cleanupPerpAcceptanceExposure(ctx, row, rest, instrument, baseline, journal); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := journal.Cleanup(ctx, rest, instrument.Symbol); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("verify exact-owned orders: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

func closeAcceptanceSocket(
	t *testing.T,
	rowCode string,
	socket interface{ Close() error },
	coverage *externalRowCoverage,
) {
	t.Helper()
	if err := socket.Close(); err != nil {
		t.Fatalf("%s WebSocket Close: %v", rowCode, err)
	}
	coverage.MarkOperation("websocket", "Close")
	if err := socket.Close(); err != nil {
		t.Fatalf("%s WebSocket idempotent Close: %v", rowCode, err)
	}
}

func requireAcceptanceSubscription[T any](
	t *testing.T,
	parent context.Context,
	operation string,
	requireEvent bool,
	start func(context.Context) (exchange.Subscription[T], error),
) {
	t.Helper()
	timeout := acceptanceSubscriptionTimeout(operation)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	witness, err := startAcceptanceSubscriptionWitness(parent, operation, start)
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	if requireEvent {
		if err := witness.WaitEvent(ctx); err != nil {
			t.Fatalf("%s did not produce required active/event evidence: %v", operation, err)
		}
	}
	if err := witness.Close(); err != nil {
		t.Fatalf("%s close: %v", operation, err)
	}
	if err := witness.Close(); err != nil {
		t.Fatalf("%s idempotent close: %v", operation, err)
	}
}

func acceptanceSubscriptionTimeout(operation string) time.Duration {
	if strings.Contains(operation, "WatchCandles") {
		return 75 * time.Second
	}
	if strings.HasPrefix(operation, "BG") && strings.Contains(operation, "WatchOrderBook") {
		return 120 * time.Second
	}
	return 45 * time.Second
}

func requireActiveAcceptanceWitness[T any](
	t *testing.T,
	parent context.Context,
	operation string,
	start func(context.Context) (exchange.Subscription[T], error),
) *acceptanceSubscriptionWitness {
	t.Helper()
	witness, err := startAcceptanceSubscriptionWitness(parent, operation, start)
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	return witness
}

func armAcceptanceWitnesses(witnesses []*acceptanceSubscriptionWitness) {
	for _, witness := range witnesses {
		witness.Arm()
	}
}

func requireAcceptanceWitnessEvents(
	t *testing.T,
	parent context.Context,
	witnesses []*acceptanceSubscriptionWitness,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 75*time.Second)
	defer cancel()
	for _, witness := range witnesses {
		if err := witness.WaitEvent(ctx); err != nil {
			t.Fatalf("%s did not produce a post-arm event: %v", witness.operation, err)
		}
		if err := witness.Close(); err != nil {
			t.Fatalf("%s close: %v", witness.operation, err)
		}
		if err := witness.Close(); err != nil {
			t.Fatalf("%s idempotent close: %v", witness.operation, err)
		}
	}
}

func startAcceptanceSubscriptionWitness[T any](
	parent context.Context,
	operation string,
	start func(context.Context) (exchange.Subscription[T], error),
) (*acceptanceSubscriptionWitness, error) {
	subscription, err := start(parent)
	if err != nil {
		return nil, err
	}
	if subscription == nil || subscription.ID() == "" {
		return nil, errors.New("returned an empty subscription")
	}

	activationCtx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	statuses := subscription.Status()
	streamErrors := subscription.Errors()
	pendingEvent := false
	for {
		select {
		case status, ok := <-statuses:
			if !ok {
				return nil, errors.New("status channel closed before activation")
			}
			if status.State == exchange.SubscriptionActive {
				goto active
			}
		case _, ok := <-subscription.Events():
			if !ok {
				return nil, errors.New("event channel closed before activation")
			}
			pendingEvent = true
		case streamErr, ok := <-streamErrors:
			if !ok {
				streamErrors = nil
				continue
			}
			if streamErr != nil {
				return nil, fmt.Errorf("stream error: %w", streamErr)
			}
		case <-activationCtx.Done():
			return nil, fmt.Errorf("did not activate: %w", activationCtx.Err())
		}
	}

active:
	events := subscription.Events()
	statuses = subscription.Status()
	streamErrors = subscription.Errors()
	return &acceptanceSubscriptionWitness{
		operation: operation,
		arm: func() {
			pendingEvent = false
			for {
				select {
				case _, ok := <-events:
					if !ok {
						return
					}
				default:
					return
				}
			}
		},
		waitEvent: func(ctx context.Context) error {
			if pendingEvent {
				pendingEvent = false
				return nil
			}
			for {
				select {
				case _, ok := <-events:
					if !ok {
						return errors.New("event channel closed before evidence")
					}
					return nil
				case streamErr, ok := <-streamErrors:
					if !ok {
						streamErrors = nil
						continue
					}
					if streamErr != nil {
						return fmt.Errorf("stream error: %w", streamErr)
					}
				case _, ok := <-statuses:
					if !ok {
						statuses = nil
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		},
		close: subscription.Close,
	}, nil
}

func selectAcceptanceInstrument(
	instruments []exchange.Instrument,
	product exchange.Product,
	hint string,
) (exchange.Instrument, error) {
	want := normalizeAcceptanceSymbol(hint)
	for _, instrument := range instruments {
		if instrument.Product != product {
			continue
		}
		if normalizeAcceptanceSymbol(instrument.Symbol) == want {
			return instrument, nil
		}
		if product == exchange.ProductPerp &&
			strings.EqualFold(instrument.QuoteAsset, "USDC") &&
			normalizeAcceptanceSymbol(instrument.BaseAsset+"PERP") == want {
			return instrument, nil
		}
	}
	return exchange.Instrument{}, fmt.Errorf(
		"configured %s instrument %q is not admitted by the exchange (%d instruments returned)",
		product,
		hint,
		len(instruments),
	)
}

func normalizeAcceptanceSymbol(symbol string) string {
	replacer := strings.NewReplacer("-", "", "_", "", "/", "", " ", "")
	return strings.ToUpper(replacer.Replace(symbol))
}

func acceptanceNotional(t *testing.T) decimal.Decimal {
	t.Helper()
	notional, err := decimal.NewFromString(envOrDefault("EXCHANGE_ACCEPTANCE_NOTIONAL", exchangeAcceptanceNotional))
	if err != nil || !notional.IsPositive() {
		t.Fatalf("EXCHANGE_ACCEPTANCE_NOTIONAL must be a positive decimal")
	}
	if notional.LessThan(decimal.RequireFromString("45")) || notional.GreaterThan(decimal.RequireFromString("55")) {
		t.Fatalf("EXCHANGE_ACCEPTANCE_NOTIONAL=%s must remain approximately 50", notional)
	}
	return notional
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func closeAcceptanceClient(t *testing.T, client interface{ Close() error }) {
	t.Helper()
	if err := client.Close(); err != nil {
		t.Errorf("exchange client close: %v", err)
	}
}

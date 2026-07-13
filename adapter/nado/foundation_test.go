package nado

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoConfigDefaultsAndAccountIDPropagation(t *testing.T) {
	cfg := DefaultConfig(sdk.EnvironmentTestnet, enums.KindPerp)
	if cfg.AccountID != AccountIDUnified {
		t.Fatalf("default AccountID=%q, want %q", cfg.AccountID, AccountIDUnified)
	}
	if cfg.ProductKind != enums.KindPerp {
		t.Fatalf("ProductKind=%s", cfg.ProductKind)
	}
	if AccountIDForKind(enums.KindSpot) != AccountIDUnified || AccountIDForKind(enums.KindPerp) != AccountIDUnified {
		t.Fatalf("Nado spot/perp must share one logical account id")
	}

	const custom = "NADO-CUSTOM-ACCOUNT"
	provider := nadoTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC))
	exec := newExecutionClient(nil, provider, clk, enums.KindSpot, custom)
	acct := newAccountClient(nil, provider, clk, enums.KindSpot, custom)
	if exec.AccountID() != custom || acct.AccountID() != custom {
		t.Fatalf("custom account id did not propagate: exec=%q acct=%q", exec.AccountID(), acct.AccountID())
	}
}

func TestNadoRegistryFromDiscoveryPreservesProductIdentityAndIncrements(t *testing.T) {
	provider, err := newInstrumentProviderFromDiscovery(nadoTestProducts(), nadoTestSymbols(), []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	if err != nil {
		t.Fatalf("newInstrumentProviderFromDiscovery: %v", err)
	}

	spotID := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindSpot}
	spot, ok := provider.Instrument(spotID)
	if !ok {
		t.Fatalf("missing spot instrument %s", spotID)
	}
	if spot.VenueSymbol != "ETH_USDT0" || spot.AssetIndex != nil || spot.VenueIntCode != nil {
		t.Fatalf("spot venue identity not preserved: %+v", spot)
	}
	if productID, ok := provider.ProductID(spotID); !ok || productID != 1 {
		t.Fatalf("spot product id=%d ok=%v, want adapter-local 1", productID, ok)
	}
	if spot.Settle != "USDT0" ||
		!spot.PriceTick.Equal(decimal.RequireFromString("0.01")) ||
		!spot.SizeStep.Equal(decimal.RequireFromString("0.0001")) ||
		!spot.MinQty.Equal(decimal.RequireFromString("0.0001")) ||
		!spot.MinNotional.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("spot increments/settle mismatch: %+v", spot)
	}

	perpID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	perp, ok := provider.Instrument(perpID)
	if !ok {
		t.Fatalf("missing perp instrument %s", perpID)
	}
	if perp.VenueSymbol != "BTC-PERP_USDT0" || perp.AssetIndex != nil || perp.VenueIntCode != nil || perp.PositionMode != model.NetOnly {
		t.Fatalf("perp venue identity/mode mismatch: %+v", perp)
	}
	if perp.Settle != "USDT0" || !perp.PriceTick.Equal(decimal.RequireFromString("0.1")) || !perp.SizeStep.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("perp increments/settle mismatch: %+v", perp)
	}

	if _, ok := provider.ResolveVenueInstrument("ETH_USDT0", enums.KindSpot, "USDT0"); !ok {
		t.Fatalf("spot venue symbol not indexed")
	}
	if id, ok := provider.ResolveProductID(2); !ok || id != perpID {
		t.Fatalf("product id resolve=%+v ok=%v", id, ok)
	}
}

func TestNadoRegistryPreservesIsolatedOnlyCapability(t *testing.T) {
	symbols := nadoTestSymbols()
	perp := symbols.Symbols["BTC_USDT0-PERP"]
	perp.IsolatedOnly = true
	symbols.Symbols["BTC_USDT0-PERP"] = perp
	provider, err := newInstrumentProviderFromDiscovery(nadoTestProducts(), symbols, []enums.InstrumentKind{enums.KindPerp})
	if err != nil {
		t.Fatalf("newInstrumentProviderFromDiscovery: %v", err)
	}
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	if isolatedOnly, ok := provider.IsolatedOnly(id); !ok || !isolatedOnly {
		t.Fatalf("isolated_only=%v ok=%v, want true true", isolatedOnly, ok)
	}
}

func TestNadoRegistryRejectsInactiveAndMismatchedDiscovery(t *testing.T) {
	symbols := nadoTestSymbols()
	inactive := symbols.Symbols["BTC_USDT0-PERP"]
	inactive.TradingStatus = sdk.TradingStatusNotTradable
	symbols.Symbols["BTC_USDT0-PERP"] = inactive
	provider, err := newInstrumentProviderFromDiscovery(nadoTestProducts(), symbols, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	if err != nil {
		t.Fatalf("inactive product should be filtered without rejecting active products: %v", err)
	}
	if _, ok := provider.ResolveProductID(2); ok {
		t.Fatal("inactive product was loaded")
	}

	mismatched := nadoTestSymbols()
	spot := mismatched.Symbols["ETH_USDT0"]
	spot.PriceIncrementX18 = "90000000000000000"
	mismatched.Symbols["ETH_USDT0"] = spot
	if _, err := newInstrumentProviderFromDiscovery(nadoTestProducts(), mismatched, []enums.InstrumentKind{enums.KindSpot}); err == nil {
		t.Fatalf("mismatched product discovery must be rejected")
	}

	malformedProducts := nadoTestProducts()
	malformedSymbols := nadoTestSymbols()
	malformedProducts.SpotProducts[1].BookInfo.SizeIncrement = "not-a-number"
	malformed := malformedSymbols.Symbols["ETH_USDT0"]
	malformed.SizeIncrement = "not-a-number"
	malformedSymbols.Symbols["ETH_USDT0"] = malformed
	if _, err := newInstrumentProviderFromDiscovery(malformedProducts, malformedSymbols, []enums.InstrumentKind{enums.KindSpot}); err == nil {
		t.Fatal("malformed but mutually matching increments must fail closed")
	}
}

func TestNadoRegistryAcceptsCurrentUnqualifiedSymbolsAndMissingProductZeroSymbol(t *testing.T) {
	products := nadoTestProducts()
	symbols := nadoTestSymbols()
	delete(symbols.Symbols, "USDT0")
	spot := symbols.Symbols["ETH_USDT0"]
	delete(symbols.Symbols, "ETH_USDT0")
	spot.Symbol = "ETH"
	symbols.Symbols["ETH"] = spot
	perp := symbols.Symbols["BTC_USDT0-PERP"]
	delete(symbols.Symbols, "BTC_USDT0-PERP")
	perp.Symbol = "BTC-PERP"
	symbols.Symbols["BTC-PERP"] = perp

	provider, err := newInstrumentProviderFromDiscovery(products, symbols, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	if err != nil {
		t.Fatalf("current Nado symbols schema rejected: %v", err)
	}
	if _, ok := provider.ResolveVenueInstrument("ETH_USDT0", enums.KindSpot, "USDT0"); !ok {
		t.Fatal("unqualified spot symbol was not normalized to its V2 ticker ID")
	}
	if id, ok := provider.ResolveVenueInstrument("BTC-PERP_USDT0", enums.KindPerp, "USDT0"); !ok || id.Symbol != "BTC-USDT0" {
		t.Fatalf("unqualified perp symbol was not normalized to its V2 ticker ID: id=%+v ok=%v", id, ok)
	}
}

func TestNadoAssetDiscoveryAddsAccountCurrencyWithoutAddingInstrument(t *testing.T) {
	provider := nadoTestProvider()
	assets := []sdk.AssetV2{
		{ProductId: 0, Symbol: "USDT0"},
		{ProductId: 1, TickerId: "ETH_USDT0", MarketType: string(sdk.MarketTypeSpot), Symbol: "ETH"},
		{ProductId: 2, TickerId: "BTC-PERP_USDT0", MarketType: string(sdk.MarketTypePerp), Symbol: "BTC-PERP"},
		{ProductId: 11, TickerId: "NLP_USDT0", MarketType: string(sdk.MarketTypeSpot), Symbol: "NLP"},
	}
	if err := provider.ApplyAssetDiscovery(assets); err != nil {
		t.Fatalf("apply asset discovery: %v", err)
	}
	if currency, ok := provider.CurrencyForProductID(11); !ok || currency != "NLP" {
		t.Fatalf("NLP account currency=%q ok=%v, want NLP true", currency, ok)
	}
	if _, ok := provider.ResolveProductID(11); ok {
		t.Fatal("account-only NLP metadata must not add a tradable instrument")
	}
	perpID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	perp, ok := provider.Instrument(perpID)
	if !ok || perp.VenueSymbol != "BTC-PERP_USDT0" {
		t.Fatalf("Perp V2 ticker identity mismatch: inst=%+v ok=%v", perp, ok)
	}
}

func TestNadoProductScopeDoesNotSliceUnifiedAccountRegistry(t *testing.T) {
	provider := nadoTestProvider()
	spotMarket := newMarketDataClient(nil, provider, clock.NewRealClock(), enums.KindSpot)
	for _, inst := range spotMarket.InstrumentProvider().All() {
		if inst.ID.Kind != enums.KindSpot {
			t.Fatalf("spot market provider leaked %s instrument", inst.ID.Kind)
		}
	}

	perpID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	if _, ok := provider.Instrument(perpID); !ok {
		t.Fatal("unified account registry lost Perp metadata under Spot product scope")
	}
	account := sdk.AccountInfo{PerpBalances: []sdk.Balance{{ProductID: 2}}}
	account.PerpBalances[0].Balance.Amount = "1000000000000000000"
	positions, err := positionsFromNado(account, provider, AccountIDUnified, time.Unix(100, 0))
	if err != nil || len(positions) != 1 || positions[0].InstrumentID != perpID {
		t.Fatalf("unified Perp position conversion positions=%+v err=%v", positions, err)
	}
}

func TestNadoPositionsIgnoreUnknownZeroBalancesButRejectUnknownOpenPositions(t *testing.T) {
	provider := nadoTestProvider()
	unknown := sdk.Balance{ProductID: 42}
	unknown.Balance.Amount = "0"
	known := sdk.Balance{ProductID: 2}
	known.Balance.Amount = "1500000000000000000"
	account := sdk.AccountInfo{PerpBalances: []sdk.Balance{unknown, known}}
	positions, err := positionsFromNado(account, provider, AccountIDUnified, time.Unix(100, 0))
	if err != nil {
		t.Fatalf("unknown zero balance should be ignored: %v", err)
	}
	if len(positions) != 1 || positions[0].InstrumentID.Symbol != "BTC-USDT0" {
		t.Fatalf("positions=%+v, want only known open position", positions)
	}

	account.PerpBalances[0].Balance.Amount = "1000000000000000000"
	if _, err := positionsFromNado(account, provider, AccountIDUnified, time.Unix(100, 0)); err == nil {
		t.Fatal("unknown non-zero position must fail closed")
	}
}

func TestNadoPositionsReuseFreshAccountStateSnapshot(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC))
	acct := newAccountClient(nil, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
	account := sdk.AccountInfo{Exists: true, PerpBalances: []sdk.Balance{{ProductID: 2}}}
	account.PerpBalances[0].Balance.Amount = "1500000000000000000"
	acct.storeAccountSnapshot(&sdk.AccountSnapshot{Account: account, ReceivedAt: clk.Now()})

	positions, err := acct.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions should reuse cached snapshot without REST: %v", err)
	}
	if len(positions) != 1 || !positions[0].Quantity.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("cached positions mismatch: %+v", positions)
	}

	clk.Advance(2 * time.Second)
	if _, err := acct.Positions(context.Background()); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("stale cached snapshot err=%v, want ErrNotSupported", err)
	}
}

func nadoTestProvider() *instrumentProvider {
	provider, err := newInstrumentProviderFromDiscovery(nadoTestProducts(), nadoTestSymbols(), []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	if err != nil {
		panic(err)
	}
	return provider
}

func nadoTestProducts() sdk.AllProductsResponse {
	return sdk.AllProductsResponse{
		SpotProducts: []sdk.SpotProduct{
			{ProductID: 0, BookInfo: sdk.ProductBookInfo{PriceIncrementX18: "1000000000000000000", SizeIncrement: "1", MinSize: "1"}},
			{ProductID: 1, BookInfo: sdk.ProductBookInfo{PriceIncrementX18: "10000000000000000", SizeIncrement: "100000000000000", MinSize: "100000000000000"}},
		},
		PerpProducts: []sdk.PerpProduct{
			{ProductID: 2, BookInfo: sdk.ProductBookInfo{PriceIncrementX18: "100000000000000000", SizeIncrement: "1000000000000000", MinSize: "1000000000000000"}},
		},
	}
}

func nadoTestSymbols() sdk.SymbolsInfo {
	return sdk.SymbolsInfo{Symbols: map[string]sdk.Symbol{
		"USDT0": {
			Type: string(sdk.MarketTypeSpot), ProductID: 0, Symbol: "USDT0",
			PriceIncrementX18: "1000000000000000000", SizeIncrement: "1", MinSize: "1",
			TradingStatus: sdk.TradingStatusLive,
		},
		"ETH_USDT0": {
			Type: string(sdk.MarketTypeSpot), ProductID: 1, Symbol: "ETH_USDT0",
			PriceIncrementX18: "10000000000000000", SizeIncrement: "100000000000000", MinSize: "100000000000000",
			LongWeightInitialX18: "800000000000000000", LongWeightMaintenanceX18: "900000000000000000",
			MakerFeeRateX18: "-100000000000000", TakerFeeRateX18: "2000000000000000",
			TradingStatus: sdk.TradingStatusLive,
		},
		"BTC_USDT0-PERP": {
			Type: string(sdk.MarketTypePerp), ProductID: 2, Symbol: "BTC_USDT0-PERP",
			PriceIncrementX18: "100000000000000000", SizeIncrement: "1000000000000000", MinSize: "1000000000000000",
			LongWeightInitialX18: "900000000000000000", LongWeightMaintenanceX18: "950000000000000000",
			MakerFeeRateX18: "-50000000000000", TakerFeeRateX18: "1000000000000000",
			TradingStatus: sdk.TradingStatusPostOnly,
		},
	}}
}

func TestNadoConversionsAreExactAndTyped(t *testing.T) {
	inst := nadoTestProvider().All()[0]
	req := model.OrderRequest{
		AccountID:    AccountIDUnified,
		InstrumentID: inst.ID,
		ClientID:     "client-1",
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     decimal.RequireFromString("0.002"),
		Price:        decimal.RequireFromString("1234.50"),
		ReduceOnly:   true,
		PositionSide: enums.PosNet,
	}
	productID, ok := nadoTestProvider().ProductID(inst.ID)
	if !ok {
		t.Fatal("missing adapter-local product id")
	}
	input, err := orderRequestToNado(req, inst, productID)
	if err != nil {
		t.Fatalf("orderRequestToNado: %v", err)
	}
	if input.ProductId != productID || input.Side != sdk.OrderSideSell || input.OrderType != sdk.OrderTypeLimit {
		t.Fatalf("unexpected order input: %+v", input)
	}
	if input.Price != "1234.5" || input.Amount != "0.002" || !input.PostOnly || !input.ReduceOnly {
		t.Fatalf("exact order flags/decimals lost: %+v", input)
	}

	if got := tifFromNado(sdk.OrderTypeIOC); got != enums.TifIOC {
		t.Fatalf("TIF conversion=%s", got)
	}
	if got := statusFromNadoReason(sdk.OrderReasonFilled); got != enums.StatusFilled {
		t.Fatalf("status conversion=%s", got)
	}
	if got, err := feeFromX18("2500000000000000"); err != nil || !got.Equal(decimal.RequireFromString("0.0025")) {
		t.Fatalf("feeFromX18=%s", got)
	}
	if _, err := orderTypeToNado(enums.TypeStopMarket); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("unsupported order type err=%v, want ErrNotSupported", err)
	}
}

func TestNadoCapabilityTruthAndUnsupportedSurfaces(t *testing.T) {
	provider := nadoTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 2, 0, 0, 0, time.UTC))
	market := newMarketDataClient(nil, provider, clk, enums.KindSpot)
	exec := newExecutionClient(nil, provider, clk, enums.KindSpot, AccountIDUnified)
	acct := newAccountClient(nil, provider, clk, enums.KindSpot, AccountIDUnified)

	var _ contract.MarketDataClient = market
	var _ contract.ExecutionClient = exec
	var _ contract.AccountClient = acct
	var _ contract.AccountIDProvider = exec
	var _ contract.AccountIDProvider = acct

	marketCaps := market.Capabilities()
	if len(marketCaps.Products) != 1 || marketCaps.Products[0].Kind != enums.KindSpot || !marketCaps.Products[0].Market || marketCaps.ReferenceData.CurrentFunding {
		t.Fatalf("spot market capabilities are not truthful: %+v", marketCaps)
	}
	execCaps := exec.Capabilities()
	if execCaps.Trading.Submit || !execCaps.Trading.Cancel || execCaps.Trading.Modify || !execCaps.Reports.OpenOrders || execCaps.Reports.FillHistory {
		t.Fatalf("execution capabilities are not truthful: %+v", execCaps)
	}
	acctCaps := acct.Capabilities()
	if !acctCaps.Reports.AccountStateSnapshots || acctCaps.Reports.PositionReports || acctCaps.Streaming.Account {
		t.Fatalf("account capabilities are not truthful: %+v", acctCaps)
	}
	perpAcctCaps := newAccountClient(nil, provider, clk, enums.KindPerp, AccountIDUnified).Capabilities()
	if !perpAcctCaps.Reports.PositionReports {
		t.Fatalf("perp account must advertise position reports: %+v", perpAcctCaps)
	}

	if err := market.SubscribeBook(context.Background(), model.InstrumentID{}); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SubscribeBook err=%v, want ErrNotSupported", err)
	}
	if _, err := exec.Modify(context.Background(), provider.All()[0].ID, "digest", decimal.RequireFromString("1"), decimal.RequireFromString("1")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("Modify err=%v, want ErrNotSupported", err)
	}
	if err := acct.SetLeverage(context.Background(), provider.All()[0].ID, decimal.RequireFromString("2")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetLeverage err=%v, want ErrNotSupported", err)
	}
}

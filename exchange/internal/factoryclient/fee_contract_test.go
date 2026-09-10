package factoryclient

import (
	"context"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

func TestVenueFeeSignsNormalizeChargesAndRebates(t *testing.T) {
	for _, fee := range []string{"0.01", "-0.002", "0"} {
		spot, err := binanceSpotFill(binancespot.Trade{ID: 1, OrderID: 2, Symbol: "BTCUSDT", Price: "100", Qty: "1", Commission: fee, CommissionAsset: "BTC", Time: 1700000000000}, "BTCUSDT", "BTCUSDT")
		if err != nil || spot.Fee.String() != fee {
			t.Fatalf("binance spot=%+v err=%v", spot, err)
		}
		perp, err := binancePerpFill(binanceperp.Trade{ID: 1, OrderID: 2, Symbol: "BTCUSDT", Price: "100", Qty: "1", Commission: fee, CommissionAsset: "USDT", Time: 1700000000000})
		if err != nil || perp.Fee.String() != fee {
			t.Fatalf("binance perp=%+v err=%v", perp, err)
		}
		okxRow, err := okxFill(okx.Fill{InstId: "BTC-USDT", Side: okx.SideBuy, FillPx: "100", FillSz: "1", Fee: decimal.RequireFromString(fee).Neg().String(), FeeCcy: "BTC", Ts: "1700000000000"}, decimal.NewFromInt(1))
		if err != nil || okxRow.Fee.String() != fee {
			t.Fatalf("okx=%+v err=%v", okxRow, err)
		}
	}
}

func feeTestMeta(t *testing.T, venue exchange.Venue) clientMeta {
	t.Helper()
	descriptor := exchange.SourceDescriptor{Name: "fee-test", Endpoint: "https://identity.invalid", WebSocketEndpoint: "wss://identity.invalid/ws", Transport: "test"}
	scope := exchange.CatalogScope{Venue: venue, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot, EndpointFingerprint: descriptor.Fingerprint()}
	catalog := exchange.NewDefaultMemoryCatalog()
	instrument := exchange.Instrument{Identity: exchange.InstrumentIdentity{Venue: venue, Product: exchange.ProductSpot, Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"}, Rules: exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")}}
	if err := catalog.RegisterSource(scope, descriptor, exchange.StaticInstrumentLoader{Snapshot: exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: []exchange.Instrument{instrument}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Refresh(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	return clientMeta{venue: venue, product: exchange.ProductSpot, catalog: catalog, catalogScope: scope}
}
func TestBybitFeeCurrencyFieldsCannotConflict(t *testing.T) {
	meta := feeTestMeta(t, exchange.VenueBybit)
	row := bybit.ExecutionRecord{Symbol: "BTCUSDT", Side: "Buy", ExecPrice: "100", ExecQty: "1", ExecFee: "-0.001", ExecTime: "1700000000000", FeeCoin: "BTC"}
	for _, legacy := range []bool{false, true} {
		if legacy {
			row.FeeCoin, row.FeeCurrency = "", "BTC"
		}
		fills, err := convertBybitFills(context.Background(), meta, "Fills", []bybit.ExecutionRecord{row}, "")
		if err != nil || len(fills) != 1 || fills[0].FeeAsset != "BTC" || fills[0].Fee.String() != "-0.001" {
			t.Fatalf("fills=%+v err=%v", fills, err)
		}
	}
	row.FeeCoin = "USDT"
	if _, err := convertBybitFills(context.Background(), meta, "Fills", []bybit.ExecutionRecord{row}, ""); err == nil {
		t.Fatal("conflicting fee currencies accepted")
	}
}
func TestBitgetDoesNotDiscardAdditionalFeeDetails(t *testing.T) {
	meta := feeTestMeta(t, exchange.VenueBitget)
	row := bitget.FillRecord{Symbol: "BTCUSDT", Side: "buy", ExecPrice: "100", ExecQty: "1", ExecTime: "1700000000000", FeeDetail: []bitget.FeeDetail{{FeeCoin: "BTC", Fee: "0.001"}, {FeeCoin: "BTC", Fee: "-0.0002"}}}
	fills, err := convertBitgetFills(context.Background(), meta, "Fills", []bitget.FillRecord{row}, "")
	if err != nil || len(fills) != 1 || fills[0].Fee.String() != "0.0008" || fills[0].FeeAsset != "BTC" {
		t.Fatalf("fills=%+v err=%v", fills, err)
	}
	row.FeeDetail[1].FeeCoin = "USDT"
	if _, err := convertBitgetFills(context.Background(), meta, "Fills", []bitget.FillRecord{row}, ""); err == nil {
		t.Fatal("multiple currencies silently discarded")
	}
	row.FeeDetail[1].FeeCoin = ""
	if _, err := convertBitgetFills(context.Background(), meta, "Fills", []bitget.FillRecord{row}, ""); err == nil {
		t.Fatal("missing fee asset silently accepted")
	}
}

func TestOKXOrderStreamUsesPerExecutionFeeAndRejectsMissingFee(t *testing.T) {
	ws := &fakeOKXPrivateWSClient{}
	backend := newOKXSpotPrivateWSBackendWithClient(okxPrivateTestMeta(exchange.ProductSpot), ws, fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT": 1001}), nil)
	var fills []exchange.FillEvent
	var failures []error
	stop, err := backend.StartFills(context.Background(), okxPrivateTestInstrument(exchange.ProductSpot), streamCallbacks[exchange.FillEvent]{Event: func(event exchange.FillEvent) { fills = append(fills, event) }, Error: func(err error) { failures = append(failures, err) }})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	row := okx.Order{InstType: okxSpotType, InstId: "BTC-USDT", OrdId: "1", ClOrdId: "12", Side: okx.SideBuy, OrdType: okx.OrderTypeLimit, Sz: "1", Px: "100", AccFillSz: "0.2", AvgPx: "100", State: okx.OrderStatusPartiallyFilled, FillPx: "100", FillSz: "0.2", FillTime: "1700000001000", TradeId: "t1", FillFee: "-0.001", FillFeeCcy: "BTC", Fee: "-0.001", FeeCcy: "BTC", CTime: "1700000000000", UTime: "1700000001000"}
	ws.orderHandler(&row)
	row.TradeId, row.FillFee, row.Fee, row.AccFillSz = "t2", "-0.002", "-0.003", "0.4"
	ws.orderHandler(&row)
	row.TradeId, row.FillFee = "t3", ""
	ws.orderHandler(&row)
	if len(fills) != 2 || fills[0].Fill.Fee.String() != "0.001" || fills[1].Fill.Fee.String() != "0.002" || len(failures) != 1 {
		t.Fatalf("fills=%+v failures=%v", fills, failures)
	}
}

func TestMissingFeeFactsAreNotInventedAsZeroCost(t *testing.T) {
	if _, err := okxFill(okx.Fill{Side: okx.SideBuy, FillPx: "100", FillSz: "1", FeeCcy: "BTC", Ts: "1700000000000"}, decimal.NewFromInt(1)); err == nil {
		t.Fatal("missing OKX cost became zero")
	}
	row := bybit.ExecutionRecord{Symbol: "BTCUSDT", Side: "Buy", ExecPrice: "100", ExecQty: "1", ExecTime: "1700000000000", FeeCurrency: "BTC"}
	if _, err := convertBybitFills(context.Background(), feeTestMeta(t, exchange.VenueBybit), "Fills", []bybit.ExecutionRecord{row}, ""); err == nil {
		t.Fatal("missing Bybit cost became zero")
	}
	row.ExecFee = "0.001"
	row.ExtraFees = []byte(`[{"feeCoin":"BTC","fee":"0.00001"}]`)
	if _, err := convertBybitFills(context.Background(), feeTestMeta(t, exchange.VenueBybit), "Fills", []bybit.ExecutionRecord{row}, ""); err == nil {
		t.Fatal("Bybit regional additional cost was discarded")
	}
	bitgetRow := bitget.FillRecord{Symbol: "BTCUSDT", Side: "buy", ExecPrice: "100", ExecQty: "1", ExecTime: "1700000000000"}
	if _, err := convertBitgetFills(context.Background(), feeTestMeta(t, exchange.VenueBitget), "Fills", []bitget.FillRecord{bitgetRow}, ""); err == nil {
		t.Fatal("missing Bitget costs became zero")
	}
	bitgetRow.FeeDetail = []bitget.FeeDetail{}
	fills, err := convertBitgetFills(context.Background(), feeTestMeta(t, exchange.VenueBitget), "Fills", []bitget.FillRecord{bitgetRow}, "")
	if err != nil || len(fills) != 1 || !fills[0].Fee.IsZero() {
		t.Fatalf("explicit empty costs=%+v %v", fills, err)
	}
}

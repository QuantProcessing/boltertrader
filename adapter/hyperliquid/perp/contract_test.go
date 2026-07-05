package perp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/cloid"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/contract/contracttest"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

var (
	_ contract.ExecutionClient  = (*executionClient)(nil)
	_ contract.AccountClient    = (*accountClient)(nil)
	_ contract.MarketDataClient = (*marketDataClient)(nil)
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func testPerpID() model.InstrumentID {
	return model.InstrumentID{Venue: venueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}
}

func testHIP3ID() model.InstrumentID {
	return model.InstrumentID{Venue: venueName, Symbol: "testdex:COIN-USDC", Kind: enums.KindPerp}
}

func testProvider(t *testing.T) *instruments.Registry {
	t.Helper()
	spotMeta := loadSpotMetaFixture(t)
	stdMeta := loadPerpMetaFixture(t, "../internal/instruments/testdata/perp_meta.json")
	hip3Meta := loadPerpMetaFixture(t, "../internal/instruments/testdata/hip3_meta.json")
	std, err := instruments.BuildStandardPerpInstruments(stdMeta)
	if err != nil {
		t.Fatalf("BuildStandardPerpInstruments: %v", err)
	}
	hip3, err := instruments.BuildHIP3PerpInstruments(sdkperp.PerpDex{Index: 2, Name: "testdex"}, hip3Meta, spotMeta)
	if err != nil {
		t.Fatalf("BuildHIP3PerpInstruments: %v", err)
	}
	return instruments.NewRegistry(append(std, hip3...)...)
}

func testREST(handler func(*http.Request, []byte) (string, int)) *sdkperp.Client {
	base := sdk.NewClient().
		WithEnvironment(sdk.EnvironmentTestnet).
		WithCredentials(strings.Repeat("01", 32), nil).
		WithAccount("0xabc")
	base.BaseURL = "https://unit.test"
	base.Http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		response, status := handler(r, body)
		if status == 0 {
			status = http.StatusOK
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(response)),
			Header:     make(http.Header),
		}, nil
	})}
	return sdkperp.NewClient(base)
}

func TestHyperliquidPerpContractCapabilities(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(*http.Request, []byte) (string, int) { return `{}`, http.StatusOK })
	market := newMarketDataClient(rest, nil, provider, clock.NewRealClock())
	exec := newExecutionClient(rest, provider, clock.NewRealClock())
	acct := newAccountClient(rest, provider, clock.NewRealClock(), "cross", d("1"), sdk.AccountAbstractionDefault)

	contracttest.RunPerpCapabilitySuite(t, contracttest.PerpCapabilitySuite{
		Venue: venueName,
		Market: contracttest.MarketCapabilities{
			OrderBook:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Perp order book is available through /info l2Book")},
			Bars:            contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Perp candles are available through /info candleSnapshot")},
			SubscribeBook:   contracttest.CapabilityProbe{Support: contracttest.InventorySupported("public websocket book stream is adapter-owned and covered by later testnet acceptance")},
			SubscribeQuotes: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("public websocket bbo stream is adapter-owned and covered by later testnet acceptance")},
			SubscribeTrades: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("public websocket trades stream is adapter-owned and covered by later testnet acceptance")},
		},
		Execution: contracttest.ExecutionCapabilities{
			Submit:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Perp order action submission is supported")},
			Cancel:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Perp cancel action is supported")},
			CancelAll:  contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Perp cancel-all is synthesized from openOrders")},
			Modify:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Perp modify action is supported")},
			OpenOrders: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Perp openOrders is supported")},
			MassStatus: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("open-order mass status is synthesized for reconciliation")},
		},
		Account: contracttest.AccountCapabilities{
			Balances:          contracttest.CapabilityProbe{Support: contracttest.InventorySupported("clearinghouseState margin summary is translated into USDC balance")},
			Positions:         contracttest.CapabilityProbe{Support: contracttest.InventorySupported("clearinghouseState positions are translated into signed net positions")},
			SetLeverage:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("updateLeverage action is supported")},
			SetCrossMargin:    contracttest.CapabilityProbe{Support: contracttest.InventorySupported("cross margin is represented by updateLeverage IsCross=true")},
			SetIsolatedMargin: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("isolated margin is represented by updateLeverage IsCross=false")},
		},
	})
	if market.Capabilities().Venue != venueName || exec.Capabilities().Venue != venueName || acct.Capabilities().Venue != venueName {
		t.Fatal("capabilities must declare Hyperliquid venue")
	}
}

func TestHyperliquidPerpNewLoadsStandardAndConfiguredHIP3(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		var response string
		switch req["type"] {
		case "allPerpMetas":
			response = `[
				{"universe":[{"name":"BTC","szDecimals":5,"maxLeverage":50}]},
				{"universe":[{"name":"ignored:SKIP","szDecimals":2,"maxLeverage":5}],"collateralToken":0},
				{"universe":[{"name":"testdex:COIN","szDecimals":2,"maxLeverage":5}],"collateralToken":0}
			]`
		case "meta":
			if req["dex"] == "testdex" {
				response = `{"universe":[{"name":"COIN","szDecimals":2,"maxLeverage":5}],"collateralToken":0}`
			} else {
				response = `{"universe":[{"name":"BTC","szDecimals":5,"maxLeverage":50}]}`
			}
		case "spotMeta":
			response = `{"tokens":[{"name":"USDC","szDecimals":6,"weiDecimals":6,"index":0,"tokenId":"0x0","isCanonical":true}],"universe":[]}`
		case "perpDexs":
			response = `[null,{"name":"ignored","fullName":"ignored dex"},{"name":"testdex","fullName":"test dex"}]`
		case "userAbstraction":
			response = `"default"`
		default:
			t.Fatalf("unexpected request: %s", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(response)),
			Header:     make(http.Header),
		}, nil
	})}

	adapter, err := New(context.Background(), Config{
		Environment:    sdk.EnvironmentTestnet,
		RESTBaseURL:    "https://unit.test",
		HTTPClient:     httpClient,
		IncludeHIP3:    true,
		HIP3Dexes:      []string{"testdex"},
		AccountAddress: "0xabc",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := adapter.Market.InstrumentProvider().Instrument(testPerpID()); !ok {
		t.Fatalf("standard perp instrument not loaded")
	}
	hip3, ok := adapter.Market.InstrumentProvider().Instrument(testHIP3ID())
	if !ok || hip3.AssetIndex == nil || *hip3.AssetIndex != 120000 {
		t.Fatalf("HIP-3 instrument=%+v ok=%v", hip3, ok)
	}
}

func TestHyperliquidPerpNewLoadsConfiguredHIP3FromAllPerpMetas(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		response := `{}`
		status := http.StatusOK
		switch req["type"] {
		case "allPerpMetas":
			response = `[
				{"universe":[{"name":"BTC","szDecimals":5,"maxLeverage":50}]},
				{"universe":[{"name":"ignored:SKIP","szDecimals":2,"maxLeverage":5}],"collateralToken":0},
				{"universe":[{"name":"testdex:COIN","szDecimals":2,"maxLeverage":5}],"collateralToken":0}
			]`
		case "spotMeta":
			response = `{"tokens":[{"name":"USDC","szDecimals":6,"weiDecimals":6,"index":0,"tokenId":"0x0","isCanonical":true}],"universe":[]}`
		case "perpDexs":
			response = `[null,{"name":"ignored"},{"name":"testdex"}]`
		case "userAbstraction":
			response = `"default"`
		case "meta":
			if req["dex"] == "testdex" {
				response = `{"error":"dex meta fallback should not be required"}`
				status = http.StatusInternalServerError
			} else {
				response = `{"universe":[{"name":"BTC","szDecimals":5,"maxLeverage":50}]}`
			}
		default:
			t.Fatalf("unexpected request: %s", body)
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(response)),
			Header:     make(http.Header),
		}, nil
	})}

	adapter, err := New(context.Background(), Config{
		Environment:    sdk.EnvironmentTestnet,
		RESTBaseURL:    "https://unit.test",
		HTTPClient:     httpClient,
		IncludeHIP3:    true,
		HIP3Dexes:      []string{"testdex"},
		AccountAddress: "0xabc",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer adapter.Close()
	hip3, ok := adapter.Market.InstrumentProvider().Instrument(testHIP3ID())
	if !ok || hip3.VenueSymbol != "testdex:COIN" {
		t.Fatalf("HIP-3 instrument not loaded from allPerpMetas: got=(%+v,%v)", hip3, ok)
	}
}

func TestHyperliquidPerpOrderBookTranslation(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/info" {
			t.Fatalf("request=%s %s, want POST /info", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req["type"] != "l2Book" || req["coin"] != "BTC" {
			t.Fatalf("unexpected l2Book request: %s", body)
		}
		return `{"coin":"BTC","levels":[[{"px":"65000","sz":"0.2","n":1}],[{"px":"65001","sz":"0.3","n":1}]],"time":1700000000000}`, 200
	})
	market := newMarketDataClient(rest, nil, provider, clock.NewRealClock())

	book, err := market.OrderBook(context.Background(), testPerpID(), 5)
	if err != nil {
		t.Fatalf("OrderBook: %v", err)
	}
	if book.InstrumentID != testPerpID() || !book.Bids[0].Price.Equal(d("65000")) || !book.Asks[0].Quantity.Equal(d("0.3")) {
		t.Fatalf("book=%+v", book)
	}
}

func TestHyperliquidPerpFundingRateTranslation(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/info" {
			t.Fatalf("request=%s %s, want POST /info", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req["type"] != "metaAndAssetCtxs" {
			t.Fatalf("unexpected funding request: %s", body)
		}
		return `[{"universe":[{"name":"BTC"},{"name":"ETH"}]},[{"funding":"0.0001","markPx":"65000.5","oraclePx":"64999.5","premium":"0.0002"},{"funding":"0.0003"}]]`, 200
	})
	market := newMarketDataClient(rest, nil, provider, clock.NewRealClock())

	funding, err := market.FundingRate(context.Background(), testPerpID())
	if err != nil {
		t.Fatalf("FundingRate: %v", err)
	}
	if funding.InstrumentID != testPerpID() || !funding.Rate.Equal(d("0.0001")) || !funding.MarkPrice.Equal(d("65000.5")) || !funding.OraclePrice.Equal(d("64999.5")) {
		t.Fatalf("funding=%+v", funding)
	}
}

func TestHyperliquidPerpSubmitStandardAndHIP3OrderRequestTranslation(t *testing.T) {
	provider := testProvider(t)
	var sawStandard, sawHIP3 bool
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/exchange" {
			t.Fatalf("request=%s %s, want POST /exchange", r.Method, r.URL.Path)
		}
		action := decodeAction(t, body)
		if action["builder"] != nil {
			t.Fatalf("first-phase testnet orders must not attach builder attribution: %s", body)
		}
		orders := action["orders"].([]any)
		order := orders[0].(map[string]any)
		switch int(order["a"].(float64)) {
		case 0:
			sawStandard = true
			if order["b"] != true || order["p"] != "65000" || order["s"] != "0.01" || order["r"] != true {
				t.Fatalf("unexpected standard order action: %s", body)
			}
			if order["c"] != cloid.ForClientID("c-perp-1") {
				t.Fatalf("standard cloid=%v, want mapped c-perp-1", order["c"])
			}
			return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":555,"cloid":"` + cloid.ForClientID("c-perp-1") + `","status":"open"}}]}}}`, 200
		case 120000:
			sawHIP3 = true
			if order["b"] != false || order["p"] != "10" || order["s"] != "2" || order["r"] != false {
				t.Fatalf("unexpected HIP-3 order action: %s", body)
			}
			if order["c"] != cloid.ForClientID("c-hip3-1") {
				t.Fatalf("HIP-3 cloid=%v, want mapped c-hip3-1", order["c"])
			}
			return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":556,"cloid":"` + cloid.ForClientID("c-hip3-1") + `","status":"open"}}]}}}`, 200
		default:
			t.Fatalf("unexpected asset id in action: %s", body)
		}
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock())

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testPerpID(),
		ClientID:     "c-perp-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.01"),
		Price:        d("65000"),
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	})
	if err != nil {
		t.Fatalf("Submit standard: %v", err)
	}
	if order.VenueOrderID != "555" || order.Status != enums.StatusNew || order.Request.ClientID != "c-perp-1" || !order.Request.ReduceOnly {
		t.Fatalf("standard order=%+v", order)
	}

	hip3Order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testHIP3ID(),
		ClientID:     "c-hip3-1",
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     d("2"),
		Price:        d("10"),
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("Submit HIP-3: %v", err)
	}
	if hip3Order.VenueOrderID != "556" || hip3Order.Request.InstrumentID != testHIP3ID() {
		t.Fatalf("HIP-3 order=%+v", hip3Order)
	}
	if !sawStandard || !sawHIP3 {
		t.Fatalf("expected standard and HIP-3 order actions, got standard=%v hip3=%v", sawStandard, sawHIP3)
	}
}

func TestHyperliquidPerpCancelModifyOpenOrdersAndMassStatus(t *testing.T) {
	provider := testProvider(t)
	var sawCancel, sawModify bool
	openCalls := 0
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		switch r.URL.Path {
		case "/exchange":
			action := decodeAction(t, body)
			switch action["type"] {
			case "cancel":
				sawCancel = true
				cancel := action["cancels"].([]any)[0].(map[string]any)
				if int(cancel["a"].(float64)) != 0 || int64(cancel["o"].(float64)) != 555 {
					t.Fatalf("unexpected cancel action: %s", body)
				}
				return `{"status":"ok","response":{"type":"cancel","data":{"statuses":["success"]}}}`, 200
			case "batchModify":
				sawModify = true
				modify := action["modifies"].([]any)[0].(map[string]any)
				order := modify["order"].(map[string]any)
				if int64(modify["oid"].(float64)) != 555 || order["b"] != true || order["p"] != "65100" || order["s"] != "0.02" {
					t.Fatalf("unexpected modify action: %s", body)
				}
				return `{"status":"ok","response":{"type":"modify","data":{"statuses":[{"resting":{"oid":555,"cloid":"c-perp-1","status":"open"}}]}}}`, 200
			default:
				t.Fatalf("unexpected exchange action: %s", body)
			}
		case "/info":
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode info body: %v", err)
			}
			switch req["type"] {
			case "orderStatus":
				return `{"order":{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.01","oid":555,"timestamp":1700000000000,"origSz":"0.01","status":"open","filledSz":"0","avgPx":"0"}}`, 200
			case "openOrders":
				openCalls++
				return `[{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.01","oid":555,"cloid":"c-perp-1","timestamp":1700000000000,"origSz":"0.01"},{"coin":"testdex:COIN","side":"A","limitPx":"10","sz":"2","oid":556,"cloid":"c-hip3-1","timestamp":1700000000000,"origSz":"2"}]`, 200
			default:
				t.Fatalf("unexpected info request: %s", body)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock())

	if err := exec.Cancel(context.Background(), testPerpID(), "555"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := exec.Modify(context.Background(), testPerpID(), "555", d("65100"), d("0.02")); err != nil {
		t.Fatalf("Modify: %v", err)
	}
	open, err := exec.OpenOrders(context.Background(), testPerpID())
	if err != nil {
		t.Fatalf("OpenOrders: %v", err)
	}
	if len(open) != 1 || open[0].VenueOrderID != "555" || open[0].Request.ClientID != "c-perp-1" {
		t.Fatalf("open orders=%+v", open)
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: "a1"})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if !mass.Partial || len(mass.OrderReports) != 2 {
		t.Fatalf("mass=%+v", mass)
	}
	if !sawCancel || !sawModify || openCalls == 0 {
		t.Fatalf("expected cancel/modify/open paths, got cancel=%v modify=%v openCalls=%d", sawCancel, sawModify, openCalls)
	}
	if openCalls != 2 {
		t.Fatalf("openOrders calls=%d, want OpenOrders + single mass-status snapshot", openCalls)
	}
}

func TestHyperliquidPerpOpenOrdersRestoresRuntimeClientIDFromMappedCloid(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		switch r.URL.Path {
		case "/exchange":
			return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":555,"cloid":"` + cloid.ForClientID("runtime-client-1") + `","status":"open"}}]}}}`, 200
		case "/info":
			return `[{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.01","oid":555,"cloid":"` + cloid.ForClientID("runtime-client-1") + `","timestamp":1700000000000,"origSz":"0.01"}]`, 200
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock())

	_, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testPerpID(),
		ClientID:     "runtime-client-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.01"),
		Price:        d("65000"),
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	open, err := exec.OpenOrders(context.Background(), testPerpID())
	if err != nil {
		t.Fatalf("OpenOrders: %v", err)
	}
	if len(open) != 1 || open[0].Request.ClientID != "runtime-client-1" {
		t.Fatalf("open orders=%+v, want original runtime client id", open)
	}
}

func TestHyperliquidPerpCancelEmitsCanceledOrderEvent(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		switch r.URL.Path {
		case "/exchange":
			action := decodeAction(t, body)
			switch action["type"] {
			case "order":
				return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":555,"cloid":"` + cloid.ForClientID("c-perp-1") + `","status":"open"}}]}}}`, 200
			case "cancel":
				return `{"status":"ok","response":{"type":"cancel","data":{"statuses":["success"]}}}`, 200
			default:
				t.Fatalf("unexpected exchange action: %s", body)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock())

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testPerpID(),
		ClientID:     "c-perp-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.01"),
		Price:        d("65000"),
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := exec.Cancel(context.Background(), testPerpID(), order.VenueOrderID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case env := <-exec.Events():
		oe, ok := env.Payload.(contract.OrderEvent)
		if !ok {
			t.Fatalf("event=%T, want OrderEvent", env.Payload)
		}
		if oe.Order.Status != enums.StatusCanceled || oe.Order.Request.ClientID != "c-perp-1" || oe.Order.VenueOrderID != "555" {
			t.Fatalf("order event=%+v", oe.Order)
		}
		if env.Source != contract.SourceAdapterREST || !env.Flags.Has(contract.EventFlagSynthetic) {
			t.Fatalf("event meta source=%s flags=%v, want REST synthetic", env.Source, env.Flags)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancel OrderEvent")
	}
}

func TestHyperliquidPerpStreamEventsRestoreRuntimeClientID(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":555,"cloid":"` + cloid.ForClientID("runtime-client-1") + `","status":"open"}}]}}}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock())

	if _, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testPerpID(),
		ClientID:     "runtime-client-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.01"),
		Price:        d("65000"),
		PositionSide: enums.PosNet,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	exec.emit(contract.OrderEvent{Order: model.Order{
		Request: model.OrderRequest{
			InstrumentID: testPerpID(),
			ClientID:     cloid.ForClientID("runtime-client-1"),
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     d("0.01"),
			Price:        d("65000"),
			PositionSide: enums.PosNet,
		},
		VenueOrderID: "555",
		Status:       enums.StatusNew,
	}})
	exec.emit(contract.FillEvent{Fill: model.Fill{
		InstrumentID: testPerpID(),
		VenueOrderID: "555",
		Side:         enums.SideBuy,
		Price:        d("65000"),
		Quantity:     d("0.01"),
		Fee:          d("0.01"),
		FeeCurrency:  "USDC",
		Liquidity:    enums.LiqMaker,
	}})

	select {
	case env := <-exec.Events():
		oe, ok := env.Payload.(contract.OrderEvent)
		if !ok {
			t.Fatalf("event=%T, want OrderEvent", env.Payload)
		}
		if oe.Order.Request.ClientID != "runtime-client-1" {
			t.Fatalf("order client id=%q, want original runtime id", oe.Order.Request.ClientID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for order event")
	}
	select {
	case env := <-exec.Events():
		fe, ok := env.Payload.(contract.FillEvent)
		if !ok {
			t.Fatalf("event=%T, want FillEvent", env.Payload)
		}
		if fe.Fill.ClientID != "runtime-client-1" {
			t.Fatalf("fill client id=%q, want original runtime id", fe.Fill.ClientID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fill event")
	}
}

func TestHyperliquidPerpBalancesPositionsAndMarginActions(t *testing.T) {
	provider := testProvider(t)
	var sawLeverage, sawIsolated bool
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		switch r.URL.Path {
		case "/info":
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode info body: %v", err)
			}
			if req["type"] != "clearinghouseState" || req["user"] != "0xabc" {
				t.Fatalf("unexpected clearinghouseState request: %s", body)
			}
			return `{"assetPositions":[{"position":{"coin":"BTC","entryPx":"65000","leverage":{"type":"cross","value":5},"szi":"-0.02","unrealizedPnl":"12.5","marginUsed":"20","positionValue":"1300"}}],"marginSummary":{"accountValue":"100","totalMarginUsed":"12","totalNtlPos":"50","totalRawUsd":"100"},"time":1700000000000,"withdrawable":"88"}`, 200
		case "/exchange":
			action := decodeAction(t, body)
			if action["type"] != "updateLeverage" || int(action["asset"].(float64)) != 0 {
				t.Fatalf("unexpected margin action: %s", body)
			}
			if action["isCross"] == true {
				sawLeverage = true
				if int(action["leverage"].(float64)) != 3 {
					t.Fatalf("SetLeverage action=%s", body)
				}
			} else {
				sawIsolated = true
				if int(action["leverage"].(float64)) != 1 {
					t.Fatalf("SetMarginMode isolated action=%s", body)
				}
			}
			return `{"status":"ok","response":{"type":"default","data":{"type":"ok"}}}`, 200
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return `{}`, 200
	})
	acct := newAccountClient(rest, provider, clock.NewRealClock(), "cross", d("1"), sdk.AccountAbstractionDefault)

	balances, err := acct.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(balances) != 1 || balances[0].Currency != "USDC" || !balances[0].Total.Equal(d("100")) || !balances[0].Available.Equal(d("88")) || !balances[0].Locked.Equal(d("12")) {
		t.Fatalf("balances=%+v", balances)
	}
	positions, err := acct.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(positions) != 1 || positions[0].InstrumentID != testPerpID() || positions[0].Side != enums.PosNet || !positions[0].Quantity.Equal(d("-0.02")) || !positions[0].Leverage.Equal(d("5")) {
		t.Fatalf("positions=%+v", positions)
	}
	if err := acct.SetLeverage(context.Background(), testPerpID(), d("3")); err != nil {
		t.Fatalf("SetLeverage: %v", err)
	}
	if err := acct.SetMarginMode(context.Background(), testPerpID(), "isolated"); err != nil {
		t.Fatalf("SetMarginMode isolated: %v", err)
	}
	if !sawLeverage || !sawIsolated {
		t.Fatalf("expected leverage and isolated actions, got leverage=%v isolated=%v", sawLeverage, sawIsolated)
	}
	if err := acct.SetMarginMode(context.Background(), testPerpID(), "portfolio"); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetMarginMode invalid err=%v, want ErrNotSupported", err)
	}
}

func TestHyperliquidPerpBalancesUseSpotStateForUnifiedAccount(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode info body: %v", err)
		}
		if req["type"] != "spotClearinghouseState" || req["user"] != "0xabc" {
			t.Fatalf("unexpected unified balance request: %s", body)
		}
		return `{"balances":[{"coin":"USDC","token":0,"hold":"1.5","total":"10","entryNtl":"0"}]}`, 200
	})
	acct := newAccountClient(rest, provider, clock.NewRealClock(), "cross", d("1"), sdk.AccountAbstractionUnifiedAccount)

	balances, err := acct.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(balances) != 1 || balances[0].Currency != "USDC" || !balances[0].Total.Equal(d("10")) || !balances[0].Available.Equal(d("8.5")) || !balances[0].Locked.Equal(d("1.5")) {
		t.Fatalf("balances=%+v", balances)
	}
}

func TestHyperliquidPerpEventTranslations(t *testing.T) {
	provider := testProvider(t)
	orderEvents := execEventsFromOrderUpdate(sdk.WsOrderUpdate{
		Order: sdk.WsOrder{
			Coin:      "BTC",
			Side:      "B",
			LimitPx:   "65000",
			Sz:        "0.01",
			Oid:       555,
			Timestamp: 1700000000000,
			OrigSz:    "0.01",
			Cliod:     "c-perp-1",
		},
		Status:          sdk.StatusOpen,
		StatusTimestamp: 1700000000123,
	}, provider)
	if len(orderEvents) != 1 {
		t.Fatalf("order events=%+v", orderEvents)
	}
	oe := orderEvents[0].(contract.OrderEvent)
	if oe.Order.Request.InstrumentID != testPerpID() || oe.Order.Status != enums.StatusNew || oe.Order.VenueOrderID != "555" {
		t.Fatalf("order event=%+v", oe)
	}

	fillEvents := execEventsFromUserFills(sdk.WsUserFills{
		User: "0xabc",
		Fills: []sdk.WsUserFill{{
			Coin:     "testdex:COIN",
			Px:       "10",
			Sz:       "2",
			Side:     "A",
			Time:     1700000000123,
			Hash:     "0xhash",
			Oid:      556,
			Crossed:  true,
			Fee:      "0.01",
			FeeToken: "USDC",
			Tid:      99,
		}},
	}, provider)
	if len(fillEvents) != 1 {
		t.Fatalf("fill events=%+v", fillEvents)
	}
	fe := fillEvents[0].(contract.FillEvent)
	if fe.Fill.InstrumentID != testHIP3ID() || fe.Fill.Liquidity != enums.LiqTaker || !fe.Fill.Quantity.Equal(d("2")) || fe.Fill.TradeID != "99" {
		t.Fatalf("fill event=%+v", fe)
	}

	accountEvents := accountEventsFromPerpPosition(&sdkperp.PerpPosition{
		AssetPositions: []struct {
			Position struct {
				Coin       string `json:"coin"`
				CumFunding struct {
					AllTime     string `json:"allTime"`
					SinceOpen   string `json:"sinceOpen"`
					SinceChange string `json:"sinceChange"`
				} `json:"cumFunding"`
				EntryPx  string `json:"entryPx"`
				Leverage struct {
					RawUsd string `json:"rawUsd"`
					Type   string `json:"type"`
					Value  int    `json:"value"`
				} `json:"leverage"`
				LiquidationPx  string `json:"liquidationPx"`
				MarginUsed     string `json:"marginUsed"`
				MaxLeverage    int    `json:"maxLeverage"`
				PositionValue  string `json:"positionValue"`
				ReturnOnEquity string `json:"returnOnEquity"`
				Szi            string `json:"szi"`
				UnrealizedPnl  string `json:"unrealizedPnl"`
			} `json:"position"`
			Type string `json:"type"`
		}{{
			Type: "oneWay",
		}},
		Time: 1700000000000,
	}, provider, clock.NewRealClock())
	if len(accountEvents) != 0 {
		t.Fatalf("zero asset position should not emit event: %+v", accountEvents)
	}
}

func decodeAction(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	action, ok := payload["action"].(map[string]any)
	if !ok {
		t.Fatalf("missing action: %s", body)
	}
	return action
}

func loadSpotMetaFixture(t *testing.T) *sdkspot.SpotMeta {
	t.Helper()
	data, err := os.ReadFile("../internal/instruments/testdata/spot_meta.json")
	if err != nil {
		t.Fatalf("read spot fixture: %v", err)
	}
	var meta sdkspot.SpotMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal spot fixture: %v", err)
	}
	return &meta
}

func loadPerpMetaFixture(t *testing.T, path string) *sdkperp.PrepMeta {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read perp fixture: %v", err)
	}
	var meta sdkperp.PrepMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal perp fixture: %v", err)
	}
	return &meta
}

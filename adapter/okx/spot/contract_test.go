package spot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/contract/contracttest"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

var (
	_ contract.ExecutionClient      = (*executionClient)(nil)
	_ contract.AccountClient        = (*accountClient)(nil)
	_ contract.AccountStateReporter = (*accountClient)(nil)
	_ contract.MarketDataClient     = (*marketDataClient)(nil)
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func ptrString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func testREST(handler func(*http.Request) (string, int)) *okx.Client {
	return okx.NewClient().
		WithCredentials("api-key", "secret", "passphrase").
		WithBaseURL("https://unit.test").
		WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, status := handler(r)
			if status == 0 {
				status = http.StatusOK
			}
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})})
}

func testSpotInstrument() *model.Instrument {
	return instrumentFromOKX(&okx.Instrument{
		InstId:   "ETH-USDT",
		InstType: instTypeSpot,
		BaseCcy:  "ETH",
		QuoteCcy: "USDT",
		State:    "live",
		TickSz:   "0.01",
		LotSz:    "0.0001",
		MinSz:    "0.0001",
	})
}

func testProvider(inst *model.Instrument) *instrumentProvider {
	p := newInstrumentProvider()
	p.byID[inst.ID.String()] = inst
	p.byInstID[inst.VenueSymbol] = inst.ID
	p.all = []*model.Instrument{inst}
	return p
}

func TestOKXSpotInstrumentTranslation(t *testing.T) {
	inst := testSpotInstrument()
	if inst == nil {
		t.Fatal("instrumentFromOKX returned nil")
	}
	if inst.ID.Kind != enums.KindSpot {
		t.Fatalf("kind=%v, want SPOT", inst.ID.Kind)
	}
	if inst.Settle != "USDT" {
		t.Fatalf("settle=%q, want quote currency", inst.Settle)
	}
	if inst.PositionMode != model.NetOnly {
		t.Fatalf("spot position mode=%v, want NetOnly", inst.PositionMode)
	}

	contracttest.RunInstrumentParsing(t, testProvider(inst), []contracttest.InstrumentExpectation{{
		ID:          model.InstrumentID{Venue: "OKX", Symbol: "ETH-USDT", Kind: enums.KindSpot},
		PriceTick:   d("0.01"),
		SizeStep:    d("0.0001"),
		MinNotional: decimal.Zero,
		VenueSymbol: "ETH-USDT",
		HasIntCode:  false,
		HasAssetIdx: false,
	}})
}

func TestOKXSpotInstrumentSkipsNonSpotAndNonLive(t *testing.T) {
	if instrumentFromOKX(&okx.Instrument{InstId: "ETH-USDT-SWAP", InstType: "SWAP", BaseCcy: "ETH", QuoteCcy: "USDT"}) != nil {
		t.Fatal("non-SPOT instrument should be skipped")
	}
	if instrumentFromOKX(&okx.Instrument{InstId: "ETH-USDT", InstType: "SPOT", BaseCcy: "ETH", QuoteCcy: "USDT", State: "suspend"}) != nil {
		t.Fatal("non-live SPOT instrument should be skipped")
	}
}

func TestOKXSpotDepthTranslation(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v5/market/books" {
			t.Fatalf("request=%s %s, want GET /api/v5/market/books", r.Method, r.URL.Path)
		}
		if q := r.URL.Query(); q.Get("instId") != "ETH-USDT" || q.Get("sz") != "5" {
			t.Fatalf("query=%s, want instId ETH-USDT sz 5", q.Encode())
		}
		return `{"code":"0","msg":"","data":[{"bids":[["3000.01","0.5","0","1"]],"asks":[["3000.02","0.7","0","1"]],"ts":"1700000000000"}]}`, 200
	})
	market := newMarketDataClient(rest, nil, testProvider(inst), clock.NewRealClock())

	book, err := market.OrderBook(context.Background(), inst.ID, 5)
	if err != nil {
		t.Fatalf("OrderBook: %v", err)
	}
	if !book.Bids[0].Price.Equal(d("3000.01")) || !book.Asks[0].Quantity.Equal(d("0.7")) {
		t.Fatalf("book=%+v", book)
	}
}

func TestOKXSpotSubmitOrderRequestTranslation(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/order" {
			t.Fatalf("request=%s %s, want POST /api/v5/trade/order", r.Method, r.URL.Path)
		}
		var req okx.OrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.InstId != "ETH-USDT" || req.TdMode != defaultSpotTdMode || req.Side != "buy" || req.OrdType != "limit" {
			t.Fatalf("unexpected request: %+v", req)
		}
		if req.PosSide != nil || req.ReduceOnly != nil {
			t.Fatalf("spot submit leaked derivative-only fields: %+v", req)
		}
		return `{"code":"0","msg":"","data":[{"ordId":"555","clOrdId":"c-spot-1","sCode":"0"}]}`, 200
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock(), "")

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst.ID,
		ClientID:     "c-spot-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.0100"),
		Price:        d("3000.01"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if order.VenueOrderID != "555" || order.Request.PositionSide != enums.PosNet || order.Request.ReduceOnly {
		t.Fatalf("order=%+v", order)
	}
}

func TestOKXSpotSubmitUsesConfiguredTdMode(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		var req okx.OrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.TdMode != spotTdModeCross {
			t.Fatalf("tdMode=%q, want cross", req.TdMode)
		}
		return `{"code":"0","msg":"","data":[{"ordId":"555","clOrdId":"c-spot-cross","sCode":"0"}]}`, 200
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock(), spotTdModeCross)

	if _, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst.ID,
		ClientID:     "c-spot-cross",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.0100"),
		Price:        d("3000.01"),
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

func TestOKXSpotSCodeRejectWrapsVenueRejected(t *testing.T) {
	err := checkSCode([]okx.OrderId{{SCode: "51008", SMsg: "insufficient balance"}})
	if !errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("sCode error=%v, want contract.ErrVenueRejected", err)
	}
}

func TestOKXSpotOrdTypeFoldingMatchesNT(t *testing.T) {
	cases := []struct {
		ot  enums.OrderType
		tif enums.TimeInForce
		okx string
	}{
		{enums.TypeMarket, enums.TifUnknown, "market"},
		{enums.TypeMarket, enums.TifIOC, "market"},
		{enums.TypeLimit, enums.TifGTC, "limit"},
		{enums.TypeLimit, enums.TifIOC, "ioc"},
		{enums.TypeLimit, enums.TifFOK, "fok"},
		{enums.TypeLimit, enums.TifGTX, "post_only"},
	}
	for _, tc := range cases {
		got, err := ordTypeToOKX(tc.ot, tc.tif)
		if err != nil {
			t.Fatalf("ordTypeToOKX(%v,%v): %v", tc.ot, tc.tif, err)
		}
		if got != tc.okx {
			t.Fatalf("ordTypeToOKX(%v,%v)=%q, want %q", tc.ot, tc.tif, got, tc.okx)
		}
	}
	if _, err := ordTypeToOKX(enums.TypeMarket, enums.TifFOK); err == nil {
		t.Fatal("Market+FOK should be rejected like NautilusTrader")
	}
}

func TestOKXSpotSubmitConditionalOrderUsesAlgoEndpoint(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/order-algo" {
			t.Fatalf("request=%s %s, want POST /api/v5/trade/order-algo", r.Method, r.URL.Path)
		}
		var req okx.AlgoOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.InstId != "ETH-USDT" || req.TdMode != defaultSpotTdMode || req.OrdType != "trigger" || req.Sz != "0.01" {
			t.Fatalf("unexpected algo request: %+v", req)
		}
		if req.AlgoClOrdId == nil || *req.AlgoClOrdId != "c-mit" {
			t.Fatalf("algoClOrdId=%v, want c-mit", req.AlgoClOrdId)
		}
		if got := ptrString(req.TriggerPx); got != "3100" {
			t.Fatalf("triggerPx=%q, want 3100", got)
		}
		if got := ptrString(req.OrderPx); got != "-1" {
			t.Fatalf("orderPx=%q, want -1", got)
		}
		return `{"code":"0","msg":"","data":[{"algoId":"spot-algo-1","algoClOrdId":"c-mit","sCode":"0"}]}`, 200
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock(), "")

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst.ID,
		ClientID:     "c-mit",
		Side:         enums.SideSell,
		Type:         enums.TypeMarketIfTouched,
		Quantity:     d("0.0100"),
		TriggerPrice: d("3100.00"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if order.VenueOrderID != "spot-algo-1" || order.Request.ClientID != "c-mit" {
		t.Fatalf("order=%+v", order)
	}
}

func TestOKXSpotRejectsDerivativeOrderFields(t *testing.T) {
	inst := testSpotInstrument()
	exec := newExecutionClient(testREST(func(r *http.Request) (string, int) {
		t.Fatalf("spot derivative-field rejection must happen before REST request: %s", r.URL.String())
		return `{}`, 500
	}), testProvider(inst), clock.NewRealClock(), "")

	for name, req := range map[string]model.OrderRequest{
		"reduce_only": {
			InstrumentID: inst.ID,
			ClientID:     "c-reduce-only",
			Side:         enums.SideSell,
			Type:         enums.TypeMarket,
			Quantity:     d("0.01"),
			ReduceOnly:   true,
		},
		"position_side": {
			InstrumentID: inst.ID,
			ClientID:     "c-position-side",
			Side:         enums.SideBuy,
			Type:         enums.TypeMarket,
			Quantity:     d("0.01"),
			PositionSide: enums.PosLong,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := exec.Submit(context.Background(), req)
			if !errors.Is(err, contract.ErrNotSupported) {
				t.Fatalf("Submit err=%v, want ErrNotSupported", err)
			}
		})
	}
}

func TestOKXSpotCancelOrderTranslation(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/cancel-order" {
			t.Fatalf("request=%s %s, want POST /api/v5/trade/cancel-order", r.Method, r.URL.Path)
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["instId"] != "ETH-USDT" || req["ordId"] != "555" {
			t.Fatalf("cancel request=%+v", req)
		}
		return `{"code":"0","msg":"","data":[{"ordId":"555","sCode":"0"}]}`, 200
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock(), "")

	if err := exec.Cancel(context.Background(), inst.ID, "555"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestOKXSpotOpenOrdersTranslation(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v5/trade/orders-pending" {
			t.Fatalf("request=%s %s, want GET /api/v5/trade/orders-pending", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("instType") != instTypeSpot {
			t.Fatalf("query=%s, want instType SPOT", q.Encode())
		}
		return `{"code":"0","msg":"","data":[{"instId":"ETH-USDT","instType":"SPOT","ordId":"777","clOrdId":"c-open","state":"live","side":"sell","ordType":"limit","sz":"0.0200","px":"3200.00","accFillSz":"0","avgPx":"","uTime":"1700000000000"}]}`, 200
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock(), "")

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 {
		t.Fatalf("orders len=%d", len(mass.OrderReports))
	}
	order := mass.OrderReports["777"].Order
	if order.Request.InstrumentID != inst.ID || order.VenueOrderID != "777" || order.Request.Side != enums.SideSell {
		t.Fatalf("order=%+v", order)
	}
}

func TestOKXSpotAccountBalancesTranslation(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s, want GET", r.Method)
		}
		switch r.URL.Path {
		case "/api/v5/account/balance":
			return `{"code":"0","msg":"","data":[{"uTime":"1700000000000","details":[{"ccy":"USDT","eq":"102.75","availBal":"100.5","frozenBal":"2.25","uTime":"1700000000001"},{"ccy":"ETH","cashBal":"0.4","availBal":"0.3","frozenBal":"0.1","uTime":"1700000000001"}]}]}`, 200
		case "/api/v5/account/config":
			return `{"code":"0","msg":"","data":[{"acctLv":"1","posMode":"net_mode","mgnIsoMode":"automatic","spotOffsetType":"","enableSpotBorrow":false}]}`, 200
		default:
			t.Fatalf("request=%s %s, want account balance/config", r.Method, r.URL.Path)
			return "", 0
		}
	})
	acct := newAccountClient(rest, testProvider(inst), clock.NewRealClock())

	bals, err := acct.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(bals) != 2 {
		t.Fatalf("balances len=%d", len(bals))
	}
	if bals[0].Currency != "USDT" || !bals[0].Available.Equal(d("100.5")) || !bals[0].Locked.Equal(d("2.25")) || !bals[0].Total.Equal(d("102.75")) {
		t.Fatalf("balance[0]=%+v", bals[0])
	}
	if !bals[0].Free.Equal(d("100.5")) {
		t.Fatalf("balance[0].Free=%s, want 100.5", bals[0].Free)
	}
	if !bals[0].CashInvariantOK() || !bals[1].CashInvariantOK() {
		t.Fatalf("spot balances must satisfy cash invariant: %+v", bals)
	}
	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.AccountID != model.AccountIDOKXSpot || state.Type != model.AccountCash || state.ModeInfo.AccountMode != string(okx.AccountLevelSimple) {
		t.Fatalf("account state identity/mode=%+v", state)
	}
	if !state.ModeInfo.Verified || state.ModeInfo.Source != "GET /api/v5/account/balance + GET /api/v5/account/config" {
		t.Fatalf("account mode not verified: %+v", state.ModeInfo)
	}
	if len(state.ModeInfo.ProductScope) != 1 || state.ModeInfo.ProductScope[0] != enums.KindSpot {
		t.Fatalf("product scope=%v, want spot", state.ModeInfo.ProductScope)
	}
	if state.TsEvent.UnixMilli() != 1700000000001 {
		t.Fatalf("TsEvent=%s, want latest balance detail uTime", state.TsEvent)
	}
	if got := state.Balances[0].Free; !got.Equal(d("100.5")) {
		t.Fatalf("state balance free=%s, want 100.5", got)
	}
	pos, err := acct.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(pos) != 0 {
		t.Fatalf("spot positions len=%d, want 0", len(pos))
	}
	if err := acct.SetLeverage(context.Background(), inst.ID, d("2")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetLeverage err=%v, want ErrNotSupported", err)
	}
	if err := acct.SetMarginMode(context.Background(), inst.ID, "cross"); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetMarginMode err=%v, want ErrNotSupported", err)
	}
}

func TestOKXSpotOrderUpdateTranslation(t *testing.T) {
	const golden = `{
	  "instId":"ETH-USDT","instType":"SPOT","ordId":"312269865356374016","clOrdId":"c-okx-spot-1",
	  "side":"buy","ordType":"limit","state":"filled",
	  "sz":"0.01","px":"3000","accFillSz":"0.01","avgPx":"3000",
	  "fillPx":"3000","fillSz":"0.01","fillTime":"1700000000123","tradeId":"99",
	  "execType":"T","fee":"-0.003","feeCcy":"USDT","uTime":"1700000000123"
	}`
	var o okx.Order
	if err := json.Unmarshal([]byte(golden), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events := execEventsFromOrder(&o, testProvider(testSpotInstrument()))
	if len(events) != 2 {
		t.Fatalf("want OrderEvent+FillEvent, got %d", len(events))
	}
	oe := events[0].(contract.OrderEvent)
	if oe.Order.Request.PositionSide != enums.PosNet || oe.Order.Request.ReduceOnly {
		t.Fatalf("spot order leaked derivative fields: %+v", oe.Order.Request)
	}
	fe := events[1].(contract.FillEvent)
	if fe.Fill.TradeID != "99" || !fe.Fill.Price.Equal(d("3000")) || !fe.Fill.Fee.Equal(d("0.003")) {
		t.Fatalf("fill=%+v", fe.Fill)
	}
}

func TestOKXSpotContractCapabilities(t *testing.T) {
	inst := testSpotInstrument()
	provider := testProvider(inst)
	restOnly := newMarketDataClient(nil, nil, provider, clock.NewRealClock())
	acct := newAccountClient(testREST(func(r *http.Request) (string, int) {
		switch r.URL.Path {
		case "/api/v5/account/balance":
			return `{"code":"0","msg":"","data":[{"details":[]}]}`, 200
		case "/api/v5/account/config":
			return `{"code":"0","msg":"","data":[{"acctLv":"1","posMode":"net_mode"}]}`, 200
		default:
			t.Fatalf("unexpected account capability request: %s", r.URL.Path)
			return "", 0
		}
	}), provider, clock.NewRealClock())
	if caps := acct.Capabilities(); !caps.Reports.AccountStateSnapshots || caps.Streaming.AccountState {
		t.Fatalf("account state capability flags=%+v, want report snapshot true and stream false", caps)
	}

	contracttest.RunSpotCapabilitySuite(t, contracttest.SpotCapabilitySuite{
		Venue: "OKX",
		Market: contracttest.MarketCapabilities{
			OrderBook:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo data tests")},
			Bars:            contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo data tests")},
			SubscribeBook:   contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and websocket tests")},
			SubscribeQuotes: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and websocket tests")},
			SubscribeTrades: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and websocket tests")},
			Reconnect:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and websocket tests")},
			RESTOnlyStreams: contracttest.CapabilityProbe{Support: contracttest.Unsupported("REST-only OKX Spot client has no market websocket"), Probe: func(ctx context.Context) error {
				return restOnly.SubscribeTrades(ctx, inst.ID)
			}},
			RESTOnlyReconnect: contracttest.CapabilityProbe{Support: contracttest.Unsupported("REST-only OKX Spot client has no market websocket"), Probe: func(ctx context.Context) error {
				return restOnly.Reconnect(ctx)
			}},
		},
		Execution: contracttest.ExecutionCapabilities{
			Submit:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			Cancel:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			CancelAll:  contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			Modify:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			OpenOrders: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			MassStatus: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
		},
		Account: contracttest.AccountCapabilities{
			AccountState: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				state, err := acct.AccountState(ctx)
				if err != nil {
					return err
				}
				if err := state.Validate(); err != nil {
					return err
				}
				return state.ModeInfo.ValidateVerified()
			}},
			Balances: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo account tests")},
			Positions: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				positions, err := acct.Positions(ctx)
				if err != nil {
					return err
				}
				if len(positions) != 0 {
					return errors.New("spot account returned positions")
				}
				return nil
			}},
			SetLeverage: contracttest.CapabilityProbe{Support: contracttest.Unsupported("spot cash account has no leverage"), Probe: func(ctx context.Context) error {
				return acct.SetLeverage(ctx, inst.ID, d("2"))
			}},
			SetCrossMargin: contracttest.CapabilityProbe{Support: contracttest.Unsupported("spot cash account has no cross margin mode"), Probe: func(ctx context.Context) error {
				return acct.SetMarginMode(ctx, inst.ID, "cross")
			}},
			SetIsolatedMargin: contracttest.CapabilityProbe{Support: contracttest.Unsupported("spot cash account has no isolated margin mode"), Probe: func(ctx context.Context) error {
				return acct.SetMarginMode(ctx, inst.ID, "isolated")
			}},
		},
	})
}

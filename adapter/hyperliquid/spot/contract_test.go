package spot

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

	hlaccount "github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/account"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/cloid"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/contract/contracttest"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
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

func testSpotID() model.InstrumentID {
	return model.InstrumentID{Venue: venueName, Symbol: "PURR-USDC", Kind: enums.KindSpot}
}

func TestHyperliquidSpotGTXMapsToPostOnlyAlo(t *testing.T) {
	tif, err := tifToHL(enums.TifGTX)
	if err != nil {
		t.Fatalf("tifToHL(GTX): %v", err)
	}
	if tif != sdk.TifAlo {
		t.Fatalf("tifToHL(GTX)=%q, want %q", tif, sdk.TifAlo)
	}
}

func TestHyperliquidSpotRejectsUnsupportedFOKBeforeTransport(t *testing.T) {
	if tif, err := tifToHL(enums.TifFOK); tif != "" || !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("tifToHL(FOK)=%q err=%v, want fail-closed ErrNotSupported", tif, err)
	}
}

func TestHyperliquidSpotMapsDocumentedRejectedStatuses(t *testing.T) {
	for _, status := range []string{
		"badAloPxRejected", "iocCancelRejected", "badTriggerPxRejected",
		"marketOrderNoLiquidityRejected", "insufficientSpotBalanceRejected",
	} {
		if got := statusFromHL(status); got != enums.StatusRejected {
			t.Errorf("statusFromHL(%q)=%s, want REJECTED", status, got)
		}
	}
	if got := statusFromHL("futureRejectedStatus"); got != enums.StatusUnknown {
		t.Fatalf("future unknown status=%s, want UNKNOWN", got)
	}
}

func testProvider(t *testing.T) *instruments.Registry {
	t.Helper()
	meta := loadSpotMetaFixture(t)
	insts, err := instruments.BuildSpotInstruments(meta)
	if err != nil {
		t.Fatalf("BuildSpotInstruments: %v", err)
	}
	return instruments.NewRegistry(insts...)
}

func testREST(handler func(*http.Request, []byte) (string, int)) *sdkspot.Client {
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
	return sdkspot.NewClient(base)
}

func TestHyperliquidSpotContractCapabilities(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(*http.Request, []byte) (string, int) { return `{}`, http.StatusOK })
	market := newMarketDataClient(rest, provider, clock.NewRealClock())
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)
	acct := newAccountClient(rest, clock.NewRealClock())

	contracttest.RunSpotCapabilitySuite(t, contracttest.SpotCapabilitySuite{
		Venue: venueName,
		Market: contracttest.MarketCapabilities{
			OrderBook:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Spot order book is available through /info l2Book")},
			Bars:            contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Spot candles are available through /info candleSnapshot")},
			SubscribeBook:   contracttest.CapabilityProbe{Support: contracttest.InventorySupported("websocket support is adapter-owned and covered by later runtime acceptance")},
			SubscribeQuotes: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("websocket support is adapter-owned and covered by later runtime acceptance")},
			SubscribeTrades: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("websocket support is adapter-owned and covered by later runtime acceptance")},
		},
		Execution: contracttest.ExecutionCapabilities{
			Submit:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Spot order action submission is supported")},
			Cancel:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Spot cancel action is supported")},
			CancelAll:  contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Spot cancel-all is synthesized from openOrders")},
			Modify:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Spot modify action is supported")},
			OpenOrders: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Spot openOrders is supported")},
		},
		Account: contracttest.AccountCapabilities{
			AccountState: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("combined Hyperliquid account state is translated from clearinghouseState and spotClearinghouseState")},
			Balances:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("Hyperliquid Spot clearinghouse balances are supported")},
			SetLeverage: contracttest.CapabilityProbe{
				Support: contracttest.Unsupported("spot cash accounts do not support leverage"),
				Probe: func(ctx context.Context) error {
					return acct.SetLeverage(ctx, testSpotID(), d("1"))
				},
			},
			SetCrossMargin: contracttest.CapabilityProbe{
				Support: contracttest.Unsupported("spot cash accounts do not support margin mode"),
				Probe: func(ctx context.Context) error {
					return acct.SetMarginMode(ctx, testSpotID(), "cross")
				},
			},
			SetIsolatedMargin: contracttest.CapabilityProbe{
				Support: contracttest.Unsupported("spot cash accounts do not support margin mode"),
				Probe: func(ctx context.Context) error {
					return acct.SetMarginMode(ctx, testSpotID(), "isolated")
				},
			},
		},
	})
	if market.Capabilities().Venue != venueName || exec.Capabilities().Venue != venueName || acct.Capabilities().Venue != venueName {
		t.Fatal("capabilities must declare Hyperliquid venue")
	}
	if caps := acct.Capabilities(); !caps.Reports.AccountStateSnapshots {
		t.Fatalf("account state snapshot capability=false, want true")
	}
}

func TestHyperliquidSpotAdapterPropagatesCanonicalAccountID(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req["type"] != "spotMeta" {
			t.Fatalf("unexpected request: %s", body)
		}
		data, err := os.ReadFile("../internal/instruments/testdata/spot_meta.json")
		if err != nil {
			t.Fatalf("read spot fixture: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(data)),
			Header:     make(http.Header),
		}, nil
	})}

	adapter, err := New(context.Background(), Config{
		AccountAddress: "0xABCDEF0000000000000000000000000000000000",
		Environment:    sdk.EnvironmentTestnet,
		RESTBaseURL:    "https://unit.test",
		HTTPClient:     httpClient,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer adapter.Close()

	const want = model.AccountIDHyperliquidDefault
	if adapter.acct.accountID != want {
		t.Fatalf("account client accountID=%q, want %q", adapter.acct.accountID, want)
	}
	if adapter.exec.accountID != want {
		t.Fatalf("execution client accountID=%q, want %q", adapter.exec.accountID, want)
	}
	if adapter.ws.AccountAddr != "0xABCDEF0000000000000000000000000000000000" {
		t.Fatalf("websocket account address=%q, want resolved API account address", adapter.ws.AccountAddr)
	}
}

func TestHyperliquidSpotAdapterUsesExplicitAccountID(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		data, err := os.ReadFile("../internal/instruments/testdata/spot_meta.json")
		if err != nil {
			t.Fatalf("read spot fixture: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(data)),
			Header:     make(http.Header),
		}, nil
	})}

	adapter, err := New(context.Background(), Config{
		AccountID:      "hl-custom",
		AccountAddress: "0xABCDEF0000000000000000000000000000000000",
		Environment:    sdk.EnvironmentTestnet,
		RESTBaseURL:    "https://unit.test",
		HTTPClient:     httpClient,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer adapter.Close()

	if adapter.acct.accountID != "hl-custom" || adapter.exec.accountID != "hl-custom" {
		t.Fatalf("adapter account ids acct=%q exec=%q, want explicit", adapter.acct.accountID, adapter.exec.accountID)
	}
}

func TestHyperliquidSpotAdapterRejectsEmptyAccountIdentity(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected HTTP request without account identity: %s", r.URL.Path)
		return nil, nil
	})}

	adapter, err := New(context.Background(), Config{
		Environment: sdk.EnvironmentTestnet,
		RESTBaseURL: "https://unit.test",
		HTTPClient:  httpClient,
	})
	if err == nil {
		adapter.Close()
		t.Fatal("New succeeded, want identity required")
	}
	if !errors.Is(err, hlaccount.ErrIdentityRequired) {
		t.Fatalf("err=%v, want ErrIdentityRequired", err)
	}
}

func TestHyperliquidSpotAdapterRejectsNonHexAccountAddress(t *testing.T) {
	const configuredAccount = "non-hex-account-alias"
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected HTTP request for invalid configured account: %s", r.URL.Path)
		return nil, nil
	})}

	adapter, err := New(context.Background(), Config{
		PrivateKey:     strings.Repeat("01", 32),
		AccountAddress: configuredAccount,
		Environment:    sdk.EnvironmentTestnet,
		RESTBaseURL:    "https://unit.test",
		HTTPClient:     httpClient,
	})
	if err == nil {
		adapter.Close()
		t.Fatal("New succeeded, want non-hex account rejection")
	}
	if !strings.Contains(err.Error(), "must be 0x address") {
		t.Fatalf("err=%v, want non-hex account rejection", err)
	}
}

func TestHyperliquidSpotAdapterResolvesAgentOwnerForConfiguredHexOwner(t *testing.T) {
	const owner = "0xabc0000000000000000000000000000000000000"
	sawUserRole := false
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		switch req["type"] {
		case "userRole":
			sawUserRole = true
			user, _ := req["user"].(string)
			if user == "" || user == owner || !strings.HasPrefix(user, "0x") {
				t.Fatalf("userRole user=%q, want derived signer address", user)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"role":"agent","data":{"user":"` + owner + `"}}`)), Header: make(http.Header)}, nil
		case "spotMeta":
			data, err := os.ReadFile("../internal/instruments/testdata/spot_meta.json")
			if err != nil {
				t.Fatalf("read spot fixture: %v", err)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
		default:
			t.Fatalf("unexpected request: %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}

	adapter, err := New(context.Background(), Config{
		PrivateKey:     strings.Repeat("01", 32),
		AccountAddress: owner,
		Environment:    sdk.EnvironmentTestnet,
		RESTBaseURL:    "https://unit.test",
		HTTPClient:     httpClient,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer adapter.Close()

	if !sawUserRole {
		t.Fatal("expected adapter to query userRole for signer")
	}
	if adapter.rest.AccountAddr != owner {
		t.Fatalf("rest account address=%q, want owner %q", adapter.rest.AccountAddr, owner)
	}
	const wantAccountID = model.AccountIDHyperliquidDefault
	if adapter.acct.accountID != wantAccountID || adapter.exec.accountID != wantAccountID {
		t.Fatalf("adapter account ids acct=%q exec=%q, want owner account id", adapter.acct.accountID, adapter.exec.accountID)
	}
}

func TestHyperliquidSpotAdapterRejectsConfiguredHexOwnerMismatch(t *testing.T) {
	const owner = "0xabc0000000000000000000000000000000000000"
	const configuredOwner = "0xdef0000000000000000000000000000000000000"
	sawUserRole := false
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		switch req["type"] {
		case "userRole":
			sawUserRole = true
			user, _ := req["user"].(string)
			if user == configuredOwner {
				t.Fatalf("userRole user=%q, want signer address before configured owner", user)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"role":"agent","data":{"user":"` + owner + `"}}`)), Header: make(http.Header)}, nil
		default:
			t.Fatalf("unexpected request: %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}

	adapter, err := New(context.Background(), Config{
		PrivateKey:     strings.Repeat("01", 32),
		AccountAddress: configuredOwner,
		Environment:    sdk.EnvironmentTestnet,
		RESTBaseURL:    "https://unit.test",
		HTTPClient:     httpClient,
	})
	if err == nil {
		adapter.Close()
		t.Fatal("New succeeded, want configured owner mismatch")
	}
	if !sawUserRole {
		t.Fatal("expected adapter to query userRole before rejecting mismatch")
	}
	if !strings.Contains(err.Error(), "does not match userRole owner") {
		t.Fatalf("err=%v, want configured owner mismatch", err)
	}
}

func TestHyperliquidSpotOrderBookTranslation(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/info" {
			t.Fatalf("request=%s %s, want POST /info", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req["type"] != "l2Book" || req["coin"] != "PURR/USDC" {
			t.Fatalf("unexpected l2Book request: %s", body)
		}
		return `{"coin":"PURR/USDC","levels":[[{"px":"1.01","sz":"2","n":1}],[{"px":"1.02","sz":"3","n":1}]],"time":1700000000000}`, 200
	})
	market := newMarketDataClient(rest, provider, clock.NewRealClock())

	book, err := market.OrderBook(context.Background(), testSpotID(), 5)
	if err != nil {
		t.Fatalf("OrderBook: %v", err)
	}
	if book.InstrumentID != testSpotID() || !book.Bids[0].Price.Equal(d("1.01")) || !book.Asks[0].Quantity.Equal(d("3")) {
		t.Fatalf("book=%+v", book)
	}
}

func TestHyperliquidSpotSubmitOrderRequestTranslation(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/exchange" {
			t.Fatalf("request=%s %s, want POST /exchange", r.Method, r.URL.Path)
		}
		action := decodeAction(t, body)
		orders := action["orders"].([]any)
		order := orders[0].(map[string]any)
		if action["builder"] != nil {
			t.Fatalf("first-phase testnet orders must not attach builder attribution: %s", body)
		}
		if action["type"] != "order" || int(order["a"].(float64)) != 10007 || order["b"] != true || order["p"] != "1.01" || order["s"] != "2" || order["r"] != false {
			t.Fatalf("unexpected order action: %s", body)
		}
		if order["c"] != cloid.ForClientID("c-spot-1") {
			t.Fatalf("cloid=%v, want mapped c-spot-1", order["c"])
		}
		return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":555,"cloid":"` + cloid.ForClientID("c-spot-1") + `","status":"open"}}]}}}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testSpotID(),
		ClientID:     "c-spot-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("2"),
		Price:        d("1.01"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if order.VenueOrderID != "555" || order.Status != enums.StatusNew || order.Request.AccountID != model.AccountIDHyperliquidDefault || order.Request.ClientID != "c-spot-1" || order.Request.PositionSide != enums.PosNet || order.Request.ReduceOnly {
		t.Fatalf("order=%+v", order)
	}
}

func TestHyperliquidSpotFilledSubmitDoesNotEmitSyntheticEconomicFill(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/exchange" {
			t.Fatalf("request=%s %s, want POST /exchange", r.Method, r.URL.Path)
		}
		return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"totalSz":"2","avgPx":"1.01","oid":777}}]}}}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)
	req := model.OrderRequest{
		InstrumentID: testSpotID(),
		ClientID:     "filled-submit",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     d("2"),
		Price:        d("1.01"),
	}

	for i := 0; i < 2; i++ {
		order, err := exec.Submit(context.Background(), req)
		if err != nil {
			t.Fatalf("Submit %d: %v", i+1, err)
		}
		if order.Status != enums.StatusFilled || !order.FilledQty.Equal(d("2")) || !order.AvgFillPrice.Equal(d("1.01")) {
			t.Fatalf("order=%+v, want synchronously filled", order)
		}
		select {
		case env := <-exec.Events():
			t.Fatalf("filled Submit emitted synthetic economic event: %+v", env)
		default:
		}
	}
}

func TestHyperliquidSpotFilledSubmitThenSnapshotEmitsOnlyAuthoritativeFeeBearingFill(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"totalSz":"2","avgPx":"1.01","oid":777}}]}}}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testSpotID(), ClientID: "private-fill", Side: enums.SideBuy,
		Type: enums.TypeLimit, TIF: enums.TifIOC, Quantity: d("2"), Price: d("1.01"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case env := <-exec.Events():
		t.Fatalf("active private fill stream received duplicate synthetic event: %+v", env)
	case <-time.After(50 * time.Millisecond):
	}

	for _, event := range execEventsFromUserFills(sdk.WsUserFills{
		IsSnapshot: true,
		Fills: []sdk.WsUserFill{{
			Coin: "PURR/USDC", Px: "1.01", Sz: "2", Side: "B", Time: 1700000000123,
			Oid: 777, Crossed: true, Fee: "-0.01", FeeToken: "USDC", Tid: 99,
		}},
	}, provider, model.AccountIDHyperliquidDefault) {
		exec.emit(event)
	}
	select {
	case env := <-exec.Events():
		fill, ok := env.Payload.(contract.FillEvent)
		if !ok || fill.Fill.TradeID != "99" || fill.Fill.ClientID != order.Request.ClientID || !fill.Fill.Fee.Equal(d("-0.01")) || fill.Fill.FeeCurrency != "USDC" || env.Source != contract.SourceAdapterStream || !env.Flags.Has(contract.EventFlagFromStream) {
			t.Fatalf("authoritative fill envelope=%+v", env)
		}
	case <-time.After(time.Second):
		t.Fatal("authoritative private fill was not emitted")
	}
	select {
	case env := <-exec.Events():
		t.Fatalf("snapshot produced duplicate economic fill: %+v", env)
	default:
	}
}

func TestHyperliquidSpotRuntimeCumulativeAckDoesNotDoubleCountSplitAuthoritativeFills(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.URL.Path == "/info" {
			return `[]`, http.StatusOK
		}
		return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"totalSz":"1","avgPx":"1.04","oid":777}}]}}}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)
	callbacks := make(chan model.Fill, 8)
	envelopes := make(chan contract.ExecEnvelope, 8)
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"hyperliquid-spot-cumulative-ack",
		btruntime.WithAccountID(model.AccountIDHyperliquidDefault),
		btruntime.WithOnFill(func(fill model.Fill) { callbacks <- fill }),
		btruntime.WithOnExecEnvelope(func(env contract.ExecEnvelope) { envelopes <- env }),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()
	if err := runtimeaccept.WaitForActive(ctx, node); err != nil {
		t.Fatalf("runtime active: %v", err)
	}

	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		AccountID: model.AccountIDHyperliquidDefault, InstrumentID: testSpotID(), ClientID: "partial-ack",
		Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifIOC,
		Quantity: d("2"), Price: d("1.1"), PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !order.FilledQty.Equal(d("1")) || order.Status != enums.StatusFilled {
		t.Fatalf("ack order=%+v, want terminal cumulative fill 1", order)
	}

	streamFills := sdk.WsUserFills{Fills: []sdk.WsUserFill{
		{Coin: "PURR/USDC", Px: "1", Sz: "0.6", Side: "B", Time: 1700000000100, Oid: 777, Crossed: true, Fee: "-0.01", FeeToken: "USDC", Tid: 91},
		{Coin: "PURR/USDC", Px: "1.1", Sz: "0.4", Side: "B", Time: 1700000000200, Oid: 777, Crossed: true, Fee: "0.02", FeeToken: "PURR", Tid: 92},
	}}
	emitFills := func(fills sdk.WsUserFills) {
		flags := contract.EventFlags(0)
		if fills.IsSnapshot {
			flags |= contract.EventFlagFromSnapshot
		}
		for _, event := range execEventsFromUserFills(fills, provider, model.AccountIDHyperliquidDefault) {
			exec.emitWithFlags(event, flags)
		}
	}
	emitFills(streamFills)
	for i := 0; i < 2; i++ {
		select {
		case fill := <-callbacks:
			if !fill.Quantity.IsPositive() {
				t.Fatalf("callback fill=%+v, want positive authoritative quantity", fill)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for authoritative callback %d: %v", i+1, ctx.Err())
		}
	}
	streamFills.IsSnapshot = true
	emitFills(streamFills)
	time.Sleep(50 * time.Millisecond)
	select {
	case fill := <-callbacks:
		t.Fatalf("duplicate snapshot fill reached callback: %+v", fill)
	default:
	}
	snapshotEnvelopes := 0
	for i := 0; i < 4; i++ {
		select {
		case env := <-envelopes:
			if env.Source != contract.SourceAdapterStream || !env.Flags.Has(contract.EventFlagFromStream) {
				t.Fatalf("runtime hook envelope=%+v, want adapter stream provenance", env.Meta())
			}
			if env.Flags.Has(contract.EventFlagFromSnapshot) {
				snapshotEnvelopes++
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for runtime envelope %d: %v", i+1, ctx.Err())
		}
	}
	if snapshotEnvelopes != 2 {
		t.Fatalf("snapshot envelopes=%d, want two duplicate snapshot tids", snapshotEnvelopes)
	}

	cached, ok := node.Cache.OrderForAccount(model.AccountIDHyperliquidDefault, order.Request.ClientID)
	if !ok || !cached.FilledQty.Equal(d("1")) || !cached.AvgFillPrice.Equal(d("1.04")) {
		t.Fatalf("cached order=%+v ok=%v, cumulative ack must not be incremented by real tids", cached, ok)
	}
	if got := node.Portfolio.NetQtyForAccount(model.AccountIDHyperliquidDefault, testSpotID(), enums.PosNet); !got.Equal(d("0.98")) {
		t.Fatalf("portfolio net base=%s, want 0.98 after 0.02 PURR fee", got)
	}
	if got := node.Portfolio.AvgPriceForAccount(model.AccountIDHyperliquidDefault, testSpotID(), enums.PosNet); !got.Equal(d("1.0612244897959184")) {
		t.Fatalf("portfolio avg price=%s, want fee-adjusted split-fill cost", got)
	}
	fees := node.Portfolio.FeesByCurrencyForAccount(model.AccountIDHyperliquidDefault)
	if !fees["USDC"].Equal(d("-0.01")) || !fees["PURR"].Equal(d("0.02")) {
		t.Fatalf("fees=%v, want signed quote rebate and base fee once", fees)
	}
	if metrics := node.Metrics(); metrics.FillsSeen != 2 {
		t.Fatalf("runtime fill counter=%d, want 2 unique authoritative tids", metrics.FillsSeen)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")
	}
}

func TestHyperliquidSpotSubmitRejectsMismatchedAccountID(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		t.Fatalf("unexpected REST call for mismatched account id: %s", body)
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	_, err := exec.Submit(context.Background(), model.OrderRequest{
		AccountID:    "HYPERLIQUID-OTHER",
		InstrumentID: testSpotID(),
		ClientID:     "c-spot-mismatch",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("2"),
		Price:        d("1.01"),
	})
	if err == nil || !strings.Contains(err.Error(), "account id") {
		t.Fatalf("Submit err=%v, want account id mismatch", err)
	}
}

func TestHyperliquidSpotSubmitMapsExplicitVenueRejection(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"Insufficient spot balance"}]}}}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testSpotID(), ClientID: "rejected-spot", Side: enums.SideBuy,
		Type: enums.TypeLimit, TIF: enums.TifIOC, Quantity: d("2"), Price: d("1.01"),
	})
	if order != nil || !errors.Is(err, contract.ErrVenueRejected) || !errors.Is(err, sdk.ErrOrderRejected) || !strings.Contains(err.Error(), "Insufficient spot balance") {
		t.Fatalf("order=%+v err=%v, want preserved typed venue rejection", order, err)
	}
}

func TestHyperliquidSpotExactStatusByClientAndFillHistory(t *testing.T) {
	provider := testProvider(t)
	const clientID = "exact-spot-client"
	venueCloid := cloid.ForClientID(clientID)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req["type"] {
		case "orderStatus":
			if req["oid"] != venueCloid {
				t.Fatalf("orderStatus oid=%v, want cloid %s", req["oid"], venueCloid)
			}
			return `{"status":"order","order":{"order":{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"0.5","oid":777,"cloid":"` + venueCloid + `","timestamp":1700000000000,"origSz":"2"},"status":"canceled","statusTimestamp":1700000001000}}`, http.StatusOK
		case "userFills":
			return `[{"coin":"PURR/USDC","px":"1.01","sz":"0.75","side":"B","time":1700000000200,"oid":777,"crossed":true,"fee":"-0.001","feeToken":"USDC","tid":41},{"coin":"PURR/USDC","px":"1.02","sz":"0.75","side":"B","time":1700000000300,"oid":777,"crossed":false,"fee":"0.002","feeToken":"USDC","tid":42},{"coin":"PURR/USDC","px":"1","sz":"9","side":"B","time":1700000000300,"oid":999,"crossed":false,"fee":"0","feeToken":"USDC","tid":43}]`, http.StatusOK
		default:
			t.Fatalf("unexpected info request: %s", body)
		}
		return `{}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID: model.AccountIDHyperliquidDefault, InstrumentID: testSpotID(), ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport: %v", err)
	}
	if report == nil || report.Order.Request.ClientID != clientID || report.Order.VenueOrderID != "777" || report.Order.Status != enums.StatusCanceled || !report.Order.FilledQty.Equal(d("1.5")) {
		t.Fatalf("report=%+v", report)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{
		AccountID: model.AccountIDHyperliquidDefault, InstrumentID: testSpotID(), VenueOrderID: "777",
	})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(fills) != 2 || fills[0].Fill.ClientID != clientID || fills[0].Fill.TradeID != "41" || !fills[0].Fill.Fee.Equal(d("-0.001")) || fills[1].Fill.Liquidity != enums.LiqMaker {
		t.Fatalf("fills=%+v", fills)
	}
}

func TestHyperliquidSpotLifecycleRecoversFullyFilledLostSubmitByCloid(t *testing.T) {
	provider := testProvider(t)
	var orderCalls int
	var openingCloid string
	var queriedByCloid bool
	var queriedFillsByVenue bool
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		switch r.URL.Path {
		case "/exchange":
			action := decodeAction(t, body)
			switch action["type"] {
			case "cancel":
				return `{"status":"ok","response":{"type":"cancel","data":{"statuses":["success"]}}}`, http.StatusOK
			case "order":
				orderCalls++
				order := action["orders"].([]any)[0].(map[string]any)
				switch orderCalls {
				case 1:
					return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":501,"cloid":"` + order["c"].(string) + `","status":"open"}}]}}}`, http.StatusOK
				case 2:
					openingCloid = order["c"].(string)
					return `{"status":"err","response":"submit response lost after venue fill"}`, http.StatusServiceUnavailable
				case 3:
					return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"totalSz":"2","avgPx":"1.02","oid":503}}]}}}`, http.StatusOK
				default:
					t.Fatalf("unexpected order call %d: %s", orderCalls, body)
				}
			default:
				t.Fatalf("unexpected exchange action: %s", body)
			}
		case "/info":
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode info request: %v", err)
			}
			switch req["type"] {
			case "frontendOpenOrders":
				return `[]`, http.StatusOK
			case "orderStatus":
				switch oid := req["oid"].(type) {
				case string:
					if oid != openingCloid {
						t.Fatalf("orderStatus cloid=%q, want %q", oid, openingCloid)
					}
					queriedByCloid = true
					return `{"status":"order","order":{"order":{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"0","oid":502,"cloid":"` + openingCloid + `","timestamp":1700000000000,"origSz":"2"},"status":"filled","statusTimestamp":1700000001000}}`, http.StatusOK
				case float64:
					if int64(oid) != 501 {
						t.Fatalf("unexpected numeric orderStatus oid=%v", oid)
					}
					return `{"status":"order","order":{"order":{"coin":"PURR/USDC","side":"B","limitPx":"0.5","sz":"2","oid":501,"timestamp":1700000000000,"origSz":"2"},"status":"canceled","statusTimestamp":1700000001000}}`, http.StatusOK
				default:
					t.Fatalf("unexpected orderStatus oid=%T(%v)", req["oid"], req["oid"])
				}
			case "userFills":
				if openingCloid != "" {
					queriedFillsByVenue = true
					return `[{"coin":"PURR/USDC","px":"1.01","sz":"2","side":"B","time":1700000000200,"oid":502,"crossed":true,"fee":"0.001","feeToken":"USDC","tid":41}]`, http.StatusOK
				}
				return `[]`, http.StatusOK
			default:
				t.Fatalf("unexpected info request: %s", body)
			}
		default:
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		return `{}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)
	spec := runtimeaccept.OrderLifecycleSpec{
		Label: "hyperliquid spot lost submit", Venue: venueName, AccountID: model.AccountIDHyperliquidDefault,
		InstrumentID: testSpotID(), Quantity: d("2"), CloseQuantity: d("2"),
		RestingPrice: d("0.5"), FillPrice: d("1.01"), ClosePrice: d("1.02"),
		PositionSide: enums.PosNet, CloseAfterFill: true, PollInterval: time.Millisecond, CleanupTimeout: 100 * time.Millisecond,
	}

	result, err := runtimeaccept.RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(d("2")) || !result.ClosedQty.Equal(d("2")) || result.Filled.VenueOrderID != "502" || result.Filled.Request.TIF != enums.TifIOC || result.Closed.Request.TIF != enums.TifIOC || result.Closed.Request.ReduceOnly {
		t.Fatalf("result=%+v, want recovered opening oid=502 and full close", result)
	}
	if openingCloid != cloid.ForClientID(result.Filled.Request.ClientID) || !queriedByCloid || !queriedFillsByVenue || orderCalls != 3 {
		t.Fatalf("cloid=%q client=%q queriedStatus=%v queriedFills=%v orderCalls=%d", openingCloid, result.Filled.Request.ClientID, queriedByCloid, queriedFillsByVenue, orderCalls)
	}
}

func TestHyperliquidSpotUnknownExactStatusDoesNotRememberZeroIdentity(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		return `{"status":"unknownOid"}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID: model.AccountIDHyperliquidDefault, InstrumentID: testSpotID(), VenueOrderID: "777",
	})
	if err != nil || report != nil || len(exec.orders) != 0 {
		t.Fatalf("report=%+v err=%v orders=%v, want authoritative absence without remembered zero order", report, err, exec.orders)
	}
}

func TestHyperliquidSpotOrderUpdatesPreservePostOnlyAndCumulativeFill(t *testing.T) {
	provider := testProvider(t)
	reduceOnly := false
	events := execEventsFromOrderUpdate(sdk.WsOrderUpdate{Order: sdk.WsOrder{
		Coin: "PURR/USDC", Side: "B", LimitPx: "1.01", Sz: "0.5", OrigSz: "2", Oid: 555,
		Cliod: "c-spot-1", ReduceOnly: &reduceOnly, OrderType: "Limit", Tif: "Alo",
	}, Status: sdk.StatusOpen}, provider, model.AccountIDHyperliquidDefault)
	if len(events) != 1 {
		t.Fatalf("events=%+v", events)
	}
	order := events[0].(contract.OrderEvent).Order
	if order.Request.Type != enums.TypeLimit || order.Request.TIF != enums.TifGTX || order.Request.ReduceOnly || !order.FilledQty.Equal(d("1.5")) {
		t.Fatalf("order=%+v", order)
	}
	if malformed := execEventsFromOrderUpdate(sdk.WsOrderUpdate{Order: sdk.WsOrder{
		Coin: "PURR/USDC", Side: "B", LimitPx: "1.01", Sz: "3", OrigSz: "2", Oid: 555,
	}, Status: sdk.StatusOpen}, provider, model.AccountIDHyperliquidDefault); len(malformed) != 0 {
		t.Fatalf("malformed update emitted=%+v", malformed)
	}
}

func TestHyperliquidSpotRememberOrderDoesNotDowngradeKnownSemantics(t *testing.T) {
	exec := newExecutionClient(nil, testProvider(t), clock.NewRealClock(), model.AccountIDHyperliquidDefault)
	known := model.Order{Request: model.OrderRequest{
		AccountID: model.AccountIDHyperliquidDefault, InstrumentID: testSpotID(), ClientID: "runtime-client",
		Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifIOC, Quantity: d("2"), Price: d("1.01"), PositionSide: enums.PosNet,
	}, VenueOrderID: "555", Status: enums.StatusNew}
	exec.rememberOrder(known)
	update := model.Order{Request: model.OrderRequest{
		AccountID: model.AccountIDHyperliquidDefault, InstrumentID: testSpotID(), Side: enums.SideBuy,
		Quantity: d("2"), Price: d("1.01"), PositionSide: enums.PosNet,
	}, VenueOrderID: "555", Status: enums.StatusFilled, FilledQty: d("2")}
	merged := exec.rememberOrder(update)
	if merged.Request.ClientID != "runtime-client" || merged.Request.Type != enums.TypeLimit || merged.Request.TIF != enums.TifIOC {
		t.Fatalf("merged=%+v, sparse WS update downgraded submitted semantics", merged)
	}
}

func TestHyperliquidSpotExactStatusRejectsMismatchedClientAndVenueIdentity(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		return `{"status":"order","order":{"order":{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"2","oid":777,"cloid":"` + cloid.ForClientID("other-client") + `","timestamp":1700000000000,"origSz":"2"},"status":"open","statusTimestamp":1700000001000}}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID: model.AccountIDHyperliquidDefault, InstrumentID: testSpotID(),
		ClientID: "expected-client", VenueOrderID: "777",
	})
	if report != nil || err == nil || !strings.Contains(err.Error(), "client identity mismatch") {
		t.Fatalf("report=%+v err=%v, want identity mismatch", report, err)
	}
	if len(exec.orders) != 0 || exec.ids.ClientID("", "777") != "" {
		t.Fatalf("mismatched identity polluted order map: orders=%v mapped=%q", exec.orders, exec.ids.ClientID("", "777"))
	}
}

func TestHyperliquidSpotCancelModifyAndOpenOrders(t *testing.T) {
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
				if int(cancel["a"].(float64)) != 10007 || int64(cancel["o"].(float64)) != 555 {
					t.Fatalf("unexpected cancel action: %s", body)
				}
				return `{"status":"ok","response":{"type":"cancel","data":{"statuses":["success"]}}}`, 200
			case "modify":
				sawModify = true
				order := action["order"].(map[string]any)
				if int64(action["oid"].(float64)) != 555 || order["b"] != true || order["p"] != "1.02" || order["s"] != "3" || order["c"] != cloid.ForClientID("c-spot-1") {
					t.Fatalf("unexpected modify action: %s", body)
				}
				limit := order["t"].(map[string]any)["limit"].(map[string]any)
				if limit["tif"] != "Alo" {
					t.Fatalf("modify lost post-only TIF: %s", body)
				}
				return `{"status":"ok","response":{"type":"modify","data":{"statuses":[{"resting":{"oid":555,"cloid":"` + cloid.ForClientID("c-spot-1") + `","status":"open"}}]}}}`, 200
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
				return `{"status":"order","order":{"order":{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"2","oid":555,"cloid":"` + cloid.ForClientID("c-spot-1") + `","timestamp":1700000000000,"origSz":"2","reduceOnly":false,"orderType":"Limit","tif":"Alo","isTrigger":false,"triggerPx":"0"},"status":"open","statusTimestamp":1700000000000}}`, 200
			case "frontendOpenOrders":
				openCalls++
				return `[{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"2","oid":555,"cloid":"c-spot-1","timestamp":1700000000000,"origSz":"2","reduceOnly":false,"orderType":"Limit","tif":"Gtc"}]`, 200
			default:
				t.Fatalf("unexpected info request: %s", body)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	if err := exec.Cancel(context.Background(), testSpotID(), "555"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := exec.Modify(context.Background(), testSpotID(), "555", d("1.02"), d("3")); err != nil {
		t.Fatalf("Modify: %v", err)
	}
	open, err := exec.OpenOrders(context.Background(), testSpotID())
	if err != nil {
		t.Fatalf("OpenOrders: %v", err)
	}
	if !sawCancel || !sawModify || openCalls == 0 {
		t.Fatalf("expected cancel/modify/open paths, got cancel=%v modify=%v openCalls=%d", sawCancel, sawModify, openCalls)
	}
	if len(open) != 1 || open[0].VenueOrderID != "555" || open[0].Request.AccountID != model.AccountIDHyperliquidDefault || open[0].Request.ClientID != cloid.ForClientID("c-spot-1") {
		t.Fatalf("open orders=%+v", open)
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if !mass.Partial || mass.AccountID != model.AccountIDHyperliquidDefault || len(mass.OrderReports) != 1 {
		t.Fatalf("mass=%+v", mass)
	}
	for _, report := range mass.OrderReports {
		if report.AccountID != model.AccountIDHyperliquidDefault || report.Order.Request.AccountID != model.AccountIDHyperliquidDefault {
			t.Fatalf("mass report account ids report=%q order=%q", report.AccountID, report.Order.Request.AccountID)
		}
	}
	if openCalls != 2 {
		t.Fatalf("frontendOpenOrders calls=%d, want OpenOrders + single mass-status snapshot", openCalls)
	}
}

func TestHyperliquidSpotModifyFailsClosedForUnreconstructableTrigger(t *testing.T) {
	provider := testProvider(t)
	exchangeCalls := 0
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.URL.Path == "/exchange" {
			exchangeCalls++
			return `{}`, 500
		}
		return `{"status":"order","order":{"order":{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"2","oid":555,"timestamp":1700000000000,"origSz":"2","reduceOnly":false,"orderType":"Stop Market","isTrigger":true,"triggerPx":"0.9"},"status":"open","statusTimestamp":1700000000000}}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)
	order, err := exec.Modify(context.Background(), testSpotID(), "555", d("1.02"), decimal.Zero)
	if order != nil || !errors.Is(err, contract.ErrNotSupported) || exchangeCalls != 0 {
		t.Fatalf("order=%+v err=%v exchangeCalls=%d, want fail-closed before transport", order, err, exchangeCalls)
	}
}

func TestHyperliquidSpotOpenOrdersRestoresRuntimeClientIDFromMappedCloid(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		switch r.URL.Path {
		case "/exchange":
			return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":555,"cloid":"` + cloid.ForClientID("runtime-client-1") + `","status":"open"}}]}}}`, 200
		case "/info":
			return `[{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"2","oid":555,"cloid":"` + cloid.ForClientID("runtime-client-1") + `","timestamp":1700000000000,"origSz":"2"}]`, 200
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	_, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testSpotID(),
		ClientID:     "runtime-client-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("2"),
		Price:        d("1.01"),
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	open, err := exec.OpenOrders(context.Background(), testSpotID())
	if err != nil {
		t.Fatalf("OpenOrders: %v", err)
	}
	if len(open) != 1 || open[0].Request.ClientID != "runtime-client-1" {
		t.Fatalf("open orders=%+v, want original runtime client id", open)
	}
}

func TestHyperliquidSpotCancelEmitsCanceledOrderEvent(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		switch r.URL.Path {
		case "/exchange":
			action := decodeAction(t, body)
			switch action["type"] {
			case "order":
				return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":555,"cloid":"` + cloid.ForClientID("c-spot-1") + `","status":"open"}}]}}}`, 200
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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testSpotID(),
		ClientID:     "c-spot-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("2"),
		Price:        d("1.01"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := exec.Cancel(context.Background(), testSpotID(), order.VenueOrderID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case env := <-exec.Events():
		oe, ok := env.Payload.(contract.OrderEvent)
		if !ok {
			t.Fatalf("event=%T, want OrderEvent", env.Payload)
		}
		if oe.Order.Status != enums.StatusCanceled || oe.Order.Request.AccountID != model.AccountIDHyperliquidDefault || oe.Order.Request.ClientID != "c-spot-1" || oe.Order.VenueOrderID != "555" {
			t.Fatalf("order event=%+v", oe.Order)
		}
		if env.Source != contract.SourceAdapterREST || !env.Flags.Has(contract.EventFlagSynthetic) {
			t.Fatalf("event meta source=%s flags=%v, want REST synthetic", env.Source, env.Flags)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancel OrderEvent")
	}
}

func TestHyperliquidSpotBalancesAndMarginOps(t *testing.T) {
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/info" {
			t.Fatalf("request=%s %s, want POST /info", r.Method, r.URL.Path)
		}
		return `{"balances":[{"coin":"USDC","token":0,"hold":"1.5","total":"10","entryNtl":"0"}]}`, 200
	})
	acct := newAccountClient(rest, clock.NewRealClock(), model.AccountIDHyperliquidDefault)

	balances, err := acct.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(balances) != 1 || balances[0].AccountID != model.AccountIDHyperliquidDefault || balances[0].Currency != "USDC" || !balances[0].Total.Equal(d("10")) || !balances[0].Available.Equal(d("8.5")) || !balances[0].Locked.Equal(d("1.5")) {
		t.Fatalf("balances=%+v", balances)
	}
	if err := acct.SetLeverage(context.Background(), testSpotID(), d("2")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetLeverage err=%v, want ErrNotSupported", err)
	}
	if err := acct.SetMarginMode(context.Background(), testSpotID(), "cross"); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetMarginMode err=%v, want ErrNotSupported", err)
	}
}

func TestHyperliquidSpotAccountStateReporterCombinesPerpAndSpot(t *testing.T) {
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/info" {
			t.Fatalf("request=%s %s, want POST /info", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		switch req["type"] {
		case "userAbstraction":
			if req["user"] != "0xabc" {
				t.Fatalf("userAbstraction user=%v, want 0xabc", req["user"])
			}
			return `"unifiedAccount"`, 200
		case "clearinghouseState":
			return `{"assetPositions":[],"crossMarginSummary":{"accountValue":"100","totalMarginUsed":"12","totalNtlPos":"50","totalRawUsd":"100"},"marginSummary":{"accountValue":"100","totalMarginUsed":"12","totalNtlPos":"50","totalRawUsd":"100"},"time":1700000000000,"withdrawable":"88","crossMaintenanceMarginUsed":"7"}`, 200
		case "spotClearinghouseState":
			return `{"balances":[{"coin":"USDC","token":0,"hold":"1.5","total":"10","entryNtl":"0"},{"coin":"PURR","token":1,"hold":"0.5","total":"2","entryNtl":"0"}]}`, 200
		default:
			t.Fatalf("unexpected request: %s", body)
		}
		return `{}`, 200
	})
	acct := newAccountClient(rest, clock.NewSimulatedClock(time.Unix(1700000000, 0)), model.AccountIDHyperliquidDefault)

	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.AccountID != model.AccountIDHyperliquidDefault || state.Type != model.AccountMargin || !state.Reported || state.EventID == "" || state.TsEvent.IsZero() || state.TsInit.IsZero() {
		t.Fatalf("state=%+v", state)
	}
	if len(state.Balances) != 2 || state.Balances[0].Currency != "USDC" || !state.Balances[0].Free.Equal(d("8.5")) || state.Balances[1].Currency != "PURR" {
		t.Fatalf("balances=%+v", state.Balances)
	}
	if len(state.Margins) != 1 || !state.Margins[0].Initial.Equal(d("12")) {
		t.Fatalf("margins=%+v", state.Margins)
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

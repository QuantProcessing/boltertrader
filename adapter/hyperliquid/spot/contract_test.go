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
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/contract/contracttest"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), "HYPERLIQUID:0xabc")
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

	const want = "HYPERLIQUID:0xabcdef0000000000000000000000000000000000"
	if adapter.acct.accountID != want {
		t.Fatalf("account client accountID=%q, want %q", adapter.acct.accountID, want)
	}
	if adapter.exec.accountID != want {
		t.Fatalf("execution client accountID=%q, want %q", adapter.exec.accountID, want)
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
	const wantAccountID = "HYPERLIQUID:0xabc0000000000000000000000000000000000000"
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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), "HYPERLIQUID:0xabc")

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
	if order.VenueOrderID != "555" || order.Status != enums.StatusNew || order.Request.AccountID != "HYPERLIQUID:0xabc" || order.Request.ClientID != "c-spot-1" || order.Request.PositionSide != enums.PosNet || order.Request.ReduceOnly {
		t.Fatalf("order=%+v", order)
	}
}

func TestHyperliquidSpotSubmitRejectsMismatchedAccountID(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		t.Fatalf("unexpected REST call for mismatched account id: %s", body)
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), "HYPERLIQUID:0xabc")

	_, err := exec.Submit(context.Background(), model.OrderRequest{
		AccountID:    "HYPERLIQUID:0xdef",
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
				if int64(action["oid"].(float64)) != 555 || order["b"] != true || order["p"] != "1.02" || order["s"] != "3" {
					t.Fatalf("unexpected modify action: %s", body)
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
				return `{"order":{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"2","oid":555,"timestamp":1700000000000,"origSz":"2","status":"open","filledSz":"0","avgPx":"0"}}`, 200
			case "openOrders":
				openCalls++
				return `[{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"2","oid":555,"cloid":"c-spot-1","timestamp":1700000000000,"origSz":"2"}]`, 200
			default:
				t.Fatalf("unexpected info request: %s", body)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), "HYPERLIQUID:0xabc")

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
	if len(open) != 1 || open[0].VenueOrderID != "555" || open[0].Request.AccountID != "HYPERLIQUID:0xabc" || open[0].Request.ClientID != cloid.ForClientID("c-spot-1") {
		t.Fatalf("open orders=%+v", open)
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if !mass.Partial || mass.AccountID != "HYPERLIQUID:0xabc" || len(mass.OrderReports) != 1 {
		t.Fatalf("mass=%+v", mass)
	}
	for _, report := range mass.OrderReports {
		if report.AccountID != "HYPERLIQUID:0xabc" || report.Order.Request.AccountID != "HYPERLIQUID:0xabc" {
			t.Fatalf("mass report account ids report=%q order=%q", report.AccountID, report.Order.Request.AccountID)
		}
	}
	if openCalls != 2 {
		t.Fatalf("openOrders calls=%d, want OpenOrders + single mass-status snapshot", openCalls)
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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), "HYPERLIQUID:0xabc")

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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), "HYPERLIQUID:0xabc")

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
		if oe.Order.Status != enums.StatusCanceled || oe.Order.Request.AccountID != "HYPERLIQUID:0xabc" || oe.Order.Request.ClientID != "c-spot-1" || oe.Order.VenueOrderID != "555" {
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
	acct := newAccountClient(rest, clock.NewRealClock(), "HYPERLIQUID:0xabc")

	balances, err := acct.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(balances) != 1 || balances[0].AccountID != "HYPERLIQUID:0xabc" || balances[0].Currency != "USDC" || !balances[0].Total.Equal(d("10")) || !balances[0].Available.Equal(d("8.5")) || !balances[0].Locked.Equal(d("1.5")) {
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
	acct := newAccountClient(rest, clock.NewSimulatedClock(time.Unix(1700000000, 0)), "HYPERLIQUID:0xabc")

	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.AccountID != "HYPERLIQUID:0xabc" || state.Type != model.AccountMargin || state.ModeInfo.CollateralMode != "unified" {
		t.Fatalf("state=%+v", state)
	}
	if len(state.Balances) != 2 || state.Balances[0].Currency != "USDC" || !state.Balances[0].Free.Equal(d("88")) || state.Balances[1].Currency != "PURR" {
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

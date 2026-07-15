package perp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
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
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

var (
	_ contract.ExecutionClient               = (*executionClient)(nil)
	_ contract.AccountClient                 = (*accountClient)(nil)
	_ contract.MarketDataClient              = (*marketDataClient)(nil)
	_ contract.DerivativeReferenceDataClient = (*marketDataClient)(nil)
	_ contract.OpenInterestClient            = (*marketDataClient)(nil)
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func testPerpID() model.InstrumentID {
	return model.InstrumentID{Venue: venueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}
}

func TestHyperliquidPerpValidateSubmitIsPure(t *testing.T) {
	client := newExecutionClient(nil, testProvider(t), clock.NewRealClock(), AccountIDDefault)
	req := model.OrderRequest{
		AccountID:    AccountIDDefault,
		InstrumentID: testPerpID(),
		ClientID:     "perp-validate-pure",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.01"),
		Price:        d("65000"),
		PositionSide: enums.PosNet,
	}

	if err := client.ValidateSubmit(req); err != nil {
		t.Fatalf("ValidateSubmit: %v", err)
	}
	if got := client.ids.VenueCloidForClient(req.ClientID); got != "" {
		t.Fatalf("ValidateSubmit remembered cloid=%q, want no side effect", got)
	}
	prospective := cloid.ForClientID(req.ClientID)
	if got := client.ids.ClientID(prospective, ""); got != prospective {
		t.Fatalf("ValidateSubmit remembered client mapping=%q, want passthrough %q", got, prospective)
	}
}

func TestHyperliquidPerpValidateSubmitTable(t *testing.T) {
	client := newExecutionClient(nil, testProvider(t), clock.NewRealClock(), AccountIDDefault)
	base := model.OrderRequest{
		AccountID:    AccountIDDefault,
		InstrumentID: testPerpID(),
		ClientID:     "perp-validation-table",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.01"),
		Price:        d("65000"),
		PositionSide: enums.PosNet,
	}
	tests := []struct {
		name    string
		mutate  func(*model.OrderRequest)
		wantErr bool
	}{
		{name: "valid"},
		{name: "unknown instrument", mutate: func(req *model.OrderRequest) { req.InstrumentID.Symbol = "UNKNOWN-USDC" }, wantErr: true},
		{name: "invalid side", mutate: func(req *model.OrderRequest) { req.Side = enums.SideUnknown }, wantErr: true},
		{name: "unsupported order type", mutate: func(req *model.OrderRequest) { req.Type = enums.OrderType(255) }, wantErr: true},
		{name: "unsupported tif", mutate: func(req *model.OrderRequest) { req.TIF = enums.TifFOK }, wantErr: true},
		{name: "non-net position", mutate: func(req *model.OrderRequest) { req.PositionSide = enums.PosShort }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := base
			req.ClientID += "-" + tt.name
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			err := client.ValidateSubmit(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateSubmit err=%v wantErr=%v", err, tt.wantErr)
			}
			if got := client.ids.VenueCloidForClient(req.ClientID); got != "" {
				t.Fatalf("ValidateSubmit remembered cloid=%q", got)
			}
		})
	}
}

func TestHyperliquidPerpMapsDocumentedRejectedStatuses(t *testing.T) {
	for _, status := range []string{
		"perpMarginRejected", "reduceOnlyRejected", "positionIncreaseAtOpenInterestCapRejected",
		"positionFlipAtOpenInterestCapRejected", "tooAggressiveAtOpenInterestCapRejected",
		"openInterestIncreaseRejected", "oracleRejected", "perpMaxPositionRejected",
	} {
		if got := statusFromHL(status); got != enums.StatusRejected {
			t.Errorf("statusFromHL(%q)=%s, want REJECTED", status, got)
		}
	}
	if got := statusFromHL("futureRejectedStatus"); got != enums.StatusUnknown {
		t.Fatalf("future unknown status=%s, want UNKNOWN", got)
	}
}

func TestHyperliquidPerpRejectsUnsupportedFOKBeforeTransport(t *testing.T) {
	if tif, err := tifToHL(enums.TifFOK); tif != "" || !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("tifToHL(FOK)=%q err=%v, want fail-closed ErrNotSupported", tif, err)
	}
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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)
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
			AccountState:      contracttest.CapabilityProbe{Support: contracttest.InventorySupported("combined Hyperliquid account state is translated from clearinghouseState and spotClearinghouseState")},
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
	if caps := acct.Capabilities(); !caps.Reports.AccountBalanceSnapshots {
		t.Fatalf("account balance snapshot capability=false, want true")
	}
	if ref := market.Capabilities().ReferenceData; !ref.CurrentFunding || !ref.CurrentMarkPrice || !ref.CurrentOraclePrice || !ref.CurrentOpenInterest || ref.FundingHistory {
		t.Fatalf("reference capabilities incomplete: %+v", ref)
	}
}

func TestHyperliquidPerpAdapterPropagatesCanonicalAccountID(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		response := `{}`
		switch req["type"] {
		case "meta":
			response = `{"universe":[{"name":"BTC","szDecimals":5,"maxLeverage":50}]}`
		case "userAbstraction":
			if req["user"] != "0xABCDEF0000000000000000000000000000000000" {
				t.Fatalf("userAbstraction user=%v, want account address", req["user"])
			}
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
		AccountAddress: "0xABCDEF0000000000000000000000000000000000",
		Environment:    sdk.EnvironmentTestnet,
		RESTBaseURL:    "https://unit.test",
		HTTPClient:     httpClient,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer adapter.Close()

	const want = AccountIDDefault
	if adapter.acct.accountID != want {
		t.Fatalf("account client accountID=%q, want %q", adapter.acct.accountID, want)
	}
	if adapter.exec.accountID != want {
		t.Fatalf("execution client accountID=%q, want %q", adapter.exec.accountID, want)
	}
}

func TestHyperliquidPerpAdapterUsesExplicitAccountID(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		response := `{}`
		switch req["type"] {
		case "meta":
			response = `{"universe":[{"name":"BTC","szDecimals":5,"maxLeverage":50}]}`
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

func TestHyperliquidPerpAdapterRejectsEmptyAccountIdentity(t *testing.T) {
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

func TestHyperliquidPerpAdapterRejectsNonHexAccountAddress(t *testing.T) {
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

func TestHyperliquidPerpAdapterResolvesAgentOwnerForConfiguredHexOwner(t *testing.T) {
	const owner = "0xabc0000000000000000000000000000000000000"
	sawUserRole := false
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		response := `{}`
		switch req["type"] {
		case "userRole":
			sawUserRole = true
			user, _ := req["user"].(string)
			if user == "" || user == owner || !strings.HasPrefix(user, "0x") {
				t.Fatalf("userRole user=%q, want derived signer address", user)
			}
			response = `{"role":"agent","data":{"user":"` + owner + `"}}`
		case "meta":
			response = `{"universe":[{"name":"BTC","szDecimals":5,"maxLeverage":50}]}`
		case "userAbstraction":
			if req["user"] != owner {
				t.Fatalf("userAbstraction user=%v, want owner", req["user"])
			}
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
	const wantAccountID = AccountIDDefault
	if adapter.acct.accountID != wantAccountID || adapter.exec.accountID != wantAccountID {
		t.Fatalf("adapter account ids acct=%q exec=%q, want owner account id", adapter.acct.accountID, adapter.exec.accountID)
	}
}

func TestHyperliquidPerpAdapterRejectsConfiguredHexOwnerMismatch(t *testing.T) {
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
		AccountAddress: "0xabc0000000000000000000000000000000000000",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer adapter.Close()
	if _, ok := adapter.Market.InstrumentProvider().Instrument(testPerpID()); !ok {
		t.Fatalf("standard perp instrument not loaded")
	}
	hip3, ok := adapter.Market.InstrumentProvider().Instrument(testHIP3ID())
	if !ok || hip3.AssetIndex == nil || *hip3.AssetIndex != 120000 {
		t.Fatalf("HIP-3 instrument=%+v ok=%v", hip3, ok)
	}
	if len(adapter.acct.hip3Dexes) != 1 || adapter.acct.hip3Dexes[0] != "testdex" {
		t.Fatalf("account HIP-3 dexes=%v, want configured testdex", adapter.acct.hip3Dexes)
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
		AccountAddress: "0xabc0000000000000000000000000000000000000",
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
		return `[{"universe":[{"name":"BTC"},{"name":"ETH"}]},[{"funding":"0.0001","markPx":"65000.5","oraclePx":"64999.5","premium":"0.0002","openInterest":"123.45"},{"funding":"0.0003"}]]`, 200
	})
	market := newMarketDataClient(rest, nil, provider, clock.NewRealClock())

	funding, err := market.FundingRate(context.Background(), testPerpID())
	if err != nil {
		t.Fatalf("FundingRate: %v", err)
	}
	if funding.InstrumentID != testPerpID() || !funding.Rate.Equal(d("0.0001")) || !funding.MarkPrice.Equal(d("65000.5")) || !funding.OraclePrice.Equal(d("64999.5")) {
		t.Fatalf("funding=%+v", funding)
	}
	ref, err := market.ReferenceSnapshot(context.Background(), testPerpID())
	if err != nil {
		t.Fatalf("ReferenceSnapshot: %v", err)
	}
	if !ref.Fields.Has(model.ReferenceHasFundingRate) || !ref.Fields.Has(model.ReferenceHasMarkPrice) || !ref.Fields.Has(model.ReferenceHasOraclePrice) || !ref.Fields.Has(model.ReferenceHasPremium) || ref.FundingInterval != time.Hour {
		t.Fatalf("unexpected reference snapshot: %+v", ref)
	}
	oi, err := market.OpenInterest(context.Background(), testPerpID())
	if err != nil {
		t.Fatalf("OpenInterest: %v", err)
	}
	if !oi.OpenInterest.Equal(d("123.45")) || oi.Unit != "contracts" {
		t.Fatalf("unexpected OI: %+v", oi)
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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

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
	if order.VenueOrderID != "555" || order.Status != enums.StatusNew || order.Request.AccountID != AccountIDDefault || order.Request.ClientID != "c-perp-1" || !order.Request.ReduceOnly {
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
	if hip3Order.VenueOrderID != "556" || hip3Order.Request.AccountID != AccountIDDefault || hip3Order.Request.InstrumentID != testHIP3ID() {
		t.Fatalf("HIP-3 order=%+v", hip3Order)
	}
	if !sawStandard || !sawHIP3 {
		t.Fatalf("expected standard and HIP-3 order actions, got standard=%v hip3=%v", sawStandard, sawHIP3)
	}
}

func TestHyperliquidPerpSubmitRejectsMismatchedAccountID(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		t.Fatalf("unexpected REST call for mismatched account id: %s", body)
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

	_, err := exec.Submit(context.Background(), model.OrderRequest{
		AccountID:    "HYPERLIQUID-OTHER",
		InstrumentID: testPerpID(),
		ClientID:     "c-perp-mismatch",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.01"),
		Price:        d("65000"),
		PositionSide: enums.PosNet,
	})
	if err == nil || !strings.Contains(err.Error(), "account id") {
		t.Fatalf("Submit err=%v, want account id mismatch", err)
	}
}

func TestHyperliquidPerpSubmitMapsExplicitVenueRejection(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"Insufficient margin"}]}}}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: testPerpID(), ClientID: "rejected-perp", Side: enums.SideBuy,
		Type: enums.TypeLimit, TIF: enums.TifIOC, Quantity: d("0.01"), Price: d("65000"), PositionSide: enums.PosNet,
	})
	if order != nil || !errors.Is(err, contract.ErrVenueRejected) || !errors.Is(err, sdk.ErrOrderRejected) || !strings.Contains(err.Error(), "Insufficient margin") {
		t.Fatalf("order=%+v err=%v, want preserved typed venue rejection", order, err)
	}
}

func TestHyperliquidPerpCancelAndModifyMapOnlyTypedVenueRejections(t *testing.T) {
	provider := testProvider(t)
	for _, operation := range []string{"cancel", "modify"} {
		t.Run(operation, func(t *testing.T) {
			rest := testREST(func(r *http.Request, body []byte) (string, int) {
				if r.URL.Path == "/info" {
					return `{"status":"order","order":{"order":{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.01","oid":555,"timestamp":1700000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Limit","tif":"Gtc","isTrigger":false,"triggerPx":"0"},"status":"open","statusTimestamp":1700000000000}}`, http.StatusOK
				}
				return `{"status":"ok","response":{"type":"default","data":{"statuses":[{"error":"Order was never placed"}]}}}`, http.StatusOK
			})
			exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)
			var err error
			if operation == "cancel" {
				err = exec.Cancel(context.Background(), testPerpID(), "555")
			} else {
				_, err = exec.Modify(context.Background(), testPerpID(), "555", d("65100"), d("0.02"))
			}
			if !errors.Is(err, contract.ErrVenueRejected) || !errors.Is(err, sdk.ErrOrderRejected) {
				t.Fatalf("err=%v, want typed venue rejection", err)
			}
		})
	}
}

func TestHyperliquidPerpCommandAmbiguityMatrixDoesNotClaimVenueRejection(t *testing.T) {
	provider := testProvider(t)
	infoResponse := `{"status":"order","order":{"order":{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.01","oid":555,"timestamp":1700000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Limit","tif":"Gtc","isTrigger":false,"triggerPx":"0"},"status":"open","statusTimestamp":1700000000000}}`
	type outcome struct {
		name      string
		status    int
		body      func(string) string
		transport error
	}
	outcomes := []outcome{
		{name: "timeout", transport: context.DeadlineExceeded},
		{name: "http 5xx", status: http.StatusInternalServerError, body: func(string) string {
			return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"server failure"}]}}}`
		}},
		{name: "malformed", status: http.StatusOK, body: func(string) string { return `{not-json` }},
		{name: "non sentinel", status: http.StatusOK, body: func(operation string) string {
			if operation == "cancel" {
				return `{"status":"ok","response":{"type":"default","data":{"statuses":["unexpected"]}}}`
			}
			return `{"status":"err","response":"temporary"}`
		}},
	}
	for _, operation := range []string{"submit", "cancel", "modify"} {
		for _, result := range outcomes {
			t.Run(operation+"/"+result.name, func(t *testing.T) {
				rest := testREST(func(r *http.Request, _ []byte) (string, int) {
					if r.URL.Path == "/info" {
						return infoResponse, http.StatusOK
					}
					return result.body(operation), result.status
				})
				if result.transport != nil {
					rest.Http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						if r.URL.Path == "/info" {
							return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(infoResponse)), Header: make(http.Header), Request: r}, nil
						}
						return nil, result.transport
					})}
				}
				exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)
				var err error
				switch operation {
				case "submit":
					_, err = exec.Submit(context.Background(), model.OrderRequest{
						InstrumentID: testPerpID(), ClientID: "ambiguous-perp", Side: enums.SideBuy,
						Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: d("0.01"), Price: d("65000"), PositionSide: enums.PosNet,
					})
				case "cancel":
					err = exec.Cancel(context.Background(), testPerpID(), "555")
				case "modify":
					_, err = exec.Modify(context.Background(), testPerpID(), "555", d("65100"), d("0.02"))
				}
				if err == nil {
					t.Fatal("ambiguous command outcome unexpectedly succeeded")
				}
				if errors.Is(err, contract.ErrVenueRejected) {
					t.Fatalf("err=%v, ambiguous command outcome must not claim venue rejection", err)
				}
				if result.transport != nil && !errors.Is(err, result.transport) {
					t.Fatalf("err=%v, want preserved transport cause %v", err, result.transport)
				}
			})
		}
	}
}

func TestHyperliquidPerpExactStatusByClientAndFillHistory(t *testing.T) {
	provider := testProvider(t)
	const clientID = "exact-perp-client"
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
			return `{"status":"order","order":{"order":{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.004","oid":888,"cloid":"` + venueCloid + `","timestamp":1700000000000,"origSz":"0.01"},"status":"canceled","statusTimestamp":1700000001000}}`, http.StatusOK
		case "userFills":
			return `[{"coin":"BTC","px":"65000","sz":"0.003","side":"B","time":1700000000200,"oid":888,"crossed":true,"fee":"-0.01","feeToken":"USDC","tid":51},{"coin":"BTC","px":"65010","sz":"0.003","side":"B","time":1700000000300,"oid":888,"crossed":false,"fee":"0.02","feeToken":"USDC","tid":52}]`, http.StatusOK
		default:
			t.Fatalf("unexpected info request: %s", body)
		}
		return `{}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID: AccountIDDefault, InstrumentID: testPerpID(), ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport: %v", err)
	}
	if report == nil || report.Order.Request.ClientID != clientID || report.Order.VenueOrderID != "888" || report.Order.Status != enums.StatusCanceled || !report.Order.FilledQty.Equal(d("0.006")) {
		t.Fatalf("report=%+v", report)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{
		AccountID: AccountIDDefault, InstrumentID: testPerpID(), VenueOrderID: "888",
	})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(fills) != 2 || fills[0].Fill.ClientID != clientID || !fills[0].Fill.Fee.Equal(d("-0.01")) || fills[1].Fill.TradeID != "52" || !fills[1].Fill.Fee.Equal(d("0.02")) {
		t.Fatalf("fills=%+v", fills)
	}
}

func TestHyperliquidPerpLifecycleRecoversFullyFilledLostSubmitByCloid(t *testing.T) {
	provider := testProvider(t)
	state := &perpLifecyclePositionState{}
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
					return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":601,"cloid":"` + order["c"].(string) + `","status":"open"}}]}}}`, http.StatusOK
				case 2:
					openingCloid = order["c"].(string)
					state.quantity = d("0.01")
					return `{"status":"err","response":"submit response lost after venue fill"}`, http.StatusServiceUnavailable
				case 3:
					state.quantity = decimal.Zero
					return `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"totalSz":"0.01","avgPx":"65010","oid":603}}]}}}`, http.StatusOK
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
					return `{"status":"order","order":{"order":{"coin":"BTC","side":"B","limitPx":"65000","sz":"0","oid":602,"cloid":"` + openingCloid + `","timestamp":1700000000000,"origSz":"0.01"},"status":"filled","statusTimestamp":1700000001000}}`, http.StatusOK
				case float64:
					if int64(oid) != 601 {
						t.Fatalf("unexpected numeric orderStatus oid=%v", oid)
					}
					return `{"status":"order","order":{"order":{"coin":"BTC","side":"B","limitPx":"60000","sz":"0.01","oid":601,"timestamp":1700000000000,"origSz":"0.01"},"status":"canceled","statusTimestamp":1700000001000}}`, http.StatusOK
				default:
					t.Fatalf("unexpected orderStatus oid=%T(%v)", req["oid"], req["oid"])
				}
			case "userFills":
				if openingCloid != "" {
					queriedFillsByVenue = true
					return `[{"coin":"BTC","px":"65000","sz":"0.01","side":"B","time":1700000000200,"oid":602,"crossed":true,"fee":"0.001","feeToken":"USDC","tid":51}]`, http.StatusOK
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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)
	spec := runtimeaccept.ConfigurePerpPositionReporter(runtimeaccept.OrderLifecycleSpec{
		Label: "hyperliquid perp lost submit", Venue: venueName, AccountID: AccountIDDefault,
		InstrumentID: testPerpID(), Quantity: d("0.01"), CloseQuantity: d("0.01"),
		RestingPrice: d("60000"), FillPrice: d("65000"), ClosePrice: d("65010"),
		PositionSide: enums.PosNet, CloseAfterFill: true, PollInterval: time.Millisecond, CleanupTimeout: 100 * time.Millisecond,
	}, &perpLifecyclePositionReporter{state: state})

	result, err := runtimeaccept.RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(d("0.01")) || !result.ClosedQty.Equal(d("0.01")) || result.Filled.VenueOrderID != "602" || result.Filled.Request.TIF != enums.TifIOC || result.Filled.Request.ReduceOnly || result.Closed.Request.TIF != enums.TifIOC || !result.Closed.Request.ReduceOnly || !state.quantity.IsZero() {
		t.Fatalf("result=%+v position=%s, want recovered opening oid=602 and flat close", result, state.quantity)
	}
	if openingCloid != cloid.ForClientID(result.Filled.Request.ClientID) || !queriedByCloid || !queriedFillsByVenue || orderCalls != 3 {
		t.Fatalf("cloid=%q client=%q queriedStatus=%v queriedFills=%v orderCalls=%d", openingCloid, result.Filled.Request.ClientID, queriedByCloid, queriedFillsByVenue, orderCalls)
	}
}

type perpLifecyclePositionState struct {
	quantity decimal.Decimal
}

type perpLifecyclePositionReporter struct {
	state *perpLifecyclePositionState
}

func (r *perpLifecyclePositionReporter) Positions(context.Context) ([]model.Position, error) {
	if r.state == nil || !r.state.quantity.IsPositive() {
		return nil, nil
	}
	return []model.Position{{
		AccountID: AccountIDDefault, InstrumentID: testPerpID(), Side: enums.PosNet, Quantity: r.state.quantity,
	}}, nil
}

func TestHyperliquidPerpExactStatusRejectsMismatchedClientAndVenueIdentity(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		return `{"status":"order","order":{"order":{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.01","oid":888,"cloid":"` + cloid.ForClientID("other-client") + `","timestamp":1700000000000,"origSz":"0.01"},"status":"open","statusTimestamp":1700000001000}}`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID: AccountIDDefault, InstrumentID: testPerpID(),
		ClientID: "expected-client", VenueOrderID: "888",
	})
	if report != nil || err == nil || !strings.Contains(err.Error(), "client identity mismatch") {
		t.Fatalf("report=%+v err=%v, want identity mismatch", report, err)
	}
	if len(exec.orders) != 0 || exec.ids.ClientID("", "888") != "" {
		t.Fatalf("mismatched identity polluted order map: orders=%v mapped=%q", exec.orders, exec.ids.ClientID("", "888"))
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
				if int64(modify["oid"].(float64)) != 555 || order["b"] != true || order["p"] != "65100" || order["s"] != "0.02" || order["r"] != true || order["c"] != "c-perp-1" {
					t.Fatalf("unexpected modify action: %s", body)
				}
				limit := order["t"].(map[string]any)["limit"].(map[string]any)
				if limit["tif"] != "Alo" {
					t.Fatalf("modify lost post-only TIF: %s", body)
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
				return `{"status":"order","order":{"order":{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.01","oid":555,"cloid":"c-perp-1","timestamp":1700000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Limit","tif":"Alo","isTrigger":false,"triggerPx":"0"},"status":"open","statusTimestamp":1700000000000}}`, 200
			case "frontendOpenOrders":
				openCalls++
				if req["dex"] == "testdex" {
					return `[{"coin":"COIN","side":"A","limitPx":"10","sz":"2","oid":556,"cloid":"c-hip3-1","timestamp":1700000000000,"origSz":"2","reduceOnly":false,"orderType":"Limit","tif":"Gtc"}]`, 200
				}
				return `[{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.01","oid":555,"cloid":"c-perp-1","timestamp":1700000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Limit","tif":"Alo"}]`, 200
			default:
				t.Fatalf("unexpected info request: %s", body)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return `{}`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

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
	if len(open) != 1 || open[0].VenueOrderID != "555" || open[0].Request.AccountID != AccountIDDefault || open[0].Request.ClientID != "c-perp-1" {
		t.Fatalf("open orders=%+v", open)
	}
	query := model.MassStatusQuery{}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.AccountID != AccountIDDefault || len(mass.OrderReports) != 2 {
		t.Fatalf("mass=%+v", mass)
	}
	for _, report := range mass.OrderReports {
		if report.AccountID != AccountIDDefault || report.Order.Request.AccountID != AccountIDDefault {
			t.Fatalf("mass report account ids report=%q order=%q", report.AccountID, report.Order.Request.AccountID)
		}
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed coverage: %v", err)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || mass.FillsCoverage.State != model.CoverageNotRequested || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("coverage=%+v/%+v/%+v", mass.OpenOrdersCoverage, mass.FillsCoverage, mass.PositionsCoverage)
	}
	wantIDs := make([]model.InstrumentID, 0, len(provider.All()))
	for _, inst := range provider.All() {
		wantIDs = append(wantIDs, inst.ID)
	}
	wantIDs = model.NormalizeInstrumentIDs(wantIDs)
	if open := mass.OpenOrdersCoverage.Scope; open.AccountID != AccountIDDefault || open.ClientID != "" || !slices.Equal(open.InstrumentIDs, wantIDs) || open.Through.IsZero() || !open.From.IsZero() {
		t.Fatalf("open-order coverage scope=%+v, want account=%q ids=%v snapshot watermark", open, AccountIDDefault, wantIDs)
	}
	if !mass.FillsCoverage.Scope.IsZero() || !mass.PositionsCoverage.Scope.IsZero() {
		t.Fatalf("not-requested scopes fills=%+v positions=%+v, want zero", mass.FillsCoverage.Scope, mass.PositionsCoverage.Scope)
	}
	if !sawCancel || !sawModify || openCalls == 0 {
		t.Fatalf("expected cancel/modify/open paths, got cancel=%v modify=%v openCalls=%d", sawCancel, sawModify, openCalls)
	}
	if openCalls != 3 {
		t.Fatalf("frontendOpenOrders calls=%d, want default OpenOrders plus default/HIP-3 mass-status snapshots", openCalls)
	}
}

func TestHyperliquidPerpHIP3OpenOrdersScopesDexAndPreservesSemantics(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["type"] != "frontendOpenOrders" || req["dex"] != "testdex" {
			t.Fatalf("request=%s, want HIP-3 frontendOpenOrders scoped to testdex", body)
		}
		return `[{"coin":"COIN","side":"A","limitPx":"10","sz":"1.5","oid":556,"cloid":"c-hip3-1","timestamp":1700000000000,"origSz":"2","reduceOnly":true,"orderType":"Limit","tif":"Alo"}]`, 200
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

	orders, err := exec.OpenOrders(context.Background(), testHIP3ID())
	if err != nil {
		t.Fatalf("OpenOrders: %v", err)
	}
	if len(orders) != 1 || orders[0].Request.InstrumentID != testHIP3ID() || !orders[0].Request.Quantity.Equal(d("2")) || !orders[0].Request.ReduceOnly || orders[0].Request.TIF != enums.TifGTX {
		t.Fatalf("HIP-3 open orders=%+v", orders)
	}
}

func TestHyperliquidPerpModifyPreservesReduceOnlyTriggerAndCloid(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		switch r.URL.Path {
		case "/info":
			return `{"status":"order","order":{"order":{"coin":"BTC","side":"A","limitPx":"65000","sz":"0.01","oid":555,"cloid":"trigger-cloid","timestamp":1700000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Stop Market","isTrigger":true,"triggerPx":"64000"},"status":"open","statusTimestamp":1700000000000}}`, 200
		case "/exchange":
			action := decodeAction(t, body)
			modify := action["modifies"].([]any)[0].(map[string]any)
			order := modify["order"].(map[string]any)
			trigger := order["t"].(map[string]any)["trigger"].(map[string]any)
			if order["r"] != true || order["c"] != "trigger-cloid" || trigger["isMarket"] != true || trigger["triggerPx"] != "64000" || trigger["tpsl"] != "sl" {
				t.Fatalf("modify lost trigger semantics: %s", body)
			}
			return `{"status":"ok","response":{"type":"modify","data":{"statuses":[{"resting":{"oid":555,"cloid":"trigger-cloid","status":"open"}}]}}}`, 200
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return `{}`, 500
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)
	order, err := exec.Modify(context.Background(), testPerpID(), "555", d("65010"), d("0.02"))
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}
	if order.Request.Type != enums.TypeStopMarket || !order.Request.TriggerPrice.Equal(d("64000")) || !order.Request.ReduceOnly {
		t.Fatalf("modified order=%+v", order)
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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

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
		if oe.Order.Status != enums.StatusCanceled || oe.Order.Request.AccountID != AccountIDDefault || oe.Order.Request.ClientID != "c-perp-1" || oe.Order.VenueOrderID != "555" {
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
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)

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
		if oe.Order.Request.ClientID != "runtime-client-1" || oe.Order.Request.AccountID != AccountIDDefault {
			t.Fatalf("order identity client=%q account=%q, want original runtime client and canonical account", oe.Order.Request.ClientID, oe.Order.Request.AccountID)
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
		if fe.Fill.ClientID != "runtime-client-1" || fe.Fill.AccountID != AccountIDDefault {
			t.Fatalf("fill identity client=%q account=%q, want original runtime client and canonical account", fe.Fill.ClientID, fe.Fill.AccountID)
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
	acct := newAccountClient(rest, provider, clock.NewRealClock(), "cross", d("1"), sdk.AccountAbstractionDefault, AccountIDDefault)

	balances, err := acct.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(balances) != 1 || balances[0].AccountID != AccountIDDefault || balances[0].Currency != "USDC" || !balances[0].Total.Equal(d("100")) || !balances[0].Free.Equal(d("88")) || !balances[0].Locked.Equal(d("12")) {
		t.Fatalf("balances=%+v", balances)
	}
	positions, err := acct.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(positions) != 1 || positions[0].AccountID != AccountIDDefault || positions[0].InstrumentID != testPerpID() || positions[0].Side != enums.PosNet || !positions[0].Quantity.Equal(d("-0.02")) || !positions[0].Leverage.Equal(d("5")) {
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
	acct := newAccountClient(rest, provider, clock.NewRealClock(), "cross", d("1"), sdk.AccountAbstractionUnifiedAccount, AccountIDDefault)

	balances, err := acct.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(balances) != 1 || balances[0].AccountID != AccountIDDefault || balances[0].Currency != "USDC" || !balances[0].Total.Equal(d("10")) || !balances[0].Free.Equal(d("8.5")) || !balances[0].Locked.Equal(d("1.5")) {
		t.Fatalf("balances=%+v", balances)
	}
}

func TestHyperliquidPerpAccountStateCombinesPerpAndSpot(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/info" {
			t.Fatalf("request=%s %s, want POST /info", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		switch req["type"] {
		case "clearinghouseState":
			return `{"assetPositions":[],"crossMarginSummary":{"accountValue":"100","totalMarginUsed":"12","totalNtlPos":"50","totalRawUsd":"100"},"marginSummary":{"accountValue":"100","totalMarginUsed":"12","totalNtlPos":"50","totalRawUsd":"100"},"time":1700000000000,"withdrawable":"88","crossMaintenanceMarginUsed":"7"}`, 200
		case "spotClearinghouseState":
			return `{"balances":[{"coin":"USDC","token":0,"hold":"1.5","total":"10","entryNtl":"0"},{"coin":"PURR","token":1,"hold":"0.5","total":"2","entryNtl":"0"}]}`, 200
		default:
			t.Fatalf("unexpected request: %s", body)
		}
		return `{}`, 200
	})
	acct := newAccountClient(rest, provider, clock.NewSimulatedClock(time.Unix(1700000000, 0)), "cross", d("1"), sdk.AccountAbstractionPortfolioMargin, AccountIDDefault)

	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.AccountID != AccountIDDefault || state.Type != model.AccountMargin || !state.Reported || state.EventID == "" || state.TsEvent.IsZero() || state.TsInit.IsZero() {
		t.Fatalf("state=%+v", state)
	}
	if len(state.Balances) != 2 || state.Balances[0].Currency != "USDC" || !state.Balances[0].Free.Equal(d("8.5")) || state.Balances[1].Currency != "PURR" {
		t.Fatalf("balances=%+v", state.Balances)
	}
	if len(state.Margins) != 1 || !state.Margins[0].Initial.Equal(d("12")) {
		t.Fatalf("margins=%+v", state.Margins)
	}
}

func TestHyperliquidPerpEventTranslations(t *testing.T) {
	provider := testProvider(t)
	reduceOnly := true
	orderEvents := execEventsFromOrderUpdate(sdk.WsOrderUpdate{
		Order: sdk.WsOrder{
			Coin:       "BTC",
			Side:       "B",
			LimitPx:    "65000",
			Sz:         "0.004",
			Oid:        555,
			Timestamp:  1700000000000,
			OrigSz:     "0.01",
			Cliod:      "c-perp-1",
			ReduceOnly: &reduceOnly,
			OrderType:  "Limit",
			Tif:        "Alo",
		},
		Status:          sdk.StatusOpen,
		StatusTimestamp: 1700000000123,
	}, provider, AccountIDDefault)
	if len(orderEvents) != 1 {
		t.Fatalf("order events=%+v", orderEvents)
	}
	oe := orderEvents[0].(contract.OrderEvent)
	if oe.Order.Request.InstrumentID != testPerpID() || oe.Order.Request.AccountID != AccountIDDefault || oe.Order.Status != enums.StatusNew || oe.Order.VenueOrderID != "555" || oe.Order.Request.Type != enums.TypeLimit || oe.Order.Request.TIF != enums.TifGTX || !oe.Order.Request.ReduceOnly || !oe.Order.FilledQty.Equal(d("0.006")) {
		t.Fatalf("order event=%+v", oe)
	}
	filledUpdate := sdk.WsOrderUpdate{Order: sdk.WsOrder{
		Coin: "BTC", Side: "B", LimitPx: "65000", Sz: "0", OrigSz: "0.01", Oid: 555,
		ReduceOnly: &reduceOnly, OrderType: "Stop Market", IsTrigger: true, TriggerPx: "64000",
	}, Status: sdk.StatusFilled}
	filledEvents := execEventsFromOrderUpdate(filledUpdate, provider, AccountIDDefault)
	if len(filledEvents) != 1 {
		t.Fatalf("filled events=%+v", filledEvents)
	}
	filledOrder := filledEvents[0].(contract.OrderEvent).Order
	if !filledOrder.FilledQty.Equal(d("0.01")) || filledOrder.Request.Type != enums.TypeStopMarket || !filledOrder.Request.TriggerPrice.Equal(d("64000")) || !filledOrder.Request.ReduceOnly {
		t.Fatalf("filled trigger order=%+v", filledOrder)
	}
	malformed := filledUpdate
	malformed.Order.Sz = "0.02"
	if events := execEventsFromOrderUpdate(malformed, provider, AccountIDDefault); len(events) != 0 {
		t.Fatalf("malformed over-remaining update emitted events=%+v", events)
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
			Fee:      "-0.01",
			FeeToken: "USDC",
			Tid:      99,
		}},
	}, provider, AccountIDDefault)
	if len(fillEvents) != 1 {
		t.Fatalf("fill events=%+v", fillEvents)
	}
	fe := fillEvents[0].(contract.FillEvent)
	if fe.Fill.InstrumentID != testHIP3ID() || fe.Fill.AccountID != AccountIDDefault || fe.Fill.Liquidity != enums.LiqTaker || !fe.Fill.Quantity.Equal(d("2")) || fe.Fill.TradeID != "99" || !fe.Fill.Fee.Equal(d("-0.01")) {
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
	}, provider, clock.NewRealClock(), AccountIDDefault)
	if len(accountEvents) != 0 {
		t.Fatalf("zero asset position should not emit event: %+v", accountEvents)
	}
}

func TestHyperliquidPerpRememberOrderDoesNotDowngradeKnownReduceOnlyTrigger(t *testing.T) {
	exec := newExecutionClient(nil, testProvider(t), clock.NewRealClock(), AccountIDDefault)
	known := model.Order{Request: model.OrderRequest{
		AccountID: AccountIDDefault, InstrumentID: testPerpID(), ClientID: "runtime-client",
		Side: enums.SideSell, Type: enums.TypeStopMarket, Quantity: d("0.01"), Price: d("64000"),
		TriggerPrice: d("64500"), PositionSide: enums.PosNet, ReduceOnly: true,
	}, VenueOrderID: "555", Status: enums.StatusNew}
	exec.rememberOrder(known)
	update := model.Order{Request: model.OrderRequest{
		AccountID: AccountIDDefault, InstrumentID: testPerpID(), Side: enums.SideSell,
		Quantity: d("0.01"), Price: d("64000"), PositionSide: enums.PosNet,
	}, VenueOrderID: "555", Status: enums.StatusFilled, FilledQty: d("0.01")}
	merged := exec.rememberOrder(update)
	if merged.Request.ClientID != "runtime-client" || merged.Request.Type != enums.TypeStopMarket || !merged.Request.ReduceOnly || !merged.Request.TriggerPrice.Equal(d("64500")) {
		t.Fatalf("merged=%+v, sparse WS update downgraded submitted semantics", merged)
	}
}

func TestHyperliquidPerpUserFillCallbackPreservesSnapshotFlag(t *testing.T) {
	provider := testProvider(t)
	exec := newExecutionClient(nil, provider, clock.NewRealClock(), AccountIDDefault)
	adapter := &Adapter{provider: provider, exec: exec}
	fill := sdk.WsUserFill{
		Coin: "BTC", Px: "65000", Sz: "0.01", Side: "B", Time: 1700000000123,
		Oid: 555, Crossed: true, Fee: "-0.01", FeeToken: "USDC", Tid: 99,
	}
	adapter.emitUserFills(sdk.WsUserFills{IsSnapshot: true, Fills: []sdk.WsUserFill{fill}})
	fill.Tid = 100
	adapter.emitUserFills(sdk.WsUserFills{Fills: []sdk.WsUserFill{fill}})

	for i, wantSnapshot := range []bool{true, false} {
		select {
		case env := <-exec.Events():
			if env.Source != contract.SourceAdapterStream || !env.Flags.Has(contract.EventFlagFromStream) || env.Flags.Has(contract.EventFlagFromSnapshot) != wantSnapshot {
				t.Fatalf("event %d meta=%+v, want stream snapshot=%v", i+1, env.Meta(), wantSnapshot)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i+1)
		}
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

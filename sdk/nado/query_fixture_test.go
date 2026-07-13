package nado

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type nadoQueryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f nadoQueryRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newNadoQueryTestnetClient(t *testing.T) *Client {
	t.Helper()
	profile, err := NewProfile(EnvironmentTestnet)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(profile)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newNadoQueryClientForServer(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client := newNadoQueryTestnetClient(t)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport
	client.WithHTTPClient(&http.Client{Transport: nadoQueryRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		clone.Host = clone.URL.Host
		return transport.RoundTrip(clone)
	})})
	return client
}

func nadoFixtureBody(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func nadoQueryFixtureServer(t *testing.T, assert func(*testing.T, *http.Request, map[string]any), fixtures map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/query" {
			t.Errorf("path = %q, want /v1/query", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		req := make(map[string]any)
		switch r.Method {
		case http.MethodGet:
			for key, values := range r.URL.Query() {
				if len(values) > 0 {
					req[key] = values[0]
				}
			}
		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("unmarshal request body: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		default:
			t.Errorf("method = %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		assert(t, r, req)
		typ, _ := req["type"].(string)
		payload, ok := fixtures[typ]
		if !ok {
			t.Errorf("unexpected query type %q", typ)
			http.Error(w, "unexpected query type", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
}

func TestNadoTypedQueryFixturesAssertRoutesAndBodies(t *testing.T) {
	t.Parallel()

	sender := "0x1111111111111111111111111111111111111111000000000000000000000000"
	maxOrderCalls := 0
	fixtures := map[string]string{
		"status":            nadoFixtureBody(t, "status.json"),
		"all_products":      nadoFixtureBody(t, "all_products.json"),
		"symbols":           nadoFixtureBody(t, "symbols.json"),
		"max_order_size":    nadoFixtureBody(t, "max_order_size.json"),
		"subaccount_info":   nadoFixtureBody(t, "subaccount_simulation.json"),
		"market_price":      `{"status":"success","data":{"product_id":2,"bid_x18":"2500000000000000000000","ask_x18":"2501000000000000000000"}}`,
		"market_prices":     `{"status":"success","data":{"market_prices":[{"product_id":1,"bid_x18":"2499000000000000000000","ask_x18":"2500000000000000000000"},{"product_id":2,"bid_x18":"2500000000000000000000","ask_x18":"2501000000000000000000"}]}}`,
		"subaccount_orders": `{"status":"success","data":{"sender":"` + sender + `","product_id":2,"orders":[]}}`,
		"orders":            `{"status":"success","data":{"sender":"` + sender + `","product_orders":[{"sender":"` + sender + `","product_id":1,"orders":[]},{"sender":"` + sender + `","product_id":2,"orders":[]}]}}`,
		"order":             `{"status":"success","data":{"product_id":2,"sender":"` + sender + `","price_x18":"2500000000000000000000","amount":"100000000000000000","expiration":"4000000000","nonce":"1849300000000000000","unfilled_amount":"100000000000000000","digest":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","placed_at":1783641600000,"order_type":"default","appendix":"1"}}`,
	}

	server := nadoQueryFixtureServer(t, func(t *testing.T, r *http.Request, req map[string]any) {
		t.Helper()
		switch req["type"] {
		case "status":
			if r.Method != http.MethodGet {
				t.Fatalf("status method = %s", r.Method)
			}
		case "all_products":
			if r.Method != http.MethodGet {
				t.Fatalf("all_products method = %s", r.Method)
			}
		case "symbols":
			if r.Method != http.MethodPost {
				t.Fatalf("symbols method = %s", r.Method)
			}
			if req["product_type"] != "spot" {
				t.Fatalf("symbols product_type = %#v", req["product_type"])
			}
			ids, ok := req["product_ids"].([]any)
			if !ok || len(ids) != 2 || ids[0] != float64(0) || ids[1] != float64(1) {
				t.Fatalf("symbols product_ids = %#v", req["product_ids"])
			}
		case "max_order_size":
			maxOrderCalls++
			if r.Method != http.MethodGet {
				t.Fatalf("max_order_size method = %s", r.Method)
			}
			if maxOrderCalls == 2 {
				if req["direction"] != "short" || req["reduce_only"] != "true" || req["spot_leverage"] != nil {
					t.Fatalf("reduce-only max_order_size = %#v", req)
				}
				break
			}
			want := map[string]string{
				"product_id":    "2",
				"sender":        sender,
				"price_x18":     "2500000000000000000000",
				"avg_price_x18": "2490000000000000000000",
				"direction":     "long",
				"spot_leverage": "false",
				"reduce_only":   "false",
				"isolated":      "false",
				"borrow_margin": "false",
			}
			for key, value := range want {
				if req[key] != value {
					t.Fatalf("max_order_size %s = %#v, want %q", key, req[key], value)
				}
			}
		case "subaccount_info":
			if r.Method != http.MethodPost {
				t.Fatalf("subaccount_info method = %s", r.Method)
			}
			if req["subaccount"] != sender || req["pre_state"] != "true" {
				t.Fatalf("subaccount_info body = %#v", req)
			}
			txns, _ := req["txns"].(string)
			if !strings.Contains(txns, `"apply_delta"`) {
				t.Fatalf("subaccount_info txns = %q", txns)
			}
		case "market_price":
			if req["product_id"] != float64(2) {
				t.Fatalf("market_price body = %#v", req)
			}
		case "market_prices":
			ids, ok := req["product_ids"].([]any)
			if !ok || len(ids) != 2 {
				t.Fatalf("market_prices body = %#v", req)
			}
		case "subaccount_orders":
			if req["sender"] != sender || req["product_id"] != float64(2) {
				t.Fatalf("subaccount_orders body = %#v", req)
			}
		case "orders":
			if _, ok := req["product_ids"].([]any); !ok || req["product_id"] != nil {
				t.Fatalf("orders body = %#v", req)
			}
		case "order":
			if req["digest"] == "" || req["product_id"] != float64(2) {
				t.Fatalf("order body = %#v", req)
			}
		default:
			t.Fatalf("unexpected query type %#v", req["type"])
		}
	}, fixtures)
	defer server.Close()

	client := newNadoQueryClientForServer(t, server)
	ctx := context.Background()

	allProducts, err := client.GetAllProducts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allProducts.SpotProducts) != 2 || allProducts.SpotProducts[0].ProductID != 0 {
		t.Fatalf("spot products = %#v", allProducts.SpotProducts)
	}
	if allProducts.PerpProducts[0].State.OpenInterest != "1250000000000000000000" {
		t.Fatalf("open interest = %q", allProducts.PerpProducts[0].State.OpenInterest)
	}

	symbols, err := client.QuerySymbols(ctx, SymbolsRequest{ProductIDs: []int64{0, 1}, ProductType: MarketTypeSpot})
	if err != nil {
		t.Fatal(err)
	}
	if symbols.Symbols["USDT0"].TradingStatus != TradingStatusLive {
		t.Fatalf("USDT0 status = %q", symbols.Symbols["USDT0"].TradingStatus)
	}

	spotLeverage, reduceOnly, isolated, borrowMargin := false, false, false, false
	maxOrder, err := client.GetMaxOrderSize(ctx, MaxOrderSizeRequest{
		ProductID:    2,
		Sender:       sender,
		PriceX18:     "2500000000000000000000",
		AvgPriceX18:  "2490000000000000000000",
		Direction:    OrderDirectionLong,
		SpotLeverage: &spotLeverage,
		ReduceOnly:   &reduceOnly,
		Isolated:     &isolated,
		BorrowMargin: &borrowMargin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if maxOrder.MaxOrderSize != "400000000000000000" {
		t.Fatalf("max order size = %q", maxOrder.MaxOrderSize)
	}
	reduceOnly = true
	reduceCapacity, err := client.GetMaxOrderSize(ctx, MaxOrderSizeRequest{
		ProductID: 2, Sender: sender, PriceX18: "2500000000000000000000",
		Direction: OrderDirectionShort, ReduceOnly: &reduceOnly,
	})
	if err != nil || reduceCapacity.MaxOrderSize == "" {
		t.Fatalf("reduce-only capacity = %+v, err=%v", reduceCapacity, err)
	}

	preState := true
	account, err := client.GetSubaccountInfo(ctx, SubaccountInfoRequest{
		Subaccount: sender,
		Txns: []SubaccountSimulationTxn{{ApplyDelta: SubaccountSimulationDelta{
			ProductID: 2, Subaccount: sender, AmountDelta: "0", VQuoteDelta: "0",
		}}},
		PreState: &preState,
	})
	if err != nil {
		t.Fatal(err)
	}
	if account.PreState == nil {
		t.Fatal("pre_state was not decoded")
	}
	if account.PreState.PerpBalances[0].Balance.VQuoteBalance == nil || *account.PreState.PerpBalances[0].Balance.VQuoteBalance != "-1250000000000000000000" {
		t.Fatalf("pre_state perp balance = %#v", account.PreState.PerpBalances)
	}

	marketPrice, err := client.GetMarketPrice(ctx, 2)
	if err != nil || marketPrice.ProductID != 2 || marketPrice.BidX18 == "" {
		t.Fatalf("market price = %+v, err=%v", marketPrice, err)
	}
	marketPrices, err := client.GetMarketPrices(ctx, []int{1, 2})
	if err != nil || len(marketPrices) != 2 || marketPrices[1].ProductID != 2 {
		t.Fatalf("market prices = %+v, err=%v", marketPrices, err)
	}
	productOrders, err := client.GetAccountProductOrders(ctx, 2, sender)
	if err != nil || productOrders.ProductID != 2 {
		t.Fatalf("product orders = %+v, err=%v", productOrders, err)
	}
	multiOrders, err := client.GetAccountMultiProductsOrders(ctx, []int64{1, 2}, sender)
	if err != nil || len(multiOrders.ProductOrders) != 2 {
		t.Fatalf("multi-product orders = %+v, err=%v", multiOrders, err)
	}
	order, err := client.GetOrder(ctx, 2, "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || order.ProductID != 2 || order.Digest == "" {
		t.Fatalf("order = %+v, err=%v", order, err)
	}
}

func TestNadoContractAndDiscoveryFixtureValidation(t *testing.T) {
	t.Parallel()

	var envelope ApiV1Response
	if err := json.Unmarshal([]byte(nadoFixtureBody(t, "contracts.json")), &envelope); err != nil {
		t.Fatal(err)
	}
	var contract ContractV1
	if err := json.Unmarshal(envelope.Data, &contract); err != nil {
		t.Fatal(err)
	}
	client := newNadoQueryTestnetClient(t)
	if err := client.ValidateContractV1(contract); err != nil {
		t.Fatal(err)
	}

	missingOfficialField := ContractV1{}
	if err := json.Unmarshal([]byte(`{"chain_id":"763373","endpoint_address":"0x4444444444444444444444444444444444444444"}`), &missingOfficialField); err != nil {
		t.Fatal(err)
	}
	if err := client.ValidateContractV1(missingOfficialField); !errors.Is(err, ErrNadoContractEndpointMalformed) {
		t.Fatalf("missing endpoint_addr err = %v", err)
	}

	wrongChain := contract
	wrongChain.ChainID = "57073"
	if err := client.ValidateContractV1(wrongChain); !errors.Is(err, ErrNadoContractProfileMismatch) {
		t.Fatalf("wrong chain err = %v", err)
	}
	malformed := contract
	malformed.EndpointAddress = "0x1234"
	if err := client.ValidateContractV1(malformed); !errors.Is(err, ErrNadoContractEndpointMalformed) {
		t.Fatalf("malformed endpoint err = %v", err)
	}

	allProductsEnvelope := ApiV1Response{}
	if err := json.Unmarshal([]byte(nadoFixtureBody(t, "all_products.json")), &allProductsEnvelope); err != nil {
		t.Fatal(err)
	}
	var allProducts AllProductsResponse
	if err := json.Unmarshal(allProductsEnvelope.Data, &allProducts); err != nil {
		t.Fatal(err)
	}
	symbolsEnvelope := ApiV1Response{}
	if err := json.Unmarshal([]byte(nadoFixtureBody(t, "symbols.json")), &symbolsEnvelope); err != nil {
		t.Fatal(err)
	}
	var symbols SymbolsInfo
	if err := json.Unmarshal(symbolsEnvelope.Data, &symbols); err != nil {
		t.Fatal(err)
	}

	if err := ValidateNadoProductDiscovery(allProducts, symbols); err != nil {
		t.Fatal(err)
	}

	unknown := symbols
	unknown.Symbols = cloneSymbols(symbols.Symbols)
	unknown.Symbols["UNKNOWN"] = Symbol{ProductID: 999, Type: string(MarketTypePerp), Symbol: "UNKNOWN-PERP", TradingStatus: TradingStatusLive}
	if err := ValidateNadoProductDiscovery(allProducts, unknown); !errors.Is(err, ErrNadoDiscoveryUnknownProduct) {
		t.Fatalf("unknown err = %v", err)
	}

	mismatch := symbols
	mismatch.Symbols = cloneSymbols(symbols.Symbols)
	eth := mismatch.Symbols["ETH_USDT0"]
	eth.PriceIncrementX18 = "1"
	mismatch.Symbols["ETH_USDT0"] = eth
	if err := ValidateNadoProductDiscovery(allProducts, mismatch); !errors.Is(err, ErrNadoDiscoveryProductMismatch) {
		t.Fatalf("mismatch err = %v", err)
	}
}

func TestValidateNadoProductDiscoveryAcceptsCurrentSymbolsWithoutProductZeroRow(t *testing.T) {
	products := AllProductsResponse{
		SpotProducts: []SpotProduct{
			{ProductID: 0},
			{ProductID: 1, BookInfo: ProductBookInfo{PriceIncrementX18: "10", SizeIncrement: "20", MinSize: "30"}},
			{ProductID: 11, BookInfo: ProductBookInfo{PriceIncrementX18: "1", SizeIncrement: "1", MinSize: "1"}},
		},
		PerpProducts: []PerpProduct{{ProductID: 2, BookInfo: ProductBookInfo{PriceIncrementX18: "40", SizeIncrement: "50", MinSize: "60"}}},
	}
	symbols := SymbolsInfo{Symbols: map[string]Symbol{
		"WBTC":     {ProductID: 1, Type: string(MarketTypeSpot), Symbol: "WBTC", PriceIncrementX18: "10", SizeIncrement: "20", MinSize: "30", TradingStatus: TradingStatusLive},
		"BTC-PERP": {ProductID: 2, Type: string(MarketTypePerp), Symbol: "BTC-PERP", PriceIncrementX18: "40", SizeIncrement: "50", MinSize: "60", TradingStatus: TradingStatusLive},
	}}
	if err := ValidateNadoProductDiscovery(products, symbols); err != nil {
		t.Fatalf("current symbols schema rejected: %v", err)
	}
}

func TestNadoContractV2ParsesMarkPrice(t *testing.T) {
	t.Parallel()

	var contracts ContractV2Map
	if err := json.Unmarshal([]byte(nadoFixtureBody(t, "contracts_v2.json")), &contracts); err != nil {
		t.Fatal(err)
	}
	if contracts["ETH-PERP_USDT0"].MarkPrice == nil || *contracts["ETH-PERP_USDT0"].MarkPrice != 2504.5 {
		t.Fatalf("mark_price = %v", contracts["ETH-PERP_USDT0"].MarkPrice)
	}
	var missing ContractV2
	if err := json.Unmarshal([]byte(`{"product_id":2}`), &missing); err != nil {
		t.Fatal(err)
	}
	if missing.MarkPrice != nil || missing.IndexPrice != nil || missing.OpenInterest != nil {
		t.Fatalf("missing reference fields looked present: %+v", missing)
	}
	if err := json.Unmarshal([]byte(`{"mark_price":"not-a-number"}`), &missing); err == nil {
		t.Fatal("malformed mark_price unexpectedly decoded")
	}
}

func TestNadoAccountFixturePreservesHealthAndSignedLiabilities(t *testing.T) {
	t.Parallel()

	var envelope ApiV1Response
	if err := json.Unmarshal([]byte(nadoFixtureBody(t, "subaccount_info.json")), &envelope); err != nil {
		t.Fatal(err)
	}
	var account AccountInfo
	if err := json.Unmarshal(envelope.Data, &account); err != nil {
		t.Fatal(err)
	}
	if len(account.Healths) != 3 || account.Healths[0].Liabilities != "200000000000000000000" || account.Healths[2].Health != "900000000000000000000" {
		t.Fatalf("healths = %+v", account.Healths)
	}
	if len(account.SpotBalances) != 2 || account.SpotBalances[1].Balance.Amount != "-2000000000000000000" {
		t.Fatalf("spot balances = %+v", account.SpotBalances)
	}
	if len(account.PerpBalances) != 1 || account.PerpBalances[0].Balance.VQuoteBalance == nil || *account.PerpBalances[0].Balance.VQuoteBalance != "-1250000000000000000000" {
		t.Fatalf("perp balances = %+v", account.PerpBalances)
	}
}

func TestNadoAccountSnapshotUsesReceiptTimeForFreshness(t *testing.T) {
	fixed := time.Date(2026, 7, 10, 12, 0, 0, 123, time.UTC)
	server := nadoQueryFixtureServer(t, func(t *testing.T, r *http.Request, req map[string]any) {
		if req["type"] != "subaccount_info" {
			t.Fatalf("request = %#v", req)
		}
	}, map[string]string{"subaccount_info": nadoFixtureBody(t, "subaccount_info.json")})
	defer server.Close()
	client := newNadoQueryClientForServer(t, server).WithClock(func() time.Time { return fixed })
	snapshot, err := client.GetSubaccountInfoSnapshot(context.Background(), SubaccountInfoRequest{
		Subaccount: "0x1111111111111111111111111111111111111111000000000000000000000000",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.ReceivedAt.Equal(fixed) || !snapshot.FreshAt(fixed.Add(time.Second), 2*time.Second) || snapshot.FreshAt(fixed.Add(3*time.Second), 2*time.Second) || snapshot.FreshAt(fixed.Add(-time.Second), 2*time.Second) {
		t.Fatalf("snapshot receipt/freshness = %s", snapshot.ReceivedAt)
	}
	if snapshot.Account.SpotBalances[1].Balance.Amount != "-2000000000000000000" || snapshot.Account.Healths[0].Liabilities == "" {
		t.Fatalf("snapshot lost account fidelity: %+v", snapshot.Account)
	}
}

func TestNadoMatchesReportUsesArchiveProfileAndFixture(t *testing.T) {
	client := newNadoTestnetClient(t)
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != client.Profile().ArchiveV1URL() {
			return nil, fmt.Errorf("unexpected archive request: %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Accept-Encoding"); !strings.Contains(got, "gzip") {
			return nil, fmt.Errorf("archive Accept-Encoding=%q, want gzip", got)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["matches"] == nil {
			return nil, fmt.Errorf("matches request missing body")
		}
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write([]byte(nadoFixtureBody(t, "matches.json"))); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		header := make(http.Header)
		header.Set("Content-Encoding", "gzip")
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(compressed.Bytes())), Request: request}, nil
	})})
	report, err := client.GetMatches(context.Background(), "0x1111111111111111111111111111111111111111000000000000000000000000", []int64{1}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 1 || report.Matches[0].Digest == "" || report.Matches[0].Timestamp == "" {
		t.Fatalf("matches report = %+v", report)
	}
}

func cloneSymbols(in map[string]Symbol) map[string]Symbol {
	out := make(map[string]Symbol, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

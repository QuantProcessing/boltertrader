package nado

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The chain ID sits in the EIP-712 domain, so a signer on the wrong chain
// produces a signature the venue rejects as malformed rather than a transport
// error that names the mistake. This used to build its two signers from a
// mainnet and a testnet profile, but the profile was only ever a way to obtain
// two chain IDs — the property is the signer's, and it survives the profile
// having only one chain to offer.
func TestSignerChainIDReachesTheEIP712Domain(t *testing.T) {
	privateKey := fmt.Sprintf("%064x", 1)
	signer, err := NewSigner(privateKey, 57073)
	if err != nil {
		t.Fatal(err)
	}
	otherChain, err := NewSigner(privateKey, 763373)
	if err != nil {
		t.Fatal(err)
	}
	order := TxOrder{
		Sender: BuildSender(signer.GetAddress(), "default"), ProductId: 2,
		Amount: "100000000000000000", PriceX18: "2500000000000000000000",
		Nonce: "1849300000000000000", Expiration: "4000000000", Appendix: "1",
	}
	_, digest, err := signer.SignOrder(order, GenOrderVerifyingContract(2))
	if err != nil {
		t.Fatal(err)
	}
	_, otherChainDigest, err := otherChain.SignOrder(order, GenOrderVerifyingContract(2))
	if err != nil {
		t.Fatal(err)
	}
	if digest == otherChainDigest {
		t.Fatal("different chain IDs produced the same EIP-712 digest")
	}
}

func TestSignerRejectsMalformedTypedDataBeforeSigning(t *testing.T) {
	signer, err := NewSigner(fmt.Sprintf("%064x", 1), 763373)
	if err != nil {
		t.Fatal(err)
	}
	valid := TxOrder{
		Sender: BuildSender(signer.GetAddress(), "default"), ProductId: 2,
		Amount: "100000000000000000", PriceX18: "2500000000000000000000",
		Nonce: "1849300000000000000", Expiration: "4000000000", Appendix: "1",
	}
	tests := []struct {
		name   string
		mutate func(*TxOrder)
	}{
		{name: "invalid sender", mutate: func(order *TxOrder) { order.Sender = "0x01" }},
		{name: "invalid amount", mutate: func(order *TxOrder) { order.Amount = "not-a-number" }},
		{name: "invalid price", mutate: func(order *TxOrder) { order.PriceX18 = "" }},
		{name: "invalid nonce", mutate: func(order *TxOrder) { order.Nonce = "-" }},
		{name: "amount over int128", mutate: func(order *TxOrder) { order.Amount = new(big.Int).Lsh(big.NewInt(1), 127).String() }},
		{name: "nonce over uint64", mutate: func(order *TxOrder) { order.Nonce = new(big.Int).Lsh(big.NewInt(1), 64).String() }},
		{name: "appendix over uint128", mutate: func(order *TxOrder) { order.Appendix = new(big.Int).Lsh(big.NewInt(1), 128).String() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			order := valid
			test.mutate(&order)
			if _, _, err := signer.SignOrder(order, GenOrderVerifyingContract(2)); err == nil {
				t.Fatal("malformed typed data unexpectedly signed")
			}
		})
	}
}

func TestEIP712SigningFixtures(t *testing.T) {
	signer, err := NewSigner(fmt.Sprintf("%064x", 1), 763373)
	if err != nil {
		t.Fatal(err)
	}
	sender := BuildSender(signer.GetAddress(), "default")
	order := TxOrder{
		Sender: sender, ProductId: 2, Amount: "-100000000000000000", PriceX18: "2500000000000000000000",
		Nonce: "1849300000000000000", Expiration: "4000000000", Appendix: "1",
	}
	orderSignature, orderDigest, err := signer.SignOrder(order, GenOrderVerifyingContract(2))
	if err != nil {
		t.Fatal(err)
	}
	cancelSignature, err := signer.SignCancelProductOrders(TxCancelProductOrders{
		Sender: sender, ProductIds: []int64{1, 2}, Nonce: "1849300000000000001",
	}, "0x4444444444444444444444444444444444444444")
	if err != nil {
		t.Fatal(err)
	}
	authSignature, err := signer.SignStreamAuthentication(TxStreamAuth{
		Sender: sender, Expiration: "1900000000000",
	}, "0x4444444444444444444444444444444444444444")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"sender":           "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf64656661756c740000000000",
		"order_signature":  "0x877e29da2eca722d3ca3bcdf1a14bb623a31bfcd19ace2d3a4b297c9f8a01520180d03f3e5eb7c1bc9e46b3107e3b8b5d7288d1bf6836ecdc1ec63041221bbc21c",
		"order_digest":     "0x07b23f2a7658cc991f7a6b6618c8432edd1a36f1a70b5c75ab9e6fdf55f4f840",
		"cancel_signature": "0x74b9f9a86368a3b400277d445af3527e5e267bbe3f60e08033a05de84b645ece16036673038ab7872c218db3432f14d65e4da7b5711c8be2e6a0729a163107461b",
		"auth_signature":   "0x29c4bce57175e922d06637aec3dd3b6a6d6b6f54350fb4bbec9587f8408dd0ef5035742b43904794d694a2165e6ab9231675c73260ac13cd235b1d8e15b221bc1c",
	}
	got := map[string]string{
		"sender": sender, "order_signature": orderSignature, "order_digest": orderDigest,
		"cancel_signature": cancelSignature, "auth_signature": authSignature,
	}
	for field, expected := range want {
		if got[field] != expected {
			t.Fatalf("%s = %s, want %s", field, got[field], expected)
		}
	}
}

func TestCredentialsRejectSubaccountNameLongerThanBytes12(t *testing.T) {
	client := newNadoClient(t)
	if _, err := client.WithCredentials(fmt.Sprintf("%064x", 1), "thirteen-bytes"); err == nil {
		t.Fatal("oversized subaccount name unexpectedly accepted")
	}
}

func TestRESTWriteDiscoversContractsAndCarriesSpotLeverageFalse(t *testing.T) {
	client, err := newNadoClient(t).WithCredentials(fmt.Sprintf("%064x", 1), "default")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	var executeBody string
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body []byte
		if request.Body != nil {
			body, _ = io.ReadAll(request.Body)
		}
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.String())
		if strings.HasSuffix(request.URL.Path, "/execute") {
			executeBody = string(body)
		}
		mu.Unlock()
		var payload string
		switch request.URL.Query().Get("type") {
		case "contracts":
			payload = `{"status":"success","data":{"chain_id":"57073","endpoint_addr":"0x4444444444444444444444444444444444444444"},"request_type":"query_contracts"}`
		case "status":
			payload = nadoFixtureBody(t, "status.json")
		case "all_products":
			payload = nadoFixtureBody(t, "all_products.json")
		case "symbols":
			payload = nadoFixtureBody(t, "symbols.json")
		}
		if strings.HasSuffix(request.URL.Path, "/execute") {
			payload = `{"status":"success","signature":"<redacted>","data":{"digest":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"request_type":"execute_place_order","id":50001}`
		}
		if payload == "" {
			return nil, fmt.Errorf("unexpected request: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
	})})

	response, err := client.PlaceOrder(context.Background(), ClientOrderInput{
		ProductId: 1, Price: "2500", Amount: "0.1", Side: OrderSideBuy,
		OrderType: OrderTypeLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Digest == "" {
		t.Fatalf("response = %+v", response)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 5 || !strings.Contains(requests[0], "/query?type=contracts") || !strings.Contains(requests[1], "/query?type=status") || !strings.Contains(requests[2], "/query?type=all_products") || !strings.Contains(requests[3], "/query?type=symbols") || !strings.Contains(requests[4], "/execute") {
		t.Fatalf("requests = %v", requests)
	}
	if !strings.Contains(executeBody, `"spot_leverage":false`) {
		t.Fatalf("execute body missing spot_leverage=false: %s", executeBody)
	}
	if strings.Contains(executeBody, "private") {
		t.Fatalf("execute body leaked private material: %s", executeBody)
	}
}

func TestRESTWriteRejectsWrongDiscoveredChainBeforeExecute(t *testing.T) {
	client, err := newNadoClient(t).WithCredentials(fmt.Sprintf("%064x", 1), "default")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		payload := `{"status":"success","data":{"chain_id":"763373","endpoint_addr":"0x05ec92d78ed421f3d3ada77ffde167106565974e"},"request_type":"query_contracts"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
	})})
	_, err = client.PlaceOrder(context.Background(), ClientOrderInput{
		ProductId: 2, Price: "2500", Amount: "0.1", Side: OrderSideBuy, OrderType: OrderTypeLimit,
	})
	if err == nil || !strings.Contains(err.Error(), "chain") {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want contracts query only", calls)
	}
}

func TestContractDiscoveryIsRevalidatedOnEveryWritePreparation(t *testing.T) {
	client := newNadoClient(t)
	calls := 0
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		chainID := "57073"
		if calls == 2 {
			chainID = "763373"
		}
		payload := fmt.Sprintf(`{"status":"success","data":{"chain_id":%q,"endpoint_addr":"0x4444444444444444444444444444444444444444"}}`, chainID)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
	})})
	if _, err := client.ensureContracts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ensureContracts(context.Background()); !errors.Is(err, ErrNadoContractProfileMismatch) {
		t.Fatalf("second discovery error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("contract discovery calls = %d", calls)
	}
}

func TestRESTWriteRejectsInactiveProductBeforeSigningOrExecute(t *testing.T) {
	client, err := newNadoClient(t).WithCredentials(fmt.Sprintf("%064x", 1), "default")
	if err != nil {
		t.Fatal(err)
	}

	var requests []string
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.String())
		var payload string
		switch request.URL.Query().Get("type") {
		case "contracts":
			payload = `{"status":"success","data":{"chain_id":"57073","endpoint_addr":"0x4444444444444444444444444444444444444444"}}`
		case "status":
			payload = nadoFixtureBody(t, "status.json")
		case "all_products":
			payload = nadoFixtureBody(t, "all_products.json")
		case "symbols":
			payload = nadoSymbolsFixtureWithStatus(t, "ETH_USDT0", TradingStatusNotTradable)
		default:
			return nil, fmt.Errorf("unexpected request: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
	})})

	_, err = client.PlaceOrder(context.Background(), ClientOrderInput{
		ProductId: 1, Price: "2500", Amount: "0.1", Side: OrderSideBuy, OrderType: OrderTypeLimit,
	})
	if !errors.Is(err, ErrNadoDiscoveryInactiveProduct) {
		t.Fatalf("error = %v", err)
	}
	for _, request := range requests {
		if strings.Contains(request, "/execute") {
			t.Fatalf("inactive product reached execute: %v", requests)
		}
	}
}

func TestRESTWriteRejectsMismatchedDiscoveredProductBeforeExecute(t *testing.T) {
	discovered := nadoTestDiscoveredProduct(1, MarketTypeSpot)
	tests := []struct {
		name   string
		mutate func(*DiscoveredProduct)
		run    func(*Client, ClientOrderInput) error
	}{
		{
			name:   "place product id mismatch",
			mutate: func(product *DiscoveredProduct) { product.ProductID = 2 },
			run: func(client *Client, input ClientOrderInput) error {
				_, err := client.PlaceOrder(context.Background(), input)
				return err
			},
		},
		{
			name:   "cancel and place symbol product id mismatch",
			mutate: func(product *DiscoveredProduct) { product.Symbol.ProductID = 2 },
			run: func(client *Client, input ClientOrderInput) error {
				_, err := client.CancelAndPlace(context.Background(), CancelOrdersInput{
					ProductIds: []int64{1},
					Digests:    []string{"0x1111111111111111111111111111111111111111111111111111111111111111"},
				}, input)
				return err
			},
		},
		{
			name:   "place product type mismatch",
			mutate: func(product *DiscoveredProduct) { product.ProductType = MarketTypePerp },
			run: func(client *Client, input ClientOrderInput) error {
				_, err := client.PlaceOrder(context.Background(), input)
				return err
			},
		},
		{
			name:   "cancel and place precision mismatch",
			mutate: func(product *DiscoveredProduct) { product.Symbol.PriceIncrementX18 = "1" },
			run: func(client *Client, input ClientOrderInput) error {
				_, err := client.CancelAndPlace(context.Background(), CancelOrdersInput{
					ProductIds: []int64{1},
					Digests:    []string{"0x1111111111111111111111111111111111111111111111111111111111111111"},
				}, input)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := newNadoClient(t).WithCredentials(fmt.Sprintf("%064x", 1), "default")
			if err != nil {
				t.Fatal(err)
			}
			var requests []string
			client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests = append(requests, request.Method+" "+request.URL.String())
				if strings.HasSuffix(request.URL.Path, "/execute") {
					t.Fatalf("mismatched discovered product reached execute: %v", requests)
				}
				payload := `{"status":"success","data":{"chain_id":"57073","endpoint_addr":"0x4444444444444444444444444444444444444444"},"request_type":"query_contracts"}`
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
			})})
			product := discovered
			test.mutate(&product)
			input := ClientOrderInput{
				ProductId:         1,
				Price:             "2500",
				Amount:            "0.1",
				Side:              OrderSideBuy,
				OrderType:         OrderTypeLimit,
				DiscoveredProduct: &product,
			}
			if err := test.run(client, input); !errors.Is(err, ErrNadoDiscoveryProductMismatch) {
				t.Fatalf("error = %v, want ErrNadoDiscoveryProductMismatch", err)
			}
			if len(requests) != 1 || !strings.Contains(requests[0], "/query?type=contracts") {
				t.Fatalf("requests = %v, want only contracts preflight", requests)
			}
		})
	}
}

func TestWSPrepareOrderRejectsMismatchedDiscoveredProduct(t *testing.T) {
	client, err := newNadoClient(t).WithCredentials(fmt.Sprintf("%064x", 1), "default")
	if err != nil {
		t.Fatal(err)
	}
	var requests []string
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.String())
		if strings.HasSuffix(request.URL.Path, "/execute") {
			t.Fatalf("mismatched WS prepared product reached execute: %v", requests)
		}
		payload := `{"status":"success","data":{"chain_id":"57073","endpoint_addr":"0x4444444444444444444444444444444444444444"},"request_type":"query_contracts"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
	})})
	ws, err := NewWsApiClient(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	product := nadoTestDiscoveredProduct(1, MarketTypeSpot)
	product.Symbol.Type = string(MarketTypePerp)
	_, err = ws.PrepareOrder(context.Background(), ClientOrderInput{
		ProductId:         1,
		Price:             "2500",
		Amount:            "0.1",
		Side:              OrderSideBuy,
		OrderType:         OrderTypeLimit,
		DiscoveredProduct: &product,
	})
	if !errors.Is(err, ErrNadoDiscoveryProductMismatch) {
		t.Fatalf("error = %v, want ErrNadoDiscoveryProductMismatch", err)
	}
	if len(requests) != 1 || !strings.Contains(requests[0], "/query?type=contracts") {
		t.Fatalf("requests = %v, want only contracts preflight", requests)
	}
}

func TestResolveProductRejectsFailedSequencerBeforeMetadata(t *testing.T) {
	client := newNadoClient(t)
	var requests []string
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		payload := `{"status":"success","data":"failed","request_type":"query_status"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
	})})

	_, err := client.ResolveProduct(context.Background(), 1)
	if !errors.Is(err, ErrNadoSequencerInactive) {
		t.Fatalf("error = %v", err)
	}
	if len(requests) != 1 || !strings.Contains(requests[0], "type=status") {
		t.Fatalf("requests = %v", requests)
	}
}

func TestResolveProductRefreshesTradingStatusEveryCall(t *testing.T) {
	client := newNadoClient(t)
	statusCalls := 0
	symbolCalls := 0
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload string
		switch request.URL.Query().Get("type") {
		case "status":
			statusCalls++
			payload = nadoFixtureBody(t, "status.json")
		case "all_products":
			payload = nadoFixtureBody(t, "all_products.json")
		case "symbols":
			symbolCalls++
			payload = nadoFixtureBody(t, "symbols.json")
			if symbolCalls == 2 {
				payload = nadoSymbolsFixtureWithStatus(t, "ETH_USDT0", TradingStatusNotTradable)
			}
		default:
			return nil, fmt.Errorf("unexpected request: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
	})})

	if _, err := client.ResolveProduct(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResolveProduct(context.Background(), 1); !errors.Is(err, ErrNadoDiscoveryInactiveProduct) {
		t.Fatalf("second resolve error = %v", err)
	}
	if statusCalls != 2 || symbolCalls != 2 {
		t.Fatalf("status calls=%d symbol calls=%d", statusCalls, symbolCalls)
	}
}

func nadoSymbolsFixtureWithStatus(t *testing.T, symbol string, status TradingStatus) string {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal([]byte(nadoFixtureBody(t, "symbols.json")), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope["data"].(map[string]any)
	symbols := data["symbols"].(map[string]any)
	entry := symbols[symbol].(map[string]any)
	entry["trading_status"] = string(status)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestPrepareNadoOrderInputSafetyEnvelope(t *testing.T) {
	spot := DiscoveredProduct{
		ProductID: 1, ProductType: MarketTypeSpot,
		Symbol: Symbol{ProductID: 1, Type: string(MarketTypeSpot), TradingStatus: TradingStatusLive},
	}
	valid := ClientOrderInput{
		ProductId: 1, Price: "2500.125", Amount: "0.1", Side: OrderSideBuy, OrderType: OrderTypeLimit,
	}
	normalized, price, amount, err := prepareNadoOrderInput(spot, valid)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SpotLeverage == nil || *normalized.SpotLeverage || price.String() != "2500125000000000000000" || amount.String() != "100000000000000000" {
		t.Fatalf("normalized=%+v price=%s amount=%s", normalized, price, amount)
	}

	trueValue := true
	tests := []struct {
		name   string
		status TradingStatus
		mutate func(*ClientOrderInput)
	}{
		{name: "invalid price", status: TradingStatusLive, mutate: func(input *ClientOrderInput) { input.Price = "nope" }},
		{name: "over precision", status: TradingStatusLive, mutate: func(input *ClientOrderInput) { input.Amount = "0.0000000000000000001" }},
		{name: "spot borrowing", status: TradingStatusLive, mutate: func(input *ClientOrderInput) { input.SpotLeverage = &trueValue }},
		{name: "post only mode", status: TradingStatusPostOnly, mutate: func(input *ClientOrderInput) {}},
		{name: "reduce only mode", status: TradingStatusReduceOnly, mutate: func(input *ClientOrderInput) {}},
		{name: "not tradable", status: TradingStatusNotTradable, mutate: func(input *ClientOrderInput) {}},
		{name: "unsupported stop type", status: TradingStatusLive, mutate: func(input *ClientOrderInput) { input.OrderType = OrderTypeStopLoss }},
		{name: "unsupported trigger appendix", status: TradingStatusLive, mutate: func(input *ClientOrderInput) { input.TriggerType = TriggerTypePrice }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			product := spot
			product.Symbol.TradingStatus = test.status
			input := valid
			test.mutate(&input)
			if _, _, _, err := prepareNadoOrderInput(product, input); err == nil {
				t.Fatal("unsafe order input unexpectedly accepted")
			}
		})
	}

	postOnly := valid
	postOnly.PostOnly = true
	spot.Symbol.TradingStatus = TradingStatusPostOnly
	if _, _, _, err := prepareNadoOrderInput(spot, postOnly); err != nil {
		t.Fatalf("valid post-only order rejected: %v", err)
	}
	perp := spot
	perp.ProductType = MarketTypePerp
	perp.Symbol.Type = string(MarketTypePerp)
	perp.Symbol.TradingStatus = TradingStatusReduceOnly
	reduceOnly := valid
	reduceOnly.ReduceOnly = true
	if _, _, _, err := prepareNadoOrderInput(perp, reduceOnly); err != nil {
		t.Fatalf("valid reduce-only order rejected: %v", err)
	}
}

func nadoTestDiscoveredProduct(productID int64, productType MarketType) DiscoveredProduct {
	book := ProductBookInfo{
		PriceIncrementX18: "10",
		SizeIncrement:     "20",
		MinSize:           "30",
	}
	return DiscoveredProduct{
		ProductID:   productID,
		ProductType: productType,
		BookInfo:    book,
		Symbol: Symbol{
			ProductID:         int(productID),
			Type:              string(productType),
			Symbol:            "ETH_USDT0",
			PriceIncrementX18: book.PriceIncrementX18,
			SizeIncrement:     book.SizeIncrement,
			MinSize:           book.MinSize,
			TradingStatus:     TradingStatusLive,
		},
	}
}

func TestNadoNonceIsUniqueAcrossConcurrentCallers(t *testing.T) {
	const count = 2000
	values := make(chan int64, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			values <- GetNonce()
		}()
	}
	wg.Wait()
	close(values)

	seen := make(map[int64]struct{}, count)
	for value := range values {
		if value <= 0 {
			t.Fatalf("nonce = %d", value)
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("duplicate nonce = %d", value)
		}
		seen[value] = struct{}{}
	}
}

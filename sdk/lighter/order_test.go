package lighter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	schnorr "github.com/elliottech/poseidon_crypto/signature/schnorr"
)

func TestClientBuildCreateOrderTxSignsCreateOrderPayload(t *testing.T) {
	fixedNow := time.UnixMilli(1710000000000).UTC()
	transport := lighterOrderRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/nextNonce" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":200,"nonce":123}`)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})

	newClient := func() *Client {
		client := NewClient().
			WithCredentials(strings.Repeat("01", 40), 42, 7).
			WithClock(func() time.Time { return fixedNow })
		client.BaseURL = "https://lighter.test"
		client.HTTPClient = &http.Client{Transport: transport}
		return client
	}
	req := CreateOrderRequest{
		MarketId:      9,
		Price:         12345,
		BaseAmount:    678,
		IsAsk:         1,
		OrderType:     OrderTypeLimit,
		TimeInForce:   OrderTimeInForceGoodTillTime,
		ClientOrderId: 55,
		ReduceOnly:    1,
		TriggerPrice:  12000,
		OrderExpiry:   Default28DayOrderExpiry,
	}
	tx, err := newClient().BuildCreateOrderTx(context.Background(), req)
	if err != nil {
		t.Fatalf("BuildCreateOrderTx: %v", err)
	}
	if tx["tx_type"] != "14" {
		t.Fatalf("unexpected tx_type: %s", tx["tx_type"])
	}
	var info struct {
		Nonce            int64
		AccountIndex     int64
		ApiKeyIndex      uint32
		MarketIndex      uint32
		ClientOrderIndex int64
		BaseAmount       int64
		Price            uint32
		IsAsk            uint32
		Type             uint32
		TimeInForce      uint32
		ReduceOnly       uint32
		TriggerPrice     uint32
		OrderExpiry      int64
		ExpiredAt        int64
		Sig              []byte
	}
	if err := json.Unmarshal([]byte(tx["tx_info"]), &info); err != nil {
		t.Fatalf("unmarshal tx_info: %v", err)
	}
	if info.Nonce != 123 || info.AccountIndex != 42 || info.ApiKeyIndex != 7 ||
		info.MarketIndex != 9 || info.ClientOrderIndex != 55 || info.BaseAmount != 678 ||
		info.Price != 12345 || info.IsAsk != 1 || info.Type != OrderTypeLimit ||
		info.TimeInForce != OrderTimeInForceGoodTillTime || info.ReduceOnly != 1 ||
		info.TriggerPrice != 12000 || info.OrderExpiry != Default28DayOrderExpiry ||
		info.ExpiredAt != fixedNow.Add(28*24*time.Hour).UnixMilli() {
		t.Fatal("create-order payload fields do not match the deterministic request")
	}
	if len(info.Sig) == 0 {
		t.Fatal("expected signature")
	}

	hash, err := HashCreateOrder(newClient().ChainId, &CreateOrderInfo{
		AccountIndex:     info.AccountIndex,
		ApiKeyIndex:      info.ApiKeyIndex,
		MarketIndex:      info.MarketIndex,
		ClientOrderIndex: info.ClientOrderIndex,
		BaseAmount:       info.BaseAmount,
		Price:            info.Price,
		IsAsk:            info.IsAsk,
		Type:             info.Type,
		TimeInForce:      info.TimeInForce,
		ReduceOnly:       info.ReduceOnly,
		TriggerPrice:     info.TriggerPrice,
		OrderExpiry:      info.OrderExpiry,
		ExpiredAt:        info.ExpiredAt,
		Nonce:            info.Nonce,
	})
	if err != nil {
		t.Fatal("create-order payload hash could not be recomputed")
	}
	pubKey := newClient().KeyManager.PubKeyBytes()
	if len(info.Sig) != 80 {
		t.Fatalf("create-order signature length = %d, want 80", len(info.Sig))
	}
	if err := schnorr.Validate(pubKey[:], hash, info.Sig); err != nil {
		t.Fatal("create-order signature validation failed")
	}
}

func TestClientCancelOrderUsesDeterministicExpiryAndSignature(t *testing.T) {
	fixedNow := time.UnixMilli(1710000000000).UTC()
	type signedCancel struct {
		AccountIndex int64
		ApiKeyIndex  uint32
		MarketIndex  uint32
		Index        int64
		Nonce        int64
		ExpiredAt    int64
		Sig          []byte
	}
	var captured []signedCancel
	transport := lighterOrderRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/v1/nextNonce":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":200,"nonce":321}`)),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		case "/api/v1/sendTx":
			reader, err := r.MultipartReader()
			if err != nil {
				t.Fatalf("open multipart request: %v", err)
			}
			fields := map[string]string{}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("read multipart request: %v", err)
				}
				value, err := io.ReadAll(io.LimitReader(part, 1<<20))
				if err != nil {
					t.Fatalf("read multipart field %s: %v", part.FormName(), err)
				}
				fields[part.FormName()] = string(value)
			}
			if fields["tx_type"] != "15" {
				t.Fatalf("cancel tx_type = %q, want 15", fields["tx_type"])
			}
			var signed signedCancel
			if err := json.Unmarshal([]byte(fields["tx_info"]), &signed); err != nil {
				t.Fatal("cancel tx_info is not valid JSON")
			}
			captured = append(captured, signed)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":200,"tx_hash":"0xcancel"}`)),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		return nil, nil
	})

	newClient := func() *Client {
		client := NewClient().
			WithCredentials(strings.Repeat("01", 40), 42, 7).
			WithClock(func() time.Time { return fixedNow })
		client.BaseURL = "https://lighter.test"
		client.HTTPClient = &http.Client{Transport: transport}
		return client
	}
	for range 2 {
		if _, err := newClient().CancelOrder(context.Background(), CancelOrderRequest{MarketId: 9, OrderId: 55}); err != nil {
			t.Fatalf("CancelOrder: %v", err)
		}
	}
	if len(captured) != 2 {
		t.Fatalf("captured cancel payloads = %d, want 2", len(captured))
	}
	wantExpiry := fixedNow.Add(7 * 24 * time.Hour).UnixMilli()
	if captured[0].ExpiredAt != wantExpiry || captured[1].ExpiredAt != wantExpiry {
		t.Fatalf("cancel ExpiredAt mismatch: got %d and %d, want %d", captured[0].ExpiredAt, captured[1].ExpiredAt, wantExpiry)
	}
	for i, payload := range captured {
		if len(payload.Sig) != 80 {
			t.Fatalf("cancel signature %d length = %d, want 80", i, len(payload.Sig))
		}
		hash, err := HashCancelOrder(newClient().ChainId, &CancelOrderInfo{
			AccountIndex: payload.AccountIndex,
			ApiKeyIndex:  payload.ApiKeyIndex,
			MarketIndex:  payload.MarketIndex,
			Index:        payload.Index,
			Nonce:        payload.Nonce,
			ExpiredAt:    payload.ExpiredAt,
		})
		if err != nil {
			t.Fatalf("cancel payload %d hash could not be recomputed", i)
		}
		pubKey := newClient().KeyManager.PubKeyBytes()
		if err := schnorr.Validate(pubKey[:], hash, payload.Sig); err != nil {
			t.Fatalf("cancel signature %d validation failed", i)
		}
	}
}

func TestClientWithClockConcurrentAccess(t *testing.T) {
	client := NewClient()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			client.WithClock(func() time.Time { return time.Unix(int64(i+1), 0) })
		}()
		go func() {
			defer wg.Done()
			_ = client.nowTime()
		}()
	}
	wg.Wait()
}

type lighterOrderRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn lighterOrderRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestClient_PlaceOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_PRICE", "LIGHTER_TEST_ORDER_BASE_AMOUNT")
	got, err := client.PlaceOrder(context.Background(), CreateOrderRequest{
		MarketId:      lighterMarketID(t),
		Price:         uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_PRICE", 0)),
		BaseAmount:    lighterInt64Env(t, "LIGHTER_TEST_ORDER_BASE_AMOUNT", 0),
		IsAsk:         uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_IS_ASK", 0)),
		OrderType:     OrderTypeLimit,
		TimeInForce:   OrderTimeInForcePostOnly,
		ClientOrderId: lighterInt64Env(t, "LIGHTER_TEST_CLIENT_ORDER_ID", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected create order response")
	}
}

func TestClient_CancelOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_ID")
	got, err := client.CancelOrder(context.Background(), CancelOrderRequest{
		MarketId: lighterMarketID(t),
		OrderId:  lighterInt64Env(t, "LIGHTER_TEST_ORDER_ID", 0),
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected cancel order response")
	}
}

func TestClient_ModifyOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_ID", "LIGHTER_TEST_ORDER_PRICE", "LIGHTER_TEST_ORDER_BASE_AMOUNT")
	got, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{
		MarketId:   lighterMarketID(t),
		OrderIndex: lighterInt64Env(t, "LIGHTER_TEST_ORDER_ID", 0),
		BaseAmount: lighterInt64Env(t, "LIGHTER_TEST_ORDER_BASE_AMOUNT", 0),
		Price:      uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_PRICE", 0)),
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected modify order response")
	}
}

func TestClient_SendTxBatch(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_BATCH_TX_TYPE", "LIGHTER_TEST_BATCH_TX_INFO")
	got, err := client.SendTxBatch(context.Background(), []map[string]string{{
		"tx_type": os.Getenv("LIGHTER_TEST_BATCH_TX_TYPE"),
		"tx_info": os.Getenv("LIGHTER_TEST_BATCH_TX_INFO"),
	}})
	if err != nil {
		t.Fatalf("SendTxBatch: %v", err)
	}
	if got == nil {
		t.Fatal("expected batch response")
	}
}

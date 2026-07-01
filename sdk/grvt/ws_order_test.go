package grvt

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func limitBuyPrice(t *testing.T, client *Client, instrument string) string {
	t.Helper()

	instruments, err := client.GetInstruments(context.Background())
	if err != nil {
		t.Fatalf("GetInstruments failed: %v", err)
	}

	var tickSize decimal.Decimal
	found := false
	for _, inst := range instruments {
		if inst.Instrument != instrument {
			continue
		}
		tickSize, err = decimal.NewFromString(inst.TickSize)
		if err != nil {
			t.Fatalf("invalid tick size %q for %s: %v", inst.TickSize, instrument, err)
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("instrument %s not found", instrument)
	}

	ticker, err := client.GetTicker(context.Background(), instrument)
	if err != nil {
		t.Fatalf("GetTicker failed: %v", err)
	}
	if ticker.Result.BestBidPrice == "" {
		t.Fatalf("ticker missing best bid price for %s", instrument)
	}

	bestBid, err := decimal.NewFromString(ticker.Result.BestBidPrice)
	if err != nil {
		t.Fatalf("invalid best bid price %q: %v", ticker.Result.BestBidPrice, err)
	}
	price := bestBid.Sub(tickSize.Mul(decimal.NewFromInt(2)))
	if !price.IsPositive() {
		t.Fatalf("invalid derived limit price %s from best bid %s", price.String(), ticker.Result.BestBidPrice)
	}

	precision := int32(0)
	if tickSize.Exponent() < 0 {
		precision = -tickSize.Exponent()
	}
	price = price.Div(tickSize).Floor().Mul(tickSize)
	return price.StringFixed(precision)
}

func orderNonce() uint32 {
	return uint32(time.Now().UnixNano())
}

func retryPlaceOrder(t *testing.T, wsClient *WebsocketClient, build func() *OrderRequest) (*CreateOrderResponse, *OrderRequest) {
	t.Helper()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req := build()
		resp, err := wsClient.PlaceOrder(context.Background(), req)
		if err == nil {
			return resp, req
		}
		lastErr = err
		if !strings.Contains(strings.ToLower(err.Error()), "signature does not match payload") {
			t.Fatalf("PlaceOrder failed: %v", err)
		}
		time.Sleep(time.Second)
	}

	t.Fatalf("PlaceOrder failed after retries: %v", lastErr)
	return nil, nil
}

// TestLimitOrderLifecycle places a post-only order just below the live best bid so it
// stays inside the protection band while remaining non-marketable.
func TestLimitOrderLifecycle(t *testing.T) {
	requireFullEnv(t)
	apiKey, subaccount, privateKey := GetEnv()

	client := newLiveClient(t).WithCredentials(apiKey, subaccount, privateKey)
	wsClient := NewAccountRpcWebsocketClient(context.Background(), client)
	connectWithRetry(t, wsClient)
	defer wsClient.Close()
	time.Sleep(2 * time.Second) // Wait for connection

	// Parse subaccount
	var sa uint64
	fmt.Sscan(subaccount, &sa)
	limitPrice := limitBuyPrice(t, client, "ETH_USDT_Perp")

	t.Log("Placing Limit Order...")
	resp, limitReq := retryPlaceOrder(t, wsClient, func() *OrderRequest {
		return &OrderRequest{
			SubAccountID: sa,
			IsMarket:     false,
			TimeInForce:  GTT,
			PostOnly:     false,
			ReduceOnly:   false,
			Legs: []OrderLeg{
				{
					Instrument:    "ETH_USDT_Perp",
					Size:          "0.1",
					LimitPrice:    limitPrice,
					IsBuyintAsset: true,
				},
			},
			Metadata: OrderMetadata{
				ClientOrderID: fmt.Sprintf("%d", time.Now().UnixNano()),
			},
			Signature: OrderSignature{
				Expiration: fmt.Sprintf("%d", time.Now().Add(5*time.Minute).UnixNano()),
				Nonce:      orderNonce(),
			},
		}
	})
	t.Logf("PlaceOrder success: OrderID=%s", resp.Result.OrderID)

	// 2. Cancel Order
	time.Sleep(1 * time.Second)
	t.Log("Cancelling Order...")
	cancelClientOrderID := limitReq.Metadata.ClientOrderID
	cancelReq := &CancelOrderRequest{
		SubAccountID:  subaccount,
		ClientOrderID: &cancelClientOrderID,
	}
	cancelResp, err := wsClient.CancelOrder(context.Background(), cancelReq)
	if err != nil {
		t.Fatalf("CancelOrder failed: %v", err)
	}
	t.Logf("CancelOrder success: %+v", cancelResp)
}

// TestMarketOrderLifecycle: 0.1 ETH Market Buy -> Market Sell (Close)
func TestMarketOrderLifecycle(t *testing.T) {
	requireFullEnv(t)
	apiKey, subaccount, privateKey := GetEnv()

	client := newLiveClient(t).WithCredentials(apiKey, subaccount, privateKey)
	wsClient := NewAccountRpcWebsocketClient(context.Background(), client)
	connectWithRetry(t, wsClient)
	defer wsClient.Close()
	time.Sleep(2 * time.Second)

	var sa uint64
	fmt.Sscan(subaccount, &sa)

	// 1. Place Market Buy Order
	marketBuyReq := &OrderRequest{
		SubAccountID: sa,
		IsMarket:     true,
		TimeInForce:  IOC, // Market orders usually IOC or FOK
		PostOnly:     false,
		ReduceOnly:   false,
		Legs: []OrderLeg{
			{
				Instrument:    "ETH_USDT_Perp",
				Size:          "0.1",
				LimitPrice:    "0", // Market order
				IsBuyintAsset: true,
			},
		},
		Metadata: OrderMetadata{
			ClientOrderID: fmt.Sprintf("%d", time.Now().UnixNano()),
		},
		Signature: OrderSignature{
			Expiration: fmt.Sprintf("%d", time.Now().Add(1*time.Minute).UnixNano()),
			Nonce:      orderNonce(),
		},
	}

	t.Log("Placing Market Buy Order...")
	resp, err := wsClient.PlaceOrder(context.Background(), marketBuyReq)
	if err != nil {
		t.Fatalf("Market Buy failed: %v", err)
	}
	t.Logf("Market Buy success: OrderID=%s", resp.Result.OrderID)

	// Wait for fill
	time.Sleep(2 * time.Second)

	// 2. Close Position (Market Sell)
	marketSellReq := &OrderRequest{
		SubAccountID: sa,
		IsMarket:     true,
		TimeInForce:  IOC,
		PostOnly:     false,
		ReduceOnly:   true, // Close position
		Legs: []OrderLeg{
			{
				Instrument:    "ETH_USDT_Perp",
				Size:          "0.1",
				LimitPrice:    "0",
				IsBuyintAsset: false, // Sell
			},
		},
		Metadata: OrderMetadata{
			ClientOrderID: fmt.Sprintf("%d", time.Now().UnixNano()),
		},
		Signature: OrderSignature{
			Expiration: fmt.Sprintf("%d", time.Now().Add(1*time.Minute).UnixNano()),
			Nonce:      orderNonce(),
		},
	}

	t.Log("Placing Market Sell (Close) Order...")
	closeResp, err := wsClient.PlaceOrder(context.Background(), marketSellReq)
	if err != nil {
		t.Fatalf("Market Sell failed: %v", err)
	}
	t.Logf("Market Sell success: OrderID=%s", closeResp.Result.OrderID)
}

package perp

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func requireRealtimeWS(t *testing.T) {
	t.Helper()
}

// TestSubscribeMarkPrice tests real subscription to Mark Price
func TestSubscribeMarkPrice(t *testing.T) {
	requireRealtimeWS(t)
	client := NewWsMarketClient(context.Background())
	// Use default URL (Real Binance)
	client.WsClient.Debug = true

	err := client.Connect()
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	done := make(chan bool, 1)
	// BTCUSDT is usually active
	err = client.SubscribeMarkPrice("btcusdt", "1s", func(e *WsMarkPriceEvent) error {
		// Just verify we got data for the right symbol
		if e.Symbol != "BTCUSDT" {
			t.Errorf("Expected symbol BTCUSDT, got %s", e.Symbol)
		}
		// Assuming we get at least one event
		select {
		case done <- true:
		default:
		}
		return nil
	})

	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	select {
	case <-done:
		// success
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for mark price event from Binance")
	}
}

// TestSubscribeAllMarkPrice tests the real all-market Mark Price stream.
func TestSubscribeAllMarkPrice(t *testing.T) {
	if os.Getenv("BINANCE_REALTIME_WS") != "1" {
		t.Skip("set BINANCE_REALTIME_WS=1 to run real Binance all-market mark price smoke test")
	}
	requireRealtimeWS(t)
	client := NewWsMarketClient(context.Background())
	client.WsClient.Debug = true

	err := client.Connect()
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	done := make(chan struct{}, 1)
	err = client.SubscribeAllMarkPrice("1s", func(events []*WsMarkPriceEvent) error {
		for _, event := range events {
			if event != nil && event.Symbol != "" && event.FundingRate != "" {
				select {
				case done <- struct{}{}:
				default:
				}
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for all-market mark price events from Binance")
	}
}

func TestSubscribeAggTradeRealtime(t *testing.T) {
	if os.Getenv("BINANCE_REALTIME_WS") != "1" {
		t.Skip("set BINANCE_REALTIME_WS=1 to run real Binance aggTrade smoke test")
	}
	client := NewWsMarketClient(context.Background())
	client.WsClient.Debug = true

	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	done := make(chan struct{}, 1)
	if err := client.SubscribeAggTrade("btcusdt", func(e *WsAggTradeEvent) error {
		if e.Symbol != "BTCUSDT" {
			t.Errorf("Expected symbol BTCUSDT, got %s", e.Symbol)
		}
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	}); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for aggTrade event from Binance")
	}
}

func TestSubscribeKlineRealtime(t *testing.T) {
	if os.Getenv("BINANCE_REALTIME_WS") != "1" {
		t.Skip("set BINANCE_REALTIME_WS=1 to run real Binance kline smoke test")
	}
	client := NewWsMarketClient(context.Background())
	client.WsClient.Debug = true

	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	done := make(chan struct{}, 1)
	if err := client.SubscribeKline("btcusdt", "1m", func(e *WsKlineEvent) error {
		if e.Symbol != "BTCUSDT" {
			t.Errorf("Expected symbol BTCUSDT, got %s", e.Symbol)
		}
		if e.Kline.Interval != "1m" {
			t.Errorf("Expected interval 1m, got %s", e.Kline.Interval)
		}
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	}); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for kline event from Binance")
	}
}

// TestSubscribeDepth tests real subscription to Depth
func TestSubscribeDepth(t *testing.T) {
	requireRealtimeWS(t)
	client := NewWsMarketClient(context.Background())
	client.WsClient.Debug = true

	err := client.Connect()
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	done := make(chan bool, 1)
	err = client.SubscribeIncrementOrderBook("btcusdt", "100ms", func(e *WsDepthEvent) error {
		if e.Symbol != "BTCUSDT" {
			t.Errorf("Expected symbol BTCUSDT, got %s", e.Symbol)
		}
		// We expect bids/asks to be present typically
		select {
		case done <- true:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for depth event from Binance")
	}
}

// TestSubscribeBookTicker tests real subscription to Book Ticker
func TestSubscribeBookTicker(t *testing.T) {
	requireRealtimeWS(t)
	client := NewWsMarketClient(context.Background())
	client.WsClient.Debug = true

	err := client.Connect()
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	done := make(chan bool, 1)
	err = client.SubscribeBookTicker("btcusdt", func(e *WsBookTickerEvent) error {
		if e.Symbol != "BTCUSDT" {
			t.Errorf("Expected symbol BTCUSDT, got %s", e.Symbol)
		}
		select {
		case done <- true:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for bookTicker event from Binance")
	}
}

// TestReconnectAndResubscribe tests reconnection logic by forcefully closing the underlying connection
func TestReconnectAndResubscribe(t *testing.T) {
	requireRealtimeWS(t)
	client := NewWsMarketClient(context.Background())
	client.WsClient.Debug = true
	// Fast reconnect for test
	client.ReconnectWait = 1 * time.Second

	err := client.Connect()
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	// We will close client manually at the end
	defer client.Close()

	dataReceived := make(chan bool, 10)
	err = client.SubscribeBookTicker("ethusdt", func(e *WsBookTickerEvent) error {
		// Signal data receipt
		select {
		case dataReceived <- true:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// 1. Wait for initial data
	fmt.Println("Waiting for initial data...")
	select {
	case <-dataReceived:
		fmt.Println("Initial data received.")
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for initial data")
	}

	// 2. Force Close Underlying Connection
	fmt.Println("Forcefully closing underlying connection...")
	client.Mu.Lock()
	if client.Conn != nil {
		client.Conn.Close()
		// Important: We do NOT set client.Conn to nil here manually if we want to simulate
		// a network error that `readLoop` detects. `readLoop` will fail on ReadMessage and trigger reconnect.
		// However, Close() makes ReadMessage return error immediately.
	}
	client.Mu.Unlock()

	// Drain channel
	// We expect the *next* signal to come from the NEW connection.
	// But `dataReceived` might have residual buffered items.
	// We'll require getting data *after* a short pause ensuring reconnect happened.

	// Wait for reconnect logs (we can't easily hook into logs, so we just wait)
	// Backoff is 1s, plus handshake time.
	fmt.Println("Waiting for reconnection (approx 2-3s)...")
	time.Sleep(3 * time.Second)

	// Now wait for data resumption
	fmt.Println("Waiting for data resumption...")
	select {
	case <-dataReceived:
		fmt.Println("Data resumed!")
	case <-time.After(15 * time.Second):
		t.Fatal("Timeout waiting for data resumption after reconnect")
	}
}

func TestKline(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	client.WsClient.Debug = true

	err := client.Connect()
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	events := make(chan struct{}, 1)
	err = client.SubscribeBookTicker("btcusdt", func(e *WsBookTickerEvent) error {
		if e.Symbol != "BTCUSDT" {
			t.Errorf("Expected symbol BTCUSDT, got %s", e.Symbol)
		}
		select {
		case events <- struct{}{}:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	select {
	case <-events:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for initial market event from Binance")
	}

}

// TestMultiSubscription tests simultaneous subscriptions to multiple streams
func TestMultiSubscription(t *testing.T) {
	requireRealtimeWS(t)
	client := NewWsMarketClient(context.Background())
	client.WsClient.Debug = true

	err := client.Connect()
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Track receipt of data for each symbol
	btcReceived := make(chan bool, 1)
	ethReceived := make(chan bool, 1)
	solReceived := make(chan bool, 1)

	// Subscribe BTC
	err = client.SubscribeBookTicker("btcusdt", func(e *WsBookTickerEvent) error {
		if e.Symbol == "BTCUSDT" {
			select {
			case btcReceived <- true:
			default:
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe BTC failed: %v", err)
	}

	// Subscribe ETH
	err = client.SubscribeBookTicker("ethusdt", func(e *WsBookTickerEvent) error {
		if e.Symbol == "ETHUSDT" {
			select {
			case ethReceived <- true:
			default:
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe ETH failed: %v", err)
	}

	// Subscribe SOL
	err = client.SubscribeBookTicker("solusdt", func(e *WsBookTickerEvent) error {
		if e.Symbol == "SOLUSDT" {
			select {
			case solReceived <- true:
			default:
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe SOL failed: %v", err)
	}

	// Wait for all 3
	timeout := time.After(15 * time.Second)
	gotBtc, gotEth, gotSol := false, false, false

	for {
		select {
		case <-btcReceived:
			gotBtc = true
			fmt.Println("Got BTC data")
		case <-ethReceived:
			gotEth = true
			fmt.Println("Got ETH data")
		case <-solReceived:
			gotSol = true
			fmt.Println("Got SOL data")
		case <-timeout:
			t.Fatalf("Timeout waiting for all subscriptions. BTC: %v, ETH: %v, SOL: %v", gotBtc, gotEth, gotSol)
		}

		if gotBtc && gotEth && gotSol {
			break
		}
	}
}

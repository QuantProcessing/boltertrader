package spot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/cloid"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/gorilla/websocket"
)

func TestHyperliquidSpotPrivateStreamReconnectMapsIdentityAndCloses(t *testing.T) {
	const accountAddress = "0xabc"
	const accountID = "HL:spot:test"
	const clientID = "runtime-client-1"
	venueCloid := cloid.ForClientID(clientID)

	var connections atomic.Int32
	subscriptions := make(chan map[string]string, 2)
	serverErrors := make(chan error, 4)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			select {
			case serverErrors <- fmt.Errorf("upgrade websocket: %w", err):
			default:
			}
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		seen := make(map[string]string, 3)
		for len(seen) < 3 {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, raw, err := conn.ReadMessage()
			if err != nil {
				select {
				case serverErrors <- fmt.Errorf("connection %d read subscription: %w", connection, err):
				default:
				}
				return
			}
			var req sdk.WsSubscribeRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				select {
				case serverErrors <- fmt.Errorf("decode subscription: %w", err):
				default:
				}
				return
			}
			if req.Method == "subscribe" {
				encoded, _ := json.Marshal(req.Subscription)
				var subscription map[string]any
				_ = json.Unmarshal(encoded, &subscription)
				typeName, _ := subscription["type"].(string)
				user, _ := subscription["user"].(string)
				seen[typeName] = user
				if err := writeSpotSubscriptionACK(conn, raw); err != nil {
					return
				}
			}
		}
		subscriptions <- seen

		if connection == 1 {
			if err := conn.WriteJSON(map[string]any{
				"channel": "orderUpdates",
				"data": []any{map[string]any{
					"order": map[string]any{
						"coin": "PURR/USDC", "side": "B", "limitPx": "1.01", "sz": "2",
						"oid": 555, "timestamp": int64(1700000000000), "origSz": "2", "cloid": venueCloid,
					},
					"status": "open", "statusTimestamp": int64(1700000000123),
				}},
			}); err != nil {
				return
			}
			if err := writeSpotUserFill(conn, accountAddress, 99, true); err != nil {
				return
			}
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"), time.Now().Add(time.Second))
			return
		}

		if err := writeSpotUserFill(conn, accountAddress, 100, false); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := sdk.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.AccountAddr = accountAddress
	base.ReconnectWait = 10 * time.Millisecond
	ws := sdkspot.NewWebsocketClient(base)
	provider := testProvider(t)
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, provider, clk, accountID)
	acct := newAccountClient(nil, clk, accountID)
	market := newMarketDataClient(nil, provider, clk)
	adapter := &Adapter{
		Market:    market,
		Execution: exec,
		Account:   acct,
		provider:  provider,
		ws:        ws,
		exec:      exec,
		acct:      acct,
		clk:       clk,
	}
	if got := exec.ids.VenueCloid(clientID); got != venueCloid {
		t.Fatalf("venue cloid=%q, want deterministic mapping %q", got, venueCloid)
	}

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for connection := 1; connection <= 2; connection++ {
		select {
		case got := <-subscriptions:
			if len(got) != 3 || got["orderUpdates"] != accountAddress || got["userFills"] != accountAddress || got["spotState"] != accountAddress {
				t.Fatalf("connection %d subscriptions=%v, want orderUpdates, userFills, and spotState for %s", connection, got, accountAddress)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for connection %d subscriptions", connection)
		}
	}

	var sawOrder, sawFill99, sawFill100, sawGapStarted, sawGapRecovered bool
	deadline := time.After(3 * time.Second)
	for !(sawOrder && sawFill99 && sawFill100 && sawGapStarted && sawGapRecovered) {
		select {
		case env, ok := <-exec.Events():
			if !ok {
				t.Fatal("execution event stream closed before private-stream assertions completed")
			}
			if env.Source != contract.SourceAdapterStream || !env.Flags.Has(contract.EventFlagFromStream) {
				t.Fatalf("event meta source=%q flags=%v, want adapter stream/from-stream", env.Source, env.Flags)
			}
			switch event := env.Payload.(type) {
			case contract.OrderEvent:
				if event.Order.Request.AccountID != accountID || event.Order.Request.ClientID != clientID || event.Order.Request.InstrumentID != testSpotID() || event.Order.VenueOrderID != "555" || event.Order.Status != enums.StatusNew {
					t.Fatalf("stream order=%+v, want mapped runtime identity", event.Order)
				}
				sawOrder = true
			case contract.FillEvent:
				if event.Fill.AccountID != accountID || event.Fill.ClientID != clientID || event.Fill.InstrumentID != testSpotID() || event.Fill.VenueOrderID != "555" {
					t.Fatalf("stream fill=%+v, want mapped runtime identity", event.Fill)
				}
				switch event.Fill.TradeID {
				case "99":
					if !env.Flags.Has(contract.EventFlagFromSnapshot) {
						t.Fatalf("snapshot fill flags=%v, want FromSnapshot", env.Flags)
					}
					sawFill99 = true
				case "100":
					if env.Flags.Has(contract.EventFlagFromSnapshot) {
						t.Fatalf("live fill flags=%v, must not include FromSnapshot", env.Flags)
					}
					sawFill100 = true
				default:
					t.Fatalf("unexpected stream fill trade id %q", event.Fill.TradeID)
				}
			case contract.StreamGapEvent:
				if event.Venue != venueName || event.AccountID != accountID || event.StreamID != "hyperliquid:spot:private" || event.Generation != 1 {
					t.Fatalf("stream gap=%+v", event)
				}
				switch event.Phase {
				case contract.StreamGapStarted:
					sawGapStarted = true
				case contract.StreamGapRecovered:
					sawGapRecovered = true
				}
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-deadline:
			t.Fatalf("timed out waiting for private stream events: order=%v fill99=%v fill100=%v gap-started=%v gap-recovered=%v", sawOrder, sawFill99, sawFill100, sawGapStarted, sawGapRecovered)
		}
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("websocket connections=%d, want initial plus one reconnect", got)
	}

	if err := adapter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case _, ok := <-exec.Events():
		if ok {
			t.Fatal("execution event stream remained open after Adapter.Close")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for execution event stream to close")
	}
}

func writeSpotUserFill(conn *websocket.Conn, user string, tradeID int64, snapshot bool) error {
	return conn.WriteJSON(map[string]any{
		"channel": "userFills",
		"data": map[string]any{
			"user": user, "isSnapshot": snapshot,
			"fills": []any{map[string]any{
				"coin": "PURR/USDC", "px": "1.01", "sz": "2", "side": "B", "time": int64(1700000000123),
				"hash": "0xhash", "oid": 555, "crossed": true, "fee": "0.01", "feeToken": "USDC", "tid": tradeID,
			}},
		},
	})
}

func writeSpotSubscriptionACK(conn *websocket.Conn, raw []byte) error {
	var req sdk.WsSubscribeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	encoded, err := json.Marshal(req.Subscription)
	if err != nil {
		return err
	}
	var acknowledged map[string]any
	if err := json.Unmarshal(encoded, &acknowledged); err != nil {
		return err
	}
	if user, ok := acknowledged["user"].(string); ok {
		acknowledged["user"] = strings.ToLower(user)
	}
	if portfolio, ok := acknowledged["isPortfolioMargin"]; ok {
		delete(acknowledged, "isPortfolioMargin")
		acknowledged["ignorePortfolioMargin"] = portfolio
	}
	return conn.WriteJSON(map[string]any{
		"channel": "subscriptionResponse",
		"data": map[string]any{
			"method":       "subscribe",
			"subscription": acknowledged,
		},
	})
}

func TestHyperliquidSpotStartRollsBackSecondSubscriptionTimeoutAndRetriesCleanly(t *testing.T) {
	const accountAddress = "0xAbC"
	var connections atomic.Int32
	secondReady := make(chan struct{})
	releaseEvent := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		for subscription := 0; subscription < 3; subscription++ {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if connection == 1 && subscription == 1 {
				_ = writeSpotUserFill(conn, strings.ToLower(accountAddress), 701, false)
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}
			if err := writeSpotSubscriptionACK(conn, raw); err != nil {
				return
			}
		}
		close(secondReady)
		<-releaseEvent
		_ = writeSpotUserFill(conn, strings.ToLower(accountAddress), 702, false)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	base := sdk.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.AccountAddr = accountAddress
	base.SubscriptionAckTimeout = 25 * time.Millisecond
	base.ReconnectWait = 5 * time.Millisecond
	ws := sdkspot.NewWebsocketClient(base)
	provider := testProvider(t)
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, provider, clk, "HL:spot:test")
	acct := newAccountClient(nil, clk, "HL:spot:test")
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, ws: ws, exec: exec, acct: acct, clk: clk}
	t.Cleanup(func() {
		cancel()
		ws.Close()
		_ = exec.Close()
		_ = acct.Close()
	})

	if err := adapter.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("first Start err=%v, want second-subscription ACK timeout", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections=%d, failed Start must not reconnect", got)
	}
	select {
	case event := <-exec.Events():
		t.Fatalf("failed Start emitted private event: %+v", event)
	default:
	}

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("retry Start: %v", err)
	}
	select {
	case <-secondReady:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry subscriptions")
	}
	close(releaseEvent)
	select {
	case env := <-exec.Events():
		fill, ok := env.Payload.(contract.FillEvent)
		if !ok || fill.Fill.TradeID != "702" {
			t.Fatalf("retry event=%+v, want mixed-case-address fill 702", env)
		}
	case <-time.After(time.Second):
		t.Fatal("retry Start did not activate private handlers")
	}
}

func TestHyperliquidSpotStartSocketDropDoesNotLeakReconnectOrGapAcrossImmediateRetry(t *testing.T) {
	const accountAddress = "0xabc"
	var connections atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		for subscription := 0; subscription < 3; subscription++ {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if connection == 1 && subscription == 1 {
				return
			}
			if writeSpotSubscriptionACK(conn, raw) != nil {
				return
			}
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	base := sdk.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.AccountAddr = accountAddress
	base.ReconnectWait = 25 * time.Millisecond
	base.SubscriptionAckTimeout = time.Second
	ws := sdkspot.NewWebsocketClient(base)
	provider := testProvider(t)
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, provider, clk, "HL:spot:test")
	acct := newAccountClient(nil, clk, "HL:spot:test")
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, ws: ws, exec: exec, acct: acct, clk: clk}
	t.Cleanup(func() {
		cancel()
		ws.Close()
		_ = exec.Close()
		_ = acct.Close()
	})

	if err := adapter.Start(context.Background()); err == nil {
		t.Fatal("first Start succeeded after startup socket drop")
	}
	// Retry immediately, before the old reconnect timer can fire. Its stale
	// epoch must not resurrect or race this new startup transaction.
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("immediate retry Start: %v", err)
	}
	time.Sleep(75 * time.Millisecond)
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections=%d, want dropped startup plus one explicit retry only", got)
	}
	select {
	case event := <-exec.Events():
		t.Fatalf("startup drop leaked pre-ready gap/private event: %+v", event)
	default:
	}
}

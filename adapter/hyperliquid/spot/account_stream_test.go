package spot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestHyperliquidSpotStartStreamsAuthoritativeBalanceChanges(t *testing.T) {
	const accountAddress = "0x000000000000000000000000000000000000dEaD"
	subscriptions := make(chan string, 3)
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		for i := 0; i < 3; i++ {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				serverErrors <- err
				return
			}
			var req sdk.WsSubscribeRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				serverErrors <- err
				return
			}
			encoded, _ := json.Marshal(req.Subscription)
			var acknowledged map[string]any
			_ = json.Unmarshal(encoded, &acknowledged)
			typeName, _ := acknowledged["type"].(string)
			subscriptions <- typeName
			if user, ok := acknowledged["user"].(string); ok {
				acknowledged["user"] = strings.ToLower(user)
			}
			if portfolio, ok := acknowledged["isPortfolioMargin"]; ok {
				delete(acknowledged, "isPortfolioMargin")
				acknowledged["ignorePortfolioMargin"] = portfolio
			}
			if err := conn.WriteJSON(map[string]any{
				"channel": "subscriptionResponse",
				"data": map[string]any{
					"method":       "subscribe",
					"subscription": acknowledged,
				},
			}); err != nil {
				serverErrors <- err
				return
			}
		}
		if err := conn.WriteJSON(map[string]any{
			"channel": "spotState",
			"data": map[string]any{
				"user":      strings.ToLower(accountAddress),
				"spotState": map[string]any{"balances": "malformed"},
			},
		}); err != nil {
			serverErrors <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"channel": "spotState",
			"data": map[string]any{
				"user": strings.ToLower(accountAddress),
				"spotState": map[string]any{
					"balances": []any{map[string]any{
						"coin": "PURR", "token": 1, "hold": "0.25", "total": "5", "entryNtl": "3",
					}},
				},
			},
		}); err != nil {
			serverErrors <- err
			return
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
	ws := sdkspot.NewWebsocketClient(base)
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, testProvider(t), clk, "HL:spot:test")
	acct := newAccountClient(nil, clk, "HL:spot:test")
	adapter := &Adapter{Execution: exec, Account: acct, provider: testProvider(t), ws: ws, exec: exec, acct: acct, clk: clk}
	t.Cleanup(func() {
		cancel()
		ws.Close()
		_ = exec.Close()
		_ = acct.Close()
	})

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for index, want := range []string{"orderUpdates", "userFills", "spotState"} {
		select {
		case got := <-subscriptions:
			if got != want {
				t.Fatalf("subscription %d=%q, want %q", index+1, got, want)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for subscription %d", index+1)
		}
	}
	for _, want := range []contract.StreamGapPhase{contract.StreamGapStarted, contract.StreamGapRecovered} {
		select {
		case envelope := <-exec.Events():
			gap, ok := envelope.Payload.(contract.StreamGapEvent)
			if !ok || gap.Phase != want || gap.StreamID != accountStateStreamID {
				t.Fatalf("exec payload=%+v, want %s account-state gap", envelope, want)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s private gap", want)
		}
	}
	select {
	case envelope := <-acct.Events():
		balance, ok := envelope.Payload.(contract.BalanceEvent)
		if !ok {
			t.Fatalf("account payload=%T, want BalanceEvent", envelope.Payload)
		}
		if balance.Balance.AccountID != "HL:spot:test" || balance.Balance.Currency != "PURR" ||
			!balance.Balance.Total.Equal(decimal.NewFromInt(5)) ||
			!balance.Balance.Free.Equal(decimal.RequireFromString("4.75")) ||
			!balance.Balance.Locked.Equal(decimal.RequireFromString("0.25")) {
			t.Fatalf("balance=%+v, want authoritative total/free/locked", balance.Balance)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Spot balance event")
	}
}

func TestAccountEventsFromSpotStateRejectsHoldAboveTotal(t *testing.T) {
	_, err := accountEventsFromSpotState(sdk.SpotClearinghouseState{Balances: []sdk.SpotBalance{{
		Coin: "PURR", Total: "1", Hold: "2",
	}}}, clock.NewRealClock().Now(), "HL:spot:test")
	if err == nil {
		t.Fatal("hold above total was accepted")
	}
}

func TestSpotStateStreamZerosCurrenciesOmittedByLaterSnapshot(t *testing.T) {
	clk := clock.NewRealClock()
	acct := newAccountClient(nil, clk, "HL:spot:test")
	defer acct.Close()

	first, err := acct.eventsFromSpotState(sdk.SpotClearinghouseState{Balances: []sdk.SpotBalance{{
		Coin: "PURR", Total: "2", Hold: "0",
	}}}, clk.Now())
	if err != nil || len(first) != 1 {
		t.Fatalf("first events=%v err=%v, want one balance", first, err)
	}
	second, err := acct.eventsFromSpotState(sdk.SpotClearinghouseState{}, clk.Now())
	if err != nil || len(second) != 1 {
		t.Fatalf("second events=%v err=%v, want one zero balance", second, err)
	}
	zero, ok := second[0].(contract.BalanceEvent)
	if !ok || zero.Balance.Currency != "PURR" || !zero.Balance.Total.IsZero() || !zero.Balance.Free.IsZero() || !zero.Balance.Locked.IsZero() {
		t.Fatalf("omitted currency event=%+v, want authoritative zero PURR balance", second[0])
	}
}

func TestSpotRESTCurrencySeedCannotErasePendingWebsocketTombstone(t *testing.T) {
	clk := clock.NewRealClock()
	acct := newAccountClient(nil, clk, "HL:spot:test")
	defer acct.Close()

	if _, err := acct.eventsFromSpotState(sdk.SpotClearinghouseState{Balances: []sdk.SpotBalance{{
		Coin: "PURR", Total: "2", Hold: "0",
	}}}, clk.Now()); err != nil {
		t.Fatalf("positive websocket snapshot: %v", err)
	}
	acct.rememberSpotCurrencies(nil)
	omitted, err := acct.eventsFromSpotState(sdk.SpotClearinghouseState{}, clk.Now())
	if err != nil || len(omitted) != 1 {
		t.Fatalf("omitted events=%v err=%v, want pending PURR tombstone", omitted, err)
	}
	zero, ok := omitted[0].(contract.BalanceEvent)
	if !ok || zero.Balance.Currency != "PURR" || !zero.Balance.Total.IsZero() {
		t.Fatalf("omitted event=%+v, want zero PURR balance", omitted[0])
	}
}

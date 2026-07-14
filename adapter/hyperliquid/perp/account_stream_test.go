package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestHyperliquidPerpUnifiedStartUsesSpotStateForBalances(t *testing.T) {
	const accountAddress = "0x000000000000000000000000000000000000dEaD"
	subscriptions := make(chan map[string]any, 4)
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		for i := 0; i < 4; i++ {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, raw, err := conn.ReadMessage()
			if err != nil {
				serverErrors <- fmt.Errorf("read subscription %d: %w", i+1, err)
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
			observed := make(map[string]any, len(acknowledged))
			for key, value := range acknowledged {
				observed[key] = value
			}
			subscriptions <- observed
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
			"channel": "clearinghouseState",
			"data": map[string]any{
				"user": strings.ToLower(accountAddress),
				"dex":  "",
				"clearinghouseState": map[string]any{
					"assetPositions":             []any{},
					"crossMaintenanceMarginUsed": "0",
					"crossMarginSummary": map[string]any{
						"accountValue": "0.012385", "totalMarginUsed": "0", "totalNtlPos": "0", "totalRawUsd": "0.012385",
					},
					"marginSummary": map[string]any{
						"accountValue": "0.012385", "totalMarginUsed": "0", "totalNtlPos": "0", "totalRawUsd": "0.012385",
					},
					"withdrawable": "0.012385",
				},
			},
		}); err != nil {
			serverErrors <- err
			return
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
						"coin": "USDC", "token": 0, "hold": "0", "total": "100", "entryNtl": "0",
					}},
				},
			},
		}); err != nil {
			serverErrors <- err
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
	base := sdk.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.AccountAddr = accountAddress
	ws := sdkperp.NewWebsocketClient(base)
	provider := testProvider(t)
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, provider, clk, "HL:test")
	acct := newAccountClient(nil, provider, clk, "cross", decimal.NewFromInt(1), sdk.AccountAbstractionUnifiedAccount, "HL:test")
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, ws: ws, exec: exec, acct: acct, clk: clk}
	t.Cleanup(func() {
		cancel()
		ws.Close()
		_ = exec.Close()
		_ = acct.Close()
	})

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for index, want := range []string{"orderUpdates", "userFills", "clearinghouseState", "spotState"} {
		select {
		case got := <-subscriptions:
			if got["type"] != want {
				t.Fatalf("subscription %d type=%v, want %s", index+1, got["type"], want)
			}
			if want == "spotState" && got["isPortfolioMargin"] != false {
				t.Fatalf("unified spotState isPortfolioMargin=%v, want false", got["isPortfolioMargin"])
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
		if balance.Balance.Currency != "USDC" || !balance.Balance.Total.Equal(decimal.NewFromInt(100)) || !balance.Balance.Free.Equal(decimal.NewFromInt(100)) {
			t.Fatalf("unified balance=%+v, want authoritative spotState USDC=100", balance.Balance)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unified spotState balance")
	}
	select {
	case envelope := <-acct.Events():
		t.Fatalf("non-authoritative clearinghouse balance escaped after spotState: %+v", envelope)
	case err := <-serverErrors:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHyperliquidPerpStartSubscribesConfiguredHIP3ClearinghouseState(t *testing.T) {
	const accountAddress = "0x000000000000000000000000000000000000dEaD"
	subscriptions := make(chan map[string]any, 4)
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		for i := 0; i < 4; i++ {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, raw, err := conn.ReadMessage()
			if err != nil {
				serverErrors <- fmt.Errorf("read subscription %d: %w", i+1, err)
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
			subscriptions <- acknowledged
			if user, ok := acknowledged["user"].(string); ok {
				acknowledged["user"] = strings.ToLower(user)
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
		for _, state := range []map[string]any{
			{
				"dex": "",
				"positions": []any{map[string]any{
					"type": "oneWay", "position": map[string]any{"coin": "BTC", "entryPx": "10", "leverage": map[string]any{"type": "cross", "value": 3}, "szi": "1", "unrealizedPnl": "0"},
				}},
			},
			{
				"dex": "testdex",
				"positions": []any{map[string]any{
					"type": "oneWay", "position": map[string]any{"coin": "COIN", "entryPx": "2", "leverage": map[string]any{"type": "cross", "value": 2}, "szi": "4", "unrealizedPnl": "0"},
				}},
			},
		} {
			if err := conn.WriteJSON(map[string]any{
				"channel": "clearinghouseState",
				"data": map[string]any{
					"user": strings.ToLower(accountAddress),
					"dex":  state["dex"],
					"clearinghouseState": map[string]any{
						"assetPositions":     state["positions"],
						"marginSummary":      map[string]any{"accountValue": "100", "totalMarginUsed": "1"},
						"crossMarginSummary": map[string]any{"accountValue": "100", "totalMarginUsed": "1"},
						"withdrawable":       "99",
						"time":               1700000000000,
					},
				},
			}); err != nil {
				serverErrors <- err
				return
			}
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
	base := sdk.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.AccountAddr = accountAddress
	ws := sdkperp.NewWebsocketClient(base)
	provider := testProvider(t)
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, provider, clk, "HL:test")
	acct := newAccountClient(nil, provider, clk, "cross", decimal.NewFromInt(1), sdk.AccountAbstractionDefault, "HL:test").withHIP3Dexes([]string{"testdex"})
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, ws: ws, exec: exec, acct: acct, clk: clk}
	t.Cleanup(func() {
		cancel()
		ws.Close()
		_ = exec.Close()
		_ = acct.Close()
	})

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	wantSubscriptions := []struct {
		typeName string
		dex      string
	}{
		{typeName: "orderUpdates"},
		{typeName: "userFills"},
		{typeName: "clearinghouseState"},
		{typeName: "clearinghouseState", dex: "testdex"},
	}
	for index, want := range wantSubscriptions {
		select {
		case got := <-subscriptions:
			gotDex := ""
			if value, ok := got["dex"]; ok {
				gotDex = fmt.Sprint(value)
			}
			if got["type"] != want.typeName || gotDex != want.dex {
				t.Fatalf("subscription %d=%v, want type=%s dex=%q", index+1, got, want.typeName, want.dex)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for subscription %d", index+1)
		}
	}

	seen := map[model.InstrumentID]decimal.Decimal{}
	deadline := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case envelope := <-acct.Events():
			position, ok := envelope.Payload.(contract.PositionEvent)
			if ok {
				seen[position.Position.InstrumentID] = position.Position.Quantity
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-deadline:
			t.Fatalf("positions=%v, want canonical and HIP-3 stream events", seen)
		}
	}
	if !seen[testPerpID()].Equal(decimal.NewFromInt(1)) || !seen[testHIP3ID()].Equal(decimal.NewFromInt(4)) {
		t.Fatalf("positions=%v, want BTC=1 and testdex:COIN=4", seen)
	}
}

func TestHyperliquidPerpAccountEventsUseAuthoritativeBalanceSourceForMode(t *testing.T) {
	var state sdkperp.PerpPosition
	if err := json.Unmarshal([]byte(`{
		"assetPositions":[{"type":"oneWay","position":{"coin":"BTC","entryPx":"10","leverage":{"type":"cross","value":3},"marginUsed":"3","positionValue":"10","szi":"1","unrealizedPnl":"0"}}],
		"crossMarginSummary":{"accountValue":"0.012385","totalMarginUsed":"0","totalNtlPos":"10","totalRawUsd":"0.012385"},
		"marginSummary":{"accountValue":"0.012385","totalMarginUsed":"0","totalNtlPos":"10","totalRawUsd":"0.012385"},
		"withdrawable":"0.012385",
		"time":1700000000000
	}`), &state); err != nil {
		t.Fatalf("decode Perp state: %v", err)
	}

	for _, tt := range []struct {
		name        string
		mode        sdk.AccountAbstraction
		wantBalance bool
	}{
		{name: "default", mode: sdk.AccountAbstractionDefault, wantBalance: true},
		{name: "unified", mode: sdk.AccountAbstractionUnifiedAccount, wantBalance: false},
		{name: "portfolio", mode: sdk.AccountAbstractionPortfolioMargin, wantBalance: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := accountEventsFromPerpPositionForMode(&state, testProvider(t), clock.NewRealClock(), tt.mode, "HL:test")
			var balances, positions int
			for _, event := range events {
				switch event.(type) {
				case contract.BalanceEvent:
					balances++
				case contract.PositionEvent:
					positions++
				}
			}
			wantBalances := 0
			if tt.wantBalance {
				wantBalances = 1
			}
			if balances != wantBalances || positions != 1 {
				t.Fatalf("events=%+v, got balances=%d positions=%d, want balances=%d positions=1", events, balances, positions, wantBalances)
			}
		})
	}
}

func TestHyperliquidPerpClearinghouseSnapshotsEmitPositionTombstonesPerDex(t *testing.T) {
	decode := func(t *testing.T, payload string) sdkperp.PerpPosition {
		t.Helper()
		var state sdkperp.PerpPosition
		if err := json.Unmarshal([]byte(payload), &state); err != nil {
			t.Fatalf("decode clearinghouse state: %v", err)
		}
		return state
	}
	positionQty := func(events []contract.AccountEvent, id model.InstrumentID) (decimal.Decimal, bool) {
		for _, event := range events {
			if position, ok := event.(contract.PositionEvent); ok && position.Position.InstrumentID == id {
				return position.Position.Quantity, true
			}
		}
		return decimal.Zero, false
	}

	acct := newAccountClient(nil, testProvider(t), clock.NewRealClock(), "cross", decimal.NewFromInt(1), sdk.AccountAbstractionDefault, "HL:test").withHIP3Dexes([]string{"testdex"})
	defer acct.Close()
	canonical := decode(t, `{"assetPositions":[{"position":{"coin":"BTC","entryPx":"10","leverage":{"type":"cross","value":3},"szi":"1","unrealizedPnl":"0"}}],"time":1700000000000}`)
	hip3 := decode(t, `{"assetPositions":[{"position":{"coin":"COIN","entryPx":"2","leverage":{"type":"cross","value":2},"szi":"4","unrealizedPnl":"0"}}],"time":1700000000001}`)

	canonicalEvents, err := acct.eventsFromClearinghouseState(&canonical, "")
	if err != nil {
		t.Fatalf("canonical snapshot: %v", err)
	}
	if qty, ok := positionQty(canonicalEvents, testPerpID()); !ok || !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("canonical events=%+v, want BTC=1", canonicalEvents)
	}
	hip3Events, err := acct.eventsFromClearinghouseState(&hip3, "testdex")
	if err != nil {
		t.Fatalf("HIP-3 snapshot: %v", err)
	}
	if qty, ok := positionQty(hip3Events, testHIP3ID()); !ok || !qty.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("HIP-3 events=%+v, want COIN=4", hip3Events)
	}

	emptyHIP3 := decode(t, `{"assetPositions":[],"time":1700000000002}`)
	hip3Tombstones, err := acct.eventsFromClearinghouseState(&emptyHIP3, "testdex")
	if err != nil {
		t.Fatalf("empty HIP-3 snapshot: %v", err)
	}
	if qty, ok := positionQty(hip3Tombstones, testHIP3ID()); !ok || !qty.IsZero() {
		t.Fatalf("HIP-3 tombstones=%+v, want COIN=0", hip3Tombstones)
	}
	if _, ok := positionQty(hip3Tombstones, testPerpID()); ok {
		t.Fatalf("HIP-3 empty snapshot cleared canonical position: %+v", hip3Tombstones)
	}

	malformedHIP3 := decode(t, `{"assetPositions":[{"position":{"coin":"UNKNOWN","szi":"1"}}],"time":1700000000003}`)
	if _, err := acct.eventsFromClearinghouseState(&malformedHIP3, "testdex"); err == nil {
		t.Fatal("unresolved nonzero HIP-3 snapshot was accepted")
	}
	emptyCanonical := decode(t, `{"assetPositions":[],"time":1700000000004}`)
	canonicalTombstones, err := acct.eventsFromClearinghouseState(&emptyCanonical, "")
	if err != nil {
		t.Fatalf("empty canonical snapshot: %v", err)
	}
	if qty, ok := positionQty(canonicalTombstones, testPerpID()); !ok || !qty.IsZero() {
		t.Fatalf("canonical tombstones=%+v, want BTC=0", canonicalTombstones)
	}
}

func TestHyperliquidPerpSpotStateStreamZerosOmittedCurrenciesAndRejectsInvalidSnapshots(t *testing.T) {
	clk := clock.NewRealClock()
	acct := newAccountClient(nil, testProvider(t), clk, "cross", decimal.NewFromInt(1), sdk.AccountAbstractionUnifiedAccount, "HL:test")
	defer acct.Close()

	first, err := acct.eventsFromSpotState(sdk.SpotClearinghouseState{Balances: []sdk.SpotBalance{{
		Coin: "USDC", Total: "100", Hold: "1",
	}}}, clk.Now())
	if err != nil || len(first) != 1 {
		t.Fatalf("first events=%v err=%v, want one balance", first, err)
	}
	second, err := acct.eventsFromSpotState(sdk.SpotClearinghouseState{}, clk.Now())
	if err != nil || len(second) != 1 {
		t.Fatalf("second events=%v err=%v, want one zero balance", second, err)
	}
	zero, ok := second[0].(contract.BalanceEvent)
	if !ok || zero.Balance.Currency != "USDC" || !zero.Balance.Total.IsZero() || !zero.Balance.Free.IsZero() || !zero.Balance.Locked.IsZero() {
		t.Fatalf("omitted currency event=%+v, want authoritative zero USDC balance", second[0])
	}

	if _, err := acct.eventsFromSpotState(sdk.SpotClearinghouseState{Balances: []sdk.SpotBalance{{
		Coin: "USDC", Total: "1", Hold: "2",
	}}}, clk.Now()); err == nil {
		t.Fatal("hold above total was accepted")
	}
}

func TestHyperliquidPerpRESTCurrencySeedCannotErasePendingWebsocketTombstone(t *testing.T) {
	clk := clock.NewRealClock()
	acct := newAccountClient(nil, testProvider(t), clk, "cross", decimal.NewFromInt(1), sdk.AccountAbstractionUnifiedAccount, "HL:test")
	defer acct.Close()

	if _, err := acct.eventsFromSpotState(sdk.SpotClearinghouseState{Balances: []sdk.SpotBalance{{
		Coin: "USDC", Total: "100", Hold: "0",
	}}}, clk.Now()); err != nil {
		t.Fatalf("positive websocket snapshot: %v", err)
	}
	acct.rememberSpotCurrencies(nil)
	omitted, err := acct.eventsFromSpotState(sdk.SpotClearinghouseState{}, clk.Now())
	if err != nil || len(omitted) != 1 {
		t.Fatalf("omitted events=%v err=%v, want pending USDC tombstone", omitted, err)
	}
	zero, ok := omitted[0].(contract.BalanceEvent)
	if !ok || zero.Balance.Currency != "USDC" || !zero.Balance.Total.IsZero() {
		t.Fatalf("omitted event=%+v, want zero USDC balance", omitted[0])
	}
}

func TestHyperliquidPerpSpotStateSubscriptionKeepsPortfolioMarginEnabled(t *testing.T) {
	for _, mode := range []sdk.AccountAbstraction{
		sdk.AccountAbstractionUnifiedAccount,
		sdk.AccountAbstractionPortfolioMargin,
	} {
		subscribe, ignorePortfolioMargin := spotStateSubscriptionMode(mode)
		if !subscribe || ignorePortfolioMargin {
			t.Fatalf("mode=%s subscribe=%v ignorePortfolioMargin=%v, want true/false", mode, subscribe, ignorePortfolioMargin)
		}
	}
	if subscribe, _ := spotStateSubscriptionMode(sdk.AccountAbstractionDefault); subscribe {
		t.Fatal("default account unexpectedly requested spotState")
	}
}

func TestHyperliquidPerpRejectsUnsupportedResolvedAccountModes(t *testing.T) {
	for _, mode := range []sdk.AccountAbstraction{
		sdk.AccountAbstractionDefault,
		sdk.AccountAbstractionUnifiedAccount,
		sdk.AccountAbstractionPortfolioMargin,
	} {
		if err := validateResolvedAccountMode(mode); err != nil {
			t.Fatalf("supported mode %s: %v", mode, err)
		}
	}
	for _, mode := range []sdk.AccountAbstraction{sdk.AccountAbstractionUnknown, sdk.AccountAbstraction("legacyDexAbstraction")} {
		if err := validateResolvedAccountMode(mode); err == nil {
			t.Fatalf("unsupported mode %q was accepted", mode)
		}
	}
}

package perp

import (
	"context"
	"testing"
)

func TestWSAccountCompanion_NewWsAccountClient(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	if client.Client == nil || client.WsClient == nil || client.BaseURL != WSPrivateBaseURL {
		t.Fatalf("unexpected account client: %+v", client)
	}
}

func TestWSAccountCompanion_NewCoinMWsAccountClientUsesDstreamAndDAPI(t *testing.T) {
	client := NewCoinMWsAccountClient(context.Background(), "api-key", "secret")
	if client.Client == nil || client.WsClient == nil {
		t.Fatalf("unexpected nil client: %+v", client)
	}
	if client.Client.BaseURL != CoinMBaseURL {
		t.Fatalf("expected COIN-M REST base URL %s, got %s", CoinMBaseURL, client.Client.BaseURL)
	}
	if client.Client.EndpointPrefix != "/dapi" {
		t.Fatalf("expected COIN-M endpoint prefix /dapi, got %s", client.Client.EndpointPrefix)
	}
	if client.Client.AccountVersion != "v1" {
		t.Fatalf("expected COIN-M account version v1, got %s", client.Client.AccountVersion)
	}
	if client.BaseURL != CoinMWSPrivateBaseURL || client.WsClient.URL != CoinMWSPrivateBaseURL {
		t.Fatalf("expected COIN-M private stream base URL %s, got base=%s ws=%s", CoinMWSPrivateBaseURL, client.BaseURL, client.WsClient.URL)
	}
}

func TestWSAccountCompanion_WithURLSetsBaseURL(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	client.WithURL("wss://example.test/private")
	if client.BaseURL != "wss://example.test/private" {
		t.Fatalf("unexpected base url: %s", client.BaseURL)
	}
}

func TestWSAccountCompanion_SetOnResubscribe(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	called := false
	client.SetOnResubscribe(func() {
		called = true
	})
	client.onResubscribe()
	if !called {
		t.Fatal("expected on resubscribe hook to be stored")
	}
}

func TestWsAccountClient_SubscribeAlgoUpdate(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	var got *AlgoUpdateEvent
	client.SubscribeAlgoUpdate(func(event *AlgoUpdateEvent) {
		got = event
	})

	payload := []byte(`{
		"e":"ALGO_UPDATE",
		"E":1700000000001,
		"T":1700000000002,
		"o":{
			"caid":"algo-client",
			"aid":9001,
			"at":"CONDITIONAL",
			"o":"STOP_MARKET",
			"s":"BTCUSDT",
			"S":"SELL",
			"ps":"SHORT",
			"f":"GTC",
			"q":"0.2",
			"X":"TRIGGERED",
			"tp":"190",
			"p":"0",
			"wt":"CONTRACT_PRICE",
			"pm":"NONE",
			"cp":false,
			"pP":true,
			"R":true,
			"tt":1700000000003,
			"gtd":1700003600000,
			"ai":"77",
			"ap":"191",
			"aq":"0.2",
			"act":"MARKET",
			"cr":"1.2",
			"V":"NONE"
		}
	}`)
	if err := client.handleAlgoUpdate(payload); err != nil {
		t.Fatalf("handleAlgoUpdate: %v", err)
	}
	if got == nil {
		t.Fatal("expected algo update callback")
	}
	if got.EventType != "ALGO_UPDATE" || got.Order.ClientAlgoID != "algo-client" || got.Order.AlgoID != 9001 {
		t.Fatalf("unexpected algo update: %+v", got)
	}
	if got.Order.ActualOrderID != "77" || got.Order.AlgoStatus != "TRIGGERED" || got.Order.PositionSide != "SHORT" {
		t.Fatalf("unexpected algo order payload: %+v", got.Order)
	}
}

func TestWSAccountCompanion_ResetWSClientInstallsReconnectRecovery(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	if client.WsClient.postReconnect == nil {
		t.Fatal("expected account websocket to install reconnect recovery hook")
	}

	client.resetWSClient()
	if client.WsClient.postReconnect == nil {
		t.Fatal("expected reset websocket to keep reconnect recovery hook")
	}
}

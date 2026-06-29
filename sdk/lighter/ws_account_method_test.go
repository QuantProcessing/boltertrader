package lighter

import (
	"testing"
	"time"
)

func lighterWSAuth(t *testing.T) (*Client, string) {
	t.Helper()
	client := newLivePrivateClient(t)
	auth, err := client.CreateAuthToken(time.Now().Add(10 * time.Minute))
	if err != nil {
		t.Fatalf("CreateAuthToken: %v", err)
	}
	return client, auth
}

func TestWebsocketClient_SubscribeAccountAll(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeAccountAll(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountAll: %v", err)
	}
}

func TestWebsocketClient_SubscribeAccountMarket(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeAccountMarket(lighterMarketID(t), client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountMarket: %v", err)
	}
}

func TestWebsocketClient_SubscribeUserStats(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeUserStats(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeUserStats: %v", err)
	}
}

func TestWebsocketClient_SubscribeAccountTx(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeAccountTx(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountTx: %v", err)
	}
}

func TestWebsocketClient_SubscribeAccountAllOrders(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeAccountAllOrders(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountAllOrders: %v", err)
	}
}

func TestWebsocketClient_SubscribeAccountOrders(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeAccountOrders(lighterMarketID(t), client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountOrders: %v", err)
	}
}

func TestWebsocketClient_SubscribeNotification(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeNotification(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeNotification: %v", err)
	}
}

func TestWebsocketClient_SubscribeAccountAllTrades(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeAccountAllTrades(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountAllTrades: %v", err)
	}
}

func TestWebsocketClient_SubscribeAccountAllPositions(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeAccountAllPositions(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountAllPositions: %v", err)
	}
}

func TestWebsocketClient_SubscribeAccountAllAssets(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeAccountAllAssets(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountAllAssets: %v", err)
	}
}

func TestWebsocketClient_SubscribeAccountSpotAvgEntryPrices(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribeAccountSpotAvgEntryPrices(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountSpotAvgEntryPrices: %v", err)
	}
}

func TestWebsocketClient_SubscribePoolData(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribePoolData(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribePoolData: %v", err)
	}
}

func TestWebsocketClient_SubscribePoolInfo(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	if err := ws.SubscribePoolInfo(client.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribePoolInfo: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAccountAll(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeAccountAll(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeAccountAll(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeAccountAll: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAccountMarket(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeAccountMarket(lighterMarketID(t), client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeAccountMarket(lighterMarketID(t), client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeAccountMarket: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeUserStats(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeUserStats(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeUserStats(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeUserStats: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAccountTx(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeAccountTx(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeAccountTx(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeAccountTx: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAccountAllOrders(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeAccountAllOrders(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeAccountAllOrders(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeAccountAllOrders: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAccountOrders(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeAccountOrders(lighterMarketID(t), client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeAccountOrders(lighterMarketID(t), client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeAccountOrders: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeNotification(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeNotification(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeNotification(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeNotification: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAccountAllTrades(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeAccountAllTrades(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeAccountAllTrades(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeAccountAllTrades: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAccountAllPositions(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeAccountAllPositions(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeAccountAllPositions(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeAccountAllPositions: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAccountAllAssets(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeAccountAllAssets(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeAccountAllAssets(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeAccountAllAssets: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAccountSpotAvgEntryPrices(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribeAccountSpotAvgEntryPrices(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribeAccountSpotAvgEntryPrices(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribeAccountSpotAvgEntryPrices: %v", err)
	}
}

func TestWebsocketClient_UnsubscribePoolData(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribePoolData(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribePoolData(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribePoolData: %v", err)
	}
}

func TestWebsocketClient_UnsubscribePoolInfo(t *testing.T) {
	client, auth := lighterWSAuth(t)
	ws := newLiveWSClient(t)
	_ = ws.SubscribePoolInfo(client.AccountIndex, auth, func([]byte) {})
	if err := ws.UnsubscribePoolInfo(client.AccountIndex); err != nil {
		t.Fatalf("UnsubscribePoolInfo: %v", err)
	}
}

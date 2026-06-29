package okx

import "testing"

func TestWSAccountCompanion_SubscribeArgs(t *testing.T) {
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP", InstId: "BTC-USDT-SWAP"}
	if args.Channel != "orders" || args.InstType != "SWAP" || args.InstId == "" {
		t.Fatalf("unexpected args: %+v", args)
	}
}

func TestWSAccountCompanion_BusinessPrivateSubscribeArgs(t *testing.T) {
	algo := WsSubscribeArgs{Channel: "orders-algo", InstType: "SWAP"}
	advance := WsSubscribeArgs{Channel: "algo-advance", InstType: "SWAP"}
	spread := WsSubscribeArgs{Channel: "sprd-orders"}
	if algo.Channel != "orders-algo" || advance.Channel != "algo-advance" || spread.Channel != "sprd-orders" {
		t.Fatalf("unexpected business args: %+v %+v %+v", algo, advance, spread)
	}
}

func TestWSClient_SubscribeOrders(t *testing.T) {
	client := newLivePrivateOKXWSClient(t)
	instID := okxSwapInstID

	if err := client.SubscribeOrders("SWAP", &instID, func(*Order) {}); err != nil {
		t.Fatalf("SubscribeOrders: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "orders", InstType: "SWAP", InstId: instID}] == nil {
		t.Fatalf("expected orders subscription to be registered")
	}
}

func TestWSClient_SubscribePositions(t *testing.T) {
	client := newLivePrivateOKXWSClient(t)

	if err := client.SubscribePositions("SWAP", func(*Position) {}); err != nil {
		t.Fatalf("SubscribePositions: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "positions", InstType: "SWAP"}] == nil {
		t.Fatalf("expected positions subscription to be registered")
	}
}

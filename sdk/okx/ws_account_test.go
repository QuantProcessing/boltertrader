package okx

import (
	"strings"
	"testing"
	"time"
)

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

func TestWSClient_PrivateAccountWithErrorHandlersSurfaceMalformedPayloads(t *testing.T) {
	client := newLocalPublicOKXWSClient(t)
	malformed := []byte(`{"arg":{"channel":"broken"},"data":{`)
	instID := okxSwapInstID

	tests := []struct {
		name      string
		args      WsSubscribeArgs
		subscribe func(func(error)) error
		want      string
	}{
		{
			name: "orders",
			args: WsSubscribeArgs{Channel: "orders", InstType: "SWAP", InstId: instID},
			subscribe: func(onError func(error)) error {
				return client.SubscribeOrdersWithError("SWAP", &instID, func(*Order) {
					t.Fatal("unexpected order callback for malformed payload")
				}, onError)
			},
			want: "unmarshal orders",
		},
		{
			name: "account",
			args: WsSubscribeArgs{Channel: "account"},
			subscribe: func(onError func(error)) error {
				return client.SubscribeAccountWithError(func(*Balance) {
					t.Fatal("unexpected account callback for malformed payload")
				}, onError)
			},
			want: "unmarshal account",
		},
		{
			name: "positions",
			args: WsSubscribeArgs{Channel: "positions", InstType: "SWAP"},
			subscribe: func(onError func(error)) error {
				return client.SubscribePositionsWithError("SWAP", func(*Position) {
					t.Fatal("unexpected position callback for malformed payload")
				}, onError)
			},
			want: "unmarshal positions",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errCh := make(chan error, 1)
			if err := test.subscribe(func(err error) { errCh <- err }); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			handler := client.Subs[test.args]
			if handler == nil {
				t.Fatalf("subscription %+v was not registered", test.args)
			}
			handler(malformed)
			select {
			case err := <-errCh:
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error = %v, want %q", err, test.want)
				}
			case <-time.After(time.Second):
				t.Fatal("expected malformed payload error callback")
			}
		})
	}
}

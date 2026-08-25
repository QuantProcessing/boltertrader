package gateway

import "testing"

func TestRealtimeMarketIsSingletonPerGatewayClient(t *testing.T) {
	client := &Client{}

	first := client.RealtimeMarket()
	second := client.RealtimeMarket()

	if first == nil || first != second {
		t.Fatalf("RealtimeMarket() must reuse one multiplexed source per client: first=%p second=%p", first, second)
	}
}

package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestWsFundingRateEventAcceptsDocumentedNumberFields(t *testing.T) {
	t.Parallel()

	var event WsFundingRateEvent
	err := json.Unmarshal([]byte(`{"type":"quote-event","channel":"fundingRate.10000001","content":{"dataType":"Snapshot","channel":"fundingRate.10000001","data":[{"contractId":10000001,"fundingRate":"0.0001","nextFundingTime":1693219200000,"timestamp":1693208170000}]}}`), &event)
	if err != nil {
		t.Fatalf("unmarshal funding event: %v", err)
	}
	if got := event.Content.Data[0].ContractId.String(); got != "10000001" {
		t.Fatalf("unexpected contract id: %s", got)
	}
	if got := event.Content.Data[0].NextFundingTime.String(); got != "1693219200000" {
		t.Fatalf("unexpected next funding time: %s", got)
	}
}

func TestSubscribeFundingRateRegistersFundingChannel(t *testing.T) {
	t.Parallel()

	wsClient := NewWsMarketClient(context.Background())
	err := wsClient.SubscribeFundingRate("10000001", func(event *WsFundingRateEvent) {})
	if err == nil {
		t.Fatal("expected websocket not connected error")
	}
	if _, ok := wsClient.subs["fundingRate.10000001"]; !ok {
		t.Fatalf("funding channel callback was not registered")
	}
}

func TestSubscribeAllFundingRatesReceivesLiveEvent(t *testing.T) {
	if os.Getenv("EDGEX_REALTIME_WS") != "1" {
		t.Skip("set EDGEX_REALTIME_WS=1 to run real EdgeX all-market funding smoke test")
	}
	wsClient := NewWsMarketClient(context.Background())
	if err := wsClient.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer wsClient.Close()

	done := make(chan struct{}, 1)
	if err := wsClient.SubscribeAllFundingRates(func(event *WsFundingRateEvent) {
		if event == nil {
			return
		}
		for _, row := range event.Content.Data {
			if row.ContractId.String() != "" && row.FundingRate != "" {
				select {
				case done <- struct{}{}:
				default:
				}
				return
			}
		}
	}); err != nil {
		t.Fatalf("SubscribeAllFundingRates: %v", err)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Timeout waiting for EdgeX all-market funding payload")
	}
}

func TestSubMarketData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping realtime websocket test under -short")
	}
	wsClient := NewWsMarketClient(context.Background())
	err := wsClient.Connect()
	if err != nil {
		fmt.Println(err)
		return
	}
	err = wsClient.SubscribeKline("10000001", PriceTypeLastPrice, KlineInterval1m, func(event *WsKlineEvent) {
		fmt.Println(event)
	})

	if err != nil {
		fmt.Println(err)
		return
	}

	timeout := time.NewTimer(5 * time.Second)
	<-timeout.C
}

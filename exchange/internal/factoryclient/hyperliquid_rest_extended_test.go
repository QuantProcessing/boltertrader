package factoryclient

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestHyperliquidHistoricalOrderRejectsUnsupportedNativeOrderTypes(t *testing.T) {
	meta := hyperliquidMarketMeta{
		instrument: exchange.Instrument{Symbol: "BTC-USDC", Product: exchange.ProductPerp},
		nativeCoin: "BTC",
	}
	tests := []struct {
		name      string
		orderType string
		isTrigger bool
	}{
		{name: "trigger", orderType: "Stop Market", isTrigger: true},
		{name: "unknown", orderType: "Trigger"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := hyperliquid.HistoricalOrder{
				Order: hyperliquid.HistoricalOrderDetails{
					Coin:          "BTC",
					Side:          hyperliquid.SideBid,
					LimitPrice:    "100",
					RemainingSize: "0",
					OrderID:       17,
					Timestamp:     1700000000000,
					OriginalSize:  "1",
					OrderType:     test.orderType,
					TimeInForce:   "Gtc",
					IsTrigger:     test.isTrigger,
				},
				Status:          "filled",
				StatusTimestamp: 1700000000100,
			}
			if _, err := hlHistoricalOrder(meta, row); err == nil {
				t.Fatalf("hlHistoricalOrder(%q, trigger=%t) succeeded; want rejection", test.orderType, test.isTrigger)
			}
		})
	}
}

func TestHyperliquidFundingRateHistoryAllowsOptionalZeroBounds(t *testing.T) {
	var fundingRequest map[string]any
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch payload["type"] {
		case "metaAndAssetCtxs":
			return openAPIJSONResponse(`[{"universe":[{"name":"BTC"}]},[{"funding":"0.0001","markPx":"100","oraclePx":"100","midPx":"100","premium":"0"}]]`), nil
		case "meta":
			return openAPIJSONResponse(`{"universe":[{"name":"BTC","szDecimals":3,"maxLeverage":50}]}`), nil
		case "fundingHistory":
			fundingRequest = payload
			return openAPIJSONResponse(`[{"coin":"BTC","fundingRate":"0.0001","premium":"0","time":1720000000000}]`), nil
		default:
			t.Fatalf("unexpected Hyperliquid info request: %#v", payload)
			return nil, nil
		}
	})
	client := NewHyperliquidPerp(openAPITestPrivateKey, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	})

	page, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{
		Instrument: "BTC-USDC",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("FundingRateHistory with optional zero bounds: %v", err)
	}
	if len(page.Rates) != 1 {
		t.Fatalf("FundingRateHistory rows=%d, want 1", len(page.Rates))
	}
	if got := fundingRequest["startTime"]; got != float64(0) {
		t.Fatalf("native startTime=%#v, want epoch sentinel 0", got)
	}
	if _, exists := fundingRequest["endTime"]; exists {
		t.Fatalf("native request unexpectedly included optional endTime: %#v", fundingRequest)
	}
}

func TestHyperliquidSpotBalancesDoesNotFabricateRowsForEmptyAccount(t *testing.T) {
	balances, err := hlSpotBalances("Balances", &hyperliquid.SpotClearinghouseState{})
	if err != nil {
		t.Fatalf("hlSpotBalances empty account: %v", err)
	}
	if balances == nil {
		t.Fatal("hlSpotBalances empty account returned nil, want an empty result")
	}
	if len(balances) != 0 {
		t.Fatalf("hlSpotBalances empty account rows=%d, want 0", len(balances))
	}
}

func TestHyperliquidSpotFillsFiltersRequestedInstrumentBeforeValidatingOtherProductRows(t *testing.T) {
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch payload["type"] {
		case "spotMeta":
			return openAPIJSONResponse(`{"tokens":[{"name":"USDC","szDecimals":6,"weiDecimals":6,"index":0,"isCanonical":true},{"name":"PURR","szDecimals":3,"weiDecimals":3,"index":1,"isCanonical":true}],"universe":[{"name":"@1","index":1,"tokens":[1,0],"isCanonical":true}]}`), nil
		case "userFills":
			return openAPIJSONResponse(`[
				{"coin":"BTC","px":"100","sz":"1","side":"B","time":1720000000000,"startPosition":"0","dir":"Open Long","closedPnl":"0","hash":"0xperp","oid":40,"crossed":true,"fee":"0.01","feeToken":"USDC","tid":2},
				{"coin":"@1","px":"0.5","sz":"10","side":"B","time":1720000001000,"startPosition":"0","dir":"Buy","closedPnl":"0","hash":"0xspot","oid":41,"crossed":true,"fee":"0.01","feeToken":"USDC","tid":3}
			]`), nil
		default:
			t.Fatalf("unexpected Hyperliquid request: %#v", payload)
			return nil, nil
		}
	})
	client := NewHyperliquidSpot(hyperliquidConstructionPrivateKey, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	})

	page, err := client.Fills(context.Background(), exchange.FillsRequest{
		Instrument: "PURR-USDC",
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("Fills for requested spot instrument: %v", err)
	}
	if len(page.Fills) != 1 || page.Fills[0].Instrument != "PURR-USDC" {
		t.Fatalf("filtered fills=%#v, want only PURR-USDC", page.Fills)
	}
}

func TestHyperliquidPerpFillsFiltersRequestedInstrumentBeforeValidatingOtherProductRows(t *testing.T) {
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch payload["type"] {
		case "metaAndAssetCtxs":
			return openAPIJSONResponse(`[{"universe":[{"name":"BTC"}]},[{"funding":"0.0001","markPx":"100","oraclePx":"100","midPx":"100","premium":"0"}]]`), nil
		case "meta":
			return openAPIJSONResponse(`{"universe":[{"name":"BTC","szDecimals":3,"maxLeverage":50}]}`), nil
		case "userFills":
			return openAPIJSONResponse(`[
				{"coin":"@1","px":"0.5","sz":"10","side":"B","time":1720000001000,"startPosition":"0","dir":"Buy","closedPnl":"0","hash":"0xspot","oid":41,"crossed":true,"fee":"0.01","feeToken":"USDC","tid":3},
				{"coin":"BTC","px":"100","sz":"1","side":"B","time":1720000000000,"startPosition":"0","dir":"Open Long","closedPnl":"0","hash":"0xperp","oid":40,"crossed":true,"fee":"0.01","feeToken":"USDC","tid":2}
			]`), nil
		default:
			t.Fatalf("unexpected Hyperliquid request: %#v", payload)
			return nil, nil
		}
	})
	client := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	})

	page, err := client.Fills(context.Background(), exchange.FillsRequest{
		Instrument: "BTC-USDC",
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("Fills for requested perp instrument: %v", err)
	}
	if len(page.Fills) != 1 || page.Fills[0].Instrument != "BTC-USDC" {
		t.Fatalf("filtered fills=%#v, want only BTC-USDC", page.Fills)
	}
}

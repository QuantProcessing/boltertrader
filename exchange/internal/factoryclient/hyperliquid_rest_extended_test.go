package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/shopspring/decimal"
)

func TestHyperliquidHistoricalOrderRejectsUnsupportedNativeOrderTypes(t *testing.T) {
	meta := hyperliquidMarketMeta{
		instrument: hyperliquidCatalogInstrument(exchange.ProductPerp, "BTC-USDC", "BTC", "0", "USDC", 3),
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

func TestHyperliquidOrderHistoryFailsClosedOnUnfilteredUnknownCoin(t *testing.T) {
	transport := hyperliquidHistoricalOrdersTransport(t, `[
		{"order":`+hyperliquidHistoricalOrderJSON("UNKNOWN", "40", "100")+`,"status":"filled","statusTimestamp":1720000001000},
		{"order":`+hyperliquidHistoricalOrderJSON("BTC", "41", "101")+`,"status":"filled","statusTimestamp":1720000002000}
	]`)
	settings := hyperliquidOpenAPICatalogSettings(t, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	}, exchange.ProductPerp)
	client := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, settings)

	_, err := client.OrderHistory(context.Background(), exchange.OrderHistoryRequest{Limit: 20})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("unfiltered unknown historical row error = %v, want ErrMalformedResponse", err)
	}
}

func TestHyperliquidOrderHistoryFiltersRequestedInstrumentBeforeUnknownRows(t *testing.T) {
	transport := hyperliquidHistoricalOrdersTransport(t, `[
		{"order":`+hyperliquidHistoricalOrderJSON("UNKNOWN", "40", "100")+`,"status":"filled","statusTimestamp":1720000001000},
		{"order":`+hyperliquidHistoricalOrderJSON("BTC", "41", "101")+`,"status":"filled","statusTimestamp":1720000002000}
	]`)
	settings := hyperliquidOpenAPICatalogSettings(t, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	}, exchange.ProductPerp)
	client := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, settings)
	instrument, err := client.ResolveInstrument(context.Background(), "BTC-USDC")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}

	page, err := client.OrderHistory(context.Background(), exchange.OrderHistoryRequest{Instrument: &instrument, Limit: 20})
	if err != nil {
		t.Fatalf("filtered OrderHistory: %v", err)
	}
	if len(page.Orders) != 1 || page.Orders[0].NativeSymbol != "BTC" || page.Orders[0].OrderID != "41" {
		t.Fatalf("filtered order history=%#v, want only BTC order 41", page.Orders)
	}
}

func hyperliquidHistoricalOrdersTransport(t *testing.T, history string) http.RoundTripper {
	t.Helper()
	return openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch payload["type"] {
		case "metaAndAssetCtxs":
			return openAPIJSONResponse(`[{"universe":[{"name":"BTC"}]},[{"funding":"0.0001","markPx":"100","oraclePx":"100","midPx":"100","premium":"0"}]]`), nil
		case "meta":
			return openAPIJSONResponse(`{"universe":[{"name":"BTC","szDecimals":3,"maxLeverage":50}]}`), nil
		case "historicalOrders":
			return openAPIJSONResponse(history), nil
		default:
			t.Fatalf("unexpected Hyperliquid request: %#v", payload)
			return nil, nil
		}
	})
}

func hyperliquidHistoricalOrderJSON(coin, orderID, clientOrderID string) string {
	return `{"coin":"` + coin + `","side":"B","limitPx":"99","sz":"0","oid":` + orderID + `,"cloid":"` + hyperliquidNativeClientID(clientOrderID) + `","timestamp":1720000000000,"origSz":"1","reduceOnly":true,"orderType":"Limit","tif":"Gtc","isTrigger":false,"triggerPx":"0","triggerCondition":""}`
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
	settings := hyperliquidOpenAPICatalogSettings(t, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	}, exchange.ProductPerp)
	client := NewHyperliquidPerp(openAPITestPrivateKey, settings)
	instrument, err := client.ResolveInstrument(context.Background(), "BTC-USDC")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}

	page, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{
		Instrument: instrument,
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
	settings := hyperliquidOpenAPICatalogSettings(t, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	}, exchange.ProductSpot)
	client := NewHyperliquidSpot(hyperliquidConstructionPrivateKey, settings)
	instrument, err := client.ResolveInstrument(context.Background(), "PURR-USDC")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}

	page, err := client.Fills(context.Background(), exchange.FillsRequest{
		Instrument: &instrument,
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("Fills for requested spot instrument: %v", err)
	}
	if len(page.Fills) != 1 || page.Fills[0].NativeSymbol != "@1" || page.Fills[0].NativeID != "10001" {
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
	settings := hyperliquidOpenAPICatalogSettings(t, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	}, exchange.ProductPerp)
	client := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, settings)
	instrument, err := client.ResolveInstrument(context.Background(), "BTC-USDC")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}

	page, err := client.Fills(context.Background(), exchange.FillsRequest{
		Instrument: &instrument,
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("Fills for requested perp instrument: %v", err)
	}
	if len(page.Fills) != 1 || page.Fills[0].NativeSymbol != "BTC" || page.Fills[0].NativeID != "0" {
		t.Fatalf("filtered fills=%#v, want only BTC-USDC", page.Fills)
	}
}

func TestHyperliquidFilteredAccountRowsSkipUnrelatedNativeCoinsBeforeMetadataValidation(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, exchange.PerpClient, exchange.Instrument) error
	}{
		{
			name: "perp open orders",
			run: func(ctx context.Context, client exchange.PerpClient, instrument exchange.Instrument) error {
				page, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &instrument, Limit: 20})
				if err != nil {
					return err
				}
				if len(page.Orders) != 1 || page.Orders[0].NativeSymbol != "BTC" {
					t.Fatalf("filtered open orders=%#v, want only BTC", page.Orders)
				}
				return nil
			},
		},
		{
			name: "perp positions",
			run: func(ctx context.Context, client exchange.PerpClient, instrument exchange.Instrument) error {
				rows, err := client.Positions(ctx, exchange.PositionsRequest{Instrument: &instrument})
				if err != nil {
					return err
				}
				if len(rows) != 1 || rows[0].NativeSymbol != "BTC" {
					t.Fatalf("filtered positions=%#v, want only BTC", rows)
				}
				return nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := hyperliquidFilteredRowsTransport(t, true)
			settings := hyperliquidOpenAPICatalogSettings(t, Settings{
				Endpoint:    "https://openapi.invalid",
				Environment: "testnet",
				HTTPClient:  &http.Client{Transport: transport},
			}, exchange.ProductPerp)
			client := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, settings)
			instrument, err := client.ResolveInstrument(context.Background(), "BTC-USDC")
			if err != nil {
				t.Fatalf("ResolveInstrument: %v", err)
			}
			if err := test.run(context.Background(), client, instrument); err != nil {
				t.Fatalf("%s filtered request: %v", test.name, err)
			}

			unfiltered := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, settings)
			_, err = unfiltered.OpenOrders(context.Background(), exchange.OpenOrdersRequest{Limit: 20})
			if !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("unfiltered unknown row error = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

func TestHyperliquidSpotOpenOrdersFiltersRequestedInstrumentBeforeUnknownRows(t *testing.T) {
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch payload["type"] {
		case "spotMeta":
			return openAPIJSONResponse(`{"tokens":[{"name":"USDC","szDecimals":6,"weiDecimals":6,"index":0,"isCanonical":true},{"name":"PURR","szDecimals":3,"weiDecimals":3,"index":1,"isCanonical":true}],"universe":[{"name":"@1","index":1,"tokens":[1,0],"isCanonical":true}]}`), nil
		case "frontendOpenOrders":
			return openAPIJSONResponse(`[
				` + hyperliquidOpenAPIOrderJSON("UNKNOWN", "1", "40", "100", false) + `,
				` + hyperliquidOpenAPIOrderJSON("@1", "1", "41", "101", false) + `
			]`), nil
		default:
			t.Fatalf("unexpected Hyperliquid request: %#v", payload)
			return nil, nil
		}
	})
	settings := hyperliquidOpenAPICatalogSettings(t, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	}, exchange.ProductSpot)
	client := NewHyperliquidSpot(hyperliquidConstructionPrivateKey, settings)
	instrument, err := client.ResolveInstrument(context.Background(), "PURR-USDC")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}
	page, err := client.OpenOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument, Limit: 20})
	if err != nil {
		t.Fatalf("filtered spot OpenOrders: %v", err)
	}
	if len(page.Orders) != 1 || page.Orders[0].NativeSymbol != "@1" {
		t.Fatalf("filtered spot open orders=%#v, want only @1", page.Orders)
	}
	_, err = client.OpenOrders(context.Background(), exchange.OpenOrdersRequest{Limit: 20})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("unfiltered unknown row error = %v, want ErrMalformedResponse", err)
	}
}

func TestHyperliquidPositionsDeriveMarkPriceFromClearinghousePositionValue(t *testing.T) {
	transport := hyperliquidFilteredRowsTransport(t, false)
	settings := hyperliquidOpenAPICatalogSettings(t, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	}, exchange.ProductPerp)
	client := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, settings)
	instrument, err := client.ResolveInstrument(context.Background(), "BTC-USDC")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}
	rows, err := client.Positions(context.Background(), exchange.PositionsRequest{Instrument: &instrument})
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("positions=%#v, want one", rows)
	}
	want := decimal.RequireFromString("82.3")
	if !rows[0].MarkPrice.Equal(want) {
		t.Fatalf("mark price = %s, want %s from positionValue/szi", rows[0].MarkPrice, want)
	}
}

func TestHyperliquidPositionsRejectMissingPositionValueForNonzeroPosition(t *testing.T) {
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
		case "clearinghouseState":
			return openAPIJSONResponse(`{"assetPositions":[{"position":{"coin":"BTC","cumFunding":{"allTime":"0","sinceOpen":"0","sinceChange":"0"},"entryPx":"99","leverage":{"rawUsd":"0","type":"isolated","value":5},"liquidationPx":"50","marginUsed":"10","maxLeverage":50,"returnOnEquity":"0.1","szi":"1.5","unrealizedPnl":"1"},"type":"oneWay"}],"crossMaintenanceMarginUsed":"1","crossMarginSummary":{"accountValue":"100","totalMarginUsed":"10","totalNtlPos":"100","totalRawUsd":"0"},"marginSummary":{"accountValue":"100","totalMarginUsed":"10","totalNtlPos":"100","totalRawUsd":"0"},"time":1720000000000,"withdrawable":"90"}`), nil
		default:
			t.Fatalf("unexpected Hyperliquid request: %#v", payload)
			return nil, nil
		}
	})
	settings := hyperliquidOpenAPICatalogSettings(t, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	}, exchange.ProductPerp)
	client := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, settings)
	instrument, err := client.ResolveInstrument(context.Background(), "BTC-USDC")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}
	_, err = client.Positions(context.Background(), exchange.PositionsRequest{Instrument: &instrument})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("Positions error = %v, want ErrMalformedResponse", err)
	}
}

func hyperliquidFilteredRowsTransport(t *testing.T, includeUnknown bool) http.RoundTripper {
	t.Helper()
	return openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch payload["type"] {
		case "metaAndAssetCtxs":
			return openAPIJSONResponse(`[{"universe":[{"name":"BTC"}]},[{"funding":"0.0001","markPx":"999","oraclePx":"999","midPx":"999","premium":"0"}]]`), nil
		case "meta":
			return openAPIJSONResponse(`{"universe":[{"name":"BTC","szDecimals":3,"maxLeverage":50}]}`), nil
		case "frontendOpenOrders":
			prefix := ""
			if includeUnknown {
				prefix = hyperliquidOpenAPIOrderJSON("UNKNOWN", "1", "40", "100", false) + ","
			}
			return openAPIJSONResponse(`[` + prefix + hyperliquidOpenAPIOrderJSON("BTC", "1", "41", "101", true) + `]`), nil
		case "clearinghouseState":
			prefix := ""
			if includeUnknown {
				prefix = `{"position":{"coin":"UNKNOWN","cumFunding":{"allTime":"0","sinceOpen":"0","sinceChange":"0"},"entryPx":"1","leverage":{"rawUsd":"0","type":"isolated","value":1},"liquidationPx":"0","marginUsed":"1","maxLeverage":1,"positionValue":"1","returnOnEquity":"0","szi":"1","unrealizedPnl":"0"},"type":"oneWay"},`
			}
			return openAPIJSONResponse(`{"assetPositions":[` + prefix + `{"position":{"coin":"BTC","cumFunding":{"allTime":"0","sinceOpen":"0","sinceChange":"0"},"entryPx":"99","leverage":{"rawUsd":"0","type":"isolated","value":5},"liquidationPx":"50","marginUsed":"10","maxLeverage":50,"positionValue":"123.45","returnOnEquity":"0.1","szi":"1.5","unrealizedPnl":"1"},"type":"oneWay"}],"crossMaintenanceMarginUsed":"1","crossMarginSummary":{"accountValue":"100","totalMarginUsed":"10","totalNtlPos":"100","totalRawUsd":"0"},"marginSummary":{"accountValue":"100","totalMarginUsed":"10","totalNtlPos":"100","totalRawUsd":"0"},"time":1720000000000,"withdrawable":"90"}`), nil
		default:
			t.Fatalf("unexpected Hyperliquid request: %#v", payload)
			return nil, nil
		}
	})
}

package factoryclient

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

func TestLighterMarketProtectionPriceUsesFarSideAndConservativeTickRounding(t *testing.T) {
	sdk := lighter.NewClient().WithBaseURL("https://openapi.invalid")
	sdk.HTTPClient = &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return openAPIJSONResponse(`{"code":200,"message":"","bids":[{"remaining_base_amount":"1","price":"99"}],"asks":[{"remaining_base_amount":"1","price":"101"}]}`), nil
	})}
	meta := lighterMarketMeta{
		marketID:   7,
		priceScale: decimal.NewFromInt(10),
	}

	buy, err := lighterMarketProtectionPrice(context.Background(), sdk, exchange.ProductSpot, meta, exchange.SideBuy)
	if err != nil {
		t.Fatalf("buy protection price: %v", err)
	}
	if buy != 1016 {
		t.Fatalf("buy protection price = %d, want 1016", buy)
	}

	sell, err := lighterMarketProtectionPrice(context.Background(), sdk, exchange.ProductSpot, meta, exchange.SideSell)
	if err != nil {
		t.Fatalf("sell protection price: %v", err)
	}
	if sell != 985 {
		t.Fatalf("sell protection price = %d, want 985", sell)
	}
}

func TestLighterValidatePlaceEnforcesVenueUint48ClientOrderIndex(t *testing.T) {
	meta := lighterMarketMeta{
		instrument: exchange.Instrument{MinQuantity: decimal.NewFromInt(1)},
		sizeScale:  decimal.NewFromInt(1),
	}
	request := exchange.PlaceOrderRequest{
		Instrument:    "ETH-USDC",
		ClientOrderID: "281474976710655",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.NewFromInt(1),
	}
	if _, _, _, err := lighterValidatePlace(exchange.ProductSpot, meta, request); err != nil {
		t.Fatalf("maximum uint48 client order index rejected: %v", err)
	}

	request.ClientOrderID = "281474976710656"
	if _, _, _, err := lighterValidatePlace(exchange.ProductSpot, meta, request); err == nil {
		t.Fatal("client order index above uint48 was accepted")
	}
}

func TestLighterPlaceRequestUsesBoundedAbsoluteExpiryForRestingOrders(t *testing.T) {
	meta := lighterMarketMeta{marketID: 7}
	base := exchange.PlaceOrderRequest{
		Instrument:    "ETH-USDC",
		ClientOrderID: "101",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.NewFromInt(1),
		LimitPrice:    decimal.NewFromInt(100),
		LimitPolicy:   exchange.LimitPolicyResting,
	}
	before := time.Now().Add(27 * 24 * time.Hour).UnixMilli()
	resting := lighterPlaceRequest(meta, base, 10000, 10, 101, 0)
	after := time.Now().Add(29 * 24 * time.Hour).UnixMilli()
	if resting.OrderExpiry < before || resting.OrderExpiry > after {
		t.Fatalf("resting expiry = %d, want an absolute timestamp about 28 days ahead", resting.OrderExpiry)
	}

	base.LimitPolicy = exchange.LimitPolicyPostOnly
	postOnly := lighterPlaceRequest(meta, base, 10000, 10, 102, 0)
	if postOnly.OrderExpiry < before || postOnly.OrderExpiry > after {
		t.Fatalf("post-only expiry = %d, want an absolute timestamp about 28 days ahead", postOnly.OrderExpiry)
	}

	base.LimitPolicy = exchange.LimitPolicyIOC
	ioc := lighterPlaceRequest(meta, base, 10000, 10, 103, 0)
	if ioc.OrderExpiry != lighter.DefaultIocExpiry {
		t.Fatalf("IOC expiry = %d, want %d", ioc.OrderExpiry, lighter.DefaultIocExpiry)
	}
}

func TestLighterSpotBalancesTreatsVenueBalanceAsAvailable(t *testing.T) {
	balances, err := lighterSpotBalances(
		[]*lighter.SpotAsset{{
			Symbol:        "USDC",
			Balance:       "2",
			LockedBalance: "8",
		}},
		exchange.ProductSpot,
		"Balances",
	)
	if err != nil {
		t.Fatalf("lighterSpotBalances: %v", err)
	}
	if len(balances) != 1 {
		t.Fatalf("balances length = %d, want 1", len(balances))
	}
	balance := balances[0]
	if !balance.Available.Equal(decimal.NewFromInt(2)) ||
		!balance.Locked.Equal(decimal.NewFromInt(8)) ||
		!balance.Total.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("portable balance = %+v, want available=2 locked=8 total=10", balance)
	}
}

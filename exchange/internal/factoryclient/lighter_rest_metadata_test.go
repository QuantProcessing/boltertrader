package factoryclient

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
)

func TestLighterBuildMetasAcceptsZeroMarketID(t *testing.T) {
	response := &lighter.OrderBookDetailsResponse{
		Code: 200,
		OrderBookDetails: []*lighter.OrderBookDetail{
			{
				Symbol:                 "ETH",
				MarketId:               0,
				MarketType:             lighterPerp,
				MinBaseAmount:          "0.001",
				MinQuoteAmount:         "5",
				SizeDecimals:           3,
				PriceDecimals:          2,
				SupportedQuoteDecimals: 6,
			},
		},
	}

	metas, byID, err := lighterBuildMetas(response, exchange.ProductPerp, lighterPerp)
	if err != nil {
		t.Fatalf("lighterBuildMetas: %v", err)
	}
	if got := metas["ETH"].marketID; got != 0 {
		t.Fatalf("symbol market id = %d, want 0", got)
	}
	if got := byID[0].instrument.Symbol; got != "ETH" {
		t.Fatalf("id 0 symbol = %q, want ETH", got)
	}
}

func TestLighterMetaDecimalsUsesSupportedValueThenLegacyValue(t *testing.T) {
	tests := []struct {
		name     string
		primary  uint8
		fallback uint8
		want     uint8
		wantErr  bool
	}{
		{name: "supported value", primary: 6, fallback: 3, want: 6},
		{name: "legacy value", primary: 0, fallback: 3, want: 3},
		{name: "zero is valid", primary: 0, fallback: 0, want: 0},
		{name: "supported value exceeds bound", primary: 19, fallback: 3, wantErr: true},
		{name: "legacy value exceeds bound", primary: 0, fallback: 19, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := lighterMetaDecimals(test.primary, test.fallback)
			if test.wantErr {
				if err == nil {
					t.Fatal("lighterMetaDecimals expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("lighterMetaDecimals: %v", err)
			}
			if got != test.want {
				t.Fatalf("lighterMetaDecimals = %d, want %d", got, test.want)
			}
		})
	}
}

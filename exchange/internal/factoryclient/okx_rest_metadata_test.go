package factoryclient

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestOKXPerpMetasFilterNonUSDTLinearSwapRows(t *testing.T) {
	rows := []okx.Instrument{
		{
			InstId:    "BTC-USD-SWAP",
			InstType:  okxSwapType,
			State:     "live",
			SettleCcy: "BTC",
			CtValCcy:  "USD",
			CtVal:     "100",
			LotSz:     "1",
			MinSz:     "1",
			TickSz:    "0.1",
		},
		{
			InstId:    "BTC-USDT-SWAP",
			InstType:  okxSwapType,
			State:     "live",
			SettleCcy: "USDT",
			CtValCcy:  "BTC",
			CtVal:     "0.01",
			LotSz:     "1",
			MinSz:     "1",
			TickSz:    "0.1",
		},
	}

	metas, err := okxPerpMetasFromRows(rows)
	if err != nil {
		t.Fatalf("okxPerpMetasFromRows: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("metas = %+v, want only one USDT-linear row", metas)
	}
	if _, ok := metas["BTC-USDT-SWAP"]; !ok {
		t.Fatalf("metas = %+v, missing BTC-USDT-SWAP", metas)
	}
}

func TestOKXPerpMetasRejectMalformedUSDTSettledSwapRow(t *testing.T) {
	_, err := okxPerpMetasFromRows([]okx.Instrument{{
		InstId:    "malformed",
		InstType:  okxSwapType,
		State:     "live",
		SettleCcy: "USDT",
		CtValCcy:  "BTC",
		CtVal:     "0.01",
		LotSz:     "1",
		MinSz:     "1",
		TickSz:    "0.1",
	}})
	if err == nil {
		t.Fatal("malformed USDT-settled SWAP row succeeded")
	}
}

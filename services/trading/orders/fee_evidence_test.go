package orders

import (
	"testing"
	"time"
)

func TestFillEvidenceEnrichmentCannotChangeMoneyOrEraseInstrumentFacts(t *testing.T) {
	original := ExecutionEvidence{Fills: []FillEvidence{{FillID: "f", AccountID: "a", InstrumentID: "BTC-USDT", Side: "buy", Quantity: "1", Price: "100", Fee: "0.001", FeeCurrency: "BTC", SettlementCurrency: "USDT", OccurredAt: time.Now()}}}
	next := original
	next.Fills = append([]FillEvidence(nil), original.Fills...)
	next.Fills[0].InstrumentProduct, next.Fills[0].BaseAsset = "spot", "BTC"
	enriched, err := mergeExecutionEvidence(original, next)
	if err != nil || len(enriched.Fills) != 1 || enriched.Fills[0].BaseAsset != "BTC" {
		t.Fatalf("enrichment=%+v err=%v", enriched, err)
	}
	replay, err := mergeExecutionEvidence(enriched, original)
	if err != nil || replay.Fills[0].InstrumentProduct != "spot" || replay.Fills[0].BaseAsset != "BTC" {
		t.Fatalf("metadata lost=%+v err=%v", replay, err)
	}
	for _, change := range []func(*FillEvidence){func(f *FillEvidence) { f.Fee = "-0.001" }, func(f *FillEvidence) { f.BaseAsset = "ETH" }, func(f *FillEvidence) { f.InstrumentProduct = "perp" }} {
		conflicting := next
		conflicting.Fills = append([]FillEvidence(nil), next.Fills...)
		change(&conflicting.Fills[0])
		if _, err := mergeExecutionEvidence(enriched, conflicting); err == nil {
			t.Fatalf("conflicting facts accepted=%+v", conflicting)
		}
	}
}

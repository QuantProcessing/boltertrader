package lighter

import "testing"

func TestL2UpdateMarginTxInfo_Hash(t *testing.T) {
	info := &L2UpdateMarginTxInfo{
		AccountIndex: 42,
		ApiKeyIndex:  7,
		MarketIndex:  1,
		USDCAmount:   100,
		Direction:    1,
		ExpiredAt:    200,
		Nonce:        300,
	}

	first, err := info.Hash(MainnetChainID)
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	second, err := info.Hash(MainnetChainID)
	if err != nil {
		t.Fatalf("Hash returned error on second call: %v", err)
	}

	if len(first) == 0 || string(first) != string(second) {
		t.Fatalf("expected deterministic non-empty hash")
	}
}

func TestL2CreateGroupedOrdersTxInfo_Hash(t *testing.T) {
	info := &L2CreateGroupedOrdersTxInfo{
		AccountIndex: 42,
		ApiKeyIndex:  7,
		GroupingType: 1,
		ExpiredAt:    200,
		Nonce:        300,
		Orders: []*CreateOrderInfo{{
			MarketIndex:      1,
			ClientOrderIndex: 10,
			BaseAmount:       100,
			Price:            200,
			IsAsk:            0,
			Type:             OrderTypeLimit,
			TimeInForce:      OrderTimeInForcePostOnly,
			ReduceOnly:       0,
			TriggerPrice:     NilTriggerPrice,
			OrderExpiry:      Default28DayOrderExpiry,
		}},
	}

	first, err := info.Hash(MainnetChainID)
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	second, err := info.Hash(MainnetChainID)
	if err != nil {
		t.Fatalf("Hash returned error on second call: %v", err)
	}

	if len(first) == 0 || string(first) != string(second) {
		t.Fatalf("expected deterministic non-empty hash")
	}
}

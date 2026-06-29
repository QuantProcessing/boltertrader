package sdk

import (
	"encoding/json"
	"testing"
)

func TestFlexibleFeeDetails_UnmarshalJSON(t *testing.T) {
	var details FlexibleFeeDetails
	if err := json.Unmarshal([]byte(`[{"feeCoin":"USDT","fee":"0.1"}]`), &details); err != nil {
		t.Fatalf("array UnmarshalJSON returned error: %v", err)
	}
	if len(details) != 1 || details[0].FeeCoin != "USDT" {
		t.Fatalf("unexpected details: %#v", details)
	}

	if err := json.Unmarshal([]byte(`""`), &details); err != nil {
		t.Fatalf("empty string UnmarshalJSON returned error: %v", err)
	}
	if details != nil {
		t.Fatalf("expected nil details, got %#v", details)
	}
}

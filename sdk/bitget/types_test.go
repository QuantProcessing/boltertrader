package sdk

import (
	"encoding/json"
	"testing"
)

func TestNumberString_UnmarshalJSON(t *testing.T) {
	var value NumberString
	if err := json.Unmarshal([]byte(`123.45`), &value); err != nil {
		t.Fatalf("numeric UnmarshalJSON returned error: %v", err)
	}
	if value != "123.45" {
		t.Fatalf("unexpected numeric value: %q", value)
	}

	if err := json.Unmarshal([]byte(`"678.90"`), &value); err != nil {
		t.Fatalf("string UnmarshalJSON returned error: %v", err)
	}
	if value != "678.90" {
		t.Fatalf("unexpected string value: %q", value)
	}
}

func TestCandle_UnmarshalJSON(t *testing.T) {
	var candle Candle
	if err := json.Unmarshal([]byte(`[1,"2",3,4,5,6,7]`), &candle); err != nil {
		t.Fatalf("UnmarshalJSON returned error: %v", err)
	}

	if candle[0] != "1" || candle[1] != "2" || candle[4] != "5" {
		t.Fatalf("unexpected candle: %#v", candle)
	}
}

package hyperliquid

import (
	"encoding/json"
	"testing"
)

func TestWsAllDexsAssetCtxsAcceptsLiveTupleList(t *testing.T) {
	var event WsAllDexsAssetCtxs
	if err := json.Unmarshal([]byte(`{"ctxs":[["",[{"funding":"0.0001","openInterest":"2","markPx":"100","oraclePx":"99"}]],["xyz",[{"funding":"0.0002","openInterest":"3","markPx":"200","oraclePx":"199"}]]]}`), &event); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if len(event.Ctxs[""]) != 1 || event.Ctxs[""][0].MarkPx != "100" {
		t.Fatalf("primary contexts = %+v", event.Ctxs[""])
	}
	if len(event.Ctxs["xyz"]) != 1 || event.Ctxs["xyz"][0].Funding != "0.0002" {
		t.Fatalf("xyz contexts = %+v", event.Ctxs["xyz"])
	}
}

func TestWsAllDexsAssetCtxsRetainsRecordCompatibility(t *testing.T) {
	var event WsAllDexsAssetCtxs
	if err := json.Unmarshal([]byte(`{"ctxs":{"xyz":[{"funding":"0.0002","openInterest":"3","markPx":"200","oraclePx":"199"}]}}`), &event); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if len(event.Ctxs["xyz"]) != 1 || event.Ctxs["xyz"][0].MarkPx != "200" {
		t.Fatalf("xyz contexts = %+v", event.Ctxs["xyz"])
	}
}

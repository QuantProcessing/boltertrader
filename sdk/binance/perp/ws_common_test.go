package perp

import "testing"

func TestWSCommonCompanion_EventMaps(t *testing.T) {
	if SingleEventMap == nil || ArrayEventMap == nil {
		t.Fatal("expected websocket event maps to be initialized")
	}
}

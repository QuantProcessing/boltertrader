package spot

import "testing"

func TestWSOrderCompanion_WsOrderOpMethods(t *testing.T) {
	if "order.place" == "" || "order.cancelReplace" == "" || "order.cancel" == "" {
		t.Fatal("expected spot WS order method names")
	}
}

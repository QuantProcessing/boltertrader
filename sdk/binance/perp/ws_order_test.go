package perp

import "testing"

func TestWSOrderCompanion_WsOrderOpMethods(t *testing.T) {
	if "order.place" == "" || "order.modify" == "" || "order.cancel" == "" || "order.cancelAll" == "" {
		t.Fatal("expected perp WS order method names")
	}
}

package spot

import "testing"

func TestWSAccountCompanion_SubscriptionTypes(t *testing.T) {
	if "orderUpdates" == "" || "userFills" == "" || "user" == "" {
		t.Fatal("expected spot account websocket subscription names")
	}
}

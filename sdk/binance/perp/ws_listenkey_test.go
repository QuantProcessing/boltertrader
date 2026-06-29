package perp

import "testing"

func TestWSListenKeyCompanion_ResponseShape(t *testing.T) {
	res := ListenKeyResponse{ListenKey: "listen-key"}
	if res.ListenKey != "listen-key" {
		t.Fatalf("unexpected listen key response: %+v", res)
	}
}

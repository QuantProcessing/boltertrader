package lighter

import "testing"

func TestWSTypesCompanion_SubscribeRequestShape(t *testing.T) {
	auth := "token"
	req := SubscribeRequest{Type: "subscribe", Channel: "account_all/1", Auth: &auth}
	if req.Type != "subscribe" || req.Channel != "account_all/1" || req.Auth == nil || *req.Auth != "token" {
		t.Fatalf("unexpected subscribe request: %+v", req)
	}
}

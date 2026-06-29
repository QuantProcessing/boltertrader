package spot

import "testing"

func TestClient_GetBalance(t *testing.T) {
	balance, err := newLivePrivateClient(t).GetBalance()
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance == nil {
		t.Fatal("expected balance")
	}
}

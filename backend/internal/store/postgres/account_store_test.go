package postgres

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
)

func TestEncodeAccountJSONUsesSQLNullForNilRotation(t *testing.T) {
	_, rotation, err := encodeAccountJSON(accounts.Account{ProductIDs: []string{"funding"}})
	if err != nil {
		t.Fatal(err)
	}
	if rotation != nil {
		t.Fatalf("nil rotation encoded as JSON %q", rotation)
	}

	_, rotation, err = encodeAccountJSON(accounts.Account{ProductIDs: []string{"funding"}, Rotation: &accounts.RotationFence{ID: "rotation-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(rotation) == "" || string(rotation) == "null" {
		t.Fatalf("object rotation encoded as %q", rotation)
	}
}

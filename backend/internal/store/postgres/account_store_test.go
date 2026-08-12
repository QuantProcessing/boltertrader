package postgres

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
)

func TestEncodeAccountJSONUsesSQLNullForNilRotation(t *testing.T) {
	_, rotation, err := encodeAccountJSON(accounts.Account{ProductIDs: []string{"manual"}})
	if err != nil {
		t.Fatal(err)
	}
	if rotation != nil {
		t.Fatalf("nil rotation encoded as JSON %q", rotation)
	}

	_, rotation, err = encodeAccountJSON(accounts.Account{ProductIDs: []string{"manual"}, Rotation: &accounts.RotationFence{ID: "rotation-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(rotation) == "" || string(rotation) == "null" {
		t.Fatalf("object rotation encoded as %q", rotation)
	}
}

func TestOrderedRoutesIsDeterministicAndDoesNotMutateInput(t *testing.T) {
	routes := []accounts.AccountRoute{{ID: "z-route"}, {ID: "a-route"}}
	ordered := orderedRoutes(routes)
	if ordered[0].ID != "a-route" || ordered[1].ID != "z-route" {
		t.Fatalf("ordered=%+v", ordered)
	}
	if routes[0].ID != "z-route" {
		t.Fatalf("input mutated=%+v", routes)
	}
}

package account

import "testing"

func TestProfileRegistryRejectsModeAndRouteConflictsBeforeCandidateBuild(t *testing.T) {
	pending := runtimeGroupPending(t)
	registry := NewProfileRegistry(nil)
	if err := registry.Validate(RuntimeModeTest, pending.DesiredSnapshot); err != nil {
		t.Fatal(err)
	}
	live := pending.DesiredSnapshot
	live.Routes[0].RouteProfileID = "binance.spot.live.v1"
	if err := registry.Validate(RuntimeModeTest, live); err == nil {
		t.Fatal("test runtime accepted live route profile")
	}
}

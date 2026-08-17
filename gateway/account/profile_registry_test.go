package account

import "testing"

// TestProfileRegistryRejectsRouteConflictsBeforeCandidateBuild pins that a
// stored snapshot is re-proved against the registry before anything is built
// from it.
//
// The snapshot was written by an earlier binary, so a route naming a profile
// this one no longer carries has to be caught here. It used to be caught as a
// cross-mode conflict — a test runtime handed a `.live.` profile id — which is
// the same failure with a name that no longer exists.
func TestProfileRegistryRejectsRouteConflictsBeforeCandidateBuild(t *testing.T) {
	pending := runtimeGroupPending(t)
	registry := NewProfileRegistry(nil)
	if err := registry.Validate(pending.DesiredSnapshot); err != nil {
		t.Fatal(err)
	}
	unknown := pending.DesiredSnapshot
	unknown.Routes[0].RouteProfileID = "binance.spot.retired.v1"
	if err := registry.Validate(unknown); err == nil {
		t.Fatal("runtime accepted a route profile the registry does not carry")
	}
	// The environment travels with the snapshot rather than with the profile,
	// so it is the one field the registry cannot re-derive and must check.
	demo := pending.DesiredSnapshot
	demo.NativeEnvironment = "demo"
	if err := registry.Validate(demo); err == nil {
		t.Fatal("runtime accepted a snapshot naming endpoints that no longer exist")
	}
}

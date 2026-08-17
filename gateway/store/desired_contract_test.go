package store

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
)

func validDesiredAccount() DesiredAccount {
	return DesiredAccount{
		AccountID:            "account-a",
		Venue:                "venue-a",
		VenueEnvironment:     "live",
		SecretRef:            "opaque://account-a/1",
		DesiredStateRevision: 1,
		RuntimeGeneration:    1,
		CredentialVersion:    1,
		RequestedState:       DesiredCreating,
	}
}

func TestDesiredAccountPersistsOnlyOpaqueReferenceAndPositiveFences(t *testing.T) {
	desired := validDesiredAccount()
	if err := desired.Validate(); err != nil {
		t.Fatalf("valid desired account rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*DesiredAccount)
	}{
		{"secret reference", func(value *DesiredAccount) { value.SecretRef = "secret\nvalue" }},
		{"revision", func(value *DesiredAccount) { value.DesiredStateRevision = 0 }},
		{"runtime generation", func(value *DesiredAccount) { value.RuntimeGeneration = 0 }},
		{"credential version", func(value *DesiredAccount) { value.CredentialVersion = 0 }},
		{"environment", func(value *DesiredAccount) { value.VenueEnvironment = "production" }},
		{"state", func(value *DesiredAccount) { value.RequestedState = "enabled" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := desired
			test.mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatalf("invalid desired account accepted: %+v", invalid)
			}
		})
	}
}

func TestRotationFenceRequiresMonotonicCandidateGenerations(t *testing.T) {
	fence := RotationFence{
		ID:                    "rotation-a",
		State:                 RotationArmed,
		FromCredentialVersion: 1,
		ToCredentialVersion:   2,
		FromRuntimeGeneration: 1,
		ToRuntimeGeneration:   2,
		ArmedAt:               time.Unix(1710000000, 0).UTC(),
	}
	if err := fence.Validate(); err != nil {
		t.Fatalf("valid rotation fence rejected: %v", err)
	}
	fence.ToRuntimeGeneration = fence.FromRuntimeGeneration
	if err := fence.Validate(); err == nil {
		t.Fatal("non-increasing runtime generation accepted")
	}
}

func TestDesiredAccountLifecycleRejectsUnsafeShortcuts(t *testing.T) {
	legal := [][2]DesiredAccountState{
		{DesiredCreating, DesiredActive}, {DesiredActive, DesiredRotating},
		{DesiredRotating, DesiredActive}, {DesiredActive, DesiredDraining},
		{DesiredDraining, DesiredDrained}, {DesiredActive, DesiredDisabled},
		{DesiredDrained, DesiredRemoving}, {DesiredDisabled, DesiredRemoving},
		{DesiredRemoving, DesiredRemoved},
	}
	for _, transition := range legal {
		if err := ValidateDesiredAccountTransition(transition[0], transition[1]); err != nil {
			t.Errorf("legal transition %s -> %s rejected: %v", transition[0], transition[1], err)
		}
	}
	for _, transition := range [][2]DesiredAccountState{
		{DesiredCreating, DesiredRemoved}, {DesiredActive, DesiredRemoved},
		{DesiredRotating, DesiredDraining}, {DesiredDisabled, DesiredActive},
	} {
		if err := ValidateDesiredAccountTransition(transition[0], transition[1]); !errors.Is(err, ErrInvalidAccountTransition) {
			t.Errorf("unsafe transition %s -> %s error = %v", transition[0], transition[1], err)
		}
	}
}

func TestDesiredSnapshotDigestIsRouteOrderIndependentAndRejectsDuplicateRoutes(t *testing.T) {
	op, err := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := secretstore.ParseOpaqueRef("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := DesiredSnapshot{
		SnapshotID:        uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c34"),
		OperationID:       op,
		AccountID:         "account-a",
		Venue:             "binance",
		NativeEnvironment: "live",
		SecretRef:         ref,
		Fence:             domain.AccountFence{AccountID: "account-a", DesiredStateRevision: 2, RuntimeGeneration: 2, CredentialVersion: 1},
		RequestedState:    DesiredActive,
		Routes: []RouteDefinition{
			{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteActive},
			{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: RouteActive},
		},
	}
	digest, err := snapshot.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	reversed := snapshot
	reversed.Routes = []RouteDefinition{snapshot.Routes[1], snapshot.Routes[0]}
	reversedDigest, err := reversed.CanonicalDigest()
	if err != nil || reversedDigest != digest {
		t.Fatalf("reversed digest=%q want=%q err=%v", reversedDigest, digest, err)
	}
	duplicate := snapshot
	duplicate.Routes = []RouteDefinition{snapshot.Routes[0], snapshot.Routes[0]}
	if _, err := duplicate.CanonicalDigest(); err == nil {
		t.Fatal("duplicate route accepted")
	}
}

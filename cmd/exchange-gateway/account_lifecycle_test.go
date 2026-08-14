package main

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

type recordingRuntimeLifecycle struct {
	*authorityLifecycle
	seen     []domain.AccountID
	blockers account.RuntimeBlockers
	err      error
}

func (lifecycle *recordingRuntimeLifecycle) Blockers(_ context.Context, accountID domain.AccountID) (account.RuntimeBlockers, error) {
	lifecycle.seen = append(lifecycle.seen, accountID)
	return lifecycle.blockers, lifecycle.err
}

func TestAccountLifecycleIsOnlyAssembledWhenItsDependenciesExist(t *testing.T) {
	runtime := &recordingRuntimeLifecycle{authorityLifecycle: newAuthorityLifecycle()}
	for name, deps := range map[string]accountLifecycleDeps{
		"no credential provider": {runtime: runtime},
		"no runtime lifecycle":   {credentials: &secretstore.FilesystemProvider{}},
		"neither":                {},
	} {
		t.Run(name, func(t *testing.T) {
			if deps.available() {
				t.Fatal("expected the lifecycle to stay unmounted without its dependencies")
			}
		})
	}
	full := accountLifecycleDeps{credentials: &secretstore.FilesystemProvider{}, runtime: runtime}
	if !full.available() {
		t.Fatal("expected the lifecycle to be assembled once both dependencies exist")
	}
}

func TestAccountBlockersCarryTheRuntimeInventory(t *testing.T) {
	runtime := &recordingRuntimeLifecycle{
		authorityLifecycle: newAuthorityLifecycle(),
		blockers:           account.RuntimeBlockers{OpenOrders: 3, Positions: 2, UncertainCommands: 1},
	}
	deps := accountLifecycleDeps{credentials: &secretstore.FilesystemProvider{}, runtime: runtime}

	blockers, err := deps.blockers()(context.Background(), "acct_1")
	if err != nil {
		t.Fatal(err)
	}
	if blockers.OpenOrders != 3 || blockers.OwnedPositions != 2 || blockers.UncertainCommands != 1 {
		t.Fatalf("runtime inventory was not carried across: %+v", blockers)
	}
	// Reservations belong to trading-svc, which does not exist yet. Reporting a
	// count the gateway cannot see would let a drain proceed over held funds.
	if blockers.Reservations != 0 {
		t.Fatalf("expected reservations to stay uncounted, got %d", blockers.Reservations)
	}
	if len(runtime.seen) != 1 || runtime.seen[0] != domain.AccountID("acct_1") {
		t.Fatalf("expected the account id to reach the runtime once, got %v", runtime.seen)
	}
}

func TestAccountBlockersRefuseAnIdentifierTheRuntimeCannotUse(t *testing.T) {
	runtime := &recordingRuntimeLifecycle{authorityLifecycle: newAuthorityLifecycle()}
	deps := accountLifecycleDeps{credentials: &secretstore.FilesystemProvider{}, runtime: runtime}

	for _, accountID := range []string{"", " ", "acct_1\n", "acct\t1"} {
		if _, err := deps.blockers()(context.Background(), accountID); err == nil {
			t.Fatalf("expected %q to be refused before it reached the runtime", accountID)
		}
	}
	if len(runtime.seen) != 0 {
		t.Fatalf("expected no runtime lookups, got %v", runtime.seen)
	}
}

func TestAccountBlockersSurfaceAnUncountableRuntime(t *testing.T) {
	// productionRuntimeLifecycle answers with an error until the restore attaches
	// the pool. Reporting zero there would read as "nothing is holding this
	// account open" and let a removal delete a live credential.
	runtime := &recordingRuntimeLifecycle{
		authorityLifecycle: newAuthorityLifecycle(),
		blockers:           account.RuntimeBlockers{OpenOrders: 9},
		err:                errors.New("runtime blocker lookup failed"),
	}
	deps := accountLifecycleDeps{credentials: &secretstore.FilesystemProvider{}, runtime: runtime}

	blockers, err := deps.blockers()(context.Background(), "acct_1")
	if err == nil {
		t.Fatal("expected the runtime failure to surface")
	}
	if blockers.Any() {
		t.Fatalf("expected an empty count alongside the failure, got %+v", blockers)
	}
}

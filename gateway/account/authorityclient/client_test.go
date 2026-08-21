package authorityclient_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/account/authorityclient"
	"github.com/QuantProcessing/boltertrader/gateway/account/lifecycle"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

const (
	operationID       secretstore.OperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
	accountID                                 = "acct_1"
	activeRuntimeID                           = "018f47a8-2578-7d3c-9d7d-5e4f4a445c34"
	retiringRuntimeID                         = "018f47a8-2578-7d3c-9d7d-5e4f4a445c35"
)

var secretRef = strings.Repeat("A", 43)

type recordingAuthority struct {
	requests []httpapi.AuthorityRequest
	response httpapi.AuthorityResponse
	err      error
}

func (authority *recordingAuthority) Execute(_ context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	authority.requests = append(authority.requests, request)
	return authority.response, authority.err
}

func (authority *recordingAuthority) last() httpapi.AuthorityRequest {
	return authority.requests[len(authority.requests)-1]
}

func newClient(t *testing.T, authority httpapi.InternalAuthority) *authorityclient.Client {
	t.Helper()
	client, err := authorityclient.New(authority)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func validAccount() lifecycle.Account {
	return lifecycle.Account{
		ID: accountID, RuntimeID: activeRuntimeID, Venue: "binance",
		VenueEnvironment:  lifecycle.EnvironmentLive,
		NativeEnvironment: lifecycle.EnvironmentLive, DisplayName: "Primary",
		ProductPolicy: []string{"manual"}, ProductIDs: []string{"manual"},
		Routes: []lifecycle.AccountRoute{{
			ID: "perp-usdt", Product: lifecycle.ProductPerp, SettlementAsset: "USDT",
			RouteProfileID: "binance.perp.usdt.v1", RequestedState: lifecycle.RouteActive,
		}},
		SecretRef: secretRef,
		State:     lifecycle.StateCreating, UpdatedAt: time.Unix(1, 0).UTC(),
	}
}

func activeView(mutate func(*httpapi.RuntimeSnapshotView)) httpapi.RuntimeView {
	snapshot := httpapi.RuntimeSnapshotView{
		OperationID: string(operationID), RuntimeID: activeRuntimeID, RequestedState: "active",
		Routes: []httpapi.RuntimeRouteView{{
			RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT",
			RouteProfileID: "binance.perp.usdt.v1", RequestedState: "active", Ready: true,
		}},
	}
	if mutate != nil {
		mutate(&snapshot)
	}
	return httpapi.RuntimeView{AccountID: accountID, Active: &snapshot}
}

func TestApplyDesiredCarriesTheWholeDesiredSnapshotAndReducesTheRuntimeView(t *testing.T) {
	authority := &recordingAuthority{response: httpapi.AuthorityResponse{Body: activeView(nil), Status: 200}}
	client := newClient(t, authority)

	truth, err := client.ApplyDesired(t.Context(), lifecycle.DesiredApply{OperationID: operationID, Account: validAccount()})
	if err != nil {
		t.Fatalf("ApplyDesired: %v", err)
	}

	request := authority.last()
	if request.Operation != httpapi.AuthorityApplyDesired ||
		request.AccountID != accountID || request.OperationID != string(operationID) {
		t.Fatalf("request envelope = %+v", request)
	}
	if request.DesiredApply == nil {
		t.Fatal("desired apply payload is missing")
	}
	desired := *request.DesiredApply
	if desired.AccountID != accountID || desired.OperationID != string(operationID) || desired.Venue != "binance" ||
		desired.NativeEnvironment != "live" || desired.SecretRef != secretRef || desired.RequestedState != "creating" ||
		desired.RuntimeID != activeRuntimeID {
		t.Fatalf("desired snapshot = %+v", desired)
	}
	if len(desired.Routes) != 1 || desired.Routes[0] != (httpapi.DesiredRoute{
		RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT",
		RouteProfileID: "binance.perp.usdt.v1", RequestedState: "active",
	}) {
		t.Fatalf("desired routes = %+v", desired.Routes)
	}

	if truth.AccountID != accountID || truth.OperationID != operationID || truth.RuntimeID != activeRuntimeID ||
		!truth.Active || truth.Tombstoned || truth.RequestedState != lifecycle.StateActive {
		t.Fatalf("truth = %+v", truth)
	}
	if len(truth.Routes) != 1 || truth.Routes[0] != (lifecycle.GatewayRuntimeRoute{
		RouteID: "perp-usdt", Product: lifecycle.ProductPerp, SettlementAsset: "USDT",
		RouteProfileID: "binance.perp.usdt.v1", RequestedState: lifecycle.RouteActive, Ready: true,
	}) {
		t.Fatalf("truth routes = %+v", truth.Routes)
	}
}

// Active means the immutable snapshot was promoted. Per-route Ready remains a
// separate signal used by admission and the settings page; a private stream
// warming up must not undo successful credential verification.
func TestRuntimeTruthKeepsRouteReadinessSeparateFromActiveSnapshot(t *testing.T) {
	tests := map[string]struct {
		mutate func(*httpapi.RuntimeSnapshotView)
	}{
		"all routes ready": {mutate: nil},
		"disabled route not up": {mutate: func(snapshot *httpapi.RuntimeSnapshotView) {
			snapshot.Routes[0].RequestedState = "disabled"
			snapshot.Routes[0].Ready = false
		}},
		"active route not up": {mutate: func(snapshot *httpapi.RuntimeSnapshotView) {
			snapshot.Routes[0].Ready = false
		}},
		"no routes at all": {mutate: func(snapshot *httpapi.RuntimeSnapshotView) {
			snapshot.Routes = nil
		}},
		"unnamed route": {mutate: func(snapshot *httpapi.RuntimeSnapshotView) {
			snapshot.Routes[0].RouteID = ""
		}},
		"unprofiled route": {mutate: func(snapshot *httpapi.RuntimeSnapshotView) {
			snapshot.Routes[0].RouteProfileID = ""
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			authority := &recordingAuthority{response: httpapi.AuthorityResponse{Body: activeView(test.mutate), Status: 200}}
			truth, err := newClient(t, authority).GetRuntimeTruth(t.Context(), accountID)
			if err != nil {
				t.Fatalf("GetRuntimeTruth: %v", err)
			}
			if !truth.Active {
				t.Fatal("promoted snapshot was hidden by route readiness")
			}
		})
	}
}

func TestRuntimeTruthRejectsViewsTheLifecycleCannotActOn(t *testing.T) {
	tests := map[string]httpapi.RuntimeView{
		"another account's view": {AccountID: "acct_2", Active: &httpapi.RuntimeSnapshotView{}},
		// A tombstone that still carries live snapshots means the authority and
		// its store disagree; retiring the credential on that word would strand a
		// runtime that is still trading.
		"tombstoned yet still active": func() httpapi.RuntimeView {
			view := activeView(nil)
			view.Tombstoned = true
			return view
		}(),
		"tombstoned yet still retiring": {AccountID: accountID, Tombstoned: true, Retiring: []httpapi.RuntimeSnapshotView{{}}},
		"unparseable operation": activeView(func(snapshot *httpapi.RuntimeSnapshotView) {
			snapshot.OperationID = "not-a-uuid"
		}),
		"missing runtime identity": activeView(func(snapshot *httpapi.RuntimeSnapshotView) {
			snapshot.RuntimeID = ""
		}),
		"unknown requested state": activeView(func(snapshot *httpapi.RuntimeSnapshotView) {
			snapshot.RequestedState = "resurrecting"
		}),
	}
	for name, view := range tests {
		t.Run(name, func(t *testing.T) {
			authority := &recordingAuthority{response: httpapi.AuthorityResponse{Body: view, Status: 200}}
			if truth, err := newClient(t, authority).GetRuntimeTruth(t.Context(), accountID); err == nil {
				t.Fatalf("GetRuntimeTruth accepted %+v", truth)
			}
		})
	}
}

func TestRuntimeTruthAcceptsATombstonedAccountWithNoSnapshots(t *testing.T) {
	authority := &recordingAuthority{response: httpapi.AuthorityResponse{
		Body: httpapi.RuntimeView{AccountID: accountID, Tombstoned: true}, Status: 200,
	}}
	truth, err := newClient(t, authority).GetRuntimeTruth(t.Context(), accountID)
	if err != nil {
		t.Fatalf("GetRuntimeTruth: %v", err)
	}
	if !truth.Tombstoned || truth.Active || truth.OperationID != "" || len(truth.Routes) != 0 {
		t.Fatalf("truth = %+v", truth)
	}
}

// The authority holds one lock for the whole call, so a request it would only
// reject is a request that stalled every other account for nothing. Each of
// these is decidable here.
func TestClientRejectsRequestsTheAuthorityMustNeverSee(t *testing.T) {
	tests := map[string]func(*authorityclient.Client) error{
		"unparseable operation": func(client *authorityclient.Client) error {
			_, err := client.ApplyDesired(t.Context(), lifecycle.DesiredApply{OperationID: "operation-1", Account: validAccount()})
			return err
		},
		"account that does not validate": func(client *authorityclient.Client) error {
			account := validAccount()
			account.Routes[0].RouteProfileID = "binance.perp.usdt.live.v1"
			_, err := client.ApplyDesired(t.Context(), lifecycle.DesiredApply{OperationID: operationID, Account: account})
			return err
		},
		"unknown requested state": func(client *authorityclient.Client) error {
			account := validAccount()
			account.State = "resurrecting"
			_, err := client.ApplyDesired(t.Context(), lifecycle.DesiredApply{OperationID: operationID, Account: account})
			return err
		},
		"malformed secret reference": func(client *authorityclient.Client) error {
			account := validAccount()
			account.SecretRef = "opaque-ref"
			_, err := client.ApplyDesired(t.Context(), lifecycle.DesiredApply{OperationID: operationID, Account: account})
			return err
		},
		"more routes than the authority bounds": func(client *authorityclient.Client) error {
			account := validAccount()
			for len(account.Routes) < 33 {
				route := account.Routes[0]
				route.ID = "perp-usdt-" + strings.Repeat("x", len(account.Routes))
				account.Routes = append(account.Routes, route)
			}
			_, err := client.ApplyDesired(t.Context(), lifecycle.DesiredApply{OperationID: operationID, Account: account})
			return err
		},
		"unnamed account": func(client *authorityclient.Client) error {
			_, err := client.GetRuntimeTruth(t.Context(), "  ")
			return err
		},
		"retirement without a fence": func(client *authorityclient.Client) error {
			_, err := client.RetireAccount(t.Context(), lifecycle.RetireRequest{
				OperationID: operationID, AccountID: accountID,
			})
			return err
		},
		"rotation cleanup with mismatched fences": func(client *authorityclient.Client) error {
			_, err := client.FinalizeRotation(t.Context(), lifecycle.RotationCleanupRequest{
				OperationID:           operationID,
				AccountID:             accountID,
				ExpectedActiveFence:   lifecycle.AccountFence{AccountID: accountID, RuntimeID: activeRuntimeID},
				ExpectedRetiringFence: lifecycle.AccountFence{AccountID: accountID, RuntimeID: activeRuntimeID},
			})
			return err
		},
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			authority := &recordingAuthority{response: httpapi.AuthorityResponse{Body: activeView(nil), Status: 200}}
			if err := call(newClient(t, authority)); err == nil {
				t.Fatal("client accepted a request the authority must never see")
			}
			if len(authority.requests) != 0 {
				t.Fatalf("authority saw %d requests", len(authority.requests))
			}
		})
	}
}

// The lifecycle retries a failed write only when it knows the write did not
// land. The authority applies the snapshot before it reads the view back, so
// an internal failure is indistinguishable from a committed one and has to be
// reported as such; a deterministic rejection did not commit.
func TestWriteFailuresReportWhetherTheResultMayHaveCommitted(t *testing.T) {
	tests := map[string]struct {
		response         httpapi.AuthorityResponse
		err              error
		code             string
		mayHaveCommitted bool
	}{
		"stale fence": {
			err:  httpapi.AuthorityError{Status: 412, Code: "DESIRED_STATE_REVISION_CONFLICT", Retryable: true},
			code: "DESIRED_STATE_REVISION_CONFLICT",
		},
		"forbidden live write": {
			err:  httpapi.AuthorityError{Status: 403, Code: "LIVE_PRIVATE_WRITE_DISABLED"},
			code: "LIVE_PRIVATE_WRITE_DISABLED",
		},
		"authority internal failure": {
			err:              httpapi.AuthorityError{Status: 500, Code: "INTERNAL_ERROR", Retryable: true},
			code:             "INTERNAL_ERROR",
			mayHaveCommitted: true,
		},
		"failure the authority did not classify": {
			err:              errors.New("authority panicked"),
			code:             "GATEWAY_AUTHORITY_UNAVAILABLE",
			mayHaveCommitted: true,
		},
		"body that is not a runtime view": {
			response:         httpapi.AuthorityResponse{Body: httpapi.RotationFinalizationProof{}, Status: 200},
			code:             "GATEWAY_AUTHORITY_RESPONSE_INVALID",
			mayHaveCommitted: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			authority := &recordingAuthority{response: test.response, err: test.err}
			_, err := newClient(t, authority).ApplyDesired(t.Context(), lifecycle.DesiredApply{OperationID: operationID, Account: validAccount()})
			var clientErr authorityclient.Error
			if !errors.As(err, &clientErr) {
				t.Fatalf("ApplyDesired error = %v", err)
			}
			if clientErr.Code != test.code {
				t.Fatalf("code = %s, want %s", clientErr.Code, test.code)
			}
			var result lifecycle.ApplyResultError = clientErr
			if result.ResultMayHaveCommitted() != test.mayHaveCommitted {
				t.Fatalf("ResultMayHaveCommitted = %v, want %v", result.ResultMayHaveCommitted(), test.mayHaveCommitted)
			}
			if clientErr.AccountID != accountID || clientErr.OperationID != string(operationID) {
				t.Fatalf("error identity = %+v", clientErr)
			}
		})
	}

	// A read cannot have changed anything, so it must not push the lifecycle
	// onto the reconcile path a possibly-committed write would.
	authority := &recordingAuthority{err: httpapi.AuthorityError{Status: 500, Code: "INTERNAL_ERROR", Retryable: true}}
	_, err := newClient(t, authority).GetRuntimeTruth(t.Context(), accountID)
	var readErr authorityclient.Error
	if !errors.As(err, &readErr) || readErr.ResultMayHaveCommitted() {
		t.Fatalf("GetRuntimeTruth error = %+v", err)
	}
}

func TestRetireAccountConfirmsTheTombstoneOnlyWhenNothingIsStillActive(t *testing.T) {
	retire := lifecycle.RetireRequest{
		OperationID: operationID, AccountID: accountID,
		RuntimeID: activeRuntimeID,
	}
	tests := map[string]struct {
		view       httpapi.RuntimeView
		tombstoned bool
	}{
		"tombstoned with nothing left": {
			view:       httpapi.RuntimeView{AccountID: accountID, Tombstoned: true},
			tombstoned: true,
		},
		// The authority answers with the account's current view, which during a
		// blocked retirement is still the live one. Reporting that as retired
		// would let the lifecycle delete a credential the runtime still holds.
		"still serving an active snapshot": {view: activeView(nil)},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			authority := &recordingAuthority{response: httpapi.AuthorityResponse{Body: test.view, Status: 200}}
			retirement, err := newClient(t, authority).RetireAccount(t.Context(), retire)
			if err != nil {
				t.Fatalf("RetireAccount: %v", err)
			}
			if retirement.Tombstoned != test.tombstoned {
				t.Fatalf("Tombstoned = %v, want %v", retirement.Tombstoned, test.tombstoned)
			}
			request := authority.last()
			if request.Operation != httpapi.AuthorityRetireDesired || request.Retirement == nil ||
				*request.Retirement != (httpapi.RetireRequest{
					OperationID: string(operationID), AccountID: accountID,
					RuntimeID: activeRuntimeID,
				}) {
				t.Fatalf("retirement request = %+v", request)
			}
		})
	}
}

func TestFinalizeRotationOnlyReturnsAProofThatAnswersTheRequest(t *testing.T) {
	cleanup := lifecycle.RotationCleanupRequest{
		OperationID:           operationID,
		AccountID:             accountID,
		ExpectedActiveFence:   lifecycle.AccountFence{AccountID: accountID, RuntimeID: activeRuntimeID},
		ExpectedRetiringFence: lifecycle.AccountFence{AccountID: accountID, RuntimeID: retiringRuntimeID},
	}
	completedAt := time.Unix(1710000000, 0).UTC()
	valid := httpapi.RotationFinalizationProof{
		OperationID:          string(operationID),
		AccountID:            accountID,
		ActiveFence:          httpapi.AccountFence{AccountID: accountID, RuntimeID: activeRuntimeID},
		RetiringFence:        httpapi.AccountFence{AccountID: accountID, RuntimeID: retiringRuntimeID},
		TombstoneRevision:    3,
		RetiringState:        "retired",
		SecretReferenceState: "tombstoned",
		CompletedAt:          completedAt,
	}

	authority := &recordingAuthority{response: httpapi.AuthorityResponse{Body: valid, Status: 200}}
	proof, err := newClient(t, authority).FinalizeRotation(t.Context(), cleanup)
	if err != nil {
		t.Fatalf("FinalizeRotation: %v", err)
	}
	if proof.OperationID != operationID || proof.AccountID != accountID ||
		proof.ActiveFence != cleanup.ExpectedActiveFence || proof.RetiringFence != cleanup.ExpectedRetiringFence ||
		proof.RetiringState != "retired" || proof.SecretReferenceState != "tombstoned" || !proof.CompletedAt.Equal(completedAt) {
		t.Fatalf("proof = %+v", proof)
	}
	request := authority.last()
	if request.Operation != httpapi.AuthorityFinalizeRotation || request.RotationFinalization == nil ||
		request.RotationFinalization.ExpectedActiveFence.RuntimeID != activeRuntimeID ||
		request.RotationFinalization.ExpectedRetiringFence.RuntimeID != retiringRuntimeID {
		t.Fatalf("finalization request = %+v", request)
	}

	// This proof is what the lifecycle stores before it deletes the previous
	// credential, so one that answers a different rotation must not be
	// persisted at all.
	mutations := map[string]func(httpapi.RotationFinalizationProof) httpapi.RotationFinalizationProof{
		"another rotation's fence": func(proof httpapi.RotationFinalizationProof) httpapi.RotationFinalizationProof {
			proof.ActiveFence.RuntimeID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c36"
			return proof
		},
		"credential not yet retired": func(proof httpapi.RotationFinalizationProof) httpapi.RotationFinalizationProof {
			proof.SecretReferenceState = "active"
			return proof
		},
		"unfinished retirement": func(proof httpapi.RotationFinalizationProof) httpapi.RotationFinalizationProof {
			proof.RetiringState = "draining"
			return proof
		},
		"no completion time": func(proof httpapi.RotationFinalizationProof) httpapi.RotationFinalizationProof {
			proof.CompletedAt = time.Time{}
			return proof
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			authority := &recordingAuthority{response: httpapi.AuthorityResponse{Body: mutate(valid), Status: 200}}
			proof, err := newClient(t, authority).FinalizeRotation(t.Context(), cleanup)
			var clientErr authorityclient.Error
			if !errors.As(err, &clientErr) || clientErr.Code != "GATEWAY_AUTHORITY_RESPONSE_INVALID" || !clientErr.ResultMayHaveCommitted() {
				t.Fatalf("FinalizeRotation = %+v / %v", proof, err)
			}
		})
	}
}

// New used to refuse an unrecognised mode as well: the mode was the only thing
// keeping a test-mode client from addressing live accounts, so it could not be
// allowed to default. There is one kind of account left, so the authority is
// all that remains to check.
func TestNewRefusesAnUnusableAuthority(t *testing.T) {
	if _, err := authorityclient.New(nil); err == nil {
		t.Fatal("New accepted a nil authority")
	}
}

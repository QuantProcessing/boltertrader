// Package authorityclient connects the account lifecycle to the gateway
// authority in-process. The lifecycle used to reach the authority over HTTP
// from a separate binary; now that both live in the gateway, the wire hop only
// added a serialization boundary that could fail in ways neither side could
// observe.
package authorityclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/gateway/account/lifecycle"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

// maxRoutes bounds a single desired snapshot. The authority resolves a route
// profile per route while holding its lock, so an unbounded list would stall
// every other account.
const maxRoutes = 32

// Client implements lifecycle.GatewayDesiredClient against the authority.
type Client struct {
	authority httpapi.InternalAuthority
}

var _ lifecycle.GatewayDesiredClient = (*Client)(nil)

func New(authority httpapi.InternalAuthority) (*Client, error) {
	if authority == nil {
		return nil, errors.New("gateway authority is required")
	}
	return &Client{authority: authority}, nil
}

// Error is the lifecycle-facing view of an authority rejection. It carries a
// stable code rather than the authority's message because the lifecycle
// records it against an account, and it answers ResultMayHaveCommitted so a
// caller that cannot retry safely knows to reconcile instead.
type Error struct {
	Code             string
	Retryable        bool
	MayHaveCommitted bool
	AccountID        string
	OperationID      string
}

var _ lifecycle.ApplyResultError = Error{}
var _ lifecycle.ApplyFailureCode = Error{}

func (err Error) ResultMayHaveCommitted() bool { return err.MayHaveCommitted }
func (err Error) StableFailureCode() string    { return err.Code }

func (err Error) Error() string {
	parts := []string{"gateway authority rejected request", "code=" + err.Code, "account=" + err.AccountID}
	if err.OperationID != "" {
		parts = append(parts, "operation="+err.OperationID)
	}
	return strings.Join(parts, " ")
}

func (client *Client) ApplyDesired(ctx context.Context, apply lifecycle.DesiredApply) (lifecycle.GatewayRuntimeTruth, error) {
	operationID, err := secretstore.ParseOperationID(string(apply.OperationID))
	if err != nil || apply.Account.Validate() != nil ||
		!validRequestedState(string(apply.Account.State)) || len(apply.Account.Routes) > maxRoutes {
		return lifecycle.GatewayRuntimeTruth{}, errors.New("gateway desired request is invalid")
	}
	ref, err := secretstore.ParseOpaqueRef(apply.Account.SecretRef)
	if err != nil {
		return lifecycle.GatewayRuntimeTruth{}, errors.New("gateway desired request is invalid")
	}
	routes := make([]httpapi.DesiredRoute, 0, len(apply.Account.Routes))
	for _, route := range apply.Account.Routes {
		routes = append(routes, httpapi.DesiredRoute{
			RouteID: route.ID, Product: string(route.Product), SettlementAsset: route.SettlementAsset,
			RouteProfileID: route.RouteProfileID, RequestedState: string(route.RequestedState),
		})
	}
	desired := httpapi.DesiredApplyRequest{
		OperationID: string(operationID), AccountID: apply.Account.ID,
		RuntimeID: apply.Account.RuntimeID,
		Venue:     apply.Account.Venue, NativeEnvironment: string(apply.Account.NativeEnvironment),
		SecretRef:      ref.RawForTrustedBoundary(),
		RequestedState: string(apply.Account.State), Routes: routes,
	}
	view, err := client.runtimeView(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired,
		AccountID: apply.Account.ID, OperationID: desired.OperationID, DesiredApply: &desired,
	}, true)
	if err != nil {
		return lifecycle.GatewayRuntimeTruth{}, err
	}
	return runtimeTruth(view, apply.Account.ID)
}

func (client *Client) GetRuntimeTruth(ctx context.Context, accountID string) (lifecycle.GatewayRuntimeTruth, error) {
	if strings.TrimSpace(accountID) == "" || accountID != strings.TrimSpace(accountID) {
		return lifecycle.GatewayRuntimeTruth{}, errors.New("gateway runtime truth request is invalid")
	}
	view, err := client.runtimeView(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityGetRuntimeAccount, AccountID: accountID,
	}, false)
	if err != nil {
		return lifecycle.GatewayRuntimeTruth{}, err
	}
	return runtimeTruth(view, accountID)
}

func (client *Client) RetireAccount(ctx context.Context, retire lifecycle.RetireRequest) (lifecycle.GatewayRetirement, error) {
	operationID, err := secretstore.ParseOperationID(string(retire.OperationID))
	if err != nil || strings.TrimSpace(retire.AccountID) == "" || retire.AccountID != strings.TrimSpace(retire.AccountID) ||
		strings.TrimSpace(retire.RuntimeID) == "" {
		return lifecycle.GatewayRetirement{}, errors.New("gateway retirement request is invalid")
	}
	retirement := httpapi.RetireRequest{
		OperationID: string(operationID), AccountID: retire.AccountID,
		RuntimeID: retire.RuntimeID,
	}
	view, err := client.runtimeView(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityRetireDesired,
		AccountID: retire.AccountID, OperationID: retirement.OperationID, Retirement: &retirement,
	}, true)
	if err != nil {
		return lifecycle.GatewayRetirement{}, err
	}
	truth, err := runtimeTruth(view, retire.AccountID)
	if err != nil {
		return lifecycle.GatewayRetirement{}, err
	}
	return lifecycle.GatewayRetirement{Tombstoned: truth.Tombstoned && !truth.Active}, nil
}

func (client *Client) FinalizeRotation(ctx context.Context, cleanup lifecycle.RotationCleanupRequest) (lifecycle.RotationCleanupProof, error) {
	if cleanup.Validate() != nil {
		return lifecycle.RotationCleanupProof{}, errors.New("gateway rotation cleanup request is invalid")
	}
	finalization := httpapi.RotationFinalizationRequest{
		OperationID: string(cleanup.OperationID), AccountID: cleanup.AccountID,
		ExpectedActiveFence:   authorityFence(cleanup.ExpectedActiveFence),
		ExpectedRetiringFence: authorityFence(cleanup.ExpectedRetiringFence),
	}
	response, err := client.execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityFinalizeRotation,
		AccountID: cleanup.AccountID, OperationID: finalization.OperationID, RotationFinalization: &finalization,
	}, true)
	if err != nil {
		return lifecycle.RotationCleanupProof{}, err
	}
	body, ok := response.Body.(httpapi.RotationFinalizationProof)
	if !ok {
		return lifecycle.RotationCleanupProof{}, responseError(cleanup.AccountID, string(cleanup.OperationID))
	}
	proof := lifecycle.RotationCleanupProof{
		OperationID: secretstore.OperationID(body.OperationID), AccountID: body.AccountID,
		ActiveFence:          lifecycleFence(body.ActiveFence),
		RetiringFence:        lifecycleFence(body.RetiringFence),
		TombstoneRevision:    body.TombstoneRevision,
		RetiringState:        body.RetiringState,
		SecretReferenceState: body.SecretReferenceState,
		CompletedAt:          body.CompletedAt.UTC(),
	}
	// The proof is what retires the previous credential, so a proof that does
	// not match what was asked for must not be persisted: the lifecycle would
	// delete material the runtime is still using.
	if proof.ValidateFor(cleanup) != nil {
		return lifecycle.RotationCleanupProof{}, responseError(cleanup.AccountID, string(cleanup.OperationID))
	}
	return proof, nil
}

func (client *Client) runtimeView(ctx context.Context, request httpapi.AuthorityRequest, write bool) (httpapi.RuntimeView, error) {
	response, err := client.execute(ctx, request, write)
	if err != nil {
		return httpapi.RuntimeView{}, err
	}
	view, ok := response.Body.(httpapi.RuntimeView)
	if !ok {
		return httpapi.RuntimeView{}, responseError(request.AccountID, request.OperationID)
	}
	return view, nil
}

// execute maps an authority failure onto the lifecycle's retry vocabulary. A
// write that failed for an internal reason may already have been committed —
// the authority applies the snapshot before it reads the view back — so those
// are reported as such rather than as clean rejections.
func (client *Client) execute(ctx context.Context, request httpapi.AuthorityRequest, write bool) (httpapi.AuthorityResponse, error) {
	response, err := client.authority.Execute(ctx, request)
	if err == nil {
		return response, nil
	}
	var authorityErr httpapi.AuthorityError
	if !errors.As(err, &authorityErr) {
		return httpapi.AuthorityResponse{}, Error{
			Code: "GATEWAY_AUTHORITY_UNAVAILABLE", Retryable: true, MayHaveCommitted: write,
			AccountID: request.AccountID, OperationID: request.OperationID,
		}
	}
	return httpapi.AuthorityResponse{}, Error{
		Code: authorityErr.Code, Retryable: authorityErr.Retryable,
		MayHaveCommitted: write && authorityErr.Status >= 500,
		AccountID:        request.AccountID, OperationID: request.OperationID,
	}
}

func responseError(accountID, operationID string) error {
	return Error{
		Code: "GATEWAY_AUTHORITY_RESPONSE_INVALID", Retryable: true, MayHaveCommitted: true,
		AccountID: accountID, OperationID: operationID,
	}
}

func authorityFence(fence lifecycle.AccountFence) httpapi.AccountFence {
	return httpapi.AccountFence{
		AccountID: fence.AccountID, RuntimeID: fence.RuntimeID,
	}
}

func lifecycleFence(fence httpapi.AccountFence) lifecycle.AccountFence {
	return lifecycle.AccountFence{
		AccountID: fence.AccountID, RuntimeID: fence.RuntimeID,
	}
}

func validRequestedState(state string) bool {
	switch lifecycle.State(state) {
	case lifecycle.StateCreating, lifecycle.StateActive, lifecycle.StateRotating, lifecycle.StateDraining,
		lifecycle.StateDrained, lifecycle.StateDisabled, lifecycle.StateRemoving, lifecycle.StateRemoved:
		return true
	default:
		return false
	}
}

// runtimeTruth reduces the authority's view to the active account fence while
// preserving per-route readiness as separate operational state. An applied
// snapshot proves that the credential passed its private balance check; private
// streams and write permissions may still be warming and must not keep the
// account record in "creating" indefinitely.
func runtimeTruth(view httpapi.RuntimeView, accountID string) (lifecycle.GatewayRuntimeTruth, error) {
	if view.AccountID != accountID {
		return lifecycle.GatewayRuntimeTruth{}, fmt.Errorf("gateway runtime truth identity is invalid")
	}
	if view.Tombstoned && (view.Active != nil || view.Pending != nil || len(view.Retiring) != 0) {
		return lifecycle.GatewayRuntimeTruth{}, fmt.Errorf("gateway runtime truth is contradictory")
	}
	truth := lifecycle.GatewayRuntimeTruth{AccountID: accountID, Tombstoned: view.Tombstoned}
	if view.Active == nil {
		return truth, nil
	}
	operationID, err := secretstore.ParseOperationID(view.Active.OperationID)
	if err != nil || strings.TrimSpace(view.Active.RuntimeID) == "" || !validRequestedState(view.Active.RequestedState) {
		return lifecycle.GatewayRuntimeTruth{}, fmt.Errorf("gateway runtime truth fence is invalid")
	}
	routes := make([]lifecycle.GatewayRuntimeRoute, 0, len(view.Active.Routes))
	for _, route := range view.Active.Routes {
		routes = append(routes, lifecycle.GatewayRuntimeRoute{
			RouteID: route.RouteID, Product: lifecycle.Product(route.Product), SettlementAsset: route.SettlementAsset,
			RouteProfileID: route.RouteProfileID, RequestedState: lifecycle.RouteState(route.RequestedState), Ready: route.Ready,
		})
	}
	truth.OperationID = operationID
	truth.RuntimeID = view.Active.RuntimeID
	truth.RequestedState = lifecycle.State(view.Active.RequestedState)
	truth.Routes = routes
	truth.Active = true
	return truth, nil
}

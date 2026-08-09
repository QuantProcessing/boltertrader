package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
)

type gatewayAuthority struct {
	mu        sync.Mutex
	store     store.DesiredStateStore
	snapshots store.RuntimeSnapshotStore
	manager   *account.Manager
	reads     authorityReadSource
	mode      account.RuntimeMode
	now       func() time.Time
}

type authorityVenue struct {
	Venue       string `json:"venue"`
	Environment string `json:"environment"`
	Ready       bool   `json:"ready"`
}

type authorityInstrument struct {
	InstrumentID string              `json:"instrumentId"`
	Venue        string              `json:"venue"`
	Environment  string              `json:"environment"`
	Product      string              `json:"product"`
	Settlement   string              `json:"settlement,omitempty"`
	Symbol       string              `json:"symbol"`
	BaseAsset    string              `json:"baseAsset"`
	QuoteAsset   string              `json:"quoteAsset"`
	Rules        authorityTradeRules `json:"rules"`
	Ready        bool                `json:"ready"`
}

type authorityCatalogInstruments struct {
	Revision    string                `json:"revision"`
	ObservedAt  time.Time             `json:"observedAt"`
	Instruments []authorityInstrument `json:"instruments"`
}

type authorityRuntimeStatus struct {
	Mode     account.RuntimeMode      `json:"mode"`
	Accounts []account.RuntimeAccount `json:"accounts"`
}

type authorityProjectionReadiness struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

type authorityRuntimeCapability struct {
	RowID      string                `json:"rowId"`
	Venue      string                `json:"venue"`
	Product    string                `json:"product"`
	Settlement string                `json:"settlement,omitempty"`
	WriteReady bool                  `json:"writeReady"`
	Operations capability.Operations `json:"operations"`
}

type authorityRuntimeProjection struct {
	AccountID                       string                       `json:"accountId"`
	DesiredStateRevision            uint64                       `json:"desiredStateRevision"`
	RuntimeGeneration               uint64                       `json:"runtimeGeneration"`
	CredentialVersion               uint64                       `json:"credentialVersion"`
	RuntimeVersion                  uint64                       `json:"runtimeVersion"`
	Readiness                       authorityProjectionReadiness `json:"readiness"`
	WriteReadiness                  authorityProjectionReadiness `json:"writeReadiness"`
	LastAppliedDesiredStateRevision uint64                       `json:"lastAppliedDesiredStateRevision"`
	LastAppliedRuntimeGeneration    uint64                       `json:"lastAppliedRuntimeGeneration"`
	BlockedReason                   *string                      `json:"blockedReason"`
	Capabilities                    []authorityRuntimeCapability `json:"capabilities"`
	ObservedAt                      time.Time                    `json:"observedAt"`
}

type authorityTradeRules struct {
	PriceIncrement    string `json:"priceIncrement"`
	QuantityIncrement string `json:"quantityIncrement"`
	MinQuantity       string `json:"minQuantity"`
	MinNotional       string `json:"minNotional,omitempty"`
}

type authorityMarketData struct {
	Venue             string                  `json:"venue"`
	Environment       string                  `json:"environment"`
	Product           string                  `json:"product"`
	InstrumentID      string                  `json:"instrumentId"`
	BestBid           string                  `json:"bestBid"`
	BestBidSize       string                  `json:"bestBidSize"`
	BestAsk           string                  `json:"bestAsk"`
	BestAskSize       string                  `json:"bestAskSize"`
	Bids              []authorityLevel        `json:"bids"`
	Asks              []authorityLevel        `json:"asks"`
	MarkPrice         string                  `json:"markPrice,omitempty"`
	FundingRate       string                  `json:"fundingRate,omitempty"`
	FundingObservedAt time.Time               `json:"fundingObservedAt,omitempty"`
	FundingTime       time.Time               `json:"fundingTime,omitempty"`
	NextFundingTime   time.Time               `json:"nextFundingTime,omitempty"`
	FundingHistory    []authorityFundingPoint `json:"fundingHistory,omitempty"`
	OpenInterest      string                  `json:"openInterest,omitempty"`
	ObservedAt        time.Time               `json:"observedAt"`
}

type authorityLevel struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

type authorityFundingPoint struct {
	Rate        string    `json:"rate"`
	ObservedAt  time.Time `json:"observedAt,omitempty"`
	FundingTime time.Time `json:"fundingTime"`
}

const (
	authorityDepthLimit          = 20
	authorityFundingHistoryLimit = 20
)

type authorityReadSource interface {
	Venues() []authorityVenue
	Instruments() (authorityCatalogInstruments, error)
	MarketData(context.Context, string, string, string, string) (authorityMarketData, error)
	Runtime(string) (authorityRuntimeProjection, error)
}

type authorityAccountSource interface {
	Accounts() []account.Resource
	Instruments(domain.AccountID) (catalog.Snapshot, error)
	Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error)
	Spot(domain.AccountID) (exchange.SpotClient, error)
	Perp(domain.AccountID) (exchange.PerpClient, error)
	Capability(domain.AccountID) (capability.Row, error)
	Runtime(domain.AccountID) (account.RuntimeAccount, error)
	Status(domain.AccountID) (account.Status, error)
}

type managerAuthorityReads struct {
	manager  authorityAccountSource
	mode     account.RuntimeMode
	profiles *routeprofile.Registry
	now      func() time.Time
}

type opaqueRuntimeLifecycle struct {
	resolve    func(context.Context, secret.OpaqueRef) (string, error)
	activate   func(context.Context, account.DesiredRuntime, string) error
	deactivate func(context.Context, domain.AccountID) error
	blockers   func(context.Context, domain.AccountID) (account.RuntimeBlockers, error)
}

func newOpaqueRuntimeLifecycle(
	resolve func(context.Context, secret.OpaqueRef) (string, error),
	activate func(context.Context, account.DesiredRuntime, string) error,
	deactivate func(context.Context, domain.AccountID) error,
	blockers func(context.Context, domain.AccountID) (account.RuntimeBlockers, error),
) (*opaqueRuntimeLifecycle, error) {
	if resolve == nil || activate == nil || deactivate == nil || blockers == nil {
		return nil, errors.New("runtime lifecycle dependencies are incomplete")
	}
	return &opaqueRuntimeLifecycle{resolve: resolve, activate: activate, deactivate: deactivate, blockers: blockers}, nil
}

func (lifecycle *opaqueRuntimeLifecycle) Validate(ctx context.Context, desired account.DesiredRuntime) error {
	material, err := lifecycle.resolve(ctx, desired.SecretRef)
	if err != nil || material == "" {
		return errors.New("runtime secret resolution failed")
	}
	material = ""
	return nil
}

func (lifecycle *opaqueRuntimeLifecycle) Activate(ctx context.Context, desired account.DesiredRuntime) error {
	material, err := lifecycle.resolve(ctx, desired.SecretRef)
	if err != nil || material == "" {
		return errors.New("runtime secret resolution failed")
	}
	defer func() { material = "" }()
	if err := lifecycle.activate(ctx, desired, material); err != nil {
		return errors.New("runtime activation failed")
	}
	return nil
}

func (lifecycle *opaqueRuntimeLifecycle) Deactivate(ctx context.Context, accountID domain.AccountID) error {
	if err := lifecycle.deactivate(ctx, accountID); err != nil {
		return errors.New("runtime deactivation failed")
	}
	return nil
}

func (lifecycle *opaqueRuntimeLifecycle) Blockers(ctx context.Context, accountID domain.AccountID) (account.RuntimeBlockers, error) {
	blockers, err := lifecycle.blockers(ctx, accountID)
	if err != nil {
		return account.RuntimeBlockers{}, errors.New("runtime blocker lookup failed")
	}
	return blockers, nil
}

func newGatewayAuthority(ctx context.Context, desiredStore store.DesiredStateStore, manager *account.Manager, mode account.RuntimeMode) (*gatewayAuthority, error) {
	if ctx == nil || desiredStore == nil || manager == nil {
		return nil, errors.New("gateway authority dependencies are incomplete")
	}
	if mode == "" {
		mode = account.RuntimeModeTest
	}
	authority := &gatewayAuthority{
		store: desiredStore, manager: manager,
		reads: managerAuthorityReads{manager: manager, mode: mode, profiles: routeprofile.DefaultRegistry(), now: time.Now},
		mode:  mode, now: time.Now,
	}
	if snapshots, ok := desiredStore.(store.RuntimeSnapshotStore); ok {
		authority.snapshots = snapshots
		if err := manager.SetRuntimeStateStore(snapshots); err != nil {
			return nil, errors.New("configure runtime snapshot store failed")
		}
	}
	if err := authority.restore(ctx); err != nil {
		return nil, errors.New("restore gateway authority failed")
	}
	return authority, nil
}

func (authority *gatewayAuthority) ApplySnapshot(ctx context.Context, desired store.DesiredSnapshot) (store.PromotionResult, error) {
	if ctx == nil || authority.snapshots == nil {
		return store.PromotionResult{}, errors.New("runtime snapshot authority is unavailable")
	}
	retired, err := authority.snapshots.IsSecretReferenceRetired(ctx, authoritySecretScope(authority.mode), desired.SecretRef)
	if err != nil {
		return store.PromotionResult{}, err
	}
	if retired {
		return store.PromotionResult{}, store.ErrRetiredSecretReference
	}
	current, err := authority.snapshots.LoadRuntimeState(ctx, desired.AccountID)
	if err == nil && current.Active != nil && current.Active.OperationID == desired.OperationID {
		currentDigest, digestErr := current.Active.CanonicalDigest()
		desiredDigest, desiredErr := desired.CanonicalDigest()
		if digestErr != nil || desiredErr != nil || currentDigest != desiredDigest {
			return store.PromotionResult{}, store.ErrOperationConflict
		}
		return store.PromotionResult{Active: *current.Active}, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.PromotionResult{}, err
	}
	pending, err := authority.snapshots.PreparePending(ctx, desired)
	if err != nil {
		return store.PromotionResult{}, err
	}
	group, err := authority.manager.BuildCandidate(ctx, pending)
	if err != nil {
		observedAt := authority.now().UTC()
		for _, route := range pending.Routes {
			if route.RequestedState != store.RouteActive {
				continue
			}
			if observationErr := authority.snapshots.RecordCandidateObservation(ctx, store.CandidateObservation{
				OperationID: pending.OperationID, ObservationRevision: 1, RouteID: route.RouteID,
				State: store.CandidateFailed, ReasonCode: "CANDIDATE_BUILD_FAILED", ObservedAt: observedAt,
			}); observationErr != nil {
				return store.PromotionResult{}, observationErr
			}
		}
		if failedErr := authority.snapshots.MarkCandidateFailed(ctx, pending.OperationID); failedErr != nil {
			return store.PromotionResult{}, failedErr
		}
		return store.PromotionResult{}, err
	}
	observedAt := authority.now().UTC()
	for _, route := range pending.Routes {
		if route.RequestedState != store.RouteActive {
			continue
		}
		if err := authority.snapshots.RecordCandidateObservation(ctx, store.CandidateObservation{
			OperationID: pending.OperationID, ObservationRevision: 1, RouteID: route.RouteID,
			State: store.CandidateReady, ObservedAt: observedAt,
		}); err != nil {
			return store.PromotionResult{}, err
		}
	}
	if err := authority.snapshots.MarkCandidateReady(ctx, pending.OperationID); err != nil {
		return store.PromotionResult{}, err
	}
	return authority.manager.PromoteCandidate(ctx, group)
}

func (authority *gatewayAuthority) Execute(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if ctx == nil {
		return httpapi.AuthorityResponse{}, authorityError(errors.New("context is required"))
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()

	var response httpapi.AuthorityResponse
	var err error
	switch request.Operation {
	case httpapi.AuthorityApplyDesired:
		response, err = authority.applyDesired(ctx, request)
	case httpapi.AuthorityRetireDesired:
		response, err = authority.retireDesired(ctx, request)
	case httpapi.AuthorityFinalizeRotation:
		response, err = authority.finalizeRotation(ctx, request)
	case httpapi.AuthorityCreateDesired:
		response, err = authority.create(ctx, request.Desired)
	case httpapi.AuthorityListDesired:
		response, err = authority.listDesired(ctx)
	case httpapi.AuthorityGetDesired:
		if authority.snapshots != nil {
			response, err = authority.getRuntimeView(ctx, request.AccountID)
		} else {
			response, err = authority.getDesired(ctx, request.AccountID)
		}
	case httpapi.AuthorityValidateDesired:
		response, err = authority.validate(ctx, request)
	case httpapi.AuthorityRotateDesired:
		response, err = authority.rotate(ctx, request)
	case httpapi.AuthorityUpdateDesired:
		response, err = authority.update(ctx, request)
	case httpapi.AuthorityDisableDesired, httpapi.AuthorityDrainDesired:
		response, err = authority.transition(ctx, request)
	case httpapi.AuthorityDeleteDesired:
		err = authority.remove(ctx, request)
	case httpapi.AuthorityListRuntimeAccounts:
		response.Body = authority.manager.Runtimes()
	case httpapi.AuthorityGetRuntimeAccount:
		if len(request.Query) != 0 {
			err = errors.New("runtime account query is not allowed")
		} else if authority.snapshots != nil {
			response, err = authority.getRuntimeView(ctx, request.AccountID)
		} else {
			response.Body, err = authority.reads.Runtime(request.AccountID)
		}
	case httpapi.AuthorityRuntimeStatus:
		response.Body = authorityRuntimeStatus{Mode: authority.mode, Accounts: authority.manager.Runtimes()}
	case httpapi.AuthorityRuntimeReadiness:
		response.Body = httpapi.RuntimeReadiness{Ready: authority.manager.RuntimeReady()}
	case httpapi.AuthorityCatalogVenues:
		response.Body = authority.reads.Venues()
	case httpapi.AuthorityCatalogInstruments:
		response.Body, err = authority.reads.Instruments()
	case httpapi.AuthorityMarketData:
		venue, environment, product, instrumentID := request.Query.Get("venue"), request.Query.Get("environment"), request.Query.Get("product"), request.Query.Get("instrumentId")
		if strings.TrimSpace(venue) == "" || strings.TrimSpace(environment) == "" || strings.TrimSpace(product) == "" || strings.TrimSpace(instrumentID) == "" {
			err = errors.New("market-data composite identity is required")
			break
		}
		var fact authorityMarketData
		fact, err = authority.reads.MarketData(ctx, venue, environment, product, instrumentID)
		if err == nil && (fact.Venue != venue || fact.Environment != environment || fact.Product != product || fact.InstrumentID != instrumentID) {
			err = errors.New("market-data composite identity mismatch")
		} else {
			response.Body = fact
		}
	case httpapi.AuthorityRuntimeOpenOrders, httpapi.AuthorityRuntimePositions,
		httpapi.AuthorityRuntimeUncertain, httpapi.AuthorityRuntimeEvents,
		httpapi.AuthorityPrivateEvents:
		response.Body = []any{}
	default:
		err = errors.New("unsupported authority operation")
	}
	if err != nil {
		return httpapi.AuthorityResponse{}, authorityError(err)
	}
	return response, nil
}

func (authority *gatewayAuthority) retireDesired(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if request.Retirement == nil || request.Mode != string(authority.mode) || request.AccountID != request.Retirement.AccountID || request.OperationID != request.Retirement.OperationID || request.Retirement.DesiredStateRevision < 2 {
		return httpapi.AuthorityResponse{}, errors.New("runtime retirement request is invalid")
	}
	operationID, err := secretstore.ParseOperationID(request.OperationID)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	accountID := domain.AccountID(request.AccountID)
	retirement := store.RetirementRequest{
		OperationID: operationID, AccountID: accountID,
		ExpectedActiveFence: domain.AccountFence{
			AccountID: accountID, DesiredStateRevision: uint64(request.Retirement.DesiredStateRevision - 1),
			RuntimeGeneration: uint64(request.Retirement.RuntimeGeneration), CredentialVersion: uint64(request.Retirement.CredentialVersion),
		},
		TombstoneRevision: uint64(request.Retirement.DesiredStateRevision),
		Scope:             authoritySecretScope(authority.mode),
	}
	result, err := authority.manager.RetireRuntime(ctx, retirement)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	state, err := authority.snapshots.LoadRuntimeState(ctx, accountID)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if !state.Tombstoned || state.TombstoneRevision != result.TombstoneRevision || state.Active != nil || state.Pending != nil || len(state.Retiring) != 0 {
		return httpapi.AuthorityResponse{}, store.ErrOperationConflict
	}
	return httpapi.AuthorityResponse{Body: authority.runtimeView(state), Status: http.StatusOK}, nil
}

func (authority *gatewayAuthority) finalizeRotation(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if request.RotationFinalization == nil || request.Mode != string(authority.mode) ||
		request.AccountID != request.RotationFinalization.AccountID || request.OperationID != request.RotationFinalization.OperationID {
		return httpapi.AuthorityResponse{}, errors.New("rotation finalization request is invalid")
	}
	operationID, err := secretstore.ParseOperationID(request.OperationID)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	accountID := domain.AccountID(request.AccountID)
	activeFence, err := accountFenceFromHTTP(request.RotationFinalization.ExpectedActiveFence, accountID)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	retiringFence, err := accountFenceFromHTTP(request.RotationFinalization.ExpectedRetiringFence, accountID)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	finalization := store.RotationFinalizationRequest{
		OperationID: operationID, AccountID: accountID,
		ExpectedActiveFence: activeFence, ExpectedRetiringFence: retiringFence,
		Scope: authoritySecretScope(authority.mode),
	}
	if err := finalization.Validate(); err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	result, err := authority.manager.FinalizeRotation(ctx, finalization)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if !rotationFinalizationResultMatches(finalization, result) {
		return httpapi.AuthorityResponse{}, store.ErrOperationConflict
	}
	return httpapi.AuthorityResponse{Status: http.StatusOK, Body: httpapi.RotationFinalizationProof{
		OperationID: string(result.OperationID), AccountID: string(result.AccountID),
		ActiveFence:          accountFenceToHTTP(result.ActiveFence),
		RetiringFence:        accountFenceToHTTP(result.RetiringFence),
		TombstoneRevision:    int64(result.TombstoneRevision),
		RetiringState:        result.RetiringState,
		SecretReferenceState: result.SecretReferenceState,
		CompletedAt:          result.CompletedAt.UTC(),
	}}, nil
}

func accountFenceFromHTTP(fence httpapi.AccountFence, accountID domain.AccountID) (domain.AccountFence, error) {
	if fence.AccountID != string(accountID) || fence.DesiredStateRevision <= 0 || fence.RuntimeGeneration <= 0 || fence.CredentialVersion <= 0 {
		return domain.AccountFence{}, store.ErrPromotionFenceConflict
	}
	result := domain.AccountFence{
		AccountID: accountID, DesiredStateRevision: uint64(fence.DesiredStateRevision),
		RuntimeGeneration: uint64(fence.RuntimeGeneration), CredentialVersion: uint64(fence.CredentialVersion),
	}
	if err := result.Validate(); err != nil {
		return domain.AccountFence{}, store.ErrPromotionFenceConflict
	}
	return result, nil
}

func accountFenceToHTTP(fence domain.AccountFence) httpapi.AccountFence {
	return httpapi.AccountFence{
		AccountID: string(fence.AccountID), DesiredStateRevision: int64(fence.DesiredStateRevision),
		RuntimeGeneration: int64(fence.RuntimeGeneration), CredentialVersion: int64(fence.CredentialVersion),
	}
}

func rotationFinalizationResultMatches(request store.RotationFinalizationRequest, result store.RotationFinalizationResult) bool {
	return result.OperationID == request.OperationID && result.AccountID == request.AccountID &&
		result.ActiveFence == request.ExpectedActiveFence && result.RetiringFence == request.ExpectedRetiringFence &&
		result.TombstoneRevision == request.ExpectedActiveFence.DesiredStateRevision &&
		result.RetiringState == "retired" && result.SecretReferenceState == "tombstoned" && !result.CompletedAt.IsZero()
}

func authoritySecretScope(mode account.RuntimeMode) secretstore.Scope {
	secretMode := secretstore.Mode(mode)
	return secretstore.Scope{Mode: secretMode, Namespace: "boltertrader/" + string(secretMode), KeyVersion: "v1"}
}

func (authority *gatewayAuthority) applyDesired(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if request.DesiredApply == nil || request.Mode != string(authority.mode) || request.AccountID != request.DesiredApply.AccountID || request.OperationID != request.DesiredApply.OperationID {
		return httpapi.AuthorityResponse{}, errors.New("desired runtime request is invalid")
	}
	desired, err := desiredSnapshot(*request.DesiredApply, routeprofile.Mode(authority.mode))
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if _, err := authority.ApplySnapshot(ctx, desired); err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return authority.getRuntimeView(ctx, request.AccountID)
}

func desiredSnapshot(request httpapi.DesiredApplyRequest, mode routeprofile.Mode) (store.DesiredSnapshot, error) {
	operationID, err := secretstore.ParseOperationID(request.OperationID)
	if err != nil {
		return store.DesiredSnapshot{}, errors.New("desired operation id is invalid")
	}
	secretRef, err := secretstore.ParseOpaqueRef(request.SecretRef)
	if err != nil {
		return store.DesiredSnapshot{}, errors.New("desired secret reference is invalid")
	}
	if request.DesiredStateRevision < 1 || request.RuntimeGeneration < 1 || request.CredentialVersion < 1 {
		return store.DesiredSnapshot{}, errors.New("desired runtime fence is invalid")
	}
	routes := make([]store.RouteDefinition, 0, len(request.Routes))
	for _, requestedRoute := range request.Routes {
		profile, err := routeprofile.DefaultRegistry().Resolve(mode, request.Venue, requestedRoute.RouteProfileID)
		if err != nil || string(profile.NativeEnvironment) != request.NativeEnvironment || string(profile.Product) != requestedRoute.Product || profile.SettlementAsset != requestedRoute.SettlementAsset {
			return store.DesiredSnapshot{}, errors.New("desired route profile is invalid")
		}
		routes = append(routes, store.RouteDefinition{
			RouteID: domain.RouteID(requestedRoute.RouteID), Product: requestedRoute.Product,
			SettlementAsset: requestedRoute.SettlementAsset, RouteProfileID: requestedRoute.RouteProfileID,
			RequestedState: store.RouteState(requestedRoute.RequestedState),
		})
	}
	accountID := domain.AccountID(request.AccountID)
	snapshot := store.DesiredSnapshot{
		SnapshotID: uuid.New(), OperationID: operationID, AccountID: accountID,
		Venue: request.Venue, NativeEnvironment: request.NativeEnvironment, SecretRef: secretRef,
		Fence: domain.AccountFence{
			AccountID: accountID, DesiredStateRevision: uint64(request.DesiredStateRevision),
			RuntimeGeneration: uint64(request.RuntimeGeneration), CredentialVersion: uint64(request.CredentialVersion),
		},
		RequestedState: store.DesiredAccountState(request.RequestedState), Routes: routes,
	}
	digest, err := snapshot.CanonicalDigest()
	if err != nil {
		return store.DesiredSnapshot{}, err
	}
	snapshot.RequestDigest = digest
	return snapshot, nil
}

func (authority *gatewayAuthority) getRuntimeView(ctx context.Context, accountID string) (httpapi.AuthorityResponse, error) {
	if authority.snapshots == nil {
		return httpapi.AuthorityResponse{}, errors.New("runtime snapshot authority is unavailable")
	}
	state, err := authority.snapshots.LoadRuntimeState(ctx, domain.AccountID(accountID))
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return httpapi.AuthorityResponse{Body: authority.runtimeView(state), Status: http.StatusOK}, nil
}

func (authority *gatewayAuthority) runtimeView(state store.RuntimeState) httpapi.RuntimeView {
	view := httpapi.RuntimeView{
		AccountID: string(state.AccountID), Retiring: []httpapi.RuntimeSnapshotView{}, Tombstoned: state.Tombstoned,
		Capabilities: []httpapi.RuntimeCapabilityView{},
	}
	if view.AccountID == "" {
		if state.Active != nil {
			view.AccountID = string(state.Active.AccountID)
		} else if state.Pending != nil {
			view.AccountID = string(state.Pending.AccountID)
		}
	}
	if state.Active != nil {
		active := authority.snapshotView(state.Active.DesiredSnapshot, nil)
		view.Active = &active
		authority.enrichRuntimeView(&view, *state.Active)
	}
	if state.Pending != nil {
		pending := httpapi.PendingSnapshotView{
			RuntimeSnapshotView: authority.snapshotView(state.Pending.DesiredSnapshot, state.Pending.Observations),
			State:               string(state.Pending.OperationState),
		}
		view.Pending = &pending
	}
	for _, retiring := range state.Retiring {
		view.Retiring = append(view.Retiring, authority.snapshotView(retiring.DesiredSnapshot, nil))
	}
	return view
}

func (authority *gatewayAuthority) enrichRuntimeView(view *httpapi.RuntimeView, active store.ActiveSnapshot) {
	fence := active.Fence
	view.DesiredStateRevision = int64(fence.DesiredStateRevision)
	view.RuntimeGeneration = int64(fence.RuntimeGeneration)
	view.CredentialVersion = int64(fence.CredentialVersion)
	view.RuntimeVersion = int64(fence.RuntimeGeneration)
	view.LastAppliedDesiredStateRevision = int64(fence.DesiredStateRevision)
	view.LastAppliedRuntimeGeneration = int64(fence.RuntimeGeneration)
	view.ObservedAt = active.PromotedAt.UTC()

	routing := authority.manager.ActiveSnapshot(active.AccountID)
	readReady := routing != nil && routing.Fence == active.Fence
	readReason := ""
	if !readReady {
		readReason = "runtime_not_promoted"
	}
	writeReady := readReady
	writeReason := ""
	rows := capability.StaticMatrix().Rows
	for _, route := range active.Routes {
		if route.RequestedState != store.RouteActive {
			continue
		}
		routeReady := false
		if routing != nil && routing.Fence == active.Fence {
			if runtimeRoute, ok := routing.Routes[route.RouteID]; ok {
				routeReady = runtimeRoute.WriteReady
			}
		}
		readReady = readReady && routeReady
		writeReady = writeReady && routeReady
		if !routeReady && readReason == "" {
			readReason = "route_not_ready"
		}
		matched := false
		for _, row := range rows {
			if row.Venue != active.Venue || row.Product != route.Product || row.Settlement != route.SettlementAsset {
				continue
			}
			view.Capabilities = append(view.Capabilities, httpapi.RuntimeCapabilityView{
				RowID: row.RowID, Venue: row.Venue, Product: row.Product, Settlement: row.Settlement,
				WriteReady: routeReady && row.WriteReadyDefault, Operations: row.Operations,
			})
			matched = true
			if !row.WriteReadyDefault {
				writeReady = false
				if writeReason == "" {
					writeReason = "capability_write_disabled"
				}
			}
			break
		}
		if !matched {
			readReady = false
			writeReady = false
			if readReason == "" {
				readReason = "capability_unavailable"
			}
		}
	}
	if len(view.Capabilities) == 0 {
		readReady = false
		writeReady = false
		if readReason == "" {
			readReason = "capability_unavailable"
		}
	}
	if !writeReady && writeReason == "" {
		writeReason = readReason
	}
	view.Readiness = httpapi.RuntimeProjectionReadiness{Ready: readReady, Reason: readReason}
	view.WriteReadiness = httpapi.RuntimeProjectionReadiness{Ready: writeReady, Reason: writeReason}
	blockedReason := writeReason
	if blockedReason == "" {
		blockedReason = readReason
	}
	if blockedReason != "" {
		view.BlockedReason = &blockedReason
	}
}

func (authority *gatewayAuthority) snapshotView(snapshot store.DesiredSnapshot, observations []store.CandidateObservation) httpapi.RuntimeSnapshotView {
	latest := make(map[domain.RouteID]store.CandidateObservation, len(observations))
	for _, observation := range observations {
		if current, ok := latest[observation.RouteID]; !ok || observation.ObservationRevision >= current.ObservationRevision {
			latest[observation.RouteID] = observation
		}
	}
	active := authority.manager.ActiveSnapshot(snapshot.AccountID)
	view := httpapi.RuntimeSnapshotView{
		OperationID:          string(snapshot.OperationID),
		DesiredStateRevision: int64(snapshot.Fence.DesiredStateRevision), RuntimeGeneration: int64(snapshot.Fence.RuntimeGeneration),
		CredentialVersion: int64(snapshot.Fence.CredentialVersion), RequestedState: string(snapshot.RequestedState),
		Routes: make([]httpapi.RuntimeRouteView, 0, len(snapshot.Routes)),
	}
	for _, route := range snapshot.Routes {
		routeView := httpapi.RuntimeRouteView{
			RouteID: string(route.RouteID), Product: route.Product, SettlementAsset: route.SettlementAsset,
			RouteProfileID: route.RouteProfileID, RequestedState: string(route.RequestedState),
		}
		if observation, ok := latest[route.RouteID]; ok {
			routeView.Ready = observation.State == store.CandidateReady
			routeView.Reason = observation.ReasonCode
		} else if active != nil && active.Fence == snapshot.Fence {
			if runtimeRoute, ok := active.Routes[route.RouteID]; ok {
				routeView.Ready = runtimeRoute.WriteReady
			}
		}
		view.Routes = append(view.Routes, routeView)
	}
	return view
}

func (reads managerAuthorityReads) Venues() []authorityVenue {
	resources := reads.manager.Accounts()
	registry := reads.profiles
	if registry == nil {
		registry = routeprofile.DefaultRegistry()
	}
	mode := routeprofile.Mode(reads.mode)
	if mode == "" {
		mode = routeprofile.ModeTest
	}
	profiles := registry.Profiles(mode)
	venues := make([]authorityVenue, 0, len(profiles))
	indexes := make(map[string]int, len(profiles))
	for _, profile := range profiles {
		key := profile.Venue + "\x00" + string(profile.NativeEnvironment)
		if _, exists := indexes[key]; exists {
			continue
		}
		indexes[key] = len(venues)
		venues = append(venues, authorityVenue{Venue: profile.Venue, Environment: string(profile.NativeEnvironment)})
	}
	for _, resource := range resources {
		key := string(resource.Venue) + "\x00" + string(resource.Environment)
		if index, ok := indexes[key]; ok {
			venues[index].Ready = venues[index].Ready || resource.ReadReady
		}
	}
	sort.Slice(venues, func(i, j int) bool {
		if venues[i].Venue == venues[j].Venue {
			return venues[i].Environment < venues[j].Environment
		}
		return venues[i].Venue < venues[j].Venue
	})
	return venues
}

func (reads managerAuthorityReads) Runtime(accountID string) (authorityRuntimeProjection, error) {
	id := domain.AccountID(accountID)
	runtime, err := reads.manager.Runtime(id)
	if err != nil {
		return authorityRuntimeProjection{}, fmt.Errorf("read runtime account: %w", err)
	}
	status, err := reads.manager.Status(id)
	if err != nil {
		return authorityRuntimeProjection{}, fmt.Errorf("read runtime status: %w", err)
	}
	row, err := reads.manager.Capability(id)
	if err != nil {
		return authorityRuntimeProjection{}, fmt.Errorf("read runtime capability: %w", err)
	}
	if runtime.Desired.AccountID != id || runtime.Desired.DesiredStateRevision == 0 || runtime.Desired.RuntimeGeneration == 0 || runtime.Desired.CredentialVersion == 0 || runtime.RuntimeVersion == 0 || runtime.LastAppliedDesiredStateRevision == 0 || runtime.LastAppliedRuntimeGeneration == 0 || runtime.ObservedAt.IsZero() || status.AccountID != id || row.RowID == "" || row.Venue == "" || row.Product == "" {
		return authorityRuntimeProjection{}, errors.New("runtime projection authority is incomplete")
	}
	readReady := runtime.State == account.RuntimeStateActive && runtime.Ready && status.ReadReady
	readReason := runtime.Reason
	if !readReady && readReason == "" {
		readReason = status.Reason
	}
	if !readReady && readReason == "" {
		readReason = "runtime_" + string(runtime.State)
	}
	writeReady := readReady && status.WriteReady
	writeReason := ""
	if !writeReady {
		writeReason = status.Reason
		if writeReason == "" {
			writeReason = readReason
		}
	}
	blockedReason := writeReason
	if blockedReason == "" {
		blockedReason = readReason
	}
	var blocked *string
	if blockedReason != "" {
		blocked = &blockedReason
	}
	return authorityRuntimeProjection{
		AccountID: accountID, DesiredStateRevision: runtime.Desired.DesiredStateRevision, RuntimeGeneration: runtime.Desired.RuntimeGeneration,
		CredentialVersion: runtime.Desired.CredentialVersion, RuntimeVersion: runtime.RuntimeVersion,
		Readiness: authorityProjectionReadiness{Ready: readReady, Reason: readReason}, WriteReadiness: authorityProjectionReadiness{Ready: writeReady, Reason: writeReason},
		LastAppliedDesiredStateRevision: runtime.LastAppliedDesiredStateRevision, LastAppliedRuntimeGeneration: runtime.LastAppliedRuntimeGeneration,
		BlockedReason: blocked, Capabilities: []authorityRuntimeCapability{{RowID: row.RowID, Venue: row.Venue, Product: row.Product, Settlement: row.Settlement, WriteReady: writeReady, Operations: row.Operations}},
		ObservedAt: runtime.ObservedAt.UTC(),
	}, nil
}

func (reads managerAuthorityReads) Instruments() (authorityCatalogInstruments, error) {
	resources := reads.manager.Accounts()
	instruments := make([]authorityInstrument, 0)
	indexes := make(map[string]int)
	contributors := make([]string, 0, len(resources))
	var observedAt time.Time
	for _, resource := range resources {
		snapshot, err := reads.manager.Instruments(resource.AccountID)
		if err != nil {
			return authorityCatalogInstruments{}, fmt.Errorf("read configured account catalog: %w", err)
		}
		if strings.TrimSpace(snapshot.Revision) == "" || snapshot.LoadedAt.IsZero() {
			return authorityCatalogInstruments{}, errors.New("configured account catalog authority is incomplete")
		}
		contributors = append(contributors, strings.Join([]string{string(resource.Venue), string(resource.Environment), string(resource.Product), resource.Settlement, snapshot.Revision}, "\x00"))
		if observedAt.IsZero() || snapshot.LoadedAt.Before(observedAt) {
			observedAt = snapshot.LoadedAt.UTC()
		}
		for _, instrument := range snapshot.Instruments {
			key := strings.Join([]string{string(instrument.Venue), string(instrument.Environment), string(instrument.Product), string(instrument.ID)}, "\x00")
			if index, ok := indexes[key]; ok {
				instruments[index].Ready = instruments[index].Ready || resource.ReadReady
				continue
			}
			minNotional := ""
			if instrument.Rules.MinNotional.Valid {
				minNotional = instrument.Rules.MinNotional.Value.String()
			}
			indexes[key] = len(instruments)
			instruments = append(instruments, authorityInstrument{
				InstrumentID: string(instrument.ID), Venue: string(instrument.Venue), Environment: string(instrument.Environment), Product: string(instrument.Product),
				Settlement: instrument.Settlement, Symbol: instrument.Symbol, BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
				Rules: authorityTradeRules{PriceIncrement: instrument.Rules.PriceIncrement.String(), QuantityIncrement: instrument.Rules.QuantityIncrement.String(), MinQuantity: instrument.Rules.MinQuantity.String(), MinNotional: minNotional},
				Ready: resource.ReadReady,
			})
		}
	}
	if observedAt.IsZero() {
		now := reads.now
		if now == nil {
			now = time.Now
		}
		observedAt = now().UTC()
	}
	sort.Slice(instruments, func(i, j int) bool {
		if instruments[i].Venue == instruments[j].Venue {
			if instruments[i].Environment == instruments[j].Environment {
				if instruments[i].Product == instruments[j].Product {
					return instruments[i].InstrumentID < instruments[j].InstrumentID
				}
				return instruments[i].Product < instruments[j].Product
			}
			return instruments[i].Environment < instruments[j].Environment
		}
		return instruments[i].Venue < instruments[j].Venue
	})
	sort.Strings(contributors)
	fingerprint, err := json.Marshal(contributors)
	if err != nil {
		return authorityCatalogInstruments{}, errors.New("catalog revision aggregation failed")
	}
	revision := sha256.Sum256(fingerprint)
	return authorityCatalogInstruments{Revision: fmt.Sprintf("gateway-%x", revision[:16]), ObservedAt: observedAt, Instruments: instruments}, nil
}

func (reads managerAuthorityReads) MarketData(ctx context.Context, venue, environment, product, instrumentID string) (authorityMarketData, error) {
	venue = strings.TrimSpace(venue)
	environment = strings.TrimSpace(environment)
	product = strings.TrimSpace(product)
	instrumentID = strings.TrimSpace(instrumentID)
	if venue == "" || environment == "" || instrumentID == "" || (product != string(exchange.ProductSpot) && product != string(exchange.ProductPerp)) {
		return authorityMarketData{}, errors.New("market-data venue, environment, product, and instrumentId are required")
	}
	for _, resource := range reads.manager.Accounts() {
		if string(resource.Venue) != venue || string(resource.Environment) != environment || string(resource.Product) != product || !resource.ReadReady {
			continue
		}
		instrument, err := reads.manager.Resolve(ctx, resource.AccountID, domain.InstrumentID(instrumentID))
		if err != nil {
			continue
		}
		result := authorityMarketData{Venue: venue, Environment: environment, Product: product, InstrumentID: instrumentID}
		var market exchange.MarketREST
		switch resource.Product {
		case exchange.ProductSpot:
			market, err = reads.manager.Spot(resource.AccountID)
		case exchange.ProductPerp:
			var client exchange.PerpClient
			client, err = reads.manager.Perp(resource.AccountID)
			market = client
			if err == nil {
				row, capabilityErr := reads.manager.Capability(resource.AccountID)
				if capabilityErr != nil {
					return authorityMarketData{}, fmt.Errorf("read market-data capability: %w", capabilityErr)
				}
				if marks, ok := any(client).(exchange.PerpMarkPriceREST); ok {
					mark, markErr := marks.MarkPrice(ctx, instrument)
					if markErr != nil {
						return authorityMarketData{}, fmt.Errorf("read mark price: %w", markErr)
					}
					result.MarkPrice = mark.Price.String()
					result.ObservedAt = mark.Time.UTC()
				}
				if !row.Operations.Funding {
					return authorityMarketData{}, errors.New("configured perp funding capability is unavailable")
				}
				if row.Operations.Funding {
					funding, fundingErr := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: instrument})
					if fundingErr != nil {
						return authorityMarketData{}, fmt.Errorf("read funding rate: %w", fundingErr)
					}
					result.FundingRate = funding.Rate.String()
					result.FundingObservedAt = funding.ObservedAt.UTC()
					result.FundingTime = funding.FundingTime.UTC()
					result.NextFundingTime = funding.NextFundingTime.UTC()
					if result.FundingObservedAt.IsZero() || result.FundingTime.IsZero() || result.NextFundingTime.IsZero() {
						return authorityMarketData{}, errors.New("current funding timestamps are unavailable")
					}
					if funding.ObservedAt.After(result.ObservedAt) {
						result.ObservedAt = funding.ObservedAt.UTC()
					}
					history, historyErr := client.FundingRateHistory(ctx, exchange.FundingRateHistoryRequest{Instrument: instrument, Limit: authorityFundingHistoryLimit})
					if historyErr != nil {
						return authorityMarketData{}, fmt.Errorf("read funding history: %w", historyErr)
					}
					if len(history.Rates) > authorityFundingHistoryLimit {
						return authorityMarketData{}, errors.New("funding history exceeds requested bound")
					}
					result.FundingHistory = make([]authorityFundingPoint, len(history.Rates))
					for index, rate := range history.Rates {
						if rate.FundingTime.IsZero() {
							return authorityMarketData{}, errors.New("funding history timestamp is unavailable")
						}
						result.FundingHistory[index] = authorityFundingPoint{Rate: rate.Rate.String(), ObservedAt: rate.ObservedAt.UTC(), FundingTime: rate.FundingTime.UTC()}
					}
				}
			}
		default:
			continue
		}
		if err != nil || market == nil {
			continue
		}
		book, err := market.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument, Limit: authorityDepthLimit})
		if err != nil {
			return authorityMarketData{}, fmt.Errorf("read order book: %w", err)
		}
		if book.Time.IsZero() || len(book.Bids) == 0 || len(book.Asks) == 0 || len(book.Bids) > authorityDepthLimit || len(book.Asks) > authorityDepthLimit {
			return authorityMarketData{}, errors.New("bounded two-sided order book is unavailable")
		}
		result.Bids, err = projectAuthorityLevels(book.Bids)
		if err != nil {
			return authorityMarketData{}, err
		}
		result.Asks, err = projectAuthorityLevels(book.Asks)
		if err != nil {
			return authorityMarketData{}, err
		}
		result.BestBid, result.BestBidSize = result.Bids[0].Price, result.Bids[0].Quantity
		result.BestAsk, result.BestAskSize = result.Asks[0].Price, result.Asks[0].Quantity
		if result.ObservedAt.IsZero() || book.Time.After(result.ObservedAt) {
			result.ObservedAt = book.Time.UTC()
		}
		if result.ObservedAt.IsZero() {
			return authorityMarketData{}, errors.New("market-data observation time is unavailable")
		}
		return result, nil
	}
	return authorityMarketData{}, errors.New("configured market-data capability is unavailable")
}

func projectAuthorityLevels(levels []exchange.BookLevel) ([]authorityLevel, error) {
	result := make([]authorityLevel, len(levels))
	for index, level := range levels {
		if !level.Price.IsPositive() || !level.Quantity.IsPositive() {
			return nil, errors.New("order-book level is invalid")
		}
		result[index] = authorityLevel{Price: level.Price.String(), Quantity: level.Quantity.String()}
	}
	return result, nil
}

func (authority *gatewayAuthority) create(ctx context.Context, input *httpapi.DesiredAccount) (httpapi.AuthorityResponse, error) {
	if input == nil {
		return httpapi.AuthorityResponse{}, errors.New("desired account is required")
	}
	desired, err := desiredFromHTTP(*input, authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	created, err := authority.store.CreateDesiredAccount(ctx, desired)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(created), nil
}

func (authority *gatewayAuthority) listDesired(ctx context.Context) (httpapi.AuthorityResponse, error) {
	items, err := authority.store.ListDesiredAccounts(ctx)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	result := make([]httpapi.DesiredAccount, len(items))
	for index := range items {
		result[index] = desiredToHTTP(items[index])
	}
	return httpapi.AuthorityResponse{Body: result}, nil
}

func (authority *gatewayAuthority) getDesired(ctx context.Context, accountID string) (httpapi.AuthorityResponse, error) {
	desired, err := authority.store.LoadDesiredAccount(ctx, domain.AccountID(accountID))
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(desired), nil
}

func (authority *gatewayAuthority) validate(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil || request.Mutation.DesiredStateRevision != current.DesiredStateRevision ||
		request.Mutation.RuntimeGeneration != current.RuntimeGeneration || request.Mutation.CredentialVersion != current.CredentialVersion {
		return httpapi.AuthorityResponse{}, store.ErrDesiredStateRevisionConflict
	}
	desired, err := authority.runtimeDesired(current, current.SecretRef, account.RuntimeStateActive)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if err := authority.manager.ValidateRuntime(ctx, desired); err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return httpapi.AuthorityResponse{Body: map[string]any{"ready": true}, DesiredStateRevision: current.DesiredStateRevision}, nil
}

func (authority *gatewayAuthority) rotate(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil || request.Mutation.RotationFence == nil {
		return httpapi.AuthorityResponse{}, errors.New("rotation mutation is incomplete")
	}
	next, err := desiredFromMutation(current, *request.Mutation, store.DesiredRotating, authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	fence := next.RotationFence
	candidate := next
	candidate.RuntimeGeneration = fence.ToRuntimeGeneration
	candidate.CredentialVersion = fence.ToCredentialVersion
	candidate.RequestedState = store.DesiredActive
	desired, err := authority.runtimeDesired(candidate, next.SecretRef, account.RuntimeStateActive)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if err := authority.manager.ValidateRuntime(ctx, desired); err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	stored, err := authority.store.ApplyDesiredAccount(ctx, request.ExpectedRevision, next)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(stored), nil
}

func (authority *gatewayAuthority) update(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil {
		return httpapi.AuthorityResponse{}, errors.New("desired mutation is required")
	}
	next, err := desiredFromMutation(current, *request.Mutation, store.DesiredAccountState(request.Mutation.RequestedState), authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	stored, err := authority.store.ApplyDesiredAccount(ctx, request.ExpectedRevision, next)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}

	switch {
	case current.RequestedState == store.DesiredCreating && next.RequestedState == store.DesiredActive:
		runtimeDesired, convertErr := authority.runtimeDesired(next, next.SecretRef, account.RuntimeStateActive)
		if convertErr != nil {
			return httpapi.AuthorityResponse{}, convertErr
		}
		if _, err := authority.manager.CreateRuntime(ctx, runtimeDesired); err != nil {
			return httpapi.AuthorityResponse{}, err
		}
	case current.RequestedState == store.DesiredRotating && next.RequestedState == store.DesiredActive:
		runtimeDesired, convertErr := authority.runtimeDesired(next, next.SecretRef, account.RuntimeStateActive)
		if convertErr != nil {
			return httpapi.AuthorityResponse{}, convertErr
		}
		if _, err := authority.manager.RotateRuntime(ctx, runtimeDesired, uint64(request.ExpectedRevision)); err != nil {
			return httpapi.AuthorityResponse{}, err
		}
	}
	return desiredResponse(stored), nil
}

func (authority *gatewayAuthority) transition(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil {
		return httpapi.AuthorityResponse{}, errors.New("desired mutation is required")
	}
	nextState := store.DesiredDisabled
	runtimeState := account.RuntimeStateDisabled
	if request.Operation == httpapi.AuthorityDrainDesired {
		nextState = store.DesiredDraining
		runtimeState = account.RuntimeStateDraining
	}
	next, err := desiredFromMutation(current, *request.Mutation, nextState, authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	runtime, err := authority.manager.Runtime(current.AccountID)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if runtimeState == account.RuntimeStateDraining {
		_, err = authority.manager.DrainRuntime(ctx, current.AccountID, runtime.Desired.DesiredStateRevision, uint64(next.DesiredStateRevision))
	} else {
		_, err = authority.manager.DisableRuntime(ctx, current.AccountID, runtime.Desired.DesiredStateRevision, uint64(next.DesiredStateRevision))
	}
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	stored, err := authority.store.ApplyDesiredAccount(ctx, request.ExpectedRevision, next)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(stored), nil
}

func (authority *gatewayAuthority) remove(ctx context.Context, request httpapi.AuthorityRequest) error {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return err
	}
	if current.RequestedState != store.DesiredRemoved {
		return store.ErrInvalidAccountTransition
	}
	if runtime, runtimeErr := authority.manager.Runtime(current.AccountID); runtimeErr == nil {
		if err := authority.manager.RemoveRuntime(ctx, current.AccountID, runtime.Desired.DesiredStateRevision); err != nil {
			return err
		}
	} else if !errors.Is(runtimeErr, account.ErrUnknownAccount) {
		return runtimeErr
	}
	return authority.store.DeleteDesiredAccount(ctx, current.AccountID, request.ExpectedRevision)
}

func (authority *gatewayAuthority) loadExpected(ctx context.Context, accountID string, revision int64) (store.DesiredAccount, error) {
	desired, err := authority.store.LoadDesiredAccount(ctx, domain.AccountID(accountID))
	if err != nil {
		return store.DesiredAccount{}, err
	}
	if revision <= 0 || desired.DesiredStateRevision != revision {
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	return desired, nil
}

func (authority *gatewayAuthority) restore(ctx context.Context) error {
	if authority.snapshots != nil {
		return authority.manager.RestoreRuntimeState(ctx)
	}
	items, err := authority.store.ListRecoverableDesiredAccounts(ctx)
	if err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	runtimes := make([]account.DesiredRuntime, 0, len(items))
	for _, desired := range items {
		if desired.RequestedState == store.DesiredCreating {
			continue
		}
		secretRef := desired.SecretRef
		state := account.RuntimeState(desired.RequestedState)
		if desired.RequestedState == store.DesiredRotating {
			secretRef = desired.PreviousSecretRef
			state = account.RuntimeStateActive
		}
		runtimeDesired, convertErr := authority.runtimeDesired(desired, secretRef, state)
		if convertErr != nil {
			return convertErr
		}
		runtimes = append(runtimes, runtimeDesired)
	}
	return authority.manager.RestoreRuntimes(ctx, runtimes)
}

func (authority *gatewayAuthority) runtimeDesired(desired store.DesiredAccount, refValue string, state account.RuntimeState) (account.DesiredRuntime, error) {
	ref, err := secret.ParseOpaqueRef(refValue)
	if err != nil {
		return account.DesiredRuntime{}, err
	}
	rotationFence := ""
	if desired.RotationFence != nil {
		rotationFence = desired.RotationFence.ID
	}
	return account.DesiredRuntime{
		AccountID: desired.AccountID, Venue: desired.Venue, VenueEnvironment: desired.VenueEnvironment,
		Mode: authority.mode, SecretRef: ref, DesiredStateRevision: uint64(desired.DesiredStateRevision),
		RuntimeGeneration: uint64(desired.RuntimeGeneration), CredentialVersion: uint64(desired.CredentialVersion),
		RequestedState: state, RotationFence: rotationFence,
	}, nil
}

func desiredFromHTTP(input httpapi.DesiredAccount, now time.Time) (store.DesiredAccount, error) {
	desired := store.DesiredAccount{
		AccountID: domain.AccountID(input.AccountID), Venue: input.Venue, VenueEnvironment: input.VenueEnvironment,
		SecretRef: input.SecretRef, DesiredStateRevision: input.DesiredStateRevision,
		RuntimeGeneration: input.RuntimeGeneration, CredentialVersion: input.CredentialVersion,
		RequestedState: store.DesiredAccountState(input.RequestedState), UpdatedAt: now.UTC(),
	}
	if input.RotationFence != nil {
		desired.RotationFence = rotationFenceFromHTTP(*input.RotationFence, now)
	}
	if err := desired.Validate(); err != nil {
		return store.DesiredAccount{}, err
	}
	return desired, nil
}

func desiredFromMutation(current store.DesiredAccount, mutation httpapi.DesiredMutation, state store.DesiredAccountState, now time.Time) (store.DesiredAccount, error) {
	next := current
	if mutation.SecretRef != "" {
		next.SecretRef = mutation.SecretRef
	}
	next.DesiredStateRevision = mutation.DesiredStateRevision
	next.RuntimeGeneration = mutation.RuntimeGeneration
	next.CredentialVersion = mutation.CredentialVersion
	next.RequestedState = state
	next.UpdatedAt = now.UTC()
	if mutation.RotationFence != nil {
		next.RotationFence = rotationFenceFromHTTP(*mutation.RotationFence, now)
	} else {
		next.RotationFence = nil
	}
	if err := next.Validate(); err != nil {
		return store.DesiredAccount{}, err
	}
	return next, nil
}

func rotationFenceFromHTTP(input httpapi.RotationFence, now time.Time) *store.RotationFence {
	fence := &store.RotationFence{
		ID: input.RotationFenceID, State: store.RotationFenceState(input.State),
		FromCredentialVersion: input.FromCredentialVersion, ToCredentialVersion: input.ToCredentialVersion,
		FromRuntimeGeneration: input.FromRuntimeGeneration, ToRuntimeGeneration: input.ToRuntimeGeneration,
		ArmedAt: now.UTC(),
	}
	if fence.State == store.RotationReleased {
		released := now.UTC()
		fence.ReleasedAt = &released
	}
	return fence
}

func desiredResponse(desired store.DesiredAccount) httpapi.AuthorityResponse {
	return httpapi.AuthorityResponse{Body: desiredToHTTP(desired), DesiredStateRevision: desired.DesiredStateRevision}
}

func desiredToHTTP(desired store.DesiredAccount) httpapi.DesiredAccount {
	result := httpapi.DesiredAccount{
		AccountID: string(desired.AccountID), Venue: desired.Venue, VenueEnvironment: desired.VenueEnvironment,
		SecretRef: "redacted", DesiredStateRevision: desired.DesiredStateRevision,
		RuntimeGeneration: desired.RuntimeGeneration, CredentialVersion: desired.CredentialVersion,
		RequestedState: string(desired.RequestedState),
	}
	if desired.RotationFence != nil {
		result.RotationFence = &httpapi.RotationFence{
			RotationFenceID: desired.RotationFence.ID, State: string(desired.RotationFence.State),
			FromCredentialVersion: desired.RotationFence.FromCredentialVersion,
			ToCredentialVersion:   desired.RotationFence.ToCredentialVersion,
			FromRuntimeGeneration: desired.RotationFence.FromRuntimeGeneration,
			ToRuntimeGeneration:   desired.RotationFence.ToRuntimeGeneration,
		}
	}
	return result
}

func authorityError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, account.ErrUnknownAccount):
		return httpapi.AuthorityError{Status: http.StatusNotFound, Code: "DESIRED_ACCOUNT_NOT_FOUND"}
	case errors.Is(err, store.ErrDesiredStateRevisionConflict), errors.Is(err, account.ErrRuntimeRevision):
		return httpapi.AuthorityError{Status: http.StatusPreconditionFailed, Code: "DESIRED_STATE_REVISION_CONFLICT", Retryable: true}
	case errors.Is(err, store.ErrInvalidAccountTransition):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "INVALID_ACCOUNT_TRANSITION"}
	case errors.Is(err, store.ErrStaleRuntimeGeneration), errors.Is(err, account.ErrStaleWriteFence):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "STALE_RUNTIME_FENCE", Retryable: true}
	case errors.Is(err, store.ErrOperationConflict):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "OPERATION_ID_CONFLICT"}
	case errors.Is(err, store.ErrPromotionFenceConflict):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "PROMOTION_FENCE_CONFLICT", Retryable: true}
	case errors.Is(err, store.ErrCandidateNotReady):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "CANDIDATE_NOT_READY", Retryable: true}
	case errors.Is(err, store.ErrHandoffDrainBlocked):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "HANDOFF_DRAIN_BLOCKED", Retryable: true}
	case errors.Is(err, store.ErrRetiredSecretReference):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "RETIRED_SECRET_REFERENCE"}
	case errors.Is(err, account.ErrRuntimeBlocked):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "ACCOUNT_RUNTIME_BLOCKED"}
	case errors.Is(err, account.ErrRuntimeModeMismatch):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "RUNTIME_MODE_MISMATCH"}
	case errors.Is(err, account.ErrLiveWriteDisabled):
		return httpapi.AuthorityError{Status: http.StatusForbidden, Code: "LIVE_PRIVATE_WRITE_DISABLED"}
	default:
		return httpapi.AuthorityError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Retryable: true}
	}
}

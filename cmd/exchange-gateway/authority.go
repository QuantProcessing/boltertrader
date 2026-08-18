package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type gatewayAuthority struct {
	mu          sync.Mutex
	store       store.DesiredStateStore
	snapshots   store.RuntimeSnapshotStore
	manager     *account.Manager
	routeStatus func(domain.AccountID, string, string) (account.Status, error)
	reads       authorityReadSource
	now         func() time.Time
}

type validationSnapshotAuthority struct {
	snapshots store.RuntimeSnapshotStore
}

func (authority validationSnapshotAuthority) Execute(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	views := func(states []store.RuntimeState) []httpapi.RuntimeView {
		projector := gatewayAuthority{snapshots: authority.snapshots}
		result := make([]httpapi.RuntimeView, 0, len(states))
		for _, state := range states {
			result = append(result, projector.runtimeView(state))
		}
		return result
	}
	switch request.Operation {
	case httpapi.AuthorityRuntimeStatus, httpapi.AuthorityListRuntimeAccounts, httpapi.AuthorityListDesired:
		states, err := authority.snapshots.ListRuntimeStateForRecovery(ctx)
		if err != nil {
			return httpapi.AuthorityResponse{}, authorityError(err)
		}
		return httpapi.AuthorityResponse{Body: map[string]any{"accounts": views(states)}}, nil
	case httpapi.AuthorityGetRuntimeAccount, httpapi.AuthorityGetDesired:
		state, err := authority.snapshots.LoadRuntimeState(ctx, domain.AccountID(request.AccountID))
		if err != nil {
			return httpapi.AuthorityResponse{}, authorityError(err)
		}
		return httpapi.AuthorityResponse{Body: views([]store.RuntimeState{state})[0]}, nil
	case httpapi.AuthorityRuntimeReadiness:
		return httpapi.AuthorityResponse{Body: httpapi.RuntimeReadiness{Ready: true}}, nil
	case httpapi.AuthorityCatalogVenues:
		return httpapi.AuthorityResponse{Body: profileAuthorityVenues(nil)}, nil
	case httpapi.AuthorityCatalogInstruments:
		revision := sha256.Sum256([]byte("[]"))
		return httpapi.AuthorityResponse{Body: authorityCatalogInstruments{
			Revision: fmt.Sprintf("gateway-%x", revision[:16]), ObservedAt: time.Now().UTC(), Instruments: []authorityInstrument{},
		}}, nil
	case httpapi.AuthorityRuntimeOpenOrders, httpapi.AuthorityRuntimePositions, httpapi.AuthorityRuntimeUncertain,
		httpapi.AuthorityRuntimeEvents, httpapi.AuthorityPrivateEvents, httpapi.AuthorityMarketData,
		httpapi.AuthorityCandles, httpapi.AuthorityFundingBoard, httpapi.AuthorityFundingHistory:
		return httpapi.AuthorityResponse{Body: []any{}}, nil
	default:
		return httpapi.AuthorityResponse{}, httpapi.AuthorityError{Status: http.StatusServiceUnavailable, Code: "GATEWAY_WRITES_NOT_READY", Retryable: true}
	}
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
	Accounts []authorityPublicRuntimeAccount `json:"accounts"`
}

type authorityPublicDesiredRuntime struct {
	AccountID            domain.AccountID     `json:"accountId"`
	Venue                string               `json:"venue"`
	VenueEnvironment     string               `json:"venueEnvironment"`
	DesiredStateRevision uint64               `json:"desiredStateRevision"`
	RuntimeGeneration    uint64               `json:"runtimeGeneration"`
	CredentialVersion    uint64               `json:"credentialVersion"`
	RequestedState       account.RuntimeState `json:"requestedState"`
	RotationFence        string               `json:"rotationFence,omitempty"`
}

type authorityPublicRuntimeAccount struct {
	Desired                         authorityPublicDesiredRuntime `json:"desired"`
	State                           account.RuntimeState          `json:"state"`
	Ready                           bool                          `json:"ready"`
	Reason                          string                        `json:"reason,omitempty"`
	RuntimeVersion                  uint64                        `json:"runtimeVersion"`
	LastAppliedDesiredStateRevision uint64                        `json:"lastAppliedDesiredStateRevision"`
	LastAppliedRuntimeGeneration    uint64                        `json:"lastAppliedRuntimeGeneration"`
	ObservedAt                      time.Time                     `json:"observedAt"`
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
	FundingObservedAt time.Time               `json:"fundingObservedAt,omitzero"`
	FundingTime       time.Time               `json:"fundingTime,omitzero"`
	NextFundingTime   time.Time               `json:"nextFundingTime,omitzero"`
	FundingHistory    []authorityFundingPoint `json:"fundingHistory,omitempty"`
	OpenInterest      string                  `json:"openInterest,omitempty"`
	ObservedAt        time.Time               `json:"observedAt"`
}

type authorityLevel struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

// authorityCandlesQuery is a struct rather than the positional arguments
// MarketData takes because it carries six fields, five of them strings, and
// four of those are venue/environment/product/instrumentId in an order nothing
// in the type system enforces.
type authorityCandlesQuery struct {
	Venue        string
	Environment  string
	Product      string
	InstrumentID string
	Interval     string
	Limit        int
}

type authorityCandles struct {
	Venue        string            `json:"venue"`
	Environment  string            `json:"environment"`
	Product      string            `json:"product"`
	InstrumentID string            `json:"instrumentId"`
	Interval     string            `json:"interval"`
	Candles      []authorityCandle `json:"candles"`
	ObservedAt   time.Time         `json:"observedAt"`
}

type authorityCandle struct {
	OpenTime  time.Time `json:"openTime"`
	CloseTime time.Time `json:"closeTime"`
	Open      string    `json:"open"`
	High      string    `json:"high"`
	Low       string    `json:"low"`
	Close     string    `json:"close"`
	Volume    string    `json:"volume"`
	Complete  bool      `json:"complete"`
}

type authorityFundingPoint struct {
	Rate        string    `json:"rate"`
	ObservedAt  time.Time `json:"observedAt,omitzero"`
	FundingTime time.Time `json:"fundingTime"`
}

// authorityFundingBoard is the whole cross-venue rate table, one column per
// funding-capable route profile.
//
// It is a separate projection from authorityMarketData rather than a list of
// them because the board is read without an account: MarketData is served from
// a configured account's authenticated client, while every cell here comes from
// a public endpoint. Folding the two together would mean an operator with no
// accounts either sees an empty board or sees one and cannot tell which cells
// required credentials.
type authorityFundingBoard struct {
	ObservedAt time.Time               `json:"observedAt"`
	Venues     []authorityFundingVenue `json:"venues"`
}

// authorityFundingVenue is one column.
//
// Stale and Unavailable are both on the wire because they are different answers
// to "may I compare this number against the next column": stale means these
// rates are the venue's last known ones and older than the rest of the table,
// unavailable means there is nothing recent enough to put beside another venue
// at all. An unavailable column carries no rows by construction upstream.
//
// ObservedAt carries omitzero, not omitempty: omitempty does not consult
// time.Time.IsZero, so an unavailable column — which has no observation time by
// definition — would ship "0001-01-01T00:00:00Z" and render as an age of two
// thousand years rather than as no reading at all.
type authorityFundingVenue struct {
	Venue          string                `json:"venue"`
	Environment    string                `json:"environment"`
	Product        string                `json:"product"`
	Settlement     string                `json:"settlement,omitempty"`
	RouteProfileID string                `json:"routeProfileId"`
	ObservedAt     time.Time             `json:"observedAt,omitzero"`
	Stale          bool                  `json:"stale"`
	Unavailable    bool                  `json:"unavailable"`
	Rows           []authorityFundingRow `json:"rows"`
}

// authorityFundingRow is one instrument's rate on one venue.
//
// Every optional cell is a plain string carrying omitempty rather than
// exchange.OptionalDecimal, which serialises as {"value":…,"valid":…} and would
// put "0" on the wire for a venue that published nothing. Seven of the ten
// venues leave open interest empty in their bulk call, so an absent cell has to
// stay absent rather than become a number the venue never quoted.
//
// NextFundingTime is the same argument in the time domain, and needs omitzero
// to make it: omitempty ignores time.Time entirely, so a zero time reaches the
// browser as "0001-01-01T00:00:00Z" and renders as a settlement countdown that
// has already elapsed. Hyperliquid and lighter publish a bare rate with no
// settlement clock, and binance, okx, and nado leave it out whenever the venue
// itself omits it, so the zero is reachable on a third of the table.
type authorityFundingRow struct {
	Symbol          string    `json:"symbol"`
	NativeSymbol    string    `json:"nativeSymbol,omitempty"`
	NativeID        string    `json:"nativeId,omitempty"`
	BaseAsset       string    `json:"baseAsset,omitempty"`
	QuoteAsset      string    `json:"quoteAsset,omitempty"`
	Rate            string    `json:"rate"`
	IntervalHours   int64     `json:"intervalHours,omitempty"`
	NextFundingTime time.Time `json:"nextFundingTime,omitzero"`
	MarkPrice       string    `json:"markPrice,omitempty"`
	IndexPrice      string    `json:"indexPrice,omitempty"`
	OpenInterest    string    `json:"openInterest,omitempty"`
}

// authorityFundingHistoryQuery is a struct for the same reason
// authorityCandlesQuery is: three adjacent strings that the type system cannot
// tell apart, in an order nothing enforces.
//
// The window is a pair of instants rather than a preset name because the
// gateway is the capability layer. Which two windows the product offers is a
// product decision that belongs to the service above; what the gateway knows,
// and the service cannot, is how far back a single venue call actually reaches.
type authorityFundingHistoryQuery struct {
	Venue       string
	Environment string
	Symbol      string
	Since       time.Time
	Until       time.Time
}

// authorityFundingSeries is one instrument's settled funding on one venue.
//
// It echoes the requested identity back for the same reason authorityCandles
// does: two of these are drawn on one chart as the legs of a spread, and a
// response that arrived under the wrong label would render as a real number
// against the wrong venue rather than as a gap.
type authorityFundingSeries struct {
	Venue       string                        `json:"venue"`
	Environment string                        `json:"environment"`
	Symbol      string                        `json:"symbol"`
	Since       time.Time                     `json:"since"`
	Until       time.Time                     `json:"until"`
	ObservedAt  time.Time                     `json:"observedAt"`
	Points      []authorityFundingSeriesPoint `json:"points"`
}

// authorityFundingSeriesPoint is one settlement.
//
// Separate from authorityFundingPoint, which carries the venue's own
// observedAt for a single live rate embedded in a market-data fact. A settled
// point has no such stamp — it has the interval it accrued over, which is what
// makes two venues on different clocks comparable at all.
type authorityFundingSeriesPoint struct {
	FundingTime   time.Time `json:"fundingTime"`
	Rate          string    `json:"rate"`
	IntervalHours int64     `json:"intervalHours,omitempty"`
}

func authorityPublicRuntimes(runtimes []account.RuntimeAccount) []authorityPublicRuntimeAccount {
	result := make([]authorityPublicRuntimeAccount, 0, len(runtimes))
	for _, runtime := range runtimes {
		result = append(result, authorityPublicRuntimeAccount{
			Desired: authorityPublicDesiredRuntime{
				AccountID: runtime.Desired.AccountID, Venue: runtime.Desired.Venue,
				VenueEnvironment:     runtime.Desired.VenueEnvironment,
				DesiredStateRevision: runtime.Desired.DesiredStateRevision,
				RuntimeGeneration:    runtime.Desired.RuntimeGeneration,
				CredentialVersion:    runtime.Desired.CredentialVersion,
				RequestedState:       runtime.Desired.RequestedState,
				RotationFence:        runtime.Desired.RotationFence,
			},
			State: runtime.State, Ready: runtime.Ready, Reason: runtime.Reason,
			RuntimeVersion:                  runtime.RuntimeVersion,
			LastAppliedDesiredStateRevision: runtime.LastAppliedDesiredStateRevision,
			LastAppliedRuntimeGeneration:    runtime.LastAppliedRuntimeGeneration,
			ObservedAt:                      runtime.ObservedAt,
		})
	}
	return result
}

const (
	authorityDepthLimit          = 20
	authorityFundingHistoryLimit = 20
	// Matches the public plane's maxListLimit. It is a screenful of 1m bars with
	// room to scroll, and every supported venue serves 500 in one call.
	authorityCandleLimitMax = 500
)

type authorityReadSource interface {
	Venues() []authorityVenue
	Instruments() (authorityCatalogInstruments, error)
	MarketData(context.Context, string, string, string, string) (authorityMarketData, error)
	Candles(context.Context, authorityCandlesQuery) (authorityCandles, error)
	FundingBoard(context.Context) (authorityFundingBoard, error)
	FundingHistory(context.Context, authorityFundingHistoryQuery) (authorityFundingSeries, error)
	Runtime(string) (authorityRuntimeProjection, error)
}

type authorityAccountSource interface {
	Accounts() []account.Resource
	Instruments(domain.AccountID) (catalog.Snapshot, error)
	Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error)
	Spot(domain.AccountID) (exchange.SpotClient, error)
	Perp(domain.AccountID) (exchange.PerpClient, error)
	// The route-scoped pair is what every reader below actually wants. These
	// readers walk Accounts(), which emits one Resource per route, so they have
	// already chosen a route by the time they need a client; asking for one by
	// bare account id throws that choice away and cannot be answered at all for
	// an account running two routes of the same product.
	SpotRoute(domain.AccountID, string) (exchange.SpotClient, error)
	PerpRoute(domain.AccountID, string) (exchange.PerpClient, error)
	Capability(domain.AccountID) (capability.Row, error)
	Runtime(domain.AccountID) (account.RuntimeAccount, error)
	RouteStatus(domain.AccountID, string, string) (account.Status, error)
}

type authorityMultiCapabilitySource interface {
	Capabilities(domain.AccountID) ([]capability.Row, error)
}

type managerAuthorityReads struct {
	manager  authorityAccountSource
	profiles *routeprofile.Registry
	// board serves the funding matrix from public endpoints. It is deliberately
	// beside manager rather than behind it: every other read on this type walks
	// reads.manager.Accounts() for a ReadReady route, and the board's whole
	// point is to answer before any account exists.
	board *fundingboard.Source
	now   func() time.Time
}

// clock reports the read's notion of now, in UTC. Tests inject now to make
// observation times deterministic; production leaves it nil.
func (reads managerAuthorityReads) clock() time.Time {
	if reads.now == nil {
		return time.Now().UTC()
	}
	return reads.now().UTC()
}

type opaqueRuntimeLifecycle struct {
	resolve    func(context.Context, secret.OpaqueRef) (string, error)
	activate   func(context.Context, account.DesiredRuntime, string) error
	deactivate func(context.Context, domain.AccountID) error
	blockers   func(context.Context, domain.AccountID) (account.RuntimeBlockers, error)
}

type runtimeBlockerQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type runtimePositionBlockerSource interface {
	RuntimePositionBlockers(context.Context, domain.AccountID) (int, error)
}

// productionRuntimeLifecycle keeps legacy activation closed while snapshot
// route groups own connector construction and teardown.
type productionRuntimeLifecycle struct {
	mu        sync.RWMutex
	querier   runtimeBlockerQuerier
	positions runtimePositionBlockerSource
}

func (lifecycle *productionRuntimeLifecycle) attach(querier runtimeBlockerQuerier, positions runtimePositionBlockerSource) error {
	if lifecycle == nil || querier == nil || positions == nil {
		return errors.New("runtime lifecycle database is unavailable")
	}
	lifecycle.mu.Lock()
	lifecycle.querier = querier
	lifecycle.positions = positions
	lifecycle.mu.Unlock()
	return nil
}

func (*productionRuntimeLifecycle) Validate(context.Context, account.DesiredRuntime) error {
	return errors.New("legacy runtime activation is unavailable")
}

func (*productionRuntimeLifecycle) Activate(context.Context, account.DesiredRuntime) error {
	return errors.New("legacy runtime activation is unavailable")
}

func (*productionRuntimeLifecycle) Deactivate(context.Context, domain.AccountID) error { return nil }

func (lifecycle *productionRuntimeLifecycle) Blockers(ctx context.Context, accountID domain.AccountID) (account.RuntimeBlockers, error) {
	if ctx == nil || accountID.Validate() != nil {
		return account.RuntimeBlockers{}, errors.New("runtime blocker lookup failed")
	}
	lifecycle.mu.RLock()
	querier := lifecycle.querier
	positions := lifecycle.positions
	lifecycle.mu.RUnlock()
	if querier == nil || positions == nil {
		return account.RuntimeBlockers{}, errors.New("runtime blocker lookup failed")
	}
	var openOrders, uncertainCommands int
	if err := querier.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE state IN ('accepted','open','partially_filled')),
			count(*) FILTER (WHERE state IN ('prepared','dispatching','uncertain'))
		FROM orders
		WHERE account_id=$1
	`, accountID).Scan(&openOrders, &uncertainCommands); err != nil {
		return account.RuntimeBlockers{}, errors.New("runtime blocker lookup failed")
	}
	openPositions, err := positions.RuntimePositionBlockers(ctx, accountID)
	if err != nil || openPositions < 0 {
		return account.RuntimeBlockers{}, errors.New("runtime blocker lookup failed")
	}
	return account.RuntimeBlockers{OpenOrders: openOrders, Positions: openPositions, UncertainCommands: uncertainCommands}, nil
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

// authorityOption adjusts the reads assembled by newGatewayAuthority.
//
// It is variadic rather than a fourth parameter because the constructor has
// nineteen call sites, eighteen of them tests that care about desired state and
// nothing else. It is also an option rather than a setter on the returned value
// because reads is read under no lock by every request: fixing it before the
// authority is reachable keeps it immutable afterwards.
type authorityOption func(*managerAuthorityReads)

// withFundingBoard supplies the credential-free source behind the funding
// matrix. A nil source leaves the board answering ErrBoardUnavailable, which is
// what a test-injected account manager should see — an honest gap rather than a
// board that silently has no venues.
func withFundingBoard(board *fundingboard.Source) authorityOption {
	return func(reads *managerAuthorityReads) { reads.board = board }
}

func newGatewayAuthority(ctx context.Context, desiredStore store.DesiredStateStore, manager *account.Manager, options ...authorityOption) (*gatewayAuthority, error) {
	if ctx == nil || desiredStore == nil || manager == nil {
		return nil, errors.New("gateway authority dependencies are incomplete")
	}
	reads := managerAuthorityReads{manager: manager, profiles: routeprofile.DefaultRegistry(), now: time.Now}
	for _, option := range options {
		if option != nil {
			option(&reads)
		}
	}
	authority := &gatewayAuthority{
		store: desiredStore, manager: manager, routeStatus: manager.RouteStatus,
		reads: reads,
		now:   time.Now,
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
	retired, err := authority.snapshots.IsSecretReferenceRetired(ctx, authoritySecretScope(), desired.SecretRef)
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
		if err := authority.manager.RestoreActiveRuntime(ctx, *current.Active); err != nil {
			return store.PromotionResult{}, err
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
	case httpapi.AuthorityListDesired:
		response, err = authority.listDesired(ctx)
	case httpapi.AuthorityGetDesired:
		if authority.snapshots != nil {
			response, err = authority.getRuntimeView(ctx, request.AccountID)
		} else {
			response, err = authority.getDesired(ctx, request.AccountID)
		}
	case httpapi.AuthorityListRuntimeAccounts:
		response.Body = authorityPublicRuntimes(authority.manager.Runtimes())
	case httpapi.AuthorityGetRuntimeAccount:
		if len(request.Query) != 0 {
			err = errors.New("runtime account query is not allowed")
		} else if authority.snapshots != nil {
			response, err = authority.getRuntimeView(ctx, request.AccountID)
		} else {
			response.Body, err = authority.reads.Runtime(request.AccountID)
		}
	case httpapi.AuthorityRuntimeStatus:
		response.Body = authorityRuntimeStatus{Accounts: authorityPublicRuntimes(authority.manager.Runtimes())}
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
	case httpapi.AuthorityCandles:
		query := authorityCandlesQuery{
			Venue: request.Query.Get("venue"), Environment: request.Query.Get("environment"),
			Product: request.Query.Get("product"), InstrumentID: request.Query.Get("instrumentId"),
			Interval: request.Query.Get("interval"),
		}
		if strings.TrimSpace(query.Venue) == "" || strings.TrimSpace(query.Environment) == "" ||
			strings.TrimSpace(query.Product) == "" || strings.TrimSpace(query.InstrumentID) == "" {
			err = errors.New("candles composite identity is required")
			break
		}
		if query.Interval, err = httpapi.PortableCandleInterval(strings.TrimSpace(query.Interval)); err != nil {
			break
		}
		if query.Limit, err = authorityCandleLimit(request.Query.Get("limit")); err != nil {
			break
		}
		var bars authorityCandles
		bars, err = authority.reads.Candles(ctx, query)
		if err == nil && (bars.Venue != query.Venue || bars.Environment != query.Environment ||
			bars.Product != query.Product || bars.InstrumentID != query.InstrumentID || bars.Interval != query.Interval) {
			err = errors.New("candles composite identity mismatch")
		} else {
			response.Body = bars
		}
	case httpapi.AuthorityFundingBoard:
		// No query to validate and no identity to echo back: the board is the
		// whole table this gateway can reach, so there is nothing a caller could
		// ask for that would make one column right and another wrong.
		response.Body, err = authority.reads.FundingBoard(ctx)
	case httpapi.AuthorityFundingHistory:
		query := authorityFundingHistoryQuery{
			Venue:       strings.TrimSpace(request.Query.Get("venue")),
			Environment: strings.TrimSpace(request.Query.Get("environment")),
			Symbol:      strings.TrimSpace(request.Query.Get("symbol")),
		}
		if query.Venue == "" || query.Environment == "" || query.Symbol == "" {
			err = errors.New("funding history composite identity is required")
			break
		}
		if query.Since, err = authorityHistoryBound(request.Query.Get("since")); err != nil {
			break
		}
		if query.Until, err = authorityHistoryBound(request.Query.Get("until")); err != nil {
			break
		}
		var series authorityFundingSeries
		series, err = authority.reads.FundingHistory(ctx, query)
		if err == nil && (series.Venue != query.Venue || series.Environment != query.Environment || series.Symbol != query.Symbol) {
			err = errors.New("funding history composite identity mismatch")
		} else {
			response.Body = series
		}
	case httpapi.AuthorityRuntimeOpenOrders, httpapi.AuthorityRuntimePositions,
		httpapi.AuthorityRuntimeUncertain, httpapi.AuthorityRuntimeEvents,
		httpapi.AuthorityPrivateEvents:
		response.Body = []any{}
	default:
		err = errors.New("unsupported authority operation")
	}
	if err != nil {
		// This is the one place every internal-plane failure passes through, and
		// the last place its cause still exists: authorityError maps the error to
		// a status and a stable code, and the HTTP layer above renders anything
		// unmapped as a bare INTERNAL_ERROR. Without this line a caller sees
		// "request denied" and nothing — not which operation, not which account,
		// not why — which is exactly what made the account-promotion and
		// market-data failures each take a full diagnostic session to place.
		//
		// The cause is safe to log here because it is ours: these errors are
		// built from store sentinels, manager state, and venue responses, none of
		// which carry credential material. The request is not logged, only its
		// operation and account, because request bodies on this plane do.
		slog.Error("gateway internal authority operation failed",
			"operation", request.Operation, "accountId", request.AccountID,
			"operationId", request.OperationID, "cause", err)
		return httpapi.AuthorityResponse{}, authorityError(err)
	}
	return response, nil
}

func (authority *gatewayAuthority) retireDesired(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if request.Retirement == nil || request.AccountID != request.Retirement.AccountID || request.OperationID != request.Retirement.OperationID || request.Retirement.DesiredStateRevision < 2 {
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
		Scope:             authoritySecretScope(),
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
	if request.RotationFinalization == nil ||
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
		Scope: authoritySecretScope(),
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

// authoritySecretScope is the single scope this gateway addresses. It used to
// take the runtime mode, which picked one of two namespaces; there is one
// namespace now, so the scope is a constant and no caller can name material the
// provider would then refuse to serve.
func authoritySecretScope() secretstore.Scope {
	return secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"}
}

func (authority *gatewayAuthority) applyDesired(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if request.DesiredApply == nil || request.AccountID != request.DesiredApply.AccountID || request.OperationID != request.DesiredApply.OperationID {
		return httpapi.AuthorityResponse{}, errors.New("desired runtime request is invalid")
	}
	desired, err := desiredSnapshot(*request.DesiredApply)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if _, err := authority.ApplySnapshot(ctx, desired); err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return authority.getRuntimeView(ctx, request.AccountID)
}

func desiredSnapshot(request httpapi.DesiredApplyRequest) (store.DesiredSnapshot, error) {
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
	// A route profile no longer carries an environment of its own — live is the
	// only one the factory can build — but the request still names one, because
	// it is written into the snapshot and stored beside the account. A request
	// naming anything else describes endpoints nothing can reach, and applying it
	// would leave a row asking for demo behind a runtime that is live.
	if request.NativeEnvironment != string(exchange.EnvironmentLive) {
		return store.DesiredSnapshot{}, errors.New("desired native environment is invalid")
	}
	routes := make([]store.RouteDefinition, 0, len(request.Routes))
	for _, requestedRoute := range request.Routes {
		profile, err := routeprofile.DefaultRegistry().Resolve(request.Venue, requestedRoute.RouteProfileID)
		if err != nil || string(profile.Product) != requestedRoute.Product || profile.SettlementAsset != requestedRoute.SettlementAsset {
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
	if authority.manager == nil {
		view.Readiness = httpapi.RuntimeProjectionReadiness{Ready: true}
		view.WriteReadiness = httpapi.RuntimeProjectionReadiness{Ready: false, Reason: "canonical_writes_not_ready"}
		blocked := "canonical_writes_not_ready"
		view.BlockedReason = &blocked
		for _, route := range active.Routes {
			for _, row := range capability.StaticMatrix().Rows {
				if row.Venue == active.Venue && row.Product == route.Product && row.Settlement == route.SettlementAsset {
					view.Capabilities = append(view.Capabilities, httpapi.RuntimeCapabilityView{RowID: row.RowID, Venue: row.Venue, Product: row.Product, Settlement: row.Settlement, WriteReady: false, Operations: row.Operations})
					break
				}
			}
		}
		return
	}

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
		routeStatus, statusErr := authority.routeStatus(active.AccountID, route.Product, route.SettlementAsset)
		routeReadReady := statusErr == nil && routeStatus.ReadReady
		routeWriteReady := statusErr == nil && routeStatus.WriteReady
		readReady = readReady && routeReadReady
		writeReady = writeReady && routeWriteReady
		if statusErr != nil {
			if readReason == "" {
				readReason = "runtime_status_unavailable"
			}
			if writeReason == "" {
				writeReason = "runtime_status_unavailable"
			}
		} else {
			if !routeReadReady && readReason == "" {
				readReason = routeStatus.Reason
			}
			if !routeWriteReady && writeReason == "" {
				writeReason = routeStatus.Reason
			}
		}
		matched := false
		for _, row := range rows {
			if row.Venue != active.Venue || row.Product != route.Product || row.Settlement != route.SettlementAsset {
				continue
			}
			view.Capabilities = append(view.Capabilities, httpapi.RuntimeCapabilityView{
				RowID: row.RowID, Venue: row.Venue, Product: row.Product, Settlement: row.Settlement,
				WriteReady: routeWriteReady && row.WriteReadyDefault, Operations: row.Operations,
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
		} else if route.RequestedState == store.RouteActive && active != nil && active.Fence == snapshot.Fence {
			routeView.Ready, routeView.Reason = authority.activeRouteLifecycleState(snapshot.AccountID, route)
		}
		view.Routes = append(view.Routes, routeView)
	}
	return view
}

func (authority *gatewayAuthority) activeRouteLifecycleState(accountID domain.AccountID, route store.RouteDefinition) (bool, string) {
	status, err := authority.routeStatus(accountID, route.Product, route.SettlementAsset)
	if err != nil {
		return false, "runtime_status_unavailable"
	}
	ready := status.ReadReady && status.WriteReady
	// A route whose only complaint is the live-write switch is still active: the
	// operator turned writes off, the venue did not turn us away. This used to
	// also require the runtime to be in live mode, back when a test runtime
	// could report the same reason for an entirely different cause.
	if status.ReadReady && status.Reason == account.ErrLiveWriteDisabled.Error() {
		ready = true
	}
	if ready {
		return true, ""
	}
	if status.Reason != "" {
		return false, status.Reason
	}
	return false, "runtime_route_not_ready"
}

func (reads managerAuthorityReads) Venues() []authorityVenue {
	resources := reads.manager.Accounts()
	venues := profileAuthorityVenues(reads.profiles)
	indexes := make(map[string]int, len(venues))
	for index, venue := range venues {
		indexes[venue.Venue+"\x00"+venue.Environment] = index
	}
	for _, resource := range resources {
		key := string(resource.Venue) + "\x00" + string(resource.Environment)
		if index, ok := indexes[key]; ok {
			venues[index].Ready = venues[index].Ready || resource.ReadReady
		}
	}
	return venues
}

// profileAuthorityVenues enumerates the venues the registry knows, one entry
// each.
//
// A venue used to appear once per environment it stood endpoints in, so the
// dedup key carried the environment and the ordering broke ties on it. Only
// live endpoints exist now, which makes a venue name the whole identity. The
// environment stays on the emitted record because the readiness join below is
// keyed on it and the resources it matches against still carry one.
func profileAuthorityVenues(registry *routeprofile.Registry) []authorityVenue {
	if registry == nil {
		registry = routeprofile.DefaultRegistry()
	}
	profiles := registry.Profiles()
	venues := make([]authorityVenue, 0, len(profiles))
	indexes := make(map[string]int, len(profiles))
	for _, profile := range profiles {
		if _, exists := indexes[profile.Venue]; exists {
			continue
		}
		indexes[profile.Venue] = len(venues)
		venues = append(venues, authorityVenue{Venue: profile.Venue, Environment: string(exchange.EnvironmentLive)})
	}
	sort.Slice(venues, func(i, j int) bool { return venues[i].Venue < venues[j].Venue })
	return venues
}

func (reads managerAuthorityReads) Runtime(accountID string) (authorityRuntimeProjection, error) {
	id := domain.AccountID(accountID)
	runtime, err := reads.manager.Runtime(id)
	if err != nil {
		return authorityRuntimeProjection{}, fmt.Errorf("read runtime account: %w", err)
	}
	rows := make([]capability.Row, 0, 1)
	if source, ok := reads.manager.(authorityMultiCapabilitySource); ok {
		rows, err = source.Capabilities(id)
	} else {
		var row capability.Row
		row, err = reads.manager.Capability(id)
		rows = []capability.Row{row}
	}
	if err != nil {
		return authorityRuntimeProjection{}, fmt.Errorf("read runtime capability: %w", err)
	}
	if runtime.Desired.AccountID != id || runtime.Desired.DesiredStateRevision == 0 || runtime.Desired.RuntimeGeneration == 0 || runtime.Desired.CredentialVersion == 0 || runtime.RuntimeVersion == 0 || runtime.LastAppliedDesiredStateRevision == 0 || runtime.LastAppliedRuntimeGeneration == 0 || runtime.ObservedAt.IsZero() || len(rows) == 0 {
		return authorityRuntimeProjection{}, errors.New("runtime projection authority is incomplete")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RowID < rows[j].RowID })
	capabilities := make([]authorityRuntimeCapability, 0, len(rows))
	runtimeReady := runtime.State == account.RuntimeStateActive && runtime.Ready
	readReady := runtimeReady
	writeReady := runtimeReady
	readReason := runtime.Reason
	writeReason := ""
	for _, row := range rows {
		if row.RowID == "" || row.Venue == "" || row.Product == "" {
			return authorityRuntimeProjection{}, errors.New("runtime projection authority is incomplete")
		}
		routeStatus, err := reads.manager.RouteStatus(id, row.Product, row.Settlement)
		if err != nil {
			return authorityRuntimeProjection{}, fmt.Errorf("read runtime route status: %w", err)
		}
		routeReadReady := runtimeReady && routeStatus.ReadReady
		routeWriteReady := routeReadReady && routeStatus.WriteReady
		capabilities = append(capabilities, authorityRuntimeCapability{
			RowID: row.RowID, Venue: row.Venue, Product: row.Product, Settlement: row.Settlement,
			WriteReady: routeWriteReady, Operations: row.Operations,
		})
		readReady = readReady && routeReadReady
		writeReady = writeReady && routeWriteReady
		if !routeReadReady && readReason == "" {
			readReason = routeStatus.Reason
		}
		if !routeWriteReady && writeReason == "" {
			writeReason = routeStatus.Reason
		}
	}
	if !readReady && readReason == "" {
		readReason = "runtime_" + string(runtime.State)
	}
	if !writeReady {
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
		BlockedReason: blocked, Capabilities: capabilities,
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
		observedAt = reads.clock()
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

// FundingBoard serves the cross-venue funding matrix from public endpoints.
//
// It is the one read on this type that never touches reads.manager: the board
// is what an operator looks at before configuring anything, so binding it to a
// ReadReady route would make a fresh install render an empty market.
//
// A nil source is ErrBoardUnavailable rather than an empty board. The two are
// indistinguishable on the wire otherwise, and "every venue is down" is a
// conclusion an operator would act on.
func (reads managerAuthorityReads) FundingBoard(ctx context.Context) (authorityFundingBoard, error) {
	if reads.board == nil {
		return authorityFundingBoard{}, fundingboard.ErrBoardUnavailable
	}
	snapshot, err := reads.board.Snapshot(ctx)
	if err != nil {
		return authorityFundingBoard{}, err
	}
	board := authorityFundingBoard{
		ObservedAt: snapshot.ObservedAt,
		Venues:     make([]authorityFundingVenue, 0, len(snapshot.Venues)),
	}
	for _, venue := range snapshot.Venues {
		column := authorityFundingVenue{
			Venue: venue.Venue, Environment: venue.Environment, Product: venue.Product,
			Settlement: venue.Settlement, RouteProfileID: venue.RouteProfileID,
			ObservedAt: venue.ObservedAt, Stale: venue.Stale, Unavailable: venue.Unavailable,
			Rows: make([]authorityFundingRow, 0, len(venue.Entries)),
		}
		for _, entry := range venue.Entries {
			row := authorityFundingRow{
				Symbol: entry.Symbol, NativeSymbol: entry.NativeSymbol, NativeID: entry.NativeID,
				BaseAsset: entry.BaseAsset, QuoteAsset: entry.QuoteAsset,
				Rate: entry.Rate.String(), NextFundingTime: entry.NextFundingTime,
			}
			if entry.IntervalHours.Valid {
				row.IntervalHours = entry.IntervalHours.Value
			}
			if entry.MarkPrice.Valid {
				row.MarkPrice = entry.MarkPrice.Value.String()
			}
			if entry.IndexPrice.Valid {
				row.IndexPrice = entry.IndexPrice.Value.String()
			}
			if entry.OpenInterest.Valid {
				row.OpenInterest = entry.OpenInterest.Value.String()
			}
			column.Rows = append(column.Rows, row)
		}
		board.Venues = append(board.Venues, column)
	}
	return board, nil
}

// FundingHistory serves one instrument's settled series from the same public
// source the board is served from.
//
// It deliberately does not walk reads.manager.Accounts() the way MarketData and
// Candles do. Those read a venue through a configured account's authenticated
// client; this reads the same public endpoints the board already polls, so a
// chart draws on a gateway with no accounts at all — which is the state an
// operator comparing venues before funding one is actually in.
func (reads managerAuthorityReads) FundingHistory(ctx context.Context, query authorityFundingHistoryQuery) (authorityFundingSeries, error) {
	if reads.board == nil {
		return authorityFundingSeries{}, fundingboard.ErrBoardUnavailable
	}
	snapshot, err := reads.board.History(ctx, fundingboard.HistoryQuery{
		Venue: query.Venue, Environment: query.Environment, Symbol: query.Symbol,
		Since: query.Since, Until: query.Until,
	})
	if err != nil {
		return authorityFundingSeries{}, err
	}
	series := authorityFundingSeries{
		Venue: query.Venue, Environment: query.Environment, Symbol: query.Symbol,
		Since: query.Since, Until: query.Until, ObservedAt: snapshot.ObservedAt,
		Points: make([]authorityFundingSeriesPoint, 0, len(snapshot.Points)),
	}
	for _, point := range snapshot.Points {
		converted := authorityFundingSeriesPoint{FundingTime: point.FundingTime, Rate: point.Rate.String()}
		if point.IntervalHours.Valid {
			converted.IntervalHours = point.IntervalHours.Value
		}
		series.Points = append(series.Points, converted)
	}
	return series, nil
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
		if !routeSettlesInstrument(resource, instrument) {
			continue
		}
		result := authorityMarketData{Venue: venue, Environment: environment, Product: product, InstrumentID: instrumentID}
		var market exchange.MarketREST
		switch resource.Product {
		case exchange.ProductSpot:
			market, err = reads.manager.SpotRoute(resource.AccountID, resource.Settlement)
		case exchange.ProductPerp:
			var client exchange.PerpClient
			client, err = reads.manager.PerpRoute(resource.AccountID, resource.Settlement)
			market = client
			if err == nil {
				row, capabilityErr := routeCapabilityForMarketData(reads.manager, resource)
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
					// Only the observation instant is required. The two schedule
					// timestamps are omitempty in exchange.FundingRate because a
					// venue is allowed not to send them, and most do not: of the
					// nine perp venues only OKX returns a last-funding time at
					// all, and Hyperliquid and Lighter return neither. Demanding
					// all three made this read fail permanently for eight of the
					// nine — a fact about those endpoints' response shapes, not
					// about the market. Same reasoning as the order book's
					// optional venue clock below; a missing schedule stays zero
					// and travels as "unknown".
					if result.FundingObservedAt.IsZero() {
						return authorityMarketData{}, errors.New("current funding observation time is unavailable")
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
		if len(book.Bids) == 0 || len(book.Asks) == 0 || len(book.Bids) > authorityDepthLimit || len(book.Asks) > authorityDepthLimit {
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
		// A venue that timestamps its own book is believed over our clock, and the
		// latest observation gathered so far wins. But exchange.OrderBook declares
		// Time omitempty because a venue is allowed not to send one — Binance
		// spot's depth endpoint returns lastUpdateId, bids and asks and nothing
		// else — and a REST snapshot is still observed at a definite instant: the
		// moment we read it. Requiring a venue timestamp rejected every Binance
		// spot book outright, which is a fact about that endpoint's response
		// shape, not about the market. Our clock is only the fallback, never an
		// override, so every path that already had a real venue time is unchanged.
		if book.Time.IsZero() {
			if result.ObservedAt.IsZero() {
				result.ObservedAt = reads.clock()
			}
		} else if result.ObservedAt.IsZero() || book.Time.After(result.ObservedAt) {
			result.ObservedAt = book.Time.UTC()
		}
		if result.ObservedAt.IsZero() {
			return authorityMarketData{}, errors.New("market-data observation time is unavailable")
		}
		return result, nil
	}
	return authorityMarketData{}, errors.New("configured market-data capability is unavailable")
}

// Candles answers the chart's read.
//
// It resolves an account the way MarketData does — scan for the first
// read-ready route on the requested venue/environment/product that can resolve
// the instrument — because the internal plane is keyed by the instrument, not
// by an account: the browser asks for BTCUSDT bars, not for one account's view
// of them. Candles are public data, so any read-ready route serves.
//
// Unlike MarketData this does not fall through to the next account when the
// venue call fails. Order-book depth is a per-route capability and a route that
// cannot serve it is worth skipping; Candles is grade A on every supported
// venue, so a failure here is the venue refusing, and retrying the same refusal
// against a sibling route would just multiply the latency before the same
// error.
func (reads managerAuthorityReads) Candles(ctx context.Context, query authorityCandlesQuery) (authorityCandles, error) {
	query.Venue = strings.TrimSpace(query.Venue)
	query.Environment = strings.TrimSpace(query.Environment)
	query.Product = strings.TrimSpace(query.Product)
	query.InstrumentID = strings.TrimSpace(query.InstrumentID)
	query.Interval = strings.TrimSpace(query.Interval)
	if query.Venue == "" || query.Environment == "" || query.InstrumentID == "" || query.Interval == "" ||
		(query.Product != string(exchange.ProductSpot) && query.Product != string(exchange.ProductPerp)) {
		return authorityCandles{}, errors.New("candles venue, environment, product, instrumentId, and interval are required")
	}
	if query.Limit <= 0 || query.Limit > authorityCandleLimitMax {
		query.Limit = authorityCandleLimitMax
	}
	for _, resource := range reads.manager.Accounts() {
		if string(resource.Venue) != query.Venue || string(resource.Environment) != query.Environment ||
			string(resource.Product) != query.Product || !resource.ReadReady {
			continue
		}
		instrument, err := reads.manager.Resolve(ctx, resource.AccountID, domain.InstrumentID(query.InstrumentID))
		if err != nil {
			continue
		}
		if !routeSettlesInstrument(resource, instrument) {
			continue
		}
		var market exchange.MarketREST
		switch resource.Product {
		case exchange.ProductSpot:
			market, err = reads.manager.SpotRoute(resource.AccountID, resource.Settlement)
		case exchange.ProductPerp:
			var client exchange.PerpClient
			client, err = reads.manager.PerpRoute(resource.AccountID, resource.Settlement)
			market = client
		default:
			continue
		}
		if err != nil || market == nil {
			continue
		}
		page, err := market.Candles(ctx, exchange.CandlesRequest{
			Instrument: instrument, Interval: query.Interval, Limit: query.Limit,
		})
		if err != nil {
			return authorityCandles{}, fmt.Errorf("read candles: %w", err)
		}
		result := authorityCandles{
			Venue: query.Venue, Environment: query.Environment, Product: query.Product,
			InstrumentID: query.InstrumentID, Interval: query.Interval,
			Candles: make([]authorityCandle, len(page.Candles)),
		}
		for index, candle := range page.Candles {
			if err := httpapi.ValidCandle(candle); err != nil {
				return authorityCandles{}, fmt.Errorf("read candles: %w", err)
			}
			// Bars arrive in ascending open-time order and the last one is the
			// live, still-open interval, so its open time is the freshest thing
			// the venue told us. It is not a venue clock — no venue timestamps a
			// candle page — but it bounds staleness the same way, and the trading
			// service refuses a projection whose observation time is missing.
			if candle.OpenTime.After(result.ObservedAt) {
				result.ObservedAt = candle.OpenTime.UTC()
			}
			result.Candles[index] = authorityCandle{
				OpenTime: candle.OpenTime.UTC(), CloseTime: candle.CloseTime.UTC(),
				Open: candle.Open.String(), High: candle.High.String(), Low: candle.Low.String(),
				Close: candle.Close.String(), Volume: candle.Volume.String(), Complete: candle.Complete,
			}
		}
		if result.ObservedAt.IsZero() {
			result.ObservedAt = reads.clock()
		}
		return result, nil
	}
	return authorityCandles{}, errors.New("configured candles capability is unavailable")
}

// authorityCandleLimit parses the caller's bound. An absent limit is the
// default rather than an error: the chart's first paint wants "a screenful",
// and naming a number is the caller narrowing that, not supplying something
// missing.
func authorityCandleLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return authorityCandleLimitMax, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > authorityCandleLimitMax {
		return 0, errors.New("candles limit is invalid")
	}
	return limit, nil
}

// authorityHistoryBound parses one end of a funding-history window.
//
// RFC 3339 with an explicit offset, not epoch milliseconds, because this bound
// is written by a browser, narrowed by a product service, and finally handed to
// a venue in whatever unit that venue wants. A bare integer that lost its unit
// somewhere along the way still parses — into a different decade — and the only
// symptom is an empty chart.
func authorityHistoryBound(raw string) (time.Time, error) {
	bound, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, errors.New("funding history since and until must be RFC 3339 timestamps")
	}
	return bound.UTC(), nil
}

// routeSettlesInstrument reports whether a route can trade an instrument at
// all. Accounts() emits one Resource per route and the catalog resolves an
// instrument for the whole account, so a venue that settles one product in
// several assets — bybit lists both USDT and USDC perpetuals — hands every
// route every instrument. Pairing them blindly asks a USDC route for a USDT
// contract, which the exchange layer rejects outright ("instrument quote must
// match USDC settlement"), and because that rejection is fatal rather than
// skipped it took the whole venue down instead of the one wrong route.
//
// Registration already guarantees the two agree when both are known: the
// catalog only files an instrument under a scope whose settlement equals the
// instrument's settle asset. An unknown settlement on either side is left
// alone — it carries no information to disqualify the route with, so the read
// tries it exactly as it always has.
func routeSettlesInstrument(resource account.Resource, instrument exchange.Instrument) bool {
	settleAsset := instrument.Identity.SettleAsset
	if resource.Settlement == "" || settleAsset == "" {
		return true
	}
	return resource.Settlement == settleAsset
}

func routeCapabilityForMarketData(manager authorityAccountSource, resource account.Resource) (capability.Row, error) {
	source, ok := manager.(authorityMultiCapabilitySource)
	if !ok {
		return capability.Row{}, errors.New("multi-route capability authority is unavailable")
	}
	rows, err := source.Capabilities(resource.AccountID)
	if err != nil {
		return capability.Row{}, err
	}
	for _, row := range rows {
		if row.Product == string(resource.Product) && row.Settlement == resource.Settlement {
			return row, nil
		}
	}
	return capability.Row{}, errors.New("route capability is unavailable")
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
		SecretRef: ref, DesiredStateRevision: uint64(desired.DesiredStateRevision),
		RuntimeGeneration: uint64(desired.RuntimeGeneration), CredentialVersion: uint64(desired.CredentialVersion),
		RequestedState: state, RotationFence: rotationFence,
	}, nil
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
	case errors.Is(err, account.ErrLiveWriteDisabled):
		return httpapi.AuthorityError{Status: http.StatusForbidden, Code: "LIVE_PRIVATE_WRITE_DISABLED"}
	case errors.Is(err, fundingboard.ErrBoardUnavailable):
		// A table where every venue failed is a snapshot, not an error, so
		// reaching here means the source itself could not be built or has been
		// closed. That is a gateway configuration fault the caller may retry
		// through, not a 500 the caller caused.
		return httpapi.AuthorityError{Status: http.StatusServiceUnavailable, Code: "FUNDING_BOARD_UNAVAILABLE", Retryable: true}
	case errors.Is(err, fundingboard.ErrHistoryUnsupported):
		// Not retryable and not a 503: nado publishes no settled series and
		// never will, and neither does a venue the board does not carry. A
		// retryable code here would have the caller poll forever for a chart
		// that cannot exist, instead of saying so beside the leg that drew.
		return httpapi.AuthorityError{Status: http.StatusNotFound, Code: "FUNDING_HISTORY_UNSUPPORTED"}
	case errors.Is(err, fundingboard.ErrHistoryUnknownInstrument):
		return httpapi.AuthorityError{Status: http.StatusNotFound, Code: "FUNDING_HISTORY_INSTRUMENT_UNKNOWN"}
	case errors.Is(err, fundingboard.ErrHistoryWindow):
		return httpapi.AuthorityError{Status: http.StatusBadRequest, Code: "FUNDING_HISTORY_WINDOW_UNSUPPORTED"}
	default:
		return httpapi.AuthorityError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Retryable: true}
	}
}

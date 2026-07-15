// Package reconcile brings the local Cache back into agreement with the venue's
// authoritative state. The local cache can diverge after a websocket gap or a
// process restart; the reconciler pulls REST snapshots and applies corrections.
// It is venue-neutral (works through the contract interfaces) so the same logic
// runs against any adapter.
package reconcile

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/latency"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
	"github.com/shopspring/decimal"
)

// Report summarizes what a reconciliation pass changed.
type Report struct {
	AccountStatesApplied int
	BalancesUpdated      int
	PositionsUpdated     int // deprecated: position reports no longer mutate cache directly
	PositionsCleared     int // deprecated: position reports no longer clear cache directly
	PositionOverwrites   int // deprecated: mismatches are emitted as blocking findings

	OrdersUpdated       int // open orders in both cache and venue, refreshed to venue truth
	OrdersExternal      int // open orders the venue reports that the cache had never seen
	OrdersClosedUnknown int // cache-open orders absent from venue open snapshot; close reason unproven
	OrdersCleared       int // deprecated: retained for older callers; ambiguous closes are not marked Canceled
	OrdersMaterialized  int

	FillsApplied   int
	FillsDuplicate int
	FillsInferred  int

	Partial          bool
	FillsPartial     bool
	CursorsCommitted int
	Warnings         []model.ReportWarning
	Findings         []Finding

	OpenOrdersCoverage model.ReportCoverage
	FillsCoverage      model.ReportCoverage
	PositionsCoverage  model.ReportCoverage
}

// ActivationVerdict is the runtime-facing safety decision produced from a
// reconciliation report. Safe is false only when the report contains evidence
// that can hide fills, open orders, or another explicitly blocking condition;
// Partial alone is not enough because several venues intentionally return an
// authoritative open-orders-only snapshot.
type ActivationVerdict struct {
	Safe   bool
	Reason string
}

// ActivationVerdict returns whether trading may be activated after this pass.
func (r Report) ActivationVerdict() ActivationVerdict {
	// An account-only reconciliation has no execution evidence to assess. Every
	// report produced from an execution mass status carries explicit coverage.
	if r.OpenOrdersCoverage.State != model.CoverageUnknown ||
		r.FillsCoverage.State != model.CoverageUnknown ||
		r.PositionsCoverage.State != model.CoverageUnknown {
		if r.FillsPartial {
			return ActivationVerdict{Reason: "reconciliation fill cursor continuity is incomplete"}
		}
		if r.OpenOrdersCoverage.State != model.CoverageComplete {
			return ActivationVerdict{Reason: "reconciliation open-order evidence is incomplete"}
		}
		if r.FillsCoverage.State != model.CoverageComplete && r.FillsCoverage.State != model.CoverageNotRequested {
			return ActivationVerdict{Reason: "reconciliation fill history is incomplete"}
		}
		if r.PositionsCoverage.State != model.CoverageComplete && r.PositionsCoverage.State != model.CoverageNotRequested {
			return ActivationVerdict{Reason: "reconciliation position evidence is incomplete"}
		}
	}
	for _, finding := range r.Findings {
		if finding.Blocking || finding.Severity == FindingBlocking {
			reason := strings.TrimSpace(finding.Message)
			if reason == "" {
				reason = strings.TrimSpace(finding.Code)
			}
			if reason == "" {
				reason = "reconciliation has an unresolved blocking finding"
			}
			return ActivationVerdict{Reason: reason}
		}
	}
	return ActivationVerdict{Safe: true}
}

// Reconciler pulls authoritative snapshots and reconciles local state. The
// account client supplies balances and optional position evidence; the
// execution client supplies order/fill/position reports. Position evidence is
// compared without a cache-only overwrite.
type Reconciler struct {
	account                contract.AccountClient
	accountCapabilities    contract.Capabilities
	accountCapabilitiesSet bool
	orders                 contract.ExecutionClient
	cache                  *cache.Cache
	clk                    clock.Clock
	latency                latency.Recorder
	state                  StateStore
	fills                  map[string]string
	// fillIdentities retains the order aliases first observed for a venue trade
	// identity. A venue trade ID reused for a different order is a data conflict,
	// not a second fill.
	fillIdentities map[string]fillOrderIdentity
	fillOrder      []string
	fillLimit      int
	passFills      map[string]string
	// overlapFills retains every successfully applied or recognized fill from
	// the immediately preceding venue report. It makes the deliberate cursor
	// overlap safe even when one report contains more identities than the
	// bounded long-lived fill index can retain.
	overlapFills map[string]string
	// observedFills is nil until a valid mass-status fill report starts being
	// processed. A complete, cursor-committed pass may replace overlapFills;
	// incomplete outcomes merge into the prior overlap so unprocessed identities
	// cannot be forgotten.
	observedFills          map[string]string
	overlapLimit           int
	observedNewFills       int
	replaceOverlapOnFinish bool
	pending                map[string]pendingAppliedFill
	accountID              string
	resolver               interface {
		ResolveInFlight(clientID, venueOrderID string, at time.Time)
	}
	orderResolver interface {
		ResolveOrderInFlight(order model.Order, at time.Time) bool
	}
	fillResolver interface {
		ResolveFillInFlight(fill model.Fill, at time.Time) (model.Fill, bool)
	}
	fillMatcher interface {
		MatchFillInFlight(fill model.Fill) (model.Fill, bool)
	}
	fillApplier func(model.Fill, contract.EventMeta) FillApplyResult
	fillSeeder  func(model.Fill)
}

// defaultFillRetentionLimit is the completed-fill idempotency horizon. Pending
// fills whose durable record has not yet been written live outside this window
// and are never evicted.
const defaultFillRetentionLimit = 100_000

// defaultFillOverlapLimit independently bounds the exact, pass-to-pass
// overlap set. If incomplete coverage would require retaining more identities,
// reconciliation fails closed before applying the identity it cannot retain.
const defaultFillOverlapLimit = 100_000

// defaultCursorOverlap deliberately re-queries a short portion of the last
// successful fill window. Venue timestamps and REST visibility are not an
// atomic watermark: a fill may become visible just after a pass while carrying
// a timestamp just before that pass's Until. Fill-key deduplication makes this
// bounded overlap safe.
const defaultCursorOverlap = time.Minute

// derivativeFillOrderPrefetchLimit bounds read-only exact-order fan-out within
// one reconciliation pass. It is a fallback for orders absent from the mass
// report, not a replacement for adapter-side batch history recovery.
const derivativeFillOrderPrefetchLimit = 4

// FillApplyResult lets the node-owned fill path distinguish a new application
// from a previously applied live/recovered fill and from an unmatched fill.
type FillApplyResult uint8

const (
	FillApplyUnmatched FillApplyResult = iota
	FillApplyApplied
	FillApplyDuplicate
	FillApplyConflict
)

type pendingAppliedFill struct {
	pass      PassHeader
	meta      contract.EventMeta
	fill      model.Fill
	appliedAt time.Time
}

type fillOrderIdentity struct {
	clientID     string
	venueOrderID string
}

type orderReportSnapshot struct {
	order          model.Order
	baseline       model.Order
	baselineFilled decimal.Decimal
}

type orderIdentityKey struct {
	accountID  string
	instrument string
	namespace  string
	id         string
}

type recognizedFillSet map[orderIdentityKey]map[string]model.Fill

type fillPresenceKey struct {
	accountID     string
	anyAccount    bool
	instrument    model.InstrumentID
	anyInstrument bool
	namespace     string
	id            string
}

type fillPresenceIndex map[fillPresenceKey]struct{}

type derivativeFillOrderPrefetchKey struct {
	accountID  string
	instrument model.InstrumentID
	namespace  string
	id         string
}

type derivativeFillOrderPrefetchRequest struct {
	key   derivativeFillOrderPrefetchKey
	fills []model.Fill
}

// New builds a Reconciler. account drives balance and fallback position-report
// reconciliation; orders drives mass-status reconciliation. Either may be nil.
// When account is non-nil, callers must install its generic contract with
// WithAccountCapabilities before Run so snapshot venue provenance is explicit.
func New(account contract.AccountClient, orders contract.ExecutionClient, c *cache.Cache) *Reconciler {
	return &Reconciler{
		account:        account,
		orders:         orders,
		cache:          c,
		clk:            clock.NewRealClock(),
		state:          noopStateStore{},
		fills:          make(map[string]string),
		fillIdentities: make(map[string]fillOrderIdentity),
		fillLimit:      defaultFillRetentionLimit,
		overlapLimit:   defaultFillOverlapLimit,
		pending:        make(map[string]pendingAppliedFill),
	}
}

// WithClock installs the runtime clock used for request-start observation
// watermarks and all reconciliation-owned application timestamps.
func (r *Reconciler) WithClock(clk clock.Clock) *Reconciler {
	if clk != nil {
		r.clk = clk
	}
	return r
}

func (r *Reconciler) now() time.Time {
	if r.clk == nil {
		return time.Now()
	}
	return r.clk.Now()
}

func (r *Reconciler) WithLatencyRecorder(rec latency.Recorder) *Reconciler {
	r.latency = rec
	return r
}

func (r *Reconciler) WithStateStore(store StateStore) *Reconciler {
	if store != nil {
		r.state = store
	}
	return r
}

func (r *Reconciler) WithAccountID(accountID string) *Reconciler {
	r.accountID = accountID
	return r
}

// WithAccountCapabilities installs the account client's generic product and
// report contract. The mandatory AccountState snapshot path never discovers
// capabilities itself; callers provide them once when constructing the
// runtime so capability flags cannot select or bypass authoritative state.
func (r *Reconciler) WithAccountCapabilities(caps contract.Capabilities) *Reconciler {
	r.accountCapabilities = caps
	r.accountCapabilitiesSet = true
	return r
}

func (r *Reconciler) WithInFlightResolver(resolver interface {
	ResolveInFlight(clientID, venueOrderID string, at time.Time)
}) *Reconciler {
	r.resolver = resolver
	if orderResolver, ok := resolver.(interface {
		ResolveOrderInFlight(order model.Order, at time.Time) bool
	}); ok {
		r.orderResolver = orderResolver
	} else {
		r.orderResolver = nil
	}
	if fillResolver, ok := resolver.(interface {
		ResolveFillInFlight(fill model.Fill, at time.Time) (model.Fill, bool)
	}); ok {
		r.fillResolver = fillResolver
	} else {
		r.fillResolver = nil
	}
	if matcher, ok := resolver.(interface {
		MatchFillInFlight(fill model.Fill) (model.Fill, bool)
	}); ok {
		r.fillMatcher = matcher
	} else {
		r.fillMatcher = nil
	}
	return r
}

// WithFillApplier routes recovered fills through the runtime's canonical fill
// application path. Standalone reconciler users may omit it and retain the
// cache-only behavior used by lower-level reconciliation tests.
func (r *Reconciler) WithFillApplier(apply func(model.Fill, contract.EventMeta) FillApplyResult) *Reconciler {
	r.fillApplier = apply
	return r
}

// WithFillSeeder installs the node-owned idempotency-index hook used during
// journal replay. Seeding never applies business state or emits callbacks.
func (r *Reconciler) WithFillSeeder(seed func(model.Fill)) *Reconciler {
	r.fillSeeder = seed
	return r
}

// WithFillRetentionLimit bounds the long-lived completed-fill index to the
// most recent limit distinct fill keys. The exact immediately-overlapping
// report set has its own fixed safety cap, and pending durability work is never
// evicted. A non-positive limit leaves the current conservative limit unchanged.
func (r *Reconciler) WithFillRetentionLimit(limit int) *Reconciler {
	if limit <= 0 {
		return r
	}
	r.fillLimit = limit
	r.trimAppliedFills()
	r.pruneFillIdentities()
	return r
}

func (r *Reconciler) rememberAppliedFill(fillKey, recordID string) {
	if r.passFills != nil {
		if previous, exists := r.passFills[fillKey]; !exists || recordID != "" || previous == "" {
			r.passFills[fillKey] = recordID
		}
	}
	if _, exists := r.fills[fillKey]; exists {
		r.fills[fillKey] = recordID
		return
	}
	r.fills[fillKey] = recordID
	r.fillOrder = append(r.fillOrder, fillKey)
	r.trimAppliedFills()
}

func (r *Reconciler) trimAppliedFills() {
	for len(r.fills) > r.fillLimit && len(r.fillOrder) > 0 {
		oldest := r.fillOrder[0]
		r.fillOrder = r.fillOrder[1:]
		delete(r.fills, oldest)
	}
}

func (r *Reconciler) pruneFillIdentities() {
	for fillKey := range r.fillIdentities {
		if _, retained := r.fills[fillKey]; retained {
			continue
		}
		if _, retained := r.overlapFills[fillKey]; retained {
			continue
		}
		if _, retained := r.passFills[fillKey]; retained {
			continue
		}
		if _, retained := r.observedFills[fillKey]; retained {
			continue
		}
		if _, retained := r.pending[fillKey]; retained {
			continue
		}
		delete(r.fillIdentities, fillKey)
	}
}

func (r *Reconciler) beginFillPass() {
	r.passFills = make(map[string]string, len(r.fills)+len(r.overlapFills))
	for fillKey, recordID := range r.fills {
		r.passFills[fillKey] = recordID
	}
	for fillKey, recordID := range r.overlapFills {
		if previous, exists := r.passFills[fillKey]; !exists || recordID != "" || previous == "" {
			r.passFills[fillKey] = recordID
		}
	}
	r.observedFills = nil
	r.observedNewFills = 0
	r.replaceOverlapOnFinish = false
}

func (r *Reconciler) finishFillPass() {
	if r.observedFills != nil {
		if r.replaceOverlapOnFinish {
			r.overlapFills = r.observedFills
		} else {
			if r.overlapFills == nil {
				r.overlapFills = make(map[string]string, len(r.observedFills))
			}
			for fillKey, recordID := range r.observedFills {
				if previous, exists := r.overlapFills[fillKey]; !exists || recordID != "" || previous == "" {
					r.overlapFills[fillKey] = recordID
				}
			}
		}
	}
	r.passFills = nil
	r.observedFills = nil
	r.observedNewFills = 0
	r.replaceOverlapOnFinish = false
	r.pruneFillIdentities()
}

func (r *Reconciler) rememberObservedFill(fillKey, recordID string) error {
	if r.observedFills == nil {
		return nil
	}
	if previous, exists := r.observedFills[fillKey]; exists {
		if recordID != "" || previous == "" {
			r.observedFills[fillKey] = recordID
		}
		return nil
	}
	if err := r.ensureObservedFillCapacity(fillKey); err != nil {
		return err
	}
	if _, retained := r.overlapFills[fillKey]; !retained {
		r.observedNewFills++
	}
	r.observedFills[fillKey] = recordID
	return nil
}

func (r *Reconciler) ensureObservedFillCapacity(fillKey string) error {
	if r.observedFills == nil {
		return nil
	}
	if _, observed := r.observedFills[fillKey]; observed {
		return nil
	}
	if _, retained := r.overlapFills[fillKey]; retained {
		return nil
	}
	if len(r.overlapFills)+r.observedNewFills >= r.overlapLimit {
		return fmt.Errorf("reconcile: fill overlap retention capacity %d exhausted", r.overlapLimit)
	}
	return nil
}

func (r *Reconciler) ensurePassFillCapacity(fillKey string) error {
	if r.passFills == nil {
		return nil
	}
	if _, retained := r.passFills[fillKey]; retained {
		return nil
	}
	maxInt := int(^uint(0) >> 1)
	limit := maxInt
	if r.fillLimit <= maxInt-r.overlapLimit {
		limit = r.fillLimit + r.overlapLimit
	}
	if len(r.passFills) >= limit {
		return fmt.Errorf("reconcile: fill pass retention capacity %d exhausted", limit)
	}
	return nil
}

func (r *Reconciler) rememberOverlapFill(fillKey, recordID string) error {
	if previous, exists := r.overlapFills[fillKey]; exists {
		if recordID != "" || previous == "" {
			r.overlapFills[fillKey] = recordID
		}
		return nil
	}
	if len(r.overlapFills) >= r.overlapLimit {
		return fmt.Errorf("reconcile: fill overlap retention capacity %d exhausted", r.overlapLimit)
	}
	if r.overlapFills == nil {
		r.overlapFills = make(map[string]string)
	}
	r.overlapFills[fillKey] = recordID
	return nil
}

// Run performs one reconciliation pass. Balance snapshots and order reports are
// applied to their canonical local paths; position reports are compared without
// silently mutating cache state. Position mismatches remain blocking until a
// fill/account event can explain and apply the difference. Open orders are
// adopted when the venue reports an order the cache never saw,
// refreshing known ones, and marking cache-open orders missing from the venue
// open snapshot as closed with unknown reason). Intended to be called at startup
// and after every reconnect.
func (r *Reconciler) Run(ctx context.Context) (Report, error) {
	cmd := latency.CommandLatency{Command: string(latency.ChainReconciliation), StartedAt: r.now()}
	defer func() {
		cmd.Finish(r.now())
		if r.latency != nil {
			r.latency.RecordCommandLatency(cmd)
		}
	}()
	var rep Report

	if r.account != nil {
		if err := r.reconcileAccount(ctx, &rep); err != nil {
			cmd.Err = err.Error()
			return rep, err
		}
	}
	if r.orders != nil {
		if err := r.reconcileOrders(ctx, &rep); err != nil {
			cmd.Err = err.Error()
			return rep, err
		}
	}
	return rep, nil
}

func (r *Reconciler) reconcileAccount(ctx context.Context, rep *Report) error {
	scopeAccountID := r.accountID
	state, accountStateAppliedAt, err := r.applyAccountStateSnapshot(ctx, scopeAccountID, r.accountCapabilities.Venue)
	if err != nil {
		return err
	}
	if scopeAccountID == "" {
		scopeAccountID = state.AccountID
	}
	rep.AccountStatesApplied++
	rep.BalancesUpdated += len(state.Balances)

	// Position reconciliation is report/event based. When there is no execution
	// client to provide a mass-status position report, normalize the account
	// snapshot into the same comparison path. Never overwrite or clear the cache
	// here: doing so would leave the node-owned portfolio/PnL/callback state
	// behind the cache.
	if r.orders == nil && r.accountCapabilitiesSet && r.accountCapabilities.Reports.PositionReports {
		positions, err := r.account.Positions(ctx)
		if err != nil {
			return err
		}
		reports := positionReportsFromSnapshot(r.accountCapabilities.Venue, scopeAccountID, state.TsEvent, positions)
		stableAt := latestPositionReportAt(reports)
		pass := positionPass(r.accountCapabilities.Venue, scopeAccountID, stableAt, r.now())
		openFindings, err := r.state.LoadOpenFindings(ctx, pass.Scope)
		if err != nil {
			return err
		}
		for _, finding := range openFindings {
			appendFindingUnique(rep, finding)
		}
		if err := r.state.BeginPass(ctx, pass); err != nil {
			return err
		}
		unresolved, err := r.reconcilePositionReports(ctx, pass, reports, r.accountCapabilities, nil, rep)
		if err != nil {
			return err
		}
		if err := r.resolvePositionFindings(ctx, rep, openFindings, pass, unresolved); err != nil {
			return err
		}
	}
	r.cache.MarkAccountReconciled(state.AccountID, accountStateAppliedAt)
	return nil
}

func (r *Reconciler) applyAccountStateSnapshot(ctx context.Context, scopeAccountID, expectedVenue string) (model.AccountState, time.Time, error) {
	state, err := r.account.AccountState(ctx)
	if err != nil {
		return model.AccountState{}, time.Time{}, err
	}
	if state.AccountID == "" {
		return model.AccountState{}, time.Time{}, fmt.Errorf("reconcile: authoritative account state account id is required")
	}
	if scopeAccountID != "" && state.AccountID != scopeAccountID {
		return model.AccountState{}, time.Time{}, fmt.Errorf("reconcile: account state account id %q does not match runtime account id %q", state.AccountID, scopeAccountID)
	}
	if strings.TrimSpace(expectedVenue) == "" {
		return model.AccountState{}, time.Time{}, fmt.Errorf("reconcile: account client capability venue is required")
	}
	if state.Venue != expectedVenue {
		return model.AccountState{}, time.Time{}, fmt.Errorf("reconcile: account state venue %q does not match account client venue %q", state.Venue, expectedVenue)
	}
	appliedAt := r.now()
	if err := r.cache.ApplyAccountStateAt(state, appliedAt); err != nil {
		return model.AccountState{}, time.Time{}, err
	}
	return state, appliedAt, nil
}

// reconcileOrders rebuilds open-order state from the venue's authoritative mass
// status report: the venue's open set is adopted wholesale (catching orders
// placed out-of-band), and any order the cache still treats as open but the
// venue no longer lists is closed locally with unknown reason. A missing order
// is no longer resting, but this bounded pass must not claim a cancel or a fill
// until trade reconciliation can prove that terminal reason.
func (r *Reconciler) reconcileOrders(ctx context.Context, rep *Report) error {
	r.beginFillPass()
	defer r.finishFillPass()
	appliedEventRecordIDs, err := r.flushPendingAppliedFills(ctx)
	if err != nil {
		return err
	}
	query, priorCursor, err := r.massStatusQuery(ctx)
	if err != nil {
		return err
	}
	// Freeze every local identity that could be resolved by absence immediately
	// before the authoritative request. Neither cache nor finding state is
	// re-read later to expand this candidate set.
	requestScope := ScopeKey{Venue: query.Venue, AccountID: query.AccountID}
	openFindings, err := r.state.LoadOpenFindings(ctx, requestScope)
	if err != nil {
		return err
	}
	openFindings = append([]Finding(nil), openFindings...)
	orderCandidates := r.cache.OpenOrders()
	mass, err := r.orders.GenerateExecutionMassStatus(ctx, query)
	if err != nil {
		return err
	}
	if mass == nil {
		return fmt.Errorf("reconcile: execution mass status is nil")
	}
	if err := validateMassBeforePositionFallback(mass, query); err != nil {
		return err
	}
	r.tryPositionFallback(ctx, mass, query)
	if err := mass.ValidateFor(query); err != nil {
		return err
	}
	stableEventAt := mass.OpenOrdersCoverage.Scope.Through
	if stableEventAt.IsZero() {
		stableEventAt = query.Until
	}
	coverageAt := query.Until
	if coverageAt.IsZero() {
		coverageAt = stableEventAt
	}
	if query.IncludeFills && mass.FillsCoverage.State == model.CoverageComplete {
		coverageAt = mass.FillsCoverage.Scope.Through
	} else if !mass.OpenOrdersCoverage.Scope.Through.IsZero() {
		coverageAt = mass.OpenOrdersCoverage.Scope.Through
	}
	scope := ScopeKey{Venue: mass.Venue, AccountID: mass.AccountID}
	queryFrom := query.Since
	if query.IncludeFills && (mass.FillsCoverage.State == model.CoverageComplete || mass.FillsCoverage.State == model.CoveragePartial) {
		queryFrom = mass.FillsCoverage.Scope.From
	} else if queryFrom.IsZero() && !stableEventAt.IsZero() {
		queryFrom = stableEventAt.Add(-mass.Lookback)
	}
	if queryFrom.After(coverageAt) {
		queryFrom = coverageAt
	}
	fillLookbackFloor := query.Since
	if query.IncludeFills && (mass.FillsCoverage.State == model.CoverageComplete || mass.FillsCoverage.State == model.CoveragePartial) {
		fillLookbackFloor = mass.FillsCoverage.Scope.From
	} else if fillLookbackFloor.IsZero() && mass.Lookback > 0 && !stableEventAt.IsZero() {
		fillLookbackFloor = stableEventAt.Add(-mass.Lookback)
	}
	if fillLookbackFloor.After(coverageAt) {
		fillLookbackFloor = coverageAt
	}
	pass := PassHeader{
		PassID:        PassID(scope, stableEventAt),
		Scope:         scope,
		StartedAt:     r.now(),
		StableEventAt: stableEventAt,
		QueryFrom:     queryFrom,
		QueryTo:       coverageAt,
	}
	rep.Findings = append(rep.Findings, openFindings...)
	var autoResolutions []FindingResolution
	if err := r.state.BeginPass(ctx, pass); err != nil {
		return err
	}
	fillsPartial := query.IncludeFills && mass.FillsCoverage.State != model.CoverageComplete
	fillCursorScopeMismatch := false
	rep.OpenOrdersCoverage = mass.OpenOrdersCoverage.Clone()
	rep.FillsCoverage = mass.FillsCoverage.Clone()
	rep.PositionsCoverage = mass.PositionsCoverage.Clone()
	rep.Partial = rep.Partial || coverageIncomplete(mass.OpenOrdersCoverage) ||
		coverageIncomplete(mass.FillsCoverage) || coverageIncomplete(mass.PositionsCoverage)
	if query.IncludeFills && mass.FillsCoverage.State == model.CoverageComplete &&
		!fillCursorScopeCompatible(query, priorCursor, mass.FillsCoverage) {
		fillsPartial = true
		fillCursorScopeMismatch = true
		rep.Partial = true
		appendCoverageWarning(mass, "FILL_CURSOR_SCOPE_CHANGED", "complete fill evidence does not match the durable cursor instrument selector; cursor reset required")
	}
	rep.FillsPartial = rep.FillsPartial || fillsPartial
	rep.Warnings = append(rep.Warnings, mass.Warnings...)
	if fillCursorScopeMismatch {
		reset := priorCursor
		reset.Scope = scope
		reset.Stream = StreamOrders
		reset.FillInstrumentIDs = append([]model.InstrumentID{}, mass.FillsCoverage.Scope.InstrumentIDs...)
		reset.LastVenueTime = time.Time{}
		reset.LookbackFloor = time.Time{}
		reset.LastLocalApplyTime = r.now()
		reset.Partial = true
		reset.FillsPartial = true
		if err := r.state.CommitCursor(ctx, reset); err != nil {
			return err
		}
		rep.CursorsCommitted++
	}
	if query.IncludePositions {
		positionState := mass.PositionsCoverage.State
		if positionState == model.CoverageComplete || positionState == model.CoveragePartial {
			var coverage *model.ReportCoverage
			coverage = &mass.PositionsCoverage
			unresolved, err := r.reconcilePositionReports(ctx, pass, sortedPositionReports(mass.PositionReports), contract.Capabilities{}, coverage, rep)
			if err != nil {
				return err
			}
			if positionState == model.CoverageComplete {
				scoped := positionFindingsWithinCoverage(openFindings, mass.PositionsCoverage)
				resolutionPass := pass
				resolutionPass.StableEventAt = mass.PositionsCoverage.Scope.Through
				autoResolutions = append(autoResolutions, r.positionResolutionCandidates(scoped, resolutionPass, unresolved)...)
			}
		}
	}

	venueKeys := make(map[orderIdentityKey]struct{}, len(mass.OrderReports)*2)
	orderSnapshots := make([]orderReportSnapshot, 0, len(mass.OrderReports))
	orderIdentityConflict := false
	for _, report := range mass.OrderReports {
		o := report.Order
		accountID := report.AccountID
		if accountID == "" {
			accountID = mass.AccountID
		}
		if o.Request.AccountID == "" {
			o.Request.AccountID = accountID
		}
		baseline := model.Order{}
		baselineFilled := decimal.Zero
		existing, known, identityErr := resolveCachedOrderByTypedIdentity(r.cache, accountID, o)
		if identityErr != nil {
			orderIdentityConflict = true
			if err := r.recordFinding(ctx, rep, r.finding(
				pass,
				StreamOrders,
				FindingBlocking,
				"ORDER_IDENTITY_CONFLICT",
				"order report conflicts with cached typed identity: "+identityErr.Error(),
				true,
			)); err != nil {
				return err
			}
			continue
		}
		if known {
			baseline = existing
			baselineFilled = existing.FilledQty
		}
		if identityErr := r.cache.UpsertOrderChecked(o); identityErr != nil {
			orderIdentityConflict = true
			if err := r.recordFinding(ctx, rep, r.finding(
				pass,
				StreamOrders,
				FindingBlocking,
				"ORDER_IDENTITY_CONFLICT",
				"order report could not be committed: "+identityErr.Error(),
				true,
			)); err != nil {
				return err
			}
			continue
		}
		if known {
			rep.OrdersUpdated++
		} else {
			rep.OrdersExternal++
		}
		canonical := o
		if merged, ok, err := resolveCachedOrderByTypedIdentity(r.cache, accountID, o); err != nil {
			orderIdentityConflict = true
			if recordErr := r.recordFinding(ctx, rep, r.finding(
				pass,
				StreamOrders,
				FindingBlocking,
				"ORDER_IDENTITY_CONFLICT",
				"committed order report has conflicting typed identity: "+err.Error(),
				true,
			)); recordErr != nil {
				return recordErr
			}
			continue
		} else if ok {
			canonical = merged
		}
		for _, key := range orderIdentityKeysForOrder(canonical, accountID) {
			venueKeys[key] = struct{}{}
		}
		orderSnapshots = append(orderSnapshots, orderReportSnapshot{order: canonical, baseline: baseline, baselineFilled: baselineFilled})
		if r.orderResolver != nil {
			r.orderResolver.ResolveOrderInFlight(canonical, stableEventAt)
		} else if r.resolver != nil {
			r.resolver.ResolveInFlight(canonical.Request.ClientID, canonical.VenueOrderID, stableEventAt)
		}
	}
	if err := r.inferMissingSnapshotFills(pass, mass, orderSnapshots); err != nil {
		return err
	}
	if err := mass.ValidateFor(query); err != nil {
		return err
	}

	appliedFills := make(map[orderIdentityKey][]model.Fill)
	recognizedFills := make(recognizedFillSet)
	fillDependencies, err := r.applyFillReports(ctx, pass, mass, rep, appliedFills, recognizedFills)
	if err != nil {
		return err
	}
	for _, recordID := range fillDependencies {
		appliedEventRecordIDs = appendAppliedEventDependency(appliedEventRecordIDs, recordID)
	}
	if fillsPartial {
		appliedEventRecordIDs = retainedAppliedEventDependencies(
			appliedEventRecordIDs,
			r.overlapFills,
			r.observedFills,
		)
	}

	unresolvedOrderProgress := make(map[orderIdentityKey]struct{})
	for _, snapshot := range orderSnapshots {
		fills := appliedFills[orderProgressKey(snapshot.order)]
		if uncovered := r.reconcileOrderSnapshotProgress(snapshot, fills); uncovered.IsPositive() {
			condition := orderProgressKey(snapshot.order)
			unresolvedOrderProgress[condition] = struct{}{}
			if err := r.recordFinding(ctx, rep, r.finding(
				pass,
				StreamFills,
				FindingBlocking,
				"ORDER_PROGRESS_WITHOUT_FILL",
				fmt.Sprintf("order %s cumulative filled quantity advanced by %s without %s of recoverable fill reports", encodeOrderProgressCondition(condition), snapshot.order.FilledQty.Sub(snapshot.baselineFilled), uncovered),
				true,
			)); err != nil {
				return err
			}
		}
	}
	if query.IncludeFills && !fillsPartial {
		progressFindings := openFindings
		resolutionPass := pass
		progressFindings = orderProgressFindingsWithinCoverage(openFindings, mass.FillsCoverage)
		resolutionPass.StableEventAt = mass.FillsCoverage.Scope.Through
		autoResolutions = append(autoResolutions, r.orderProgressResolutionCandidates(progressFindings, resolutionPass, unresolvedOrderProgress, recognizedFills)...)
	}

	partialFindingRecorded := false
	for _, co := range orderCandidates {
		if !orderInAccountScope(co, mass.AccountID) {
			continue
		}
		if !orderWithinCoverage(co, mass.OpenOrdersCoverage, mass.AccountID) {
			continue
		}
		matched := false
		for _, key := range orderIdentityKeysForOrder(co, mass.AccountID) {
			if _, ok := venueKeys[key]; ok {
				matched = true
				continue
			}
		}
		if matched {
			continue
		}
		if orderIdentityConflict {
			continue
		}
		openOrdersIncomplete := mass.OpenOrdersCoverage.State != model.CoverageComplete
		if openOrdersIncomplete {
			if !partialFindingRecorded {
				finding := r.finding(pass, StreamOrders, FindingWarning, "PARTIAL_ORDER_REPORT", "incomplete mass-status coverage cannot prove missing open order terminal state", false)
				appendFindingUnique(rep, finding)
				if err := r.state.RecordFinding(ctx, finding); err != nil {
					return err
				}
				partialFindingRecorded = true
			}
			continue
		}
		updated, err := r.cache.MarkOrderUnknownIfUnchanged(mass.AccountID, co)
		if err != nil {
			return fmt.Errorf("reconcile: close missing order by frozen identity: %w", err)
		}
		if updated {
			rep.OrdersClosedUnknown++
		}
	}

	cursor := Cursor{
		Scope:                 scope,
		Stream:                StreamOrders,
		LastSuccessfulPass:    pass.PassID,
		LastReportID:          mass.ReportID,
		LastVenueTime:         coverageAt,
		LastLocalApplyTime:    r.now(),
		LookbackFloor:         fillLookbackFloor,
		Partial:               rep.Partial,
		FillsPartial:          fillsPartial,
		AppliedEventRecordIDs: appliedEventRecordIDs,
	}
	if query.IncludeFills && mass.FillsCoverage.State == model.CoverageComplete {
		cursor.FillInstrumentIDs = append([]model.InstrumentID{}, mass.FillsCoverage.Scope.InstrumentIDs...)
	}
	if hasBlockingFindingExcept(rep.Findings, autoResolutions) {
		return nil
	}
	if fillsPartial {
		// Positive evidence has already been applied, but an incomplete history
		// cannot advance the durable full-coverage cursor.
		return nil
	}
	if err := r.state.CommitCursor(ctx, cursor); err != nil {
		return err
	}
	rep.CursorsCommitted++
	if err := r.applyFindingResolutions(ctx, autoResolutions); err != nil {
		return err
	}
	if err := r.refreshResolvedFindings(ctx, rep, scope, autoResolutions); err != nil {
		return err
	}
	if !fillsPartial {
		if _, nonPersistent := r.state.(noopStateStore); !nonPersistent {
			r.replaceOverlapOnFinish = true
		}
	}
	return nil
}

func (r *Reconciler) massStatusQuery(ctx context.Context) (model.MassStatusQuery, Cursor, error) {
	caps := r.orders.Capabilities()
	includePositions := caps.Reports.PositionReports
	if r.account != nil && r.accountCapabilitiesSet && r.accountCapabilities.Reports.PositionReports {
		includePositions = true
	}
	query := model.MassStatusQuery{
		Venue:            caps.Venue,
		AccountID:        r.accountID,
		IncludeFills:     caps.Reports.FillHistory,
		IncludePositions: includePositions,
	}
	scope := ScopeKey{Venue: caps.Venue, AccountID: r.accountID}
	cursor, err := r.state.LoadCursor(ctx, scope, StreamOrders)
	if err != nil {
		return model.MassStatusQuery{}, Cursor{}, err
	}
	if err := r.seedAppliedFillDependencies(ctx, scope, cursor); err != nil {
		return model.MassStatusQuery{}, Cursor{}, err
	}
	// The bound is captured after every prerequisite state read and immediately
	// before callers freeze local candidates and enter the authoritative request.
	query.Until = r.now()
	if cursor.FillsPartial {
		query.Since = cursor.LookbackFloor
	} else {
		query.Since = cursor.LastVenueTime
		if query.IncludeFills && !query.Since.IsZero() {
			query.Since = query.Since.Add(-defaultCursorOverlap)
			if !cursor.LookbackFloor.IsZero() && query.Since.Before(cursor.LookbackFloor) {
				query.Since = cursor.LookbackFloor
			}
		}
		if query.Since.IsZero() {
			query.Since = cursor.LookbackFloor
		}
	}
	if query.Since.After(query.Until) {
		query.Since = query.Until
	}
	if !query.Since.IsZero() {
		query.Lookback = query.Until.Sub(query.Since)
	}
	return query, cursor, nil
}

func validateMassBeforePositionFallback(mass *model.ExecutionMassStatus, query model.MassStatusQuery) error {
	if mass == nil {
		return fmt.Errorf("reconcile: execution mass status is nil")
	}
	validation := mass.Clone()
	if query.IncludePositions && validation.PositionsCoverage.State == model.CoverageUnknown {
		if !validation.PositionsCoverage.Scope.IsZero() {
			return validation.ValidateFor(query)
		}
		// Unknown has no selector to authorize a fallback, but validate every
		// other domain and the empty shape before the final strict rejection.
		validation.PositionsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
	}
	return validation.ValidateFor(query)
}

func fillCursorScopeCompatible(query model.MassStatusQuery, cursor Cursor, coverage model.ReportCoverage) bool {
	if coverage.State != model.CoverageComplete {
		return false
	}
	responseIDs := coverage.Scope.InstrumentIDs
	if query.InstrumentIDs != nil && !slices.Equal(model.NormalizeInstrumentIDs(query.InstrumentIDs), responseIDs) {
		return false
	}
	if cursor.FillsPartial {
		return cursor.FillInstrumentIDs != nil &&
			slices.Equal(model.NormalizeInstrumentIDs(cursor.FillInstrumentIDs), responseIDs)
	}
	if cursor.LastVenueTime.IsZero() {
		return true
	}
	if cursor.FillInstrumentIDs == nil {
		return false
	}
	return slices.Equal(model.NormalizeInstrumentIDs(cursor.FillInstrumentIDs), responseIDs)
}

func (r *Reconciler) tryPositionFallback(ctx context.Context, mass *model.ExecutionMassStatus, query model.MassStatusQuery) {
	if mass == nil || !query.IncludePositions || r.account == nil {
		return
	}
	original := mass.PositionsCoverage.Clone()
	if original.State == model.CoverageComplete || original.State == model.CoverageNotRequested {
		return
	}
	accountID := strings.TrimSpace(r.accountID)
	if accountProvider, ok := r.account.(contract.AccountIDProvider); ok {
		providedAccountID := strings.TrimSpace(accountProvider.AccountID())
		if accountID != "" && providedAccountID != accountID {
			appendCoverageWarning(mass, "POSITION_FALLBACK_ACCOUNT_UNPROVEN", "account position fallback cannot prove the execution coverage account")
			return
		}
		accountID = providedAccountID
	}
	if accountID == "" || accountID != original.Scope.AccountID {
		appendCoverageWarning(mass, "POSITION_FALLBACK_ACCOUNT_UNPROVEN", "account position fallback cannot prove the execution coverage account")
		return
	}
	if !r.accountCapabilitiesSet {
		appendCoverageWarning(mass, "POSITION_FALLBACK_CAPABILITIES_UNAVAILABLE", "account position fallback has no configured account capability contract")
		return
	}
	caps := r.accountCapabilities
	if !accountCapabilitiesCoverPositionSelector(caps, mass.Venue, original.Scope) {
		appendCoverageWarning(mass, "POSITION_FALLBACK_SCOPE_UNPROVEN", "account position fallback cannot prove the original frozen instrument selector")
		return
	}
	fallbackStartedAt := r.now()
	if !original.Scope.Through.IsZero() && fallbackStartedAt.Before(original.Scope.Through) {
		appendCoverageWarning(mass, "POSITION_FALLBACK_CLOCK_REGRESSION", "account position fallback request-start precedes execution position coverage")
		return
	}
	positions, err := r.account.Positions(ctx)
	if err != nil {
		appendCoverageWarning(mass, "POSITION_FALLBACK_UNAVAILABLE", "account position fallback failed: "+err.Error())
		return
	}
	venue := mass.Venue
	if venue == "" {
		venue = caps.Venue
	}
	replacement := model.NewExecutionMassStatus(venue, mass.AccountID, fallbackStartedAt)
	seen := make(map[string]struct{}, len(positions))
	for _, position := range positions {
		if position.AccountID == "" {
			position.AccountID = mass.AccountID
		}
		if position.AccountID != original.Scope.AccountID || !original.Scope.ContainsInstrument(position.InstrumentID) {
			appendCoverageWarning(mass, "POSITION_FALLBACK_SCOPE_MISMATCH", "account position fallback returned a row outside the original frozen scope")
			return
		}
		report := model.PositionReport{
			Venue:      venue,
			AccountID:  position.AccountID,
			Position:   position,
			ReportedAt: fallbackStartedAt,
		}
		if _, duplicate := seen[report.Key()]; duplicate {
			appendCoverageWarning(mass, "POSITION_FALLBACK_INVALID", "account position fallback returned duplicate position identities")
			return
		}
		seen[report.Key()] = struct{}{}
		if err := replacement.AddPositionReport(report); err != nil {
			appendCoverageWarning(mass, "POSITION_FALLBACK_INVALID", "account position fallback returned an invalid row: "+err.Error())
			return
		}
	}
	mass.PositionReports = replacement.PositionReports
	mass.PositionsCoverage = model.NewSnapshotCoverage(
		model.CoverageComplete,
		original.Scope.AccountID,
		original.Scope.ClientID,
		original.Scope.InstrumentIDs,
		fallbackStartedAt,
	)
}

func accountCapabilitiesCoverPositionSelector(caps contract.Capabilities, venue string, scope model.CoverageScope) bool {
	if !caps.Reports.PositionReports || scope.InstrumentIDs == nil || caps.Venue == "" || !strings.EqualFold(caps.Venue, venue) {
		return false
	}
	for _, id := range scope.InstrumentIDs {
		if !strings.EqualFold(caps.Venue, id.Venue) {
			return false
		}
		covered := false
		for _, product := range caps.Products {
			if product.Kind == id.Kind && product.Account {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func appendCoverageWarning(mass *model.ExecutionMassStatus, code, message string) {
	mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: code, Message: message})
}

func coverageIncomplete(coverage model.ReportCoverage) bool {
	return coverage.State != model.CoverageComplete && coverage.State != model.CoverageNotRequested
}

func orderWithinCoverage(order model.Order, coverage model.ReportCoverage, fallbackAccountID string) bool {
	if coverage.State != model.CoverageComplete && coverage.State != model.CoveragePartial && coverage.State != model.CoverageUnavailable {
		return false
	}
	accountID := order.Request.AccountID
	if accountID == "" {
		accountID = fallbackAccountID
	}
	if accountID != coverage.Scope.AccountID || !coverage.Scope.ContainsInstrument(order.Request.InstrumentID) {
		return false
	}
	return coverage.Scope.ClientID == "" || order.Request.ClientID == coverage.Scope.ClientID
}

func positionFindingsWithinCoverage(findings []Finding, coverage model.ReportCoverage) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		condition, ok := positionFindingCondition(finding)
		if !ok {
			continue
		}
		for _, id := range coverage.Scope.InstrumentIDs {
			if strings.HasPrefix(condition, coverage.Scope.AccountID+"|"+id.String()+"|") {
				out = append(out, finding)
				break
			}
		}
	}
	return out
}

func orderProgressFindingsWithinCoverage(findings []Finding, coverage model.ReportCoverage) []Finding {
	instruments := make(map[string]struct{}, len(coverage.Scope.InstrumentIDs))
	for _, id := range coverage.Scope.InstrumentIDs {
		instruments[id.String()] = struct{}{}
	}
	out := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		key, _, ok := orderProgressFindingCondition(finding)
		if !ok || key.accountID != coverage.Scope.AccountID {
			continue
		}
		if _, ok := instruments[key.instrument]; !ok {
			continue
		}
		if coverage.Scope.ClientID != "" && (key.namespace != "client" || key.id != coverage.Scope.ClientID) {
			continue
		}
		out = append(out, finding)
	}
	return out
}

func positionReportsFromSnapshot(venue, accountID string, reportedAt time.Time, positions []model.Position) []model.PositionReport {
	reports := make([]model.PositionReport, 0, len(positions))
	for _, position := range positions {
		if position.AccountID == "" {
			position.AccountID = accountID
		}
		at := position.UpdatedAt
		if at.IsZero() {
			at = reportedAt
		}
		reports = append(reports, model.PositionReport{
			Venue:      venue,
			AccountID:  position.AccountID,
			Position:   position,
			ReportedAt: at,
		})
	}
	return reports
}

func latestPositionReportAt(reports []model.PositionReport) time.Time {
	var latest time.Time
	for _, report := range reports {
		at := report.ReportedAt
		if !report.Position.UpdatedAt.IsZero() {
			at = report.Position.UpdatedAt
		}
		if !at.IsZero() && (latest.IsZero() || at.After(latest)) {
			latest = at
		}
	}
	return latest
}

func positionPass(venue, accountID string, stableAt, startedAt time.Time) PassHeader {
	scope := ScopeKey{Venue: venue, AccountID: accountID}
	return PassHeader{
		PassID:        PassID(scope, stableAt),
		Scope:         scope,
		StartedAt:     startedAt,
		StableEventAt: stableAt,
		QueryFrom:     stableAt,
		QueryTo:       stableAt,
	}
}

func sortedPositionReports(groups map[string][]model.PositionReport) []model.PositionReport {
	var reports []model.PositionReport
	for _, group := range groups {
		reports = append(reports, group...)
	}
	sort.SliceStable(reports, func(i, j int) bool {
		left, right := reports[i], reports[j]
		leftAt, rightAt := left.ReportedAt, right.ReportedAt
		if !left.Position.UpdatedAt.IsZero() {
			leftAt = left.Position.UpdatedAt
		}
		if !right.Position.UpdatedAt.IsZero() {
			rightAt = right.Position.UpdatedAt
		}
		if !leftAt.Equal(rightAt) {
			if leftAt.IsZero() {
				return false
			}
			if rightAt.IsZero() {
				return true
			}
			return leftAt.Before(rightAt)
		}
		return left.Key() < right.Key()
	})
	return reports
}

func (r *Reconciler) reconcilePositionReports(
	ctx context.Context,
	pass PassHeader,
	reports []model.PositionReport,
	caps contract.Capabilities,
	coverage *model.ReportCoverage,
	rep *Report,
) (map[string]struct{}, error) {
	unresolved := make(map[string]struct{})
	authoritative := make(map[positionKey]model.Position, len(reports))
	reported := make(map[positionKey]struct{}, len(reports))
	lastReported := make(map[positionKey]model.Position, len(reports))
	authoritativeAt := make(map[positionKey]time.Time, len(reports))
	ambiguous := make(map[positionKey]struct{})
	for _, report := range reports {
		position := report.Position
		if position.AccountID == "" {
			position.AccountID = report.AccountID
		}
		if position.AccountID == "" {
			position.AccountID = pass.Scope.AccountID
		}
		if !positionInReportScope(position, pass.Scope, caps.Products) ||
			(coverage != nil && !coverage.Scope.ContainsInstrument(position.InstrumentID)) {
			continue
		}
		key := positionKey{position.AccountID, position.InstrumentID.String(), position.Side}
		reported[key] = struct{}{}
		at := report.ReportedAt
		if !position.UpdatedAt.IsZero() {
			at = position.UpdatedAt
		}
		if prior, exists := lastReported[key]; exists && !prior.Quantity.Equal(position.Quantity) {
			priorAt := authoritativeAt[key]
			if at.IsZero() || priorAt.IsZero() || !at.After(priorAt) {
				ambiguous[key] = struct{}{}
			}
		}
		if position.Quantity.IsZero() {
			delete(authoritative, key)
		} else {
			authoritative[key] = position
		}
		lastReported[key] = position
		authoritativeAt[key] = at
	}

	local := make(map[positionKey]model.Position)
	for _, position := range r.cache.Positions() {
		if position.AccountID == "" {
			position.AccountID = pass.Scope.AccountID
		}
		if !positionInReportScope(position, pass.Scope, caps.Products) ||
			(coverage != nil && !coverage.Scope.ContainsInstrument(position.InstrumentID)) {
			continue
		}
		key := positionKey{position.AccountID, position.InstrumentID.String(), position.Side}
		if coverage != nil && coverage.State != model.CoverageComplete {
			if _, explicitlyReported := reported[key]; !explicitlyReported {
				if _, conflict := ambiguous[key]; !conflict {
					continue
				}
			}
		}
		local[key] = position
	}

	keys := make(map[positionKey]struct{}, len(authoritative)+len(local)+len(ambiguous))
	for key := range authoritative {
		keys[key] = struct{}{}
	}
	for key := range local {
		keys[key] = struct{}{}
	}
	for key := range ambiguous {
		keys[key] = struct{}{}
	}
	ordered := make([]positionKey, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool { return positionKeyString(ordered[i]) < positionKeyString(ordered[j]) })

	for _, key := range ordered {
		if _, conflict := ambiguous[key]; conflict {
			unresolved[positionKeyString(key)] = struct{}{}
			if err := r.recordFinding(ctx, rep, r.finding(
				pass,
				StreamPositions,
				FindingBlocking,
				"POSITION_REPORT_AMBIGUOUS",
				fmt.Sprintf("position report %s contains conflicting quantities without one unambiguous snapshot", positionKeyString(key)),
				true,
			)); err != nil {
				return nil, err
			}
			continue
		}
		localPosition := local[key]
		authoritativePosition := authoritative[key]
		if localPosition.Quantity.Equal(authoritativePosition.Quantity) {
			continue
		}
		unresolved[positionKeyString(key)] = struct{}{}
		if err := r.recordFinding(ctx, rep, r.finding(
			pass,
			StreamPositions,
			FindingBlocking,
			"POSITION_MISMATCH",
			fmt.Sprintf("position %s local quantity %s differs from authoritative quantity %s", positionKeyString(key), localPosition.Quantity, authoritativePosition.Quantity),
			true,
		)); err != nil {
			return nil, err
		}
	}
	return unresolved, nil
}

func positionInReportScope(position model.Position, scope ScopeKey, products []contract.ProductCapability) bool {
	if scope.AccountID != "" && position.AccountID != "" && position.AccountID != scope.AccountID {
		return false
	}
	if scope.Venue != "" && position.InstrumentID.Venue != "" && !strings.EqualFold(position.InstrumentID.Venue, scope.Venue) {
		return false
	}
	if len(products) == 0 {
		return true
	}
	for _, product := range products {
		if product.Kind == position.InstrumentID.Kind {
			return true
		}
	}
	return false
}

func positionKeyString(key positionKey) string {
	return strings.Join([]string{key.accountID, key.instrument, key.side.String()}, "|")
}

func (r *Reconciler) recordFinding(ctx context.Context, rep *Report, finding Finding) error {
	appendFindingUnique(rep, finding)
	return r.state.RecordFinding(ctx, finding)
}

func appendFindingUnique(rep *Report, finding Finding) {
	for _, existing := range rep.Findings {
		if existing.ID == finding.ID {
			return
		}
	}
	rep.Findings = append(rep.Findings, finding)
}

func (r *Reconciler) resolvePositionFindings(
	ctx context.Context,
	rep *Report,
	open []Finding,
	pass PassHeader,
	unresolved map[string]struct{},
) error {
	resolutions := r.positionResolutionCandidates(open, pass, unresolved)
	if err := r.applyFindingResolutions(ctx, resolutions); err != nil {
		return err
	}
	return r.refreshResolvedFindings(ctx, rep, pass.Scope, resolutions)
}

func (r *Reconciler) positionResolutionCandidates(
	open []Finding,
	pass PassHeader,
	unresolved map[string]struct{},
) []FindingResolution {
	if _, ok := r.state.(revisionFindingResolver); !ok {
		return nil
	}
	var resolutions []FindingResolution
	for _, finding := range open {
		if finding.Scope != pass.Scope || finding.Stream != StreamPositions {
			continue
		}
		condition, known := positionFindingCondition(finding)
		if !known {
			continue
		}
		if _, stillUnresolved := unresolved[condition]; stillUnresolved {
			continue
		}
		resolutions = append(resolutions, FindingResolution{
			FindingID:        finding.ID,
			PassID:           pass.PassID,
			ResolvedAt:       resolutionTime(pass),
			Reason:           "complete authoritative position report no longer reproduces the condition",
			expectedRevision: finding.revision,
		})
	}
	return resolutions
}

func positionFindingCondition(finding Finding) (string, bool) {
	switch finding.Code {
	case "POSITION_MISMATCH":
		const prefix = "position "
		const suffix = " local quantity "
		if !strings.HasPrefix(finding.Message, prefix) {
			return "", false
		}
		end := strings.Index(finding.Message[len(prefix):], suffix)
		if end < 0 {
			return "", false
		}
		return finding.Message[len(prefix) : len(prefix)+end], true
	case "POSITION_REPORT_AMBIGUOUS":
		const prefix = "position report "
		const suffix = " contains conflicting quantities"
		if !strings.HasPrefix(finding.Message, prefix) {
			return "", false
		}
		end := strings.Index(finding.Message[len(prefix):], suffix)
		if end < 0 {
			return "", false
		}
		return finding.Message[len(prefix) : len(prefix)+end], true
	default:
		return "", false
	}
}

func resolutionTime(pass PassHeader) time.Time {
	if !pass.StableEventAt.IsZero() {
		return pass.StableEventAt
	}
	if !pass.StartedAt.IsZero() {
		return pass.StartedAt
	}
	return time.Now()
}

func (r *Reconciler) applyFindingResolutions(ctx context.Context, resolutions []FindingResolution) error {
	if len(resolutions) == 0 {
		return nil
	}
	resolver, ok := r.state.(FindingResolver)
	if !ok {
		return fmt.Errorf("reconcile: state store cannot resolve findings")
	}
	resolved := make(map[string]struct{}, len(resolutions))
	unique := make([]FindingResolution, 0, len(resolutions))
	for _, resolution := range resolutions {
		if _, duplicate := resolved[resolution.FindingID]; duplicate {
			continue
		}
		resolved[resolution.FindingID] = struct{}{}
		unique = append(unique, resolution)
	}
	if batch, ok := r.state.(findingBatchResolver); ok {
		return batch.resolveFindings(ctx, unique)
	}
	for _, resolution := range unique {
		if err := resolver.ResolveFinding(ctx, resolution); err != nil {
			return err
		}
	}
	return nil
}

func removeResolvedFindings(rep *Report, resolutions []FindingResolution) {
	if len(resolutions) == 0 {
		return
	}
	resolved := make(map[string]struct{}, len(resolutions))
	for _, resolution := range resolutions {
		resolved[resolution.FindingID] = struct{}{}
	}
	out := rep.Findings[:0]
	for _, finding := range rep.Findings {
		if _, ok := resolved[finding.ID]; !ok {
			out = append(out, finding)
		}
	}
	rep.Findings = out
}

func (r *Reconciler) refreshResolvedFindings(ctx context.Context, rep *Report, scope ScopeKey, resolutions []FindingResolution) error {
	removeResolvedFindings(rep, resolutions)
	open, err := r.state.LoadOpenFindings(ctx, scope)
	if err != nil {
		return err
	}
	byID := make(map[string]int, len(rep.Findings)+len(open))
	for i, finding := range rep.Findings {
		byID[finding.ID] = i
	}
	for _, finding := range open {
		if index, exists := byID[finding.ID]; exists {
			rep.Findings[index] = finding
			continue
		}
		byID[finding.ID] = len(rep.Findings)
		rep.Findings = append(rep.Findings, finding)
	}
	return nil
}

func rememberRecognizedFill(recognized recognizedFillSet, fillKey string, fill model.Fill) {
	if fillKey == "" {
		return
	}
	for _, orderKey := range orderIdentityKeysForFill(fill) {
		if recognized[orderKey] == nil {
			recognized[orderKey] = make(map[string]model.Fill)
		}
		recognized[orderKey][fillKey] = fill
	}
}

func (r *Reconciler) orderProgressResolutionCandidates(
	open []Finding,
	pass PassHeader,
	unresolved map[orderIdentityKey]struct{},
	recognized recognizedFillSet,
) []FindingResolution {
	if _, ok := r.state.(revisionFindingResolver); !ok {
		return nil
	}
	var resolutions []FindingResolution
	for _, finding := range open {
		if finding.Scope != pass.Scope || finding.Stream != StreamFills || finding.Code != "ORDER_PROGRESS_WITHOUT_FILL" {
			continue
		}
		orderKey, missing, parsed := orderProgressFindingCondition(finding)
		if !parsed {
			continue
		}
		if _, stillUnresolved := unresolved[orderKey]; stillUnresolved {
			continue
		}
		var recognizedQuantity decimal.Decimal
		for _, fill := range recognized[orderKey] {
			recognizedQuantity = recognizedQuantity.Add(fill.Quantity)
		}
		if recognizedQuantity.LessThan(missing) {
			continue
		}
		resolutions = append(resolutions, FindingResolution{
			FindingID:        finding.ID,
			PassID:           pass.PassID,
			ResolvedAt:       resolutionTime(pass),
			Reason:           "complete fill report coverage closed the cumulative order-progress gap",
			expectedRevision: finding.revision,
		})
	}
	return resolutions
}

func orderProgressFindingCondition(finding Finding) (orderIdentityKey, decimal.Decimal, bool) {
	const prefix = "order "
	const middle = " cumulative filled quantity advanced by "
	const missingMarker = " without "
	const suffix = " of recoverable fill reports"
	if !strings.HasPrefix(finding.Message, prefix) {
		return orderIdentityKey{}, decimal.Zero, false
	}
	conditionEnd := strings.Index(finding.Message[len(prefix):], middle)
	if conditionEnd < 0 {
		return orderIdentityKey{}, decimal.Zero, false
	}
	conditionEnd += len(prefix)
	orderKey, ok := decodeOrderProgressCondition(finding.Message[len(prefix):conditionEnd])
	if !ok {
		return orderIdentityKey{}, decimal.Zero, false
	}
	rest := finding.Message[conditionEnd+len(middle):]
	missingStart := strings.Index(rest, missingMarker)
	if missingStart < 0 {
		return orderIdentityKey{}, decimal.Zero, false
	}
	rest = rest[missingStart+len(missingMarker):]
	missingEnd := strings.Index(rest, suffix)
	if missingEnd < 0 {
		return orderIdentityKey{}, decimal.Zero, false
	}
	missing, err := decimal.NewFromString(rest[:missingEnd])
	if err != nil || !missing.IsPositive() {
		return orderIdentityKey{}, decimal.Zero, false
	}
	return orderKey, missing, true
}

func (r *Reconciler) inferMissingSnapshotFills(
	pass PassHeader,
	mass *model.ExecutionMassStatus,
	snapshots []orderReportSnapshot,
) error {
	fillIndex := newFillPresenceIndex(mass.FillReports)
	for _, snapshot := range snapshots {
		progress := snapshot.order.FilledQty.Sub(snapshot.baselineFilled)
		if !progress.IsPositive() || fillIndex.hasOrder(snapshot.order) {
			continue
		}
		if snapshot.order.UpdatedAt.IsZero() {
			continue
		}
		fill, ok := inferredFillFromSnapshot(snapshot, progress, pass.StableEventAt)
		if !ok {
			continue
		}
		if !fillWithinCoverage(fill, mass.FillsCoverage) {
			continue
		}
		if err := mass.AddFillReport(model.FillReport{
			Venue:      mass.Venue,
			AccountID:  fill.AccountID,
			Fill:       fill,
			ReportedAt: fill.Timestamp,
		}); err != nil {
			return err
		}
		fillIndex.add(fill)
	}
	return nil
}

func fillWithinCoverage(fill model.Fill, coverage model.ReportCoverage) bool {
	if coverage.State != model.CoverageComplete && coverage.State != model.CoveragePartial {
		return false
	}
	if fill.AccountID != coverage.Scope.AccountID || !coverage.Scope.ContainsInstrument(fill.InstrumentID) {
		return false
	}
	if coverage.Scope.ClientID != "" && fill.ClientID != coverage.Scope.ClientID {
		return false
	}
	return !fill.Timestamp.IsZero() &&
		!fill.Timestamp.Before(coverage.Scope.From) &&
		!fill.Timestamp.After(coverage.Scope.Through)
}

func newFillPresenceIndex(groups map[string][]model.FillReport) fillPresenceIndex {
	index := make(fillPresenceIndex)
	for _, reports := range groups {
		for _, report := range reports {
			index.add(report.Fill)
		}
	}
	return index
}

func (index fillPresenceIndex) add(fill model.Fill) {
	instrument := fill.InstrumentID
	if instrument.Symbol == "" {
		instrument = model.InstrumentID{}
	}
	aliases := [...]struct {
		namespace string
		id        string
	}{
		{namespace: "client", id: fill.ClientID},
		{namespace: "venue", id: fill.VenueOrderID},
	}
	for _, alias := range aliases {
		if alias.id == "" {
			continue
		}
		for _, anyAccount := range [...]bool{false, true} {
			for _, anyInstrument := range [...]bool{false, true} {
				accountID := fill.AccountID
				if anyAccount {
					accountID = ""
				}
				indexedInstrument := instrument
				if anyInstrument {
					indexedInstrument = model.InstrumentID{}
				}
				index[fillPresenceKey{
					accountID:     accountID,
					anyAccount:    anyAccount,
					instrument:    indexedInstrument,
					anyInstrument: anyInstrument,
					namespace:     alias.namespace,
					id:            alias.id,
				}] = struct{}{}
			}
		}
	}
}

func (index fillPresenceIndex) hasOrder(order model.Order) bool {
	accountScopes := []fillPresenceKey{{accountID: order.Request.AccountID}}
	if order.Request.AccountID == "" {
		accountScopes = []fillPresenceKey{{anyAccount: true}}
	} else {
		accountScopes = append(accountScopes, fillPresenceKey{})
	}
	instrumentScopes := []fillPresenceKey{{instrument: order.Request.InstrumentID}}
	if order.Request.InstrumentID.Symbol == "" {
		instrumentScopes = []fillPresenceKey{{anyInstrument: true}}
	} else {
		instrumentScopes = append(instrumentScopes, fillPresenceKey{})
	}
	aliases := [...]struct {
		namespace string
		id        string
	}{
		{namespace: "client", id: order.Request.ClientID},
		{namespace: "venue", id: order.VenueOrderID},
	}
	for _, alias := range aliases {
		if alias.id == "" {
			continue
		}
		for _, account := range accountScopes {
			for _, instrument := range instrumentScopes {
				key := fillPresenceKey{
					accountID:     account.accountID,
					anyAccount:    account.anyAccount,
					instrument:    instrument.instrument,
					anyInstrument: instrument.anyInstrument,
					namespace:     alias.namespace,
					id:            alias.id,
				}
				if _, present := index[key]; present {
					return true
				}
			}
		}
	}
	return false
}

func inferredFillFromSnapshot(snapshot orderReportSnapshot, quantity decimal.Decimal, stableAt time.Time) (model.Fill, bool) {
	order := snapshot.order
	if !quantity.IsPositive() || order.Request.InstrumentID.Symbol == "" || order.Request.Side == enums.SideUnknown || !order.AvgFillPrice.IsPositive() {
		return model.Fill{}, false
	}
	price := order.AvgFillPrice
	if snapshot.baselineFilled.IsPositive() {
		if !snapshot.baseline.AvgFillPrice.IsPositive() {
			return model.Fill{}, false
		}
		newNotional := order.AvgFillPrice.Mul(order.FilledQty)
		oldNotional := snapshot.baseline.AvgFillPrice.Mul(snapshot.baselineFilled)
		price = newNotional.Sub(oldNotional).Div(quantity)
		if !price.IsPositive() {
			return model.Fill{}, false
		}
	}
	at := order.UpdatedAt
	if at.IsZero() {
		at = stableAt
	}
	return model.Fill{
		AccountID:    order.Request.AccountID,
		InstrumentID: order.Request.InstrumentID,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
		Side:         order.Request.Side,
		Price:        price,
		Quantity:     quantity,
		Timestamp:    at,
	}, true
}

func (r *Reconciler) applyFillReports(
	ctx context.Context,
	pass PassHeader,
	mass *model.ExecutionMassStatus,
	rep *Report,
	appliedFills map[orderIdentityKey][]model.Fill,
	recognizedFills recognizedFillSet,
) ([]string, error) {
	var appliedEventRecordIDs []string
	reports := sortedFillReports(mass.FillReports)
	prefetchedOrders, err := r.prefetchDerivativeFillOrders(ctx, mass, reports)
	if err != nil {
		return nil, err
	}
	r.observedFills = make(map[string]string)
	for _, report := range reports {
		accountID := report.AccountID
		if accountID == "" {
			accountID = mass.AccountID
		}
		fill := report.Fill
		if fill.AccountID == "" {
			fill.AccountID = accountID
		}
		if r.fillMatcher != nil {
			if resolvedFill, resolved := r.fillMatcher.MatchFillInFlight(fill); resolved {
				fill = resolvedFill
			}
		}
		if fill.AccountID == "" {
			fill.AccountID = accountID
		}
		if !hasRecoverableFillEconomics(fill) {
			if err := r.recordFinding(ctx, rep, r.finding(
				pass,
				StreamFills,
				FindingBlocking,
				"FILL_INVALID_ECONOMICS",
				"fill report has an invalid side, price, or quantity",
				true,
			)); err != nil {
				return appliedEventRecordIDs, err
			}
			continue
		}
		order, orderFound, identityErr := r.orderForFill(fill)
		if identityErr != nil {
			if err := r.recordFinding(ctx, rep, r.finding(
				pass,
				StreamFills,
				FindingBlocking,
				"FILL_ORDER_IDENTITY_CONFLICT",
				"fill report conflicts with cached order identity: "+identityErr.Error(),
				true,
			)); err != nil {
				return appliedEventRecordIDs, err
			}
			continue
		}
		if orderFound {
			fill = normalizeFillIdentity(fill, order)
		}
		if fill.TradeID == "" {
			stableFillAt := report.ReportedAt
			if !fill.Timestamp.IsZero() {
				stableFillAt = fill.Timestamp
			}
			if stableFillAt.IsZero() {
				stableFillAt = pass.StableEventAt
			}
			fill.TradeID = SyntheticTradeID(accountID, fill, stableFillAt)
			rep.FillsInferred++
		}
		fillKey := orderstate.FillKey(fill)
		if r.fillIdentityConflicts(fillKey, fill) {
			rep.FillsDuplicate++
			warning := model.ReportWarning{
				Code:    "FILL_TRADE_IDENTITY_CONFLICT",
				Message: fmt.Sprintf("trade %s was reported for conflicting order identities; duplicate report skipped", fill.TradeID),
			}
			rep.Warnings = append(rep.Warnings, warning)
			if err := r.recordFinding(ctx, rep, r.finding(pass, StreamFills, FindingWarning, warning.Code, warning.Message, false)); err != nil {
				return appliedEventRecordIDs, err
			}
			continue
		}
		if pending, ok := r.pending[fillKey]; ok {
			rememberRecognizedFill(recognizedFills, fillKey, fill)
			if err := r.ensurePassFillCapacity(fillKey); err != nil {
				return appliedEventRecordIDs, err
			}
			if err := r.rememberObservedFill(fillKey, ""); err != nil {
				return appliedEventRecordIDs, err
			}
			recorder, recordable := r.state.(AppliedFillRecorder)
			if !recordable {
				return appliedEventRecordIDs, fmt.Errorf("reconcile: applied fill %q lost its durability recorder", fillKey)
			}
			recordID, err := recorder.RecordAppliedFill(ctx, pending.pass, pending.meta, pending.fill, pending.appliedAt)
			if err != nil {
				return appliedEventRecordIDs, err
			}
			if recordID == "" {
				return appliedEventRecordIDs, fmt.Errorf("reconcile: applied fill %q returned an empty durable record ID", fillKey)
			}
			delete(r.pending, fillKey)
			r.rememberAppliedFill(fillKey, recordID)
			if err := r.rememberObservedFill(fillKey, recordID); err != nil {
				return appliedEventRecordIDs, err
			}
			appliedEventRecordIDs = appendAppliedEventDependency(appliedEventRecordIDs, recordID)
			rep.FillsDuplicate++
			r.resolveAcceptedFillInFlight(fill, pass.StableEventAt)
			continue
		}
		if recordID, ok := r.passFills[fillKey]; ok {
			rememberRecognizedFill(recognizedFills, fillKey, fill)
			if err := r.rememberObservedFill(fillKey, recordID); err != nil {
				return appliedEventRecordIDs, err
			}
			appliedEventRecordIDs = appendAppliedEventDependency(appliedEventRecordIDs, recordID)
			rep.FillsDuplicate++
			r.resolveAcceptedFillInFlight(fill, pass.StableEventAt)
			continue
		}
		if recordID, ok := r.fills[fillKey]; ok {
			rememberRecognizedFill(recognizedFills, fillKey, fill)
			if err := r.rememberObservedFill(fillKey, recordID); err != nil {
				return appliedEventRecordIDs, err
			}
			appliedEventRecordIDs = appendAppliedEventDependency(appliedEventRecordIDs, recordID)
			rep.FillsDuplicate++
			r.resolveAcceptedFillInFlight(fill, pass.StableEventAt)
			continue
		}
		if _, recordable := r.state.(AppliedFillRecorder); recordable && !canReplayUncommittedAppliedFills(r.state) {
			return appliedEventRecordIDs, fmt.Errorf("reconcile: applied-fill journal cannot enumerate records for crash recovery")
		}
		materializedOrder := false
		if !orderFound {
			hydrated, hydratedOK, hydrateErr := r.hydrateAuthoritativeOrderForFill(ctx, fill, prefetchedOrders)
			if hydrateErr != nil {
				return appliedEventRecordIDs, hydrateErr
			}
			if hydratedOK {
				order = hydrated
				orderFound = true
				fill = normalizeFillIdentity(fill, order)
				rep.OrdersExternal++
			}
		}
		if !orderFound {
			if materialized, materializedOK := materializeOrderFromFill(fill, pass.StableEventAt); materializedOK {
				order = materialized
				orderFound = true
				materializedOrder = true
			}
		}
		if !orderFound {
			finding := r.finding(pass, StreamFills, FindingBlocking, "FILL_WITHOUT_ORDER", "fill report could not be matched or materialized", true)
			rep.Findings = append(rep.Findings, finding)
			if err := r.state.RecordFinding(ctx, finding); err != nil {
				return appliedEventRecordIDs, err
			}
			continue
		}
		if !hasExecutableFillEconomics(fill) {
			if err := r.recordFinding(ctx, rep, r.finding(
				pass,
				StreamFills,
				FindingBlocking,
				"FILL_INVALID_ECONOMICS",
				"fill report side could not be resolved to BUY or SELL",
				true,
			)); err != nil {
				return appliedEventRecordIDs, err
			}
			continue
		}
		if err := r.ensureObservedFillCapacity(fillKey); err != nil {
			return appliedEventRecordIDs, err
		}
		if err := r.ensurePassFillCapacity(fillKey); err != nil {
			return appliedEventRecordIDs, err
		}
		appliedAt := r.now()
		flags := contract.EventFlagFromSnapshot | contract.EventFlagFromReconciliation
		if report.Fill.TradeID == "" {
			flags |= contract.EventFlagSynthetic
		}
		env := contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: fill}, contract.EventMeta{
			Source:    contract.SourceReconciliation,
			TsApplied: appliedAt,
			Flags:     flags,
		})
		applyResult := FillApplyApplied
		if r.fillApplier != nil {
			applyResult = r.fillApplier(fill, env.Meta())
		} else {
			r.applyFillToOrder(order, fill)
		}
		duplicate := false
		switch applyResult {
		case FillApplyUnmatched:
			return appliedEventRecordIDs, fmt.Errorf("reconcile: recovered fill %q could not be applied", fillKey)
		case FillApplyDuplicate:
			duplicate = true
		case FillApplyConflict:
			if err := r.recordFinding(ctx, rep, r.finding(
				pass,
				StreamFills,
				FindingBlocking,
				"FILL_ORDER_IDENTITY_CONFLICT",
				"recovered fill was rejected by the runtime identity guard",
				true,
			)); err != nil {
				return appliedEventRecordIDs, err
			}
			continue
		case FillApplyApplied:
		default:
			return appliedEventRecordIDs, fmt.Errorf("reconcile: recovered fill %q returned unknown apply result %d", fillKey, applyResult)
		}
		fill = normalizeFillIdentity(fill, order)
		if materializedOrder {
			// The canonical runtime path must run its FillBuffer identity guard
			// before an externally discovered alias becomes authoritative.
			if _, cached := cachedOrderByTypedIdentity(r.cache, order.Request.AccountID, order); !cached {
				r.cache.UpsertOrder(order)
			}
			rep.OrdersMaterialized++
		}
		r.rememberFillIdentity(fillKey, fill)
		rememberRecognizedFill(recognizedFills, fillKey, fill)
		r.resolveAcceptedFillInFlight(fill, pass.StableEventAt)
		recordID := ""
		if recorder, ok := r.state.(AppliedFillRecorder); ok {
			var err error
			recordID, err = recorder.RecordAppliedFill(ctx, pass, env.Meta(), fill, appliedAt)
			if err != nil {
				r.pending[fillKey] = pendingAppliedFill{pass: pass, meta: env.Meta(), fill: fill, appliedAt: appliedAt}
				return appliedEventRecordIDs, err
			}
			if recordID == "" {
				r.pending[fillKey] = pendingAppliedFill{pass: pass, meta: env.Meta(), fill: fill, appliedAt: appliedAt}
				return appliedEventRecordIDs, fmt.Errorf("reconcile: applied fill %q returned an empty durable record ID", fillKey)
			}
			appliedEventRecordIDs = appendAppliedEventDependency(appliedEventRecordIDs, recordID)
		}
		r.rememberAppliedFill(fillKey, recordID)
		if err := r.rememberObservedFill(fillKey, recordID); err != nil {
			return appliedEventRecordIDs, err
		}
		if duplicate {
			rep.FillsDuplicate++
		} else {
			key := orderProgressKey(order)
			appliedFills[key] = append(appliedFills[key], fill)
			rep.FillsApplied++
		}
	}
	return appliedEventRecordIDs, nil
}

func (r *Reconciler) resolveAcceptedFillInFlight(fill model.Fill, fallback time.Time) {
	if r.resolver == nil {
		return
	}
	resolvedAt := fill.Timestamp
	if resolvedAt.IsZero() {
		resolvedAt = fallback
	}
	if r.orderResolver != nil {
		if order, ok, err := r.orderForFill(fill); err == nil && ok {
			r.orderResolver.ResolveOrderInFlight(order, resolvedAt)
			return
		}
	}
	if r.fillResolver != nil {
		r.fillResolver.ResolveFillInFlight(fill, resolvedAt)
		return
	}
	r.resolver.ResolveInFlight(fill.ClientID, fill.VenueOrderID, resolvedAt)
}

func (r *Reconciler) flushPendingAppliedFills(ctx context.Context) ([]string, error) {
	if len(r.pending) == 0 {
		return nil, nil
	}
	recorder, ok := r.state.(AppliedFillRecorder)
	if !ok {
		return nil, fmt.Errorf("reconcile: pending applied fills lost their durability recorder")
	}
	keys := make([]string, 0, len(r.pending))
	for fillKey := range r.pending {
		keys = append(keys, fillKey)
	}
	sort.Strings(keys)
	dependencies := make([]string, 0, len(keys))
	for _, fillKey := range keys {
		pending := r.pending[fillKey]
		if err := r.ensurePassFillCapacity(fillKey); err != nil {
			return nil, err
		}
		recordID, err := recorder.RecordAppliedFill(ctx, pending.pass, pending.meta, pending.fill, pending.appliedAt)
		if err != nil {
			return nil, err
		}
		if recordID == "" {
			return nil, fmt.Errorf("reconcile: applied fill %q returned an empty durable record ID", fillKey)
		}
		delete(r.pending, fillKey)
		r.rememberAppliedFill(fillKey, recordID)
		dependencies = appendAppliedEventDependency(dependencies, recordID)
	}
	return dependencies, nil
}

func (r *Reconciler) seedAppliedFillDependencies(ctx context.Context, scope ScopeKey, cursor Cursor) error {
	var dependencies []AppliedFillDependency
	if len(cursor.AppliedEventRecordIDs) > 0 {
		loader, ok := r.state.(AppliedFillLoader)
		if !ok {
			return fmt.Errorf("reconcile: cursor has applied-fill dependencies but state store cannot replay them")
		}
		loaded, err := loader.LoadAppliedFills(ctx, cursor.AppliedEventRecordIDs)
		if err != nil {
			return err
		}
		if err := validateAppliedFillDependencies(cursor.AppliedEventRecordIDs, loaded); err != nil {
			return err
		}
		dependencies = append(dependencies, loaded...)
	}
	if loader, ok := r.state.(AppliedFillReplayLoader); ok && canReplayUncommittedAppliedFills(r.state) {
		loaded, err := loader.LoadAppliedFillReplay(ctx, scope)
		if err != nil {
			return err
		}
		dependencies = append(dependencies, loaded...)
	}
	for _, dependency := range dependencies {
		if dependency.RecordID == "" {
			return fmt.Errorf("reconcile: applied fill dependency has an empty record ID")
		}
	}
	seededRecords := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		if _, seeded := seededRecords[dependency.RecordID]; seeded {
			continue
		}
		seededRecords[dependency.RecordID] = struct{}{}
		fillKey := orderstate.FillKey(dependency.Fill)
		if fillKey == "" {
			return fmt.Errorf("reconcile: applied fill dependency %q has no stable identity", dependency.RecordID)
		}
		if err := r.ensurePassFillCapacity(fillKey); err != nil {
			return err
		}
		if err := r.rememberOverlapFill(fillKey, dependency.RecordID); err != nil {
			return err
		}
		r.rememberFillIdentity(fillKey, dependency.Fill)
		r.rememberAppliedFill(fillKey, dependency.RecordID)
		if r.fillSeeder != nil {
			r.fillSeeder(dependency.Fill)
		}
	}
	return nil
}

func validateAppliedFillDependencies(requested []string, loaded []AppliedFillDependency) error {
	wanted := make(map[string]struct{}, len(requested))
	for _, recordID := range requested {
		if recordID == "" {
			return fmt.Errorf("reconcile: cursor contains an empty applied-fill dependency ID")
		}
		if _, duplicate := wanted[recordID]; duplicate {
			return fmt.Errorf("reconcile: cursor contains duplicate applied-fill dependency %q", recordID)
		}
		wanted[recordID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(loaded))
	for _, dependency := range loaded {
		if dependency.RecordID == "" {
			return fmt.Errorf("reconcile: applied-fill loader returned an empty dependency ID")
		}
		if _, expected := wanted[dependency.RecordID]; !expected {
			return fmt.Errorf("reconcile: applied-fill loader returned unexpected dependency %q", dependency.RecordID)
		}
		if _, duplicate := seen[dependency.RecordID]; duplicate {
			return fmt.Errorf("reconcile: applied-fill loader returned duplicate dependency %q", dependency.RecordID)
		}
		seen[dependency.RecordID] = struct{}{}
	}
	for recordID := range wanted {
		if _, present := seen[recordID]; !present {
			return fmt.Errorf("reconcile: applied-fill loader omitted dependency %q", recordID)
		}
	}
	return nil
}

func sortedFillReports(groups map[string][]model.FillReport) []model.FillReport {
	var reports []model.FillReport
	for _, group := range groups {
		reports = append(reports, group...)
	}
	sort.SliceStable(reports, func(i, j int) bool {
		iAt := fillReportEventAt(reports[i])
		jAt := fillReportEventAt(reports[j])
		if !iAt.Equal(jAt) {
			if iAt.IsZero() {
				return false
			}
			if jAt.IsZero() {
				return true
			}
			return iAt.Before(jAt)
		}
		return fillReportSortKey(reports[i]) < fillReportSortKey(reports[j])
	})
	return reports
}

func fillReportEventAt(report model.FillReport) time.Time {
	if !report.Fill.Timestamp.IsZero() {
		return report.Fill.Timestamp
	}
	return report.ReportedAt
}

func fillReportSortKey(report model.FillReport) string {
	fill := report.Fill
	return strings.Join([]string{
		report.AccountID,
		fill.InstrumentID.String(),
		fill.VenueOrderID,
		fill.ClientID,
		fill.TradeID,
		fill.Side.String(),
		fill.Price.String(),
		fill.Quantity.String(),
		string(report.ReportID),
	}, "\x00")
}

func normalizeFillIdentity(fill model.Fill, order model.Order) model.Fill {
	if fill.AccountID == "" {
		fill.AccountID = order.Request.AccountID
	}
	if fill.InstrumentID == (model.InstrumentID{}) {
		fill.InstrumentID = order.Request.InstrumentID
	}
	if fill.ClientID == "" {
		fill.ClientID = order.Request.ClientID
	}
	if fill.VenueOrderID == "" {
		fill.VenueOrderID = order.VenueOrderID
	}
	if fill.Side == enums.SideUnknown {
		fill.Side = order.Request.Side
	}
	return fill
}

func orderProgressKey(order model.Order) orderIdentityKey {
	if order.Request.ClientID != "" {
		return orderIdentityKey{
			accountID:  order.Request.AccountID,
			instrument: order.Request.InstrumentID.String(),
			namespace:  "client",
			id:         order.Request.ClientID,
		}
	}
	return orderIdentityKey{
		accountID:  order.Request.AccountID,
		instrument: order.Request.InstrumentID.String(),
		namespace:  "venue",
		id:         order.VenueOrderID,
	}
}

func (r *Reconciler) reconcileOrderSnapshotProgress(snapshot orderReportSnapshot, fills []model.Fill) decimal.Decimal {
	covered := snapshot.order.FilledQty.Sub(snapshot.baselineFilled)
	if !covered.IsPositive() {
		return decimal.Zero
	}
	if len(fills) == 0 {
		return covered
	}

	// Start from the cumulative venue quantity, then apply only the portion of
	// newly recovered fills that exceeds the snapshot's progress over the
	// pre-pass cache. Passing a zero observation time intentionally performs the
	// reset: the fill list below already preserves events newer than the snapshot.
	r.cache.UpsertOrderSnapshot(snapshot.order, time.Time{})
	for _, fill := range fills {
		if covered.IsPositive() {
			if !fill.Quantity.GreaterThan(covered) {
				covered = covered.Sub(fill.Quantity)
				continue
			}
			fill.Quantity = fill.Quantity.Sub(covered)
			covered = decimal.Zero
		}
		order, ok := cachedOrderByTypedIdentity(r.cache, snapshot.order.Request.AccountID, snapshot.order)
		if !ok {
			return covered
		}
		r.applyFillToOrder(order, fill)
	}
	return covered
}

func hasBlockingFindingExcept(findings []Finding, resolutions []FindingResolution) bool {
	resolved := make(map[string]struct{}, len(resolutions))
	for _, resolution := range resolutions {
		resolved[resolution.FindingID] = struct{}{}
	}
	for _, finding := range findings {
		if _, resolving := resolved[finding.ID]; resolving {
			continue
		}
		if finding.Blocking || finding.Severity == FindingBlocking {
			return true
		}
	}
	return false
}

func appendAppliedEventDependency(ids []string, recordID string) []string {
	if recordID == "" {
		return ids
	}
	for _, existing := range ids {
		if existing == recordID {
			return ids
		}
	}
	return append(ids, recordID)
}

func retainedAppliedEventDependencies(current []string, retained ...map[string]string) []string {
	unique := make(map[string]struct{}, len(current))
	for _, recordID := range current {
		if recordID != "" {
			unique[recordID] = struct{}{}
		}
	}
	for _, records := range retained {
		for _, recordID := range records {
			if recordID != "" {
				unique[recordID] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(unique))
	for recordID := range unique {
		out = append(out, recordID)
	}
	sort.Strings(out)
	return out
}

func (r *Reconciler) fillIdentityConflicts(fillKey string, fill model.Fill) bool {
	if fillKey == "" {
		return false
	}
	known, exists := r.fillIdentities[fillKey]
	if !exists {
		return false
	}
	if known.clientID != "" && fill.ClientID != "" && known.clientID != fill.ClientID {
		return true
	}
	return known.venueOrderID != "" && fill.VenueOrderID != "" && known.venueOrderID != fill.VenueOrderID
}

func (r *Reconciler) rememberFillIdentity(fillKey string, fill model.Fill) {
	if fillKey == "" {
		return
	}
	if r.fillIdentities == nil {
		r.fillIdentities = make(map[string]fillOrderIdentity)
	}
	identity := r.fillIdentities[fillKey]
	if identity.clientID == "" {
		identity.clientID = fill.ClientID
	}
	if identity.venueOrderID == "" {
		identity.venueOrderID = fill.VenueOrderID
	}
	r.fillIdentities[fillKey] = identity
}

func (r *Reconciler) orderForFill(fill model.Fill) (model.Order, bool, error) {
	return r.cache.ResolveOrderForFill(r.accountID, fill)
}

func (r *Reconciler) prefetchDerivativeFillOrders(
	ctx context.Context,
	mass *model.ExecutionMassStatus,
	reports []model.FillReport,
) (map[derivativeFillOrderPrefetchKey]*model.OrderStatusReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !r.orders.Capabilities().Reports.SingleOrderStatus {
		return nil, nil
	}
	requestIndex := make(map[derivativeFillOrderPrefetchKey]int)
	requests := make([]derivativeFillOrderPrefetchRequest, 0)
	for _, report := range reports {
		fill := report.Fill
		if fill.AccountID == "" {
			fill.AccountID = report.AccountID
		}
		if fill.AccountID == "" {
			fill.AccountID = mass.AccountID
		}
		if !r.derivativeFillNeedsOrderPrefetch(fill) {
			continue
		}
		key, ok := derivativeFillOrderKey(fill)
		if !ok {
			continue
		}
		if index, exists := requestIndex[key]; exists {
			requests[index].fills = append(requests[index].fills, fill)
			continue
		}
		requestIndex[key] = len(requests)
		requests = append(requests, derivativeFillOrderPrefetchRequest{key: key, fills: []model.Fill{fill}})
	}
	if len(requests) == 0 {
		return nil, nil
	}

	results := make([]*model.OrderStatusReport, len(requests))
	jobs := make(chan int, len(requests))
	for index := range requests {
		jobs <- index
	}
	close(jobs)
	workerCount := derivativeFillOrderPrefetchLimit
	if len(requests) < workerCount {
		workerCount = len(requests)
	}
	var workers sync.WaitGroup
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	var firstErr error
	var firstErrOnce sync.Once
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				var index int
				var ok bool
				select {
				case <-workerCtx.Done():
					return
				case index, ok = <-jobs:
					if !ok {
						return
					}
				}
				if workerCtx.Err() != nil {
					return
				}
				request := requests[index]
				report, err := r.queryAuthoritativeOrderForFill(workerCtx, request.fills[0])
				if err == nil && report != nil {
					for _, fill := range request.fills[1:] {
						if _, validateErr := validateAuthoritativeOrderForFill(fill, *report); validateErr != nil {
							err = validateErr
							break
						}
					}
				}
				if err != nil {
					firstErrOnce.Do(func() {
						firstErr = err
						cancelWorkers()
					})
					return
				}
				results[index] = report
			}
		}()
	}
	workers.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validatePrefetchedDerivativeOrderBatch(r.cache.Orders(), results); err != nil {
		return nil, err
	}
	prefetched := make(map[derivativeFillOrderPrefetchKey]*model.OrderStatusReport, len(requests))
	for index, request := range requests {
		prefetched[request.key] = results[index]
	}
	return prefetched, nil
}

func validatePrefetchedDerivativeOrderBatch(existing []model.Order, reports []*model.OrderStatusReport) error {
	staged := cache.NewWithTerminalOrderLimit(derivativeFillOrderValidationTerminalLimit(existing, reports))
	for _, order := range existing {
		if err := staged.UpsertOrderChecked(order); err != nil {
			return fmt.Errorf("reconcile: cached order identity graph is invalid before derivative fill hydration: %w", err)
		}
	}
	for _, report := range reports {
		if report == nil {
			continue
		}
		if err := staged.UpsertOrderChecked(report.Order); err != nil {
			return fmt.Errorf("reconcile: derivative fill order batch identity conflict: %w", err)
		}
	}
	return nil
}

func derivativeFillOrderValidationTerminalLimit(existing []model.Order, reports []*model.OrderStatusReport) int {
	limit := len(existing)
	for _, report := range reports {
		if report != nil {
			limit++
		}
	}
	if limit == 0 {
		return 1
	}
	return limit
}

func (r *Reconciler) derivativeFillNeedsOrderPrefetch(fill model.Fill) bool {
	if fill.InstrumentID.Kind == enums.KindSpot || fill.TradeID == "" || !hasRecoverableFillEconomics(fill) {
		return false
	}
	if _, found, err := r.orderForFill(fill); err != nil || found {
		return false
	}
	fillKey := orderstate.FillKey(fill)
	if fillKey == "" || r.fillIdentityConflicts(fillKey, fill) {
		return false
	}
	if _, duplicate := r.pending[fillKey]; duplicate {
		return false
	}
	if _, duplicate := r.passFills[fillKey]; duplicate {
		return false
	}
	_, duplicate := r.fills[fillKey]
	return !duplicate
}

func derivativeFillOrderKey(fill model.Fill) (derivativeFillOrderPrefetchKey, bool) {
	key := derivativeFillOrderPrefetchKey{accountID: fill.AccountID, instrument: fill.InstrumentID}
	if fill.VenueOrderID != "" {
		key.namespace = "venue"
		key.id = fill.VenueOrderID
		return key, true
	}
	if fill.ClientID != "" {
		key.namespace = "client"
		key.id = fill.ClientID
		return key, true
	}
	return derivativeFillOrderPrefetchKey{}, false
}

func (r *Reconciler) hydrateAuthoritativeOrderForFill(
	ctx context.Context,
	fill model.Fill,
	prefetched map[derivativeFillOrderPrefetchKey]*model.OrderStatusReport,
) (model.Order, bool, error) {
	if fill.InstrumentID.Kind == enums.KindSpot || !r.orders.Capabilities().Reports.SingleOrderStatus {
		return model.Order{}, false, nil
	}
	if key, ok := derivativeFillOrderKey(fill); ok {
		if report, found := prefetched[key]; found {
			if report == nil {
				return model.Order{}, false, nil
			}
			return r.cacheAuthoritativeOrderForFill(fill, *report)
		}
	}
	report, err := r.queryAuthoritativeOrderForFill(ctx, fill)
	if err != nil {
		return model.Order{}, false, err
	}
	if report == nil {
		return model.Order{}, false, nil
	}
	return r.cacheAuthoritativeOrderForFill(fill, *report)
}

func (r *Reconciler) queryAuthoritativeOrderForFill(ctx context.Context, fill model.Fill) (*model.OrderStatusReport, error) {
	if fill.InstrumentID.Kind == enums.KindSpot || !r.orders.Capabilities().Reports.SingleOrderStatus {
		return nil, nil
	}
	report, err := r.orders.GenerateOrderStatusReport(ctx, model.SingleOrderStatusQuery{
		InstrumentID: fill.InstrumentID,
		AccountID:    fill.AccountID,
		ClientID:     fill.ClientID,
		VenueOrderID: fill.VenueOrderID,
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile: hydrate order for derivative fill: %w", err)
	}
	if report == nil {
		return nil, nil
	}
	order, err := validateAuthoritativeOrderForFill(fill, *report)
	if err != nil {
		return nil, err
	}
	copy := *report
	copy.Order = order
	return &copy, nil
}

func validateAuthoritativeOrderForFill(fill model.Fill, report model.OrderStatusReport) (model.Order, error) {
	if report.AccountID != "" && fill.AccountID != "" && report.AccountID != fill.AccountID {
		return model.Order{}, fmt.Errorf("reconcile: derivative fill order report account %q does not match fill account %q", report.AccountID, fill.AccountID)
	}
	order := report.Order
	if order.Request.AccountID == "" {
		order.Request.AccountID = fill.AccountID
	}
	report.Order = order
	if err := report.Validate(); err != nil {
		return model.Order{}, fmt.Errorf("reconcile: invalid derivative fill order report: %w", err)
	}
	if !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{
		InstrumentID: fill.InstrumentID,
		AccountID:    fill.AccountID,
		ClientID:     fill.ClientID,
		VenueOrderID: fill.VenueOrderID,
	}) {
		return model.Order{}, fmt.Errorf("reconcile: derivative fill order report does not match fill identity")
	}
	if order.Request.Side != enums.SideBuy && order.Request.Side != enums.SideSell {
		return model.Order{}, fmt.Errorf("reconcile: derivative fill order report has invalid side %d", order.Request.Side)
	}
	if fill.Side != enums.SideUnknown && fill.Side != order.Request.Side {
		return model.Order{}, fmt.Errorf("reconcile: derivative fill side %d does not match order side %d", fill.Side, order.Request.Side)
	}
	if order.Request.PositionSide != enums.PosNet && order.Request.PositionSide != enums.PosLong && order.Request.PositionSide != enums.PosShort {
		return model.Order{}, fmt.Errorf("reconcile: derivative fill order report has invalid position side %d", order.Request.PositionSide)
	}
	return order, nil
}

func (r *Reconciler) cacheAuthoritativeOrderForFill(fill model.Fill, report model.OrderStatusReport) (model.Order, bool, error) {
	order, err := validateAuthoritativeOrderForFill(fill, report)
	if err != nil {
		return model.Order{}, false, err
	}
	if err := r.cache.UpsertOrderChecked(order); err != nil {
		return model.Order{}, false, fmt.Errorf("reconcile: derivative fill order identity conflict: %w", err)
	}
	canonical, ok, err := r.orderForFill(fill)
	if err != nil {
		return model.Order{}, false, fmt.Errorf("reconcile: derivative fill order identity conflict after hydration: %w", err)
	}
	if !ok {
		return model.Order{}, false, fmt.Errorf("reconcile: hydrated derivative fill order is not addressable by fill identity")
	}
	return canonical, true, nil
}

func (r *Reconciler) applyFillToOrder(order model.Order, fill model.Fill) {
	r.cache.UpsertOrder(orderstate.ApplyFill(order, fill, r.now()))
}

func orderInAccountScope(o model.Order, accountID string) bool {
	return accountID == "" || o.Request.AccountID == "" || o.Request.AccountID == accountID
}

func materializeOrderFromFill(fill model.Fill, stableEventAt time.Time) (model.Order, bool) {
	if fill.InstrumentID.Kind != enums.KindSpot || fill.InstrumentID.Symbol == "" || fill.VenueOrderID == "" ||
		!hasExecutableFillEconomics(fill) {
		return model.Order{}, false
	}
	clientID := fill.ClientID
	if clientID == "" {
		clientID = "external-"
		if fill.AccountID != "" {
			clientID += fill.AccountID + "-"
		}
		clientID += fill.VenueOrderID
	}
	if fill.ClientID == "" && fill.TradeID != "" {
		clientID += "-" + fill.TradeID
	}
	ts := fill.Timestamp
	if ts.IsZero() {
		ts = stableEventAt
	}
	return model.Order{
		Request: model.OrderRequest{
			AccountID:    fill.AccountID,
			InstrumentID: fill.InstrumentID,
			ClientID:     clientID,
			Side:         fill.Side,
			Type:         enums.TypeMarket,
			Quantity:     fill.Quantity,
			Price:        fill.Price,
			PositionSide: enums.PosNet,
		},
		VenueOrderID: fill.VenueOrderID,
		Status:       enums.StatusNew,
		CreatedAt:    ts,
		UpdatedAt:    ts,
	}, true
}

func hasRecoverableFillEconomics(fill model.Fill) bool {
	return fill.Quantity.IsPositive() && fill.Price.IsPositive() &&
		(fill.Side == enums.SideUnknown || fill.Side == enums.SideBuy || fill.Side == enums.SideSell)
}

func hasExecutableFillEconomics(fill model.Fill) bool {
	return fill.Quantity.IsPositive() && fill.Price.IsPositive() &&
		(fill.Side == enums.SideBuy || fill.Side == enums.SideSell)
}

func (r *Reconciler) finding(pass PassHeader, stream ReportStream, severity FindingSeverity, code, message string, blocking bool) Finding {
	idParts := []string{"finding", string(pass.PassID), string(stream), code, message}
	if blocking || severity == FindingBlocking {
		// Blocking findings are sticky and journal-protected, so key them by
		// logical condition instead of creating one permanent record per pass.
		idParts = []string{"blocking-finding", pass.Scope.String(), string(stream), code, message}
	}
	id := string(DeterministicID(idParts...))
	return Finding{
		ID:        id,
		PassID:    pass.PassID,
		Scope:     pass.Scope,
		Stream:    stream,
		Severity:  severity,
		Code:      code,
		Message:   message,
		Blocking:  blocking,
		CreatedAt: r.now(),
	}
}

func cachedOrderByTypedIdentity(c *cache.Cache, accountID string, order model.Order) (model.Order, bool) {
	existing, found, err := resolveCachedOrderByTypedIdentity(c, accountID, order)
	return existing, found && err == nil
}

func resolveCachedOrderByTypedIdentity(c *cache.Cache, accountID string, order model.Order) (model.Order, bool, error) {
	existing, found, err := c.ResolveOrderForFill(accountID, model.Fill{
		AccountID:    order.Request.AccountID,
		InstrumentID: order.Request.InstrumentID,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
		Side:         order.Request.Side,
	})
	return existing, found, err
}

func orderIdentityKeysForOrder(order model.Order, fallbackAccountID string) []orderIdentityKey {
	accountID := order.Request.AccountID
	if accountID == "" {
		accountID = fallbackAccountID
	}
	base := orderIdentityKey{accountID: accountID, instrument: order.Request.InstrumentID.String()}
	keys := make([]orderIdentityKey, 0, 2)
	if order.Request.ClientID != "" {
		key := base
		key.namespace = "client"
		key.id = order.Request.ClientID
		keys = append(keys, key)
	}
	if order.VenueOrderID != "" {
		key := base
		key.namespace = "venue"
		key.id = order.VenueOrderID
		keys = append(keys, key)
	}
	return keys
}

func orderIdentityKeysForFill(fill model.Fill) []orderIdentityKey {
	base := orderIdentityKey{accountID: fill.AccountID, instrument: fill.InstrumentID.String()}
	keys := make([]orderIdentityKey, 0, 2)
	if fill.ClientID != "" {
		key := base
		key.namespace = "client"
		key.id = fill.ClientID
		keys = append(keys, key)
	}
	if fill.VenueOrderID != "" {
		key := base
		key.namespace = "venue"
		key.id = fill.VenueOrderID
		keys = append(keys, key)
	}
	return keys
}

func encodeOrderProgressCondition(condition orderIdentityKey) string {
	raw := strings.Join([]string{
		condition.accountID,
		condition.instrument,
		condition.namespace,
		condition.id,
	}, "\x00")
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeOrderProgressCondition(encoded string) (orderIdentityKey, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return orderIdentityKey{}, false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 4 || (parts[2] != "client" && parts[2] != "venue") || parts[3] == "" {
		return orderIdentityKey{}, false
	}
	return orderIdentityKey{
		accountID:  parts[0],
		instrument: parts[1],
		namespace:  parts[2],
		id:         parts[3],
	}, true
}

// orderKeyOf mirrors the cache's keying: ClientID is preferred (stable across
// the submit/ack boundary), VenueOrderID is the fallback for venue-discovered
// orders.
func orderKeyOf(o model.Order) string {
	if o.Request.ClientID != "" {
		return o.Request.ClientID
	}
	return o.VenueOrderID
}

type positionKey struct {
	accountID  string
	instrument string
	side       enums.PositionSide
}

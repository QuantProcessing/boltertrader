// Package exec provides the ExecutionEngine: the runtime's order-submission
// front door. It assigns a stable ClientID, records the intended order in the
// Cache as PendingNew, submits through the venue-neutral ExecutionClient, and
// records the acknowledged order. Subsequent lifecycle/fill events flow in via
// the bus, not here.
package exec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/latency"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
	"github.com/shopspring/decimal"
)

// Engine submits orders and keeps the Cache in step with what we sent.
type Engine struct {
	client contract.ExecutionClient
	cache  *cache.Cache
	clk    clock.Clock

	seq       uint64
	prefix    string
	accountID string

	// risk, if set, gates every submission. provider resolves instrument
	// metadata for instrument-level checks. Both optional.
	risk     RiskChecker
	provider model.InstrumentProvider
	latency  latency.Recorder
	gate     CommandGate
	journal  journal.Store
	inflight *InFlightJournal

	// commandEntropy is private so callers cannot weaken command identities.
	// Tests replace it to prove entropy failures stop before durable or venue
	// side effects.
	commandEntropy io.Reader

	onRecoverabilityBreach func(error)
	onTerminalOrder        func(model.Order)
}

// RiskChecker is the pre-trade gate ExecEngine consults before submitting. It is
// satisfied by runtime/risk.Engine. Decoupled via interface so exec doesn't
// import risk (and to keep it swappable/testable).
type RiskChecker interface {
	Check(req model.OrderRequest, inst *model.Instrument) error
}

// ContextRiskChecker is the optional context-aware risk surface used when a
// venue must authoritatively validate capacity or a prepared payload. Existing
// RiskChecker implementations remain source-compatible.
type ContextRiskChecker interface {
	CheckContext(
		ctx context.Context,
		req model.OrderRequest,
		inst *model.Instrument,
	) (contract.PreTradeLease, error)
}

// SubmissionRiskChecker atomically holds local risk exposure across the gap
// between validation and PendingNew cache insertion. release must be safe to
// call after any later submit outcome.
type SubmissionRiskChecker interface {
	CheckSubmission(
		ctx context.Context,
		req model.OrderRequest,
		inst *model.Instrument,
	) (contract.PreTradeLease, func(), error)
}

type CommandGate interface {
	CanSubmit(model.OrderRequest) error
	CanCancel() error
	CanModify() error
}

type SubmitValidator interface {
	ValidateSubmit(model.OrderRequest) error
}

// New builds an ExecutionEngine. idPrefix namespaces generated client ids
// (e.g. a strategy name) so concurrent strategies don't collide.
func New(client contract.ExecutionClient, c *cache.Cache, clk clock.Clock, idPrefix string) *Engine {
	if idPrefix == "" {
		idPrefix = "bt"
	}
	return &Engine{
		client:         client,
		cache:          c,
		clk:            clk,
		prefix:         idPrefix,
		accountID:      idPrefix,
		journal:        journal.NewMemory(),
		inflight:       NewInFlightJournal(),
		commandEntropy: rand.Reader,
	}
}

// WithRisk attaches a pre-trade risk gate and an instrument provider for
// instrument-level checks (provider may be nil).
func (e *Engine) WithRisk(r RiskChecker, provider model.InstrumentProvider) *Engine {
	e.risk = r
	e.provider = provider
	if aware, ok := r.(interface {
		SetInstrumentProvider(model.InstrumentProvider)
	}); ok {
		aware.SetInstrumentProvider(provider)
	}
	return e
}

func (e *Engine) WithLatencyRecorder(rec latency.Recorder) *Engine {
	e.latency = rec
	return e
}

func (e *Engine) WithCommandGate(gate CommandGate) *Engine {
	e.gate = gate
	return e
}

// WithJournal installs the durable command store. Store methods may read Cache,
// but must not synchronously invoke order-mutating Cache methods or re-enter the
// same Engine while a command result is being committed.
func (e *Engine) WithJournal(store journal.Store) *Engine {
	if store != nil {
		e.journal = store
	}
	return e
}

func (e *Engine) WithAccountID(accountID string) *Engine {
	if accountID == "" {
		return e
	}
	e.accountID = accountID
	return e
}

func (e *Engine) WithInFlightJournal(inflight *InFlightJournal) *Engine {
	if inflight != nil {
		e.inflight = inflight
	}
	return e
}

func (e *Engine) WithRecoverabilityHandler(fn func(error)) *Engine {
	e.onRecoverabilityBreach = fn
	return e
}

// WithTerminalOrderHandler installs a synchronous notification for terminal
// orders applied directly by command responses rather than the event bus.
func (e *Engine) WithTerminalOrderHandler(fn func(model.Order)) *Engine {
	e.onTerminalOrder = fn
	return e
}

func (e *Engine) OpenInFlight() []InFlightEntry {
	if e.inflight == nil {
		return nil
	}
	return e.inflight.Open()
}

func (e *Engine) InFlightCount() int {
	if e.inflight == nil {
		return 0
	}
	return e.inflight.Count()
}

func (e *Engine) ResolveInFlight(clientID, venueOrderID string, at time.Time) {
	if e.inflight == nil {
		return
	}
	entry, ok := e.inflight.ByClientID(clientID)
	if !ok && venueOrderID != "" {
		entry, ok = e.inflight.ByVenueOrderID(venueOrderID)
	}
	if !ok {
		return
	}
	e.resolveEntry(entry, venueOrderID, OutcomeConfirmedAccepted, "", at)
}

// ResolveOrderInFlight applies authoritative order evidence according to the
// command that is still awaiting confirmation. An unchanged open order proves
// submission, but it does not prove that a cancel or modify took effect.
func (e *Engine) ResolveOrderInFlight(order model.Order, at time.Time) bool {
	entry, ok := e.matchOrderInFlight(order)
	if !ok {
		return false
	}
	outcome, reason, conclusive := classifyOrderEvidence(entry, order)
	if !conclusive {
		return false
	}
	return e.resolveEntry(entry, order.VenueOrderID, outcome, reason, at)
}

func (e *Engine) matchOrderInFlight(order model.Order) (InFlightEntry, bool) {
	if e.inflight == nil {
		return InFlightEntry{}, false
	}
	var matched InFlightEntry
	matchedOK := false
	if order.Request.ClientID != "" {
		matched, matchedOK = e.inflight.ByClientID(order.Request.ClientID)
	}
	if order.VenueOrderID != "" {
		byVenue, venueOK := e.inflight.ByVenueOrderID(order.VenueOrderID)
		if matchedOK && venueOK && byVenue.Intent.RecordID != matched.Intent.RecordID {
			return InFlightEntry{}, false
		}
		if !matchedOK && venueOK {
			matched, matchedOK = byVenue, true
		}
	}
	if !matchedOK || !inFlightEntryMatchesOrder(matched, order) {
		return InFlightEntry{}, false
	}
	return matched, true
}

func inFlightEntryMatchesOrder(entry InFlightEntry, order model.Order) bool {
	intent := entry.Intent
	req := order.Request
	if req.AccountID != "" && intent.AccountID != "" && req.AccountID != intent.AccountID {
		return false
	}
	if req.InstrumentID != (model.InstrumentID{}) && intent.InstrumentID != (model.InstrumentID{}) && req.InstrumentID != intent.InstrumentID {
		return false
	}
	if req.Side != enums.SideUnknown && intent.Side != enums.SideUnknown && req.Side != intent.Side {
		return false
	}
	if req.ClientID != "" && intent.ClientID != "" && req.ClientID != intent.ClientID {
		return false
	}
	if order.VenueOrderID != "" && intent.VenueOrderID != "" && order.VenueOrderID != intent.VenueOrderID {
		return false
	}
	return true
}

func classifyOrderEvidence(entry InFlightEntry, order model.Order) (OutcomeClass, string, bool) {
	switch entry.Intent.Type {
	case journal.CommandSubmit:
		if order.Status == enums.StatusRejected || order.Status == enums.StatusExpired {
			return OutcomeDefinitiveVenueRejected, orderEvidenceReason(entry, order), true
		}
		return OutcomeConfirmedAccepted, "", true
	case journal.CommandCancel:
		if order.Status == enums.StatusCanceled {
			return OutcomeConfirmedAccepted, "", true
		}
		if orderstate.IsTerminal(order.Status) {
			return OutcomeDefinitiveVenueRejected, orderEvidenceReason(entry, order), true
		}
		return "", "", false
	case journal.CommandModify:
		if order.Status == enums.StatusRejected || order.Status == enums.StatusExpired {
			return OutcomeDefinitiveVenueRejected, orderEvidenceReason(entry, order), true
		}
		if order.Request.Price.Equal(entry.Intent.Price) && order.Request.Quantity.Equal(entry.Intent.Quantity) {
			return OutcomeConfirmedAccepted, "", true
		}
		if orderstate.IsTerminal(order.Status) {
			return OutcomeDefinitiveVenueRejected, orderEvidenceReason(entry, order), true
		}
		return "", "", false
	default:
		return "", "", false
	}
}

func orderEvidenceReason(entry InFlightEntry, order model.Order) string {
	return fmt.Sprintf("authoritative order status %s did not confirm pending %s", order.Status, entry.Intent.Type)
}

// MatchFillInFlight read-only matches and enriches a fill from pending command
// identity. It never writes a command result or consumes the in-flight entry.
func (e *Engine) MatchFillInFlight(fill model.Fill) (model.Fill, bool) {
	fill, _, ok := e.matchFillInFlight(fill)
	return fill, ok
}

func (e *Engine) matchFillInFlight(fill model.Fill) (model.Fill, InFlightEntry, bool) {
	if e.inflight == nil {
		return fill, InFlightEntry{}, false
	}
	entry, ok := e.inflight.MatchFill(fill)
	if !ok {
		return fill, InFlightEntry{}, false
	}
	if fill.AccountID == "" {
		fill.AccountID = entry.Intent.AccountID
	}
	if fill.InstrumentID == (model.InstrumentID{}) {
		fill.InstrumentID = entry.Intent.InstrumentID
	}
	if fill.ClientID == "" {
		fill.ClientID = entry.Intent.ClientID
	}
	if fill.VenueOrderID == "" {
		fill.VenueOrderID = entry.Intent.VenueOrderID
	}
	if fill.Side == enums.SideUnknown {
		fill.Side = entry.Intent.Side
	}
	return fill, entry, true
}

// ResolveFillInFlight preserves the original mutating API for compatibility.
// Runtime paths use MatchFillInFlight and resolve only after identity guards.
func (e *Engine) ResolveFillInFlight(fill model.Fill, at time.Time) (model.Fill, bool) {
	fill, entry, ok := e.matchFillInFlight(fill)
	if !ok {
		return fill, false
	}
	if entry.State != InFlightSubmitted || entry.Intent.Type != journal.CommandSubmit {
		return fill, false
	}
	if !e.resolveEntry(entry, fill.VenueOrderID, OutcomeConfirmedAccepted, "", at) {
		return fill, false
	}
	return fill, true
}

func (e *Engine) notifyTerminalOrder(order model.Order) {
	if e.onTerminalOrder != nil && orderstate.IsTerminal(order.Status) {
		e.onTerminalOrder(order)
	}
}

func (e *Engine) RejectInFlight(clientID, venueOrderID, reason string, at time.Time) {
	if e.inflight == nil {
		return
	}
	entry, ok := e.inflight.ByClientID(clientID)
	if !ok && venueOrderID != "" {
		entry, ok = e.inflight.ByVenueOrderID(venueOrderID)
	}
	if !ok {
		return
	}
	e.resolveEntry(entry, venueOrderID, OutcomeDefinitiveVenueRejected, reason, at)
}

func (e *Engine) resolveEntry(entry InFlightEntry, venueOrderID string, outcome OutcomeClass, resultErr string, at time.Time) bool {
	if at.IsZero() {
		at = e.clk.Now()
	}
	if venueOrderID == "" {
		venueOrderID = entry.Intent.VenueOrderID
	}
	result := journal.CommandResult{
		RecordID: journal.NewRecordID(
			"result",
			entry.Intent.RecordID,
			"resolved",
			string(outcome),
			venueOrderID,
			at.Format(time.RFC3339Nano),
		),
		IntentRecordID: entry.Intent.RecordID,
		CommandID:      entry.Intent.CommandID,
		Type:           entry.Intent.Type,
		ClientID:       entry.Intent.ClientID,
		VenueOrderID:   venueOrderID,
		Outcome:        string(outcome),
		Error:          resultErr,
		ResultAt:       at,
	}
	// Recovery callers may install an in-flight journal reconstructed from a
	// different process before attaching the durable Store. Re-append the exact
	// intent idempotently so every event-derived result remains self-contained
	// and passes the journal's intent/result identity guard.
	err := e.inflight.commitResult(result, func() error {
		if err := e.journal.AppendCommandIntent(context.Background(), entry.Intent); err != nil {
			return err
		}
		return e.journal.AppendCommandResult(context.Background(), result)
	})
	if err != nil {
		if !errors.Is(err, ErrInFlightIntentAlreadyResolved) {
			e.notifyRecoverabilityBreach(err)
		}
		return false
	}
	return true
}

func (e *Engine) ReplayOpenIntents(ctx context.Context) error {
	if e.journal == nil || e.inflight == nil {
		return nil
	}
	intents, err := e.journal.OpenIntents(ctx)
	if err != nil {
		return err
	}
	return e.inflight.ReplayOpenIntentsChecked(intents)
}

func (e *Engine) ensureClientIDAvailable(clientID string, includeCache bool) error {
	if e.inflight != nil {
		if _, exists := e.inflight.ByClientID(clientID); exists {
			return fmt.Errorf("%w %q", ErrDuplicateClientID, clientID)
		}
	}
	if includeCache && e.cache != nil {
		if _, exists := e.cache.OrderByClientIDForAccount(e.accountID, clientID); exists {
			return fmt.Errorf("%w %q", ErrDuplicateClientID, clientID)
		}
	}
	return nil
}

func (e *Engine) reserveAndAppendIntent(ctx context.Context, intent journal.CommandIntent, state InFlightState, rejectCachedClientID bool) error {
	if err := e.inflight.TrackIntentChecked(intent, state); err != nil {
		return err
	}
	if rejectCachedClientID && e.cache != nil {
		if _, exists := e.cache.OrderByClientIDForAccount(e.accountID, intent.ClientID); exists {
			e.inflight.discardIntent(intent.RecordID)
			return fmt.Errorf("%w %q", ErrDuplicateClientID, intent.ClientID)
		}
	}
	if err := e.journal.AppendCommandIntent(ctx, intent); err != nil {
		e.inflight.discardIntent(intent.RecordID)
		return err
	}
	return nil
}

// nextClientID generates a stable, unique idempotency key. It is monotonic
// within a process run and namespaced by prefix + submit time.
func (e *Engine) nextClientID() string {
	n := atomic.AddUint64(&e.seq, 1)
	return fmt.Sprintf("%s-%d-%d", e.prefix, e.clk.Now().UnixMilli(), n)
}

// Submit assigns a ClientID if absent, journals the command intent before the
// venue boundary, and only applies definitive venue evidence to cache.
func (e *Engine) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	cmd := latency.CommandLatency{Command: "submit", StartedAt: time.Now()}
	defer func() {
		cmd.Finish(time.Now())
		if e.latency != nil {
			e.latency.RecordCommandLatency(cmd)
		}
	}()

	if req.AccountID != "" && req.AccountID != e.accountID {
		err := fmt.Errorf("exec: request account %q does not match engine account %q", req.AccountID, e.accountID)
		cmd.Err = err.Error()
		return nil, err
	}
	if req.ClientID == "" {
		req.ClientID = e.nextClientID()
	}
	if req.AccountID == "" {
		req.AccountID = e.accountID
	}
	cmd.ClientID = req.ClientID
	if err := e.ensureClientIDAvailable(req.ClientID, true); err != nil {
		cmd.Err = err.Error()
		return nil, err
	}

	if e.gate != nil {
		if err := e.gate.CanSubmit(req); err != nil {
			cmd.Err = err.Error()
			return nil, err
		}
	}
	if err := e.ensureSupported(journal.CommandSubmit); err != nil {
		cmd.Err = err.Error()
		return nil, err
	}
	if validator, ok := e.client.(SubmitValidator); ok {
		if err := validator.ValidateSubmit(req); err != nil {
			cmd.Err = err.Error()
			return nil, err
		}
	}

	// Pre-trade risk gate. A rejection never touches the execution venue or the
	// cache. Context-aware risk may perform read-only venue validation and hand
	// exec a lease for the prepared payload.
	var preTradeLease contract.PreTradeLease
	var releaseRiskReservation func()
	if e.risk != nil {
		cmd.RiskStart = time.Now()
		var inst *model.Instrument
		if e.provider != nil {
			if got, ok := e.provider.Instrument(req.InstrumentID); ok {
				inst = got
			}
		}
		var err error
		if checker, ok := e.risk.(SubmissionRiskChecker); ok {
			preTradeLease, releaseRiskReservation, err = checker.CheckSubmission(ctx, req, inst)
			if releaseRiskReservation != nil {
				defer releaseRiskReservation()
			}
			if preTradeLease != nil {
				defer preTradeLease.Release()
			}
		} else if checker, ok := e.risk.(ContextRiskChecker); ok {
			preTradeLease, err = checker.CheckContext(ctx, req, inst)
			if preTradeLease != nil {
				defer preTradeLease.Release()
			}
		} else {
			err = e.risk.Check(req, inst)
		}
		if err != nil {
			cmd.RiskEnd = time.Now()
			cmd.Err = err.Error()
			return nil, err
		}
		cmd.RiskEnd = time.Now()
	}
	if err := ctx.Err(); err != nil {
		cmd.Err = err.Error()
		return nil, err
	}
	var preparedClient contract.PreparedExecutionClient
	if preTradeLease != nil {
		var ok bool
		preparedClient, ok = e.client.(contract.PreparedExecutionClient)
		if !ok {
			err := fmt.Errorf("exec: pre-trade lease requires prepared execution client: %w", contract.ErrNotSupported)
			cmd.Err = err.Error()
			return nil, err
		}
	}

	now := e.clk.Now()
	intent, intentErr := e.intent(journal.CommandSubmit, req, "", now)
	if intentErr != nil {
		cmd.Err = intentErr.Error()
		return nil, intentErr
	}
	if err := e.reserveAndAppendIntent(ctx, intent, InFlightSubmitted, true); err != nil {
		cmd.Err = err.Error()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		outcome := Outcome{Class: OutcomeLocalDenied, Sent: false, Err: err}
		if appendErr := e.appendResult(closeCtx, intent, outcome, ""); appendErr != nil {
			joined := errors.Join(err, appendErr)
			cmd.Err = joined.Error()
			return nil, joined
		}
		cmd.Err = err.Error()
		return nil, err
	}
	pendingOrder := model.Order{
		Request:   req,
		Status:    enums.StatusPendingNew,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if cacheErr := e.cache.InsertPendingOrderIfAbsent(pendingOrder); cacheErr != nil {
		err := errors.Join(
			fmt.Errorf("%w %q", ErrDuplicateClientID, req.ClientID),
			cacheErr,
		)
		outcome := Outcome{Class: OutcomeLocalDenied, Sent: false, Err: err}
		if appendErr := e.appendVenueResult(intent, outcome, ""); appendErr != nil {
			joined := errors.Join(err, appendErr)
			cmd.Err = joined.Error()
			return nil, joined
		}
		cmd.Err = err.Error()
		return nil, err
	}

	cmd.AdapterStart = time.Now()
	var order *model.Order
	var err error
	if preTradeLease != nil {
		order, err = preparedClient.SubmitPrepared(ctx, req)
	} else {
		order, err = e.client.Submit(ctx, req)
	}
	cmd.AdapterEnd = time.Now()
	sent := !errors.Is(err, contract.ErrPreparedStateUnavailable)
	if order != nil {
		normalized, identityErr := e.normalizeCommandOrder(req, *order, true)
		if identityErr != nil {
			identityErr = e.orderIdentityError("submit", identityErr)
			cmd.Err = identityErr.Error()
			return nil, identityErr
		}
		if normalized.CreatedAt.IsZero() {
			normalized.CreatedAt = now
		}
		if normalized.UpdatedAt.IsZero() {
			normalized.UpdatedAt = now
		}
		order = &normalized
	}
	outcome := ClassifySubmitResult(sent, order, err)
	if order != nil {
		cmd.VenueOrderID = order.VenueOrderID
	}
	switch outcome.Class {
	case OutcomeConfirmedAccepted:
		if commitErr := e.commitOrderUpsertResult("submit", intent, outcome, cmd.VenueOrderID, *order); commitErr != nil {
			if errors.Is(commitErr, ErrInFlightIntentAlreadyResolved) {
				return e.resultFromCanonical(intent)
			}
			cmd.Err = commitErr.Error()
			return nil, commitErr
		}
		e.notifyTerminalOrder(*order)
		cmd.CacheApplied = time.Now()
		return order, nil
	case OutcomeDefinitiveVenueRejected:
		rejectErr := err
		rejectReason := errorString(rejectErr)
		if rejectReason == "" && order != nil {
			rejectReason = order.RejectReason
		}
		if rejectErr == nil {
			rejectErr = DefinitiveReject(rejectReason)
		}
		rejected := model.Order{
			Request:      req,
			Status:       enums.StatusRejected,
			CreatedAt:    now,
			UpdatedAt:    e.clk.Now(),
			RejectReason: rejectReason,
		}
		if order != nil {
			rejected.VenueOrderID = order.VenueOrderID
		}
		if commitErr := e.commitOrderUpsertResult("submit", intent, outcome, cmd.VenueOrderID, rejected); commitErr != nil {
			if errors.Is(commitErr, ErrInFlightIntentAlreadyResolved) {
				return e.resultFromCanonical(intent)
			}
			cmd.Err = commitErr.Error()
			return nil, commitErr
		}
		e.notifyTerminalOrder(rejected)
		cmd.CacheApplied = time.Now()
		cmd.Err = errorString(rejectErr)
		return nil, rejectErr
	case OutcomeAmbiguous:
		if order != nil && order.VenueOrderID != "" {
			alias := pendingOrder
			alias.VenueOrderID = order.VenueOrderID
			if commitErr := e.commitOrderUpsertResult("submit", intent, outcome, order.VenueOrderID, alias); commitErr != nil {
				if errors.Is(commitErr, ErrInFlightIntentAlreadyResolved) {
					return e.resultFromCanonical(intent)
				}
				cmd.Err = commitErr.Error()
				return nil, commitErr
			}
		} else if appendErr := e.appendVenueResult(intent, outcome, cmd.VenueOrderID); appendErr != nil {
			if errors.Is(appendErr, ErrInFlightIntentAlreadyResolved) {
				return e.resultFromCanonical(intent)
			}
			cmd.Err = appendErr.Error()
			return nil, appendErr
		}
		cmd.Err = errorString(err)
		return nil, err
	case OutcomeLocalDenied, OutcomeUnsupported:
		rejected := model.Order{
			Request:      req,
			Status:       enums.StatusRejected,
			CreatedAt:    now,
			UpdatedAt:    e.clk.Now(),
			RejectReason: errorString(err),
		}
		if commitErr := e.commitOrderUpsertResult("submit", intent, outcome, cmd.VenueOrderID, rejected); commitErr != nil {
			if errors.Is(commitErr, ErrInFlightIntentAlreadyResolved) {
				return e.resultFromCanonical(intent)
			}
			cmd.Err = commitErr.Error()
			return nil, commitErr
		}
		e.notifyTerminalOrder(rejected)
		cmd.CacheApplied = time.Now()
		cmd.Err = errorString(err)
		return nil, err
	default:
		if appendErr := e.appendVenueResult(intent, outcome, cmd.VenueOrderID); appendErr != nil {
			if errors.Is(appendErr, ErrInFlightIntentAlreadyResolved) {
				return e.resultFromCanonical(intent)
			}
			cmd.Err = appendErr.Error()
			return nil, appendErr
		}
		cmd.Err = errorString(err)
		return nil, err
	}
}

// Cancel cancels a known order by client id, resolving its venue id from the
// cache.
func (e *Engine) Cancel(ctx context.Context, clientID string) error {
	if e.gate != nil {
		if err := e.gate.CanCancel(); err != nil {
			return err
		}
	}
	if err := e.ensureSupported(journal.CommandCancel); err != nil {
		return err
	}
	if err := e.ensureClientIDAvailable(clientID, false); err != nil {
		return err
	}
	// Re-read only after the in-flight check. A preceding result transaction may
	// have been durably committing a replacement alias while an earlier cache
	// read still exposed the old order incarnation.
	o, ok := e.cache.OrderByClientIDForAccount(e.accountID, clientID)
	if !ok {
		return fmt.Errorf("exec: unknown order %q", clientID)
	}
	intent, intentErr := e.intent(journal.CommandCancel, o.Request, o.VenueOrderID, e.clk.Now())
	if intentErr != nil {
		return intentErr
	}
	if err := e.reserveAndAppendIntent(ctx, intent, InFlightPendingCancel, false); err != nil {
		return err
	}
	err := e.client.Cancel(ctx, o.Request.InstrumentID, o.VenueOrderID)
	outcome := ClassifyCommandResult(true, err)
	if outcome.Class == OutcomeConfirmedAccepted {
		canonical, commitErr := e.commitOrderMutationResult(
			"cancel",
			intent,
			outcome,
			o.VenueOrderID,
			clientID,
			o.VenueOrderID,
			func(current model.Order) (model.Order, error) {
				// A terminal event that won the venue race is stronger than the
				// synchronous cancel acknowledgement. Otherwise preserve every
				// latest field and apply the confirmed terminal state.
				if !orderstate.IsTerminal(current.Status) {
					current.Status = enums.StatusCanceled
				}
				at := e.clk.Now()
				if current.UpdatedAt.IsZero() || at.After(current.UpdatedAt) {
					current.UpdatedAt = at
				}
				return current, nil
			},
		)
		if commitErr != nil {
			if errors.Is(commitErr, ErrInFlightIntentAlreadyResolved) {
				_, resolvedErr := e.resultFromCanonical(intent)
				return resolvedErr
			}
			return commitErr
		}
		e.notifyTerminalOrder(canonical)
		return err
	}
	if appendErr := e.appendVenueResult(intent, outcome, o.VenueOrderID); appendErr != nil {
		if errors.Is(appendErr, ErrInFlightIntentAlreadyResolved) {
			_, resolvedErr := e.resultFromCanonical(intent)
			return resolvedErr
		}
		return appendErr
	}
	return err
}

func (e *Engine) Modify(ctx context.Context, clientID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	if e.gate != nil {
		if err := e.gate.CanModify(); err != nil {
			return nil, err
		}
	}
	if err := e.ensureSupported(journal.CommandModify); err != nil {
		return nil, err
	}
	if err := e.ensureClientIDAvailable(clientID, false); err != nil {
		return nil, err
	}
	// Read the target only after the in-flight check for the same reason as
	// Cancel: result/cache transactions keep the prior intent visible until the
	// replacement alias has been applied.
	o, ok := e.cache.OrderByClientIDForAccount(e.accountID, clientID)
	if !ok {
		return nil, fmt.Errorf("exec: unknown order %q", clientID)
	}
	req := amendRequest(o.Request, newPrice, newQty)
	intent, intentErr := e.intent(journal.CommandModify, req, o.VenueOrderID, e.clk.Now())
	if intentErr != nil {
		return nil, intentErr
	}
	if err := e.reserveAndAppendIntent(ctx, intent, InFlightPendingModify, false); err != nil {
		return nil, err
	}
	order, err := e.client.Modify(ctx, o.Request.InstrumentID, o.VenueOrderID, newPrice, newQty)
	if order != nil {
		normalized, identityErr := e.normalizeCommandOrder(req, *order, false)
		if identityErr != nil {
			return nil, e.orderIdentityError("modify", identityErr)
		}
		order = &normalized
	}
	outcome := ClassifySubmitResult(true, order, err)
	if outcome.Class == OutcomeDefinitiveVenueRejected && err == nil {
		reason := ""
		if order != nil {
			reason = order.RejectReason
		}
		err = DefinitiveReject(reason)
		outcome.Err = err
	}
	resultVenueOrderID := o.VenueOrderID
	responseVenueOrderID := ""
	if order != nil {
		responseVenueOrderID = order.VenueOrderID
	}
	if outcome.Class == OutcomeConfirmedAccepted && order != nil {
		if responseVenueOrderID != "" {
			resultVenueOrderID = responseVenueOrderID
		}
		var commitErr error
		var canonical model.Order
		if resultVenueOrderID != o.VenueOrderID {
			merged := mergeAcceptedModifyOrder(o, req, *order, e.clk.Now())
			commitErr = e.commitOrderVenueAliasChangeResult(
				"modify", intent, outcome, resultVenueOrderID,
				o.Request.ClientID, o.VenueOrderID, merged,
			)
			canonical = merged
		} else {
			canonical, commitErr = e.commitOrderMutationResult(
				"modify",
				intent,
				outcome,
				resultVenueOrderID,
				o.Request.ClientID,
				o.VenueOrderID,
				func(current model.Order) (model.Order, error) {
					return mergeAcceptedModifyOrder(current, req, *order, e.clk.Now()), nil
				},
			)
		}
		if commitErr != nil {
			if errors.Is(commitErr, ErrInFlightIntentAlreadyResolved) {
				return e.resultFromCanonical(intent)
			}
			return nil, commitErr
		}
		if cached, found := e.cache.OrderByClientIDForAccount(e.accountID, o.Request.ClientID); found {
			canonical = cached
		}
		e.notifyTerminalOrder(canonical)
		return &canonical, nil
	}
	if order != nil && responseVenueOrderID != "" {
		candidate := mergeAcceptedModifyOrder(o, req, *order, e.clk.Now())
		var commitErr error
		if responseVenueOrderID != o.VenueOrderID {
			commitErr = e.validateOrderVenueAliasChangeAndCommitResult(
				"modify", intent, outcome, resultVenueOrderID,
				o.Request.ClientID, o.VenueOrderID, candidate,
			)
		} else {
			commitErr = e.validateOrderUpsertAndCommitResult("modify", intent, outcome, resultVenueOrderID, candidate)
		}
		if commitErr != nil {
			if errors.Is(commitErr, ErrInFlightIntentAlreadyResolved) {
				return e.resultFromCanonical(intent)
			}
			return nil, commitErr
		}
	} else if appendErr := e.appendVenueResult(intent, outcome, resultVenueOrderID); appendErr != nil {
		if errors.Is(appendErr, ErrInFlightIntentAlreadyResolved) {
			return e.resultFromCanonical(intent)
		}
		return nil, appendErr
	}
	if outcome.Class == OutcomeDefinitiveVenueRejected {
		return nil, err
	}
	return order, err
}

func (e *Engine) orderIdentityError(command string, identityErr error) error {
	err := fmt.Errorf("exec: %s order identity conflict: %w", command, identityErr)
	e.notifyRecoverabilityBreach(err)
	return err
}

func (e *Engine) commitOrderUpsertResult(
	command string,
	intent journal.CommandIntent,
	outcome Outcome,
	venueOrderID string,
	order model.Order,
) error {
	var finalize func()
	err := e.cache.CommitOrderUpsertChecked(order, func() error {
		var prepareErr error
		finalize, prepareErr = e.prepareVenueResultDeferredRecovery(intent, outcome, venueOrderID)
		return prepareErr
	})
	if err = e.wrapOrderCommitError(command, err); err != nil {
		return err
	}
	if finalize != nil {
		finalize()
	}
	return nil
}

func (e *Engine) commitOrderMutationResult(
	command string,
	intent journal.CommandIntent,
	outcome Outcome,
	venueOrderID, clientID, expectedVenueOrderID string,
	mutate func(model.Order) (model.Order, error),
) (model.Order, error) {
	var finalize func()
	canonical, err := e.cache.CommitOrderMutationByClientIDChecked(
		e.accountID,
		clientID,
		expectedVenueOrderID,
		mutate,
		func() error {
			var prepareErr error
			finalize, prepareErr = e.prepareVenueResultDeferredRecovery(intent, outcome, venueOrderID)
			return prepareErr
		},
	)
	if err = e.wrapOrderCommitError(command, err); err != nil {
		return model.Order{}, err
	}
	if finalize != nil {
		finalize()
	}
	return canonical, nil
}

func (e *Engine) validateOrderUpsertAndCommitResult(
	command string,
	intent journal.CommandIntent,
	outcome Outcome,
	venueOrderID string,
	order model.Order,
) error {
	var finalize func()
	err := e.cache.ValidateOrderUpsertAndCommit(order, func() error {
		var prepareErr error
		finalize, prepareErr = e.prepareVenueResultDeferredRecovery(intent, outcome, venueOrderID)
		return prepareErr
	})
	if err = e.wrapOrderCommitError(command, err); err != nil {
		return err
	}
	if finalize != nil {
		finalize()
	}
	return nil
}

func (e *Engine) commitOrderVenueAliasChangeResult(
	command string,
	intent journal.CommandIntent,
	outcome Outcome,
	venueOrderID, clientID, expectedVenueOrderID string,
	order model.Order,
) error {
	var finalize func()
	err := e.cache.CommitOrderVenueAliasChangeChecked(
		e.accountID,
		clientID,
		expectedVenueOrderID,
		order,
		func() error {
			var prepareErr error
			finalize, prepareErr = e.prepareVenueResultDeferredRecovery(intent, outcome, venueOrderID)
			return prepareErr
		},
	)
	if err = e.wrapOrderCommitError(command, err); err != nil {
		return err
	}
	if finalize != nil {
		finalize()
	}
	return nil
}

func (e *Engine) validateOrderVenueAliasChangeAndCommitResult(
	command string,
	intent journal.CommandIntent,
	outcome Outcome,
	venueOrderID, clientID, expectedVenueOrderID string,
	order model.Order,
) error {
	var finalize func()
	err := e.cache.ValidateOrderVenueAliasChangeAndCommit(
		e.accountID,
		clientID,
		expectedVenueOrderID,
		order,
		func() error {
			var prepareErr error
			finalize, prepareErr = e.prepareVenueResultDeferredRecovery(intent, outcome, venueOrderID)
			return prepareErr
		},
	)
	if err = e.wrapOrderCommitError(command, err); err != nil {
		return err
	}
	if finalize != nil {
		finalize()
	}
	return nil
}

func (e *Engine) wrapOrderCommitError(command string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInFlightIntentAlreadyResolved) {
		return err
	}
	if !errors.Is(err, cache.ErrOrderIdentityConflict) {
		e.notifyRecoverabilityBreach(err)
		return err
	}
	return e.orderIdentityError(command, err)
}

// resultFromCanonical maps an authoritative event that already resolved an
// intent back to the synchronous command result. It never mutates cache or
// appends a second result: the first definitive evidence remains authoritative.
func (e *Engine) resultFromCanonical(intent journal.CommandIntent) (*model.Order, error) {
	canonical, ok := e.cache.OrderByClientIDForAccount(e.accountID, intent.ClientID)
	if !ok {
		return nil, fmt.Errorf("%w: canonical order %q is unavailable", ErrInFlightIntentAlreadyResolved, intent.ClientID)
	}
	switch intent.Type {
	case journal.CommandSubmit:
		if canonical.Status == enums.StatusRejected || canonical.Status == enums.StatusExpired {
			return nil, DefinitiveReject(canonical.RejectReason)
		}
		if canonical.Status == enums.StatusPendingNew || canonical.Status == enums.StatusUnknown {
			return nil, fmt.Errorf("%w: submit outcome is not definitive in canonical cache", ErrInFlightIntentAlreadyResolved)
		}
		return &canonical, nil
	case journal.CommandCancel:
		if canonical.Status == enums.StatusCanceled {
			return &canonical, nil
		}
		if orderstate.IsTerminal(canonical.Status) {
			return nil, DefinitiveReject(canonical.RejectReason)
		}
		return nil, fmt.Errorf("%w: cancel outcome is not definitive in canonical cache", ErrInFlightIntentAlreadyResolved)
	case journal.CommandModify:
		if canonical.Status == enums.StatusRejected || canonical.Status == enums.StatusExpired {
			return nil, DefinitiveReject(canonical.RejectReason)
		}
		if canonical.Request.Price.Equal(intent.Price) && canonical.Request.Quantity.Equal(intent.Quantity) {
			return &canonical, nil
		}
		if orderstate.IsTerminal(canonical.Status) {
			return nil, DefinitiveReject(canonical.RejectReason)
		}
		return nil, fmt.Errorf("%w: modify outcome is not definitive in canonical cache", ErrInFlightIntentAlreadyResolved)
	default:
		return nil, fmt.Errorf("%w: unsupported command type %q", ErrInFlightIntentAlreadyResolved, intent.Type)
	}
}

func (e *Engine) notifyRecoverabilityBreach(err error) {
	if err != nil && e.onRecoverabilityBreach != nil {
		e.onRecoverabilityBreach(err)
	}
}

func (e *Engine) normalizeCommandOrder(expected model.OrderRequest, response model.Order, strictClientID bool) (model.Order, error) {
	got := response.Request
	if got.AccountID != "" && expected.AccountID != "" && got.AccountID != expected.AccountID {
		return model.Order{}, orderResponseIdentityConflict("response account %q does not match request account %q", got.AccountID, expected.AccountID)
	}
	if got.ClientID != "" && got.ClientID != expected.ClientID {
		if strictClientID {
			return model.Order{}, orderResponseIdentityConflict("response client id %q does not match request client id %q", got.ClientID, expected.ClientID)
		}
		accountID := expected.AccountID
		if accountID == "" {
			accountID = e.accountID
		}
		if existing, ok := e.cache.OrderByClientIDForAccount(accountID, got.ClientID); ok && existing.Request.ClientID != expected.ClientID {
			return model.Order{}, orderResponseIdentityConflict("response client id %q belongs to another cached order", got.ClientID)
		}
	}
	if got.InstrumentID != (model.InstrumentID{}) && expected.InstrumentID != (model.InstrumentID{}) && got.InstrumentID != expected.InstrumentID {
		return model.Order{}, orderResponseIdentityConflict("response instrument %s does not match request instrument %s", got.InstrumentID, expected.InstrumentID)
	}
	if got.Side != enums.SideUnknown && expected.Side != enums.SideUnknown && got.Side != expected.Side {
		return model.Order{}, orderResponseIdentityConflict("response side %s does not match request side %s", got.Side, expected.Side)
	}
	if got.Type != enums.TypeUnknown && expected.Type != enums.TypeUnknown && got.Type != expected.Type {
		return model.Order{}, orderResponseIdentityConflict("response order type %s does not match request type %s", got.Type, expected.Type)
	}
	if got.TIF != enums.TifUnknown && expected.TIF != enums.TifUnknown && got.TIF != expected.TIF {
		return model.Order{}, orderResponseIdentityConflict("response time in force %s does not match request time in force %s", got.TIF, expected.TIF)
	}
	if got.PositionSide != enums.PosNet && got.PositionSide != expected.PositionSide {
		return model.Order{}, orderResponseIdentityConflict("response position side %s does not match request position side %s", got.PositionSide, expected.PositionSide)
	}
	if got.ReduceOnly && !expected.ReduceOnly {
		return model.Order{}, orderResponseIdentityConflict("response reduce-only flag does not match request")
	}
	response.Request = mergeModifyRequest(expected, got)
	response.Request.AccountID = expected.AccountID
	response.Request.ClientID = expected.ClientID
	return response, nil
}

func orderResponseIdentityConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", cache.ErrOrderIdentityConflict, fmt.Sprintf(format, args...))
}

func amendRequest(req model.OrderRequest, newPrice, newQty decimal.Decimal) model.OrderRequest {
	if !newPrice.IsZero() {
		req.Price = newPrice
	}
	if !newQty.IsZero() {
		req.Quantity = newQty
	}
	return req
}

func mergeAcceptedModifyOrder(cached model.Order, amendedReq model.OrderRequest, venue model.Order, fallbackUpdatedAt time.Time) model.Order {
	if venue.VenueOrderID != "" && cached.VenueOrderID != "" && venue.VenueOrderID != cached.VenueOrderID {
		status := venue.Status
		if status == enums.StatusUnknown {
			status = enums.StatusNew
		}
		createdAt := venue.CreatedAt
		if createdAt.IsZero() {
			createdAt = fallbackUpdatedAt
		}
		updatedAt := venue.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = fallbackUpdatedAt
		}
		return model.Order{
			Request:      mergeModifyRequest(amendedReq, venue.Request),
			VenueOrderID: venue.VenueOrderID,
			Status:       status,
			FilledQty:    venue.FilledQty,
			AvgFillPrice: venue.AvgFillPrice,
			CreatedAt:    createdAt,
			UpdatedAt:    updatedAt,
			RejectReason: venue.RejectReason,
		}
	}
	out := cached
	out.Request = mergeModifyRequest(amendedReq, venue.Request)
	if venue.VenueOrderID != "" {
		out.VenueOrderID = venue.VenueOrderID
	}
	if venue.Status != enums.StatusUnknown && !orderstate.IsTerminal(cached.Status) {
		out.Status = venue.Status
	}
	if venue.FilledQty.GreaterThan(cached.FilledQty) {
		out.FilledQty = venue.FilledQty
		if !venue.AvgFillPrice.IsZero() {
			out.AvgFillPrice = venue.AvgFillPrice
		}
	}
	if !venue.CreatedAt.IsZero() {
		out.CreatedAt = venue.CreatedAt
	}
	out.UpdatedAt = cached.UpdatedAt
	if venue.UpdatedAt.After(out.UpdatedAt) {
		out.UpdatedAt = venue.UpdatedAt
	}
	if fallbackUpdatedAt.After(out.UpdatedAt) {
		out.UpdatedAt = fallbackUpdatedAt
	}
	if venue.RejectReason != "" && !orderstate.IsTerminal(cached.Status) {
		out.RejectReason = venue.RejectReason
	}
	if out.FilledQty.IsPositive() && !orderstate.IsTerminal(out.Status) {
		out.Status = enums.StatusPartiallyFilled
	}
	return out
}

func mergeModifyRequest(base, venue model.OrderRequest) model.OrderRequest {
	out := base
	if venue.InstrumentID.Symbol != "" {
		out.InstrumentID = venue.InstrumentID
	}
	if venue.Side != enums.SideUnknown {
		out.Side = venue.Side
	}
	if venue.Type != enums.TypeUnknown {
		out.Type = venue.Type
	}
	if venue.TIF != enums.TifUnknown {
		out.TIF = venue.TIF
	}
	if !venue.Quantity.IsZero() {
		out.Quantity = venue.Quantity
	}
	if !venue.Price.IsZero() {
		out.Price = venue.Price
	}
	if !venue.TriggerPrice.IsZero() {
		out.TriggerPrice = venue.TriggerPrice
	}
	if !venue.ActivationPrice.IsZero() {
		out.ActivationPrice = venue.ActivationPrice
	}
	if !venue.TrailingOffsetBps.IsZero() {
		out.TrailingOffsetBps = venue.TrailingOffsetBps
	}
	if venue.PositionSide != enums.PosNet || out.PositionSide == enums.PosNet {
		out.PositionSide = venue.PositionSide
	}
	if venue.ReduceOnly {
		out.ReduceOnly = true
	}
	if venue.Venue != nil {
		out.Venue = venue.Venue
	}
	return out
}

func (e *Engine) ensureSupported(cmd journal.CommandType) error {
	caps := e.client.Capabilities()
	var ok bool
	switch cmd {
	case journal.CommandSubmit:
		ok = caps.Trading.Submit
	case journal.CommandCancel:
		ok = caps.Trading.Cancel
	case journal.CommandModify:
		ok = caps.Trading.Modify
	}
	if ok {
		return nil
	}
	return fmt.Errorf("exec: %s unsupported by venue: %w", cmd, contract.ErrNotSupported)
}

func (e *Engine) intent(cmd journal.CommandType, req model.OrderRequest, venueOrderID string, at time.Time) (journal.CommandIntent, error) {
	var nonce [16]byte
	entropy := e.commandEntropy
	if entropy == nil {
		entropy = rand.Reader
	}
	if _, err := io.ReadFull(entropy, nonce[:]); err != nil {
		return journal.CommandIntent{}, fmt.Errorf("exec: generate command identity: %w", err)
	}
	commandID := journal.NewRecordID(
		"command",
		e.accountID,
		string(cmd),
		req.ClientID,
		venueOrderID,
		at.Format(time.RFC3339Nano),
		hex.EncodeToString(nonce[:]),
	)
	return journal.CommandIntent{
		RecordID:      journal.NewRecordID("intent", commandID),
		CommandID:     commandID,
		Type:          cmd,
		ClientID:      req.ClientID,
		VenueOrderID:  venueOrderID,
		InstrumentID:  req.InstrumentID,
		Side:          req.Side,
		OrderType:     req.Type,
		TIF:           req.TIF,
		Quantity:      req.Quantity,
		Price:         req.Price,
		ReduceOnly:    req.ReduceOnly,
		AccountID:     e.accountID,
		SubmittedAt:   at,
		CorrelationID: commandID,
		Attempt:       1,
	}, nil
}

func (e *Engine) appendResult(ctx context.Context, intent journal.CommandIntent, outcome Outcome, venueOrderID string) error {
	return e.appendResultWithRecovery(ctx, intent, outcome, venueOrderID, true)
}

func (e *Engine) appendResultWithRecovery(
	ctx context.Context,
	intent journal.CommandIntent,
	outcome Outcome,
	venueOrderID string,
	notifyRecovery bool,
) error {
	result := e.commandResult(intent, outcome, venueOrderID)
	return e.commitCommandResultWithRecovery(ctx, result, notifyRecovery)
}

func (e *Engine) commandResult(intent journal.CommandIntent, outcome Outcome, venueOrderID string) journal.CommandResult {
	return journal.CommandResult{
		RecordID:       journal.NewRecordID("result", intent.RecordID, string(outcome.Class), venueOrderID, e.clk.Now().Format(time.RFC3339Nano)),
		IntentRecordID: intent.RecordID,
		CommandID:      intent.CommandID,
		Type:           intent.Type,
		ClientID:       intent.ClientID,
		VenueOrderID:   venueOrderID,
		Outcome:        string(outcome.Class),
		Error:          errorString(outcome.Err),
		ResultAt:       e.clk.Now(),
	}
}

func (e *Engine) commitCommandResult(ctx context.Context, result journal.CommandResult) error {
	return e.commitCommandResultWithRecovery(ctx, result, true)
}

func (e *Engine) commitCommandResultWithRecovery(ctx context.Context, result journal.CommandResult, notifyRecovery bool) error {
	err := e.inflight.commitResult(result, func() error {
		return e.journal.AppendCommandResult(ctx, result)
	})
	if notifyRecovery && !errors.Is(err, ErrInFlightIntentAlreadyResolved) {
		e.notifyRecoverabilityBreach(err)
	}
	return err
}

func (e *Engine) appendVenueResult(intent journal.CommandIntent, outcome Outcome, venueOrderID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return e.appendResult(ctx, intent, outcome, venueOrderID)
}

func (e *Engine) prepareVenueResultDeferredRecovery(
	intent journal.CommandIntent,
	outcome Outcome,
	venueOrderID string,
) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := e.commandResult(intent, outcome, venueOrderID)
	return e.inflight.prepareResultCommit(result, func() error {
		return e.journal.AppendCommandResult(ctx, result)
	})
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

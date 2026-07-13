// Package exec provides the ExecutionEngine: the runtime's order-submission
// front door. It assigns a stable ClientID, records the intended order in the
// Cache as PendingNew, submits through the venue-neutral ExecutionClient, and
// records the acknowledged order. Subsequent lifecycle/fill events flow in via
// the bus, not here.
package exec

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/latency"
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

	onRecoverabilityBreach func(error)
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
		client:    client,
		cache:     c,
		clk:       clk,
		prefix:    idPrefix,
		accountID: idPrefix,
		journal:   journal.NewMemory(),
		inflight:  NewInFlightJournal(),
	}
}

// WithRisk attaches a pre-trade risk gate and an instrument provider for
// instrument-level checks (provider may be nil).
func (e *Engine) WithRisk(r RiskChecker, provider model.InstrumentProvider) *Engine {
	e.risk = r
	e.provider = provider
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

func (e *Engine) ResolveFillInFlight(fill model.Fill, at time.Time) (model.Fill, bool) {
	if e.inflight == nil {
		return fill, false
	}
	entry, ok := e.inflight.MatchFill(fill)
	if !ok {
		return fill, false
	}
	if fill.ClientID == "" {
		fill.ClientID = entry.Intent.ClientID
	}
	if fill.VenueOrderID == "" {
		fill.VenueOrderID = entry.Intent.VenueOrderID
	}
	if !e.resolveEntry(entry, fill.VenueOrderID, OutcomeConfirmedAccepted, "", at) {
		return fill, false
	}
	return fill, true
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
	if e.journal != nil {
		if err := e.journal.AppendCommandResult(context.Background(), result); err != nil {
			if e.onRecoverabilityBreach != nil {
				e.onRecoverabilityBreach(err)
			}
			return false
		}
	}
	e.inflight.ApplyResult(result)
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
	e.inflight.ReplayOpenIntents(intents)
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

	if req.ClientID == "" {
		req.ClientID = e.nextClientID()
	}
	if req.AccountID == "" {
		req.AccountID = e.accountID
	}
	cmd.ClientID = req.ClientID

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
	if e.risk != nil {
		cmd.RiskStart = time.Now()
		var inst *model.Instrument
		if e.provider != nil {
			if got, ok := e.provider.Instrument(req.InstrumentID); ok {
				inst = got
			}
		}
		var err error
		if checker, ok := e.risk.(ContextRiskChecker); ok {
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
	intent := e.intent(journal.CommandSubmit, req, "", now)
	if err := e.journal.AppendCommandIntent(ctx, intent); err != nil {
		cmd.Err = err.Error()
		return nil, err
	}
	e.inflight.TrackIntent(intent, InFlightSubmitted)
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
	e.cache.UpsertOrder(model.Order{
		Request:   req,
		Status:    enums.StatusPendingNew,
		CreatedAt: now,
		UpdatedAt: now,
	})

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
	outcome := ClassifySubmitResult(sent, order, err)
	if order != nil {
		cmd.VenueOrderID = order.VenueOrderID
	}
	if appendErr := e.appendResult(ctx, intent, outcome, cmd.VenueOrderID); appendErr != nil {
		cmd.Err = appendErr.Error()
		return nil, appendErr
	}
	switch outcome.Class {
	case OutcomeConfirmedAccepted:
		e.cache.UpsertOrder(*order)
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
		e.cache.UpsertOrder(rejected)
		cmd.CacheApplied = time.Now()
		cmd.Err = errorString(rejectErr)
		return nil, rejectErr
	case OutcomeAmbiguous:
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
		e.cache.UpsertOrder(rejected)
		cmd.CacheApplied = time.Now()
		cmd.Err = errorString(err)
		return nil, err
	default:
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
	o, ok := e.cache.OrderForAccount(e.accountID, clientID)
	if !ok {
		return fmt.Errorf("exec: unknown order %q", clientID)
	}
	intent := e.intent(journal.CommandCancel, o.Request, o.VenueOrderID, e.clk.Now())
	if err := e.journal.AppendCommandIntent(ctx, intent); err != nil {
		return err
	}
	e.inflight.TrackIntent(intent, InFlightPendingCancel)
	err := e.client.Cancel(ctx, o.Request.InstrumentID, o.VenueOrderID)
	outcome := ClassifyCommandResult(true, err)
	if appendErr := e.appendResult(ctx, intent, outcome, o.VenueOrderID); appendErr != nil {
		return appendErr
	}
	if outcome.Class == OutcomeConfirmedAccepted {
		o.Status = enums.StatusCanceled
		o.UpdatedAt = e.clk.Now()
		e.cache.UpsertOrder(o)
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
	o, ok := e.cache.OrderForAccount(e.accountID, clientID)
	if !ok {
		return nil, fmt.Errorf("exec: unknown order %q", clientID)
	}
	req := amendRequest(o.Request, newPrice, newQty)
	intent := e.intent(journal.CommandModify, req, o.VenueOrderID, e.clk.Now())
	if err := e.journal.AppendCommandIntent(ctx, intent); err != nil {
		return nil, err
	}
	e.inflight.TrackIntent(intent, InFlightPendingModify)
	order, err := e.client.Modify(ctx, o.Request.InstrumentID, o.VenueOrderID, newPrice, newQty)
	outcome := ClassifySubmitResult(true, order, err)
	venueOrderID := o.VenueOrderID
	if order != nil && order.VenueOrderID != "" {
		venueOrderID = order.VenueOrderID
	}
	if appendErr := e.appendResult(ctx, intent, outcome, venueOrderID); appendErr != nil {
		return nil, appendErr
	}
	if outcome.Class == OutcomeConfirmedAccepted && order != nil {
		merged := mergeAcceptedModifyOrder(o, req, *order, e.clk.Now())
		e.cache.UpsertOrder(merged)
		return &merged, nil
	}
	return order, err
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
	out := cached
	out.Request = mergeModifyRequest(amendedReq, venue.Request)
	if venue.VenueOrderID != "" {
		out.VenueOrderID = venue.VenueOrderID
	}
	if venue.Status != enums.StatusUnknown {
		out.Status = venue.Status
	}
	if !venue.FilledQty.IsZero() {
		out.FilledQty = venue.FilledQty
	}
	if !venue.AvgFillPrice.IsZero() {
		out.AvgFillPrice = venue.AvgFillPrice
	}
	if !venue.CreatedAt.IsZero() {
		out.CreatedAt = venue.CreatedAt
	}
	out.UpdatedAt = venue.UpdatedAt
	if out.UpdatedAt.IsZero() {
		out.UpdatedAt = fallbackUpdatedAt
	}
	if venue.RejectReason != "" {
		out.RejectReason = venue.RejectReason
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

func (e *Engine) intent(cmd journal.CommandType, req model.OrderRequest, venueOrderID string, at time.Time) journal.CommandIntent {
	commandID := journal.NewRecordID("command", string(cmd), req.ClientID, venueOrderID, at.Format(time.RFC3339Nano))
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
	}
}

func (e *Engine) appendResult(ctx context.Context, intent journal.CommandIntent, outcome Outcome, venueOrderID string) error {
	result := journal.CommandResult{
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
	if err := e.journal.AppendCommandResult(ctx, result); err != nil {
		if e.onRecoverabilityBreach != nil {
			e.onRecoverabilityBreach(err)
		}
		return err
	}
	e.inflight.ApplyResult(result)
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

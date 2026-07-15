// Package runtimetest provides an in-memory fake venue that implements the
// core/contract interfaces. It lets the runtime be exercised end-to-end with no
// network. It is intentionally simple: Submit immediately acks, while tests push
// the live-style order, fill, reject, balance, position, quote, and trade events
// explicitly through the same channels real adapters use.
package runtimetest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// FakeExec is an in-memory ExecutionClient. Submit synchronously returns an
// acknowledged order (status New) and the test injects fills/order events via
// the Emit* helpers, which land on Events() exactly like a real venue push.
type FakeExec struct {
	events    chan contract.ExecEnvelope
	seq       int64
	accountID string
	clk       clock.Clock

	// reports is the canned venue-wide open-order snapshot returned by mass
	// status generation; set it to drive reconciliation tests.
	reports     []model.Order
	instruments []model.InstrumentID
	reportErr   error

	submitErr       error
	cancelErr       error
	modifyErr       error
	submitOrder     *model.Order
	modifyOrder     *model.Order
	onSubmit        func(model.OrderRequest)
	onCancel        func(model.InstrumentID, string)
	onModify        func(model.InstrumentID, string, decimal.Decimal, decimal.Decimal)
	modifySupported bool
}

const fakeAccountID = "FAKE"

// NewFakeExec returns a FakeExec with a buffered event channel.
func NewFakeExec() *FakeExec {
	return &FakeExec{
		events:    make(chan contract.ExecEnvelope, 256),
		accountID: fakeAccountID,
		clk:       clock.NewRealClock(),
	}
}

func (f *FakeExec) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: f.configuredVenue(),
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Trading: true},
			{Kind: enums.KindPerp, Trading: true},
		},
		Reports: contract.ReportCapabilities{
			SingleOrderStatus:         true,
			OpenOrders:                true,
			OpenOnlyNotFoundAmbiguous: false,
		},
		Streaming: contract.StreamCapabilities{Execution: true},
		Trading:   contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true, Modify: f.modifySupported},
	}
}

func (f *FakeExec) AccountID() string { return f.accountID }

// WithClock uses clk for every fake report observation timestamp. Runtime
// tests should pass the same clock to FakeExec and TradingNode.
func (f *FakeExec) WithClock(clk clock.Clock) *FakeExec {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	f.clk = clk
	return f
}

func (f *FakeExec) now() time.Time {
	if f.clk == nil {
		return clock.NewRealClock().Now()
	}
	return f.clk.Now()
}

func (f *FakeExec) configuredVenue() string {
	for _, report := range f.reports {
		if venue := strings.TrimSpace(report.Request.InstrumentID.Venue); venue != "" {
			return venue
		}
	}
	for _, id := range f.instruments {
		if venue := strings.TrimSpace(id.Venue); venue != "" {
			return venue
		}
	}
	return "FAKE"
}

func (f *FakeExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if f.onSubmit != nil {
		f.onSubmit(req)
	}
	if f.submitErr != nil {
		return f.submitOrder, f.submitErr
	}
	if f.submitOrder != nil {
		order := *f.submitOrder
		if order.Request.ClientID == "" {
			order.Request = req
		}
		return &order, nil
	}
	f.seq++
	venueID := "v" + decimal.NewFromInt(f.seq).String()
	return &model.Order{
		Request:      req,
		VenueOrderID: venueID,
		Status:       enums.StatusNew,
	}, nil
}

func (f *FakeExec) ValidateSubmit(req model.OrderRequest) error { return nil }

func (f *FakeExec) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	if f.onCancel != nil {
		f.onCancel(id, venueOrderID)
	}
	return f.cancelErr
}
func (f *FakeExec) CancelAll(ctx context.Context, id model.InstrumentID) error { return nil }
func (f *FakeExec) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	if f.onModify != nil {
		f.onModify(id, venueOrderID, newPrice, newQty)
	}
	if !f.modifySupported {
		return nil, fmt.Errorf("fake execution amend is not modeled: %w", contract.ErrNotSupported)
	}
	if f.modifyErr != nil {
		return f.modifyOrder, f.modifyErr
	}
	if f.modifyOrder != nil {
		order := *f.modifyOrder
		return &order, nil
	}
	return &model.Order{
		Request: model.OrderRequest{
			InstrumentID: id,
			Price:        newPrice,
			Quantity:     newQty,
		},
		VenueOrderID: venueOrderID,
		Status:       enums.StatusNew,
	}, nil
}
func (f *FakeExec) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	out := make([]model.Order, 0, len(f.reports))
	for _, o := range f.reports {
		if o.Request.InstrumentID == id {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *FakeExec) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if f.reportErr != nil {
		return nil, f.reportErr
	}
	reportedAt := f.now()
	out := make([]model.OrderStatusReport, 0, len(f.reports))
	for _, o := range f.reports {
		if !model.OrderMatchesStatusQuery(o, query) {
			continue
		}
		accountID := query.AccountID
		if accountID == "" {
			accountID = o.Request.AccountID
		}
		out = append(out, model.OrderStatusReport{Venue: o.Request.InstrumentID.Venue, AccountID: accountID, Order: o, ReportedAt: reportedAt})
	}
	return out, nil
}

func (f *FakeExec) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	reports, err := f.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{
		InstrumentID: query.InstrumentID,
		AccountID:    query.AccountID,
		ClientID:     query.ClientID,
		VenueOrderID: query.VenueOrderID,
	})
	if err != nil || len(reports) == 0 {
		return nil, err
	}
	return &reports[0], nil
}

func (f *FakeExec) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	return nil, fmt.Errorf("fake execution fill history is not modeled: %w", contract.ErrNotSupported)
}

func (f *FakeExec) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	return nil, fmt.Errorf("fake execution position reports are served by FakeAccount snapshots: %w", contract.ErrNotSupported)
}

func (f *FakeExec) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	requestStartedAt := f.now()
	if f.reportErr != nil {
		return nil, f.reportErr
	}
	venue, err := f.massStatusVenue(query)
	if err != nil {
		return nil, err
	}
	accountID, err := f.massStatusAccount(query)
	if err != nil {
		return nil, err
	}
	availableIDs, err := f.massStatusInstruments(venue)
	if err != nil {
		return nil, err
	}
	ids, err := massStatusSelector(availableIDs, query.InstrumentIDs, venue)
	if err != nil {
		return nil, err
	}
	selected := make(map[model.InstrumentID]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	now := requestStartedAt
	mass := model.NewExecutionMassStatus(venue, accountID, now)
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	for _, o := range f.reports {
		if query.ClientID != "" && o.Request.ClientID != query.ClientID {
			continue
		}
		if _, ok := selected[o.Request.InstrumentID]; !ok {
			continue
		}
		report := model.OrderStatusReport{Venue: o.Request.InstrumentID.Venue, AccountID: accountID, Order: o, ReportedAt: now}
		if err := mass.AddOrderReport(report); err != nil {
			return nil, err
		}
	}
	mass.OpenOrdersCoverage = model.NewSnapshotCoverage(model.CoverageComplete, accountID, query.ClientID, ids, now)
	if query.IncludeFills {
		mass.FillsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
	} else {
		mass.FillsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	}
	if query.IncludePositions {
		mass.PositionsCoverage = model.NewSnapshotCoverage(model.CoverageUnavailable, accountID, query.ClientID, ids, now)
	} else {
		mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	}
	if err := mass.ValidateFor(query); err != nil {
		return nil, err
	}
	return mass, nil
}

func (f *FakeExec) massStatusVenue(query model.MassStatusQuery) (string, error) {
	venue := f.configuredVenue()
	queryVenue := strings.TrimSpace(query.Venue)
	if queryVenue != "" && queryVenue != venue {
		return "", fmt.Errorf("fake execution mass status: query venue %q does not match configured venue %q", queryVenue, venue)
	}
	for _, id := range f.instruments {
		if err := validateFakeInstrument(id, venue); err != nil {
			return "", err
		}
	}
	for _, report := range f.reports {
		if err := validateFakeInstrument(report.Request.InstrumentID, venue); err != nil {
			return "", err
		}
	}
	return venue, nil
}

func (f *FakeExec) massStatusAccount(query model.MassStatusQuery) (string, error) {
	configured := strings.TrimSpace(f.AccountID())
	if configured == "" || configured != f.AccountID() {
		return "", fmt.Errorf("fake execution mass status: normalized account identity required")
	}
	for _, report := range f.reports {
		accountID := strings.TrimSpace(report.Request.AccountID)
		if accountID == "" {
			continue
		}
		if accountID != report.Request.AccountID {
			return "", fmt.Errorf("fake execution mass status: normalized report account id required")
		}
		if configured != accountID {
			return "", fmt.Errorf("fake execution mass status: report account %q does not match configured account %q", accountID, configured)
		}
	}
	queryAccountID := strings.TrimSpace(query.AccountID)
	if queryAccountID != "" && queryAccountID != configured {
		return "", fmt.Errorf("fake execution mass status: query account %q does not match configured account %q", queryAccountID, configured)
	}
	return configured, nil
}

func (f *FakeExec) massStatusInstruments(venue string) ([]model.InstrumentID, error) {
	ids := make([]model.InstrumentID, 0, len(f.instruments)+len(f.reports))
	ids = append(ids, f.instruments...)
	for _, report := range f.reports {
		ids = append(ids, report.Request.InstrumentID)
	}
	for _, id := range ids {
		if err := validateFakeInstrument(id, venue); err != nil {
			return nil, err
		}
	}
	return model.NormalizeInstrumentIDs(ids), nil
}

func massStatusSelector(available, requested []model.InstrumentID, venue string) ([]model.InstrumentID, error) {
	if requested == nil {
		return append([]model.InstrumentID{}, available...), nil
	}
	requested = model.NormalizeInstrumentIDs(requested)
	for _, id := range requested {
		if err := validateFakeInstrument(id, venue); err != nil {
			return nil, fmt.Errorf("fake execution mass status: query instrument: %w", err)
		}
	}
	availableSet := make(map[model.InstrumentID]struct{}, len(available))
	for _, id := range available {
		availableSet[id] = struct{}{}
	}
	selected := make([]model.InstrumentID, 0, len(requested))
	for _, id := range requested {
		if _, ok := availableSet[id]; ok {
			selected = append(selected, id)
		}
	}
	return selected, nil
}

func validateFakeInstrument(id model.InstrumentID, venue string) error {
	normalized := model.NormalizeInstrumentIDs([]model.InstrumentID{id})
	if len(normalized) != 1 || normalized[0] != id {
		return fmt.Errorf("fake execution mass status: normalized instrument required: %s", id)
	}
	if id.Venue != venue {
		return fmt.Errorf("fake execution mass status: instrument %s does not match venue %q", id, venue)
	}
	if id.Symbol == "" || id.Kind == enums.KindUnknown {
		return fmt.Errorf("fake execution mass status: invalid instrument %s", id)
	}
	return nil
}

// SetOrderStatusReports installs the venue-wide open-order snapshot returned by
// GenerateExecutionMassStatus/OpenOrders, simulating the venue's authoritative
// resting set.
func (f *FakeExec) SetOrderStatusReports(orders ...model.Order) {
	f.reports = orders
	for _, order := range orders {
		if accountID := strings.TrimSpace(order.Request.AccountID); accountID != "" {
			f.accountID = accountID
			break
		}
	}
}

// SetAccountID configures the single logical account represented by this fake.
// The runtime requires execution and account clients to expose the same value.
func (f *FakeExec) SetAccountID(accountID string) { f.accountID = strings.TrimSpace(accountID) }

// SetInstruments freezes the explicit venue selector covered by fake mass
// status snapshots, including complete-empty responses.
func (f *FakeExec) SetInstruments(ids ...model.InstrumentID) {
	f.instruments = model.NormalizeInstrumentIDs(ids)
}

func (f *FakeExec) SetReportError(err error) { f.reportErr = err }

func (f *FakeExec) SetSubmitResult(order *model.Order, err error) {
	f.submitOrder = order
	f.submitErr = err
}

func (f *FakeExec) SetCancelError(err error) { f.cancelErr = err }

func (f *FakeExec) SetModifyResult(order *model.Order, err error) {
	f.modifyOrder = order
	f.modifyErr = err
}

func (f *FakeExec) SetModifySupported(ok bool) { f.modifySupported = ok }

func (f *FakeExec) OnSubmit(fn func(model.OrderRequest)) { f.onSubmit = fn }

func (f *FakeExec) OnCancel(fn func(model.InstrumentID, string)) { f.onCancel = fn }

func (f *FakeExec) OnModify(fn func(model.InstrumentID, string, decimal.Decimal, decimal.Decimal)) {
	f.onModify = fn
}

func (f *FakeExec) Events() <-chan contract.ExecEnvelope { return f.events }
func (f *FakeExec) Close() error                         { close(f.events); return nil }

func (f *FakeExec) EmitEnvelope(env contract.ExecEnvelope) { f.events <- env }

// EmitOrder pushes an order lifecycle event.
func (f *FakeExec) EmitOrder(o model.Order) {
	f.events <- contract.NewExecEnvelopeWithMeta(contract.OrderEvent{Order: o}, testEventMeta())
}

// EmitFill pushes a fill event.
func (f *FakeExec) EmitFill(fill model.Fill) {
	f.events <- contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: fill}, testEventMeta())
}

// EmitReject pushes a venue-side definitive rejection.
func (f *FakeExec) EmitReject(clientID, reason string) {
	f.events <- contract.NewExecEnvelopeWithMeta(contract.RejectEvent{ClientID: clientID, Reason: reason}, testEventMeta())
}

// FakeAccount is an in-memory AccountClient driven by Emit* helpers.
type FakeAccount struct {
	events       chan contract.AccountEnvelope
	accountID    string
	venue        string
	balances     []model.AccountBalance
	positions    []model.Position
	accountState model.AccountState
	hasState     bool
}

var _ contract.AccountClient = (*FakeAccount)(nil)

// NewFakeAccount returns a FakeAccount with a buffered event channel.
func NewFakeAccount() *FakeAccount {
	return &FakeAccount{events: make(chan contract.AccountEnvelope, 256), accountID: fakeAccountID, venue: "FAKE"}
}

func (f *FakeAccount) AccountID() string { return f.accountID }

// SetAccountID configures the same logical identity exposed to the runtime.
func (f *FakeAccount) SetAccountID(accountID string) { f.accountID = strings.TrimSpace(accountID) }

// SetVenue configures the single venue represented by account capabilities.
func (f *FakeAccount) SetVenue(venue string) { f.venue = strings.TrimSpace(venue) }

func (f *FakeAccount) Capabilities() contract.Capabilities {
	reports := contract.ReportCapabilities{PositionReports: true, AccountBalanceSnapshots: true}
	streaming := contract.StreamCapabilities{Account: true, AccountState: true}
	return contract.Capabilities{
		Venue: f.venue,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Account: true},
			{Kind: enums.KindPerp, Account: true},
		},
		Reports:   reports,
		Streaming: streaming,
	}
}

func (f *FakeAccount) AccountState(ctx context.Context) (model.AccountState, error) {
	if !f.hasState {
		return model.AccountState{}, fmt.Errorf("fake account state snapshot is not configured: %w", contract.ErrNotSupported)
	}
	return cloneAccountState(f.accountState), nil
}

func (f *FakeAccount) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	return append([]model.AccountBalance(nil), f.balances...), nil
}
func (f *FakeAccount) Positions(ctx context.Context) ([]model.Position, error) {
	return append([]model.Position(nil), f.positions...), nil
}
func (f *FakeAccount) SetLeverage(ctx context.Context, id model.InstrumentID, lev decimal.Decimal) error {
	return nil
}
func (f *FakeAccount) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return nil
}
func (f *FakeAccount) Events() <-chan contract.AccountEnvelope { return f.events }
func (f *FakeAccount) Close() error                            { close(f.events); return nil }

// SetSnapshots installs the account snapshots returned by Balances/Positions,
// simulating the venue's authoritative REST state for reconciliation.
func (f *FakeAccount) SetSnapshots(balances []model.AccountBalance, positions []model.Position) {
	f.balances = append([]model.AccountBalance(nil), balances...)
	f.positions = append([]model.Position(nil), positions...)
	for _, position := range positions {
		if venue := strings.TrimSpace(position.InstrumentID.Venue); venue != "" {
			f.venue = venue
			break
		}
	}
}

func (f *FakeAccount) SetAccountStateSnapshot(state model.AccountState) {
	f.accountState = cloneAccountState(state)
	f.hasState = true
	if venue := strings.TrimSpace(state.Venue); venue != "" {
		f.venue = venue
	}
}

// EmitBalance pushes a balance event.
func (f *FakeAccount) EmitBalance(b model.AccountBalance) {
	f.events <- contract.NewAccountEnvelopeWithMeta(contract.BalanceEvent{Balance: b}, testEventMeta())
}

// EmitPosition pushes a position event.
func (f *FakeAccount) EmitPosition(p model.Position) {
	f.events <- contract.NewAccountEnvelopeWithMeta(contract.PositionEvent{Position: p}, testEventMeta())
}

func (f *FakeAccount) EmitAccountState(state model.AccountState) {
	f.events <- contract.NewAccountEnvelopeWithMeta(contract.AccountStateEvent{State: cloneAccountState(state)}, testEventMeta())
}

func cloneAccountState(state model.AccountState) model.AccountState {
	return model.CloneAccountState(state)
}

// FakeMarket is an in-memory MarketDataClient driven by Emit* helpers. The
// Subscribe* methods are no-ops (the test pushes data directly). It also
// implements contract.Reconnectable so node.Reconnect can be exercised: each
// call increments Reconnects and connected flips to true.
type FakeMarket struct {
	events             chan contract.MarketEnvelope
	provider           model.InstrumentProvider
	referenceSnapshots map[string]model.DerivativeReferenceSnapshot
	openInterests      map[string]model.OpenInterestSnapshot
	referenceSubs      map[string]bool

	Reconnects int  // number of Reconnect calls
	connected  bool // reported by Connected; set true after Reconnect
}

// NewFakeMarket returns a FakeMarket with a buffered event channel.
func NewFakeMarket() *FakeMarket {
	return &FakeMarket{
		events:             make(chan contract.MarketEnvelope, 1024),
		referenceSnapshots: make(map[string]model.DerivativeReferenceSnapshot),
		openInterests:      make(map[string]model.OpenInterestSnapshot),
		referenceSubs:      make(map[string]bool),
	}
}

func (f *FakeMarket) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:     "FAKE",
		Reports:   contract.ReportCapabilities{},
		Streaming: contract.StreamCapabilities{Market: true},
		ReferenceData: contract.ReferenceDataCapabilities{
			CurrentFunding:      true,
			CurrentMarkPrice:    true,
			CurrentIndexPrice:   true,
			CurrentOpenInterest: true,
			ReferenceStream:     true,
		},
	}
}

func (f *FakeMarket) InstrumentProvider() model.InstrumentProvider { return f.provider }
func (f *FakeMarket) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	return nil, fmt.Errorf("fake market order book snapshots are not modeled: %w", contract.ErrNotSupported)
}
func (f *FakeMarket) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	return nil, fmt.Errorf("fake market historical bars are not modeled: %w", contract.ErrNotSupported)
}
func (f *FakeMarket) SubscribeBook(ctx context.Context, id model.InstrumentID) error   { return nil }
func (f *FakeMarket) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error { return nil }
func (f *FakeMarket) SubscribeTrades(ctx context.Context, id model.InstrumentID) error { return nil }
func (f *FakeMarket) SubscribeReference(ctx context.Context, id model.InstrumentID) error {
	f.referenceSubs[id.String()] = true
	return nil
}
func (f *FakeMarket) ReferenceSnapshot(ctx context.Context, id model.InstrumentID) (model.DerivativeReferenceSnapshot, error) {
	if s, ok := f.referenceSnapshots[id.String()]; ok {
		return s, nil
	}
	return model.DerivativeReferenceSnapshot{}, fmt.Errorf("fake market reference snapshot: %w", contract.ErrNotSupported)
}
func (f *FakeMarket) SetReferenceSnapshot(s model.DerivativeReferenceSnapshot) {
	f.referenceSnapshots[s.InstrumentID.String()] = s
}
func (f *FakeMarket) OpenInterest(ctx context.Context, id model.InstrumentID) (model.OpenInterestSnapshot, error) {
	if s, ok := f.openInterests[id.String()]; ok {
		return s, nil
	}
	return model.OpenInterestSnapshot{}, fmt.Errorf("fake market open interest: %w", contract.ErrNotSupported)
}
func (f *FakeMarket) SetOpenInterestSnapshot(s model.OpenInterestSnapshot) {
	f.openInterests[s.InstrumentID.String()] = s
}
func (f *FakeMarket) Events() <-chan contract.MarketEnvelope { return f.events }
func (f *FakeMarket) Close() error                           { close(f.events); return nil }

// Connected reports the simulated link state.
func (f *FakeMarket) Connected() bool { return f.connected }

// Reconnect simulates re-establishing the stream: it records the call and marks
// the link up.
func (f *FakeMarket) Reconnect(ctx context.Context) error {
	f.Reconnects++
	f.connected = true
	return nil
}

// EmitQuote pushes a top-of-book update.
func (f *FakeMarket) EmitQuote(q model.QuoteTick) {
	f.events <- contract.NewMarketEnvelopeWithMeta(contract.QuoteEvent{Quote: q}, testEventMeta())
}

// EmitTrade pushes a public trade print.
func (f *FakeMarket) EmitTrade(t model.TradeTick) {
	f.events <- contract.NewMarketEnvelopeWithMeta(contract.TradeEvent{Trade: t}, testEventMeta())
}

// EmitDerivativeReference pushes a derivative reference-data update.
func (f *FakeMarket) EmitDerivativeReference(s model.DerivativeReferenceSnapshot) {
	f.SetReferenceSnapshot(s)
	f.events <- contract.NewMarketEnvelopeWithMeta(contract.ReferenceDataEvent{Snapshot: s}, testEventMeta())
}

func testEventMeta() contract.EventMeta {
	return contract.EventMeta{Source: contract.SourceTest, Flags: contract.EventFlagSynthetic}
}

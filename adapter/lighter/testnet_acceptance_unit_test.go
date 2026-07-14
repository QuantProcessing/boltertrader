package lighter

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

type lighterAcceptanceCleanupFake struct {
	openSnapshots [][]model.Order
	openCalls     int
	statusReports []*model.OrderStatusReport
	statusCalls   int
	exactReports  []*model.OrderStatusReport
	exactCalls    int
	statusQueries []model.SingleOrderStatusQuery
	cancels       []string
}

func (f *lighterAcceptanceCleanupFake) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	if len(f.openSnapshots) == 0 {
		return nil, nil
	}
	index := f.openCalls
	if index >= len(f.openSnapshots) {
		index = len(f.openSnapshots) - 1
	}
	f.openCalls++
	return append([]model.Order(nil), f.openSnapshots[index]...), nil
}

func (f *lighterAcceptanceCleanupFake) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	f.statusQueries = append(f.statusQueries, query)
	if len(f.statusReports) == 0 {
		return nil, nil
	}
	index := f.statusCalls
	if index >= len(f.statusReports) {
		index = len(f.statusReports) - 1
	}
	f.statusCalls++
	return f.statusReports[index], nil
}

func (f *lighterAcceptanceCleanupFake) lighterAcceptanceExactOrderStatus(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if len(f.exactReports) == 0 {
		return f.GenerateOrderStatusReport(ctx, query)
	}
	f.statusQueries = append(f.statusQueries, query)
	index := f.exactCalls
	if index >= len(f.exactReports) {
		index = len(f.exactReports) - 1
	}
	f.exactCalls++
	return f.exactReports[index], nil
}

func (f *lighterAcceptanceCleanupFake) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	f.cancels = append(f.cancels, venueOrderID)
	return nil
}

func TestSelectLighterTestnetQuantityNeverExceedsMaxNotional(t *testing.T) {
	for _, tc := range []struct {
		name  string
		inst  *model.Instrument
		price decimal.Decimal
	}{
		{
			name:  "single step above cap",
			inst:  &model.Instrument{SizeStep: decimal.NewFromInt(1)},
			price: decimal.NewFromInt(101),
		},
		{
			name: "minimum quantity above cap",
			inst: &model.Instrument{
				SizeStep: decimal.NewFromInt(1),
				MinQty:   decimal.NewFromInt(2),
			},
			price: decimal.NewFromInt(60),
		},
		{
			name: "minimum notional rounding above cap",
			inst: &model.Instrument{
				SizeStep:    decimal.NewFromInt(1),
				MinNotional: decimal.NewFromInt(95),
			},
			price: decimal.NewFromInt(60),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			maxNotional := decimal.NewFromInt(100)
			qty, err := selectLighterTestnetQuantity(tc.inst, maxNotional, tc.price)
			if err == nil {
				t.Fatalf("select quantity returned qty=%s, want max-notional error", qty)
			}
			if !qty.IsZero() {
				t.Fatalf("failed selection qty=%s, want zero", qty)
			}
		})
	}
}

func TestSelectLighterTestnetQuantityHonorsVenueMinimums(t *testing.T) {
	inst := &model.Instrument{
		SizeStep:    decimal.RequireFromString("0.1"),
		MinQty:      decimal.NewFromInt(3),
		MinNotional: decimal.NewFromInt(40),
	}
	maxNotional := decimal.NewFromInt(100)
	price := decimal.NewFromInt(10)

	qty, err := selectLighterTestnetQuantity(inst, maxNotional, price)
	if err != nil {
		t.Fatalf("select quantity: %v", err)
	}
	if qty.LessThan(inst.MinQty) {
		t.Fatalf("qty=%s below min quantity %s", qty, inst.MinQty)
	}
	if qty.Mul(price).LessThan(inst.MinNotional) {
		t.Fatalf("qty=%s notional=%s below min notional %s", qty, qty.Mul(price), inst.MinNotional)
	}
	if !qty.Mod(inst.SizeStep).IsZero() {
		t.Fatalf("qty=%s is not aligned to step %s", qty, inst.SizeStep)
	}
	if qty.Mul(price).GreaterThan(maxNotional) {
		t.Fatalf("qty=%s notional=%s exceeds max notional %s", qty, qty.Mul(price), maxNotional)
	}
}

func TestSelectLighterTestnetQuantityRejectsInvalidInputs(t *testing.T) {
	validInst := &model.Instrument{SizeStep: decimal.RequireFromString("0.1")}
	for _, tc := range []struct {
		name        string
		inst        *model.Instrument
		maxNotional decimal.Decimal
		price       decimal.Decimal
	}{
		{name: "nil instrument", maxNotional: decimal.NewFromInt(100), price: decimal.NewFromInt(10)},
		{name: "zero max notional", inst: validInst, price: decimal.NewFromInt(10)},
		{name: "negative max notional", inst: validInst, maxNotional: decimal.NewFromInt(-1), price: decimal.NewFromInt(10)},
		{name: "zero price", inst: validInst, maxNotional: decimal.NewFromInt(100)},
		{name: "negative price", inst: validInst, maxNotional: decimal.NewFromInt(100), price: decimal.NewFromInt(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if qty, err := selectLighterTestnetQuantity(tc.inst, tc.maxNotional, tc.price); err == nil || !qty.IsZero() {
				t.Fatalf("select quantity qty=%s err=%v, want zero quantity and error", qty, err)
			}
		})
	}
}

func TestLighterRestingCleanupFindsInitiallyInvisibleOwnOrder(t *testing.T) {
	id := lighterCleanupInstrumentID()
	own := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusNew, decimal.Zero)
	unrelated := lighterCleanupOrder(id, "other-client", "venue-other", enums.StatusNew, decimal.Zero)
	fake := &lighterAcceptanceCleanupFake{openSnapshots: [][]model.Order{
		{},
		{unrelated, own},
		{unrelated, own},
		{unrelated},
		{unrelated},
	}}
	cleanup := newLighterRestingOrderCleanup(fake, id, model.AccountIDLighterDefault, "btac-own", decimal.NewFromInt(1))
	configureFastLighterCleanup(cleanup)

	if err := cleanup.CancelAndConfirm(context.Background()); err != nil {
		t.Fatalf("cleanup initially invisible order: %v", err)
	}
	if len(fake.cancels) == 0 {
		t.Fatal("own order was never canceled after becoming visible")
	}
	for _, venueOrderID := range fake.cancels {
		if venueOrderID != "venue-own" {
			t.Fatalf("canceled venue order %q, want only venue-own", venueOrderID)
		}
	}
	if len(fake.statusQueries) == 0 || fake.statusQueries[0].ClientID != "btac-own" {
		t.Fatalf("status recovery queries=%+v, want exact client id", fake.statusQueries)
	}
}

func TestLighterRestingCleanupWaitsPastKnownOrderWindowForAmbiguousSubmit(t *testing.T) {
	id := lighterCleanupInstrumentID()
	own := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusNew, decimal.Zero)
	unrelated := lighterCleanupOrder(id, "other-client", "venue-other", enums.StatusNew, decimal.Zero)
	fake := &lighterAcceptanceCleanupFake{openSnapshots: [][]model.Order{
		{},
		{},
		{},
		{},
		{},
		{},
		{unrelated, own},
		{unrelated, own},
		{unrelated},
		{unrelated},
		{unrelated},
	}}
	cleanup := newLighterRestingOrderCleanup(fake, id, model.AccountIDLighterDefault, "btac-own", decimal.NewFromInt(1))
	cleanup.pollInterval = 0

	if err := cleanup.CancelAndConfirm(context.Background()); err != nil {
		t.Fatalf("cleanup delayed ambiguous order: %v", err)
	}
	if len(fake.cancels) == 0 {
		t.Fatal("ambiguous order appearing after the known-order window was never canceled")
	}
	for _, venueOrderID := range fake.cancels {
		if venueOrderID != "venue-own" {
			t.Fatalf("canceled venue order %q, want only venue-own", venueOrderID)
		}
	}
}

func TestLighterAmbiguousCleanupCoversSubmitVisibilityTimeout(t *testing.T) {
	covered := time.Duration(lighterAcceptanceAmbiguousOrderMinPolls-1) * lighterAcceptanceCleanupPollInterval
	if covered < lighterAcceptanceSubmitVisibilityTimeout {
		t.Fatalf("ambiguous visibility coverage=%s, want at least %s", covered, lighterAcceptanceSubmitVisibilityTimeout)
	}
	if lighterAcceptanceCleanupMaxPolls < lighterAcceptanceAmbiguousOrderMinPolls+lighterAcceptanceStableAbsentPolls {
		t.Fatalf("cleanup max polls=%d cannot collect stable absence after ambiguous visibility poll %d", lighterAcceptanceCleanupMaxPolls, lighterAcceptanceAmbiguousOrderMinPolls)
	}
	if lighterAcceptanceDeferredCleanupTimeout <= lighterAcceptanceSubmitVisibilityTimeout {
		t.Fatalf("deferred cleanup timeout=%s must exceed submit visibility timeout=%s", lighterAcceptanceDeferredCleanupTimeout, lighterAcceptanceSubmitVisibilityTimeout)
	}
}

func TestLighterRestingCleanupRetriesAfterSuccessfulCancelUntilNoOpenEvidence(t *testing.T) {
	id := lighterCleanupInstrumentID()
	own := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusNew, decimal.Zero)
	fake := &lighterAcceptanceCleanupFake{openSnapshots: [][]model.Order{
		{own},
		{own},
		{},
		{},
	}}
	cleanup := newLighterRestingOrderCleanup(fake, id, model.AccountIDLighterDefault, "btac-own", decimal.NewFromInt(1))
	configureFastLighterCleanup(cleanup)

	if err := cleanup.CancelAndConfirm(context.Background()); err != nil {
		t.Fatalf("cleanup still-open order: %v", err)
	}
	if got := len(fake.cancels); got < 2 {
		t.Fatalf("cancel attempts=%d, want retry while order remains open after nil cancel", got)
	}
}

func TestLighterRestingCleanupPreservesUnrelatedOrders(t *testing.T) {
	id := lighterCleanupInstrumentID()
	unrelated := lighterCleanupOrder(id, "other-client", "venue-other", enums.StatusNew, decimal.Zero)
	fake := &lighterAcceptanceCleanupFake{openSnapshots: [][]model.Order{
		{unrelated},
		{unrelated},
		{unrelated},
	}}
	cleanup := newLighterRestingOrderCleanup(fake, id, model.AccountIDLighterDefault, "btac-own", decimal.NewFromInt(1))
	configureFastLighterCleanup(cleanup)

	if err := cleanup.CancelAndConfirm(context.Background()); err != nil {
		t.Fatalf("cleanup with unrelated order: %v", err)
	}
	if len(fake.cancels) != 0 {
		t.Fatalf("cleanup canceled unrelated orders: %+v", fake.cancels)
	}
}

func TestLighterRestingCleanupFailsClosedOnLatePartialFill(t *testing.T) {
	id := lighterCleanupInstrumentID()
	partial := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusPartiallyFilled, decimal.RequireFromString("0.25"))
	unrelated := lighterCleanupOrder(id, "other-client", "venue-other", enums.StatusNew, decimal.Zero)
	fake := &lighterAcceptanceCleanupFake{openSnapshots: [][]model.Order{
		{},
		{unrelated, partial},
		{unrelated},
		{unrelated},
	}}
	cleanup := newLighterRestingOrderCleanup(fake, id, model.AccountIDLighterDefault, "btac-own", decimal.NewFromInt(1))
	configureFastLighterCleanup(cleanup)

	err := cleanup.CancelAndConfirm(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected fill") {
		t.Fatalf("late partial cleanup err=%v, want fail-closed unexpected-fill error", err)
	}
	if len(fake.cancels) == 0 {
		t.Fatal("partially filled resting order remainder was not canceled")
	}
	for _, venueOrderID := range fake.cancels {
		if venueOrderID != "venue-own" {
			t.Fatalf("canceled venue order %q, want only venue-own", venueOrderID)
		}
	}
}

func TestLighterRestingCleanupAcceptsExactTerminalStatusEvidence(t *testing.T) {
	id := lighterCleanupInstrumentID()
	terminal := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusCanceled, decimal.Zero)
	fake := &lighterAcceptanceCleanupFake{
		openSnapshots: [][]model.Order{{lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusNew, decimal.Zero)}},
		statusReports: []*model.OrderStatusReport{{Order: terminal}},
	}
	cleanup := newLighterRestingOrderCleanup(fake, id, model.AccountIDLighterDefault, "btac-own", decimal.NewFromInt(1))
	configureFastLighterCleanup(cleanup)

	if err := cleanup.CancelAndConfirm(context.Background()); err != nil {
		t.Fatalf("cleanup terminal status: %v", err)
	}
	if len(fake.cancels) != 0 {
		t.Fatalf("terminal status should avoid cancel retry, got %+v", fake.cancels)
	}
}

func TestLighterRestingCleanupFailsClosedOnInactiveTerminalFill(t *testing.T) {
	id := lighterCleanupInstrumentID()
	terminalFill := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusCanceled, decimal.RequireFromString("0.25"))
	fake := &lighterAcceptanceCleanupFake{
		openSnapshots: [][]model.Order{{}, {}, {}},
		exactReports:  []*model.OrderStatusReport{{Order: terminalFill}},
	}
	cleanup := newLighterRestingOrderCleanup(fake, id, model.AccountIDLighterDefault, "btac-own", decimal.NewFromInt(1))
	configureFastLighterCleanup(cleanup)

	err := cleanup.CancelAndConfirm(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected fill") {
		t.Fatalf("inactive terminal fill cleanup err=%v, want fail-closed unexpected-fill error", err)
	}
}

func TestLighterRestingCleanupFullFillStillNeedsExposureCleanup(t *testing.T) {
	id := lighterCleanupInstrumentID()
	cleanup := newLighterRestingOrderCleanup(
		&lighterAcceptanceCleanupFake{},
		id,
		model.AccountIDLighterDefault,
		"btac-own",
		decimal.NewFromInt(1),
	)
	full := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusFilled, decimal.RequireFromString("0.25"))

	if err := cleanup.ObserveSubmitResult(&full); err == nil {
		t.Fatal("full resting fill must be reported as an acceptance failure")
	}
	if cleanup.NeedsCleanup() {
		t.Fatal("terminal full fill should not need order cancellation")
	}
	if !cleanup.NeedsExposureCleanup() {
		t.Fatal("terminal full fill must still require exposure cleanup")
	}
	if got := cleanup.ConfirmedFilledQty(); !got.Equal(decimal.RequireFromString("0.25")) {
		t.Fatalf("confirmed filled=%s, want 0.25", got)
	}
}

func TestLighterFullFillCleanupRecoversExposureEvenWhenOrderIsTerminal(t *testing.T) {
	id := lighterCleanupInstrumentID()
	inst := &model.Instrument{ID: id, PriceTick: decimal.RequireFromString("0.01"), SizeStep: decimal.RequireFromString("0.01")}
	orderCleanup := newLighterRestingOrderCleanup(
		&lighterAcceptanceCleanupFake{},
		id,
		model.AccountIDLighterDefault,
		"btac-own",
		decimal.RequireFromString("0.25"),
	)
	full := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusFilled, decimal.RequireFromString("0.25"))
	if err := orderCleanup.ObserveSubmitResult(&full); err == nil {
		t.Fatal("full fill must remain an acceptance failure")
	}
	exposure := &lighterAcceptanceExposureFake{
		positionSnapshots: [][]model.Position{
			{{AccountID: model.AccountIDLighterDefault, InstrumentID: id, Side: enums.PosNet, Quantity: decimal.RequireFromString("0.25")}},
			{},
		},
		book:        &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
		submitOrder: &model.Order{Status: enums.StatusFilled, FilledQty: decimal.RequireFromString("0.25")},
	}
	cleaner := newLighterAcceptanceExposureCleaner(exposure, exposure, exposure)
	cleaner.pollInterval = 0

	err := orderCleanup.CancelConfirmAndRecover(context.Background(), cleaner, inst, lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindPerp})
	if err == nil || !strings.Contains(err.Error(), "unexpected fill") {
		t.Fatalf("combined full-fill cleanup err=%v, want original acceptance failure", err)
	}
	if len(exposure.submits) != 1 || !exposure.submits[0].ReduceOnly {
		t.Fatalf("terminal full fill was not boundedly recovered: %+v", exposure.submits)
	}
}

func TestLighterFullSpotFillCleanupPreservesPreExistingInventory(t *testing.T) {
	id := model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot}
	inst := &model.Instrument{
		ID:          id,
		Base:        "ETH",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.01"),
		MinQty:      decimal.RequireFromString("0.01"),
		MinNotional: decimal.RequireFromString("1"),
	}
	orderCleanup := newLighterRestingOrderCleanup(
		&lighterAcceptanceCleanupFake{},
		id,
		model.AccountIDLighterDefault,
		"btac-own",
		decimal.RequireFromString("0.25"),
	)
	full := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusFilled, decimal.RequireFromString("0.25"))
	if err := orderCleanup.ObserveSubmitResult(&full); err == nil {
		t.Fatal("full fill must remain an acceptance failure")
	}
	baseline := lighterAcceptanceExposureBaseline{
		InstrumentID: id, Kind: enums.KindSpot, BaseCurrency: "ETH", BaseTotal: decimal.NewFromInt(3), BaseAvailable: decimal.NewFromInt(3),
	}
	exposure := &lighterAcceptanceExposureFake{
		accountStates: []model.AccountState{
			lighterExposureAccountState("ETH", "3.25", "3.25"),
			lighterExposureAccountState("ETH", "3.00", "3.00"),
		},
		book:        &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
		submitOrder: &model.Order{Status: enums.StatusFilled, FilledQty: decimal.RequireFromString("0.25")},
	}
	cleaner := newLighterAcceptanceExposureCleaner(exposure, exposure, exposure)
	cleaner.pollInterval = 0

	err := orderCleanup.CancelConfirmAndRecover(context.Background(), cleaner, inst, baseline)
	if err == nil || !strings.Contains(err.Error(), "unexpected fill") {
		t.Fatalf("combined Spot full-fill cleanup err=%v, want original acceptance failure", err)
	}
	if len(exposure.submits) != 1 || exposure.submits[0].ReduceOnly || !exposure.submits[0].Quantity.Equal(decimal.RequireFromString("0.25")) {
		t.Fatalf("Spot full fill cleanup request=%+v", exposure.submits)
	}
}

func TestLighterFillAboveOwnedQuantityNeverAuthorizesReverseOrder(t *testing.T) {
	id := lighterCleanupInstrumentID()
	inst := &model.Instrument{ID: id, PriceTick: decimal.RequireFromString("0.01"), SizeStep: decimal.RequireFromString("0.01")}
	orderCleanup := newLighterRestingOrderCleanup(
		&lighterAcceptanceCleanupFake{},
		id,
		model.AccountIDLighterDefault,
		"btac-own",
		decimal.RequireFromString("0.25"),
	)
	invalid := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusFilled, decimal.RequireFromString("0.26"))
	_ = orderCleanup.ObserveSubmitResult(&invalid)
	exposure := &lighterAcceptanceExposureFake{
		positionSnapshots: [][]model.Position{{{
			AccountID: model.AccountIDLighterDefault, InstrumentID: id, Side: enums.PosNet, Quantity: decimal.RequireFromString("0.25"),
		}}},
		book: &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
	}
	cleaner := newLighterAcceptanceExposureCleaner(exposure, exposure, exposure)
	cleaner.pollInterval = 0

	err := orderCleanup.CancelConfirmAndRecover(context.Background(), cleaner, inst, lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindPerp})
	if err == nil || !strings.Contains(err.Error(), "exceeds owned acceptance quantity") {
		t.Fatalf("overfilled ownership err=%v", err)
	}
	if len(exposure.submits) != 0 {
		t.Fatalf("overfilled record authorized reverse order: %+v", exposure.submits)
	}
}

func TestLighterAmbiguousExposureCleanupIsNeverRetriedByOrderCleanup(t *testing.T) {
	id := lighterCleanupInstrumentID()
	inst := &model.Instrument{ID: id, PriceTick: decimal.RequireFromString("0.01"), SizeStep: decimal.RequireFromString("0.01")}
	orderCleanup := newLighterRestingOrderCleanup(
		&lighterAcceptanceCleanupFake{},
		id,
		model.AccountIDLighterDefault,
		"btac-own",
		decimal.RequireFromString("0.25"),
	)
	full := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusFilled, decimal.RequireFromString("0.25"))
	_ = orderCleanup.ObserveSubmitResult(&full)
	exposure := &lighterAcceptanceExposureFake{
		positionSnapshots: [][]model.Position{{{
			AccountID: model.AccountIDLighterDefault, InstrumentID: id, Side: enums.PosNet, Quantity: decimal.RequireFromString("0.25"),
		}}},
		book:      &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
		submitErr: errors.New("ambiguous transport failure"),
	}
	cleaner := newLighterAcceptanceExposureCleaner(exposure, exposure, exposure)
	cleaner.pollInterval = 0
	baseline := lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindPerp}

	_ = orderCleanup.CancelConfirmAndRecover(context.Background(), cleaner, inst, baseline)
	_ = orderCleanup.CancelConfirmAndRecover(context.Background(), cleaner, inst, baseline)
	if len(exposure.submits) != 1 {
		t.Fatalf("ambiguous exposure cleanup submits=%d, want exactly one across repeated cleanup calls", len(exposure.submits))
	}
}

func TestLighterPartialFillCleanupCancelsRemainderBeforeExposureRecovery(t *testing.T) {
	id := lighterCleanupInstrumentID()
	inst := &model.Instrument{ID: id, PriceTick: decimal.RequireFromString("0.01"), SizeStep: decimal.RequireFromString("0.01")}
	partial := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusPartiallyFilled, decimal.RequireFromString("0.25"))
	canceled := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusCanceled, decimal.RequireFromString("0.25"))
	orderIO := &lighterAcceptanceCleanupFake{
		exactReports:  []*model.OrderStatusReport{{Order: partial}, {Order: canceled}},
		openSnapshots: [][]model.Order{{partial}},
	}
	orderCleanup := newLighterRestingOrderCleanup(orderIO, id, model.AccountIDLighterDefault, "btac-own", decimal.NewFromInt(1))
	configureFastLighterCleanup(orderCleanup)
	exposure := &lighterAcceptanceExposureFake{
		positionSnapshots: [][]model.Position{
			{{AccountID: model.AccountIDLighterDefault, InstrumentID: id, Side: enums.PosNet, Quantity: decimal.RequireFromString("0.25")}},
			{},
		},
		book:        &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
		submitOrder: &model.Order{Status: enums.StatusFilled, FilledQty: decimal.RequireFromString("0.25")},
	}
	cleaner := newLighterAcceptanceExposureCleaner(exposure, exposure, exposure)
	cleaner.pollInterval = 0

	err := orderCleanup.CancelConfirmAndRecover(context.Background(), cleaner, inst, lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindPerp})
	if err == nil || !strings.Contains(err.Error(), "unexpected fill") {
		t.Fatalf("combined partial-fill cleanup err=%v, want original acceptance failure", err)
	}
	if len(orderIO.cancels) == 0 || orderIO.cancels[0] != "venue-own" {
		t.Fatalf("partial fill remainder was not canceled first: %+v", orderIO.cancels)
	}
	if len(exposure.submits) != 1 || !exposure.submits[0].ReduceOnly {
		t.Fatalf("partial fill exposure was not boundedly recovered: %+v", exposure.submits)
	}
}

func TestLighterPartialFillDoesNotRecoverBeforeRemainderIsTerminal(t *testing.T) {
	id := lighterCleanupInstrumentID()
	inst := &model.Instrument{ID: id, PriceTick: decimal.RequireFromString("0.01"), SizeStep: decimal.RequireFromString("0.01")}
	partial := lighterCleanupOrder(id, "btac-own", "venue-own", enums.StatusPartiallyFilled, decimal.RequireFromString("0.25"))
	orderIO := &lighterAcceptanceCleanupFake{
		exactReports:  []*model.OrderStatusReport{{Order: partial}},
		openSnapshots: [][]model.Order{{partial}},
	}
	orderCleanup := newLighterRestingOrderCleanup(orderIO, id, model.AccountIDLighterDefault, "btac-own", decimal.NewFromInt(1))
	orderCleanup.pollInterval = 0
	orderCleanup.maxPolls = 2
	orderCleanup.minObservationPolls = 2
	orderCleanup.ambiguousMinPolls = 2
	orderCleanup.stableAbsentPolls = 2
	exposure := &lighterAcceptanceExposureFake{
		positionSnapshots: [][]model.Position{{{
			AccountID: model.AccountIDLighterDefault, InstrumentID: id, Side: enums.PosNet, Quantity: decimal.RequireFromString("0.25"),
		}}},
		book: &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
	}
	cleaner := newLighterAcceptanceExposureCleaner(exposure, exposure, exposure)
	cleaner.pollInterval = 0

	if err := orderCleanup.CancelConfirmAndRecover(context.Background(), cleaner, inst, lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindPerp}); err == nil {
		t.Fatal("unresolved partially filled order must fail cleanup")
	}
	if !orderCleanup.NeedsCleanup() {
		t.Fatal("persistently open remainder must remain unresolved")
	}
	if len(exposure.submits) != 0 {
		t.Fatalf("exposure recovery raced an open buy remainder: %+v", exposure.submits)
	}
}

func TestLighterExposureBaselineIsAuthoritative(t *testing.T) {
	spotID := model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot}
	spot := &model.Instrument{ID: spotID, Base: "ETH"}
	fake := &lighterAcceptanceExposureFake{accountStates: []model.AccountState{lighterExposureAccountState("ETH", "3.5", "3.25")}}
	cleaner := newLighterAcceptanceExposureCleaner(fake, fake, fake)
	baseline, err := cleaner.CaptureBaseline(context.Background(), spot)
	if err != nil {
		t.Fatalf("capture Spot baseline: %v", err)
	}
	if !baseline.BaseTotal.Equal(decimal.RequireFromString("3.5")) || !baseline.BaseAvailable.Equal(decimal.RequireFromString("3.25")) {
		t.Fatalf("Spot baseline=%+v", baseline)
	}

	perpID := lighterCleanupInstrumentID()
	perp := &model.Instrument{ID: perpID}
	fake.positionSnapshots = [][]model.Position{{{
		AccountID: model.AccountIDLighterDefault, InstrumentID: perpID, Side: enums.PosNet, Quantity: decimal.RequireFromString("0.1"),
	}}}
	if _, err := cleaner.CaptureBaseline(context.Background(), perp); err == nil {
		t.Fatal("non-flat Perp baseline must fail closed")
	}
}

func TestLighterPerpExposureCleanupUsesSingleBoundedReduceOnlyIOC(t *testing.T) {
	id := lighterCleanupInstrumentID()
	inst := &model.Instrument{
		ID:          id,
		Base:        "ETH",
		Quote:       "USDC",
		Settle:      "USDC",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.01"),
		MinQty:      decimal.RequireFromString("0.01"),
		MinNotional: decimal.RequireFromString("1"),
	}
	fake := &lighterAcceptanceExposureFake{
		positionSnapshots: [][]model.Position{
			{{AccountID: model.AccountIDLighterDefault, InstrumentID: id, Side: enums.PosNet, Quantity: decimal.RequireFromString("0.25")}},
			{},
		},
		book: &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
		submitOrder: &model.Order{
			Status:    enums.StatusFilled,
			FilledQty: decimal.RequireFromString("0.25"),
		},
	}
	cleaner := newLighterAcceptanceExposureCleaner(fake, fake, fake)
	cleaner.pollInterval = 0
	baseline := lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindPerp}

	if err := cleaner.Recover(context.Background(), inst, baseline, decimal.RequireFromString("0.25")); err != nil {
		t.Fatalf("recover Perp exposure: %v", err)
	}
	if len(fake.submits) != 1 {
		t.Fatalf("close submits=%d, want exactly one", len(fake.submits))
	}
	req := fake.submits[0]
	if req.Side != enums.SideSell || req.TIF != enums.TifIOC || !req.ReduceOnly || req.Type != enums.TypeLimit {
		t.Fatalf("unexpected Perp close request: %+v", req)
	}
	if !req.Quantity.Equal(decimal.RequireFromString("0.25")) || req.ClientID == "" {
		t.Fatalf("Perp close quantity/client id invalid: %+v", req)
	}
}

func TestLighterPerpExposureCleanupFailsClosedOutsideOwnedFill(t *testing.T) {
	id := lighterCleanupInstrumentID()
	inst := &model.Instrument{
		ID:        id,
		PriceTick: decimal.RequireFromString("0.01"),
		SizeStep:  decimal.RequireFromString("0.01"),
	}
	for _, tc := range []struct {
		name string
		qty  decimal.Decimal
	}{
		{name: "short", qty: decimal.RequireFromString("-0.1")},
		{name: "above own fill", qty: decimal.RequireFromString("0.26")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &lighterAcceptanceExposureFake{positionSnapshots: [][]model.Position{{{
				AccountID: model.AccountIDLighterDefault, InstrumentID: id, Side: enums.PosNet, Quantity: tc.qty,
			}}}}
			cleaner := newLighterAcceptanceExposureCleaner(fake, fake, fake)
			cleaner.pollInterval = 0
			err := cleaner.Recover(context.Background(), inst, lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindPerp}, decimal.RequireFromString("0.25"))
			if err == nil {
				t.Fatal("unowned Perp exposure must fail closed")
			}
			if len(fake.submits) != 0 {
				t.Fatalf("unowned Perp exposure submitted close: %+v", fake.submits)
			}
		})
	}
}

func TestLighterSpotExposureCleanupSellsOnlyOwnedAvailableDelta(t *testing.T) {
	id := model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot}
	inst := &model.Instrument{
		ID:          id,
		Base:        "ETH",
		Quote:       "USDC",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.10"),
		MinQty:      decimal.RequireFromString("0.10"),
		MinNotional: decimal.RequireFromString("1"),
	}
	baseline := lighterAcceptanceExposureBaseline{
		InstrumentID:  id,
		Kind:          enums.KindSpot,
		BaseCurrency:  "ETH",
		BaseTotal:     decimal.NewFromInt(3),
		BaseAvailable: decimal.NewFromInt(3),
	}
	fake := &lighterAcceptanceExposureFake{
		accountStates: []model.AccountState{
			lighterExposureAccountState("ETH", "3.25", "3.20"),
			lighterExposureAccountState("ETH", "3.05", "3.05"),
		},
		book: &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
		submitOrder: &model.Order{
			Status:    enums.StatusFilled,
			FilledQty: decimal.RequireFromString("0.20"),
		},
	}
	cleaner := newLighterAcceptanceExposureCleaner(fake, fake, fake)
	cleaner.pollInterval = 0

	if err := cleaner.Recover(context.Background(), inst, baseline, decimal.RequireFromString("0.25")); err != nil {
		t.Fatalf("recover Spot exposure: %v", err)
	}
	if len(fake.submits) != 1 {
		t.Fatalf("close submits=%d, want exactly one", len(fake.submits))
	}
	req := fake.submits[0]
	if req.Side != enums.SideSell || req.TIF != enums.TifIOC || req.ReduceOnly {
		t.Fatalf("unexpected Spot close request: %+v", req)
	}
	if !req.Quantity.Equal(decimal.RequireFromString("0.20")) {
		t.Fatalf("Spot close quantity=%s, want min(fill=0.25,total_delta=0.25,available_delta=0.20)", req.Quantity)
	}
}

func TestLighterSpotExposureCleanupUsesNetDeltaAfterBaseFee(t *testing.T) {
	id := model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot}
	inst := &model.Instrument{
		ID:          id,
		Base:        "ETH",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.01"),
		MinQty:      decimal.RequireFromString("0.01"),
		MinNotional: decimal.RequireFromString("1"),
	}
	baseline := lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindSpot, BaseCurrency: "ETH", BaseTotal: decimal.NewFromInt(3), BaseAvailable: decimal.NewFromInt(3)}
	fake := &lighterAcceptanceExposureFake{
		accountStates: []model.AccountState{
			lighterExposureAccountState("ETH", "3.24", "3.24"),
			lighterExposureAccountState("ETH", "3.00", "3.00"),
		},
		book:        &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
		submitOrder: &model.Order{Status: enums.StatusFilled, FilledQty: decimal.RequireFromString("0.24")},
	}
	cleaner := newLighterAcceptanceExposureCleaner(fake, fake, fake)
	cleaner.pollInterval = 0

	if err := cleaner.Recover(context.Background(), inst, baseline, decimal.RequireFromString("0.25")); err != nil {
		t.Fatalf("recover fee-reduced Spot exposure: %v", err)
	}
	if len(fake.submits) != 1 || !fake.submits[0].Quantity.Equal(decimal.RequireFromString("0.24")) {
		t.Fatalf("Spot close requests=%+v, want only net acquired 0.24", fake.submits)
	}
}

func TestLighterExposureCleanupDoesNotSubmitWhenAlreadyAtBaseline(t *testing.T) {
	id := model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot}
	inst := &model.Instrument{ID: id, Base: "ETH", PriceTick: decimal.RequireFromString("0.01"), SizeStep: decimal.RequireFromString("0.01")}
	baseline := lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindSpot, BaseCurrency: "ETH", BaseTotal: decimal.NewFromInt(3), BaseAvailable: decimal.NewFromInt(3)}
	fake := &lighterAcceptanceExposureFake{accountStates: []model.AccountState{lighterExposureAccountState("ETH", "3", "3")}}
	cleaner := newLighterAcceptanceExposureCleaner(fake, fake, fake)
	cleaner.pollInterval = 0

	if err := cleaner.Recover(context.Background(), inst, baseline, decimal.RequireFromString("0.25")); err != nil {
		t.Fatalf("already-clean recovery: %v", err)
	}
	if len(fake.submits) != 0 {
		t.Fatalf("clean exposure submitted reverse order: %+v", fake.submits)
	}
}

func TestLighterSpotExposureCleanupRefusesToConsumePreExistingInventory(t *testing.T) {
	id := model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot}
	inst := &model.Instrument{ID: id, Base: "ETH", PriceTick: decimal.RequireFromString("0.01"), SizeStep: decimal.RequireFromString("0.01")}
	baseline := lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindSpot, BaseCurrency: "ETH", BaseTotal: decimal.NewFromInt(3), BaseAvailable: decimal.NewFromInt(3)}
	fake := &lighterAcceptanceExposureFake{accountStates: []model.AccountState{lighterExposureAccountState("ETH", "2.99", "2.99")}}
	cleaner := newLighterAcceptanceExposureCleaner(fake, fake, fake)
	cleaner.pollInterval = 0

	if err := cleaner.Recover(context.Background(), inst, baseline, decimal.RequireFromString("0.25")); err == nil {
		t.Fatal("balance below pre-submit baseline must fail closed")
	}
	if len(fake.submits) != 0 {
		t.Fatalf("unsafe Spot reverse order submitted: %+v", fake.submits)
	}
}

func TestLighterExposureCleanupDoesNotRetryAmbiguousClose(t *testing.T) {
	id := lighterCleanupInstrumentID()
	inst := &model.Instrument{ID: id, PriceTick: decimal.RequireFromString("0.01"), SizeStep: decimal.RequireFromString("0.01")}
	fake := &lighterAcceptanceExposureFake{
		positionSnapshots: [][]model.Position{{{
			AccountID: model.AccountIDLighterDefault, InstrumentID: id, Side: enums.PosNet, Quantity: decimal.RequireFromString("0.25"),
		}}},
		book:      &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
		submitErr: errors.New("ambiguous transport failure"),
	}
	cleaner := newLighterAcceptanceExposureCleaner(fake, fake, fake)
	cleaner.pollInterval = 0

	err := cleaner.Recover(context.Background(), inst, lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindPerp}, decimal.RequireFromString("0.25"))
	if err == nil || !strings.Contains(err.Error(), "not retried") {
		t.Fatalf("ambiguous close err=%v, want explicit no-retry error", err)
	}
	if len(fake.submits) != 1 {
		t.Fatalf("ambiguous close submits=%d, want one", len(fake.submits))
	}
}

func TestLighterSpotExposureCleanupDoesNotRetryAmbiguousClose(t *testing.T) {
	id := model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot}
	inst := &model.Instrument{ID: id, Base: "ETH", PriceTick: decimal.RequireFromString("0.01"), SizeStep: decimal.RequireFromString("0.01")}
	baseline := lighterAcceptanceExposureBaseline{InstrumentID: id, Kind: enums.KindSpot, BaseCurrency: "ETH", BaseTotal: decimal.NewFromInt(3), BaseAvailable: decimal.NewFromInt(3)}
	fake := &lighterAcceptanceExposureFake{
		accountStates: []model.AccountState{
			lighterExposureAccountState("ETH", "3.25", "3.25"),
			lighterExposureAccountState("ETH", "3.00", "3.00"),
		},
		book:      &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}},
		submitErr: errors.New("ambiguous transport failure"),
	}
	cleaner := newLighterAcceptanceExposureCleaner(fake, fake, fake)
	cleaner.pollInterval = 0

	err := cleaner.Recover(context.Background(), inst, baseline, decimal.RequireFromString("0.25"))
	if err == nil || !strings.Contains(err.Error(), "not retried") {
		t.Fatalf("ambiguous Spot close err=%v, want explicit no-retry error", err)
	}
	if len(fake.submits) != 1 {
		t.Fatalf("ambiguous Spot close submits=%d, want one", len(fake.submits))
	}
}

func TestLighterRestingPriceUsesConservativePercentageAndStaysWithinMaxNotional(t *testing.T) {
	inst := &model.Instrument{
		PriceTick: decimal.RequireFromString("0.01"),
		SizeStep:  decimal.RequireFromString("0.01"),
		MinQty:    decimal.RequireFromString("0.01"),
	}
	book := &model.OrderBook{Bids: []model.BookLevel{{Price: decimal.NewFromInt(100)}}}
	price := lighterRestingBuyPrice(inst, book)
	if price.GreaterThan(decimal.NewFromInt(99)) || price.LessThan(decimal.NewFromInt(95)) {
		t.Fatalf("resting price=%s, want conservative 1%%-5%% below bid", price)
	}
	if !price.Mod(inst.PriceTick).IsZero() {
		t.Fatalf("resting price=%s not tick-aligned to %s", price, inst.PriceTick)
	}
	maxNotional := decimal.NewFromInt(100)
	qty, err := selectLighterTestnetQuantity(inst, maxNotional, price)
	if err != nil {
		t.Fatalf("select quantity: %v", err)
	}
	if qty.Mul(price).GreaterThan(maxNotional) {
		t.Fatalf("resting notional=%s exceeds max=%s", qty.Mul(price), maxNotional)
	}
}

func TestLighterRestingPriceKeepsOneTickBestBidPositive(t *testing.T) {
	inst := &model.Instrument{PriceTick: decimal.RequireFromString("0.01")}
	book := &model.OrderBook{
		Bids: []model.BookLevel{{Price: decimal.RequireFromString("0.01")}},
		Asks: []model.BookLevel{{Price: decimal.NewFromInt(1500)}},
	}

	price := lighterRestingBuyPrice(inst, book)
	if !price.Equal(inst.PriceTick) || !price.LessThan(book.Asks[0].Price) {
		t.Fatalf("resting price=%s, want positive one-tick bid below ask", price)
	}
}

func TestLighterAcceptanceClientIDsAreExplicitAndUnique(t *testing.T) {
	first := newLighterAcceptanceClientID("Spot")
	second := newLighterAcceptanceClientID("Spot")
	if first == "" || second == "" {
		t.Fatalf("client ids must be explicit: first=%q second=%q", first, second)
	}
	if first == second {
		t.Fatalf("client ids must be unique, both=%q", first)
	}
}

func TestLighterRestingAcceptanceCallsitesUseExactDeferredCleanup(t *testing.T) {
	t.Parallel()

	for _, file := range []string{"testnet_acceptance_test.go", "testnet_runtime_acceptance_test.go"} {
		file := file
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			source := string(data)
			for _, required := range []string{
				"clientID := newLighterAcceptanceClientID",
				"ClientID:     clientID",
				"CaptureBaseline(ctx, inst)",
				"newLighterRestingOrderCleanup",
				"cleanup.ObserveSubmitResult(order)",
				"cleanup.CancelAndConfirm(ctx)",
				"cleanup.CancelConfirmAndRecover",
			} {
				if !strings.Contains(source, required) {
					t.Fatalf("%s must contain %q", file, required)
				}
			}
			if strings.Contains(source, "CancelAll(") {
				t.Fatalf("%s must not use CancelAll for acceptance cleanup", file)
			}
			primaryCancel := "adapter.Execution.Cancel(ctx, inst.ID, order.VenueOrderID)"
			if file == "testnet_runtime_acceptance_test.go" {
				primaryCancel = "node.Exec.Cancel(ctx, order.Request.ClientID)"
				if !strings.Contains(source, "runtimeaccept.AssertOversizedOrderRejected") {
					t.Fatalf("%s must retain the offline oversized-order handoff assertion", file)
				}
				if got := strings.Count(source, "node.Exec.Submit(ctx"); got != 1 {
					t.Fatalf("%s node.Exec.Submit calls=%d, want only the guarded resting lifecycle submit", file, got)
				}
				clientID := strings.Index(source, "clientID := newLighterAcceptanceClientID")
				submit := strings.Index(source, "node.Exec.Submit(ctx")
				if clientID < 0 || submit < clientID {
					t.Fatalf("%s runtime submit must occur only after explicit cleanup ownership is established", file)
				}
			}
			if !strings.Contains(source, primaryCancel) {
				t.Fatalf("%s must preserve its primary cancel path %q before exact cleanup confirmation", file, primaryCancel)
			}
		})
	}
}

func configureFastLighterCleanup(cleanup *lighterRestingOrderCleanup) {
	cleanup.pollInterval = 0
	cleanup.maxPolls = 8
	cleanup.minObservationPolls = 3
	cleanup.ambiguousMinPolls = 3
	cleanup.stableAbsentPolls = 2
}

type lighterAcceptanceExposureFake struct {
	positionSnapshots [][]model.Position
	positionCalls     int
	accountStates     []model.AccountState
	accountCalls      int
	book              *model.OrderBook
	bookErr           error
	submits           []model.OrderRequest
	submitOrder       *model.Order
	submitErr         error
}

func (f *lighterAcceptanceExposureFake) Positions(context.Context) ([]model.Position, error) {
	if len(f.positionSnapshots) == 0 {
		return nil, nil
	}
	index := f.positionCalls
	if index >= len(f.positionSnapshots) {
		index = len(f.positionSnapshots) - 1
	}
	f.positionCalls++
	return append([]model.Position(nil), f.positionSnapshots[index]...), nil
}

func (f *lighterAcceptanceExposureFake) AccountState(context.Context) (model.AccountState, error) {
	if len(f.accountStates) == 0 {
		return model.AccountState{}, nil
	}
	index := f.accountCalls
	if index >= len(f.accountStates) {
		index = len(f.accountStates) - 1
	}
	f.accountCalls++
	return f.accountStates[index], nil
}

func (f *lighterAcceptanceExposureFake) OrderBook(context.Context, model.InstrumentID, int) (*model.OrderBook, error) {
	return f.book, f.bookErr
}

func (f *lighterAcceptanceExposureFake) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	f.submits = append(f.submits, req)
	return f.submitOrder, f.submitErr
}

func lighterExposureAccountState(currency, total, available string) model.AccountState {
	return model.AccountState{Balances: []model.AccountBalance{{
		AccountID: model.AccountIDLighterDefault,
		Currency:  currency,
		Total:     decimal.RequireFromString(total),
		Free:      decimal.RequireFromString(available),
		Available: decimal.RequireFromString(available),
	}}}
}

func lighterCleanupInstrumentID() model.InstrumentID {
	return model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindPerp}
}

func lighterCleanupOrder(id model.InstrumentID, clientID, venueOrderID string, status enums.OrderStatus, filled decimal.Decimal) model.Order {
	return model.Order{
		Request: model.OrderRequest{
			AccountID:    model.AccountIDLighterDefault,
			InstrumentID: id,
			ClientID:     clientID,
			Quantity:     decimal.NewFromInt(1),
		},
		VenueOrderID: venueOrderID,
		Status:       status,
		FilledQty:    filled,
	}
}

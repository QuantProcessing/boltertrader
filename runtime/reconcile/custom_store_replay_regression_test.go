package reconcile_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/reconcile"
	"github.com/shopspring/decimal"
)

type recordOnlyStateStore struct {
	cursorErr    error
	recordedFill *model.Fill
	recordCalls  int
	commitCalls  int
}

var _ reconcile.StateStore = (*recordOnlyStateStore)(nil)
var _ reconcile.AppliedFillRecorder = (*recordOnlyStateStore)(nil)

// Intentionally no AppliedFillReplayLoader: a successful record cannot be
// rediscovered after the process loses its in-memory reconciliation state.

func (*recordOnlyStateStore) LoadCursor(context.Context, reconcile.ScopeKey, reconcile.ReportStream) (reconcile.Cursor, error) {
	return reconcile.Cursor{}, nil
}

func (*recordOnlyStateStore) BeginPass(context.Context, reconcile.PassHeader) error {
	return nil
}

func (*recordOnlyStateStore) RecordFinding(context.Context, reconcile.Finding) error {
	return nil
}

func (s *recordOnlyStateStore) CommitCursor(context.Context, reconcile.Cursor) error {
	s.commitCalls++
	return s.cursorErr
}

func (*recordOnlyStateStore) LoadOpenFindings(context.Context, reconcile.ScopeKey) ([]reconcile.Finding, error) {
	return nil, nil
}

func (s *recordOnlyStateStore) RecordAppliedFill(
	_ context.Context,
	_ reconcile.PassHeader,
	_ contract.EventMeta,
	fill model.Fill,
	_ time.Time,
) (string, error) {
	s.recordCalls++
	s.recordedFill = &fill
	return "recorded-fill", nil
}

type recoveredFillExec struct {
	fill model.Fill
	at   time.Time
}

func (*recoveredFillExec) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: "CUSTOM",
		Reports: contract.ReportCapabilities{
			FillHistory: true,
		},
	}
}

func (*recoveredFillExec) Submit(context.Context, model.OrderRequest) (*model.Order, error) {
	return nil, contract.ErrNotSupported
}

func (*recoveredFillExec) Cancel(context.Context, model.InstrumentID, string) error {
	return contract.ErrNotSupported
}

func (*recoveredFillExec) CancelAll(context.Context, model.InstrumentID) error {
	return contract.ErrNotSupported
}

func (*recoveredFillExec) Modify(context.Context, model.InstrumentID, string, decimal.Decimal, decimal.Decimal) (*model.Order, error) {
	return nil, contract.ErrNotSupported
}

func (*recoveredFillExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	return nil, contract.ErrNotSupported
}

func (*recoveredFillExec) GenerateOrderStatusReports(context.Context, model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	return nil, contract.ErrNotSupported
}

func (*recoveredFillExec) GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return nil, contract.ErrNotSupported
}

func (*recoveredFillExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, contract.ErrNotSupported
}

func (*recoveredFillExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	return nil, contract.ErrNotSupported
}

func (e *recoveredFillExec) GenerateExecutionMassStatus(context.Context, model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	mass := model.NewExecutionMassStatus("CUSTOM", e.fill.AccountID, e.at)
	err := mass.AddFillReport(model.FillReport{
		Venue:      "CUSTOM",
		AccountID:  e.fill.AccountID,
		Fill:       e.fill,
		ReportedAt: e.at,
	})
	return mass, err
}

func (*recoveredFillExec) Events() <-chan contract.ExecEnvelope { return nil }
func (*recoveredFillExec) Close() error                         { return nil }

func TestRecorderWithoutReplayLoaderFailsClosedAfterCursorCommitCrash(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	instrument := model.InstrumentID{Venue: "CUSTOM", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	fill := model.Fill{
		AccountID:    "acct",
		InstrumentID: instrument,
		VenueOrderID: "venue-order",
		ClientID:     "client-order",
		TradeID:      "trade",
		Side:         enums.SideBuy,
		Price:        decimal.RequireFromString("100"),
		Quantity:     decimal.RequireFromString("1"),
		Timestamp:    at,
	}
	cursorErr := errors.New("injected cursor commit failure")
	store := &recordOnlyStateStore{cursorErr: cursorErr}

	// Model the crash window directly: the external store durably records the
	// business-applied fill, then fails to commit the cursor that references it.
	if _, err := store.RecordAppliedFill(ctx, reconcile.PassHeader{}, contract.EventMeta{}, fill, at); err != nil {
		t.Fatalf("record applied fill: %v", err)
	}
	if err := store.CommitCursor(ctx, reconcile.Cursor{}); !errors.Is(err, cursorErr) {
		t.Fatalf("commit cursor error=%v, want %v", err, cursorErr)
	}
	if store.recordedFill == nil || store.recordedFill.TradeID != fill.TradeID {
		t.Fatalf("recorded fill=%+v, want durable pre-crash fill %q", store.recordedFill, fill.TradeID)
	}

	c := cache.New()
	c.UpsertOrder(model.Order{
		Request: model.OrderRequest{
			AccountID:    fill.AccountID,
			InstrumentID: instrument,
			ClientID:     fill.ClientID,
			Side:         fill.Side,
			Quantity:     decimal.RequireFromString("1"),
			Price:        decimal.RequireFromString("100"),
		},
		VenueOrderID: fill.VenueOrderID,
		Status:       enums.StatusNew,
	})
	applications := 0
	_, err := reconcile.New(nil, &recoveredFillExec{fill: fill, at: at}, c).
		WithAccountID(fill.AccountID).
		WithStateStore(store).
		WithFillApplier(func(model.Fill, contract.EventMeta) reconcile.FillApplyResult {
			applications++
			return reconcile.FillApplyApplied
		}).
		Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "crash recovery") {
		t.Fatalf("restart error=%v, want fail-closed crash-recovery error", err)
	}
	if errors.Is(err, cursorErr) {
		t.Fatalf("restart reached cursor commit again: %v", err)
	}
	if applications != 0 {
		t.Fatalf("restart business applications=%d, want 0", applications)
	}
	if store.recordCalls != 1 {
		t.Fatalf("applied-fill records=%d, want only the pre-crash record", store.recordCalls)
	}
	if store.commitCalls != 1 {
		t.Fatalf("cursor commits=%d, want only the pre-crash failed commit", store.commitCalls)
	}
}

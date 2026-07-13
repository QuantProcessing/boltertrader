package runtimeaccept

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/shopspring/decimal"
)

func TestAdapterOrderLifecyclePlacesCancelsFillsAndCloses(t *testing.T) {
	exec := &recordingLifecycleExec{}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	result, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "test perp",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result.FilledQty.String() != "0.01" {
		t.Fatalf("filled qty=%s, want 0.01", result.FilledQty)
	}
	if result.ClosedQty.String() != "0.01" {
		t.Fatalf("closed qty=%s, want 0.01", result.ClosedQty)
	}
	if len(exec.submits) != 3 {
		t.Fatalf("submits=%d, want resting/fill/close: %+v", len(exec.submits), exec.submits)
	}
	if got := exec.submits[0]; got.Side != enums.SideBuy || got.TIF != enums.TifGTX || got.Price.String() != "49000" {
		t.Fatalf("resting submit=%+v", got)
	}
	if got := exec.cancelVenueOrderID; got != "venue-1" {
		t.Fatalf("cancel venue order id=%q, want venue-1", got)
	}
	if got := exec.submits[1]; got.Side != enums.SideBuy || got.TIF != enums.TifIOC || got.Price.String() != "51000" {
		t.Fatalf("fill submit=%+v", got)
	}
	if got := exec.submits[2]; got.Side != enums.SideSell || got.TIF != enums.TifIOC || !got.ReduceOnly || got.Price.String() != "50000" {
		t.Fatalf("close submit=%+v", got)
	}
}

func TestAdapterOrderLifecycleUsesExplicitCloseQuantity(t *testing.T) {
	exec := &recordingLifecycleExec{}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "ETH-USDT", Kind: enums.KindSpot}
	result, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "test spot",
		AccountID:      "TEST:cash",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		CloseQuantity:  decimal.RequireFromString("0.009"),
		RestingPrice:   decimal.RequireFromString("4900"),
		FillPrice:      decimal.RequireFromString("5100"),
		ClosePrice:     decimal.RequireFromString("5000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result.ClosedQty.String() != "0.009" {
		t.Fatalf("closed qty=%s, want 0.009", result.ClosedQty)
	}
	if len(exec.submits) != 3 {
		t.Fatalf("submits=%d, want resting/fill/close: %+v", len(exec.submits), exec.submits)
	}
	if got := exec.submits[2]; got.Side != enums.SideSell || got.Quantity.String() != "0.009" || got.ReduceOnly {
		t.Fatalf("close submit=%+v", got)
	}
}

func TestAdapterOrderLifecycleLogsAcceptanceEvidence(t *testing.T) {
	exec := &recordingLifecycleExec{}
	var logs []string
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:               "test perp",
		Venue:               "TEST",
		Environment:         "Demo",
		Product:             "USDT-linear Perp/SWAP",
		AccountID:           "TEST:unified",
		InstrumentID:        instID,
		Quantity:            decimal.RequireFromString("0.01"),
		RestingPrice:        decimal.RequireFromString("49000"),
		FillPrice:           decimal.RequireFromString("51000"),
		ClosePrice:          decimal.RequireFromString("50000"),
		PositionSide:        enums.PosNet,
		CloseAfterFill:      true,
		PrivateStreamTopics: []string{"order", "execution", "position", "wallet"},
		Logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{
		"venue=TEST",
		"environment=Demo",
		"product=USDT-linear Perp/SWAP",
		"instrument=TEST:BTC-USDT:PERP",
		"account_id=TEST:unified",
		"private_stream_topics=order,execution,position,wallet",
		"resting_order",
		"venue_order_id=venue-1",
		"filled_order",
		"closed_order",
		"cleanup=no_open_orders",
		"cleanup=flat_position",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("logs missing %q:\n%s", want, joined)
		}
	}
}

func TestAdapterOrderLifecycleRejectsMismatchedAccountIDEvidence(t *testing.T) {
	exec := &mismatchedAccountLifecycleExec{}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "mismatched account",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
	})
	if err == nil || !strings.Contains(err.Error(), "account_id") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want account_id mismatch", err)
	}
}

func TestAdapterOrderLifecycleCanCleanExistingPosition(t *testing.T) {
	exec := &cleanupLifecycleExec{existing: decimal.RequireFromString("0.0003")}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:                 "cleanup test perp",
		AccountID:             "TEST:unified",
		InstrumentID:          instID,
		Quantity:              decimal.RequireFromString("0.01"),
		RestingPrice:          decimal.RequireFromString("49000"),
		FillPrice:             decimal.RequireFromString("51000"),
		ClosePrice:            decimal.RequireFromString("50000"),
		PositionSide:          enums.PosNet,
		CloseAfterFill:        true,
		CleanExistingPosition: true,
		PollInterval:          time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if len(exec.submits) != 4 {
		t.Fatalf("submits=%d, want pre-clean plus lifecycle: %+v", len(exec.submits), exec.submits)
	}
	if got := exec.submits[0]; got.Side != enums.SideSell || !got.ReduceOnly || got.Quantity.String() != "0.0003" {
		t.Fatalf("pre-clean submit=%+v", got)
	}
}

func TestAdapterOrderLifecycleAttemptsEmergencyFlattenAfterCloseFailure(t *testing.T) {
	exec := &closeFailureCleanupExec{}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "close failure cleanup",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "forced close failure") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want close failure", err)
	}
	if !exec.existing.IsZero() || exec.reduceOnlyAttempts != 2 || exec.cancelAllCalls == 0 {
		t.Fatalf("cleanup evidence existing=%s reduceAttempts=%d cancelAll=%d", exec.existing, exec.reduceOnlyAttempts, exec.cancelAllCalls)
	}
}

func TestAdapterOrderLifecycleRetriesSlowFillPoll(t *testing.T) {
	exec := &slowFillLifecycleExec{}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	result, err := RunAdapterOrderLifecycle(ctx, exec, OrderLifecycleSpec{
		Label:              "slow fill spot",
		AccountID:          "TEST:unified",
		InstrumentID:       instID,
		Quantity:           decimal.RequireFromString("0.01"),
		RestingPrice:       decimal.RequireFromString("49000"),
		FillPrice:          decimal.RequireFromString("51000"),
		ClosePrice:         decimal.RequireFromString("50000"),
		PositionSide:       enums.PosNet,
		CloseAfterFill:     false,
		PollInterval:       time.Millisecond,
		PollRequestTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if !result.FilledQty.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("filled qty=%s, want 0.01", result.FilledQty)
	}
	if exec.fillReportCalls < 2 {
		t.Fatalf("fill report calls=%d, want retry after slow poll", exec.fillReportCalls)
	}
}

func TestRuntimeOrderLifecycleUsesTradingNodeExecution(t *testing.T) {
	exec := newRuntimeLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-lifecycle",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()

	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	var beforeClose atomic.Bool
	result, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime test perp",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		BeforeRuntimeClose: func(_ context.Context, qty decimal.Decimal) error {
			if !qty.Equal(decimal.RequireFromString("0.01")) {
				return fmt.Errorf("close readiness qty=%s", qty)
			}
			beforeClose.Store(true)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunRuntimeOrderLifecycle: %v", err)
	}
	if result.FilledQty.String() != "0.01" {
		t.Fatalf("filled qty=%s, want 0.01", result.FilledQty)
	}
	if !beforeClose.Load() {
		t.Fatal("runtime close readiness hook was not called")
	}
	if len(exec.submits) != 3 {
		t.Fatalf("submits=%d, want resting/fill/close: %+v", len(exec.submits), exec.submits)
	}
	if got := exec.submits[0].AccountID; got != "TEST:unified" {
		t.Fatalf("runtime submit account id=%q", got)
	}
	if got := exec.submits[2]; got.Side != enums.SideSell || !got.ReduceOnly {
		t.Fatalf("runtime close submit=%+v", got)
	}
	if open := node.Cache.OpenOrders(); len(open) != 0 {
		t.Fatalf("runtime cache open orders=%d, want 0: %+v", len(open), open)
	}
}

func TestRuntimeOrderLifecycleWaitsForLateFillQuantity(t *testing.T) {
	exec := newLateFillRuntimeLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-late-fill",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()

	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	result, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime late fill spot",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunRuntimeOrderLifecycle: %v", err)
	}
	if !result.FilledQty.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("filled qty=%s, want 0.01", result.FilledQty)
	}
	if got := exec.lateFills.Load(); got < 2 {
		t.Fatalf("late fill events=%d, want fill and close events", got)
	}
}

func TestRuntimeOrderLifecycleRejectsPartialCloseFill(t *testing.T) {
	exec := newPartialCloseRuntimeLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-partial-close",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()

	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime partial close",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "partial fill") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want partial fill failure", err)
	}
}

type recordingLifecycleExec struct {
	submits            []model.OrderRequest
	cancelVenueOrderID string
}

func (e *recordingLifecycleExec) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: "TEST",
		Reports: contract.ReportCapabilities{
			SingleOrderStatus: true,
			OpenOrders:        true,
			FillHistory:       true,
			PositionReports:   true,
		},
		Streaming: contract.StreamCapabilities{Execution: true},
		Trading:   contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true, Modify: true},
	}
}

func (e *recordingLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	venueID := fmt.Sprintf("venue-%d", len(e.submits))
	status := enums.StatusNew
	filled := decimal.Zero
	if len(e.submits) > 1 {
		status = enums.StatusFilled
		filled = req.Quantity
	}
	return &model.Order{
		Request:      req,
		VenueOrderID: venueID,
		Status:       status,
		FilledQty:    filled,
		AvgFillPrice: req.Price,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

func (e *recordingLifecycleExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	e.cancelVenueOrderID = venueOrderID
	return nil
}

func (e *recordingLifecycleExec) CancelAll(context.Context, model.InstrumentID) error { return nil }

func (e *recordingLifecycleExec) Modify(context.Context, model.InstrumentID, string, decimal.Decimal, decimal.Decimal) (*model.Order, error) {
	return nil, nil
}

func (e *recordingLifecycleExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	return nil, nil
}

func (e *recordingLifecycleExec) GenerateOrderStatusReports(context.Context, model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	return nil, nil
}

func (e *recordingLifecycleExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return &model.OrderStatusReport{Order: model.Order{
		Request: model.OrderRequest{
			AccountID:    query.AccountID,
			InstrumentID: query.InstrumentID,
			ClientID:     query.ClientID,
		},
		VenueOrderID: query.VenueOrderID,
		Status:       enums.StatusFilled,
		FilledQty:    decimal.RequireFromString("0.01"),
	}}, nil
}

func (e *recordingLifecycleExec) GenerateFillReports(_ context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	return []model.FillReport{{Fill: model.Fill{
		AccountID:    query.AccountID,
		InstrumentID: query.InstrumentID,
		VenueOrderID: query.VenueOrderID,
		ClientID:     query.ClientID,
		Quantity:     decimal.RequireFromString("0.01"),
		Price:        decimal.RequireFromString("51000"),
	}}}, nil
}

func (e *recordingLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	return nil, nil
}

func (e *recordingLifecycleExec) GenerateExecutionMassStatus(context.Context, model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	return model.NewExecutionMassStatus("TEST", "TEST:unified", time.Now()), nil
}

func (e *recordingLifecycleExec) Events() <-chan contract.ExecEnvelope { return nil }
func (e *recordingLifecycleExec) Close() error                         { return nil }

type mismatchedAccountLifecycleExec struct {
	recordingLifecycleExec
}

func (e *mismatchedAccountLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	order.Request.AccountID = "TEST:other"
	return order, nil
}

type runtimeLifecycleExec struct {
	recordingLifecycleExec
	events     chan contract.ExecEnvelope
	venueToReq map[string]model.OrderRequest
}

func newRuntimeLifecycleExec() *runtimeLifecycleExec {
	return &runtimeLifecycleExec{
		events:     make(chan contract.ExecEnvelope, 16),
		venueToReq: make(map[string]model.OrderRequest),
	}
}

func (e *runtimeLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	e.venueToReq[order.VenueOrderID] = order.Request
	return order, nil
}

func (e *runtimeLifecycleExec) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	if err := e.recordingLifecycleExec.Cancel(ctx, id, venueOrderID); err != nil {
		return err
	}
	req := e.venueToReq[venueOrderID]
	e.events <- contract.NewExecEnvelope(contract.OrderEvent{Order: model.Order{
		Request:      req,
		VenueOrderID: venueOrderID,
		Status:       enums.StatusCanceled,
		UpdatedAt:    time.Now(),
	}})
	return nil
}

func (e *runtimeLifecycleExec) Events() <-chan contract.ExecEnvelope { return e.events }
func (e *runtimeLifecycleExec) Close() error                         { close(e.events); return nil }

type lateFillRuntimeLifecycleExec struct {
	runtimeLifecycleExec
	lateFills atomic.Int32
}

type partialCloseRuntimeLifecycleExec struct {
	runtimeLifecycleExec
}

func newPartialCloseRuntimeLifecycleExec() *partialCloseRuntimeLifecycleExec {
	return &partialCloseRuntimeLifecycleExec{runtimeLifecycleExec: *newRuntimeLifecycleExec()}
}

func (e *partialCloseRuntimeLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.runtimeLifecycleExec.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.ReduceOnly {
		order.Status = enums.StatusCanceled
		order.FilledQty = req.Quantity.Div(decimal.NewFromInt(2))
	}
	return order, nil
}

func newLateFillRuntimeLifecycleExec() *lateFillRuntimeLifecycleExec {
	return &lateFillRuntimeLifecycleExec{runtimeLifecycleExec: *newRuntimeLifecycleExec()}
}

func (e *lateFillRuntimeLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	venueID := fmt.Sprintf("late-venue-%d", len(e.submits))
	order := model.Order{
		Request:      req,
		VenueOrderID: venueID,
		Status:       enums.StatusNew,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	e.venueToReq[venueID] = req
	if req.TIF == enums.TifIOC {
		go func(order model.Order) {
			e.events <- contract.NewExecEnvelope(contract.OrderEvent{Order: model.Order{
				Request:      order.Request,
				VenueOrderID: order.VenueOrderID,
				Status:       enums.StatusFilled,
				UpdatedAt:    time.Now(),
			}})
			time.Sleep(20 * time.Millisecond)
			e.lateFills.Add(1)
			e.events <- contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
				AccountID:    order.Request.AccountID,
				InstrumentID: order.Request.InstrumentID,
				VenueOrderID: order.VenueOrderID,
				ClientID:     order.Request.ClientID,
				TradeID:      order.VenueOrderID + "-fill",
				Side:         order.Request.Side,
				Price:        order.Request.Price,
				Quantity:     order.Request.Quantity,
				Timestamp:    time.Now(),
			}})
		}(order)
	}
	return &order, nil
}

type cleanupLifecycleExec struct {
	recordingLifecycleExec
	existing decimal.Decimal
}

type closeFailureCleanupExec struct {
	recordingLifecycleExec
	existing           decimal.Decimal
	reduceOnlyAttempts int
	cancelAllCalls     int
}

func (e *closeFailureCleanupExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		e.reduceOnlyAttempts++
		if e.reduceOnlyAttempts == 1 {
			return nil, errors.New("forced close failure")
		}
		e.existing = decimal.Zero
	}
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err == nil && req.TIF == enums.TifIOC && !req.ReduceOnly {
		e.existing = req.Quantity
	}
	return order, err
}

func (e *closeFailureCleanupExec) CancelAll(context.Context, model.InstrumentID) error {
	e.cancelAllCalls++
	return nil
}

func (e *closeFailureCleanupExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if e.existing.IsZero() {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosLong,
		Quantity:     e.existing,
	}}}, nil
}

type slowFillLifecycleExec struct {
	recordingLifecycleExec
	fillReportCalls int
}

func (e *slowFillLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	status := enums.StatusNew
	if req.TIF == enums.TifGTX {
		status = enums.StatusNew
	}
	return &model.Order{
		Request:      req,
		VenueOrderID: fmt.Sprintf("slow-venue-%d", len(e.submits)),
		Status:       status,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

func (e *slowFillLifecycleExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return &model.OrderStatusReport{Order: model.Order{
		Request: model.OrderRequest{
			AccountID:    query.AccountID,
			InstrumentID: query.InstrumentID,
			ClientID:     query.ClientID,
		},
		VenueOrderID: query.VenueOrderID,
		Status:       enums.StatusNew,
	}}, nil
}

func (e *slowFillLifecycleExec) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	e.fillReportCalls++
	if query.VenueOrderID != "" && query.ClientID != "" {
		return nil, errors.New("fill query must prefer venue order id over local client id")
	}
	if e.fillReportCalls == 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []model.FillReport{{Fill: model.Fill{
		AccountID:    query.AccountID,
		InstrumentID: query.InstrumentID,
		VenueOrderID: query.VenueOrderID,
		ClientID:     query.ClientID,
		Quantity:     decimal.RequireFromString("0.01"),
		Price:        decimal.RequireFromString("51000"),
	}}}, nil
}

func (e *cleanupLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		e.existing = decimal.Zero
	}
	e.submits = append(e.submits, req)
	venueID := fmt.Sprintf("venue-%d", len(e.submits))
	status := enums.StatusNew
	filled := decimal.Zero
	if req.TIF == enums.TifIOC || req.ReduceOnly {
		status = enums.StatusFilled
		filled = req.Quantity
	}
	return &model.Order{
		Request:      req,
		VenueOrderID: venueID,
		Status:       status,
		FilledQty:    filled,
		AvgFillPrice: req.Price,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

func (e *cleanupLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if e.existing.IsZero() {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosLong,
		Quantity:     e.existing,
	}}}, nil
}

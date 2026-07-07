package runtimetest

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

// ExecTesterConfig configures an NT-style execution smoke strategy.
type ExecTesterConfig struct {
	InstrumentID   model.InstrumentID
	OrderQty       decimal.Decimal
	RestingPrice   decimal.Decimal
	FillPrice      decimal.Decimal
	PositionSide   enums.PositionSide
	ClientIDPrefix string
}

// ExecTester submits a post-only resting order, cancels it, then submits a
// fill order and waits for the fill via runtime callbacks.
type ExecTester struct {
	strategy.Base

	instID       model.InstrumentID
	qty          decimal.Decimal
	restingPrice decimal.Decimal
	fillPrice    decimal.Decimal
	posSide      enums.PositionSide
	prefix       string

	mu              sync.Mutex
	restingClientID string
	fillClientID    string
	venueOrderIDs   []string

	fillCh chan model.Fill
	errCh  chan error
}

// NewExecTester builds an execution smoke strategy for runtime acceptance.
func NewExecTester(config ExecTesterConfig) *ExecTester {
	posSide := config.PositionSide
	if posSide != enums.PosLong && posSide != enums.PosShort {
		posSide = enums.PosNet
	}
	prefix := config.ClientIDPrefix
	if prefix == "" {
		prefix = "bte"
	}
	return &ExecTester{
		instID:       config.InstrumentID,
		qty:          config.OrderQty,
		restingPrice: config.RestingPrice,
		fillPrice:    config.FillPrice,
		posSide:      posSide,
		prefix:       prefix,
		fillCh:       make(chan model.Fill, 1),
		errCh:        make(chan error, 1),
	}
}

func (s *ExecTester) OnStart(c *strategy.Context) {
	restingClientID := s.clientOrderID("rest")
	resting, err := c.Orders.Submit(c.Ctx, model.OrderRequest{
		InstrumentID: s.instID,
		ClientID:     restingClientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     s.qty,
		Price:        s.restingPrice,
		PositionSide: s.posSide,
	})
	if err != nil {
		s.fail(fmt.Errorf("runtime submit resting order: %w", err))
		return
	}
	s.recordRestingOrder(restingClientID, resting.VenueOrderID)
	if err := c.Orders.Cancel(c.Ctx, restingClientID); err != nil {
		s.fail(fmt.Errorf("runtime cancel resting order: %w", err))
		return
	}

	fillClientID := s.clientOrderID("fill")
	fillReq := model.OrderRequest{
		InstrumentID: s.instID,
		ClientID:     fillClientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeMarket,
		Quantity:     s.qty,
		PositionSide: s.posSide,
	}
	if s.fillPrice.IsPositive() {
		fillReq.Type = enums.TypeLimit
		fillReq.TIF = enums.TifIOC
		fillReq.Price = s.fillPrice
	}
	filled, err := c.Orders.Submit(c.Ctx, fillReq)
	if err != nil {
		s.fail(fmt.Errorf("runtime submit fill order: %w", err))
		return
	}
	s.recordFillOrder(fillClientID, filled.VenueOrderID)
}

func (s *ExecTester) OnFill(_ *strategy.Context, fill model.Fill) {
	s.mu.Lock()
	want := s.fillClientID
	s.mu.Unlock()
	if want == "" || fill.ClientID != want {
		return
	}
	select {
	case s.fillCh <- fill:
	default:
	}
}

func (s *ExecTester) OnStop(c *strategy.Context) {
	for _, order := range c.Cache.OpenOrders() {
		if order.Request.InstrumentID == s.instID {
			_ = c.Orders.Cancel(context.Background(), order.Request.ClientID)
		}
	}
}

// WaitForFill blocks until the tester observes the market order fill.
func (s *ExecTester) WaitForFill(ctx context.Context) (model.Fill, error) {
	select {
	case fill := <-s.fillCh:
		return fill, nil
	case err := <-s.errCh:
		return model.Fill{}, err
	case <-ctx.Done():
		return model.Fill{}, fmt.Errorf("timed out waiting for runtime fill: %w", ctx.Err())
	}
}

// RestingClientID returns the post-only order client id.
func (s *ExecTester) RestingClientID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restingClientID
}

func (s *ExecTester) clientOrderID(kind string) string {
	return fmt.Sprintf("%s-%s-%s", s.prefix, kind, strconv.FormatInt(time.Now().UnixNano(), 36))
}

func (s *ExecTester) fail(err error) {
	select {
	case s.errCh <- err:
	default:
	}
}

func (s *ExecTester) recordRestingOrder(clientID, venueOrderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restingClientID = clientID
	if venueOrderID != "" {
		s.venueOrderIDs = append(s.venueOrderIDs, venueOrderID)
	}
}

func (s *ExecTester) recordFillOrder(clientID, venueOrderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fillClientID = clientID
	if venueOrderID != "" {
		s.venueOrderIDs = append(s.venueOrderIDs, venueOrderID)
	}
}

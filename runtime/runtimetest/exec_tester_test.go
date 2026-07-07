package runtimetest

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

func TestExecTesterSubmitsRestingCancelThenMarket(t *testing.T) {
	inst := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	orders := &recordingSubmitter{}
	tester := NewExecTester(ExecTesterConfig{
		InstrumentID:   inst,
		OrderQty:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("1500"),
		PositionSide:   enums.PosNet,
		ClientIDPrefix: "bte",
	})

	tester.OnStart(&strategy.Context{Ctx: context.Background(), Orders: orders})

	if len(orders.submits) != 2 {
		t.Fatalf("submits=%d, want resting+market", len(orders.submits))
	}
	resting := orders.submits[0]
	if resting.Type != enums.TypeLimit || resting.TIF != enums.TifGTX || !resting.Price.Equal(decimal.RequireFromString("1500")) {
		t.Fatalf("unexpected resting request: %+v", resting)
	}
	if len(orders.cancels) != 1 || orders.cancels[0] != resting.ClientID {
		t.Fatalf("cancels=%v, want first client id %q", orders.cancels, resting.ClientID)
	}
	market := orders.submits[1]
	if market.Type != enums.TypeMarket || !market.Price.IsZero() {
		t.Fatalf("unexpected market request: %+v", market)
	}
	if len(resting.ClientID) >= 36 || len(market.ClientID) >= 36 {
		t.Fatalf("client ids must fit Binance limit: resting=%q market=%q", resting.ClientID, market.ClientID)
	}

	tester.OnFill(nil, model.Fill{ClientID: resting.ClientID, Quantity: decimal.RequireFromString("0.01")})
	tester.OnFill(nil, model.Fill{ClientID: market.ClientID, Quantity: decimal.RequireFromString("0.01")})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fill, err := tester.WaitForFill(ctx)
	if err != nil {
		t.Fatalf("WaitForFill: %v", err)
	}
	if fill.ClientID != market.ClientID {
		t.Fatalf("fill client id=%q, want market %q", fill.ClientID, market.ClientID)
	}
}

func TestExecTesterUsesLimitIOCWhenFillPriceProvided(t *testing.T) {
	inst := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	orders := &recordingSubmitter{}
	tester := NewExecTester(ExecTesterConfig{
		InstrumentID:   inst,
		OrderQty:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("1500"),
		FillPrice:      decimal.RequireFromString("1600"),
		PositionSide:   enums.PosNet,
		ClientIDPrefix: "bte",
	})

	tester.OnStart(&strategy.Context{Ctx: context.Background(), Orders: orders})

	if len(orders.submits) != 2 {
		t.Fatalf("submits=%d, want resting+fill", len(orders.submits))
	}
	fill := orders.submits[1]
	if fill.Type != enums.TypeLimit || fill.TIF != enums.TifIOC || !fill.Price.Equal(decimal.RequireFromString("1600")) {
		t.Fatalf("unexpected fill request: %+v", fill)
	}
}

type recordingSubmitter struct {
	submits []model.OrderRequest
	cancels []string
}

func (r *recordingSubmitter) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	r.submits = append(r.submits, req)
	return &model.Order{
		Request:      req,
		VenueOrderID: "venue-" + req.ClientID,
		Status:       enums.StatusNew,
	}, nil
}

func (r *recordingSubmitter) Cancel(_ context.Context, clientID string) error {
	r.cancels = append(r.cancels, clientID)
	return nil
}

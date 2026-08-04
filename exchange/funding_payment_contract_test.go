package exchange

import (
	"context"
	"testing"
	"time"
)

func TestFundingPaymentRESTIsOptionalPerpFacet(t *testing.T) {
	var _ FundingPaymentREST = fundingPaymentFacetFunc(nil)
	var _ PerpFundingPaymentREST = fundingPaymentFacetFunc(nil)

	var perp PerpClient
	if _, ok := perp.(FundingPaymentREST); ok {
		t.Fatal("nil PerpClient unexpectedly satisfies optional funding-payment facet")
	}
}

func TestPerpMarkPriceRESTIsOptionalPerpFacet(t *testing.T) {
	var _ PerpMarkPriceREST = perpMarkPriceFacetFunc(nil)

	var perp PerpClient
	if _, ok := perp.(PerpMarkPriceREST); ok {
		t.Fatal("nil PerpClient unexpectedly satisfies optional mark-price REST facet")
	}
}

type fundingPaymentFacetFunc func(context.Context, FundingPaymentHistoryRequest) (FundingPaymentPage, error)

func (fn fundingPaymentFacetFunc) FundingPayments(ctx context.Context, request FundingPaymentHistoryRequest) (FundingPaymentPage, error) {
	return fn(ctx, request)
}

type perpMarkPriceFacetFunc func(context.Context, Instrument) (MarkPriceEvent, error)

func (fn perpMarkPriceFacetFunc) MarkPrice(ctx context.Context, instrument Instrument) (MarkPriceEvent, error) {
	return fn(ctx, instrument)
}

func TestFundingPaymentPageStatusesAreExactScopeOnly(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)
	page := FundingPaymentPage{
		Status: FundingPaymentsComplete,
		Page: PageInfo{
			Limit:        100,
			WindowStart:  start,
			WindowEnd:    end,
			HasMoreKnown: true,
			HasMore:      false,
		},
		CompleteThrough: end,
		Watermark: FundingPaymentWatermark{
			CompleteThrough:  end,
			EffectiveThrough: end,
			Verified:         true,
			Revision:         1,
		},
	}
	if page.Status != FundingPaymentsComplete || !page.Watermark.Verified || !page.CompleteThrough.Equal(end) {
		t.Fatalf("funding payment page lost exact-scope completeness: %+v", page)
	}
}

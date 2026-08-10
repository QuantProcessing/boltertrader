package ledgersync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
)

func TestSyncTargetDistinguishesGatewayCompletenessStatuses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     exchange.FundingPaymentStatus
		items      []boltertrader.FundingPaymentResource
		wantIngest bool
		wantFacts  int
	}{
		{name: "unsupported", status: exchange.FundingPaymentsUnsupported},
		{name: "temporarily unavailable", status: exchange.FundingPaymentsTemporarilyUnavailable},
		{name: "incomplete", status: exchange.FundingPaymentsIncomplete},
		{name: "complete empty", status: exchange.FundingPaymentsCompleteEmpty, wantIngest: true},
		{name: "complete", status: exchange.FundingPaymentsComplete, items: []boltertrader.FundingPaymentResource{paymentResource("pay-1", "0.25", "gateway-fact-1")}, wantIngest: true, wantFacts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := testTarget()
			gateway := &fakeFundingGateway{pages: []boltertrader.FundingPaymentPageResource{
				pageResource(target, tc.status, "", false, tc.items...),
			}}
			sink := &fakeSink{}
			publisher := &fakePublisher{}
			resolver := fakeResolver{"gateway-fact-1": "ledger-fact-1"}
			service := NewService(gateway, sink, ServiceOptions{Publisher: publisher, Resolver: resolver, PageLimit: 2, MaxPages: 3})

			result, err := service.SyncTarget(context.Background(), target)
			if err != nil {
				t.Fatalf("SyncTarget: %v", err)
			}

			if result.Status != ledgerStatus(tc.status) {
				t.Fatalf("status=%q want %q", result.Status, ledgerStatus(tc.status))
			}
			if result.Verified != (tc.status == exchange.FundingPaymentsComplete || tc.status == exchange.FundingPaymentsCompleteEmpty) {
				t.Fatalf("verified=%v", result.Verified)
			}
			if len(sink.ingests) != boolInt(tc.wantIngest) {
				t.Fatalf("ingests=%d want %v", len(sink.ingests), tc.wantIngest)
			}
			if len(sink.watermarks) != boolInt(!tc.wantIngest) {
				t.Fatalf("watermarks=%d want non-final append=%v", len(sink.watermarks), !tc.wantIngest)
			}
			if tc.wantIngest && len(sink.ingests[0].Payments) != tc.wantFacts {
				t.Fatalf("facts=%d want %d", len(sink.ingests[0].Payments), tc.wantFacts)
			}
		})
	}
}

func TestSyncTargetAggregatesTwoPagesThenIngestsOnceAndPublishesAfterCommit(t *testing.T) {
	target := testTarget()
	second := pageResource(target, exchange.FundingPaymentsComplete, "", false,
		paymentResource("pay-2", "-0.75", "gateway-fact-2"),
	)
	first := pageResource(target, exchange.FundingPaymentsComplete, "cursor-2", true,
		paymentResource("pay-1", "0.25", "gateway-fact-1"),
	)
	first.Status = exchange.FundingPaymentsIncomplete
	gateway := &fakeFundingGateway{pages: []boltertrader.FundingPaymentPageResource{first, second}}
	sink := &fakeSink{}
	publisher := &fakePublisher{}
	service := NewService(gateway, sink, ServiceOptions{
		Publisher: publisher,
		Resolver:  fakeResolver{"gateway-fact-1": "ledger-fact-1", "gateway-fact-2": "ledger-fact-2"},
		PageLimit: 1,
		MaxPages:  3,
	})

	result, err := service.SyncTarget(context.Background(), target)
	if err != nil {
		t.Fatalf("SyncTarget: %v", err)
	}

	if result.Status != ledgerfacts.Complete || !result.Verified {
		t.Fatalf("result=%+v", result)
	}
	if len(gateway.requests) != 2 || gateway.requests[0].Cursor != "" || gateway.requests[1].Cursor != "cursor-2" {
		t.Fatalf("requests=%+v", gateway.requests)
	}
	if len(sink.ingests) != 1 || len(sink.watermarks) != 0 {
		t.Fatalf("ingests=%d watermarks=%d", len(sink.ingests), len(sink.watermarks))
	}
	batch := sink.ingests[0]
	if len(batch.Payments) != 2 {
		t.Fatalf("payments=%d", len(batch.Payments))
	}
	if batch.Scope.InstrumentID != target.InstrumentID || batch.Watermark.Status != ledgerfacts.Complete || !batch.Watermark.Verified {
		t.Fatalf("batch=%+v watermark=%+v", batch.Scope, batch.Watermark)
	}
	if got := batch.Payments[0].Payment.Amount.String(); got != "0.25" {
		t.Fatalf("amount=%s", got)
	}
	if batch.Payments[0].VenueEnvironment != "demo" || batch.Payments[0].Product != "perp" ||
		batch.Payments[0].Provenance.SourceRef != "pay-1-gateway-fact" || batch.Payments[0].Provenance.SourceRevision == "" ||
		batch.Payments[0].FormulaVersion != "gateway-funding-v1" {
		t.Fatalf("mapped payment missing native metadata/provenance: %+v", batch.Payments[0])
	}
	if batch.Scope.PaginationTraversal == "gateway_funding_payments" || !strings.HasPrefix(batch.Scope.PaginationTraversal, "gateway_funding_payments:") {
		t.Fatalf("pagination traversal did not bind the full cursor walk: %q", batch.Scope.PaginationTraversal)
	}
	if batch.Payments[0].SupersedesFactID != "ledger-fact-1" {
		t.Fatalf("supersedes=%q", batch.Payments[0].SupersedesFactID)
	}
	if len(publisher.events) != 3 {
		t.Fatalf("events=%d want payment,payment,verification", len(publisher.events))
	}
	if publisher.events[0].Topic != "ledger.funding_payment.append" || publisher.events[2].Topic != "verification.status" {
		t.Fatalf("events=%+v", publisher.events)
	}
	if publisher.firstPublishOrder <= sink.commitOrder {
		t.Fatalf("publish order=%d commit order=%d", publisher.firstPublishOrder, sink.commitOrder)
	}
}

func TestSyncTargetNormalizesFinalCompleteEmptyAfterEarlierPaymentPage(t *testing.T) {
	target := testTarget()
	first := pageResource(target, exchange.FundingPaymentsIncomplete, "cursor-2", true,
		paymentResource("pay-1", "0.25", ""),
	)
	last := pageResource(target, exchange.FundingPaymentsCompleteEmpty, "", false)
	sink := &fakeSink{}
	service := NewService(&fakeFundingGateway{pages: []boltertrader.FundingPaymentPageResource{first, last}}, sink, ServiceOptions{MaxPages: 3})

	result, err := service.SyncTarget(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ledgerfacts.Complete || len(sink.ingests) != 1 || len(sink.ingests[0].Payments) != 1 {
		t.Fatalf("result=%+v ingests=%+v", result, sink.ingests)
	}
}

func TestSyncTargetUsesAllInstrumentsLedgerScopeWhenGatewayScopeIsEmpty(t *testing.T) {
	target := testTarget()
	target.InstrumentID = ""
	page := pageResource(target, exchange.FundingPaymentsComplete, "", false,
		paymentResource("pay-all", "1.5", ""),
	)
	page.Scope.AllInstruments = true
	gateway := &fakeFundingGateway{pages: []boltertrader.FundingPaymentPageResource{page}}
	sink := &fakeSink{}
	service := NewService(gateway, sink, ServiceOptions{MaxPages: 2})

	_, err := service.SyncTarget(context.Background(), target)
	if err != nil {
		t.Fatalf("SyncTarget: %v", err)
	}
	if got := sink.ingests[0].Scope.InstrumentID; got != ledgerfacts.AllInstruments {
		t.Fatalf("instrument scope=%q want all", got)
	}
	if got := sink.ingests[0].Payments[0].Scope.InstrumentID; got != ledgerfacts.AllInstruments {
		t.Fatalf("payment instrument scope=%q want all", got)
	}
}

func TestSyncTargetRejectsCursorCycleScopeDriftAndInvalidPayment(t *testing.T) {
	target := testTarget()
	for _, tc := range []struct {
		name string
		page boltertrader.FundingPaymentPageResource
	}{
		{name: "cursor cycle", page: pageResource(target, exchange.FundingPaymentsIncomplete, "loop", true)},
		{name: "scope drift", page: func() boltertrader.FundingPaymentPageResource {
			page := pageResource(target, exchange.FundingPaymentsCompleteEmpty, "", false)
			page.Scope.AccountID = "other-account"
			return page
		}()},
		{name: "invalid decimal", page: pageResource(target, exchange.FundingPaymentsComplete, "", false,
			paymentResource("pay-bad", "not-a-decimal", ""),
		)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &fakeFundingGateway{pages: []boltertrader.FundingPaymentPageResource{tc.page, tc.page}}
			sink := &fakeSink{}
			service := NewService(gateway, sink, ServiceOptions{MaxPages: 3})

			_, err := service.SyncTarget(context.Background(), target)
			if err == nil {
				t.Fatalf("SyncTarget error=nil")
			}
			if len(sink.ingests) != 0 || len(sink.watermarks) != 0 {
				t.Fatalf("sink should not be called on validation error: %+v %+v", sink.ingests, sink.watermarks)
			}
		})
	}
}

func TestSyncTargetRequiresSupersededFactResolutionAndDoesNotPublishOnCommitFailure(t *testing.T) {
	target := testTarget()
	gateway := &fakeFundingGateway{pages: []boltertrader.FundingPaymentPageResource{
		pageResource(target, exchange.FundingPaymentsComplete, "", false,
			paymentResource("pay-revision", "0.25", "gateway-fact-missing"),
		),
	}}
	service := NewService(gateway, &fakeSink{}, ServiceOptions{MaxPages: 2})

	_, err := service.SyncTarget(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("error=%v want superseded resolution failure", err)
	}

	sink := &fakeSink{ingestErr: errors.New("commit token=secret raw upstream")}
	publisher := &fakePublisher{}
	gateway = &fakeFundingGateway{pages: []boltertrader.FundingPaymentPageResource{
		pageResource(target, exchange.FundingPaymentsComplete, "", false,
			paymentResource("pay-revision", "0.25", "gateway-fact-missing"),
		),
	}}
	service = NewService(gateway, sink, ServiceOptions{
		Publisher: publisher,
		Resolver:  fakeResolver{"gateway-fact-missing": "ledger-fact-old"},
		MaxPages:  2,
	})

	_, err = service.SyncTarget(context.Background(), target)
	if err == nil {
		t.Fatalf("commit error=nil")
	}
	if len(publisher.events) != 0 {
		t.Fatalf("published despite failed commit: %+v", publisher.events)
	}
}

func TestSyncTargetPublishesSanitizedUnavailableStatusOnGatewayError(t *testing.T) {
	target := testTarget()
	gateway := &fakeFundingGateway{err: errors.New("upstream failed token=secret raw upstream stack")}
	sink := &fakeSink{}
	publisher := &fakePublisher{}
	service := NewService(gateway, sink, ServiceOptions{Publisher: publisher, MaxPages: 2})

	result, err := service.SyncTarget(context.Background(), target)
	if err == nil {
		t.Fatalf("SyncTarget error=nil")
	}
	if result.Status != ledgerfacts.TemporarilyUnavailable || len(sink.watermarks) != 1 {
		t.Fatalf("result=%+v watermarks=%d", result, len(sink.watermarks))
	}
	if len(publisher.events) != 1 || publisher.events[0].Topic != "verification.status" {
		t.Fatalf("events=%+v", publisher.events)
	}
	rendered := fmt.Sprintf("%+v", publisher.events[0].Payload)
	if strings.Contains(rendered, "secret") || strings.Contains(rendered, "raw upstream") {
		t.Fatalf("payload leaked raw upstream error: %s", rendered)
	}
}

func testTarget() SyncTarget {
	start := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	return SyncTarget{
		PrincipalID:     "principal-1",
		Mode:            "test",
		AccountID:       "account-1",
		InstrumentID:    "BTC-USDT-PERP",
		SettlementAsset: "USDT",
		WindowStart:     start,
		WindowEnd:       start.Add(8 * time.Hour),
		PositionID:      "position-1",
	}
}

func pageResource(target SyncTarget, status exchange.FundingPaymentStatus, nextCursor string, hasMore bool, items ...boltertrader.FundingPaymentResource) boltertrader.FundingPaymentPageResource {
	return boltertrader.FundingPaymentPageResource{
		Items:           items,
		NextCursor:      nextCursor,
		AccountID:       target.AccountID,
		InstrumentID:    target.InstrumentID,
		SettlementAsset: target.SettlementAsset,
		Scope: boltertrader.FundingPaymentScopeResource{
			AccountID:       target.AccountID,
			InstrumentID:    target.InstrumentID,
			AllInstruments:  target.InstrumentID == "",
			SettlementAsset: target.SettlementAsset,
			Start:           target.WindowStart,
			End:             target.WindowEnd,
		},
		PaginationTraversal: boltertrader.FundingPaymentTraversalResource{
			HasMoreKnown: true,
			HasMore:      hasMore,
		},
		Start: target.WindowStart,
		End:   target.WindowEnd,
		Page: exchange.PageInfo{
			WindowStart:  target.WindowStart,
			WindowEnd:    target.WindowEnd,
			HasMoreKnown: true,
			HasMore:      hasMore,
		},
		Watermark: exchange.FundingPaymentWatermark{
			Cursor:           nextCursor,
			Revision:         7,
			ObservedAt:       target.WindowEnd.Add(time.Minute),
			EffectiveThrough: target.WindowEnd,
			CompleteThrough:  target.WindowEnd,
			Verified:         status == exchange.FundingPaymentsComplete || status == exchange.FundingPaymentsCompleteEmpty,
		},
		Status:          status,
		AsOf:            target.WindowEnd.Add(time.Minute),
		CompleteThrough: target.WindowEnd,
		Revision:        7,
	}
}

func paymentResource(paymentID, amount, supersedes string) boltertrader.FundingPaymentResource {
	return boltertrader.FundingPaymentResource{
		PaymentID:        paymentID,
		FactID:           paymentID + "-gateway-fact",
		SupersedesFactID: supersedes,
		AccountID:        "account-1",
		InstrumentID:     "BTC-USDT-PERP",
		Venue:            exchange.Venue("binance"),
		VenueEnvironment: "demo",
		Product:          exchange.Product("perp"),
		SettlementAsset:  "USDT",
		Amount:           amount,
		EffectiveAt:      time.Date(2026, 7, 31, 4, 0, 0, 0, time.UTC),
		ObservedAt:       time.Date(2026, 7, 31, 4, 1, 0, 0, time.UTC),
		Revision:         7,
		Version:          "v7",
		SourceRef:        "gateway://funding/" + paymentID,
		FormulaVersion:   "gateway-funding-v1",
	}
}

type fakeFundingGateway struct {
	pages    []boltertrader.FundingPaymentPageResource
	err      error
	requests []boltertrader.GatewayFundingPaymentRequest
}

func (gateway *fakeFundingGateway) FundingPayments(_ context.Context, request boltertrader.GatewayFundingPaymentRequest) (boltertrader.FundingPaymentPageResource, error) {
	gateway.requests = append(gateway.requests, request)
	if gateway.err != nil {
		return boltertrader.FundingPaymentPageResource{}, gateway.err
	}
	if len(gateway.pages) == 0 {
		return boltertrader.FundingPaymentPageResource{}, errors.New("no page")
	}
	page := gateway.pages[0]
	gateway.pages = gateway.pages[1:]
	return page, nil
}

type fakeSink struct {
	ingests      []ledgerfacts.FundingIngestBatch
	watermarks   []ledgerfacts.SyncWatermark
	ingestErr    error
	watermarkErr error
	commitOrder  int
}

var commitCounter int

func (sink *fakeSink) IngestFundingPayments(_ context.Context, batch ledgerfacts.FundingIngestBatch) ([]ledgerfacts.FundingPaymentFact, ledgerfacts.SyncWatermark, error) {
	if sink.ingestErr != nil {
		return nil, ledgerfacts.SyncWatermark{}, sink.ingestErr
	}
	commitCounter++
	sink.commitOrder = commitCounter
	sink.ingests = append(sink.ingests, batch)
	return batch.Payments, batch.Watermark, nil
}

func (sink *fakeSink) AppendSyncWatermark(_ context.Context, watermark ledgerfacts.SyncWatermark) (ledgerfacts.SyncWatermark, error) {
	if sink.watermarkErr != nil {
		return ledgerfacts.SyncWatermark{}, sink.watermarkErr
	}
	commitCounter++
	sink.commitOrder = commitCounter
	sink.watermarks = append(sink.watermarks, watermark)
	return watermark, nil
}

type fakePublisher struct {
	events            []Event
	firstPublishOrder int
}

func (publisher *fakePublisher) Publish(_ context.Context, event Event) error {
	if publisher.firstPublishOrder == 0 {
		commitCounter++
		publisher.firstPublishOrder = commitCounter
	}
	publisher.events = append(publisher.events, event)
	return nil
}

type fakeResolver map[string]string

func (resolver fakeResolver) ResolveGatewayFundingFactID(_ context.Context, scope ledgerfacts.Scope, gatewayFactID string) (string, bool, error) {
	if scope.PrincipalID == "" || scope.AccountID == "" || scope.InstrumentID == "" {
		return "", false, errors.New("missing resolver scope")
	}
	value, ok := resolver[gatewayFactID]
	return value, ok, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ledgerStatus(status exchange.FundingPaymentStatus) ledgerfacts.CompletenessStatus {
	return ledgerfacts.CompletenessStatus(status)
}

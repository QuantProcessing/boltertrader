package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/shopspring/decimal"
)

func TestFundingPaymentsRouteReturnsStructuredUnsupportedWithoutFacet(t *testing.T) {
	instrumentID, _, client, handler := newFundingPaymentFixture(t)
	client.paymentErr = exchange.ErrUnsupported

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z&pagination.limit=50", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page fundingPaymentPageResource
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if page.Status != exchange.FundingPaymentsUnsupported || len(page.Items) != 0 || page.AccountID != "acct-a" {
		t.Fatalf("unsupported page = %+v", page)
	}
	if strings.Contains(response.Body.String(), "api-secret") || strings.Contains(response.Body.String(), "native body") {
		t.Fatalf("funding payment response leaked upstream detail: %s", response.Body.String())
	}
}

func TestFundingPaymentsRouteRejectsCompletePageWithCursor(t *testing.T) {
	instrumentID, instrument, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	client.paymentPage = exchange.FundingPaymentPage{
		Status: exchange.FundingPaymentsComplete,
		Items: []exchange.FundingPayment{{
			PaymentID:       "tran-1",
			FactID:          "fact-1",
			NativeSymbol:    instrument.Identity.NativeSymbol,
			NativeID:        instrument.Identity.NativeID,
			SettlementAsset: "USDT",
			Amount:          mustDecimal("1.25"),
			EffectiveAt:     start.Add(time.Hour),
			ObservedAt:      start.Add(2 * time.Hour),
			Revision:        1,
			SourceRef:       "income:FUNDING_FEE:tran-1",
			FormulaVersion:  "actual-funding-payment-v1",
		}},
		Page: exchange.PageInfo{
			Cursor:       "next-cursor",
			Limit:        50,
			WindowStart:  start,
			WindowEnd:    end,
			HasMoreKnown: true,
			HasMore:      true,
		},
		Watermark: exchange.FundingPaymentWatermark{Cursor: "next-cursor", ObservedAt: start.Add(3 * time.Hour)},
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z&pagination.cursor=opaque&pagination.limit=50", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if client.lastPaymentRequest.Cursor != "opaque" || client.lastPaymentRequest.Limit != 50 ||
		!client.lastPaymentRequest.Start.Equal(start) || !client.lastPaymentRequest.End.Equal(end) {
		t.Fatalf("request = %+v", client.lastPaymentRequest)
	}
	_ = instrumentID
}

func TestFundingPaymentsRouteMapsTemporaryErrorsToStatus(t *testing.T) {
	_, _, client, handler := newFundingPaymentFixture(t)
	client.paymentErr = exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{
		Operation:   "FundingPayments",
		SafeMessage: "native body contained api-secret and must not leak",
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page fundingPaymentPageResource
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if page.Status != exchange.FundingPaymentsTemporarilyUnavailable {
		t.Fatalf("status=%q body=%s", page.Status, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "api-secret") || strings.Contains(response.Body.String(), "native body") {
		t.Fatalf("temporary page leaked upstream detail: %s", response.Body.String())
	}
}

func TestFundingPaymentsRouteMapsInvalidCursorToHTTP400(t *testing.T) {
	_, _, client, handler := newFundingPaymentFixture(t)
	client.paymentErr = exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{
		Operation:   "FundingPayments",
		SafeMessage: "cursor signature is invalid and api-secret must not leak",
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z&pagination.cursor=tampered", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("expected invalid_request envelope: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "api-secret") || strings.Contains(response.Body.String(), "cursor signature") ||
		strings.Contains(response.Body.String(), string(exchange.FundingPaymentsTemporarilyUnavailable)) {
		t.Fatalf("invalid cursor response leaked detail or status page: %s", response.Body.String())
	}
}

func TestFundingPaymentsRouteSupportsAllInstrumentScope(t *testing.T) {
	firstID, secondID, _, second, client, handler := newMultiFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	client.paymentPage = exchange.FundingPaymentPage{
		Status: exchange.FundingPaymentsComplete,
		Items: []exchange.FundingPayment{{
			PaymentID:       "tran-1",
			NativeSymbol:    second.Identity.NativeSymbol,
			NativeID:        second.Identity.NativeID,
			SettlementAsset: "USDT",
			Amount:          mustDecimal("1.25"),
			EffectiveAt:     start.Add(time.Hour),
			ObservedAt:      start.Add(2 * time.Hour),
			SourceRef:       "income:FUNDING_FEE:tran-1",
		}},
		Page:            exchange.PageInfo{Limit: 50, WindowStart: start, WindowEnd: end, HasMoreKnown: true},
		Watermark:       exchange.FundingPaymentWatermark{Verified: true, ObservedAt: end.Add(time.Second), EffectiveThrough: end, CompleteThrough: end, Revision: 1},
		CompleteThrough: end,
		Revision:        1,
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&settlement_asset=USDT&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z&pagination.limit=50", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if client.lastPaymentRequest.Instrument != nil || client.lastPaymentRequest.SettlementAsset != "USDT" {
		t.Fatalf("request = %+v", client.lastPaymentRequest)
	}
	var page fundingPaymentPageResource
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if page.Status != exchange.FundingPaymentsComplete || page.InstrumentID != "" || len(page.Items) != 1 ||
		page.Items[0].InstrumentID != secondID || page.Items[0].InstrumentID == firstID || page.SettlementAsset != "USDT" {
		t.Fatalf("all-scope page = %+v", page)
	}
}

func TestFundingPaymentsRouteKeepsCompleteOnlyWithVerifiedTerminalWatermark(t *testing.T) {
	instrumentID, instrument, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	client.paymentPage = exchange.FundingPaymentPage{
		Status: exchange.FundingPaymentsComplete,
		Items: []exchange.FundingPayment{{
			PaymentID: "tran-1", NativeSymbol: instrument.Identity.NativeSymbol,
			NativeID: instrument.Identity.NativeID, SettlementAsset: "USDT",
			Amount: mustDecimal("1.25"), EffectiveAt: start,
		}},
		Page:            exchange.PageInfo{Limit: 50, WindowStart: start, WindowEnd: end, HasMoreKnown: true},
		Watermark:       exchange.FundingPaymentWatermark{Verified: true, ObservedAt: end.Add(time.Second), EffectiveThrough: end, CompleteThrough: end, Revision: 7},
		CompleteThrough: end,
		Revision:        7,
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page fundingPaymentPageResource
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if page.Status != exchange.FundingPaymentsComplete {
		t.Fatalf("status=%s page=%+v", page.Status, page)
	}
}

func TestFundingPaymentsRouteRejectsCompleteWatermarkObservedBeforeEnd(t *testing.T) {
	instrumentID, instrument, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	for _, tc := range []struct {
		name       string
		observedAt time.Time
	}{
		{name: "zero"},
		{name: "before end", observedAt: end.Add(-time.Nanosecond)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client.paymentPage = exchange.FundingPaymentPage{
				Status: exchange.FundingPaymentsComplete,
				Items: []exchange.FundingPayment{{
					PaymentID: "tran-1", NativeSymbol: instrument.Identity.NativeSymbol,
					NativeID: instrument.Identity.NativeID, SettlementAsset: "USDT",
					Amount: mustDecimal("1.25"), EffectiveAt: start,
				}},
				Page:            exchange.PageInfo{Limit: 50, WindowStart: start, WindowEnd: end, HasMoreKnown: true},
				Watermark:       exchange.FundingPaymentWatermark{Verified: true, ObservedAt: tc.observedAt, EffectiveThrough: end, CompleteThrough: end, Revision: 8},
				CompleteThrough: end,
				Revision:        8,
			}

			request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil)
			authorizeFundingPaymentRequest(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFundingPaymentsRouteEnforcesPaymentEffectiveAtWindow(t *testing.T) {
	instrumentID, instrument, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	for _, tc := range []struct {
		name       string
		effective  time.Time
		wantStatus int
	}{
		{name: "before start", effective: start.Add(-time.Nanosecond), wantStatus: http.StatusInternalServerError},
		{name: "lower inclusive", effective: start, wantStatus: http.StatusOK},
		{name: "upper exclusive", effective: end, wantStatus: http.StatusInternalServerError},
		{name: "zero", wantStatus: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client.paymentPage = exchange.FundingPaymentPage{
				Status: exchange.FundingPaymentsComplete,
				Items: []exchange.FundingPayment{{
					PaymentID: "tran-1", NativeSymbol: instrument.Identity.NativeSymbol,
					NativeID: instrument.Identity.NativeID, SettlementAsset: "USDT",
					Amount: mustDecimal("1.25"), EffectiveAt: tc.effective,
				}},
				Page:            exchange.PageInfo{Limit: 50, WindowStart: start, WindowEnd: end, HasMoreKnown: true},
				Watermark:       exchange.FundingPaymentWatermark{Verified: true, ObservedAt: end.Add(time.Second), EffectiveThrough: end, CompleteThrough: end, Revision: 10},
				CompleteThrough: end,
				Revision:        10,
			}

			request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil)
			authorizeFundingPaymentRequest(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFundingPaymentsRouteMarksVerifiedTerminalEmptyPageCompleteEmpty(t *testing.T) {
	instrumentID, _, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	client.paymentPage = exchange.FundingPaymentPage{
		Status:          exchange.FundingPaymentsCompleteEmpty,
		Page:            exchange.PageInfo{Limit: 50, WindowStart: start, WindowEnd: end, HasMoreKnown: true},
		Watermark:       exchange.FundingPaymentWatermark{Verified: true, ObservedAt: end.Add(time.Second), EffectiveThrough: end, CompleteThrough: end, Revision: 3},
		CompleteThrough: end,
		Revision:        3,
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var page fundingPaymentPageResource
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if page.Status != exchange.FundingPaymentsCompleteEmpty || len(page.Items) != 0 {
		t.Fatalf("page = %+v", page)
	}
}

func TestFundingPaymentsRouteKeepsEmptyTerminalCompleteTraversalStatus(t *testing.T) {
	instrumentID, _, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	client.paymentPage = exchange.FundingPaymentPage{
		Status:          exchange.FundingPaymentsComplete,
		Page:            exchange.PageInfo{Limit: 50, WindowStart: start, WindowEnd: end, HasMoreKnown: true},
		Watermark:       exchange.FundingPaymentWatermark{Verified: true, ObservedAt: end.Add(time.Second), EffectiveThrough: end, CompleteThrough: end, Revision: 5},
		CompleteThrough: end,
		Revision:        5,
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z&pagination.cursor=terminal", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page fundingPaymentPageResource
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if page.Status != exchange.FundingPaymentsComplete || len(page.Items) != 0 {
		t.Fatalf("page = %+v", page)
	}
}

func TestFundingPaymentsRouteRejectsNonEmptyCompleteEmptyPage(t *testing.T) {
	instrumentID, instrument, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	client.paymentPage = exchange.FundingPaymentPage{
		Status: exchange.FundingPaymentsCompleteEmpty,
		Items: []exchange.FundingPayment{{
			PaymentID: "tran-1", NativeSymbol: instrument.Identity.NativeSymbol,
			NativeID: instrument.Identity.NativeID, SettlementAsset: "USDT",
			Amount: mustDecimal("1.25"), EffectiveAt: start.Add(time.Hour),
		}},
		Page:            exchange.PageInfo{Limit: 50, WindowStart: start, WindowEnd: end, HasMoreKnown: true},
		Watermark:       exchange.FundingPaymentWatermark{Verified: true, ObservedAt: end.Add(time.Second), EffectiveThrough: end, CompleteThrough: end, Revision: 6},
		CompleteThrough: end,
		Revision:        6,
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), string(exchange.FundingPaymentsComplete)) ||
		strings.Contains(response.Body.String(), string(exchange.FundingPaymentsCompleteEmpty)) {
		t.Fatalf("non-empty complete_empty status leaked: %s", response.Body.String())
	}
}

func TestFundingPaymentsRouteRejectsInconsistentCompleteWatermark(t *testing.T) {
	instrumentID, instrument, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	client.paymentPage = exchange.FundingPaymentPage{
		Status: exchange.FundingPaymentsComplete,
		Items: []exchange.FundingPayment{{
			PaymentID: "tran-1", NativeSymbol: instrument.Identity.NativeSymbol,
			NativeID: instrument.Identity.NativeID, SettlementAsset: "USDT",
			Amount: mustDecimal("1.25"), EffectiveAt: start.Add(time.Hour),
		}},
		Page:            exchange.PageInfo{Limit: 50, WindowStart: start, WindowEnd: end, HasMoreKnown: true},
		Watermark:       exchange.FundingPaymentWatermark{Verified: false, CompleteThrough: end.Add(-time.Second), Revision: 4},
		CompleteThrough: end.Add(-time.Second),
		Revision:        4,
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), string(exchange.FundingPaymentsIncomplete)) {
		t.Fatalf("inconsistent proof was emitted as incomplete: %s", response.Body.String())
	}
}

func TestFundingPaymentsRouteRejectsIncompletePageWithVerifiedWatermark(t *testing.T) {
	instrumentID, instrument, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	client.paymentPage = exchange.FundingPaymentPage{
		Status: exchange.FundingPaymentsIncomplete,
		Items: []exchange.FundingPayment{{
			PaymentID: "tran-1", NativeSymbol: instrument.Identity.NativeSymbol,
			NativeID: instrument.Identity.NativeID, SettlementAsset: "USDT",
			Amount: mustDecimal("1.25"), EffectiveAt: start.Add(time.Hour),
		}},
		Page:            exchange.PageInfo{Limit: 50, WindowStart: start, WindowEnd: end, HasMoreKnown: true, HasMore: true, Cursor: "next"},
		Watermark:       exchange.FundingPaymentWatermark{Verified: true, ObservedAt: end.Add(time.Second), EffectiveThrough: end, CompleteThrough: end, Revision: 9},
		CompleteThrough: end,
		Revision:        9,
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&instrument_id="+string(instrumentID)+"&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), string(exchange.FundingPaymentsIncomplete)) ||
		strings.Contains(response.Body.String(), `"verified":true`) {
		t.Fatalf("incomplete page retained verified proof: %s", response.Body.String())
	}
}

func TestFundingPaymentsRouteRejectsSettlementAssetMismatch(t *testing.T) {
	_, instrument, client, handler := newFundingPaymentFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client.paymentPage = exchange.FundingPaymentPage{
		Status: exchange.FundingPaymentsComplete,
		Items: []exchange.FundingPayment{{
			PaymentID: "tran-1", NativeSymbol: instrument.Identity.NativeSymbol,
			NativeID: instrument.Identity.NativeID, SettlementAsset: "USDC",
			Amount: mustDecimal("1.25"), EffectiveAt: start.Add(time.Hour),
		}},
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/funding-payments?account_id=acct-a&settlement_asset=USDT&start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil)
	authorizeFundingPaymentRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func newFundingPaymentFixture(t *testing.T) (domain.InstrumentID, exchange.Instrument, *fakeFundingPaymentPerpClient, http.Handler) {
	t.Helper()
	instrumentID, instrument, client, _ := newPublicMarketFixture(t)
	paymentClient := &fakeFundingPaymentPerpClient{PerpClient: client}
	accounts := &fakeHTTPAccounts{
		resource:   fundingPaymentAccountResource("acct-a"),
		status:     fundingPaymentAccountStatus("acct-a"),
		instrument: instrument,
		public:     fundingPaymentCatalogInstrument(instrumentID),
		capability: capabilityRowWithFundingPayments(),
		perp:       paymentClient,
	}
	wrapped := newWrappedHTTPHandler(t, accounts)
	return instrumentID, instrument, paymentClient, wrapped
}

func authorizeFundingPaymentRequest(request *http.Request) {
	request.Header.Set("Authorization", "Bearer test-token")
}

func newMultiFundingPaymentFixture(t *testing.T) (domain.InstrumentID, domain.InstrumentID, exchange.Instrument, exchange.Instrument, *fakeFundingPaymentPerpClient, http.Handler) {
	t.Helper()
	firstID, first, client, _ := newPublicMarketFixture(t)
	secondID := domain.InstrumentID("binance:live:perp:USDT:ETH-USDT")
	second := exchange.Instrument{Identity: exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
		Symbol: "ETH-USDT", BaseAsset: "ETH", QuoteAsset: "USDT", SettleAsset: "USDT",
		NativeSymbol: "ETHUSDT", NativeID: "native-ETHUSDT",
	}}
	paymentClient := &fakeFundingPaymentPerpClient{PerpClient: client}
	accounts := &fakeHTTPAccounts{
		resource:   fundingPaymentAccountResource("acct-a"),
		status:     fundingPaymentAccountStatus("acct-a"),
		instrument: first,
		public:     fundingPaymentCatalogInstrument(firstID),
		instruments: []catalog.Instrument{
			fundingPaymentCatalogInstrument(firstID),
			{
				ID: secondID, Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive,
				Product: exchange.ProductPerp, Settlement: "USDT", Symbol: "ETH-USDT",
				BaseAsset: "ETH", QuoteAsset: "USDT",
			},
		},
		resolved:   map[domain.InstrumentID]exchange.Instrument{firstID: first, secondID: second},
		capability: capabilityRowWithFundingPayments(),
		perp:       paymentClient,
	}
	return firstID, secondID, first, second, paymentClient, newWrappedHTTPHandler(t, accounts)
}

func fundingPaymentAccountResource(accountID domain.AccountID) account.Resource {
	return account.Resource{
		AccountID: accountID, Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
		Environment: exchange.EnvironmentLive, Settlement: "USDT", ReadReady: true, WriteReady: true,
	}
}

func fundingPaymentAccountStatus(accountID domain.AccountID) account.Status {
	return account.Status{Resource: fundingPaymentAccountResource(accountID)}
}

func fundingPaymentCatalogInstrument(instrumentID domain.InstrumentID) catalog.Instrument {
	return catalog.Instrument{
		ID: instrumentID, Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive,
		Product: exchange.ProductPerp, Settlement: "USDT", Symbol: "BTC-USDT",
		BaseAsset: "BTC", QuoteAsset: "USDT",
	}
}

func capabilityRowWithFundingPayments() capability.Row {
	row := capability.StaticMatrix().Rows[1]
	row.Operations.AccountFundingPayments = true
	return row
}

func newWrappedHTTPHandler(t *testing.T, accounts *fakeHTTPAccounts) http.Handler {
	t.Helper()
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: accounts, References: &fakeHTTPReferences{accounts: accounts},
		Orders: &fakeHTTPOrders{}, Events: newTestEventHub(t), Auditor: &fakeHTTPAuditor{},
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, err := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	return middleware.Wrap(handler)
}

type fakeFundingPaymentPerpClient struct {
	exchange.PerpClient
	paymentPage        exchange.FundingPaymentPage
	paymentErr         error
	lastPaymentRequest exchange.FundingPaymentHistoryRequest
}

func (client *fakeFundingPaymentPerpClient) FundingPayments(_ context.Context, request exchange.FundingPaymentHistoryRequest) (exchange.FundingPaymentPage, error) {
	client.lastPaymentRequest = request
	if client.paymentErr != nil {
		return exchange.FundingPaymentPage{}, client.paymentErr
	}
	return client.paymentPage, nil
}

func mustDecimal(value string) decimal.Decimal {
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

func TestHandlerPassesCorrelationIntoAtomicPlaceAudit(t *testing.T) {
	fixture := newHTTPFixture(t)
	body := `{"account_id":"acct-a","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-audit","side":"buy","type":"market","quantity":"0.001"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/orders", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem-audit")
	recorder := httptest.NewRecorder()

	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := fixture.orders.place.CorrelationID.Validate(); err != nil {
		t.Fatalf("place correlation ID = %q: %v", fixture.orders.place.CorrelationID, err)
	}
}

func TestHandlerAuditFailureBlocksCancelAndLeverageBeforeVenueIO(t *testing.T) {
	fixture := newAuditedHTTPFixture(t, errors.New("audit unavailable"))
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodDelete, "/v1/orders/018f0000-0000-7000-8000-000000000002", ""},
		{http.MethodPut, "/v1/accounts/acct-p/instruments/binance:demo:perp:USDT:BTC-USDT/leverage", `{"leverage":5}`},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
		request.Header.Set("Authorization", "Bearer test-token")
		if test.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("%s %s status=%d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
	if fixture.orders.cancelCalls != 0 || fixture.perp.leverageCalls != 0 {
		t.Fatalf("audit failure reached mutation backend: cancel=%d leverage=%d",
			fixture.orders.cancelCalls, fixture.perp.leverageCalls)
	}
}

func TestHandlerAuditsCancelLeverageAndUnsupportedAmendWithSafeFields(t *testing.T) {
	fixture := newAuditedHTTPFixture(t, nil)
	tests := []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodDelete, "/v1/orders/018f0000-0000-7000-8000-000000000002", "", http.StatusAccepted},
		{http.MethodPut, "/v1/accounts/acct-p/instruments/binance:demo:perp:USDT:BTC-USDT/leverage", `{"leverage":5}`, http.StatusNotImplemented},
		{http.MethodPatch, "/v1/orders/018f0000-0000-7000-8000-000000000002", `{}`, http.StatusNotImplemented},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
		request.Header.Set("Authorization", "Bearer test-token")
		if test.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}

	if len(fixture.auditor.records) != 3 {
		t.Fatalf("audit records=%+v", fixture.auditor.records)
	}
	wantActions := []store.AuditAction{store.AuditCancelOrder, store.AuditSetLeverage, store.AuditAmendOrder}
	wantOutcomes := []store.AuditOutcome{store.AuditRequested, store.AuditUnsupported, store.AuditUnsupported}
	for index, record := range fixture.auditor.records {
		if record.PrincipalID == "" || record.Action != wantActions[index] ||
			record.Outcome != wantOutcomes[index] || record.ResourceID == "" {
			t.Errorf("audit[%d]=%+v", index, record)
		}
		if err := record.CorrelationID.Validate(); err != nil {
			t.Errorf("audit[%d] correlation=%q: %v", index, record.CorrelationID, err)
		}
	}
}

func TestHandlerFailsClosedBeforeCallingNonDurableLeverageBackend(t *testing.T) {
	fixture := newAuditedHTTPFixture(t, nil)
	fixture.perp.leverageErr = exchange.ErrAmbiguousOutcome
	request := httptest.NewRequest(
		http.MethodPut,
		"/v1/accounts/acct-p/instruments/binance:demo:perp:USDT:BTC-USDT/leverage",
		bytes.NewBufferString(`{"leverage":5}`),
	)
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fixture.perp.leverageCalls != 0 {
		t.Fatalf("fail-closed leverage reached venue %d times", fixture.perp.leverageCalls)
	}
	if len(fixture.auditor.records) != 1 ||
		fixture.auditor.records[0].Action != store.AuditSetLeverage ||
		fixture.auditor.records[0].Outcome != store.AuditUnsupported {
		t.Fatalf("fail-closed leverage audit=%+v", fixture.auditor.records)
	}
}

func TestHandlerAuditsUnsupportedLeverageBeforeReturning(t *testing.T) {
	fixture := newAuditedHTTPFixture(t, nil)
	fixture.orders.cancelCalls = 0
	fixture.perp.leverageCalls = 0
	fixture.auditor.records = nil
	fixture.handler = rebuildAuditedHandler(t, fixture, capability.StaticMatrix().Rows[1])

	request := httptest.NewRequest(
		http.MethodPut,
		"/v1/accounts/acct-p/instruments/binance:demo:perp:USDT:BTC-USDT/leverage",
		bytes.NewBufferString(`{"leverage":5}`),
	)
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fixture.perp.leverageCalls != 0 {
		t.Fatalf("unsupported leverage reached venue %d times", fixture.perp.leverageCalls)
	}
	if len(fixture.auditor.records) != 1 ||
		fixture.auditor.records[0].Action != store.AuditSetLeverage ||
		fixture.auditor.records[0].Outcome != store.AuditUnsupported {
		t.Fatalf("unsupported leverage audit=%+v", fixture.auditor.records)
	}
}

type auditedHTTPFixture struct {
	handler  http.Handler
	auditor  *fakeHTTPAuditor
	orders   *fakeHTTPOrders
	perp     *fakeHTTPPerpClient
	accounts *fakeHTTPAccounts
}

func newAuditedHTTPFixture(t *testing.T, auditErr error) auditedHTTPFixture {
	t.Helper()
	base := newHTTPFixture(t)
	perp := &fakeHTTPPerpClient{}
	base.accounts.resource.AccountID = "acct-p"
	base.accounts.resource.Product = exchange.ProductPerp
	base.accounts.status.Resource = base.accounts.resource
	base.accounts.public.ID = "binance:demo:perp:USDT:BTC-USDT"
	base.accounts.public.Product = exchange.ProductPerp
	base.accounts.public.Settlement = "USDT"
	base.accounts.instrument.Identity.Product = exchange.ProductPerp
	base.accounts.instrument.Identity.SettleAsset = "USDT"
	base.accounts.capability = capability.StaticMatrix().Rows[1]
	base.accounts.perp = perp
	auditor := &fakeHTTPAuditor{err: auditErr}
	references := &fakeHTTPReferences{accounts: base.accounts}
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: base.accounts, References: references, Orders: base.orders,
		Events:  newTestEventHub(t),
		Auditor: auditor, Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, _ := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	return auditedHTTPFixture{
		handler: middleware.Wrap(handler), auditor: auditor, orders: base.orders, perp: perp, accounts: base.accounts,
	}
}

func rebuildAuditedHandler(t *testing.T, fixture auditedHTTPFixture, row capability.Row) http.Handler {
	t.Helper()
	fixture.accounts.capability = row
	references := &fakeHTTPReferences{accounts: fixture.accounts}
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: fixture.accounts, References: references, Orders: fixture.orders,
		Events:  newTestEventHub(t),
		Auditor: fixture.auditor, Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, _ := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	return middleware.Wrap(handler)
}

type fakeHTTPAuditor struct {
	records []store.AuditRecord
	err     error
}

func (auditor *fakeHTTPAuditor) AppendAudit(_ context.Context, record store.AuditRecord) error {
	if auditor.err != nil {
		return auditor.err
	}
	auditor.records = append(auditor.records, record)
	return nil
}

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/auth"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
)

func TestLedgerAndVerificationRoutesAreAuthenticatedAndExposeContractDTOs(t *testing.T) {
	totalNetPnl := "9.5"
	ledger := &fakeLedgerAPI{
		funding:      fundingPaymentPageDTO{Items: []fundingPaymentDTO{{PaymentID: "payment-1", Amount: "1.25", SettlementAsset: "USDT"}}, Status: "complete"},
		portfolio:    portfolioLedgerDTO{PrincipalID: auth.DefaultPrincipalID, Mode: "test", ReportingAsset: "USDT", TotalNetPnl: &totalNetPnl, VerificationStatus: "verified"},
		closed:       []closedPositionLedgerDTO{{PositionID: "position-1", PrincipalID: auth.DefaultPrincipalID, Mode: "test", ReportingAsset: "USDT", VerificationStatus: "verified", UnrealizedTradingPnl: "0"}},
		verification: verificationStatusDTO{Status: "verified", OpenReady: false, CloseReady: true, RecoveryReady: true},
	}
	handler, cookie := newLedgerContractServer(t, ledger)

	for _, path := range []string{
		"/api/v1/ledger/funding-payments?accountId=account-1&instrumentId=BTC-USDT-PERP",
		"/api/v1/ledger/portfolio",
		"/api/v1/positions?state=closed",
		"/api/v1/positions/position-1",
		"/api/v1/system/verification",
	} {
		unauthenticated := httptest.NewRecorder()
		handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, path, nil))
		if unauthenticated.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated status=%d body=%s", path, unauthenticated.Code, unauthenticated.Body.String())
		}

		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "gatewayInstrumentId") || strings.Contains(response.Body.String(), "credential") {
			t.Fatalf("%s leaked private transport fields: %s", path, response.Body.String())
		}
	}
	if ledger.fundingQuery.PrincipalID != auth.DefaultPrincipalID || ledger.fundingQuery.Mode != "test" ||
		ledger.fundingQuery.AccountID != "account-1" || ledger.fundingQuery.InstrumentID != "BTC-USDT-PERP" {
		t.Fatalf("funding query lost server-owned scope: %+v", ledger.fundingQuery)
	}
}

func TestLedgerFundingPaymentQueryRequiresExactAccountInstrumentScope(t *testing.T) {
	ledger := &fakeLedgerAPI{}
	handler, cookie := newLedgerContractServer(t, ledger)
	for _, path := range []string{
		"/api/v1/ledger/funding-payments",
		"/api/v1/ledger/funding-payments?accountId=account-1",
		"/api/v1/ledger/funding-payments?accountId=account-1&instrumentId=BTC-USDT-PERP&limit=101",
		"/api/v1/ledger/funding-payments?accountId=account-1&instrumentId=BTC-USDT-PERP&from=bad-time",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if ledger.fundingCalls != 0 {
		t.Fatalf("ledger was queried before scope validation: %d", ledger.fundingCalls)
	}
}

func TestLedgerUpstreamErrorsAreRedacted(t *testing.T) {
	secret := "gateway bearer super-secret"
	ledger := &fakeLedgerAPI{fundingErr: errors.New(secret)}
	handler, cookie := newLedgerContractServer(t, ledger)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/ledger/funding-payments?accountId=account-1&instrumentId=BTC-USDT-PERP", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret) || !strings.Contains(response.Body.String(), `"code":"ledger_unavailable"`) {
		t.Fatalf("unsafe ledger error body=%s", response.Body.String())
	}
}

func TestReadinessRouteExtendsLegacyPayloadAndLedgerKindsUseTheSingleStream(t *testing.T) {
	ledger := &fakeLedgerAPI{}
	handler, cookie := newLedgerContractServer(t, ledger)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/readiness", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"openReady":false`) ||
		!strings.Contains(response.Body.String(), `"closeReady":true`) ||
		!strings.Contains(response.Body.String(), `"recoveryReady":true`) {
		t.Fatalf("readiness response=%d %s", response.Code, response.Body.String())
	}

	for _, kind := range []string{
		"ledger.funding_payment.append",
		"ledger.position.verification_changed",
		"ledger.portfolio.updated",
		"verification.status",
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/stream?cursor=tampered&kind="+kind, nil)
		request.AddCookie(cookie)
		response := newStreamResponseRecorder()
		handler.ServeHTTP(response, request)
		if body := response.Body(); !strings.Contains(body, "event: resnapshot.required") {
			t.Fatalf("kind=%s stream response=%s", kind, body)
		}
	}
}

type fakeTradingReadiness struct{}

func (fakeTradingReadiness) CheckTrading(context.Context) readiness.TradingReadinessResult {
	return readiness.TradingReadinessResult{
		OpenReady: false, CloseReady: true, RecoveryReady: true,
		Reason: readiness.TradingReadinessReasonNormalTestCloseRecovery,
	}
}

type fakeLedgerAPI struct {
	funding      fundingPaymentPageDTO
	portfolio    portfolioLedgerDTO
	closed       []closedPositionLedgerDTO
	verification verificationStatusDTO
	fundingQuery ledgerFundingPaymentQuery
	fundingCalls int
	fundingErr   error
}

func (ledger *fakeLedgerAPI) FundingPayments(_ context.Context, query ledgerFundingPaymentQuery) (fundingPaymentPageDTO, error) {
	ledger.fundingCalls++
	ledger.fundingQuery = query
	return ledger.funding, ledger.fundingErr
}

func (ledger *fakeLedgerAPI) Portfolio(context.Context, readmodel.Scope, string) (portfolioLedgerDTO, bool, error) {
	return ledger.portfolio, true, nil
}

func (ledger *fakeLedgerAPI) ClosedPositions(context.Context, readmodel.Scope, string) ([]closedPositionLedgerDTO, error) {
	return append([]closedPositionLedgerDTO(nil), ledger.closed...), nil
}

func (ledger *fakeLedgerAPI) ClosedPosition(context.Context, readmodel.Scope, string) (closedPositionLedgerDTO, bool, error) {
	if len(ledger.closed) == 0 {
		return closedPositionLedgerDTO{}, false, nil
	}
	return ledger.closed[0], true, nil
}

func (ledger *fakeLedgerAPI) Verification(context.Context, readmodel.Scope) (verificationStatusDTO, error) {
	return ledger.verification, nil
}

func newLedgerContractServer(t *testing.T, ledger ledgerAPI) (http.Handler, *http.Cookie) {
	t.Helper()
	authStore := auth.NewMemoryStore()
	authService := auth.NewService(authStore, "test", time.Hour, false)
	if err := authService.Bootstrap(context.Background(), "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	stream, err := uistream.NewService(uistream.ServiceConfig{
		BootID: "ledger-http-contract", PrincipalID: auth.DefaultPrincipalID, Mode: "test",
		SigningKey: []byte("0123456789abcdef0123456789abcdef"),
		Store:      uistream.NewMemoryEventStore(), SnapshotSource: contractSnapshotSource{}, HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(ServerConfig{
		Mode: "test", Version: "test", Build: "test", Auth: authService,
		ReadModel: contractReadModel{}, Ledger: ledger, Stream: stream,
		TradingReadiness: fakeTradingReadiness{},
		Readiness: readiness.StaticChecks(map[string]readiness.Component{
			"maintenance": {Ready: true}, "process": {Ready: true}, "database": {Ready: true},
			"gateway": {Ready: true}, "publicData": {Ready: true}, "execution": {Ready: true},
		}),
	})
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"owner@example.com","password":"first secure password"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, login)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 1 {
		t.Fatalf("login status=%d body=%s cookies=%+v", response.Code, response.Body.String(), response.Result().Cookies())
	}
	return handler, response.Result().Cookies()[0]
}

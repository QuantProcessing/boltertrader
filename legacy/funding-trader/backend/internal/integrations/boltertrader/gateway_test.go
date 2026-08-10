package boltertrader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
)

func TestGatewayClientAddsBearerAuthAndRedactsErrors(t *testing.T) {
	const token = "super-secret-token"
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization = %q", got)
		}
		return jsonResponse(http.StatusBadGateway, `{"error":{"code":"venue_authentication_failed","message":"raw secret venue msg","correlation_id":"c","retryable":false}}`), nil
	})

	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: token, Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.Live(context.Background())
	if err == nil {
		t.Fatal("expected gateway error")
	}
	got := err.Error()
	if strings.Contains(got, token) || strings.Contains(got, "raw secret venue msg") {
		t.Fatalf("gateway error leaked secret/raw body: %s", got)
	}
	if !strings.Contains(got, "venue_authentication_failed") {
		t.Fatalf("gateway error lost safe code: %s", got)
	}
}

func TestGatewayClientFundingPaymentsUsesActualPaymentEndpointAndRedactsErrors(t *testing.T) {
	const token = "super-secret-token"
	start := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	end := start.Add(24*time.Hour + 987654321*time.Nanosecond)
	var sawRequest bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawRequest = true
		if r.URL.Path != "/v1/funding-payments" {
			t.Fatalf("path=%s, want /v1/funding-payments", r.URL.Path)
		}
		if r.URL.Query().Get("account_id") != "acct-a" ||
			r.URL.Query().Get("instrument_id") != "binance:demo:perp:USDT:BTC-USDT" ||
			r.URL.Query().Get("start") != start.Format(time.RFC3339Nano) ||
			r.URL.Query().Get("end") != end.Format(time.RFC3339Nano) ||
			r.URL.Query().Get("pagination.cursor") != "opaque" ||
			r.URL.Query().Get("pagination.limit") != "100" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization=%q", got)
		}
		return jsonResponse(http.StatusBadGateway, `{"error":{"code":"venue_authentication_failed","message":"native secret body","correlation_id":"c","retryable":true}}`), nil
	})

	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: token, Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.FundingPayments(context.Background(), GatewayFundingPaymentRequest{
		AccountID: "acct-a", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
		Start: start, End: end, Cursor: "opaque", Limit: 100,
	})
	if !sawRequest {
		t.Fatal("request was not sent")
	}
	if err == nil {
		t.Fatal("expected redacted gateway error")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "native secret body") {
		t.Fatalf("gateway funding-payment error leaked secret/raw body: %s", err)
	}
	if !strings.Contains(err.Error(), "venue_authentication_failed") {
		t.Fatalf("gateway funding-payment error lost safe code: %s", err)
	}
}

func TestGatewayClientFundingPaymentsDecodesStrictPageAndCapability(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/funding-payments":
			return jsonResponse(http.StatusOK, `{
				"items":[{
					"payment_id":"tran-1",
					"fact_id":"fact-1",
					"account_id":"acct-a",
					"instrument_id":"binance:demo:perp:USDT:BTC-USDT",
					"venue":"binance",
					"venue_environment":"demo",
					"product":"perp",
					"settlement_asset":"USDT",
					"amount":"1.25",
					"effective_at":"2026-01-01T08:00:00Z",
					"observed_at":"2026-01-01T08:01:00Z",
					"revision":1,
					"version":"binance-fapi-income-v1",
					"source_ref":"income:FUNDING_FEE:tran-1",
					"formula_version":"actual-funding-payment-v1"
				}],
				"scope":{"account_id":"acct-a","instrument_id":"binance:demo:perp:USDT:BTC-USDT","all_instruments":false,"settlement_asset":"USDT","start":"2026-01-01T00:00:00Z","end":"2026-01-02T00:00:00Z"},
				"account_id":"acct-a",
				"instrument_id":"binance:demo:perp:USDT:BTC-USDT",
				"settlement_asset":"USDT",
				"pagination_traversal":{"limit":100,"has_more_known":true,"has_more":false},
				"start":"2026-01-01T00:00:00Z",
				"end":"2026-01-02T00:00:00Z",
				"page":{"limit":100,"window_start":"2026-01-01T00:00:00Z","window_end":"2026-01-02T00:00:00Z","has_more_known":true,"has_more":false},
				"watermark":{"revision":1,"observed_at":"2026-01-01T08:01:00Z","effective_through":"2026-01-02T00:00:00Z","complete_through":"2026-01-02T00:00:00Z","verified":true},
				"status":"complete",
				"as_of":"2026-01-01T08:01:00Z",
				"complete_through":"2026-01-02T00:00:00Z",
				"revision":1
			}`), nil
		case "/v1/capabilities":
			return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{{
				AccountID: "acct-a",
				Capability: CapabilityRow{Operations: CapabilityOperations{
					Funding: true, AccountFundingPayments: true,
				}},
			}}}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	})

	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	page, err := client.FundingPayments(context.Background(), GatewayFundingPaymentRequest{
		AccountID: "acct-a", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
		SettlementAsset: "USDT", Start: start, End: end, Limit: 100,
	})
	if err != nil {
		t.Fatalf("FundingPayments: %v", err)
	}
	if page.Status != "complete" || !page.Watermark.Verified || len(page.Items) != 1 ||
		page.Scope.AllInstruments || page.PaginationTraversal.HasMore || page.Items[0].FactID != "fact-1" {
		t.Fatalf("funding payment page = %+v", page)
	}
	capabilities, err := client.ListCapabilities(context.Background(), "acct-a")
	if err != nil {
		t.Fatalf("ListCapabilities: %v", err)
	}
	if len(capabilities) != 1 || !capabilities[0].Capability.Operations.AccountFundingPayments {
		t.Fatalf("capabilities = %+v", capabilities)
	}
}

func TestGatewayClientRejectsRemotePlaintextLiveTransport(t *testing.T) {
	for _, rawURL := range []string{
		"http://gateway.example",
		"http://192.0.2.10:18082",
		"http://0.0.0.0:18082",
	} {
		t.Run("reject "+rawURL, func(t *testing.T) {
			if _, err := NewGatewayClient(GatewayClientOptions{
				BaseURL: rawURL, Token: "token", Mode: config.Live,
			}); err == nil || !strings.Contains(err.Error(), "live mode gateway requires https or loopback") {
				t.Fatalf("live plaintext URL %q error=%v", rawURL, err)
			}
		})
	}
	for _, rawURL := range []string{
		"http://127.0.0.1:18082",
		"http://localhost:18082",
		"http://[::1]:18082",
		"https://gateway.example",
	} {
		t.Run("allow "+rawURL, func(t *testing.T) {
			if _, err := NewGatewayClient(GatewayClientOptions{
				BaseURL: rawURL, Token: "token", Mode: config.Live,
			}); err != nil {
				t.Fatalf("live Gateway URL %q rejected: %v", rawURL, err)
			}
		})
	}
}

func TestPlaceOrderSendsMatchingIdempotencyHeaderAndBody(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts":
			return jsonFixture(GatewayCollection[AccountResource]{Items: []AccountResource{{
				AccountID: "binance-demo-usdt-perp", Venue: "binance", Product: "perp",
				VenueEnvironment: GatewayDemo, ReadReady: true, WriteReady: true,
			}}}), nil
		case "/v1/capabilities":
			return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{{
				AccountID: "binance-demo-usdt-perp",
				Capability: CapabilityRow{Operations: CapabilityOperations{
					Place: true, Cancel: true,
				}},
			}}}), nil
		case "/v1/accounts/binance-demo-usdt-perp":
			return jsonFixture(AccountStatus{AccountResource: AccountResource{
				AccountID: "binance-demo-usdt-perp", Venue: "binance", Product: "perp",
				VenueEnvironment: GatewayDemo, ReadReady: true, WriteReady: true,
			}}), nil
		case "/v1/orders":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s", r.Method)
			}
			var body PlaceOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if r.Header.Get("Idempotency-Key") != "idem-1" || body.IdempotencyKey != "idem-1" {
				t.Fatalf("idempotency mismatch header=%q body=%q", r.Header.Get("Idempotency-Key"), body.IdempotencyKey)
			}
			return jsonFixtureStatus(http.StatusAccepted, OrderResult{GatewayOrderID: "018f0000-0000-7000-8000-000000000001", State: "accepted"}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	})

	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	result, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{
		AccountID: "binance-demo-usdt-perp", InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
		IdempotencyKey: "idem-1", Side: "buy", Type: "market", Quantity: "0.001",
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if result.State != "accepted" {
		t.Fatalf("result = %+v", result)
	}
}

func TestWritePolicyBlocksLiveUnlessExplicitlyEnabled(t *testing.T) {
	transport := discoveryTransport(t, GatewayLive, true, true)
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "https://gateway.local", Token: "token", Mode: config.Live,
		ProductionWritesEnabled: false,
		HTTPClient:              &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.PlaceOrder(context.Background(), PlaceOrderRequest{AccountID: "acct", IdempotencyKey: "idem"})
	if !errors.Is(err, ErrGatewayWriteBlocked) {
		t.Fatalf("PlaceOrder err = %v, want write blocked", err)
	}

	client.productionWritesEnabled = true
	_, err = client.PlaceOrder(context.Background(), PlaceOrderRequest{AccountID: "acct", IdempotencyKey: "idem"})
	if err != nil {
		t.Fatalf("enabled live PlaceOrder: %v", err)
	}
}

func TestWritePolicyRejectsEnvironmentMismatchAndNotReadyAccount(t *testing.T) {
	mismatch := discoveryTransport(t, GatewayLive, true, true)
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: mismatch},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.PlaceOrder(context.Background(), PlaceOrderRequest{AccountID: "acct", IdempotencyKey: "idem"})
	if !errors.Is(err, ErrGatewayEnvironment) {
		t.Fatalf("mismatch err = %v, want environment error", err)
	}

	notReady := discoveryTransport(t, GatewayDemo, true, false)
	client, err = NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: notReady},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.PlaceOrder(context.Background(), PlaceOrderRequest{AccountID: "acct", IdempotencyKey: "idem"})
	if !errors.Is(err, ErrGatewayAccountNotReady) {
		t.Fatalf("not-ready err = %v, want account not ready", err)
	}
}

func TestWritePolicyRejectsMismatchedAccountProjection(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return jsonFixture(AccountStatus{AccountResource: AccountResource{
				AccountID: "other-acct", Venue: "binance", Product: "perp",
				VenueEnvironment: GatewayDemo, ReadReady: true, WriteReady: true,
			}}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	})
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.PlaceOrder(context.Background(), PlaceOrderRequest{
		AccountID: "acct", IdempotencyKey: "idem",
	})
	if !errors.Is(err, ErrGatewayEnvironment) {
		t.Fatalf("mismatched account projection err = %v, want environment error", err)
	}
}

func TestWritePolicyRequiresOperationCapability(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts/acct":
			return jsonFixture(AccountStatus{AccountResource: AccountResource{
				AccountID: "acct", Venue: "binance", Product: "perp", VenueEnvironment: GatewayDemo,
				ReadReady: true, WriteReady: true,
			}}), nil
		case "/v1/capabilities":
			if r.URL.Query().Get("account_id") != "acct" {
				t.Fatalf("capability query = %q", r.URL.RawQuery)
			}
			return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{{
				AccountID: "acct",
				Capability: CapabilityRow{Operations: CapabilityOperations{
					Place: true, Cancel: false,
				}},
			}}}), nil
		case "/v1/orders":
			return jsonFixtureStatus(http.StatusAccepted, OrderResult{GatewayOrderID: "order-1", State: "accepted"}), nil
		case "/v1/orders/order-1":
			return jsonFixtureStatus(http.StatusAccepted, OrderResult{GatewayOrderID: "order-1", State: "accepted"}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	})
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	if _, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{AccountID: "acct", IdempotencyKey: "idem"}); err != nil {
		t.Fatalf("PlaceOrder with place capability: %v", err)
	}
	_, err = client.CancelOrder(context.Background(), CancelOrderRequest{AccountID: "acct", GatewayOrderID: "order-1"})
	if !errors.Is(err, ErrGatewayOperationBlocked) {
		t.Fatalf("CancelOrder err = %v, want operation blocked", err)
	}
}

func TestCancelOrderRequiresAuthorizedAccountOwnership(t *testing.T) {
	const targetOrderID = "018f0000-0000-7000-8000-000000000009"

	t.Run("finds target across scoped pages before delete", func(t *testing.T) {
		var listCalls int
		var deleteCalled bool
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v1/accounts/demo-acct":
				return jsonFixture(AccountStatus{AccountResource: AccountResource{
					AccountID: "demo-acct", Venue: "binance", Product: "perp",
					VenueEnvironment: GatewayDemo, ReadReady: true, WriteReady: true,
				}}), nil
			case "/v1/capabilities":
				return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{{
					AccountID:  "demo-acct",
					Capability: CapabilityRow{Operations: CapabilityOperations{Cancel: true}},
				}}}), nil
			case "/v1/orders":
				if r.Method != http.MethodGet || r.URL.Query().Get("account_id") != "demo-acct" ||
					r.URL.Query().Get("pagination.limit") != "500" {
					t.Fatalf("ownership query = %s %q", r.Method, r.URL.RawQuery)
				}
				listCalls++
				switch r.URL.Query().Get("pagination.cursor") {
				case "":
					return jsonFixture(GatewayCollection[OrderResult]{
						Items:      []OrderResult{{GatewayOrderID: "018f0000-0000-7000-8000-000000000008"}},
						NextCursor: "page-2",
					}), nil
				case "page-2":
					return jsonFixture(GatewayCollection[OrderResult]{
						Items: []OrderResult{{GatewayOrderID: targetOrderID}},
					}), nil
				default:
					t.Fatalf("unexpected ownership cursor %q", r.URL.Query().Get("pagination.cursor"))
				}
			case "/v1/orders/" + targetOrderID:
				if r.Method != http.MethodDelete {
					t.Fatalf("cancel method = %s", r.Method)
				}
				deleteCalled = true
				return jsonFixtureStatus(http.StatusAccepted, OrderResult{
					GatewayOrderID: targetOrderID, State: "cancel_pending",
				}), nil
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			return nil, nil
		})
		client, err := NewGatewayClient(GatewayClientOptions{
			BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
			HTTPClient: &http.Client{Transport: transport},
		})
		if err != nil {
			t.Fatalf("NewGatewayClient: %v", err)
		}
		result, err := client.CancelOrder(context.Background(), CancelOrderRequest{
			AccountID: "demo-acct", GatewayOrderID: targetOrderID,
		})
		if err != nil || result.GatewayOrderID != targetOrderID {
			t.Fatalf("CancelOrder = %+v, %v", result, err)
		}
		if listCalls != 2 || !deleteCalled {
			t.Fatalf("listCalls=%d deleteCalled=%t", listCalls, deleteCalled)
		}
	})

	t.Run("blocks order absent from authorized account", func(t *testing.T) {
		var deleteCalled bool
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v1/accounts/demo-acct":
				return jsonFixture(AccountStatus{AccountResource: AccountResource{
					AccountID: "demo-acct", Venue: "binance", Product: "perp",
					VenueEnvironment: GatewayDemo, ReadReady: true, WriteReady: true,
				}}), nil
			case "/v1/capabilities":
				return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{{
					AccountID:  "demo-acct",
					Capability: CapabilityRow{Operations: CapabilityOperations{Cancel: true}},
				}}}), nil
			case "/v1/orders":
				if r.URL.Query().Get("account_id") != "demo-acct" {
					t.Fatalf("ownership account query = %q", r.URL.RawQuery)
				}
				return jsonFixture(GatewayCollection[OrderResult]{
					Items: []OrderResult{{GatewayOrderID: "018f0000-0000-7000-8000-000000000008"}},
				}), nil
			case "/v1/orders/" + targetOrderID:
				deleteCalled = true
				return jsonFixtureStatus(http.StatusAccepted, OrderResult{GatewayOrderID: targetOrderID}), nil
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			return nil, nil
		})
		client, err := NewGatewayClient(GatewayClientOptions{
			BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
			HTTPClient: &http.Client{Transport: transport},
		})
		if err != nil {
			t.Fatalf("NewGatewayClient: %v", err)
		}
		_, err = client.CancelOrder(context.Background(), CancelOrderRequest{
			AccountID: "demo-acct", GatewayOrderID: targetOrderID,
		})
		if !errors.Is(err, ErrGatewayOrderOwnership) {
			t.Fatalf("CancelOrder err = %v, want ownership error", err)
		}
		if deleteCalled {
			t.Fatal("cancel DELETE was sent for an order outside the authorized account")
		}
	})
}

func TestDiscoveryUsesModeEligibilityAndRejectsUnreadyTargetAccount(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts":
			return jsonFixture(GatewayCollection[AccountResource]{Items: []AccountResource{
				{
					AccountID: "demo-perp", Venue: "binance", Product: "perp",
					VenueEnvironment: GatewayDemo, ReadReady: true, WriteReady: true,
				},
				{
					AccountID: "testnet-perp", Venue: "lighter", Product: "perp",
					VenueEnvironment: GatewayTestnet, ReadReady: false, WriteReady: false, Reason: "catalog_unavailable",
				},
			}}), nil
		case "/v1/capabilities":
			return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{
				{AccountID: "demo-perp", Capability: CapabilityRow{Operations: CapabilityOperations{Place: true, Cancel: true}}},
				{AccountID: "testnet-perp", Capability: CapabilityRow{Operations: CapabilityOperations{Place: true, Cancel: true}}},
			}}), nil
		case "/v1/accounts/demo-perp":
			return jsonFixture(AccountStatus{AccountResource: AccountResource{
				AccountID: "demo-perp", Venue: "binance", Product: "perp",
				VenueEnvironment: GatewayDemo, ReadReady: true, WriteReady: true,
			}}), nil
		case "/v1/accounts/testnet-perp":
			return jsonFixture(AccountStatus{AccountResource: AccountResource{
				AccountID: "testnet-perp", Venue: "lighter", Product: "perp",
				VenueEnvironment: GatewayTestnet, ReadReady: false, WriteReady: false, Reason: "catalog_unavailable",
			}}), nil
		case "/v1/orders":
			return jsonFixtureStatus(http.StatusAccepted, OrderResult{GatewayOrderID: "018f0000-0000-7000-8000-000000000001", State: "accepted"}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	})
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	discovery, err := client.DiscoverAccounts(context.Background())
	if err != nil {
		t.Fatalf("DiscoverAccounts: %v", err)
	}
	if !discovery.Mixed || discovery.Ready || discovery.Mode != config.Test || len(discovery.Accounts) != 2 ||
		discovery.Accounts[0].AccountID != "demo-perp" ||
		discovery.Accounts[1].AccountID != "testnet-perp" {
		t.Fatalf("discovery = %+v", discovery)
	}
	if _, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{AccountID: "demo-perp", IdempotencyKey: "idem"}); err != nil {
		t.Fatalf("demo PlaceOrder: %v", err)
	}
	_, err = client.PlaceOrder(context.Background(), PlaceOrderRequest{AccountID: "testnet-perp", IdempotencyKey: "idem"})
	if !errors.Is(err, ErrGatewayAccountNotReady) {
		t.Fatalf("testnet target err = %v, want account-not-ready error", err)
	}
}

func TestListRulesDecodesGatewayCollection(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/rules" || r.URL.Query().Get("account_id") != "acct" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		return jsonResponse(http.StatusOK, `{"items":[{"instrument_id":"binance:demo:perp:USDT:BTC-USDT","rules":{"price_increment":"0.1"}}]}`), nil
	})
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	rules, err := client.ListRules(context.Background(), "acct")
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 || rules[0].InstrumentID != "binance:demo:perp:USDT:BTC-USDT" {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestGatewayReadAPIsMatchOpenAPIContract(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/instruments":
			if r.URL.Query().Get("account_id") != "acct" || r.URL.Query().Get("pagination.limit") != "500" {
				t.Fatalf("instrument query = %q", r.URL.RawQuery)
			}
			return jsonResponse(http.StatusOK, `{"items":[{"instrument_id":"binance:demo:perp:USDT:BTC-USDT","venue":"binance","environment":"demo","product":"perp","settlement":"USDT","symbol":"BTC-USDT","base_asset":"BTC","quote_asset":"USDT","rules":{}}]}`), nil
		case "/v1/instruments/binance:demo:perp:USDT:BTC-USDT":
			return jsonResponse(http.StatusOK, `{"instrument_id":"binance:demo:perp:USDT:BTC-USDT","venue":"binance","environment":"demo","product":"perp","settlement":"USDT","symbol":"BTC-USDT","base_asset":"BTC","quote_asset":"USDT","rules":{}}`), nil
		case "/v1/orders":
			if r.URL.Query().Get("account_id") != "acct" || r.URL.Query().Get("instrument_id") != "inst" ||
				r.URL.Query().Get("pagination.cursor") != "cursor-1" || r.URL.Query().Get("pagination.limit") != "25" {
				t.Fatalf("orders query = %q", r.URL.RawQuery)
			}
			return jsonResponse(http.StatusOK, `{"items":[{"gateway_order_id":"order-1","state":"open"}],"next_cursor":"cursor-2"}`), nil
		case "/v1/open-orders":
			if r.URL.Query().Get("pagination.cursor") != "cursor-open" || r.URL.Query().Get("pagination.limit") != "10" {
				t.Fatalf("open-orders query = %q", r.URL.RawQuery)
			}
			return jsonResponse(http.StatusOK, `{"items":[{"gateway_order_id":"order-2","state":"open"}],"next_cursor":"cursor-open-2"}`), nil
		case "/v1/fills":
			if r.URL.Query().Get("gateway_order_id") != "gw-order-1" ||
				r.URL.Query().Get("pagination.cursor") != "cursor-fill" || r.URL.Query().Get("pagination.limit") != "5" {
				t.Fatalf("fills query = %q", r.URL.RawQuery)
			}
			return jsonResponse(http.StatusOK, `{"items":[{"account_id":"acct","instrument_id":"inst","gateway_order_id":"gw-order-1","fill":{"fill_id":"fill-1"}}],"next_cursor":"cursor-fill-2"}`), nil
		case "/v1/orders/order-lookup":
			return jsonResponse(http.StatusOK, `{"gateway_order_id":"order-lookup","state":"open"}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	})
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	instruments, err := client.ListInstruments(context.Background(), "acct")
	if err != nil || len(instruments) != 1 {
		t.Fatalf("ListInstruments = %+v, %v", instruments, err)
	}
	instrument, err := client.GetInstrument(context.Background(), "binance:demo:perp:USDT:BTC-USDT")
	if err != nil || instrument.InstrumentID == "" {
		t.Fatalf("GetInstrument = %+v, %v", instrument, err)
	}
	orders, err := client.ListOrdersPage(context.Background(), GatewayPageRequest{
		AccountID: "acct", InstrumentID: "inst", Cursor: "cursor-1", Limit: 25,
	})
	if err != nil || len(orders.Items) != 1 || orders.NextCursor != "cursor-2" {
		t.Fatalf("ListOrdersPage = %+v, %v", orders, err)
	}
	open, err := client.OpenOrdersPage(context.Background(), GatewayPageRequest{Cursor: "cursor-open", Limit: 10})
	if err != nil || len(open.Items) != 1 || open.NextCursor != "cursor-open-2" {
		t.Fatalf("OpenOrdersPage = %+v, %v", open, err)
	}
	fills, err := client.FillsPage(context.Background(), GatewayPageRequest{Cursor: "cursor-fill", Limit: 5, GatewayOrderID: "gw-order-1"})
	if err != nil || len(fills.Items) != 1 || fills.NextCursor != "cursor-fill-2" {
		t.Fatalf("FillsPage = %+v, %v", fills, err)
	}
	if fills.Items[0].GatewayOrderID != "gw-order-1" {
		t.Fatalf("fill gateway_order_id = %q, want gw-order-1", fills.Items[0].GatewayOrderID)
	}
	order, err := client.GetOrder(context.Background(), "order-lookup")
	if err != nil || order.GatewayOrderID != "order-lookup" {
		t.Fatalf("GetOrder = %+v, %v", order, err)
	}
}

func TestGatewayPageRequestsRejectOutOfRangeLimitsWithoutHTTP(t *testing.T) {
	var called bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return jsonResponse(http.StatusOK, `{"items":[]}`), nil
	})
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	for _, limit := range []int{-1, gatewayMaxPageLimit + 1} {
		_, err := client.ListOrdersPage(context.Background(), GatewayPageRequest{Limit: limit})
		if !errors.Is(err, ErrGatewayPageLimit) {
			t.Fatalf("limit %d err = %v, want page limit error", limit, err)
		}
	}
	if called {
		t.Fatal("out-of-range page limit reached Gateway")
	}
}

func TestGatewayRemainingReadAPIsUseBoundedFullPage(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts":
			if r.URL.Query().Get("pagination.limit") != "500" {
				t.Fatalf("accounts query = %q", r.URL.RawQuery)
			}
			return jsonResponse(http.StatusOK, `{"items":[{"account_id":"acct","venue":"binance","product":"perp","environment":"demo","read_ready":true,"write_ready":true}]}`), nil
		case "/v1/risk-limits":
			if r.URL.Query().Get("account_id") != "acct" {
				t.Fatalf("risk-limits query = %q", r.URL.RawQuery)
			}
			return jsonResponse(http.StatusOK, `{"available":false,"reason":"risk_limits_not_normalized","limits":[]}`), nil
		case "/v1/balances":
			if r.URL.Query().Get("account_id") != "acct" || r.URL.Query().Get("pagination.limit") != "500" {
				t.Fatalf("balances query = %q", r.URL.RawQuery)
			}
			return jsonResponse(http.StatusOK, `{"items":[{"account_id":"acct","balance":{"asset":"USDT","total":"100"}}]}`), nil
		case "/v1/positions":
			if r.URL.Query().Get("account_id") != "acct" ||
				r.URL.Query().Get("instrument_id") != "inst" ||
				r.URL.Query().Get("pagination.limit") != "500" {
				t.Fatalf("positions query = %q", r.URL.RawQuery)
			}
			return jsonResponse(http.StatusOK, `{"items":[{"account_id":"acct","instrument_id":"inst","position":{"quantity":"0.01"}}]}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	})
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	accounts, err := client.ListAccounts(context.Background())
	if err != nil || len(accounts) != 1 || accounts[0].AccountID != "acct" {
		t.Fatalf("ListAccounts = %+v, %v", accounts, err)
	}
	riskLimits, err := client.RiskLimits(context.Background(), "acct")
	if err != nil || riskLimits.Available || riskLimits.Reason != "risk_limits_not_normalized" {
		t.Fatalf("RiskLimits = %+v, %v", riskLimits, err)
	}
	balances, err := client.Balances(context.Background(), "acct")
	if err != nil || len(balances) != 1 || balances[0].AccountID != "acct" {
		t.Fatalf("Balances = %+v, %v", balances, err)
	}
	positions, err := client.Positions(context.Background(), "acct", "inst")
	if err != nil || len(positions) != 1 || positions[0].InstrumentID != "inst" {
		t.Fatalf("Positions = %+v, %v", positions, err)
	}
}

func TestGatewayNestedResponseContractsRejectUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		call func(*GatewayClient) error
	}{
		{
			name: "rules",
			body: `{"items":[{"instrument_id":"inst","rules":{"unexpected":true}}]}`,
			call: func(client *GatewayClient) error {
				_, err := client.ListRules(context.Background(), "acct")
				return err
			},
		},
		{
			name: "risk limits",
			body: `{"available":true,"reason":"","limits":[{"unexpected":true}]}`,
			call: func(client *GatewayClient) error {
				_, err := client.RiskLimits(context.Background(), "acct")
				return err
			},
		},
		{
			name: "fill",
			body: `{"items":[{"account_id":"acct","fill":{"unexpected":true}}]}`,
			call: func(client *GatewayClient) error {
				_, err := client.FillsPage(context.Background(), GatewayPageRequest{AccountID: "acct"})
				return err
			},
		},
		{
			name: "balance",
			body: `{"items":[{"account_id":"acct","balance":{"unexpected":true}}]}`,
			call: func(client *GatewayClient) error {
				_, err := client.Balances(context.Background(), "acct")
				return err
			},
		},
		{
			name: "position",
			body: `{"items":[{"account_id":"acct","position":{"unexpected":true}}]}`,
			call: func(client *GatewayClient) error {
				_, err := client.Positions(context.Background(), "acct", "")
				return err
			},
		},
		{
			name: "funding rate",
			body: `{"account_id":"acct","instrument_id":"inst","funding_rate":{"unexpected":true}}`,
			call: func(client *GatewayClient) error {
				_, err := client.FundingRates(context.Background(), "acct", "inst")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, test.body), nil
			})
			client, err := NewGatewayClient(GatewayClientOptions{
				BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
				HTTPClient: &http.Client{Transport: transport},
			})
			if err != nil {
				t.Fatalf("NewGatewayClient: %v", err)
			}
			err = test.call(client)
			if err == nil || !strings.Contains(err.Error(), "decode gateway response failed") {
				t.Fatalf("nested unknown field err = %v", err)
			}
		})
	}
}

func TestGatewayErrorIncludesSafeCorrelationAndIdempotencyConflictOnly(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusConflict, `{"error":{"code":"idempotency_conflict\nleak","message":"do not show this","correlation_id":"018f0000-0000-7000-8000-000000000002","retryable":false},"existing_command_id":"018f0000-0000-7000-8000-000000000003"}`), nil
	})
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	_, err = client.Live(context.Background())
	if err == nil {
		t.Fatal("expected gateway error")
	}
	var gatewayErr *GatewayHTTPError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("err type = %T", err)
	}
	got := err.Error()
	if !strings.Contains(got, "code=gateway_error") ||
		!strings.Contains(got, "correlation_id=018f0000-0000-7000-8000-000000000002") ||
		!strings.Contains(got, "existing_command_id=018f0000-0000-7000-8000-000000000003") ||
		strings.Contains(got, "do not show this") || strings.Contains(got, "idempotency_conflict") {
		t.Fatalf("unsafe gateway error text: %s", got)
	}
	if gatewayErr.ExistingCommandID != "018f0000-0000-7000-8000-000000000003" {
		t.Fatalf("existing command id = %q", gatewayErr.ExistingCommandID)
	}
}

func TestGatewayClientAppliesDefaultTimeoutToCustomClientWithZeroTimeout(t *testing.T) {
	custom := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"live":true}`), nil
	})}
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: custom,
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	if client.httpClient == custom {
		t.Fatal("client reused mutable custom http.Client instead of cloning to apply timeout")
	}
	if client.httpClient.Timeout != defaultGatewayTimeout {
		t.Fatalf("timeout = %s, want %s", client.httpClient.Timeout, defaultGatewayTimeout)
	}
	if custom.Timeout != 0 {
		t.Fatalf("custom client was mutated: timeout=%s", custom.Timeout)
	}
}

func TestGatewayResponseContractRejectsNonJSONAndOversizedBodies(t *testing.T) {
	t.Run("content-type", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return textResponse(http.StatusOK, "ok"), nil
		})
		client, err := NewGatewayClient(GatewayClientOptions{
			BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
			HTTPClient: &http.Client{Transport: transport},
		})
		if err != nil {
			t.Fatalf("NewGatewayClient: %v", err)
		}
		_, err = client.Live(context.Background())
		if err == nil || !strings.Contains(err.Error(), "invalid_content_type") {
			t.Fatalf("err = %v, want invalid content type", err)
		}
	})
	t.Run("body-limit", func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"live":true}`), nil
		})
		client, err := NewGatewayClient(GatewayClientOptions{
			BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
			BodyLimit: 4, HTTPClient: &http.Client{Transport: transport},
		})
		if err != nil {
			t.Fatalf("NewGatewayClient: %v", err)
		}
		_, err = client.Live(context.Background())
		if err == nil || !strings.Contains(err.Error(), "response_too_large") {
			t.Fatalf("err = %v, want body limit", err)
		}
	})
}

func discoveryTransport(t *testing.T, env VenueEnvironment, readReady, writeReady bool) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts":
			return jsonFixture(GatewayCollection[AccountResource]{Items: []AccountResource{{
				AccountID: "acct", Venue: "binance", Product: "perp", VenueEnvironment: env,
				ReadReady: readReady, WriteReady: writeReady, Reason: "catalog_unavailable",
			}}}), nil
		case "/v1/accounts/acct":
			return jsonFixture(AccountStatus{AccountResource: AccountResource{
				AccountID: "acct", Venue: "binance", Product: "perp", VenueEnvironment: env,
				ReadReady: readReady, WriteReady: writeReady, Reason: "catalog_unavailable",
			}}), nil
		case "/v1/capabilities":
			return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{{
				AccountID: "acct",
				Capability: CapabilityRow{Operations: CapabilityOperations{
					Place: true, Cancel: true,
				}},
			}}}), nil
		case "/v1/orders":
			return jsonFixtureStatus(http.StatusAccepted, OrderResult{GatewayOrderID: "018f0000-0000-7000-8000-000000000001", State: "accepted"}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func jsonFixture(value any) *http.Response {
	return jsonFixtureStatus(http.StatusOK, value)
}

func jsonFixtureStatus(status int, value any) *http.Response {
	var body bytes.Buffer
	_ = json.NewEncoder(&body).Encode(value)
	return jsonResponse(status, body.String())
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func textResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

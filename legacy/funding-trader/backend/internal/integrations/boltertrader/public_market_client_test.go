package boltertrader

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/shopspring/decimal"
)

func TestPublicMarketClientRejectsRemotePlaintextLiveTransport(t *testing.T) {
	for _, rawURL := range []string{
		"http://gateway.example",
		"http://192.0.2.10:18082",
		"http://0.0.0.0:18082",
	} {
		t.Run("reject "+rawURL, func(t *testing.T) {
			if _, err := NewPublicMarketClient(PublicMarketClientOptions{
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
			if _, err := NewPublicMarketClient(PublicMarketClientOptions{
				BaseURL: rawURL, Token: "token", Mode: config.Live,
			}); err != nil {
				t.Fatalf("live Gateway URL %q rejected: %v", rawURL, err)
			}
		})
	}
}

func TestPublicMarketClientReadsEveryNormalizedRESTResourceWithExactScope(t *testing.T) {
	instrument := publicClientInstrument()
	observedAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	server := newHTTPFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer public-token" {
			t.Fatalf("authorization header=%q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("account_id") != instrument.AccountID ||
			r.URL.Query().Get("instrument_id") != instrument.InstrumentID {
			t.Fatalf("scope query=%s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/funding-rates":
			writePublicFixture(t, w, map[string]any{
				"account_id": instrument.AccountID, "instrument_id": instrument.InstrumentID,
				"funding_rate": fundingFixture(observedAt, "0.0001"),
			})
		case "/v1/funding-rates/history":
			if r.URL.Query().Get("pagination.cursor") != "opaque" ||
				r.URL.Query().Get("pagination.limit") != "7" {
				t.Fatalf("history query=%s", r.URL.RawQuery)
			}
			historical := fundingFixture(observedAt, "-0.0002")
			delete(historical, "observed_at")
			writePublicFixture(t, w, map[string]any{
				"account_id": instrument.AccountID, "instrument_id": instrument.InstrumentID,
				"items": []any{historical}, "next_cursor": "next",
			})
		case "/v1/order-book":
			if r.URL.Query().Get("limit") != "25" {
				t.Fatalf("book query=%s", r.URL.RawQuery)
			}
			writePublicFixture(t, w, orderBookFixture(instrument, observedAt))
		case "/v1/bbo":
			writePublicFixture(t, w, bboFixture(instrument, observedAt))
		case "/v1/mark-price":
			writePublicFixture(t, w, map[string]any{
				"account_id": instrument.AccountID, "instrument_id": instrument.InstrumentID,
				"price": "100.75", "observed_at": observedAt,
			})
		default:
			t.Fatalf("unexpected public path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := mustPublicMarketClient(t, server.URL, server.Client())
	ctx := context.Background()

	rate, err := client.CurrentFunding(ctx, instrument)
	if err != nil || !rate.Rate.Equal(decimal.RequireFromString("0.0001")) ||
		!rate.MarkPrice.Valid || rate.Instrument.Key() != instrument.Key() {
		t.Fatalf("funding=%+v err=%v", rate, err)
	}
	history, err := client.FundingHistory(ctx, marketdata.FundingRateHistoryRequest{
		Instrument: instrument, Cursor: "opaque", Limit: 7,
		Start: observedAt.Add(-24 * time.Hour), End: observedAt,
	})
	if err != nil || len(history.Rates) != 1 || history.NextCursor != "next" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	book, err := client.OrderBook(ctx, marketdata.OrderBookRequest{Instrument: instrument, Limit: 25})
	if err != nil || len(book.Bids) != 1 || len(book.Asks) != 1 || book.Page.Limit != 25 {
		t.Fatalf("book=%+v err=%v", book, err)
	}
	bbo, err := client.BBO(ctx, instrument)
	if err != nil || !bbo.Bid.Price.Equal(decimal.NewFromInt(100)) ||
		!bbo.Ask.Price.Equal(decimal.NewFromInt(101)) {
		t.Fatalf("bbo=%+v err=%v", bbo, err)
	}
	mark, err := client.MarkPrice(ctx, instrument)
	if err != nil || !mark.Price.Equal(decimal.RequireFromString("100.75")) {
		t.Fatalf("mark=%+v err=%v", mark, err)
	}
}

func TestPublicMarketClientRejectsInvalidRequestsAndMislabeledOrUnknownResponses(t *testing.T) {
	instrument := publicClientInstrument()
	calls := 0
	server := newHTTPFixtureServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		writePublicFixture(t, w, map[string]any{
			"account_id": "other-account", "instrument_id": instrument.InstrumentID,
			"price": "100", "observed_at": time.Now().UTC(), "unexpected": true,
		})
	}))
	defer server.Close()
	client := mustPublicMarketClient(t, server.URL, server.Client())

	if _, err := client.OrderBook(context.Background(), marketdata.OrderBookRequest{
		Instrument: instrument, Limit: marketdata.MaxPageLimit + 1,
	}); !errors.Is(err, marketdata.ErrInvalidMarketData) {
		t.Fatalf("invalid limit error=%v", err)
	}
	if calls != 0 {
		t.Fatalf("invalid request reached server, calls=%d", calls)
	}
	if _, err := client.MarkPrice(context.Background(), instrument); err == nil ||
		!strings.Contains(err.Error(), "decode public market response failed") {
		t.Fatalf("unknown-field error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}

	var destination any
	if err := client.getJSON(context.Background(), "/v1/orders", &destination); err == nil {
		t.Fatal("private endpoint escaped public allowlist")
	}
	if calls != 1 {
		t.Fatalf("private endpoint reached server, calls=%d", calls)
	}
}

func TestPublicMarketClientWebSocketPreservesEnvelopeAndRequiresResnapshotOnGatewaySignal(t *testing.T) {
	instrument := publicClientInstrument()
	observedAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	serverErrors := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer public-token" ||
			r.URL.Query().Get("account_id") != instrument.AccountID ||
			r.URL.Query().Get("instrument_id") != instrument.InstrumentID ||
			r.URL.Query().Get("kind") != "funding_rate" {
			serverErrors <- errors.New("websocket request scope or auth mismatch")
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer connection.CloseNow()
		ctx := r.Context()
		events := []map[string]any{
			publicEnvelopeFixture(instrument, 1, "019fae38-252b-75e3-a7e6-13860359dc01", "snapshot", observedAt, fundingFixture(observedAt, "0.0001")),
			publicEnvelopeFixture(instrument, 2, "019fae38-252b-75e3-a7e6-13860359dc02", "update", observedAt.Add(time.Second), map[string]any{
				"rate": "0.0002", "observed_at": observedAt.Add(time.Second),
				"funding_time": observedAt.Add(8 * time.Hour), "next_funding_time": observedAt.Add(16 * time.Hour),
			}),
			publicEnvelopeFixture(instrument, 3, "019fae38-252b-75e3-a7e6-13860359dc03", "resync_required", observedAt.Add(2*time.Second), map[string]any{
				"reason": "stream-gap", "resources": []string{"funding_rate"},
			}),
		}
		for _, event := range events {
			if err := wsjson.Write(ctx, connection, event); err != nil {
				serverErrors <- err
				return
			}
		}
		_ = connection.Close(websocket.StatusPolicyViolation, "public_stream_resnapshot_required")
	})
	server := newWebSocketFixtureServer(t, handler)
	defer server.Close()
	client := mustPublicMarketClient(t, server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subscription, err := client.WatchFundingRate(ctx, instrument)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	snapshot, err := subscription.Next(ctx)
	if err != nil || snapshot.Type != marketdata.StreamSnapshot || snapshot.Sequence != 1 ||
		snapshot.BootID != "019fae38-252b-75e3-a7e6-13860359dc0e" || !snapshot.Data.Rate.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	update, err := subscription.Next(ctx)
	if err != nil || update.Type != marketdata.StreamUpdate || update.Sequence != 2 ||
		!update.Data.Rate.Equal(decimal.RequireFromString("0.0002")) {
		t.Fatalf("update=%+v err=%v", update, err)
	}
	if _, err := subscription.Next(ctx); !errors.Is(err, marketdata.ErrResnapshotRequired) ||
		!strings.Contains(err.Error(), "stream-gap") {
		t.Fatalf("resnapshot error=%v", err)
	}
	if _, err := subscription.Next(ctx); !errors.Is(err, marketdata.ErrResnapshotRequired) {
		t.Fatalf("sticky resnapshot error=%v", err)
	}
	select {
	case err := <-serverErrors:
		t.Fatal(err)
	default:
	}
}

func TestPublicMarketClientWebSocketRejectsSequenceGap(t *testing.T) {
	instrument := publicClientInstrument()
	observedAt := time.Now().UTC()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		_ = wsjson.Write(r.Context(), connection,
			publicEnvelopeFixture(instrument, 1, "019fae38-252b-75e3-a7e6-13860359dc01", "snapshot", observedAt, fundingFixture(observedAt, "0")))
		_ = wsjson.Write(r.Context(), connection,
			publicEnvelopeFixture(instrument, 3, "019fae38-252b-75e3-a7e6-13860359dc03", "update", observedAt.Add(time.Second), map[string]any{
				"rate": "0", "observed_at": observedAt.Add(time.Second),
				"funding_time": observedAt, "next_funding_time": observedAt.Add(8 * time.Hour),
			}))
		_ = connection.Close(websocket.StatusPolicyViolation, "fixture_complete")
	})
	server := newWebSocketFixtureServer(t, handler)
	defer server.Close()
	client := mustPublicMarketClient(t, server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subscription, err := client.WatchFundingRate(ctx, instrument)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if _, err := subscription.Next(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := subscription.Next(ctx); !errors.Is(err, marketdata.ErrResnapshotRequired) ||
		!strings.Contains(err.Error(), "sequence-gap") {
		t.Fatalf("sequence gap error=%v", err)
	}
}

func publicClientInstrument() marketdata.Instrument {
	return marketdata.Instrument{
		AccountID:    "binance-demo-usdt-perp",
		InstrumentID: "binance:demo:perp:USDT:BTC-USDT",
		Venue:        "binance", VenueEnvironment: "demo", Product: "perp",
		Settlement: "USDT", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
	}
}

func fundingFixture(observedAt time.Time, rate string) map[string]any {
	return map[string]any{
		"rate":              rate,
		"mark_price":        map[string]any{"value": "100.5", "valid": true},
		"observed_at":       observedAt,
		"funding_time":      observedAt.Add(8 * time.Hour),
		"next_funding_time": observedAt.Add(16 * time.Hour),
	}
}

func orderBookFixture(instrument marketdata.Instrument, observedAt time.Time) map[string]any {
	return map[string]any{
		"account_id": instrument.AccountID, "instrument_id": instrument.InstrumentID,
		"bids":        []any{map[string]any{"price": "100", "quantity": "2"}},
		"asks":        []any{map[string]any{"price": "101", "quantity": "3"}},
		"observed_at": observedAt, "sequence": "10",
		"page": map[string]any{
			"limit": 25, "has_more_known": true, "has_more": false,
		},
	}
}

func bboFixture(instrument marketdata.Instrument, observedAt time.Time) map[string]any {
	return map[string]any{
		"account_id": instrument.AccountID, "instrument_id": instrument.InstrumentID,
		"bid":         map[string]any{"price": "100", "quantity": "2"},
		"ask":         map[string]any{"price": "101", "quantity": "3"},
		"observed_at": observedAt,
	}
}

func publicEnvelopeFixture(
	instrument marketdata.Instrument,
	sequence uint64,
	eventID string,
	eventType string,
	occurredAt time.Time,
	payload any,
) map[string]any {
	return map[string]any{
		"boot_id": "019fae38-252b-75e3-a7e6-13860359dc0e", "sequence": sequence, "event_id": eventID,
		"account_id": instrument.AccountID, "instrument_id": instrument.InstrumentID,
		"kind": "funding_rate", "event_type": eventType,
		"occurred_at": occurredAt, "payload": payload,
	}
}

func mustPublicMarketClient(t *testing.T, baseURL string, httpClient *http.Client) *PublicMarketClient {
	t.Helper()
	client, err := NewPublicMarketClient(PublicMarketClientOptions{
		BaseURL: baseURL, Token: "public-token", HTTPClient: httpClient,
		Mode: config.Test,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writePublicFixture(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func newHTTPFixtureServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("loopback socket binding is not permitted in this environment", err)
			return nil
		}
		t.Fatal(err)
		return nil
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}

func newWebSocketFixtureServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("loopback socket binding is not permitted in this environment", err)
			return nil
		}
		t.Fatal(err)
		return nil
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}

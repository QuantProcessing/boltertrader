package nado

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoAmbiguousPreparedSubmitRetainsExactDigestCorrelation(t *testing.T) {
	const signedDigest = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC))
	var (
		mu             sync.Mutex
		queriedDigests []string
		sender         string
		reportedDigest = signedDigest
		baseFilled     = "2000000000000000000"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode exact order query: %v", err)
		}
		switch r.URL.Path {
		case "/v1/query":
			if req["type"] != "subaccount_orders" || req["product_id"] != float64(1) || req["sender"] != sender {
				t.Fatalf("unexpected open-order query: %#v", req)
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"sender":"` + sender + `","product_id":1,"orders":[]}}`))
		case "/v1":
			orders, ok := req["orders"].(map[string]any)
			if !ok {
				t.Fatalf("unexpected archive query: %#v", req)
			}
			digests, ok := orders["digests"].([]any)
			if !ok || len(digests) != 1 {
				t.Fatalf("archive digests: %#v", orders["digests"])
			}
			digest, _ := digests[0].(string)
			mu.Lock()
			queriedDigests = append(queriedDigests, digest)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"orders":[{"digest":"` + reportedDigest + `","subaccount":"` + sender + `","product_id":1,"submission_idx":"42","last_fill_submission_idx":"42","amount":"2000000000000000000","price_x18":"1000000000000000000","base_filled":"` + baseFilled + `","quote_filled":"-` + baseFilled + `","fee":"1000000000000000","first_fill_timestamp":"1783908000","last_fill_timestamp":"1783908001","expiration":"4000000000","appendix":"1"}]}`))
		default:
			t.Fatalf("unexpected exact order path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	rest := nadoTestRESTClient(t, server)
	var err error
	rest, err = rest.WithCredentials(strings.Repeat("1", 64), "arb")
	if err != nil {
		t.Fatal(err)
	}
	sender, err = rest.Sender()
	if err != nil {
		t.Fatal(err)
	}
	exec := newExecutionClient(rest, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	deps := &recordingPreTradeDeps{
		sender:     sender,
		maxSizeX18: "5000000000000000000",
		prepared:   preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", signedDigest),
		executeErr: context.DeadlineExceeded,
	}
	exec.pretrade = deps
	exec.reports = &recordingReportDeps{
		sender: sender,
		matches: &sdk.ArchiveMatchesResponse{
			Matches: []sdk.Match{
				{
					Digest: signedDigest, BaseFilled: "2000000000000000000", Fee: "1000000000000000", SubmissionIdx: "42", Timestamp: "2026-07-13T02:00:01Z",
					Order: sdk.MatchOrder{PriceX18: "1000000000000000000", Amount: "2000000000000000000"},
				},
				{
					Digest: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", BaseFilled: "9000000000000000000", Fee: "0", SubmissionIdx: "43", Timestamp: "2026-07-13T02:00:02Z",
					Order: sdk.MatchOrder{PriceX18: "1000000000000000000", Amount: "9000000000000000000"},
				},
			},
			Txs: []sdk.Tx{
				{SubmissionIdx: "42", TxInfo: sdk.TxInfo{MatchOrders: sdk.MatchOrders{ProductId: 1}}},
				{SubmissionIdx: "43", TxInfo: sdk.TxInfo{MatchOrders: sdk.MatchOrders{ProductId: 1}}},
			},
		},
	}

	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	req.ClientID = " client-pretrade-1 "
	req.TIF = enums.TifIOC
	lease, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID))
	if err != nil {
		t.Fatalf("ValidatePreTrade: %v", err)
	}
	defer lease.Release()
	if order, submitErr := exec.SubmitPrepared(context.Background(), req); order != nil || !errors.Is(submitErr, context.DeadlineExceeded) {
		t.Fatalf("SubmitPrepared order=%+v err=%v, want nil ambiguous timeout", order, submitErr)
	}
	if deps.prepared.Digest != "" || deps.prepared.Signature != "" || deps.prepared.EncodedOrder != "" || deps.prepared.Request != nil {
		t.Fatalf("prepared secrets were not redacted after ambiguous result: %+v", deps.prepared)
	}

	// Active correlations are the only source of Nado client-ID identity and must
	// outlive the terminal-report retention window until a terminal state is
	// authoritative.
	clk.Advance(nadoOrderCorrelationRetention + time.Minute)
	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID: AccountIDUnified, InstrumentID: req.InstrumentID, ClientID: req.ClientID,
	})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport by client id: %v", err)
	}
	if report == nil || report.Order.VenueOrderID != signedDigest || report.Order.Request.ClientID != req.ClientID || report.Order.Status != enums.StatusFilled {
		t.Fatalf("exact recovered report mismatch: %+v", report)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{
		AccountID: AccountIDUnified, InstrumentID: req.InstrumentID, ClientID: req.ClientID,
	})
	if err != nil {
		t.Fatalf("GenerateFillReports by client id: %v", err)
	}
	if len(fills) != 1 || fills[0].Fill.VenueOrderID != signedDigest || fills[0].Fill.ClientID != req.ClientID {
		t.Fatalf("exact recovered fills mismatch: %+v", fills)
	}
	baseFilled = "1000000000000000000"
	report, err = exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID: AccountIDUnified, InstrumentID: req.InstrumentID, ClientID: req.ClientID,
	})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport partial IOC by client id: %v", err)
	}
	if report == nil || report.Order.Status != enums.StatusCanceled || report.Order.FilledQty.String() != "1" {
		t.Fatalf("partial historical IOC report mismatch: %+v", report)
	}
	reportedDigest = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID: AccountIDUnified, InstrumentID: req.InstrumentID, ClientID: req.ClientID,
	}); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("foreign exact-order identity err=%v, want digest mismatch", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queriedDigests) != 3 || queriedDigests[0] != signedDigest || queriedDigests[1] != signedDigest || queriedDigests[2] != signedDigest {
		t.Fatalf("exact queries used digests %v, want signed digest only", queriedDigests)
	}
}

func TestNadoRejectsForeignResponseDigestAndRecoversSignedDigest(t *testing.T) {
	const (
		signedDigest  = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		foreignDigest = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC))
	var queriedDigest, sender string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode exact order query: %v", err)
		}
		switch r.URL.Path {
		case "/v1/query":
			_, _ = w.Write([]byte(`{"status":"success","data":{"sender":"` + sender + `","product_id":1,"orders":[]}}`))
		case "/v1":
			orders, _ := req["orders"].(map[string]any)
			digests, _ := orders["digests"].([]any)
			if len(digests) != 1 {
				t.Fatalf("archive digests: %#v", orders["digests"])
			}
			queriedDigest, _ = digests[0].(string)
			_, _ = w.Write([]byte(`{"orders":[{"digest":"` + signedDigest + `","subaccount":"` + sender + `","product_id":1,"submission_idx":"42","last_fill_submission_idx":"42","amount":"2000000000000000000","price_x18":"1000000000000000000","base_filled":"2000000000000000000","quote_filled":"-2000000000000000000","fee":"0","first_fill_timestamp":"1783911600","last_fill_timestamp":"1783911601","expiration":"4000000000","appendix":"1"}]}`))
		default:
			t.Fatalf("unexpected exact order path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	rest := nadoTestRESTClient(t, server)
	var err error
	rest, err = rest.WithCredentials(strings.Repeat("1", 64), "arb")
	if err != nil {
		t.Fatal(err)
	}
	sender, err = rest.Sender()
	if err != nil {
		t.Fatal(err)
	}
	exec := newExecutionClient(rest, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	deps := &recordingPreTradeDeps{
		sender:     sender,
		maxSizeX18: "5000000000000000000",
		prepared:   preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", signedDigest),
		executed:   &sdk.PlaceOrderResponse{Digest: foreignDigest},
	}
	exec.pretrade = deps
	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	req.TIF = enums.TifIOC
	lease, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID))
	if err != nil {
		t.Fatalf("ValidatePreTrade: %v", err)
	}
	defer lease.Release()
	order, submitErr := exec.SubmitPrepared(context.Background(), req)
	if order != nil || submitErr == nil || !strings.Contains(submitErr.Error(), "digest mismatch") {
		t.Fatalf("foreign digest result order=%+v err=%v", order, submitErr)
	}
	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID: AccountIDUnified, InstrumentID: req.InstrumentID, ClientID: req.ClientID,
	})
	if err != nil {
		t.Fatalf("recover signed digest: %v", err)
	}
	if report == nil || report.Order.VenueOrderID != signedDigest {
		t.Fatalf("signed digest was not recovered: %+v", report)
	}
	if queriedDigest != signedDigest || queriedDigest == foreignDigest {
		t.Fatalf("status query used digest %q, want signed digest %q", queriedDigest, signedDigest)
	}
}

func TestNadoOrderCorrelationRejectsDigestCollisionAndPreservesExactClientID(t *testing.T) {
	now := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
	id := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy).InstrumentID
	cache := newNadoOrderCorrelationCache(2, time.Minute)
	first := nadoOrderCorrelation{
		accountID: AccountIDUnified, instrumentID: id, clientID: " client-one ", venueOrderID: "0xabc",
	}
	if err := cache.remember(first, now); err != nil {
		t.Fatalf("remember first: %v", err)
	}
	if got, ok := cache.byClientID(AccountIDUnified, id, first.clientID, now); !ok || got.clientID != first.clientID {
		t.Fatalf("exact client id was normalized or lost: %+v ok=%v", got, ok)
	}
	if err := cache.remember(nadoOrderCorrelation{
		accountID: AccountIDUnified, instrumentID: id, clientID: "client-two", venueOrderID: first.venueOrderID,
	}, now); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("digest collision err=%v, want fail closed", err)
	}
	if got, ok := cache.byVenueOrderID(AccountIDUnified, id, first.venueOrderID, now); !ok || got.clientID != first.clientID {
		t.Fatalf("digest collision replaced reverse mapping: %+v ok=%v", got, ok)
	}
	if err := cache.remember(nadoOrderCorrelation{
		accountID: AccountIDUnified, instrumentID: id, clientID: "client-two", venueOrderID: "0xdef",
	}, now); err != nil {
		t.Fatalf("remember second: %v", err)
	}
	if err := cache.remember(nadoOrderCorrelation{
		accountID: AccountIDUnified, instrumentID: id, clientID: "client-three", venueOrderID: "0x123",
	}, now); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("capacity err=%v, want fail closed", err)
	}
	if got, ok := cache.byClientID(AccountIDUnified, id, first.clientID, now.Add(2*time.Minute)); !ok || got.clientID != first.clientID {
		t.Fatalf("active client correlation expired before a terminal state: %+v ok=%v", got, ok)
	}
	cache.markTerminalByVenueOrderID(AccountIDUnified, id, first.venueOrderID, enums.StatusCanceled, now.Add(2*time.Minute))
	if got, ok := cache.byVenueOrderID(AccountIDUnified, id, first.venueOrderID, now.Add(2*time.Minute)); !ok || got.terminalStatus != enums.StatusCanceled {
		t.Fatalf("terminal evidence was not retained: %+v ok=%v", got, ok)
	}
	if _, ok := cache.byClientID(AccountIDUnified, id, first.clientID, now.Add(2*time.Minute+time.Minute)); ok {
		t.Fatal("terminal client correlation remained visible after retention")
	}
	if _, ok := cache.byVenueOrderID(AccountIDUnified, id, first.venueOrderID, now.Add(2*time.Minute+time.Minute)); ok {
		t.Fatal("terminal reverse correlation remained visible after retention")
	}
}

func TestNadoArchivePartialRequiresAuthoritativeTerminalEvidence(t *testing.T) {
	qty := decimal.RequireFromString("2")
	partial := decimal.RequireFromString("1")
	if got := nadoArchiveOrderStatus(partial, qty, enums.TifGTC, enums.StatusUnknown); got != enums.StatusUnknown {
		t.Fatalf("unconfirmed partial GTC status=%s, want Unknown", got)
	}
	if got := nadoArchiveOrderStatus(partial, qty, enums.TifGTC, enums.StatusCanceled); got != enums.StatusCanceled {
		t.Fatalf("confirmed canceled partial GTC status=%s, want Canceled", got)
	}
	if got := nadoArchiveOrderStatus(partial, qty, enums.TifIOC, enums.StatusUnknown); got != enums.StatusCanceled {
		t.Fatalf("partial IOC status=%s, want Canceled", got)
	}
	if got := nadoArchiveOrderStatus(qty, qty, enums.TifGTC, enums.StatusUnknown); got != enums.StatusFilled {
		t.Fatalf("fully filled GTC status=%s, want Filled", got)
	}
}

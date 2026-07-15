package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
)

func TestNadoExecutionMassStatusUsesMaximumFillLimitAndWarnsOnSaturation(t *testing.T) {
	for _, test := range []struct {
		name        string
		matchCount  int
		wantWarning bool
	}{
		{name: "below limit", matchCount: 499},
		{name: "at limit", matchCount: 500, wantWarning: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if request["type"] != "subaccount_orders" {
					http.Error(w, "unexpected query", http.StatusBadRequest)
					return
				}
				_, _ = fmt.Fprintf(w, `{"status":"success","data":{"sender":%q,"product_id":%v,"orders":[]}}`, request["sender"], request["product_id"])
			}))
			defer server.Close()

			rest := nadoTestRESTClient(t, server)
			var err error
			rest, err = rest.WithCredentials("1111111111111111111111111111111111111111111111111111111111111111", "mass-status")
			if err != nil {
				t.Fatalf("configure REST fixture: %v", err)
			}
			backend := &boundedMatchesReportBackend{matches: nadoMassStatusMatches(test.matchCount)}
			until := time.Date(2026, 7, 10, 4, 59, 59, 0, time.UTC)
			lookback := 15 * time.Minute
			exec := newExecutionClient(rest, nadoTestProvider(), clock.NewSimulatedClock(until.Add(time.Minute)), enums.KindPerp, AccountIDUnified)
			exec.reports = backend

			query := model.MassStatusQuery{
				AccountID:    AccountIDUnified,
				Until:        until,
				Lookback:     lookback,
				IncludeFills: true,
			}
			mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
			if err != nil {
				t.Fatalf("GenerateExecutionMassStatus: %v", err)
			}
			if backend.calls != 1 || backend.limit != 500 {
				t.Fatalf("GetMatches calls=%d limit=%d, want one call with Nado maximum 500", backend.calls, backend.limit)
			}
			if mass.Lookback != lookback {
				t.Fatalf("mass lookback=%s, want %s", mass.Lookback, lookback)
			}
			if len(mass.FillReports) != 0 {
				t.Fatalf("out-of-window fill reports=%+v, want none", mass.FillReports)
			}
			if got := nadoHasReportWarning(mass.Warnings, "FILL_REPORTS_LIMIT_REACHED"); got != test.wantWarning {
				t.Fatalf("limit warning=%v, want %v; warnings=%+v", got, test.wantWarning, mass.Warnings)
			}
			wantCoverage := model.CoverageComplete
			if test.wantWarning {
				wantCoverage = model.CoveragePartial
			}
			if mass.OpenOrdersCoverage.State != model.CoverageComplete || mass.FillsCoverage.State != wantCoverage || mass.PositionsCoverage.State != model.CoverageNotRequested {
				t.Fatalf("coverage=%+v/%+v/%+v, want Complete/%s/NotRequested", mass.OpenOrdersCoverage, mass.FillsCoverage, mass.PositionsCoverage, wantCoverage)
			}
			wantID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
			if open := mass.OpenOrdersCoverage.Scope; open.AccountID != AccountIDUnified || open.ClientID != "" || len(open.InstrumentIDs) != 1 || open.InstrumentIDs[0] != wantID || !open.Through.Equal(until.Add(time.Minute)) || !open.From.IsZero() {
				t.Fatalf("open-order coverage scope=%+v, want exact Nado perp snapshot scope", open)
			}
			if fills := mass.FillsCoverage.Scope; fills.AccountID != AccountIDUnified || fills.ClientID != "" || len(fills.InstrumentIDs) != 1 || fills.InstrumentIDs[0] != wantID || !fills.From.Equal(until.Add(-lookback)) || !fills.Through.Equal(until) {
				t.Fatalf("fill coverage scope=%+v, want exact [%s,%s] scope", fills, until.Add(-lookback), until)
			}
			if !mass.PositionsCoverage.Scope.IsZero() {
				t.Fatalf("not-requested position coverage scope=%+v, want zero", mass.PositionsCoverage.Scope)
			}
			if err := mass.ValidateFor(query); err != nil {
				t.Fatalf("ValidateFor: %v", err)
			}
		})
	}
}

type boundedMatchesReportBackend struct {
	matches *sdk.ArchiveMatchesResponse
	calls   int
	limit   int
}

func (*boundedMatchesReportBackend) Sender() (string, error) { return "sender-1", nil }

func (b *boundedMatchesReportBackend) GetMatches(_ context.Context, _ string, _ []int64, limit int) (*sdk.ArchiveMatchesResponse, error) {
	b.calls++
	b.limit = limit
	return b.matches, nil
}

func (*boundedMatchesReportBackend) GetAccountSnapshot(context.Context) (*sdk.AccountSnapshot, error) {
	return nil, nil
}

func nadoMassStatusMatches(count int) *sdk.ArchiveMatchesResponse {
	match := sdk.Match{
		Digest:        "digest-fill",
		BaseFilled:    "1000000000000000000",
		Fee:           "0",
		SubmissionIdx: "42",
		Timestamp:     "2026-07-10T05:00:00Z",
		Order: sdk.MatchOrder{
			PriceX18: "3000000000000000000",
			Amount:   "1000000000000000000",
		},
	}
	matches := make([]sdk.Match, count)
	for i := range matches {
		matches[i] = match
	}
	return &sdk.ArchiveMatchesResponse{
		Matches: matches,
		Txs: []sdk.Tx{{
			SubmissionIdx: "42",
			TxInfo:        sdk.TxInfo{MatchOrders: sdk.MatchOrders{ProductId: 2}},
		}},
	}
}

func nadoHasReportWarning(warnings []model.ReportWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

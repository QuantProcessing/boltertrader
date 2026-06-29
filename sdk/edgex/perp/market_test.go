package perp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetFundingRatePreservesLatestFundingRateResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/public/funding/getLatestFundingRate" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("contractId") != "10000001" {
			t.Fatalf("unexpected contractId: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","data":[{"contractId":"10000001","fundingRate":"0.00080000","fundingTimestamp":"1000","fundingRateIntervalMin":"480","markPrice":"201","indexPrice":"199","oraclePrice":"200"}]}`))
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	client.PublicAPIVersion = "v2"
	got, err := client.GetFundingRate(context.Background(), "10000001")
	if err != nil {
		t.Fatalf("GetFundingRate: %v", err)
	}
	if got.FundingRate != "0.00080000" {
		t.Fatalf("expected settlement-interval rate, got %q", got.FundingRate)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal raw response: %v", err)
	}
	if bytes.Contains(raw, []byte("hourlyFundingRate")) || bytes.Contains(raw, []byte("nextFundingTime")) {
		t.Fatalf("SDK must not add derived funding fields: %s", raw)
	}
}

func TestGetFundingRateHistoryPaginatesV2SettlementFunding(t *testing.T) {
	t.Parallel()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v2/public/funding/getFundingRatePage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("contractId") != "10000001" {
			t.Fatalf("unexpected contractId: %s", r.URL.RawQuery)
		}
		if q.Get("filterBeginTimeInclusive") != "1000" || q.Get("filterEndTimeExclusive") != "9000" {
			t.Fatalf("unexpected time filters: %s", r.URL.RawQuery)
		}
		if q.Get("filterSettlementFundingRate") != "true" {
			t.Fatalf("expected settlement funding filter, got: %s", r.URL.RawQuery)
		}
		switch calls {
		case 1:
			if q.Get("size") != "100" || q.Get("offsetData") != "" {
				t.Fatalf("unexpected first page query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"SUCCESS","data":{"dataList":[{"contractId":"10000001","fundingRate":"0.0001","fundingTime":"1000","fundingTimestamp":"1000","fundingRateIntervalMin":"240"}],"nextPageOffsetData":"next-page"}}`))
		case 2:
			if q.Get("size") != "100" || q.Get("offsetData") != "next-page" {
				t.Fatalf("unexpected second page query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"SUCCESS","data":{"dataList":[{"contractId":"10000001","fundingRate":"0.0002","fundingTime":"2000","fundingTimestamp":"2000","fundingRateIntervalMin":"240"}],"nextPageOffsetData":""}}`))
		default:
			t.Fatalf("unexpected extra page request: %s", r.URL.RawQuery)
		}
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	client.PublicAPIVersion = "v2"
	rows, err := client.GetFundingRateHistory(context.Background(), "10000001", 1000, 9000, 150)
	if err != nil {
		t.Fatalf("GetFundingRateHistory: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 page requests, got %d", calls)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 funding rows, got %d", len(rows))
	}
	if rows[0].FundingRate != "0.0001" || rows[1].FundingRate != "0.0002" {
		t.Fatalf("unexpected funding rates: %#v", rows)
	}
}

// TestGetFundingRate tests retrieving funding rate for a specific contract
func TestGetFundingRate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	client := NewClient()
	ctx := context.Background()

	rate, err := client.GetFundingRate(ctx, liveEdgeXContractID(t, client))
	if err != nil {
		t.Fatalf("Failed to get funding rate: %v", err)
	}

	if rate == nil {
		t.Fatal("Expected funding rate, got nil")
	}

	if rate.ContractId == "" {
		t.Error("Expected non-empty contract ID")
	}

	if rate.FundingRate == "" {
		t.Error("Expected non-empty funding rate")
	}

	t.Logf("Contract %s funding rate: %s", rate.ContractId, rate.FundingRate)
	t.Logf("Index price: %s", rate.IndexPrice)
	t.Logf("Oracle price: %s", rate.OraclePrice)
}

// TestGetAllFundingRates tests retrieving all funding rates
// Note: EdgeX API may not return all rates when contractId is not specified
func TestGetAllFundingRates(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	client := NewClient()
	ctx := context.Background()

	rates, err := client.GetAllFundingRates(ctx)
	if err != nil {
		t.Fatalf("Failed to get all funding rates: %v", err)
	}

	// EdgeX may return empty array when no contractId is specified
	// This is expected behavior and not an error
	t.Logf("Total contracts with funding rates: %d", len(rates))

	if len(rates) > 0 {
		// Show first 3 rates if available
		for i, rate := range rates {
			if i >= 3 {
				break
			}
			t.Logf("Contract %s: rate=%s, indexPrice=%s",
				rate.ContractId, rate.FundingRate, rate.IndexPrice)
		}
	} else {
		t.Log("No funding rates returned (API behavior when contractId not specified)")
	}
}

func liveEdgeXContractID(t *testing.T, client *Client) string {
	t.Helper()
	info, err := client.GetExchangeInfo(context.Background())
	if err != nil {
		t.Fatalf("GetExchangeInfo: %v", err)
	}
	if len(info.ContractList) == 0 {
		t.Fatal("expected at least one EdgeX V2 contract")
	}
	return info.ContractList[0].ContractId
}

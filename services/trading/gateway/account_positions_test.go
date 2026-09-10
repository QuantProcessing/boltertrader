package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
)

func TestPositionSnapshotReadsCurrentGatewaySnapshotWithoutAccountPreflight(t *testing.T) {
	const token = "offline-gateway-token-at-least-32-bytes"
	var calls atomic.Int64
	verified := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/v1/position-snapshot" || request.URL.Query().Get("account_id") != "account-a" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL)
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("X-Bolter-Principal") != "" {
			t.Error("missing service identity")
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": accountpositions.SourceSnapshot{
			AccountID: "account-a", Freshness: accountpositions.FreshnessLive, VerifiedAt: verified,
			Items: []accountpositions.SourceItem{},
		}})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, false, []byte(token), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.PositionSnapshot(t.Context(), "owner", "account-a")
	if err != nil || snapshot.AccountID != "account-a" || snapshot.Freshness != accountpositions.FreshnessLive || !snapshot.VerifiedAt.Equal(verified) || snapshot.Items == nil || len(snapshot.Items) != 0 {
		t.Fatalf("current empty snapshot=%+v err=%v", snapshot, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests=%d, expected just the current position snapshot", calls.Load())
	}
	for _, scope := range [][2]string{{"", "account-a"}, {"owner", ""}} {
		if _, err := client.PositionSnapshot(t.Context(), scope[0], scope[1]); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("invalid scope reached Gateway")
	}
}

func TestPositionSnapshotRejectsDifferentAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"data":{"account_id":"account-b","freshness":"live","items":[]}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, false, []byte(strings.Repeat("x", 32)), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PositionSnapshot(t.Context(), "owner", "account-a"); err == nil {
		t.Fatal("another account's positions accepted")
	}
}

package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestGetOrdersByDigestsUsesArchiveExactQuery(t *testing.T) {
	const digest = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1" {
			t.Fatalf("archive request=%s %s, want POST /v1", r.Method, r.URL.Path)
		}
		var request ArchiveOrdersRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode archive orders request: %v", err)
		}
		if !reflect.DeepEqual(request.Orders.Digests, []string{digest}) {
			t.Fatalf("archive digests=%v, want exact digest", request.Orders.Digests)
		}
		if request.Orders.Limit != 1 {
			t.Fatalf("archive limit=%d, want one result per requested digest", request.Orders.Limit)
		}
		_, _ = w.Write([]byte(`{"orders":[{"digest":"` + digest + `","subaccount":"sender","product_id":1,"submission_idx":"42","last_fill_submission_idx":"43","amount":"2000000000000000000","price_x18":"1000000000000000000","base_filled":"1000000000000000000","quote_filled":"-1000000000000000000","fee":"1000000000000000","first_fill_timestamp":"1783908000","last_fill_timestamp":"1783908001","expiration":"4000000000","appendix":"1"}]}`))
	}))
	defer server.Close()

	client := newNadoQueryClientForServer(t, server)
	orders, err := client.GetOrdersByDigests(context.Background(), []string{digest})
	if err != nil {
		t.Fatalf("GetOrdersByDigests: %v", err)
	}
	if orders == nil || len(orders.Orders) != 1 || orders.Orders[0].Digest != digest || orders.Orders[0].BaseFilled != "1000000000000000000" {
		t.Fatalf("archive orders=%+v", orders)
	}
}

func TestGetOrdersByDigestsRequestsEveryDigestAboveDefaultLimit(t *testing.T) {
	digests := make([]string, 101)
	for i := range digests {
		digests[i] = "0x" + strings.Repeat("0", 62) + fmt.Sprintf("%02x", i)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ArchiveOrdersRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode archive orders request: %v", err)
		}
		if request.Orders.Limit != len(digests) {
			t.Fatalf("archive limit=%d, want %d", request.Orders.Limit, len(digests))
		}
		if !reflect.DeepEqual(request.Orders.Digests, digests) {
			t.Fatalf("archive digests were truncated: got=%d want=%d", len(request.Orders.Digests), len(digests))
		}
		_, _ = w.Write([]byte(`{"orders":[]}`))
	}))
	defer server.Close()

	client := newNadoQueryClientForServer(t, server)
	if _, err := client.GetOrdersByDigests(context.Background(), digests); err != nil {
		t.Fatalf("GetOrdersByDigests: %v", err)
	}
}

func TestGetOrdersByDigestsRejectsInvalidScopeBeforeIO(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid digest scope reached archive transport")
	}))
	defer server.Close()
	client := newNadoQueryClientForServer(t, server)
	if _, err := client.GetOrdersByDigests(context.Background(), nil); err == nil {
		t.Fatal("empty digest query succeeded")
	}
	tooMany := make([]string, 501)
	if _, err := client.GetOrdersByDigests(context.Background(), tooMany); err == nil {
		t.Fatal("oversized digest query succeeded")
	}
}

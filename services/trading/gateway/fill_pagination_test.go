package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestGatewayFillPaginationRejectsNonAdjacentCursorCycle(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	responses := map[string]string{
		"":  `{"items":[],"next_cursor":"A"}`,
		"A": `{"items":[],"next_cursor":"B"}`,
		"B": `{"items":[],"next_cursor":"A"}`,
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 10 {
			_, _ = fmt.Fprintf(w, `{"items":[],"next_cursor":%q}`, r.URL.Query().Get("pagination.cursor"))
			return
		}
		_, _ = w.Write([]byte(responses[r.URL.Query().Get("pagination.cursor")]))
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.fetchFills(context.Background(), "order-a", "account-a", "BTC-USDT"); err == nil || !strings.Contains(err.Error(), "cursor cycled") {
		t.Fatalf("non-adjacent fill cursor cycle error=%v", err)
	}
	if requests != 3 {
		t.Fatalf("non-adjacent cursor cycle required %d requests, want 3", requests)
	}
}

func TestGatewayFillPaginationHasFinitePageBound(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		next := "page-" + strconv.Itoa(requests)
		if requests > maxFillPages+5 {
			next = r.URL.Query().Get("pagination.cursor")
		}
		_, _ = fmt.Fprintf(w, `{"items":[],"next_cursor":%q}`, next)
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.fetchFills(context.Background(), "order-a", "account-a", "BTC-USDT"); err == nil || !strings.Contains(err.Error(), "pagination exceeded") {
		t.Fatalf("fill pagination page-bound error=%v", err)
	}
	if requests != maxFillPages {
		t.Fatalf("fill pagination made %d requests, want %d", requests, maxFillPages)
	}
}

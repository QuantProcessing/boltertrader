package spot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestClient_SubmitOutcomeActionsBuildUserOutcomeRequests(t *testing.T) {
	type expectedAction struct {
		name        string
		assertChild func(t *testing.T, child map[string]any)
		call        func(ctx context.Context, client *Client) (string, error)
	}
	cases := []expectedAction{
		{
			name: "splitOutcome",
			assertChild: func(t *testing.T, child map[string]any) {
				t.Helper()
				if got := int(child["outcome"].(float64)); got != 50 {
					t.Fatalf("unexpected split outcome: %d", got)
				}
				if got := child["amount"]; got != "1.25" {
					t.Fatalf("unexpected split amount: %v", got)
				}
			},
			call: func(ctx context.Context, client *Client) (string, error) {
				return client.SubmitSplitOutcome(ctx, 50, "1.25")
			},
		},
		{
			name: "mergeOutcome",
			assertChild: func(t *testing.T, child map[string]any) {
				t.Helper()
				if got := int(child["outcome"].(float64)); got != 51 {
					t.Fatalf("unexpected merge outcome: %d", got)
				}
				if _, ok := child["amount"]; !ok || child["amount"] != nil {
					t.Fatalf("expected merge amount to be explicit null, got %#v", child["amount"])
				}
			},
			call: func(ctx context.Context, client *Client) (string, error) {
				return client.SubmitMergeOutcome(ctx, 51, nil)
			},
		},
		{
			name: "mergeQuestion",
			assertChild: func(t *testing.T, child map[string]any) {
				t.Helper()
				if got := int(child["question"].(float64)); got != 9 {
					t.Fatalf("unexpected merge question: %d", got)
				}
				if got := child["amount"]; got != "2" {
					t.Fatalf("unexpected merge question amount: %v", got)
				}
			},
			call: func(ctx context.Context, client *Client) (string, error) {
				amount := "2"
				return client.SubmitMergeQuestion(ctx, 9, &amount)
			},
		},
		{
			name: "negateOutcome",
			assertChild: func(t *testing.T, child map[string]any) {
				t.Helper()
				if got := int(child["question"].(float64)); got != 9 {
					t.Fatalf("unexpected negate question: %d", got)
				}
				if got := int(child["outcome"].(float64)); got != 52 {
					t.Fatalf("unexpected negate outcome: %d", got)
				}
				if got := child["amount"]; got != "0.5" {
					t.Fatalf("unexpected negate amount: %v", got)
				}
			},
			call: func(ctx context.Context, client *Client) (string, error) {
				return client.SubmitNegateOutcome(ctx, 9, 52, "0.5")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seenAction map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var payload struct {
					Action map[string]any `json:"action"`
				}
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				seenAction = payload.Action
				_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"default","data":{"status":"ok","hash":"0xabc"}}}`))
			}))
			defer srv.Close()

			base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
			base.BaseURL = srv.URL
			client := NewClient(base)

			result, err := tc.call(context.Background(), client)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if result != "0xabc" {
				t.Fatalf("unexpected result: %s", result)
			}
			if got := seenAction["type"]; got != "userOutcome" {
				t.Fatalf("unexpected action type: %v", got)
			}
			child, ok := seenAction[tc.name].(map[string]any)
			if !ok {
				t.Fatalf("missing %s child action: %#v", tc.name, seenAction)
			}
			tc.assertChild(t, child)
		})
	}
}

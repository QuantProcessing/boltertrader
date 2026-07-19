package okx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSOrderMutationPreSendFailuresUseTypedSentinel(t *testing.T) {
	client := NewWSClient(context.Background()).WithCredentials("key", "secret", "pass")
	instIDCode := int64(1)
	_, placeErr := client.PlaceOrderWS(&OrderRequest{InstIdCode: &instIDCode, Sz: "1"})
	if !errors.Is(placeErr, ErrWSPreSend) {
		t.Fatalf("PlaceOrderWS error=%v, want ErrWSPreSend", placeErr)
	}
	orderID := "1"
	_, cancelErr := client.CancelOrderWS(instIDCode, &orderID, nil)
	if !errors.Is(cancelErr, ErrWSPreSend) {
		t.Fatalf("CancelOrderWS error=%v, want ErrWSPreSend", cancelErr)
	}
}

func TestWSOrderMutationTimeoutsUsePostSendUnknownSentinel(t *testing.T) {
	restore := setTestWSOrderResponseTimeout(t, 20*time.Millisecond)
	defer restore()

	for _, tc := range testOKXWSMutationCalls() {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestOKXWSMutationClient(t, func(t *testing.T, conn *websocket.Conn, request testOKXWSRequest) {
				if request.Op != tc.op {
					t.Fatalf("unexpected op %s", request.Op)
				}
			})
			_, err := tc.call(client)
			if !errors.Is(err, ErrWSOutcomeUnknown) {
				t.Fatalf("%s error=%v, want ErrWSOutcomeUnknown", tc.name, err)
			}
		})
	}
}

func TestWSOrderMutationReadySocketWriteFailureIsPostSendUnknown(t *testing.T) {
	for _, tc := range testOKXWSMutationCalls() {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestOKXReadySocketWithoutReader(t)
			conn := client.currentConnection()
			if conn == nil {
				t.Fatal("test client is not connected")
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("close ready connection: %v", err)
			}
			_, err := tc.call(client)
			if !errors.Is(err, ErrWSOutcomeUnknown) {
				t.Fatalf("%s closed-ready-socket error=%v, want ErrWSOutcomeUnknown", tc.name, err)
			}
		})
	}
}

func TestWSOrderMutationTopLevelErrorEnvelopeUsesTypedAPIError(t *testing.T) {
	for _, tc := range testOKXWSMutationCalls() {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestOKXWSMutationClient(t, func(t *testing.T, conn *websocket.Conn, request testOKXWSRequest) {
				if request.Op != tc.op {
					t.Fatalf("unexpected op %s", request.Op)
				}
				if err := conn.WriteJSON(map[string]any{
					"id":    request.ID,
					"event": "error",
					"code":  "51000",
					"msg":   "secret exchange rejection detail",
				}); err != nil {
					t.Fatalf("write error response: %v", err)
				}
			})
			var apiErr *APIError
			_, err := tc.call(client)
			if !errors.As(err, &apiErr) || apiErr.Code != "51000" {
				t.Fatalf("%s error=%T %[2]v, want APIError code 51000", tc.name, err)
			}
			if strings.Contains(err.Error(), "{") {
				t.Fatalf("%s leaked raw JSON envelope: %v", tc.name, err)
			}
		})
	}
}

func TestWSOrderMutationRowRejectionUsesTypedAPIError(t *testing.T) {
	for _, tc := range testOKXWSMutationCalls() {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestOKXWSMutationClient(t, func(t *testing.T, conn *websocket.Conn, request testOKXWSRequest) {
				if request.Op != tc.op {
					t.Fatalf("unexpected op %s", request.Op)
				}
				if err := conn.WriteJSON(map[string]any{
					"id":   request.ID,
					"code": "0",
					"data": []map[string]any{{
						"ordId":   "venue-order",
						"clOrdId": "client-order",
						"sCode":   "51000",
						"sMsg":    "secret row rejection detail",
					}},
				}); err != nil {
					t.Fatalf("write row rejection response: %v", err)
				}
			})
			result, err := tc.call(client)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Code != "51000" {
				t.Fatalf("%s error=%T %[2]v, want APIError code 51000", tc.name, err)
			}
			if result == nil || result.OrdId != "venue-order" || result.ClOrdId != "client-order" {
				t.Fatalf("%s row=%+v, want rejected order identifiers", tc.name, result)
			}
		})
	}
}

func TestWSOrderMutationMalformedEnvelopesUseStablePrefix(t *testing.T) {
	for _, tc := range testOKXWSMutationCalls() {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestOKXWSMutationClient(t, func(t *testing.T, conn *websocket.Conn, request testOKXWSRequest) {
				if request.Op != tc.op {
					t.Fatalf("unexpected op %s", request.Op)
				}
				if err := conn.WriteJSON(map[string]any{
					"id":   request.ID,
					"code": "0",
					"data": []any{},
				}); err != nil {
					t.Fatalf("write malformed response: %v", err)
				}
			})
			_, err := tc.call(client)
			if !errors.Is(err, ErrWSMalformedResponse) {
				t.Fatalf("%s error=%v, want ErrWSMalformedResponse", tc.name, err)
			}
			if err == nil || !strings.HasPrefix(err.Error(), "okx ws "+tc.action+" malformed response:") {
				t.Fatalf("%s malformed error=%v, want stable prefix", tc.name, err)
			}
		})
	}
}

type testOKXWSMutationCall struct {
	name   string
	op     string
	action string
	call   func(*WSClient) (*OrderId, error)
}

func testOKXWSMutationCalls() []testOKXWSMutationCall {
	return []testOKXWSMutationCall{
		{
			name:   "place",
			op:     "order",
			action: "order",
			call: func(client *WSClient) (*OrderId, error) {
				instIDCode := int64(1)
				return client.PlaceOrderWS(&OrderRequest{InstIdCode: &instIDCode, Sz: "1"})
			},
		},
		{
			name:   "cancel",
			op:     "cancel-order",
			action: "cancel",
			call: func(client *WSClient) (*OrderId, error) {
				orderID := "1"
				return client.CancelOrderWS(1, &orderID, nil)
			},
		},
	}
}

type testOKXWSRequest struct {
	ID string `json:"id"`
	Op string `json:"op"`
}

func newTestOKXWSMutationClient(t *testing.T, handle func(*testing.T, *websocket.Conn, testOKXWSRequest)) *WSClient {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request testOKXWSRequest
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			switch request.Op {
			case "login":
				if err := conn.WriteJSON(map[string]any{"event": "login", "code": "0"}); err != nil {
					t.Errorf("write login response: %v", err)
					return
				}
			default:
				handle(t, conn, request)
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	client := NewWSClient(context.Background()).
		WithURL("ws"+strings.TrimPrefix(server.URL, "http")).
		WithCredentials("key", "secret", "pass")
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

func newTestOKXReadySocketWithoutReader(t *testing.T) *WSClient {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	client := NewWSClient(context.Background()).WithCredentials("key", "secret", "pass")
	client.mu.Lock()
	client.Conn = conn
	client.readyConn = conn
	client.authenticatedConn = conn
	client.mu.Unlock()
	t.Cleanup(client.Close)
	return client
}

func setTestWSOrderResponseTimeout(t *testing.T, timeout time.Duration) func() {
	t.Helper()
	previous := wsOrderResponseTimeout
	wsOrderResponseTimeout = timeout
	return func() { wsOrderResponseTimeout = previous }
}

func TestParseWSActionErrorRejectsMalformedErrorEnvelope(t *testing.T) {
	err := parseWSActionError("order", []byte(`{"event":"error","msg":"missing code"}`))
	if !errors.Is(err, ErrWSMalformedResponse) {
		t.Fatalf("parseWSActionError error=%v, want ErrWSMalformedResponse", err)
	}
	if strings.Contains(err.Error(), "missing code") {
		t.Fatalf("malformed error leaked raw venue msg: %v", err)
	}
}

func TestParseWSActionResponseRejectsInvalidJSON(t *testing.T) {
	_, err := parseWSActionResponse("cancel", []byte(`{`))
	if !errors.Is(err, ErrWSMalformedResponse) {
		t.Fatalf("parseWSActionResponse invalid json error=%v, want ErrWSMalformedResponse", err)
	}
}

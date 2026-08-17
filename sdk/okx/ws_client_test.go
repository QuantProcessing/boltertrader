package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSClientCompanion_NewWSClient(t *testing.T) {
	client := NewWSClient(context.Background())
	if client.URL != WSBaseURL || client.Subs == nil || client.PendingReqs == nil {
		t.Fatalf("unexpected ws client: %+v", client)
	}
}

func TestWSClientCompanion_BusinessPrivateURL(t *testing.T) {
	client := NewWSClient(context.Background()).WithCredentials("key", "secret", "pass").WithBusinessURL()
	if client.URL != WSBusinessBaseURL || !client.IsPrivate {
		t.Fatalf("unexpected business ws client: url=%s private=%v", client.URL, client.IsPrivate)
	}
}

// Credentials select the private host, not just the private flag. This was
// previously asserted only inside the demo-URL tests, where the interesting
// part was the host family; the role-to-host mapping is what actually matters
// and it outlives them.
func TestWSClient_CredentialsSelectPrivateHost(t *testing.T) {
	t.Parallel()

	client := NewWSClient(context.Background()).WithCredentials("key", "secret", "pass")
	if client.URL != WSPrivateBaseURL || !client.IsPrivate {
		t.Fatalf("private URL=%s private=%v, want %s true", client.URL, client.IsPrivate, WSPrivateBaseURL)
	}
}

// An explicit URL has to survive role changes that come after it. Callers set
// one to point at a recorded server or a proxy, and a later WithCredentials
// silently replacing it would send the test at the real venue.
func TestWSClient_ExplicitURLSurvivesLaterRoleChanges(t *testing.T) {
	t.Parallel()

	const custom = "wss://custom.example.test/ws/v5/private"
	client := NewWSClient(context.Background()).
		WithURL(custom).
		WithCredentials("key", "secret", "pass")
	if client.URL != custom {
		t.Fatalf("URL=%s, want explicit URL %s", client.URL, custom)
	}
	client.WithBusinessURL()
	if client.URL != custom {
		t.Fatalf("URL after WithBusinessURL=%s, want explicit URL %s", client.URL, custom)
	}
}

func TestWSClientDispatchBuffersWhilePausedAndDrainsInOrder(t *testing.T) {
	client := NewWSClient(context.Background())
	args := WsSubscribeArgs{Channel: "orders", InstType: "SPOT"}

	var got []string
	client.Subs[args] = func(data []byte) {
		if strings.Contains(string(data), `"ordId":"one"`) {
			got = append(got, "one")
			return
		}
		if strings.Contains(string(data), `"ordId":"two"`) {
			got = append(got, "two")
			return
		}
		got = append(got, "unknown")
	}

	client.PauseDispatch()
	client.handleMessage([]byte(`{"arg":{"channel":"orders","instType":"SPOT"},"data":[{"ordId":"one"}]}`))
	client.handleMessage([]byte(`{"arg":{"channel":"orders","instType":"SPOT"},"data":[{"ordId":"two"}]}`))
	if len(got) != 0 {
		t.Fatalf("paused dispatch delivered messages: %v", got)
	}

	client.ResumeDispatch(func() { got = append(got, "hook") })
	want := []string{"hook", "one", "two"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestWSClientReconnectResubscribesOffline(t *testing.T) {
	gotSubscribe := make(chan string, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		gotSubscribe <- string(msg)
		time.Sleep(25 * time.Millisecond)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.Subs[WsSubscribeArgs{Channel: "orders", InstType: "SPOT"}] = func([]byte) {}
	defer client.Close()

	client.reconnect()

	select {
	case msg := <-gotSubscribe:
		if !strings.Contains(msg, `"op":"subscribe"`) || !strings.Contains(msg, `"channel":"orders"`) {
			t.Fatalf("replayed subscribe=%s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for replayed subscribe")
	}
}

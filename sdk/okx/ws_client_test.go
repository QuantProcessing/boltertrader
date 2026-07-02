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

func TestWSClient_DemoGlobalURLs(t *testing.T) {
	t.Parallel()

	client := NewWSClient(context.Background()).WithEnvironment(Simulated)
	if client.URL != WSDemoBaseURL {
		t.Fatalf("public URL=%s, want %s", client.URL, WSDemoBaseURL)
	}
	client.WithCredentials("key", "secret", "pass")
	if client.URL != WSDemoPrivateBaseURL || !client.IsPrivate {
		t.Fatalf("private URL=%s private=%v", client.URL, client.IsPrivate)
	}
	client.WithBusinessURL()
	if client.URL != WSDemoBusinessBaseURL {
		t.Fatalf("business URL=%s, want %s", client.URL, WSDemoBusinessBaseURL)
	}
}

func TestWSClient_DemoEEAURLs(t *testing.T) {
	t.Parallel()

	client := NewWSClient(context.Background()).WithEnvironment(Simulated).WithDemoHostProfile(DemoHostProfileEEA)
	if client.URL != WSDemoEEABaseURL {
		t.Fatalf("public URL=%s, want %s", client.URL, WSDemoEEABaseURL)
	}
	client.WithCredentials("key", "secret", "pass")
	if client.URL != WSDemoEEAPrivateBaseURL {
		t.Fatalf("private URL=%s, want %s", client.URL, WSDemoEEAPrivateBaseURL)
	}
	client.WithBusinessURL()
	if client.URL != WSDemoEEABusinessBaseURL {
		t.Fatalf("business URL=%s, want %s", client.URL, WSDemoEEABusinessBaseURL)
	}
}

func TestWSClient_EnvironmentPreservesSelectedRole(t *testing.T) {
	t.Parallel()

	client := NewWSClient(context.Background()).WithCredentials("key", "secret", "pass")
	client.WithEnvironment(Simulated)
	if client.URL != WSDemoPrivateBaseURL {
		t.Fatalf("private URL=%s, want %s", client.URL, WSDemoPrivateBaseURL)
	}
}

func TestWSClient_CustomURLOverride(t *testing.T) {
	t.Parallel()

	const custom = "wss://example.test/ws/v5/private"
	client := NewWSClient(context.Background()).
		WithEnvironment(Simulated).
		WithDemoHostProfile(DemoHostProfileCustom).
		WithURL(custom)
	if client.URL != custom {
		t.Fatalf("URL=%s, want %s", client.URL, custom)
	}
}

func TestWSClient_CustomProfileRequiresURLOverride(t *testing.T) {
	t.Parallel()

	client := NewWSClient(context.Background()).
		WithEnvironment(Simulated).
		WithDemoHostProfile(DemoHostProfileCustom)
	if err := client.Connect(); err == nil || !strings.Contains(err.Error(), "custom demo host profile") {
		t.Fatalf("Connect err=%v, want custom profile error", err)
	}
	if client.URL != "" {
		t.Fatalf("URL=%s, want empty fail-closed URL", client.URL)
	}
}

func TestWSClient_UnknownProfileRequiresValidEndpoint(t *testing.T) {
	t.Parallel()

	client := NewWSClient(context.Background()).
		WithEnvironment(Simulated).
		WithDemoHostProfile(DemoHostProfile("mars"))
	if err := client.Connect(); err == nil || !strings.Contains(err.Error(), "unknown demo host profile") {
		t.Fatalf("Connect err=%v, want unknown profile error", err)
	}
	if client.URL != "" {
		t.Fatalf("URL=%s, want empty fail-closed URL", client.URL)
	}
}

func TestWSClient_ExplicitURLSurvivesEnvironmentCallOrder(t *testing.T) {
	t.Parallel()

	const custom = "wss://custom.example.test/ws/v5/private"
	client := NewWSClient(context.Background()).
		WithURL(custom).
		WithEnvironment(Simulated).
		WithDemoHostProfile(DemoHostProfileCustom).
		WithCredentials("key", "secret", "pass")
	if client.URL != custom {
		t.Fatalf("URL=%s, want explicit URL %s", client.URL, custom)
	}
	if client.endpointErr != nil {
		t.Fatalf("endpointErr=%v, want nil for explicit URL", client.endpointErr)
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

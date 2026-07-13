package spot

import (
	"context"
	"reflect"
	"testing"
)

func TestWsTransportEndpointIsNotPubliclyMutable(t *testing.T) {
	if field, exists := reflect.TypeOf(WSClient{}).FieldByName("URL"); exists && field.IsExported() {
		t.Fatal("WSClient exposes a mutable URL endpoint")
	}
	client := newWSClient(context.Background(), "wss://sstream.asterdex-testnet.com/ws")
	t.Cleanup(client.Close)
	if client.endpoint != "wss://sstream.asterdex-testnet.com/ws" {
		t.Fatalf("endpoint = %q", client.endpoint)
	}
}

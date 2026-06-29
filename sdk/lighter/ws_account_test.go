package lighter

import (
	"strconv"
	"testing"
)

func TestWSAccountCompanion_ChannelFormat(t *testing.T) {
	rest, auth := lighterWSAuth(t)
	client := newLiveWSClient(t)
	if err := client.SubscribeAccountAll(rest.AccountIndex, auth, func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAccountAll: %v", err)
	}
	if client.Subscriptions["account_all/"+strconv.FormatInt(rest.AccountIndex, 10)] == nil {
		t.Fatal("expected account_all subscription")
	}
}

package spot

import (
	"testing"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

func TestApplySDKRequestOpts(t *testing.T) {
	params := map[string]interface{}{"symbol": "BTCUSDT"}
	applySDKRequestOpts(params, sdkcore.RequestOpts{})
	if _, ok := params["recvWindow"]; ok {
		t.Fatal("empty opts should not set recvWindow")
	}

	applySDKRequestOpts(params, sdkcore.RequestOpts{
		RecvWindowMillis: 2500,
		ClientRequestID:  "client-1",
	})
	if params["recvWindow"] != int64(2500) {
		t.Fatalf("unexpected recvWindow: %#v", params["recvWindow"])
	}
	if params["newClientOrderId"] != "client-1" {
		t.Fatalf("unexpected client id: %#v", params["newClientOrderId"])
	}
}

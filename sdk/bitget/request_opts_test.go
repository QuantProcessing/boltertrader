package sdk

import (
	"testing"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

func TestApplySDKRequestOptsString(t *testing.T) {
	params := map[string]string{"symbol": "BTCUSDT"}
	applySDKRequestOptsString(params, sdkcore.RequestOpts{})
	if _, ok := params["recvWindow"]; ok {
		t.Fatal("empty opts should not set recvWindow")
	}

	applySDKRequestOptsString(params, sdkcore.RequestOpts{
		RecvWindowMillis: 2500,
		ClientRequestID:  "client-1",
	})
	if params["recvWindow"] != "2500" {
		t.Fatalf("unexpected recvWindow: %#v", params["recvWindow"])
	}
	if params["clientOid"] != "client-1" {
		t.Fatalf("unexpected client id: %#v", params["clientOid"])
	}
}

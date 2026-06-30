package lighter

import (
	"testing"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

func TestApplySDKRequestOptsString(t *testing.T) {
	params := map[string]string{"market_id": "0"}
	applySDKRequestOptsString(params, sdkcore.RequestOpts{})
	if _, ok := params["recv_window"]; ok {
		t.Fatal("empty opts should not set recv_window")
	}

	applySDKRequestOptsString(params, sdkcore.RequestOpts{
		RecvWindowMillis: 2500,
		ClientRequestID:  "client-1",
	})
	if params["recv_window"] != "2500" {
		t.Fatalf("unexpected recv_window: %#v", params["recv_window"])
	}
	if params["client_request_id"] != "client-1" {
		t.Fatalf("unexpected client id: %#v", params["client_request_id"])
	}
}

package perp

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestOKXPerpDemoEndpointsCustomProfile(t *testing.T) {
	endpoints := okxDemoEndpoints(t, testenv.OKXDemoConfig{
		HostProfile: testenv.OKXDemoHostProfileCustom,
		RESTBaseURL: "https://okx-rest.example.test",
		WSBaseURL:   "wss://okx-ws.example.test/",
	})

	if endpoints.REST != "https://okx-rest.example.test" {
		t.Fatalf("REST=%q", endpoints.REST)
	}
	if endpoints.WSPublic != "wss://okx-ws.example.test/ws/v5/public" {
		t.Fatalf("WSPublic=%q", endpoints.WSPublic)
	}
	if endpoints.WSPrivate != "wss://okx-ws.example.test/ws/v5/private" {
		t.Fatalf("WSPrivate=%q", endpoints.WSPrivate)
	}
	if endpoints.WSBusiness != "wss://okx-ws.example.test/ws/v5/business" {
		t.Fatalf("WSBusiness=%q", endpoints.WSBusiness)
	}
}

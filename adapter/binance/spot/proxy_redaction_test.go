package spot

import (
	"strings"
	"testing"
	"time"
)

func TestDemoHTTPClientRedactsInvalidProxyCredentials(t *testing.T) {
	const secret = "proxy-super-secret"
	t.Setenv("PROXY", "http://user:"+secret+"@%gh")
	_, err := demoHTTPClient(time.Second)
	if err == nil {
		t.Fatal("demoHTTPClient unexpectedly accepted malformed proxy")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("proxy credential leaked in error: %v", err)
	}
}

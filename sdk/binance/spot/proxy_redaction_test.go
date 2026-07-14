package spot

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestWebSocketProxyLogsNeverExposeCredentials(t *testing.T) {
	const secret = "proxy-super-secret"
	for _, proxy := range []string{
		"http://user:" + secret + "@127.0.0.1:1",
		"http://user:" + secret + "@%gh",
	} {
		t.Run(proxy[len(proxy)-3:], func(t *testing.T) {
			t.Setenv("PROXY", proxy)
			var logs bytes.Buffer
			logger := zap.New(zapcore.NewCore(
				zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
				zapcore.AddSync(&logs),
				zap.DebugLevel,
			)).Sugar()

			stream := NewWSClient(context.Background(), "ws://127.0.0.1:1")
			stream.Logger = logger
			_ = stream.Connect()

			api := NewWsAPIClient(context.Background()).WithURL("ws://127.0.0.1:1")
			api.Logger = logger
			_ = api.Connect()

			if strings.Contains(logs.String(), secret) {
				t.Fatalf("proxy credential leaked in websocket logs: %s", logs.String())
			}
		})
	}
}

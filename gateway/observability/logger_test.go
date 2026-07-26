package observability

import (
	"bytes"
	"strings"
	"testing"
)

func TestSafeLoggerEmitsOnlyBoundedStructuredFields(t *testing.T) {
	var output bytes.Buffer
	logger := NewSafeLogger(&output)
	logger.Info("gateway_started", "ready", "018f0000-0000-7000-8000-000000000001")
	logger.Error(`secret-canary-"雪"`, "native venue secret-canary", "secret-canary")

	text := output.String()
	if !strings.Contains(text, `"event":"gateway_started"`) ||
		!strings.Contains(text, `"code":"ready"`) ||
		!strings.Contains(text, `"correlation_id":"018f0000-0000-7000-8000-000000000001"`) {
		t.Fatalf("structured safe fields missing: %s", text)
	}
	if strings.Contains(text, "secret-canary") || strings.Contains(text, "native venue") {
		t.Fatalf("logger exposed an untrusted value: %s", text)
	}
	if !strings.Contains(text, `"event":"unknown"`) || !strings.Contains(text, `"code":"unknown"`) {
		t.Fatalf("invalid dimensions were not collapsed: %s", text)
	}
}

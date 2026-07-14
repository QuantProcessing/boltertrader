package okx

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestInvalidProxyNeverLeaksCredentialsToStdout(t *testing.T) {
	const proxySecret = "proxy-super-secret"
	t.Setenv("PROXY", "http://user:"+proxySecret+"@%zz")

	output := captureOKXStdout(t, func() {
		_ = NewClient()
		_ = NewWSClient(context.Background())
	})
	if strings.Contains(output, proxySecret) || strings.Contains(output, "user:") {
		t.Fatalf("invalid proxy diagnostics leaked credentials: %q", output)
	}
}

func captureOKXStdout(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()

	run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output)
}

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReadsCredentialBundleFromStdinAndRedactsOutput(t *testing.T) {
	metadata := writeMetadata(t)
	prior := provisionAccount
	provisionAccount = func(_ context.Context, request provisionRequest) (provisionResult, error) {
		if request.Mode != "test" || request.IdempotencyKey != "provision-1" {
			t.Fatalf("request=%+v", request)
		}
		if !bytes.Contains(request.CredentialBundle, []byte("browser-secret")) {
			t.Fatalf("bundle=%q", request.CredentialBundle)
		}
		return provisionResult{AccountID: "acct_1", OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", State: "active"}, nil
	}
	t.Cleanup(func() { provisionAccount = prior })
	var stdout bytes.Buffer
	err := run([]string{"--mode", "test", "--account-file", metadata, "--idempotency-key", "provision-1"}, strings.NewReader(`{"schemaVersion":1,"secrets":{"apiSecret":"browser-secret"}}`), &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, want := range []string{`"accountId":"acct_1"`, `"operationId":"018f47a8-2578-7d3c-9d7d-5e4f4a445c33"`, `"state":"active"`} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %s: %s", want, output)
		}
	}
	for _, forbidden := range []string{"browser-secret", "secretRef", "credentialBundle", "provider"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("output exposes %q: %s", forbidden, output)
		}
	}
}

func writeMetadata(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account.json")
	data := []byte(`{"venue":"binance","displayName":"Primary","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunRejectsCredentialFlagsAndLiveMode(t *testing.T) {
	metadata := writeMetadata(t)
	for _, args := range [][]string{
		{"--mode", "live", "--account-file", metadata, "--idempotency-key", "provision-1"},
		{"--mode", "test", "--account-file", metadata, "--idempotency-key", "provision-1", "--api-secret", "forbidden"},
	} {
		if err := run(args, strings.NewReader(`{}`), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("args accepted: %v", args)
		}
	}
}

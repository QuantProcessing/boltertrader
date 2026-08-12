package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/QuantProcessing/boltertrader/backend/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestRunReadsCredentialBundleFromStdinAndRedactsOutput(t *testing.T) {
	metadata := writeMetadata(t)
	priorPreflight := preflightProvision
	prior := provisionAccount
	preflightProvision = func(string) (provisionRuntime, error) { return provisionRuntime{}, nil }
	provisionAccount = func(_ context.Context, _ provisionRuntime, request provisionRequest) (provisionResult, error) {
		if request.Mode != "test" || request.IdempotencyKey != "provision-1" {
			t.Fatalf("request=%+v", request)
		}
		if !bytes.Contains(request.CredentialBundle, []byte("browser-secret")) {
			t.Fatalf("bundle=%q", request.CredentialBundle)
		}
		return provisionResult{AccountID: "acct_1", OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", State: "active"}, nil
	}
	t.Cleanup(func() {
		preflightProvision = priorPreflight
		provisionAccount = prior
	})
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

func TestRunRejectsRealNonLoopbackConfigurationBeforeCredentialRead(t *testing.T) {
	metadata := writeMetadata(t)
	setPlatformCredentialTestEnvironment(t, "https://example.com")
	priorReader := readCredentialInput
	read := false
	readCredentialInput = func(io.Reader) ([]byte, error) {
		read = true
		return nil, fmt.Errorf("credential input must not be read")
	}
	t.Cleanup(func() { readCredentialInput = priorReader })

	err := run([]string{"--mode", "test", "--account-file", metadata, "--idempotency-key", "provision-1"}, strings.NewReader("must-not-be-read"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "NON_LOOPBACK_GATEWAY_REJECTED") {
		t.Fatalf("error=%v", err)
	}
	if read {
		t.Fatal("credential stdin was read before production configuration preflight")
	}
}

func setPlatformCredentialTestEnvironment(t *testing.T, gatewayURL string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BT_MODE", "test")
	t.Setenv("BT_BOOTSTRAP_USERNAME", "test-owner")
	t.Setenv("BT_BOOTSTRAP_PASSWORD_HASH", string(hash))
	t.Setenv("BT_PLATFORM_DATABASE", "bolter_platform_test")
	t.Setenv("BT_PLATFORM_DATABASE_ROLE", "bolter_platform_test")
	t.Setenv(config.LivePrivateWritesEnabledEnv, "false")
	t.Setenv(config.GatewayURLEnv, gatewayURL)

	writeProtected := func(directory, name, value string) string {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	modeDirectory := filepath.Join(t.TempDir(), "test")
	t.Setenv(config.ManualPreviewSigningKeyFileEnv, writeProtected(modeDirectory, "manual-preview.key", strings.Repeat("manual-key-", 4)))
	t.Setenv(config.GatewayTokenFileEnv, writeProtected(modeDirectory, "gateway.token", strings.Repeat("gateway-token-", 3)))
	secretDirectory := t.TempDir()
	t.Setenv(config.SecretKeySourceEnv, "file")
	t.Setenv(config.SecretStoreDirEnv, filepath.Join(secretDirectory, "store"))
	t.Setenv(config.SecretStoreNamespaceEnv, "boltertrader/test")
	t.Setenv(config.SecretMasterKeyVersionEnv, "v1")
	t.Setenv(config.SecretMasterKeyFileEnv, writeProtected(secretDirectory, "master.key", strings.Repeat("01", 32)))
}

func TestTTYInputRestoresEchoAfterInvalidBundle(t *testing.T) {
	var transitions []bool
	_, err := readTTYCredentialBundle(strings.NewReader("not-json\n"), func(enabled bool) error {
		transitions = append(transitions, enabled)
		return nil
	})
	if err == nil || len(transitions) != 2 || transitions[0] || !transitions[1] {
		t.Fatalf("error=%v transitions=%v", err, transitions)
	}
}

func TestTTYInputRestoresEchoAfterInjectedTerminationSignal(t *testing.T) {
	reader := &blockingReader{release: make(chan struct{})}
	t.Cleanup(func() { close(reader.release) })
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	var transitions []bool
	_, err := readTTYCredentialBundleWithSignals(reader, func(enabled bool) error {
		transitions = append(transitions, enabled)
		return nil
	}, signals)
	if err == nil || !strings.Contains(err.Error(), "CREDENTIAL_STDIN_INTERRUPTED") || len(transitions) != 2 || transitions[0] || !transitions[1] {
		t.Fatalf("error=%v transitions=%v", err, transitions)
	}
}

func TestTTYInputRestoresEchoAfterUnexpectedReadPanic(t *testing.T) {
	var echo []bool
	_, err := readTTYCredentialBundleWithSignals(&panicReader{}, func(enabled bool) error {
		echo = append(echo, enabled)
		return nil
	}, make(chan os.Signal))
	if err == nil || err.Error() != "CREDENTIAL_STDIN_UNAVAILABLE" {
		t.Fatalf("error=%v", err)
	}
	if len(echo) != 2 || echo[0] || !echo[1] {
		t.Fatalf("echo transitions=%v", echo)
	}
}

func TestRunRestoresTTYEchoBeforeProvisioningError(t *testing.T) {
	metadata := writeMetadata(t)
	priorReader := readCredentialInput
	priorPreflight := preflightProvision
	priorProvision := provisionAccount
	var transitions []bool
	preflightProvision = func(string) (provisionRuntime, error) { return provisionRuntime{}, nil }
	readCredentialInput = func(io.Reader) ([]byte, error) {
		return readTTYCredentialBundleWithSignals(strings.NewReader("{}\n"), func(enabled bool) error {
			transitions = append(transitions, enabled)
			return nil
		}, make(chan os.Signal))
	}
	provisionAccount = func(context.Context, provisionRuntime, provisionRequest) (provisionResult, error) {
		if len(transitions) != 2 || transitions[0] || !transitions[1] {
			t.Fatalf("echo was not restored before service call: %v", transitions)
		}
		return provisionResult{}, fmt.Errorf("synthetic service failure")
	}
	t.Cleanup(func() {
		readCredentialInput = priorReader
		preflightProvision = priorPreflight
		provisionAccount = priorProvision
	})
	err := run([]string{"--mode", "test", "--account-file", metadata, "--idempotency-key", "provision-1"}, strings.NewReader("ignored"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || len(transitions) != 2 {
		t.Fatalf("error=%v transitions=%v", err, transitions)
	}
}

type blockingReader struct {
	release chan struct{}
}

func (reader *blockingReader) Read([]byte) (int, error) {
	<-reader.release
	return 0, io.EOF
}

func TestRunRejectsNonLoopbackAndGatewayBypassConfigurationBeforeReadingStdin(t *testing.T) {
	metadata := writeMetadata(t)
	for _, args := range [][]string{
		{"--mode", "test", "--account-file", metadata, "--idempotency-key", "provision-1", "--platform-url", "http://192.0.2.1:18091"},
		{"--mode", "test", "--account-file", metadata, "--idempotency-key", "provision-1", "--gateway-bypass"},
	} {
		reader := &panicReader{}
		if err := run(args, reader, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("unsafe configuration accepted: %v", args)
		}
	}
}

func TestPlatformCredentialRejectsDeceptiveConnectedDatabaseIdentity(t *testing.T) {
	err := validateConnectedPlatformIdentity("bolter_platform_live", "bolter_platform_live", "bolter_platform_test", "bolter_platform_test")
	if err == nil || !strings.Contains(err.Error(), "CONNECTED_DATABASE_IDENTITY_MISMATCH") {
		t.Fatalf("error=%v", err)
	}
}

type panicReader struct{}

func (*panicReader) Read([]byte) (int, error) { panic("stdin read before configuration rejection") }

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeMappingValidatesTrustedProfiles(t *testing.T) {
	artifact, err := decodeMapping([]byte(`{
		"schemaVersion":1,
		"mappings":[{"venue":"binance","nativeEnvironment":"demo","legacyProduct":"perp-usdt","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","routeId":"perp-usdt"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Mappings) != 1 || artifact.Mappings[0].RouteID != "perp-usdt" {
		t.Fatalf("artifact=%+v", artifact)
	}
}

func TestRepositoryMappingValidates(t *testing.T) {
	data, err := os.ReadFile("../../migration/backfills/account-route-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := decodeMapping(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Mappings) < 20 {
		t.Fatalf("mapping count=%d", len(artifact.Mappings))
	}
}

func TestDecodeMappingRejectsAmbiguousLegacyProfile(t *testing.T) {
	_, err := decodeMapping([]byte(`{
		"schemaVersion":1,
		"mappings":[
			{"venue":"bybit","nativeEnvironment":"demo","legacyProduct":"perp","settlementAsset":"USDT","routeProfileId":"bybit.perp.usdt.demo.v1","routeId":"perp-usdt"},
			{"venue":"bybit","nativeEnvironment":"demo","legacyProduct":"perp","settlementAsset":"USDC","routeProfileId":"bybit.perp.usdc.demo.v1","routeId":"perp-usdc"}
		]
	}`))
	if err == nil || !strings.Contains(err.Error(), "AMBIGUOUS_LEGACY_PROFILE") {
		t.Fatalf("error=%v", err)
	}
}

func TestRunRejectsLiveBeforeExecution(t *testing.T) {
	path := writeMapping(t)
	called := false
	prior := executeBackfill
	executeBackfill = func(context.Context, backfillRequest) (backfillResult, error) {
		called = true
		return backfillResult{}, nil
	}
	t.Cleanup(func() { executeBackfill = prior })
	err := run([]string{"--mapping", path, "--mode", "live", "--target-service", "target", "--target-database", "target_db"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "LIVE_PROVISIONING_DISABLED") || called {
		t.Fatalf("error=%v called=%t", err, called)
	}
}

func TestRunEmitsOnlyCountsAndAccountIDs(t *testing.T) {
	path := writeMapping(t)
	prior := executeBackfill
	executeBackfill = func(_ context.Context, request backfillRequest) (backfillResult, error) {
		if request.TargetService != "target" || request.TargetDatabase != "target_db" || request.Mode != "test" {
			t.Fatalf("request=%+v", request)
		}
		return backfillResult{Accounts: 1, Routes: 2, AccountIDs: []string{"acct_1"}}, nil
	}
	t.Cleanup(func() { executeBackfill = prior })
	var stdout bytes.Buffer
	if err := run([]string{"--mapping", path, "--mode", "test", "--target-service", "target", "--target-database", "target_db"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, want := range []string{`"accounts":1`, `"routes":2`, `"accountIds":["acct_1"]`} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %s: %s", want, output)
		}
	}
	for _, forbidden := range []string{"secretRef", "credential", "provider", "postgres://"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("output exposes %q: %s", forbidden, output)
		}
	}
}

func writeMapping(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account-route-v1.json")
	data := []byte(`{"schemaVersion":1,"mappings":[{"venue":"binance","nativeEnvironment":"demo","legacyProduct":"perp-usdt","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","routeId":"perp-usdt"}]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

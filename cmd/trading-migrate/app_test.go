package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func stubMigrate(t *testing.T, applied result, failure error) *[]string {
	t.Helper()
	calls := []string{}
	original := migrate
	migrate = func(_ context.Context, host, database, role string) (result, error) {
		calls = append(calls, fmt.Sprintf("%s|%s|%s", host, database, role))
		return applied, failure
	}
	t.Cleanup(func() { migrate = original })
	return &calls
}

func TestRunReportsTheAppliedLedger(t *testing.T) {
	calls := stubMigrate(t, result{Database: "bolter_trading_test", SchemaVersion: 1, LedgerDigest: "abc"}, nil)
	var stdout, stderr bytes.Buffer
	err := run([]string{"-database", "bolter_trading_test", "-role", "bolter_trading_test_migrator"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	var decoded result
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != 1 || decoded.LedgerDigest != "abc" || decoded.Database != "bolter_trading_test" {
		t.Fatalf("decoded=%+v", decoded)
	}
	if len(*calls) != 1 || (*calls)[0] != "127.0.0.1:15432|bolter_trading_test|bolter_trading_test_migrator" {
		t.Fatalf("calls=%v", *calls)
	}
}

// The flag values are interpolated into a connection URL, so a value carrying a
// space, a quote or an equals sign could change what the connection means. They
// are rejected before any connection is attempted.
func TestRunRejectsUnsafeIdentities(t *testing.T) {
	for name, args := range map[string][]string{
		"empty database":  {"-database", "", "-role", "bolter_trading_test_migrator"},
		"empty role":      {"-database", "bolter_trading_test", "-role", ""},
		"spaced database": {"-database", "bolter trading", "-role", "bolter_trading_test_migrator"},
		"equals in role":  {"-database", "bolter_trading_test", "-role", "role=other"},
		"quoted role":     {"-database", "bolter_trading_test", "-role", `role'or'1`},
		"long database":   {"-database", strings.Repeat("d", 65), "-role", "bolter_trading_test_migrator"},
		"unsafe host":     {"-database", "bolter_trading_test", "-role", "bolter_trading_test_migrator", "-host", "127.0.0.1:15432 x"},
		"stray argument":  {"-database", "bolter_trading_test", "-role", "bolter_trading_test_migrator", "extra"},
	} {
		calls := stubMigrate(t, result{}, nil)
		var stdout, stderr bytes.Buffer
		err := run(args, &stdout, &stderr)
		if err == nil || err.Error() != "TRADING_MIGRATION_IDENTITY_INVALID" {
			t.Fatalf("%s: err=%v", name, err)
		}
		if len(*calls) != 0 {
			t.Fatalf("%s: connected anyway", name)
		}
		if stdout.Len() != 0 {
			t.Fatalf("%s: wrote output", name)
		}
	}
}

// Failures surface as opaque codes. These modes are about database identity and
// privilege, and a message quoting the connection back would put deployment
// topology into whatever collects stderr.
func TestRunSurfacesOpaqueFailureCodes(t *testing.T) {
	stubMigrate(t, result{}, fmt.Errorf("TRADING_MIGRATION_BLOCKED"))
	var stdout, stderr bytes.Buffer
	err := run([]string{"-database", "bolter_trading_test", "-role", "bolter_trading_test_migrator"}, &stdout, &stderr)
	if err == nil || err.Error() != "TRADING_MIGRATION_BLOCKED" {
		t.Fatalf("err=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

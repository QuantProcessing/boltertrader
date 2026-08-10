package postgres

import (
	"errors"
	"strings"
	"testing"
)

func TestDatabasePoolConfigsUseDistinctStableIdentities(t *testing.T) {
	const dsn = "postgres://user:secret@localhost/funding_trader_cutover_test"

	application, err := ApplicationPoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cutover, err := CutoverPoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if application.ConnConfig.RuntimeParams["application_name"] != ApplicationDBIdentity {
		t.Fatalf("application identity=%q", application.ConnConfig.RuntimeParams["application_name"])
	}
	if cutover.ConnConfig.RuntimeParams["application_name"] != CutoverDBIdentity {
		t.Fatalf("cutover identity=%q", cutover.ConnConfig.RuntimeParams["application_name"])
	}
	if application.ConnConfig.RuntimeParams["application_name"] ==
		cutover.ConnConfig.RuntimeParams["application_name"] {
		t.Fatal("application and cutover identities must differ")
	}
	if cutover.MaxConns != 1 || application.MaxConns == 1 {
		t.Fatalf("pool sizes application=%d cutover=%d", application.MaxConns, cutover.MaxConns)
	}
	if strings.Contains(application.ConnString(), "application_name=") ||
		strings.Contains(cutover.ConnString(), "application_name=") {
		t.Fatal("stable identities must be runtime parameters, not rewritten DSN secrets")
	}
}

func TestCutoverErrorsAreClassifiableAndFailClosed(t *testing.T) {
	for _, target := range []error{
		ErrCutoverIdentity,
		ErrActiveApplicationSessions,
		ErrCutoverBusy,
		ErrAuthoritativeCollision,
		ErrMissingProvenance,
		ErrSchemaDrift,
	} {
		wrapped := cutoverError(target, "safe diagnostic")
		if !errors.Is(wrapped, target) {
			t.Fatalf("%v was not classifiable through wrapping", target)
		}
		if strings.Contains(wrapped.Error(), "postgres://") {
			t.Fatalf("error leaked a database URL: %v", wrapped)
		}
	}
}

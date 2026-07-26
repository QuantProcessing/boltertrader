package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

const redactionCanary = "postgres://canary-user:canary-password@db.internal/canary-detail"

type canaryError struct{}

func (*canaryError) Error() string { return redactionCanary }

func TestRedactErrDoesNotExposeOrUnwrapRawError(t *testing.T) {
	raw := &canaryError{}
	redacted := redactErr("reserve command", raw)

	if strings.Contains(redacted.Error(), redactionCanary) {
		t.Fatalf("redacted error exposed raw canary: %v", redacted)
	}
	if errors.Is(redacted, raw) {
		t.Fatal("redacted error retained raw error in its unwrap chain")
	}
	var leaked *canaryError
	if errors.As(redacted, &leaked) {
		t.Fatal("redacted error allowed errors.As to recover the raw error")
	}
}

func TestRedactErrKeepsOnlySafePostgresClassification(t *testing.T) {
	raw := &pgconn.PgError{
		Severity:      "ERROR",
		Code:          "23505",
		Message:       "duplicate key " + redactionCanary,
		Detail:        "failing row " + redactionCanary,
		Hint:          redactionCanary,
		InternalQuery: redactionCanary,
		Where:         redactionCanary,
	}
	redacted := redactErr("insert idempotency command", raw)

	if !strings.Contains(redacted.Error(), "insert idempotency command") || !strings.Contains(redacted.Error(), "SQLSTATE 23505") {
		t.Fatalf("redacted error lost safe operation or SQLSTATE: %v", redacted)
	}
	if strings.Contains(redacted.Error(), redactionCanary) || strings.Contains(redacted.Error(), raw.Message) {
		t.Fatalf("redacted error exposed PostgreSQL fields: %v", redacted)
	}
	var leaked *pgconn.PgError
	if errors.As(redacted, &leaked) {
		t.Fatal("redacted error allowed errors.As to recover *pgconn.PgError")
	}
}

func TestRedactErrDoesNotExposeConnectErrorConfig(t *testing.T) {
	raw := &pgconn.ConnectError{Config: &pgconn.Config{
		User:     redactionCanary,
		Database: redactionCanary,
	}}
	redacted := redactErr("acquire migration connection", raw)

	var leaked *pgconn.ConnectError
	if errors.As(redacted, &leaked) {
		t.Fatal("redacted error allowed errors.As to recover *pgconn.ConnectError")
	}
	if strings.Contains(redacted.Error(), redactionCanary) {
		t.Fatalf("redacted error exposed connection configuration: %v", redacted)
	}
}

func TestRedactErrPreservesOnlyApprovedCauses(t *testing.T) {
	safeCauses := []error{
		context.Canceled,
		context.DeadlineExceeded,
		ErrConflict,
		ErrNotFound,
		ErrOwnershipUnavailable,
		ErrMigrationMismatch,
	}
	for _, safeCause := range safeCauses {
		t.Run(safeCause.Error(), func(t *testing.T) {
			raw := fmt.Errorf("%s: %w", redactionCanary, safeCause)
			redacted := redactErr("postgres operation", raw)
			if !errors.Is(redacted, safeCause) {
				t.Fatalf("errors.Is(%v) = false", safeCause)
			}
			if errors.Is(redacted, raw) {
				t.Fatal("redacted error retained the raw wrapper")
			}
			if strings.Contains(redacted.Error(), redactionCanary) {
				t.Fatalf("redacted error exposed wrapper canary: %v", redacted)
			}
		})
	}
}

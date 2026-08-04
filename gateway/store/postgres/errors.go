package postgres

import (
	"context"
	"errors"

	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrConflict                     = errors.New("gateway postgres conflict")
	ErrNotFound                     = store.ErrNotFound
	ErrDesiredStateRevisionConflict = store.ErrDesiredStateRevisionConflict
	ErrStaleRuntimeGeneration       = store.ErrStaleRuntimeGeneration
	ErrInvalidAccountTransition     = store.ErrInvalidAccountTransition
	ErrOwnershipUnavailable         = errors.New("gateway postgres ownership unavailable")
	ErrMigrationMismatch            = errors.New("gateway postgres migration mismatch")
)

func redactErr(operation string, err error) error {
	if err == nil {
		return nil
	}
	class, sqlState := classifyError(err)
	return newRedactedError(operation, class, sqlState, approvedCause(err))
}

type redactedError struct {
	operation string
	class     string
	sqlState  string
	cause     error
}

func newRedactedError(operation, class, sqlState string, cause error) error {
	return &redactedError{
		operation: operation,
		class:     class,
		sqlState:  sqlState,
		cause:     cause,
	}
}

func (err *redactedError) Error() string {
	message := err.operation + ": " + err.class
	if err.sqlState != "" {
		message += " (SQLSTATE " + err.sqlState + ")"
	}
	return message
}

func (err *redactedError) Unwrap() error {
	return err.cause
}

func classifyError(err error) (string, string) {
	switch {
	case errors.Is(err, context.Canceled):
		return "operation canceled", ""
	case errors.Is(err, context.DeadlineExceeded):
		return "operation deadline exceeded", ""
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "postgres error", safeSQLState(pgErr.Code)
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return "postgres connection error", ""
	}
	return "postgres error", ""
}

func approvedCause(err error) error {
	approved := [...]error{
		context.Canceled,
		context.DeadlineExceeded,
		ErrConflict,
		ErrNotFound,
		ErrDesiredStateRevisionConflict,
		ErrStaleRuntimeGeneration,
		ErrInvalidAccountTransition,
		ErrOwnershipUnavailable,
		ErrMigrationMismatch,
	}
	for _, candidate := range approved {
		if errors.Is(err, candidate) {
			return candidate
		}
	}
	return nil
}

func safeSQLState(code string) string {
	if len(code) != 5 {
		return ""
	}
	for _, char := range code {
		if (char < '0' || char > '9') && (char < 'A' || char > 'Z') {
			return ""
		}
	}
	return code
}

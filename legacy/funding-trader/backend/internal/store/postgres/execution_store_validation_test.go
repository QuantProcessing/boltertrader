package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/shopspring/decimal"
)

func TestExecutionStoreValidationFailsClosedOnScopeMismatch(t *testing.T) {
	store := NewExecutionStore(nil, "")
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	mismatched := testExecution(t, readmodel.Scope{PrincipalID: "principal-b", Mode: "test"}, "exec-scope-mismatch").Snapshot()

	if _, err := store.ReserveExecution(context.Background(), scope, "POST", "/api/executions", "idem", "hash", mismatched); !errors.Is(err, ErrExecutionScopeConflict) {
		t.Fatalf("reserve scope mismatch err=%v", err)
	}
	if _, err := store.PersistTransition(context.Background(), PersistTransitionInput{
		Scope: scope, Execution: mismatched, ExpectedVersion: 1,
		Kind: "scope_mismatch", RequestID: "req-scope", Payload: json.RawMessage(`{"safe":true}`),
	}); !errors.Is(err, ErrExecutionScopeConflict) {
		t.Fatalf("transition scope mismatch err=%v", err)
	}
}

func TestExecutionStoreValidationRejectsNativeEnvironmentAsProductMode(t *testing.T) {
	store := NewExecutionStore(nil, "")
	for _, mode := range []string{"", "demo", "testnet", "mainnet"} {
		scope := readmodel.Scope{PrincipalID: "principal-a", Mode: mode}
		if _, err := store.GetExecution(context.Background(), scope, "exec-1"); err == nil {
			t.Fatalf("mode=%q unexpectedly accepted", mode)
		}
	}
}

func TestExecutionStoreValidationRejectsMissingFillID(t *testing.T) {
	store := NewExecutionStore(nil, "")
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	exec := testExecution(t, scope, "exec-missing-fill")
	if err := exec.Apply(execution.Event{Type: execution.EventBeginSubmit, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{Type: execution.EventLegAccepted, Role: execution.LegRoleLong, GatewayID: "gw-1", OrderID: "order-1", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{
		Type: execution.EventLegPartial, GatewayID: "gw-1", GatewayFillID: "gateway-fill",
		Quantity: decimal.RequireFromString("2"), Price: decimal.RequireFromString("100"),
		Fee: execution.Money{Amount: decimal.RequireFromString("0.1"), Currency: "USDT"}, At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PersistTransition(context.Background(), PersistTransitionInput{
		Scope: scope, Execution: exec.Snapshot(), ExpectedVersion: 1,
		Kind: "missing_fill", RequestID: "req-fill", Payload: json.RawMessage(`{"safe":true}`),
	}); !errors.Is(err, ErrExecutionValidation) {
		t.Fatalf("missing fill id err=%v", err)
	}
}

func TestScanStoredExecutionUsesLatestPersistedTimestamp(t *testing.T) {
	payloadAt := time.Date(2026, 8, 3, 5, 35, 49, 170000000, time.UTC)
	rowAt := time.Date(2026, 8, 3, 5, 39, 0, 216496000, time.UTC)
	payload, err := json.Marshal(execution.Execution{ID: "exec-1", UpdatedAt: payloadAt})
	if err != nil {
		t.Fatalf("marshal execution: %v", err)
	}

	for _, test := range []struct {
		name string
		row  time.Time
		want time.Time
	}{
		{name: "row timestamp repairs older payload", row: rowAt, want: rowAt},
		{name: "older row does not regress payload", row: payloadAt.Add(-time.Second), want: payloadAt},
	} {
		t.Run(test.name, func(t *testing.T) {
			stored, scanErr := scanStoredExecution(storedExecutionRow{
				id: "exec-1", status: string(execution.StateClosed),
				payload: payload, version: 2, updatedAt: test.row,
			})
			if scanErr != nil {
				t.Fatalf("scanStoredExecution: %v", scanErr)
			}
			if !stored.Execution.UpdatedAt.Equal(test.want) {
				t.Fatalf("execution updatedAt = %v, want %v", stored.Execution.UpdatedAt, test.want)
			}
		})
	}
}

type storedExecutionRow struct {
	id        string
	status    string
	payload   json.RawMessage
	version   int64
	updatedAt time.Time
}

func (row storedExecutionRow) Scan(dest ...any) error {
	if len(dest) != 5 {
		return fmt.Errorf("scan destination count = %d, want 5", len(dest))
	}
	*(dest[0].(*string)) = row.id
	*(dest[1].(*string)) = row.status
	*(dest[2].(*json.RawMessage)) = append(json.RawMessage(nil), row.payload...)
	*(dest[3].(*int64)) = row.version
	*(dest[4].(*time.Time)) = row.updatedAt
	return nil
}

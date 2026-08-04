package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestServiceStoresStructuredRedactedRecords(t *testing.T) {
	store := NewMemoryStore()
	service, err := New(store, func() time.Time { return time.Date(2026, 8, 5, 3, 0, 0, 0, time.UTC) }, func() string { return "audit_1" })
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Record(context.Background(), Entry{PrincipalID: "browser-session", Action: "account.rotate", ResourceType: "account", ResourceID: "acct_1", Outcome: "accepted", RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "audit_1" || record.OccurredAt.IsZero() {
		t.Fatalf("record = %#v", record)
	}
	rows, err := service.List(context.Background(), Filter{ResourceID: "acct_1", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("List = %#v, %v", rows, err)
	}
	body, _ := json.Marshal(rows)
	if strings.Contains(string(body), "secretMaterial") || strings.Contains(string(body), "secretRef") {
		t.Fatalf("audit schema admitted sensitive fields: %s", body)
	}
}

package store

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

func TestAuditRecordRequiresStableSafeIdentity(t *testing.T) {
	valid := AuditRecord{
		PrincipalID:   "principal-a",
		Action:        AuditCancelOrder,
		ResourceID:    "018f0000-0000-7000-8000-000000000002",
		Outcome:       AuditRequested,
		CorrelationID: "018f0000-0000-7000-8000-000000000003",
		AccountID:     "acct-a",
		InstrumentID:  "binance:live:spot:BTC-USDT",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid audit record: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*AuditRecord)
	}{
		{"principal", func(record *AuditRecord) { record.PrincipalID = "" }},
		{"action", func(record *AuditRecord) { record.Action = "raw_native_action" }},
		{"resource", func(record *AuditRecord) { record.ResourceID = "" }},
		{"outcome", func(record *AuditRecord) { record.Outcome = "raw_native_outcome" }},
		{"correlation", func(record *AuditRecord) { record.CorrelationID = "" }},
		{"account", func(record *AuditRecord) { record.AccountID = domain.AccountID("bad\naccount") }},
		{"instrument", func(record *AuditRecord) { record.InstrumentID = domain.InstrumentID("bad instrument") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			test.mutate(&record)
			if err := record.Validate(); err == nil {
				t.Fatalf("invalid audit record accepted: %+v", record)
			}
		})
	}
}

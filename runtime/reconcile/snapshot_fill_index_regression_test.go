package reconcile

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestFillPresenceIndexPreservesScopeAndTypedAliasMatching(t *testing.T) {
	fill := model.Fill{
		AccountID: "acct-a", InstrumentID: btc, ClientID: "client-a", VenueOrderID: "venue-a",
	}
	index := newFillPresenceIndex(map[string][]model.FillReport{
		"fixture": {{Fill: fill}},
	})

	matching := order("client-a", btc, "1", enums.StatusNew)
	matching.Request.AccountID = "acct-a"
	matching.VenueOrderID = "venue-a"
	if !index.hasOrder(matching) {
		t.Fatal("exact account/instrument/client alias did not match")
	}
	venueOnly := matching
	venueOnly.Request.ClientID = ""
	if !index.hasOrder(venueOnly) {
		t.Fatal("exact venue alias did not match")
	}
	differentAccount := matching
	differentAccount.Request.AccountID = "acct-b"
	if index.hasOrder(differentAccount) {
		t.Fatal("different non-empty account matched")
	}
	differentInstrument := matching
	differentInstrument.Request.InstrumentID = eth
	if index.hasOrder(differentInstrument) {
		t.Fatal("different non-empty instrument matched")
	}
	unscopedOrder := matching
	unscopedOrder.Request.AccountID = ""
	unscopedOrder.Request.InstrumentID = model.InstrumentID{}
	if !index.hasOrder(unscopedOrder) {
		t.Fatal("empty order scope should match the alias across scopes")
	}

	unscopedFill := fill
	unscopedFill.AccountID = ""
	unscopedFill.InstrumentID = model.InstrumentID{}
	unscopedFill.ClientID = "unscoped-client"
	unscopedFill.VenueOrderID = ""
	index.add(unscopedFill)
	anyScopedOrder := order("unscoped-client", eth, "1", enums.StatusNew)
	anyScopedOrder.Request.AccountID = "acct-z"
	if !index.hasOrder(anyScopedOrder) {
		t.Fatal("empty fill scope should match a scoped order alias")
	}
}

func TestInferMissingSnapshotFillsUsesBoundedPresenceIndexForLargeReport(t *testing.T) {
	const count = 1000
	at := time.Unix(500, 0)
	mass := model.NewExecutionMassStatus("T", "acct", at)
	snapshots := make([]orderReportSnapshot, 0, count)
	for i := 0; i < count; i++ {
		clientID := fmt.Sprintf("indexed-client-%04d", i)
		venueOrderID := fmt.Sprintf("indexed-venue-%04d", i)
		current := order(clientID, btc, "1", enums.StatusFilled)
		current.Request.AccountID = "acct"
		current.Request.Side = enums.SideBuy
		current.VenueOrderID = venueOrderID
		current.FilledQty = d("1")
		current.AvgFillPrice = d("100")
		baseline := current
		baseline.FilledQty = d("0")
		snapshots = append(snapshots, orderReportSnapshot{order: current, baseline: baseline})
		fill := model.Fill{
			AccountID: "acct", InstrumentID: btc, ClientID: clientID, VenueOrderID: venueOrderID,
			TradeID: fmt.Sprintf("indexed-trade-%04d", i), Side: enums.SideBuy,
			Price: d("100"), Quantity: d("1"), Timestamp: at,
		}
		if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
			t.Fatalf("add fill %d: %v", i, err)
		}
	}

	if err := New(nil, nil, nil).inferMissingSnapshotFills(PassHeader{StableEventAt: at}, mass, snapshots); err != nil {
		t.Fatalf("infer missing fills: %v", err)
	}
	retained := 0
	for _, reports := range mass.FillReports {
		retained += len(reports)
	}
	if retained != count {
		t.Fatalf("fill reports=%d, want %d existing reports and no inferred duplicates", retained, count)
	}
}

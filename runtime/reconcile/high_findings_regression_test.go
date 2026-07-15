package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
)

func TestPositionMismatchCreatesFindingNotOverwrite(t *testing.T) {
	t.Run("different authoritative quantity", func(t *testing.T) {
		c := cache.New()
		c.UpsertPosition(model.Position{
			AccountID: "acct", InstrumentID: btc, Side: enums.PosNet, Quantity: d("1"),
		})
		mass := model.NewExecutionMassStatus("T", "acct", time.Unix(100, 0))
		if err := mass.AddPositionReport(model.PositionReport{
			Venue: "T", AccountID: "acct",
			Position: model.Position{
				AccountID: "acct", InstrumentID: btc, Side: enums.PosNet, Quantity: d("2.5"),
			},
			ReportedAt: time.Unix(100, 0),
		}); err != nil {
			t.Fatalf("add position report: %v", err)
		}
		exec := &snapshotExec{mass: mass, positions: true}

		rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(exec.queries) != 1 || !exec.queries[0].IncludePositions {
			t.Fatalf("queries=%+v, want IncludePositions", exec.queries)
		}
		if got, ok := c.PositionForAccount("acct", btc, enums.PosNet); !ok || !got.Quantity.Equal(d("1")) {
			t.Fatalf("cache position=%+v ok=%v, want original quantity 1", got, ok)
		}
		if rep.PositionOverwrites != 0 || rep.PositionsUpdated != 0 || rep.PositionsCleared != 0 {
			t.Fatalf("report=%+v, position report must not mutate cache directly", rep)
		}
		if !hasFindingCode(rep.Findings, "POSITION_MISMATCH") {
			t.Fatalf("findings=%+v, want POSITION_MISMATCH", rep.Findings)
		}
		if verdict := rep.ActivationVerdict(); verdict.Safe {
			t.Fatalf("activation verdict=%+v, want fail-closed", verdict)
		}
	})

	t.Run("authoritative flat does not clear local position", func(t *testing.T) {
		c := cache.New()
		c.UpsertPosition(model.Position{
			AccountID: "acct", InstrumentID: eth, Side: enums.PosNet, Quantity: d("3"),
		})
		exec := &snapshotExec{
			mass:      model.NewExecutionMassStatus("T", "acct", time.Unix(101, 0)),
			positions: true,
		}

		rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if got, ok := c.PositionForAccount("acct", eth, enums.PosNet); !ok || !got.Quantity.Equal(d("3")) {
			t.Fatalf("cache position=%+v ok=%v, want original quantity 3", got, ok)
		}
		if !hasFindingCode(rep.Findings, "POSITION_MISMATCH") || rep.ActivationVerdict().Safe {
			t.Fatalf("report=%+v, want blocking flat-position mismatch", rep)
		}
	})

	t.Run("matching report remains safe", func(t *testing.T) {
		c := cache.New()
		c.UpsertPosition(model.Position{
			AccountID: "acct", InstrumentID: btc, Side: enums.PosNet, Quantity: d("1"),
		})
		mass := model.NewExecutionMassStatus("T", "acct", time.Unix(102, 0))
		if err := mass.AddPositionReport(model.PositionReport{
			Venue: "T", AccountID: "acct",
			Position: model.Position{
				AccountID: "acct", InstrumentID: btc, Side: enums.PosNet, Quantity: d("1"),
			},
			ReportedAt: time.Unix(102, 0),
		}); err != nil {
			t.Fatalf("add position report: %v", err)
		}

		rep, err := New(nil, &snapshotExec{mass: mass, positions: true}, c).
			WithAccountID("acct").
			Run(context.Background())
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(rep.Findings) != 0 || !rep.ActivationVerdict().Safe {
			t.Fatalf("report=%+v, matching position evidence should remain safe", rep)
		}
	})
}

func TestCumulativeOrderProgressWithoutFillReportsUsesRecoveredFillPath(t *testing.T) {
	c := cache.New()
	known := order("progress-gap", btc, "2", enums.StatusNew)
	known.Request.AccountID = "acct"
	known.Request.Side = enums.SideBuy
	c.UpsertOrder(known)

	snapshot := known
	snapshot.Status = enums.StatusPartiallyFilled
	snapshot.FilledQty = d("1")
	snapshot.AvgFillPrice = d("100")
	snapshot.UpdatedAt = time.Unix(200, 0)
	mass := model.NewExecutionMassStatus("T", "acct", time.Unix(200, 0))
	if err := mass.AddOrderReport(model.OrderStatusReport{
		Venue: "T", AccountID: "acct", Order: snapshot, ReportedAt: time.Unix(200, 0),
	}); err != nil {
		t.Fatalf("add order report: %v", err)
	}

	var applied []model.Fill
	rep, err := New(nil, &snapshotExec{mass: mass, fillHistory: true}, c).
		WithAccountID("acct").
		WithFillApplier(func(fill model.Fill, _ contract.EventMeta) FillApplyResult {
			applied = append(applied, fill)
			return FillApplyApplied
		}).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(applied) != 1 || !applied[0].Quantity.Equal(d("1")) || !applied[0].Price.Equal(d("100")) {
		t.Fatalf("applied fills=%+v, want one inferred fill qty=1 price=100", applied)
	}
	if rep.FillsApplied != 1 || rep.FillsInferred != 1 {
		t.Fatalf("report=%+v, want one inferred applied fill", rep)
	}
	if verdict := rep.ActivationVerdict(); !verdict.Safe {
		t.Fatalf("activation verdict=%+v, inferred fill closes progress gap", verdict)
	}
}

func TestCumulativeOrderProgressWithoutDerivablePriceBlocksActivation(t *testing.T) {
	c := cache.New()
	known := order("unpriced-progress-gap", btc, "2", enums.StatusNew)
	known.Request.AccountID = "acct"
	c.UpsertOrder(known)

	snapshot := known
	snapshot.Status = enums.StatusPartiallyFilled
	snapshot.FilledQty = d("1")
	mass := model.NewExecutionMassStatus("T", "acct", time.Unix(201, 0))
	if err := mass.AddOrderReport(model.OrderStatusReport{
		Venue: "T", AccountID: "acct", Order: snapshot, ReportedAt: time.Unix(201, 0),
	}); err != nil {
		t.Fatalf("add order report: %v", err)
	}

	rep, err := New(nil, &snapshotExec{mass: mass, fillHistory: true}, c).
		WithAccountID("acct").
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !hasFindingCode(rep.Findings, "ORDER_PROGRESS_WITHOUT_FILL") {
		t.Fatalf("findings=%+v, want ORDER_PROGRESS_WITHOUT_FILL", rep.Findings)
	}
	if verdict := rep.ActivationVerdict(); verdict.Safe {
		t.Fatalf("activation verdict=%+v, want fail-closed", verdict)
	}
}

func TestZeroGeneratedAtAnonymousFillUsesStableIdentity(t *testing.T) {
	at := time.Unix(300, 0)
	c := cache.New()
	known := order("anonymous-fill", btc, "2", enums.StatusNew)
	known.Request.AccountID = "acct"
	known.Request.Side = enums.SideBuy
	c.UpsertOrder(known)

	mass := model.NewExecutionMassStatus("T", "acct", time.Time{})
	if err := mass.AddFillReport(model.FillReport{
		Venue: "T", AccountID: "acct",
		Fill: model.Fill{
			AccountID: "acct", InstrumentID: btc, ClientID: known.Request.ClientID,
			VenueOrderID: known.VenueOrderID, Side: enums.SideBuy,
			Price: d("100"), Quantity: d("1"), Timestamp: at,
		},
	}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}

	var tradeIDs []string
	r := New(nil, &snapshotExec{mass: mass, fillHistory: true}, c).
		WithAccountID("acct").
		WithClock(clock.NewSimulatedClock(at)).
		WithFillApplier(func(fill model.Fill, _ contract.EventMeta) FillApplyResult {
			tradeIDs = append(tradeIDs, fill.TradeID)
			return FillApplyApplied
		})
	first, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(tradeIDs) != 1 || tradeIDs[0] == "" {
		t.Fatalf("applied trade ids=%q, want one stable synthetic identity", tradeIDs)
	}
	if first.FillsApplied != 1 || second.FillsApplied != 0 || second.FillsDuplicate != 1 {
		t.Fatalf("first=%+v second=%+v, want apply once then dedupe", first, second)
	}
}

func hasFindingCode(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

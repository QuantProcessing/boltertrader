package accounting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

func inventoryFixture() (orders.Command, FillFact) {
	command := orders.Command{PrincipalID: "p", AccountID: "a", CommandID: "c", ProductID: "manual", LineageID: "l"}
	fill := FillFact{FillID: "buy", CommandID: "c", AccountID: "a", InstrumentID: "venue:live:spot:BTC-USDT", InstrumentProduct: "spot", BaseAsset: "BTC", Side: portfolio.SideBuy, Quantity: "1", Price: "100", Fee: "0.001", FeeCurrency: "BTC", SettlementCurrency: "USDT", OccurredAt: time.Now().UTC()}
	return command, fill
}

func TestSpotBaseFeeChangesInventoryAndCloseUsesNetQuantity(t *testing.T) {
	ctx := context.Background()
	projection, book := portfolio.NewProjection(), ledger.NewService(ledger.NewMemoryStore(), time.Now)
	command, open := inventoryFixture()
	if err := RecordFills(ctx, projection, book, command, []FillFact{open, open}); err != nil {
		t.Fatal(err)
	}
	positions := projection.List("p", "a", "manual")
	if len(positions) != 1 || positions[0].Quantity != "0.999" || positions[0].AverageEntryPrice != "100" {
		t.Fatalf("inventory=%+v", positions)
	}
	close := open
	close.FillID, close.Side, close.Quantity, close.Price, close.Fee, close.FeeCurrency = "sell", portfolio.SideSell, "0.999", "110", "0.05", "USDT"
	if err := RecordFills(ctx, projection, book, command, []FillFact{close, close}); err != nil {
		t.Fatal(err)
	}
	positions = projection.List("p", "a", "manual")
	if positions[0].Quantity != "0" || positions[0].RealizedPnL != "9.99" {
		t.Fatalf("closed inventory=%+v", positions)
	}
	snapshot, err := book.PnL(ctx, ledger.Query{PrincipalID: "p", ProductID: "manual", LineageID: "l", AccountID: "a", InstrumentID: open.InstrumentID})
	if err != nil || len(snapshot.ByCurrency) != 2 {
		t.Fatalf("pnl=%+v err=%v", snapshot, err)
	}
	if snapshot.ByCurrency[0].Currency != "BTC" || snapshot.ByCurrency[0].BookedNetPnL != "-0.001" || snapshot.ByCurrency[1].Currency != "USDT" || snapshot.ByCurrency[1].BookedNetPnL != "9.94" || snapshot.ByCurrency[1].NetPnL != "" {
		t.Fatalf("native pnl=%+v", snapshot)
	}
	entries, _ := book.List(ctx, ledger.Query{PrincipalID: "p"})
	if len(entries) != 3 {
		t.Fatalf("duplicate ledger rows=%+v", entries)
	}
}

func TestInventoryDistinguishesRebateThirdTokenAndCollateralFee(t *testing.T) {
	for _, test := range []struct{ name, product, currency, fee, quantity string }{
		{"base rebate", "spot", "BTC", "-0.001", "1.001"},
		{"third token cost", "spot", "BNB", "0.01", "1"},
		{"collateral cost", "perp", "BTC", "0.001", "1"},
		{"zero fee without currency", "spot", "", "0", "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection, book := portfolio.NewProjection(), ledger.NewService(ledger.NewMemoryStore(), time.Now)
			command, fill := inventoryFixture()
			fill.InstrumentProduct, fill.FeeCurrency, fill.Fee = test.product, test.currency, test.fee
			if err := RecordFills(context.Background(), projection, book, command, []FillFact{fill}); err != nil {
				t.Fatal(err)
			}
			if positions := projection.List("p", "a", "manual"); len(positions) != 1 || positions[0].Quantity != test.quantity {
				t.Fatalf("positions=%+v", positions)
			}
			entries, _ := book.List(context.Background(), ledger.Query{PrincipalID: "p"})
			if test.currency == "" {
				if len(entries) != 0 {
					t.Fatal("invented a currency for zero cost")
				}
				return
			}
			if len(entries) != 1 || entries[0].Amount != test.fee || entries[0].Currency != test.currency {
				t.Fatalf("fee facts=%+v", entries)
			}
		})
	}
}

func TestPayingBaseFeeRealizesItsInventoryGainWithoutCapitalizingCostTwice(t *testing.T) {
	projection, book := portfolio.NewProjection(), ledger.NewService(ledger.NewMemoryStore(), time.Now)
	command, fill := inventoryFixture()
	fill.Quantity, fill.Price, fill.Fee = "1.01", "80", "0"
	if err := RecordFills(context.Background(), projection, book, command, []FillFact{fill}); err != nil {
		t.Fatal(err)
	}
	fill.FillID, fill.Side, fill.Quantity, fill.Price, fill.Fee = "sell", portfolio.SideSell, "1", "100", "0.01"
	if err := RecordFills(context.Background(), projection, book, command, []FillFact{fill}); err != nil {
		t.Fatal(err)
	}
	positions := projection.List("p", "a", "manual")
	// Trade gain 20 + fee asset disposal gain 0.2, with the 0.01 BTC cost
	// recorded separately. At the fill's 100 price this is 19.2 net, matching
	// 100 sale proceeds minus the original 80.8 cost.
	if positions[0].Quantity != "0" || positions[0].RealizedPnL != "20.2" {
		t.Fatalf("inventory=%+v", positions)
	}
}

type interruptedLedger struct {
	ledger.Store
	fail bool
}

func (store *interruptedLedger) Append(ctx context.Context, entry ledger.Entry) error {
	if store.fail {
		return errors.New("simulated ledger write failure")
	}
	return store.Store.Append(ctx, entry)
}
func TestReplayAfterLedgerFailureDoesNotDeductBaseFeeTwice(t *testing.T) {
	projection := portfolio.NewProjection()
	store := &interruptedLedger{Store: ledger.NewMemoryStore(), fail: true}
	command, fill := inventoryFixture()
	if err := RecordFills(context.Background(), projection, ledger.NewService(store, time.Now), command, []FillFact{fill}); err == nil {
		t.Fatal("injection failed")
	}
	store.fail = false
	if err := RecordFills(context.Background(), projection, ledger.NewService(store, time.Now), command, []FillFact{fill}); err != nil {
		t.Fatal(err)
	}
	if positions := projection.List("p", "a", "manual"); positions[0].Quantity != "0.999" {
		t.Fatalf("replayed inventory=%+v", positions)
	}
}

func TestUnknownNonSettlementFeeCannotInventInventory(t *testing.T) {
	command, fill := inventoryFixture()
	fill.InstrumentProduct, fill.BaseAsset = "", ""
	projection := portfolio.NewProjection()
	if err := RecordFills(context.Background(), projection, ledger.NewService(ledger.NewMemoryStore(), time.Now), command, []FillFact{fill}); err == nil {
		t.Fatal("missing instrument facts accepted")
	}
	if len(projection.List("p", "a", "manual")) != 0 {
		t.Fatal("unknown inventory was recorded")
	}
}

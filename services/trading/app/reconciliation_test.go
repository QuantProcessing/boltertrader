package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/arbitrage"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

// acceptCommand puts one command on a service's own ledger so a later
// reconciliation has something to land on.
func acceptCommand(t *testing.T, commands *orders.Service, productID, kind string) orders.Command {
	t.Helper()
	command, _, err := commands.Create(context.Background(), orders.CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: productID,
		LineageID: "lineage-" + productID, IdempotencyKey: "idem-" + productID,
		Method: "POST", Path: "/api/v1/products/" + productID + "/commands",
		CommandKind: kind, Mode: orders.ModeTest, Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.State != orders.StateAccepted {
		t.Fatalf("seeded command state=%s", command.State)
	}
	return command
}

func newProductServices(t *testing.T) (*manual.Service, *orders.Service, *arbitrage.Service, *orders.Service) {
	t.Helper()
	// Deliberately separate stores. Both products answer a non-authoritative
	// outcome with the same sentence, so the only way to observe which service a
	// command actually reached is to see which ledger moved.
	manualCommands := orders.NewService(orders.NewMemoryStore(), nil, time.Now)
	arbitrageCommands := orders.NewService(orders.NewMemoryStore(), nil, time.Now)
	manualService := manual.NewGatewayService(manualCommands, portfolio.NewProjection(), ledger.NewService(ledger.NewMemoryStore(), time.Now), nil, time.Now)
	arbitrageService := arbitrage.NewGatewayServiceWithPreviewKey(arbitrageCommands, portfolio.NewProjection(), ledger.NewService(ledger.NewMemoryStore(), time.Now), nil, time.Now, nil)
	return manualService, manualCommands, arbitrageService, arbitrageCommands
}

// TestArbitrageReconciliationDispatch covers the branch arbitrage added to the
// dispatcher, and the one it must not have loosened.
//
// The two products share a command shape, so a reconciliation handed to the
// wrong service would still decode and still look plausible — it would simply
// write arbitrage's fills into manual's ledger. Routing is therefore asserted
// positively, by which product's command ledger moves, rather than by an error
// string the two services happen to share.
func TestArbitrageReconciliationDispatch(t *testing.T) {
	ctx := context.Background()
	rejected := orders.HandoffResult{Outcome: orders.HandoffRejected}

	t.Run("routes each product to its own ledger", func(t *testing.T) {
		manualService, manualCommands, arbitrageService, arbitrageCommands := newProductServices(t)
		dispatcher := reconciliationDispatcher{manual: manualService, arbitrage: arbitrageService}
		manualCommand := acceptCommand(t, manualCommands, "manual", "manual.place")
		arbitrageCommand := acceptCommand(t, arbitrageCommands, arbitrage.ProductID, arbitrage.CommandKindPlace)

		// A command routed to the wrong service would fail to resolve against
		// that service's store, so reaching StateFailed is itself the proof.
		updated, err := dispatcher.RecordReconciliation(ctx, arbitrageCommand, rejected)
		if err != nil {
			t.Fatalf("arbitrage reconciliation: %v", err)
		}
		if updated.State != orders.StateFailed || updated.CommandID != arbitrageCommand.CommandID {
			t.Fatalf("arbitrage command=%+v", updated)
		}

		untouched, err := manualCommands.Get(ctx, manualCommand.CommandID)
		if err != nil {
			t.Fatal(err)
		}
		if untouched.State != orders.StateAccepted {
			t.Fatalf("the manual ledger moved on an arbitrage reconciliation: %+v", untouched)
		}

		updated, err = dispatcher.RecordReconciliation(ctx, manualCommand, rejected)
		if err != nil {
			t.Fatalf("manual reconciliation: %v", err)
		}
		if updated.State != orders.StateFailed || updated.CommandID != manualCommand.CommandID {
			t.Fatalf("manual command=%+v", updated)
		}
	})

	t.Run("refuses a product it cannot reach", func(t *testing.T) {
		manualService, manualCommands, arbitrageService, arbitrageCommands := newProductServices(t)
		manualCommand := acceptCommand(t, manualCommands, "manual", "manual.place")
		arbitrageCommand := acceptCommand(t, arbitrageCommands, arbitrage.ProductID, arbitrage.CommandKindPlace)
		unknown := arbitrageCommand
		unknown.ProductID = "funding"

		// Dropping a reconciliation silently would leave the ledger permanently
		// disagreeing with the Gateway, so every unreachable route is an error —
		// including a product that was configured away.
		cases := []struct {
			name       string
			dispatcher reconciliationDispatcher
			command    orders.Command
		}{
			{"arbitrage absent", reconciliationDispatcher{manual: manualService}, arbitrageCommand},
			{"manual absent", reconciliationDispatcher{arbitrage: arbitrageService}, manualCommand},
			{"product unknown", reconciliationDispatcher{manual: manualService, arbitrage: arbitrageService}, unknown},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				returned, err := testCase.dispatcher.RecordReconciliation(ctx, testCase.command, rejected)
				if err == nil || !strings.Contains(err.Error(), "unsupported reconciliation product") {
					t.Fatalf("err=%v", err)
				}
				if returned.CommandID != testCase.command.CommandID || returned.State != orders.StateAccepted {
					t.Fatalf("a refused reconciliation must not mutate the command: %+v", returned)
				}
			})
		}

		// Nothing above may have moved either ledger.
		for _, seeded := range []struct {
			commands *orders.Service
			command  orders.Command
		}{{manualCommands, manualCommand}, {arbitrageCommands, arbitrageCommand}} {
			current, err := seeded.commands.Get(ctx, seeded.command.CommandID)
			if err != nil {
				t.Fatal(err)
			}
			if current.State != orders.StateAccepted {
				t.Fatalf("a refused reconciliation reached a ledger: %+v", current)
			}
		}
	})
}

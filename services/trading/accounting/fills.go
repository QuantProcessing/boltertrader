// Package accounting books the same confirmed fill facts for every product.
// It does not dispatch, reconcile, or infer venue facts from order requests.
package accounting

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/shopspring/decimal"
)

type FillFact struct {
	FillID, CommandID, AccountID, InstrumentID            string
	InstrumentProduct, BaseAsset                          string
	Side                                                  portfolio.Side
	Quantity, Price, Fee, FeeCurrency, SettlementCurrency string
	OccurredAt                                            time.Time
}

func ValidateFill(fill FillFact) error {
	quantity, qErr := decimal.NewFromString(fill.Quantity)
	price, pErr := decimal.NewFromString(fill.Price)
	fee, feeErr := decimal.NewFromString(fill.Fee)
	if fill.FillID == "" || (!fee.IsZero() && fill.FeeCurrency == "") || fill.SettlementCurrency == "" || fill.OccurredAt.IsZero() || qErr != nil || pErr != nil || feeErr != nil || !quantity.IsPositive() || !price.IsPositive() {
		return fmt.Errorf("fill facts are incomplete or invalid")
	}
	_, err := portfolio.QuantityDelta(portfolio.Fill{Quantity: fill.Quantity, Side: fill.Side, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency, SettlementCurrency: fill.SettlementCurrency, InstrumentProduct: fill.InstrumentProduct, BaseAsset: fill.BaseAsset})
	return err
}

// RecordFills is retryable by scoped fill identity. If ledger persistence fails
// after a position update, ApplyFill returns its original result on replay;
// the remaining ledger writes are then completed without applying quantity twice.
func RecordFills(ctx context.Context, projection *portfolio.Projection, book *ledger.Service, command orders.Command, fills []FillFact) error {
	for _, fact := range fills {
		_, result, err := projection.ApplyFill(ctx, portfolio.Fill{
			PrincipalID: command.PrincipalID, FillID: fact.FillID, CommandID: fact.CommandID, LineageID: command.LineageID, ProductID: command.ProductID,
			AccountID: fact.AccountID, InstrumentID: fact.InstrumentID, Side: fact.Side, InstrumentProduct: fact.InstrumentProduct, BaseAsset: fact.BaseAsset,
			Quantity: fact.Quantity, Price: fact.Price, Fee: fact.Fee, FeeCurrency: fact.FeeCurrency, SettlementCurrency: fact.SettlementCurrency, OccurredAt: fact.OccurredAt,
		})
		if err != nil {
			return fmt.Errorf("apply confirmed fill: %w", err)
		}
		if fact.FeeCurrency != "" {
			if err := book.Record(ctx, ledger.Entry{
				PrincipalID: command.PrincipalID, EntryID: fact.FillID + ":fee", LineageID: command.LineageID, ProductID: command.ProductID,
				AccountID: fact.AccountID, InstrumentID: fact.InstrumentID, Kind: ledger.KindFee, Amount: result.Fee, Currency: fact.FeeCurrency, OccurredAt: fact.OccurredAt,
			}); err != nil {
				return fmt.Errorf("record fill fee: %w", err)
			}
		}
		if result.RealizedPnL != "0" {
			if err := book.Record(ctx, ledger.Entry{
				PrincipalID: command.PrincipalID, EntryID: fact.FillID + ":realized_pnl", LineageID: command.LineageID, ProductID: command.ProductID,
				AccountID: fact.AccountID, InstrumentID: fact.InstrumentID, Kind: ledger.KindRealizedPnL, Amount: result.RealizedPnL, Currency: fact.SettlementCurrency, OccurredAt: fact.OccurredAt,
			}); err != nil {
				return fmt.Errorf("record fill realized PnL: %w", err)
			}
		}
	}
	return nil
}

package accounting

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
)

type LedgerSink interface {
	Record(context.Context, ledger.Entry) error
	SetUnrealized(context.Context, ledger.Unrealized) error
}

type FundingSupersessionSink interface {
	SupersedeFunding(context.Context, string, ledger.Entry) error
}

type PortfolioSink interface {
	ApplyFill(context.Context, portfolio.Fill) (portfolio.Position, portfolio.FillResult, error)
	ApplyMarkContext(context.Context, string, portfolio.Mode, string, string, string, string, string, time.Time) (portfolio.Position, error)
}

// PlatformAdapter translates normalized Funding facts into existing shared
// Platform contracts. Persistence remains owned by the injected Platform sinks.
type PlatformAdapter struct {
	ledger    LedgerSink
	portfolio PortfolioSink
}

func NewPlatformAdapter(ledgerSink LedgerSink, portfolioSink PortfolioSink) *PlatformAdapter {
	return &PlatformAdapter{ledger: ledgerSink, portfolio: portfolioSink}
}

func (adapter *PlatformAdapter) Apply(ctx context.Context, fact Fact) error {
	if err := validateFact(fact); err != nil {
		return err
	}
	if adapter == nil || adapter.ledger == nil || adapter.portfolio == nil {
		return fmt.Errorf("Platform accounting sinks are required")
	}
	switch fact.Kind {
	case KindFill:
		_, result, err := adapter.portfolio.ApplyFill(ctx, portfolio.Fill{
			PrincipalID: fact.PrincipalID, FillID: fact.FactID, CommandID: fact.CommandID, LineageID: fact.LineageID, ProductID: fact.ProductID,
			Mode: portfolio.Mode(fact.Mode), AccountID: fact.AccountID, InstrumentID: fact.InstrumentID,
			Side: portfolio.Side(fact.Side), Quantity: fact.Quantity.String(), Price: fact.Price.String(),
			Fee: fact.Fee.String(), FeeCurrency: fact.FeeAsset, SettlementCurrency: fact.SettlementAsset, OccurredAt: fact.EffectiveAt,
		})
		if err != nil {
			return err
		}
		if result.RealizedPnL != "" && result.RealizedPnL != "0" {
			if err := adapter.record(ctx, fact, fact.FactID+":realized", ledger.KindRealizedPnL, result.RealizedPnL, fact.SettlementAsset); err != nil {
				return err
			}
		}
		if fact.Fee.IsPositive() {
			return adapter.record(ctx, fact, fact.FactID+":fee", ledger.KindFee, fact.Fee.String(), fact.FeeAsset)
		}
	case KindFunding:
		entry := adapter.entry(fact, fact.FactID+":funding", ledger.KindFunding, fact.Amount.String(), fact.Asset)
		if fact.SupersedesFactID == "" {
			return adapter.ledger.Record(ctx, entry)
		}
		sink, ok := adapter.ledger.(FundingSupersessionSink)
		if !ok {
			return fmt.Errorf("funding accounting sink does not support supersession")
		}
		return sink.SupersedeFunding(ctx, fact.SupersedesFactID+":funding", entry)
	case KindMark:
		position, err := adapter.portfolio.ApplyMarkContext(ctx, fact.PrincipalID, portfolio.Mode(fact.Mode), fact.AccountID, fact.ProductID, fact.LineageID, fact.InstrumentID, fact.Price.String(), fact.EffectiveAt)
		if err != nil {
			return err
		}
		return adapter.ledger.SetUnrealized(ctx, ledger.Unrealized{
			PrincipalID: fact.PrincipalID, LineageID: fact.LineageID, ProductID: fact.ProductID, AccountID: fact.AccountID, InstrumentID: fact.InstrumentID,
			Mode: ledger.Mode(fact.Mode), Amount: position.UnrealizedPnL, Currency: position.SettlementCurrency, AsOf: fact.EffectiveAt,
		})
	case KindUnrealized:
		return adapter.ledger.SetUnrealized(ctx, ledger.Unrealized{
			PrincipalID: fact.PrincipalID, LineageID: fact.LineageID, ProductID: fact.ProductID, AccountID: fact.AccountID, InstrumentID: fact.InstrumentID,
			Mode: ledger.Mode(fact.Mode), Amount: fact.Amount.String(), Currency: fact.Asset, AsOf: fact.EffectiveAt,
		})
	}
	return nil
}

func (adapter *PlatformAdapter) record(ctx context.Context, fact Fact, id string, kind ledger.Kind, amount, currency string) error {
	return adapter.ledger.Record(ctx, adapter.entry(fact, id, kind, amount, currency))
}

func (adapter *PlatformAdapter) entry(fact Fact, id string, kind ledger.Kind, amount, currency string) ledger.Entry {
	return ledger.Entry{
		PrincipalID: fact.PrincipalID, EntryID: id, Mode: ledger.Mode(fact.Mode), LineageID: fact.LineageID, ProductID: fact.ProductID,
		AccountID: fact.AccountID, InstrumentID: fact.InstrumentID, Kind: kind, Amount: amount,
		Currency: currency, OccurredAt: fact.EffectiveAt,
	}
}

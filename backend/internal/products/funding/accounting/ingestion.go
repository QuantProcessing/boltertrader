package accounting

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrIngestionIncomplete  = errors.New("funding accounting ingestion incomplete")
	ErrIngestionUnsupported = errors.New("funding accounting ingestion unsupported")
)

const maxFundingPaymentPages = 100

type MarkPrice struct {
	AccountID, InstrumentID, Price string
	ObservedAt                     time.Time
}

type FundingPayment struct {
	FactID, SupersedesFactID, AccountID, InstrumentID, SettlementAsset, Amount string
	EffectiveAt, ObservedAt                                                    time.Time
	Revision                                                                   int64
}

type FundingPaymentPage struct {
	Items                                   []FundingPayment
	AccountID, InstrumentID, NextCursor     string
	Start, End, CompleteThrough, ObservedAt time.Time
	Revision                                int64
	Status                                  Completeness
	Verified                                bool
}

type FundingPaymentQuery struct {
	AccountID, InstrumentID, SettlementAsset, Cursor string
	Start, End                                       time.Time
	Limit                                            int
}

type AuthoritativeSource interface {
	MarkPrice(context.Context, string, string) (MarkPrice, error)
	FundingPayments(context.Context, FundingPaymentQuery) (FundingPaymentPage, error)
}

type AppliedFactObserver interface {
	AccountingFactApplied(context.Context, AppliedFact) error
}

type AppliedFact struct {
	Fact
	PrincipalID, RequestID string
}

type IngestionRequest struct {
	PrincipalID, RequestID, ProductID, Mode, LineageID, PositionID, AccountID, InstrumentID, SettlementAsset string
	Start, End                                                                                               time.Time
}

type Ingestor struct {
	source   AuthoritativeSource
	adapter  *PlatformAdapter
	observer AppliedFactObserver
}

func NewIngestor(source AuthoritativeSource, adapter *PlatformAdapter, observer AppliedFactObserver) (*Ingestor, error) {
	if source == nil || adapter == nil {
		return nil, fmt.Errorf("funding accounting ingestion dependencies are required")
	}
	return &Ingestor{source: source, adapter: adapter, observer: observer}, nil
}

func (ingestor *Ingestor) Ingest(ctx context.Context, request IngestionRequest) error {
	if err := validateIngestionRequest(request); err != nil {
		return err
	}
	mark, err := ingestor.source.MarkPrice(ctx, request.AccountID, request.InstrumentID)
	if err != nil {
		return fmt.Errorf("read authoritative mark price: %w", err)
	}
	price, err := decimal.NewFromString(mark.Price)
	if err != nil || !price.IsPositive() || mark.AccountID != request.AccountID || mark.InstrumentID != request.InstrumentID || mark.ObservedAt.IsZero() || mark.ObservedAt.After(request.End) {
		return fmt.Errorf("%w: invalid mark price", ErrIngestionIncomplete)
	}
	facts := []Fact{{
		PrincipalID: request.PrincipalID, FactID: fmt.Sprintf("mark:%s:%s:%d", request.AccountID, request.InstrumentID, mark.ObservedAt.UTC().UnixNano()),
		Revision: mark.ObservedAt.UTC().UnixNano(), Kind: KindMark, ProductID: request.ProductID, Mode: request.Mode,
		LineageID: request.LineageID, PositionID: request.PositionID, AccountID: request.AccountID, InstrumentID: request.InstrumentID,
		EffectiveAt: mark.ObservedAt.UTC(), Price: price,
	}}

	cursor := ""
	complete := false
	for pageNumber := 0; pageNumber < maxFundingPaymentPages; pageNumber++ {
		page, err := ingestor.source.FundingPayments(ctx, FundingPaymentQuery{
			AccountID: request.AccountID, InstrumentID: request.InstrumentID, SettlementAsset: request.SettlementAsset,
			Start: request.Start, End: request.End, Cursor: cursor, Limit: 500,
		})
		if err != nil {
			return fmt.Errorf("read authoritative funding payments: %w", err)
		}
		if err := validateFundingPaymentPage(request, cursor, page); err != nil {
			return err
		}
		for _, payment := range page.Items {
			amount, parseErr := decimal.NewFromString(payment.Amount)
			if parseErr != nil || payment.FactID == "" || payment.AccountID != request.AccountID || payment.InstrumentID != request.InstrumentID || payment.SettlementAsset != request.SettlementAsset || payment.EffectiveAt.Before(request.Start) || !payment.EffectiveAt.Before(request.End) {
				return fmt.Errorf("%w: invalid funding payment", ErrIngestionIncomplete)
			}
			revision := payment.Revision
			if revision <= 0 {
				revision = page.Revision
			}
			facts = append(facts, Fact{
				PrincipalID: request.PrincipalID, FactID: payment.FactID, SupersedesFactID: payment.SupersedesFactID, Revision: revision, Kind: KindFunding,
				ProductID: request.ProductID, Mode: request.Mode, LineageID: request.LineageID, PositionID: request.PositionID,
				AccountID: request.AccountID, InstrumentID: request.InstrumentID, EffectiveAt: payment.EffectiveAt.UTC(), Amount: amount, Asset: payment.SettlementAsset,
			})
		}
		if page.NextCursor == "" {
			complete = true
			break
		}
		if page.NextCursor == cursor {
			return fmt.Errorf("%w: funding payment cursor did not advance", ErrIngestionIncomplete)
		}
		cursor = page.NextCursor
	}
	if !complete {
		return fmt.Errorf("%w: funding payment pagination exceeded %d pages", ErrIngestionIncomplete, maxFundingPaymentPages)
	}
	for _, fact := range facts {
		if err := ingestor.apply(ctx, request, fact); err != nil {
			return fmt.Errorf("apply authoritative %s fact %q: %w", fact.Kind, fact.FactID, err)
		}
	}
	return nil
}

func (ingestor *Ingestor) apply(ctx context.Context, request IngestionRequest, fact Fact) error {
	if err := ingestor.adapter.Apply(ctx, fact); err != nil {
		return err
	}
	if ingestor.observer != nil {
		return ingestor.observer.AccountingFactApplied(ctx, AppliedFact{Fact: fact, PrincipalID: request.PrincipalID, RequestID: request.RequestID})
	}
	return nil
}

func validateIngestionRequest(request IngestionRequest) error {
	if request.PrincipalID == "" || request.RequestID == "" || request.ProductID != "funding" || request.Mode != "test" || request.LineageID == "" || request.PositionID == "" || request.AccountID == "" || request.InstrumentID == "" || request.SettlementAsset == "" || request.Start.IsZero() || request.End.IsZero() || !request.Start.Before(request.End) {
		return fmt.Errorf("invalid funding accounting ingestion request")
	}
	return nil
}

func validateFundingPaymentPage(request IngestionRequest, cursor string, page FundingPaymentPage) error {
	if page.AccountID != request.AccountID || page.InstrumentID != request.InstrumentID || !page.Start.Equal(request.Start) || !page.End.Equal(request.End) || page.ObservedAt.IsZero() {
		return fmt.Errorf("%w: invalid funding payment scope", ErrIngestionIncomplete)
	}
	if page.Status == "unsupported" {
		return ErrIngestionUnsupported
	}
	if page.Revision <= 0 {
		return fmt.Errorf("%w: invalid funding payment revision", ErrIngestionIncomplete)
	}
	if page.NextCursor != "" {
		if page.Verified || page.Status != CompletenessIncomplete {
			return fmt.Errorf("%w: paginated funding payment page claimed completeness", ErrIngestionIncomplete)
		}
		return nil
	}
	if !page.Verified || (page.Status != CompletenessComplete && page.Status != CompletenessCompleteEmpty) || page.CompleteThrough.Before(request.End) || page.ObservedAt.Before(request.End) {
		return fmt.Errorf("%w: funding payment completeness is unverified", ErrIngestionIncomplete)
	}
	if page.Status == CompletenessCompleteEmpty && len(page.Items) != 0 {
		return fmt.Errorf("%w: complete-empty page contains payments", ErrIngestionIncomplete)
	}
	_ = cursor
	return nil
}

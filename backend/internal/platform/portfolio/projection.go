package portfolio

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

type Side string

type Mode string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
	ModeTest Mode = "test"
	ModeLive Mode = "live"
)

type Fill struct {
	FillID, CommandID, LineageID, ProductID, AccountID, InstrumentID string
	Mode                                                             Mode
	Side                                                             Side
	Quantity, Price, Fee, FeeCurrency                                string
	OccurredAt                                                       time.Time
}

type FillResult struct {
	RealizedPnL string
	Fee         string
}

type Position struct {
	LineageID         string    `json:"lineageId"`
	ProductID         string    `json:"productId"`
	Mode              Mode      `json:"mode"`
	AccountID         string    `json:"accountId"`
	InstrumentID      string    `json:"instrumentId"`
	Quantity          string    `json:"quantity"`
	AverageEntryPrice string    `json:"averageEntryPrice"`
	MarkPrice         string    `json:"markPrice"`
	RealizedPnL       string    `json:"realizedPnl"`
	UnrealizedPnL     string    `json:"unrealizedPnl"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type positionState struct {
	Position
	quantity, average, mark, realized decimal.Decimal
}

type Projection struct {
	mu        sync.Mutex
	positions map[string]positionState
	fills     map[string]FillResult
	store     Store
}

type Store interface {
	Upsert(context.Context, Position) error
	List(context.Context, Mode, string, string) ([]Position, error)
}

func NewProjection() *Projection {
	return &Projection{positions: make(map[string]positionState), fills: make(map[string]FillResult)}
}

func NewPersistentProjection(store Store) *Projection {
	projection := NewProjection()
	projection.store = store
	return projection
}

func (projection *Projection) ApplyFill(_ context.Context, fill Fill) (Position, FillResult, error) {
	quantity, price, fee, err := validateFill(fill)
	if err != nil {
		return Position{}, FillResult{}, err
	}
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if result, exists := projection.fills[fill.FillID]; exists {
		return projection.positions[positionKey(string(fill.Mode), fill.AccountID, fill.ProductID, fill.LineageID, fill.InstrumentID)].Position, result, nil
	}
	key := positionKey(string(fill.Mode), fill.AccountID, fill.ProductID, fill.LineageID, fill.InstrumentID)
	state := projection.positions[key]
	state.LineageID, state.ProductID, state.Mode, state.AccountID, state.InstrumentID = fill.LineageID, fill.ProductID, fill.Mode, fill.AccountID, fill.InstrumentID
	signed := quantity
	if fill.Side == SideSell {
		signed = signed.Neg()
	}
	realized := decimal.Zero
	if state.quantity.IsZero() || state.quantity.Sign() == signed.Sign() {
		total := state.quantity.Abs().Mul(state.average).Add(signed.Abs().Mul(price))
		state.quantity = state.quantity.Add(signed)
		state.average = total.Div(state.quantity.Abs())
	} else {
		closed := decimal.Min(state.quantity.Abs(), signed.Abs())
		if state.quantity.IsPositive() {
			realized = price.Sub(state.average).Mul(closed)
		} else {
			realized = state.average.Sub(price).Mul(closed)
		}
		remaining := state.quantity.Add(signed)
		if remaining.IsZero() {
			state.average = decimal.Zero
		} else if remaining.Sign() != state.quantity.Sign() {
			state.average = price
		}
		state.quantity = remaining
	}
	state.realized = state.realized.Add(realized)
	state.UpdatedAt = fill.OccurredAt.UTC()
	state.refresh()
	if projection.store != nil {
		if err := projection.store.Upsert(context.Background(), state.Position); err != nil {
			return Position{}, FillResult{}, err
		}
	}
	projection.positions[key] = state
	result := FillResult{RealizedPnL: number(realized), Fee: number(fee)}
	projection.fills[fill.FillID] = result
	return state.Position, result, nil
}

func (projection *Projection) ApplyMark(mode Mode, accountID, productID, lineageID, instrumentID, mark string, asOf time.Time) (Position, error) {
	price, err := decimal.NewFromString(mark)
	if err != nil || price.IsNegative() {
		return Position{}, fmt.Errorf("mark price must be a non-negative decimal")
	}
	projection.mu.Lock()
	defer projection.mu.Unlock()
	key := positionKey(string(mode), accountID, productID, lineageID, instrumentID)
	state, ok := projection.positions[key]
	if !ok {
		return Position{}, fmt.Errorf("position not found")
	}
	state.mark, state.UpdatedAt = price, asOf.UTC()
	state.refresh()
	if projection.store != nil {
		if err := projection.store.Upsert(context.Background(), state.Position); err != nil {
			return Position{}, err
		}
	}
	projection.positions[key] = state
	return state.Position, nil
}

func (projection *Projection) ListContext(ctx context.Context, mode Mode, accountID, productID string) ([]Position, error) {
	if projection.store != nil {
		return projection.store.List(ctx, mode, accountID, productID)
	}
	return projection.List(mode, accountID, productID), nil
}

func (projection *Projection) List(mode Mode, accountID, productID string) []Position {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	result := make([]Position, 0)
	for _, state := range projection.positions {
		if state.Mode == mode && (accountID == "" || state.AccountID == accountID) && (productID == "" || state.ProductID == productID) {
			result = append(result, state.Position)
		}
	}
	return result
}

func (state *positionState) refresh() {
	unrealized := decimal.Zero
	if !state.mark.IsZero() && !state.quantity.IsZero() {
		unrealized = state.mark.Sub(state.average).Mul(state.quantity)
	}
	state.Quantity = number(state.quantity)
	state.AverageEntryPrice = number(state.average)
	state.MarkPrice = number(state.mark)
	state.RealizedPnL = number(state.realized)
	state.UnrealizedPnL = number(unrealized)
}

func validateFill(fill Fill) (decimal.Decimal, decimal.Decimal, decimal.Decimal, error) {
	if fill.FillID == "" || fill.CommandID == "" || fill.LineageID == "" || fill.ProductID == "" || fill.AccountID == "" || fill.InstrumentID == "" || fill.FeeCurrency == "" || fill.OccurredAt.IsZero() || (fill.Mode != ModeTest && fill.Mode != ModeLive) {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("fill attribution is incomplete")
	}
	if fill.Side != SideBuy && fill.Side != SideSell {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("invalid fill side")
	}
	quantity, qErr := decimal.NewFromString(fill.Quantity)
	price, pErr := decimal.NewFromString(fill.Price)
	fee, fErr := decimal.NewFromString(fill.Fee)
	if qErr != nil || pErr != nil || fErr != nil || !quantity.IsPositive() || !price.IsPositive() || fee.IsNegative() {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("invalid fill decimals")
	}
	return quantity, price, fee, nil
}

func positionKey(parts ...string) string  { return strings.Join(parts, "\x00") }
func number(value decimal.Decimal) string { return value.String() }

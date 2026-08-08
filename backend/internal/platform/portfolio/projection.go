package portfolio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	Quantity, Price, Fee, FeeCurrency, SettlementCurrency            string
	OccurredAt                                                       time.Time
}

type FillResult struct {
	RealizedPnL        string
	Fee                string
	SettlementCurrency string
}

var (
	ErrFillConflict     = errors.New("fill identity conflict")
	ErrCurrencyMismatch = errors.New("portfolio settlement currency mismatch")
)

type Position struct {
	LineageID          string    `json:"lineageId"`
	ProductID          string    `json:"productId"`
	Mode               Mode      `json:"mode"`
	AccountID          string    `json:"accountId"`
	InstrumentID       string    `json:"instrumentId"`
	SettlementCurrency string    `json:"settlementCurrency"`
	Quantity           string    `json:"quantity"`
	AverageEntryPrice  string    `json:"averageEntryPrice"`
	MarkPrice          string    `json:"markPrice"`
	RealizedPnL        string    `json:"realizedPnl"`
	UnrealizedPnL      string    `json:"unrealizedPnl"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type positionState struct {
	Position
	quantity, average, mark, realized decimal.Decimal
}

type processedFill struct {
	fingerprint string
	result      FillResult
}

type Projection struct {
	mu        sync.Mutex
	positions map[string]positionState
	fills     map[string]processedFill
	store     Store
}

type Store interface {
	ApplyFill(context.Context, Fill, func(Position) (Position, FillResult, error)) (Position, FillResult, error)
	ApplyMark(context.Context, Mode, string, string, string, string, func(Position) (Position, error)) (Position, error)
	Upsert(context.Context, Position) error
	List(context.Context, Mode, string, string) ([]Position, error)
}

func NewProjection() *Projection {
	return &Projection{positions: make(map[string]positionState), fills: make(map[string]processedFill)}
}

func NewPersistentProjection(store Store) *Projection {
	projection := NewProjection()
	projection.store = store
	return projection
}

func (projection *Projection) ApplyFill(ctx context.Context, fill Fill) (Position, FillResult, error) {
	quantity, price, fee, err := validateFill(fill)
	if err != nil {
		return Position{}, FillResult{}, err
	}
	if projection.store != nil {
		return projection.store.ApplyFill(ctx, fill, func(current Position) (Position, FillResult, error) {
			state, err := stateFromPosition(current)
			if err != nil {
				return Position{}, FillResult{}, err
			}
			return applyFill(state, fill, quantity, price, fee)
		})
	}
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if recorded, exists := projection.fills[fill.FillID]; exists {
		if recorded.fingerprint != FillFingerprint(fill) {
			return Position{}, FillResult{}, fmt.Errorf("%w: %s", ErrFillConflict, fill.FillID)
		}
		return projection.positions[positionKey(string(fill.Mode), fill.AccountID, fill.ProductID, fill.LineageID, fill.InstrumentID)].Position, recorded.result, nil
	}
	key := positionKey(string(fill.Mode), fill.AccountID, fill.ProductID, fill.LineageID, fill.InstrumentID)
	position, result, err := applyFill(projection.positions[key], fill, quantity, price, fee)
	if err != nil {
		return Position{}, FillResult{}, err
	}
	state, err := stateFromPosition(position)
	if err != nil {
		return Position{}, FillResult{}, err
	}
	projection.positions[key] = state
	projection.fills[fill.FillID] = processedFill{fingerprint: FillFingerprint(fill), result: result}
	return position, result, nil
}

func applyFill(state positionState, fill Fill, quantity, price, fee decimal.Decimal) (Position, FillResult, error) {
	if state.SettlementCurrency != "" && state.SettlementCurrency != fill.SettlementCurrency {
		return Position{}, FillResult{}, ErrCurrencyMismatch
	}
	state.SettlementCurrency = fill.SettlementCurrency
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
	result := FillResult{RealizedPnL: number(realized), Fee: number(fee), SettlementCurrency: fill.SettlementCurrency}
	return state.Position, result, nil
}

func stateFromPosition(position Position) (positionState, error) {
	state := positionState{Position: position}
	if position.Quantity == "" {
		return state, nil
	}
	var err error
	if state.quantity, err = decimal.NewFromString(position.Quantity); err != nil {
		return positionState{}, fmt.Errorf("invalid persisted position quantity")
	}
	if state.average, err = decimal.NewFromString(position.AverageEntryPrice); err != nil {
		return positionState{}, fmt.Errorf("invalid persisted average entry price")
	}
	if state.mark, err = decimal.NewFromString(position.MarkPrice); err != nil {
		return positionState{}, fmt.Errorf("invalid persisted mark price")
	}
	if state.realized, err = decimal.NewFromString(position.RealizedPnL); err != nil {
		return positionState{}, fmt.Errorf("invalid persisted realized PnL")
	}
	return state, nil
}

func FillFingerprint(fill Fill) string {
	payload, _ := json.Marshal(struct {
		FillID, CommandID, LineageID, ProductID, AccountID, InstrumentID string
		Mode                                                             Mode
		Side                                                             Side
		Quantity, Price, Fee, FeeCurrency, SettlementCurrency            string
		OccurredAt                                                       time.Time
	}{
		fill.FillID, fill.CommandID, fill.LineageID, fill.ProductID, fill.AccountID, fill.InstrumentID,
		fill.Mode, fill.Side, fill.Quantity, fill.Price, fill.Fee, fill.FeeCurrency, fill.SettlementCurrency, fill.OccurredAt.UTC(),
	})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func (projection *Projection) ApplyMark(mode Mode, accountID, productID, lineageID, instrumentID, mark string, asOf time.Time) (Position, error) {
	price, err := decimal.NewFromString(mark)
	if err != nil || price.IsNegative() {
		return Position{}, fmt.Errorf("mark price must be a non-negative decimal")
	}
	if projection.store != nil {
		return projection.store.ApplyMark(context.Background(), mode, accountID, productID, lineageID, instrumentID, func(current Position) (Position, error) {
			state, err := stateFromPosition(current)
			if err != nil {
				return Position{}, err
			}
			state.mark, state.UpdatedAt = price, asOf.UTC()
			state.refresh()
			return state.Position, nil
		})
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
	if projection.store != nil {
		items, err := projection.store.List(context.Background(), mode, accountID, productID)
		if err != nil {
			return nil
		}
		return items
	}
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
	if fill.FillID == "" || fill.CommandID == "" || fill.LineageID == "" || fill.ProductID == "" || fill.AccountID == "" || fill.InstrumentID == "" || fill.FeeCurrency == "" || fill.SettlementCurrency == "" || fill.OccurredAt.IsZero() || (fill.Mode != ModeTest && fill.Mode != ModeLive) {
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

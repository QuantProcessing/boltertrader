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

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

type Fill struct {
	InstrumentProduct, BaseAsset                                                  string
	PrincipalID, FillID, CommandID, LineageID, ProductID, AccountID, InstrumentID string
	Side                                                                          Side
	Quantity, Price, Fee, FeeCurrency, SettlementCurrency                         string
	OccurredAt                                                                    time.Time
}

type FillResult struct {
	RealizedPnL        string
	Fee                string
	SettlementCurrency string
}

var (
	ErrFillConflict     = errors.New("fill identity conflict")
	ErrMarkConflict     = errors.New("mark identity conflict")
	ErrCurrencyMismatch = errors.New("portfolio settlement currency mismatch")
	ErrAccountingReview = errors.New("historical fee accounting requires review")
)

type Position struct {
	AccountingStatus   string    `json:"accountingStatus"`
	PrincipalID        string    `json:"-"`
	LineageID          string    `json:"lineageId"`
	ProductID          string    `json:"productId"`
	AccountID          string    `json:"accountId"`
	InstrumentID       string    `json:"instrumentId"`
	SettlementCurrency string    `json:"settlementCurrency"`
	Quantity           string    `json:"quantity"`
	AverageEntryPrice  string    `json:"averageEntryPrice"`
	MarkPrice          string    `json:"markPrice"`
	RealizedPnL        string    `json:"realizedPnl"`
	UnrealizedPnL      string    `json:"unrealizedPnl"`
	MarkObservedAt     time.Time `json:"-"`
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
	ApplyMark(context.Context, string, string, string, string, string, func(Position) (Position, error)) (Position, error)
	Upsert(context.Context, Position) error
	List(context.Context, string, string, string) ([]Position, error)
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
	fillKey := positionKey(fill.PrincipalID, fill.AccountID, fill.InstrumentID, fill.FillID)
	if recorded, exists := projection.fills[fillKey]; exists {
		if !ReplayCompatible(recorded.fingerprint, fill) {
			return Position{}, FillResult{}, fmt.Errorf("%w: %s", ErrFillConflict, fill.FillID)
		}
		return projection.positions[positionKey(fill.PrincipalID, fill.AccountID, fill.ProductID, fill.LineageID, fill.InstrumentID)].Position, recorded.result, nil
	}
	key := positionKey(fill.PrincipalID, fill.AccountID, fill.ProductID, fill.LineageID, fill.InstrumentID)
	position, result, err := applyFill(projection.positions[key], fill, quantity, price, fee)
	if err != nil {
		return Position{}, FillResult{}, err
	}
	state, err := stateFromPosition(position)
	if err != nil {
		return Position{}, FillResult{}, err
	}
	projection.positions[key] = state
	projection.fills[fillKey] = processedFill{fingerprint: FillFingerprint(fill), result: result}
	return position, result, nil
}

func applyFill(state positionState, fill Fill, quantity, price, fee decimal.Decimal) (Position, FillResult, error) {
	if state.AccountingStatus != "" && state.AccountingStatus != "ready" {
		return Position{}, FillResult{}, ErrAccountingReview
	}
	delta, err := QuantityDelta(fill)
	if err != nil {
		return Position{}, FillResult{}, err
	}
	state.AccountingStatus = "ready"
	if state.SettlementCurrency != "" && state.SettlementCurrency != fill.SettlementCurrency {
		return Position{}, FillResult{}, ErrCurrencyMismatch
	}
	state.SettlementCurrency = fill.SettlementCurrency
	state.PrincipalID, state.LineageID, state.ProductID, state.AccountID, state.InstrumentID = fill.PrincipalID, fill.LineageID, fill.ProductID, fill.AccountID, fill.InstrumentID
	signed := quantity
	if fill.Side == SideSell {
		signed = signed.Neg()
	}
	realized := applyQuantity(&state, signed, price)
	// A base-asset fee is a separate inventory movement at the observed trade
	// price. Its disposal gain is realized; the fee itself stays in its original
	// currency in the ledger. Do not capitalize it and then deduct it again.
	realized = realized.Add(applyQuantity(&state, delta.Sub(signed), price))
	state.realized = state.realized.Add(realized)
	if state.UpdatedAt.Before(fill.OccurredAt) {
		state.UpdatedAt = fill.OccurredAt.UTC()
	}
	state.refresh()
	result := FillResult{RealizedPnL: number(realized), Fee: number(fee), SettlementCurrency: fill.SettlementCurrency}
	return state.Position, result, nil
}

func applyQuantity(state *positionState, signed, price decimal.Decimal) decimal.Decimal {
	if signed.IsZero() {
		return decimal.Zero
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
	return realized
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
		PrincipalID, FillID, CommandID, LineageID, ProductID, AccountID, InstrumentID string
		Side                                                                          Side
		Quantity, Price, Fee, FeeCurrency, SettlementCurrency                         string
		OccurredAt                                                                    time.Time
		InstrumentProduct                                                             string `json:",omitempty"`
		BaseAsset                                                                     string `json:",omitempty"`
	}{
		fill.PrincipalID, fill.FillID, fill.CommandID, fill.LineageID, fill.ProductID, fill.AccountID, fill.InstrumentID,
		fill.Side, fill.Quantity, fill.Price, fill.Fee, fill.FeeCurrency, fill.SettlementCurrency, fill.OccurredAt.UTC(),
		fill.InstrumentProduct, fill.BaseAsset,
	})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func (projection *Projection) ApplyMark(principalID string, accountID, productID, lineageID, instrumentID, mark string, asOf time.Time) (Position, error) {
	return projection.ApplyMarkContext(context.Background(), principalID, accountID, productID, lineageID, instrumentID, mark, asOf)
}

func (projection *Projection) ApplyMarkContext(ctx context.Context, principalID string, accountID, productID, lineageID, instrumentID, mark string, asOf time.Time) (Position, error) {
	price, err := decimal.NewFromString(mark)
	if err != nil || principalID == "" || price.IsNegative() || asOf.IsZero() {
		return Position{}, fmt.Errorf("mark price must be a non-negative decimal")
	}
	if projection.store != nil {
		return projection.store.ApplyMark(ctx, principalID, accountID, productID, lineageID, instrumentID, func(current Position) (Position, error) {
			if unchanged, handled, monotonicErr := preserveMonotonicMark(current, price, asOf); handled {
				return unchanged, monotonicErr
			}
			state, err := stateFromPosition(current)
			if err != nil {
				return Position{}, err
			}
			state.mark, state.MarkObservedAt = price, asOf.UTC()
			if state.UpdatedAt.Before(asOf) {
				state.UpdatedAt = asOf.UTC()
			}
			state.refresh()
			return state.Position, nil
		})
	}
	projection.mu.Lock()
	defer projection.mu.Unlock()
	key := positionKey(principalID, accountID, productID, lineageID, instrumentID)
	state, ok := projection.positions[key]
	if !ok {
		return Position{}, fmt.Errorf("position not found")
	}
	if unchanged, handled, monotonicErr := preserveMonotonicMark(state.Position, price, asOf); handled {
		return unchanged, monotonicErr
	}
	state.mark, state.MarkObservedAt = price, asOf.UTC()
	if state.UpdatedAt.Before(asOf) {
		state.UpdatedAt = asOf.UTC()
	}
	state.refresh()
	projection.positions[key] = state
	return state.Position, nil
}

func preserveMonotonicMark(current Position, price decimal.Decimal, asOf time.Time) (Position, bool, error) {
	if current.MarkObservedAt.IsZero() || current.MarkObservedAt.Before(asOf) {
		return Position{}, false, nil
	}
	if current.MarkObservedAt.Equal(asOf) && current.MarkPrice != price.String() {
		return Position{}, true, ErrMarkConflict
	}
	return current, true, nil
}

func (projection *Projection) ListContext(ctx context.Context, principalID string, accountID, productID string) ([]Position, error) {
	if projection.store != nil {
		return projection.store.List(ctx, principalID, accountID, productID)
	}
	return projection.List(principalID, accountID, productID), nil
}

func (projection *Projection) List(principalID string, accountID, productID string) []Position {
	if projection.store != nil {
		items, err := projection.store.List(context.Background(), principalID, accountID, productID)
		if err != nil {
			return nil
		}
		return items
	}
	projection.mu.Lock()
	defer projection.mu.Unlock()
	result := make([]Position, 0)
	for _, state := range projection.positions {
		if state.PrincipalID == principalID && (accountID == "" || state.AccountID == accountID) && (productID == "" || state.ProductID == productID) {
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
	if fill.PrincipalID == "" || fill.FillID == "" || fill.CommandID == "" || fill.LineageID == "" || fill.ProductID == "" || fill.AccountID == "" || fill.InstrumentID == "" || fill.SettlementCurrency == "" || fill.OccurredAt.IsZero() {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("fill attribution is incomplete")
	}
	if fill.Side != SideBuy && fill.Side != SideSell {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("invalid fill side")
	}
	quantity, qErr := decimal.NewFromString(fill.Quantity)
	price, pErr := decimal.NewFromString(fill.Price)
	fee, fErr := decimal.NewFromString(fill.Fee)
	if qErr != nil || pErr != nil || fErr != nil || !quantity.IsPositive() || !price.IsPositive() {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("invalid fill decimals")
	}
	if _, err := QuantityDelta(fill); err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	return quantity, price, fee, nil
}

func positionKey(parts ...string) string  { return strings.Join(parts, "\x00") }
func number(value decimal.Decimal) string { return value.String() }

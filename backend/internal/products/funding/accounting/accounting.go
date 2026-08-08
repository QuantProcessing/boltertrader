package accounting

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrFactConflict     = errors.New("funding accounting fact conflict")
	ErrSupersessionFork = errors.New("funding accounting supersession fork")
	ErrInvalidFact      = errors.New("invalid funding accounting fact")
	ErrScopeMismatch    = errors.New("funding accounting projection scope mismatch")
	ErrCurrencyMismatch = errors.New("funding accounting currency mismatch")
)

type Kind string

const (
	KindFill       Kind = "fill"
	KindFunding    Kind = "funding"
	KindMark       Kind = "mark"
	KindUnrealized Kind = "unrealized"
	KindWatermark  Kind = "watermark"
	KindExecution  Kind = "execution"
	KindResidual   Kind = "residual"
)

type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

type Completeness string

const (
	CompletenessIncomplete    Completeness = "incomplete"
	CompletenessComplete      Completeness = "complete"
	CompletenessCompleteEmpty Completeness = "complete_empty"
)

type ExecutionState string

const (
	ExecutionHedgedOpen      ExecutionState = "hedged_open"
	ExecutionClosed          ExecutionState = "closed"
	ExecutionCompensatedFlat ExecutionState = "compensated_flat"
	ExecutionManualAttention ExecutionState = "manual_attention"
)

type PositionStatus string

const (
	StatusOpen      PositionStatus = "open"
	StatusClosed    PositionStatus = "closed"
	StatusAttention PositionStatus = "attention"
)

type Residual struct {
	LegQuantity         decimal.Decimal `json:"legQuantity"`
	OpenOrderCount      int             `json:"openOrderCount"`
	PendingCompensation int             `json:"pendingCompensation"`
	BlockerClass        string          `json:"blockerClass"`
}

func (residual Residual) Present() bool {
	return !residual.LegQuantity.IsZero() || residual.OpenOrderCount > 0 || residual.PendingCompensation > 0 || residual.BlockerClass != ""
}

// Fact is a normalized Platform fact. The Funding accounting package owns no
// transport, venue client, credential, or persistence implementation.
type Fact struct {
	FactID           string          `json:"factId"`
	SupersedesFactID string          `json:"supersedesFactId,omitempty"`
	Revision         int64           `json:"revision"`
	Kind             Kind            `json:"kind"`
	ProductID        string          `json:"productId"`
	Mode             string          `json:"mode"`
	LineageID        string          `json:"lineageId"`
	PositionID       string          `json:"positionId"`
	AccountID        string          `json:"accountId"`
	InstrumentID     string          `json:"instrumentId"`
	CommandID        string          `json:"commandId,omitempty"`
	EffectiveAt      time.Time       `json:"effectiveAt"`
	Side             Side            `json:"side,omitempty"`
	Quantity         decimal.Decimal `json:"quantity,omitempty"`
	Price            decimal.Decimal `json:"price,omitempty"`
	Fee              decimal.Decimal `json:"fee,omitempty"`
	Amount           decimal.Decimal `json:"amount,omitempty"`
	Asset            string          `json:"asset,omitempty"`
	FeeAsset         string          `json:"feeAsset,omitempty"`
	SettlementAsset  string          `json:"settlementAsset,omitempty"`
	Completeness     Completeness    `json:"completeness,omitempty"`
	Verified         bool            `json:"verified,omitempty"`
	ExecutionState   ExecutionState  `json:"executionState,omitempty"`
	Residual         Residual        `json:"residual,omitempty"`
}

type Journal struct {
	mu           sync.Mutex
	facts        map[string]Fact
	hashes       map[string][sha256.Size]byte
	supersededBy map[string]string
}

func NewJournal() *Journal {
	return &Journal{
		facts: make(map[string]Fact), hashes: make(map[string][sha256.Size]byte), supersededBy: make(map[string]string),
	}
}

func (journal *Journal) Append(fact Fact) error {
	if err := validateFact(fact); err != nil {
		return err
	}
	body, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("marshal accounting fact: %w", err)
	}
	hash := sha256.Sum256(body)
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if existing, ok := journal.hashes[fact.FactID]; ok {
		if existing != hash {
			return ErrFactConflict
		}
		return nil
	}
	if fact.SupersedesFactID != "" {
		target, ok := journal.facts[fact.SupersedesFactID]
		if !ok || target.Revision >= fact.Revision || scopeKey(target) != scopeKey(fact) || target.Kind != fact.Kind {
			return ErrFactConflict
		}
		if successor := journal.supersededBy[fact.SupersedesFactID]; successor != "" && successor != fact.FactID {
			return ErrSupersessionFork
		}
		journal.supersededBy[fact.SupersedesFactID] = fact.FactID
	}
	journal.facts[fact.FactID], journal.hashes[fact.FactID] = fact, hash
	return nil
}

func (journal *Journal) Facts() []Fact {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	result := make([]Fact, 0, len(journal.facts))
	for id, fact := range journal.facts {
		if journal.supersededBy[id] == "" {
			result = append(result, fact)
		}
	}
	sortFacts(result)
	return result
}

type Projection struct {
	ProductID            string          `json:"productId"`
	Mode                 string          `json:"mode"`
	LineageID            string          `json:"lineageId"`
	PositionID           string          `json:"positionId"`
	Status               PositionStatus  `json:"status"`
	RealizedTradingPnL   decimal.Decimal `json:"realizedTradingPnl"`
	UnrealizedTradingPnL decimal.Decimal `json:"unrealizedTradingPnl"`
	Funding              decimal.Decimal `json:"funding"`
	Fees                 decimal.Decimal `json:"fees"`
	NetPnL               decimal.Decimal `json:"netPnl"`
	Verified             bool            `json:"verified"`
	VerificationStatus   string          `json:"verificationStatus"`
	CompleteThrough      time.Time       `json:"completeThrough"`
	Residual             Residual        `json:"residual"`
	AsOf                 time.Time       `json:"asOf"`
}

type positionState struct {
	quantity decimal.Decimal
	average  decimal.Decimal
	mark     decimal.Decimal
	realized decimal.Decimal
}

func Rebuild(source []Fact) (Projection, error) {
	facts := append([]Fact(nil), source...)
	sortFacts(facts)
	journal := NewJournal()
	for _, fact := range facts {
		if err := journal.Append(fact); err != nil {
			return Projection{}, err
		}
	}
	facts = journal.Facts()
	projection := Projection{}
	positions := make(map[string]positionState)
	currency := ""
	var completeness Completeness
	var execution ExecutionState
	for index, fact := range facts {
		if index == 0 {
			projection.ProductID, projection.Mode, projection.LineageID, projection.PositionID = fact.ProductID, fact.Mode, fact.LineageID, fact.PositionID
		} else if projectionKey(fact) != projectionKey(facts[0]) {
			return Projection{}, ErrScopeMismatch
		}
		if fact.EffectiveAt.After(projection.AsOf) {
			projection.AsOf = fact.EffectiveAt.UTC()
		}
		key := fact.AccountID + "\x00" + fact.InstrumentID
		factCurrency := fact.Asset
		if fact.Kind == KindFill {
			if fact.FeeAsset != fact.SettlementAsset {
				return Projection{}, ErrCurrencyMismatch
			}
			factCurrency = fact.SettlementAsset
		}
		if factCurrency != "" && (fact.Kind == KindFill || fact.Kind == KindFunding || fact.Kind == KindUnrealized) {
			if currency == "" {
				currency = factCurrency
			} else if factCurrency != currency {
				return Projection{}, ErrCurrencyMismatch
			}
		}
		switch fact.Kind {
		case KindFill:
			state := positions[key]
			applyFill(&state, fact)
			positions[key] = state
			projection.Fees = projection.Fees.Add(fact.Fee)
		case KindFunding:
			projection.Funding = projection.Funding.Add(fact.Amount)
		case KindMark:
			state := positions[key]
			state.mark = fact.Price
			positions[key] = state
		case KindUnrealized:
			projection.UnrealizedTradingPnL = projection.UnrealizedTradingPnL.Add(fact.Amount)
		case KindWatermark:
			completeness, projection.Verified, projection.CompleteThrough = fact.Completeness, fact.Verified, fact.EffectiveAt.UTC()
		case KindExecution:
			execution = fact.ExecutionState
		case KindResidual:
			projection.Residual = fact.Residual
		}
	}
	for _, state := range positions {
		projection.RealizedTradingPnL = projection.RealizedTradingPnL.Add(state.realized)
		if !state.quantity.IsZero() && (execution == ExecutionClosed || execution == ExecutionCompensatedFlat) {
			projection.Residual.LegQuantity = projection.Residual.LegQuantity.Add(state.quantity.Abs())
		}
		if !state.quantity.IsZero() && !state.mark.IsZero() {
			projection.UnrealizedTradingPnL = projection.UnrealizedTradingPnL.Add(state.mark.Sub(state.average).Mul(state.quantity))
		}
	}
	projection.NetPnL = projection.RealizedTradingPnL.Add(projection.UnrealizedTradingPnL).Add(projection.Funding).Sub(projection.Fees)
	projection.Status = statusFor(execution)
	complete := completeness == CompletenessComplete || completeness == CompletenessCompleteEmpty
	if projection.Residual.Present() {
		projection.Status, projection.Verified, projection.VerificationStatus = StatusAttention, false, "residual_exposure"
	} else if execution == ExecutionManualAttention {
		projection.Status, projection.Verified, projection.VerificationStatus = StatusAttention, false, "manual_attention"
	} else if !complete || !projection.Verified {
		projection.Verified, projection.VerificationStatus = false, "incomplete"
	} else {
		projection.VerificationStatus = "verified"
	}
	return projection, nil
}

func applyFill(state *positionState, fact Fact) {
	signed := fact.Quantity
	if fact.Side == SideSell {
		signed = signed.Neg()
	}
	if state.quantity.IsZero() || state.quantity.Sign() == signed.Sign() {
		total := state.quantity.Abs().Mul(state.average).Add(signed.Abs().Mul(fact.Price))
		state.quantity = state.quantity.Add(signed)
		state.average = total.Div(state.quantity.Abs())
		return
	}
	closed := decimal.Min(state.quantity.Abs(), signed.Abs())
	if state.quantity.IsPositive() {
		state.realized = state.realized.Add(fact.Price.Sub(state.average).Mul(closed))
	} else {
		state.realized = state.realized.Add(state.average.Sub(fact.Price).Mul(closed))
	}
	remaining := state.quantity.Add(signed)
	if remaining.IsZero() {
		state.average = decimal.Zero
	} else if remaining.Sign() != state.quantity.Sign() {
		state.average = fact.Price
	}
	state.quantity = remaining
}

func statusFor(state ExecutionState) PositionStatus {
	switch state {
	case ExecutionClosed, ExecutionCompensatedFlat:
		return StatusClosed
	case ExecutionManualAttention:
		return StatusAttention
	default:
		return StatusOpen
	}
}

func validateFact(fact Fact) error {
	if fact.FactID == "" || fact.Revision <= 0 || fact.ProductID != "funding" || fact.Mode != "test" ||
		fact.LineageID == "" || fact.PositionID == "" || fact.AccountID == "" || fact.InstrumentID == "" || fact.EffectiveAt.IsZero() {
		return ErrInvalidFact
	}
	switch fact.Kind {
	case KindFill:
		if fact.CommandID == "" || (fact.Side != SideBuy && fact.Side != SideSell) || !fact.Quantity.IsPositive() || !fact.Price.IsPositive() || fact.Fee.IsNegative() || fact.FeeAsset == "" || fact.SettlementAsset == "" {
			return ErrInvalidFact
		}
	case KindFunding:
		if fact.Asset == "" {
			return ErrInvalidFact
		}
	case KindMark:
		if !fact.Price.IsPositive() {
			return ErrInvalidFact
		}
	case KindWatermark:
		if fact.Completeness != CompletenessComplete && fact.Completeness != CompletenessCompleteEmpty && fact.Completeness != CompletenessIncomplete {
			return ErrInvalidFact
		}
		if fact.Verified && fact.Completeness != CompletenessComplete && fact.Completeness != CompletenessCompleteEmpty {
			return ErrInvalidFact
		}
	case KindExecution:
		if !validExecutionState(fact.ExecutionState) {
			return ErrInvalidFact
		}
	case KindUnrealized, KindResidual:
	default:
		return ErrInvalidFact
	}
	return nil
}

func validExecutionState(state ExecutionState) bool {
	switch state {
	case ExecutionHedgedOpen, ExecutionClosed, ExecutionCompensatedFlat, ExecutionManualAttention:
		return true
	default:
		return false
	}
}

func scopeKey(fact Fact) string {
	return strings.Join([]string{fact.ProductID, fact.Mode, fact.LineageID, fact.PositionID, fact.AccountID, fact.InstrumentID}, "\x00")
}

func projectionKey(fact Fact) string {
	return strings.Join([]string{fact.ProductID, fact.Mode, fact.LineageID, fact.PositionID}, "\x00")
}

func sortFacts(facts []Fact) {
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Revision == facts[j].Revision {
			return facts[i].FactID < facts[j].FactID
		}
		return facts[i].Revision < facts[j].Revision
	})
}

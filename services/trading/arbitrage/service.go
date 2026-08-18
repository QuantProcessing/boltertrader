// Package arbitrage executes the cross-venue funding hedge: one underlying, two
// venues, one long leg and one short leg, held for the funding spread rather
// than for direction.
//
// It is trading's second product and deliberately its thinnest. Everything
// durable — commands, reservations, the portfolio projection, the ledger, the
// reconciler — is the machinery manual already uses, parameterised by
// ProductID, and the order channel is the same Gateway adapter. Two things are
// genuinely new: a leg's identity travels inside the intent, and a lineage's
// legs get one verdict (ClassifyPair) that is allowed to say "partially open"
// out loud.
//
// What this package will not do is unwind a half-open pair by itself. A leg
// that filled while its sibling failed is a real directional position somebody
// has to decide about, and closing it automatically would be the service
// placing an order nobody asked for — the line the operator drew when they
// chose preview-plus-one-click over a background worker.
package arbitrage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/shopspring/decimal"
)

// ProductID and the command kinds are exported because the HTTP layer, the
// reconciliation dispatcher and the tests all have to name the same strings,
// and a typo in any one of them would route a real order to the wrong product.
const (
	ProductID        = "arbitrage"
	CommandKindPlace = "arbitrage.place"
)

// previewTokenPrefix and previewDomain let this product share manual's signing
// key without sharing its previews.
//
// The key is one secret for the whole service, so the separation has to come
// from the message: every arbitrage MAC is computed over an extra leading
// domain label, which means an arbitrage preview presented to manual's opener
// (or the reverse) fails the comparison rather than opening. Without that
// label, a sealed two-leg payload would verify against manual's single-leg
// route and be decoded as an intent nobody previewed.
const (
	previewTokenPrefix = "arbitrage_preview_"
	previewDomain      = "arbitrage"
)

// Leg names which side of the hedge a command belongs to.
//
// It is not the same thing as the order's side: the long leg buys to open and
// sells to close. Carrying the leg rather than inferring it from the side is
// what lets a closing command still say which half of the pair it belongs to.
type Leg string

const (
	LegLong  Leg = "long"
	LegShort Leg = "short"
)

// PairState is a lineage's verdict, read off its legs.
//
// PairStatePartiallyOpen is the one that earns this type. Every other state has
// an obvious rendering; that one means the operator is carrying unhedged delta
// they did not ask for, and it exists so the interface can say so instead of
// showing two rows that each look individually fine.
type PairState string

const (
	PairStateOpening       PairState = "opening"
	PairStateOpen          PairState = "open"
	PairStatePartiallyOpen PairState = "partially_open"
	PairStateClosing       PairState = "closing"
	PairStateClosed        PairState = "closed"
	PairStateFailed        PairState = "failed"
)

// LegView is one command reduced to what a pair needs to know about it.
//
// The HTTP layer builds these from orders.Command so that the intent is decoded
// in exactly one place; a second decoder that disagreed with this one would put
// a leg in the wrong half of the pair.
type LegView struct {
	Leg          Leg          `json:"leg"`
	CommandID    string       `json:"commandId"`
	LineageID    string       `json:"lineageId"`
	AccountID    string       `json:"accountId"`
	InstrumentID string       `json:"instrumentId"`
	Side         string       `json:"side"`
	Quantity     string       `json:"quantity"`
	OrderType    string       `json:"orderType"`
	LimitPrice   string       `json:"limitPrice,omitempty"`
	ReduceOnly   bool         `json:"reduceOnly"`
	State        orders.State `json:"state"`
	CreatedAt    time.Time    `json:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
}

// placeIntent is arbitrage's own payload shape.
//
// It cannot be manual's placeIntent plus a field: that decoder rejects unknown
// fields and would refuse anything carrying a leg, and manual's Execute refuses
// a command whose ProductID is not "manual" anyway. So the two intents are
// siblings rather than one extending the other, and the extra fields are the
// pairing facts.
//
// counterAccountId/counterInstrumentId name the sibling leg rather than its
// command id. The command id cannot be here: the two commands are created in
// sequence, so the first leg's payload would have to reference an id that does
// not exist yet, and the payload is hashed for idempotency and therefore cannot
// be patched afterwards. The account and instrument are known before either
// command is minted, they identify the sibling just as well for a human reading
// the blotter, and the authoritative pairing key is the shared LineageID.
type placeIntent struct {
	Leg                  string `json:"leg"`
	CounterAccountID     string `json:"counterAccountId"`
	CounterInstrumentID  string `json:"counterInstrumentId"`
	InstrumentID         string `json:"instrumentId"`
	Side                 string `json:"side"`
	Quantity             string `json:"quantity"`
	OrderType            string `json:"orderType"`
	LimitPrice           string `json:"limitPrice,omitempty"`
	TimeInForce          string `json:"timeInForce,omitempty"`
	ReduceOnly           bool   `json:"reduceOnly,omitempty"`
	RouteID              string `json:"routeId,omitempty"`
	DesiredStateRevision int64  `json:"desiredStateRevision,omitempty"`
	RuntimeGeneration    int64  `json:"runtimeGeneration,omitempty"`
	CredentialVersion    int64  `json:"credentialVersion,omitempty"`
}

// Service holds the same dependencies manual's does, and for the same reasons.
// The Gateway, the request and the result types are manual's exported ones
// rather than copies: both products place orders down one channel, and a second
// set of structurally identical types would let the adapter drift under one
// product without the other noticing.
type Service struct {
	commands   *orders.Service
	portfolio  *portfolio.Projection
	ledger     *ledger.Service
	gateway    manual.Gateway
	previewKey []byte
	now        func() time.Time
	liveWrites bool
}

func NewGatewayServiceWithPreviewKey(commands *orders.Service, portfolioProjection *portfolio.Projection, ledgerService *ledger.Service, gateway manual.Gateway, now func() time.Time, previewKey []byte) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{commands: commands, portfolio: portfolioProjection, ledger: ledgerService, gateway: gateway, previewKey: bytes.Clone(previewKey), now: now}
}

// SetExecutionAdmission used to take the service's mode alongside this switch,
// and refused any pairing that armed live writes under a test mode. With one
// mode left there is nothing to pair the switch against.
func (service *Service) SetExecutionAdmission(liveWrites bool) error {
	if service == nil {
		return fmt.Errorf("arbitrage execution admission is invalid")
	}
	service.liveWrites = liveWrites
	return nil
}

func (service *Service) Ready() bool {
	return service != nil && service.commands != nil && service.portfolio != nil && service.ledger != nil && service.gateway != nil
}

func (service *Service) PreviewNow() time.Time {
	if service == nil || service.now == nil {
		return time.Time{}
	}
	return service.now().UTC()
}

func (service *Service) SealPreview(principal string, payload []byte) (string, error) {
	if service == nil || len(service.previewKey) < sha256.Size || principal == "" || len(payload) == 0 {
		return "", fmt.Errorf("arbitrage preview signing dependencies are required")
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := service.previewSignature(principal, encoded)
	return previewTokenPrefix + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (service *Service) OpenPreview(principal string, token string) ([]byte, error) {
	if service == nil || len(service.previewKey) < sha256.Size || principal == "" {
		return nil, fmt.Errorf("arbitrage preview signing dependencies are required")
	}
	value := strings.TrimPrefix(token, previewTokenPrefix)
	if value == token {
		return nil, fmt.Errorf("invalid arbitrage preview token")
	}
	encoded, signatureText, found := strings.Cut(value, ".")
	if !found || encoded == "" || signatureText == "" {
		return nil, fmt.Errorf("invalid arbitrage preview token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signatureText)
	if err != nil || !hmac.Equal(signature, service.previewSignature(principal, encoded)) {
		return nil, fmt.Errorf("invalid arbitrage preview token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) == 0 {
		return nil, fmt.Errorf("invalid arbitrage preview token")
	}
	return payload, nil
}

// previewSignature binds a token to the principal that minted it, under this
// product's own domain. The mode used to be a fourth field, between the
// principal and the payload, so that a token sealed under test could not be
// opened under live. Every token is live now.
func (service *Service) previewSignature(principal string, encoded string) []byte {
	mac := hmac.New(sha256.New, service.previewKey)
	_, _ = mac.Write([]byte(previewDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(principal))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(encoded))
	return mac.Sum(nil)
}

// Execute places one leg.
//
// It is deliberately per-leg and knows nothing about its sibling. Two legs are
// two commands, submitted one after the other by the HTTP layer, and this
// method's refusal to look sideways is what guarantees a failure on the second
// leg cannot roll back the first: there is no code path here that could.
func (service *Service) Execute(ctx context.Context, command orders.Command) (orders.Command, error) {
	if !service.Ready() {
		return command, fmt.Errorf("arbitrage execution dependencies are required")
	}
	if command.ProductID != ProductID || command.CommandKind != CommandKindPlace {
		return command, nil
	}
	// The command used to have to declare the same mode this service was wired
	// with, and only a live command was subject to the switch below. Both halves
	// went with the mode axis: every command is live, so the switch alone
	// decides whether this call reaches the venue.
	if !service.liveWrites {
		return command, orders.ErrLiveWriteDisabled
	}

	current, err := service.commands.Get(ctx, command.CommandID)
	if err != nil {
		return command, err
	}
	if terminalState(current.State) {
		return current, nil
	}

	intent, err := decodePlaceIntent(current.Payload)
	if err != nil {
		failed, transitionErr := service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateFailed})
		if transitionErr != nil {
			return current, fmt.Errorf("arbitrage intent is invalid: %w", err)
		}
		return failed, fmt.Errorf("arbitrage intent is invalid: %w", err)
	}
	request := gatewayRequest(current, intent)
	result, err := service.gateway.Execute(ctx, request)
	if err != nil {
		switch result.Handoff.Outcome {
		case orders.HandoffNotAttempted, orders.HandoffRejected:
			updated, transitionErr := service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateFailed})
			if transitionErr != nil {
				return current, transitionErr
			}
			return updated, fmt.Errorf("Gateway execution handoff: %w", err)
		default:
			updated, transitionErr := service.markUncertain(ctx, current, orders.ReconciliationAmbiguousHandoff, result.Handoff.Evidence)
			if transitionErr != nil {
				return updated, errors.Join(fmt.Errorf("Gateway execution handoff: %w", err), transitionErr)
			}
			return updated, fmt.Errorf("Gateway execution handoff: %w", err)
		}
	}
	if err := validateGatewayResult(request, result); err != nil {
		updated, transitionErr := service.markUncertain(ctx, current, orders.ReconciliationEvidenceInvalid, result.Handoff.Evidence)
		if transitionErr != nil {
			return updated, errors.Join(fmt.Errorf("Gateway execution facts are invalid: %w", err), transitionErr)
		}
		return updated, fmt.Errorf("Gateway execution facts are invalid: %w", err)
	}
	return service.recordGatewayResult(ctx, current, result)
}

// Cancel withdraws a resting leg.
//
// A half-open pair has two remedies and they are not interchangeable: a leg
// that filled has to be closed by a reverse reduce-only order, and a leg still
// resting on the book has to be cancelled. Offering only the close would leave
// a resting leg unreachable, because a reduce-only close against a position
// that does not exist yet is rejected by the venue.
func (service *Service) Cancel(ctx context.Context, command orders.Command) (orders.Command, error) {
	if !service.Ready() {
		return command, fmt.Errorf("arbitrage execution dependencies are required")
	}
	if command.ProductID != ProductID || command.CommandKind != CommandKindPlace {
		return command, fmt.Errorf("arbitrage cancel requires a leg command")
	}
	// Same collapse as Execute: the mode agreement is gone and the operator
	// switch is what remains.
	if !service.liveWrites {
		return command, orders.ErrLiveWriteDisabled
	}
	current, err := service.commands.Get(ctx, command.CommandID)
	if err != nil {
		return command, err
	}
	if terminalState(current.State) {
		return current, nil
	}
	if current.ExecutionEvidence.GatewayOrderID == "" || current.ExecutionEvidence.CommandID == "" {
		return current, fmt.Errorf("arbitrage leg has no authoritative Gateway identity")
	}
	intent, err := decodePlaceIntent(current.Payload)
	if err != nil {
		return current, fmt.Errorf("arbitrage intent is invalid: %w", err)
	}
	authority := gatewayRequest(current, intent)
	gateway, ok := service.gateway.(manual.CancelGateway)
	if !ok {
		return current, fmt.Errorf("arbitrage Gateway cancellation is unavailable")
	}
	result, err := gateway.Cancel(ctx, manual.GatewayCancelRequest{
		CommandID: current.CommandID, AccountID: current.AccountID,
		InstrumentID: authority.InstrumentID, GatewayOrderID: current.ExecutionEvidence.GatewayOrderID,
	})
	if err != nil {
		switch result.Handoff.Outcome {
		case orders.HandoffNotAttempted, orders.HandoffRejected:
			return current, fmt.Errorf("Gateway cancel handoff: %w", err)
		default:
			updated, transitionErr := service.markUncertain(ctx, current, orders.ReconciliationAmbiguousHandoff, result.Handoff.Evidence)
			if transitionErr != nil {
				return updated, errors.Join(fmt.Errorf("Gateway cancel handoff: %w", err), transitionErr)
			}
			return updated, fmt.Errorf("Gateway cancel handoff: %w", err)
		}
	}
	if err := validateGatewayResult(authority, result); err != nil {
		updated, transitionErr := service.markUncertain(ctx, current, orders.ReconciliationEvidenceInvalid, result.Handoff.Evidence)
		if transitionErr != nil {
			return updated, errors.Join(fmt.Errorf("Gateway cancel facts are invalid: %w", err), transitionErr)
		}
		return updated, fmt.Errorf("Gateway cancel facts are invalid: %w", err)
	}
	return service.recordGatewayResult(ctx, current, result)
}

// RecordReconciliation settles a leg the reconciler chased down.
//
// The dispatcher in services/trading/app routes here by ProductID. Without it,
// an arbitrage leg that ended uncertain would be retried forever against a
// product that refuses to answer for it, and the pair would never leave
// "opening" even though the venue had long since decided.
func (service *Service) RecordReconciliation(ctx context.Context, command orders.Command, handoff orders.HandoffResult) (orders.Command, error) {
	if handoff.Outcome == orders.HandoffRejected {
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: command.CommandID, State: orders.StateFailed, Evidence: handoff.Evidence})
	}
	if handoff.Outcome != orders.HandoffAccepted {
		return command, fmt.Errorf("Gateway reconciliation outcome is not authoritative")
	}
	current, err := service.commands.Get(ctx, command.CommandID)
	if err != nil {
		return command, err
	}
	intent, err := decodePlaceIntent(current.Payload)
	if err != nil {
		return current, err
	}
	request := gatewayRequest(current, intent)
	result := manual.GatewayResult{CommandID: current.CommandID, AccountID: current.AccountID, State: reconciledGatewayState(handoff.Evidence.State), Handoff: handoff}
	for _, fill := range handoff.Evidence.Fills {
		accountID := fill.AccountID
		if accountID == "" {
			accountID = current.AccountID
		}
		result.Fills = append(result.Fills, manual.FillFact{FillID: fill.FillID, CommandID: current.CommandID, AccountID: accountID, InstrumentID: fill.InstrumentID, Side: portfolio.Side(fill.Side), Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency, SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt})
	}
	if result.State == manual.GatewayStateDenied {
		result.DenialCode = "gateway_rejected"
	}
	if err := validateGatewayResult(request, result); err != nil {
		return current, err
	}
	return service.recordGatewayResult(ctx, current, result)
}

func (service *Service) recordGatewayResult(ctx context.Context, current orders.Command, result manual.GatewayResult) (orders.Command, error) {
	switch result.State {
	case manual.GatewayStateDenied:
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateFailed, Evidence: result.Handoff.Evidence})
	case manual.GatewayStateSubmitted:
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateSubmitted, Evidence: result.Handoff.Evidence})
	case manual.GatewayStatePartiallyFilled:
		if err := service.applyFills(ctx, current, result.Fills); err != nil {
			return current, err
		}
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateSubmitted, Evidence: result.Handoff.Evidence})
	case manual.GatewayStateFilled:
		if err := service.applyFills(ctx, current, result.Fills); err != nil {
			updated, transitionErr := service.markUncertain(ctx, current, orders.ReconciliationEvidenceInvalid, result.Handoff.Evidence)
			if transitionErr != nil {
				return updated, errors.Join(err, transitionErr)
			}
			return updated, err
		}
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateFilled, Evidence: result.Handoff.Evidence})
	case manual.GatewayStateCanceled:
		if err := service.applyFills(ctx, current, result.Fills); err != nil {
			updated, transitionErr := service.markUncertain(ctx, current, orders.ReconciliationEvidenceInvalid, result.Handoff.Evidence)
			if transitionErr != nil {
				return updated, errors.Join(err, transitionErr)
			}
			return updated, err
		}
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateCanceled, Evidence: result.Handoff.Evidence})
	default:
		// validateGatewayResult ran first and rejects every other state, so
		// reaching here means the two disagree — a bug, not a venue condition.
		return current, fmt.Errorf("validated Gateway state is unhandled")
	}
}

func (service *Service) applyFills(ctx context.Context, command orders.Command, fills []manual.FillFact) error {
	for _, fact := range fills {
		_, result, err := service.portfolio.ApplyFill(ctx, portfolio.Fill{
			PrincipalID: command.PrincipalID, FillID: fact.FillID, CommandID: fact.CommandID, LineageID: command.LineageID, ProductID: command.ProductID,
			AccountID: fact.AccountID, InstrumentID: fact.InstrumentID, Side: fact.Side,
			Quantity: fact.Quantity, Price: fact.Price, Fee: fact.Fee, FeeCurrency: fact.FeeCurrency, SettlementCurrency: fact.SettlementCurrency, OccurredAt: fact.OccurredAt,
		})
		if err != nil {
			return fmt.Errorf("apply authoritative fill %q: %w", fact.FillID, err)
		}
		if err := service.ledger.Record(ctx, ledger.Entry{
			PrincipalID: command.PrincipalID, EntryID: fact.FillID + ":fee", LineageID: command.LineageID, ProductID: command.ProductID,
			AccountID: fact.AccountID, InstrumentID: fact.InstrumentID, Kind: ledger.KindFee, Amount: result.Fee,
			Currency: fact.FeeCurrency, OccurredAt: fact.OccurredAt,
		}); err != nil {
			return fmt.Errorf("record authoritative fill fee %q: %w", fact.FillID, err)
		}
		if result.RealizedPnL != "0" {
			if err := service.ledger.Record(ctx, ledger.Entry{
				PrincipalID: command.PrincipalID, EntryID: fact.FillID + ":realized_pnl", LineageID: command.LineageID, ProductID: command.ProductID,
				AccountID: fact.AccountID, InstrumentID: fact.InstrumentID, Kind: ledger.KindRealizedPnL, Amount: result.RealizedPnL,
				Currency: fact.SettlementCurrency, OccurredAt: fact.OccurredAt,
			}); err != nil {
				return fmt.Errorf("record authoritative fill PnL %q: %w", fact.FillID, err)
			}
		}
	}
	return nil
}

func (service *Service) markUncertain(ctx context.Context, command orders.Command, reason orders.ReconciliationReason, evidence orders.ExecutionEvidence) (orders.Command, error) {
	updated, err := service.commands.Transition(ctx, orders.TransitionRequest{
		CommandID: command.CommandID, State: orders.StateUncertain, Reason: reason,
		NextAttemptAt: service.now().UTC().Add(time.Minute), Evidence: evidence,
	})
	if err != nil {
		command.ExecutionEvidence = evidence
		return command, err
	}
	return updated, nil
}

/* --- Intent -------------------------------------------------------------- */

// DescribeLeg reduces a stored command to its pairing facts.
//
// Exported so the HTTP layer never has to decode the payload itself: one
// decoder means the leg a command is filed under on the positions screen is
// necessarily the leg the executor placed it as.
func DescribeLeg(command orders.Command) (LegView, error) {
	intent, err := decodePlaceIntent(command.Payload)
	if err != nil {
		return LegView{}, err
	}
	return LegView{
		Leg: Leg(intent.Leg), CommandID: command.CommandID, LineageID: command.LineageID,
		AccountID: command.AccountID, InstrumentID: intent.InstrumentID, Side: intent.Side,
		Quantity: intent.Quantity, OrderType: intent.OrderType, LimitPrice: intent.LimitPrice,
		ReduceOnly: intent.ReduceOnly, State: command.State,
		CreatedAt: command.CreatedAt, UpdatedAt: command.UpdatedAt,
	}, nil
}

// EncodeIntent builds a leg's payload.
//
// The HTTP layer calls this rather than marshalling a map, so that the field
// names the executor decodes and the field names the placer writes cannot drift
// apart — DisallowUnknownFields turns any drift into a leg that fails at
// execution time, after the sibling has already gone to the venue.
func EncodeIntent(leg Leg, instrumentID, side, quantity, orderType, limitPrice, timeInForce, routeID, counterAccountID, counterInstrumentID string, reduceOnly bool, desiredStateRevision, runtimeGeneration, credentialVersion int64) (json.RawMessage, error) {
	intent := placeIntent{
		Leg: string(leg), CounterAccountID: counterAccountID, CounterInstrumentID: counterInstrumentID,
		InstrumentID: instrumentID, Side: side, Quantity: quantity, OrderType: strings.ToLower(orderType),
		LimitPrice: limitPrice, TimeInForce: timeInForce, ReduceOnly: reduceOnly, RouteID: routeID,
		DesiredStateRevision: desiredStateRevision, RuntimeGeneration: runtimeGeneration, CredentialVersion: credentialVersion,
	}
	payload, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	// Validate what was just built rather than trusting it: this is the last
	// point before a payload becomes a durable, hash-pinned command.
	if _, err := decodePlaceIntent(payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func decodePlaceIntent(payload json.RawMessage) (placeIntent, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var intent placeIntent
	if err := decoder.Decode(&intent); err != nil {
		return intent, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return intent, fmt.Errorf("intent must contain exactly one JSON object")
	}
	if Leg(intent.Leg) != LegLong && Leg(intent.Leg) != LegShort {
		return intent, fmt.Errorf("leg must be long or short")
	}
	if intent.CounterAccountID == "" || intent.CounterInstrumentID == "" {
		return intent, fmt.Errorf("leg must name its counterpart")
	}
	if intent.InstrumentID == "" {
		return intent, fmt.Errorf("instrument is required")
	}
	quantity, err := decimal.NewFromString(intent.Quantity)
	if err != nil || !quantity.IsPositive() {
		return intent, fmt.Errorf("positive quantity is required")
	}
	// The side is not free: the hedge is delta-neutral only if the long leg
	// buys to open and sells to close, and the short leg does the reverse.
	// Checking it here means a mis-built payload fails before it reaches a
	// venue, rather than opening two same-direction positions that look like a
	// pair on the screen and like double exposure in the account.
	if intent.Side != string(portfolio.SideBuy) && intent.Side != string(portfolio.SideSell) {
		return intent, fmt.Errorf("side must be buy or sell")
	}
	if intent.Side != openingSide(Leg(intent.Leg), intent.ReduceOnly) {
		return intent, fmt.Errorf("leg %q must be %s when reduceOnly is %t", intent.Leg, openingSide(Leg(intent.Leg), intent.ReduceOnly), intent.ReduceOnly)
	}
	if intent.DesiredStateRevision != 0 || intent.RuntimeGeneration != 0 || intent.CredentialVersion != 0 {
		if intent.DesiredStateRevision < 1 || intent.RuntimeGeneration < 1 || intent.CredentialVersion < 1 {
			return intent, fmt.Errorf("account write fence is incomplete")
		}
	}
	intent.OrderType = strings.ToLower(intent.OrderType)
	switch intent.OrderType {
	case "market":
		if intent.LimitPrice != "" {
			return intent, fmt.Errorf("market intent cannot specify limit price")
		}
	case "limit":
		price, priceErr := decimal.NewFromString(intent.LimitPrice)
		if priceErr != nil || !price.IsPositive() {
			return intent, fmt.Errorf("limit intent requires a positive limit price")
		}
	default:
		return intent, fmt.Errorf("order type must be market or limit")
	}
	return intent, nil
}

// openingSide is the only place the leg-to-side mapping is written down.
func openingSide(leg Leg, reduceOnly bool) string {
	buys := leg == LegLong
	if reduceOnly {
		buys = !buys
	}
	if buys {
		return string(portfolio.SideBuy)
	}
	return string(portfolio.SideSell)
}

// ReduceSide is the side that closes the given leg, for the HTTP layer to build
// the reverse order with.
func ReduceSide(leg Leg) string { return openingSide(leg, true) }

// OpenSide is the side that opens the given leg.
func OpenSide(leg Leg) string { return openingSide(leg, false) }

func gatewayRequest(command orders.Command, intent placeIntent) manual.GatewayRequest {
	request := manual.GatewayRequest{
		CommandID: command.CommandID, IdempotencyKey: command.IdempotencyKey, AccountID: command.AccountID,
		InstrumentID: intent.InstrumentID, Side: intent.Side, Quantity: intent.Quantity, OrderType: intent.OrderType,
		LimitPrice: intent.LimitPrice, TimeInForce: intent.TimeInForce, ReduceOnly: intent.ReduceOnly, RouteID: intent.RouteID,
		DesiredStateRevision: intent.DesiredStateRevision, RuntimeGeneration: intent.RuntimeGeneration, CredentialVersion: intent.CredentialVersion,
	}
	if command.ReservationID != nil {
		request.ReservationID = *command.ReservationID
	}
	return request
}

func validateGatewayResult(request manual.GatewayRequest, result manual.GatewayResult) error {
	if result.CommandID != request.CommandID || result.AccountID != request.AccountID {
		return fmt.Errorf("command authority identity mismatch")
	}
	if result.Handoff.Outcome != "" && (result.Handoff.Outcome != orders.HandoffAccepted || result.Handoff.Evidence.CommandID == "" || result.Handoff.Evidence.GatewayOrderID == "" || result.Handoff.Evidence.ClientOrderID == "" || result.Handoff.Evidence.ResourceVersion < 1) {
		return fmt.Errorf("Gateway execution evidence is invalid")
	}
	switch result.State {
	case manual.GatewayStateDenied:
		if result.DenialCode == "" || len(result.Fills) != 0 {
			return fmt.Errorf("denial must have a code and no fill facts")
		}
	case manual.GatewayStateSubmitted:
		if result.DenialCode != "" || len(result.Fills) != 0 {
			return fmt.Errorf("submitted result cannot contain denial or fill facts")
		}
	case manual.GatewayStatePartiallyFilled, manual.GatewayStateFilled:
		if result.DenialCode != "" || len(result.Fills) == 0 {
			return fmt.Errorf("filled result requires fill facts")
		}
	case manual.GatewayStateCanceled:
		if result.DenialCode != "" {
			return fmt.Errorf("canceled result cannot contain denial")
		}
	default:
		return fmt.Errorf("unknown Gateway state")
	}

	seen := make(map[string]struct{}, len(result.Fills))
	for _, fill := range result.Fills {
		if fill.CommandID != request.CommandID || fill.AccountID != request.AccountID || fill.InstrumentID != request.InstrumentID || string(fill.Side) != request.Side {
			return fmt.Errorf("fill authority identity mismatch")
		}
		if _, duplicate := seen[fill.FillID]; duplicate {
			return fmt.Errorf("duplicate fill identity")
		}
		seen[fill.FillID] = struct{}{}
		if err := validateFillFact(fill); err != nil {
			return err
		}
	}
	return nil
}

func validateFillFact(fill manual.FillFact) error {
	quantity, quantityErr := decimal.NewFromString(fill.Quantity)
	price, priceErr := decimal.NewFromString(fill.Price)
	fee, feeErr := decimal.NewFromString(fill.Fee)
	if fill.FillID == "" || fill.FeeCurrency == "" || fill.SettlementCurrency == "" || fill.OccurredAt.IsZero() || quantityErr != nil || priceErr != nil || feeErr != nil || !quantity.IsPositive() || !price.IsPositive() || fee.IsNegative() {
		return fmt.Errorf("fill facts are incomplete or invalid")
	}
	return nil
}

func reconciledGatewayState(state string) manual.GatewayState {
	switch strings.ToLower(state) {
	case "filled":
		return manual.GatewayStateFilled
	case "partially_filled":
		return manual.GatewayStatePartiallyFilled
	case "canceled", "cancelled":
		return manual.GatewayStateCanceled
	case "rejected", "failed":
		return manual.GatewayStateDenied
	default:
		return manual.GatewayStateSubmitted
	}
}

func terminalState(state orders.State) bool {
	return state == orders.StateFilled || state == orders.StateCanceled || state == orders.StateFailed
}

/* --- The pair ------------------------------------------------------------ */

// ClassifyPair reads one verdict off a lineage's legs.
//
// A hedge is two orders, and two orders have nine outcome combinations that the
// operator does not want to enumerate by eye. The rule below collapses them,
// and the collapse is deliberately pessimistic in one direction: a leg with no
// command at all counts as dead, so a pair whose second Create never happened
// reports partially_open rather than opening forever.
//
// Legs are keyed by (role, leg) and the newest wins, because a retried close
// after a failed one is the same leg making a second attempt, not a third leg.
func ClassifyPair(legs []LegView) PairState {
	opening, closing := newestByLeg(legs, false), newestByLeg(legs, true)

	openFilled, openInflight := 0, 0
	for _, leg := range []Leg{LegLong, LegShort} {
		view, present := opening[leg]
		switch {
		case !present:
			// A leg that was never commanded is a leg that will not fill.
		case view.State == orders.StateFilled:
			openFilled++
		case !terminalState(view.State):
			openInflight++
		}
	}
	if openInflight > 0 {
		return PairStateOpening
	}
	if openFilled == 0 {
		return PairStateFailed
	}

	closeFilled, closeInflight := 0, 0
	for _, leg := range []Leg{LegLong, LegShort} {
		if view, present := opening[leg]; !present || view.State != orders.StateFilled {
			continue
		}
		view, present := closing[leg]
		switch {
		case !present:
		case view.State == orders.StateFilled:
			closeFilled++
		case !terminalState(view.State):
			closeInflight++
		}
	}
	switch {
	case closeFilled == openFilled:
		return PairStateClosed
	case closeInflight > 0 || closeFilled > 0:
		return PairStateClosing
	case openFilled == 2:
		return PairStateOpen
	default:
		return PairStatePartiallyOpen
	}
}

func newestByLeg(legs []LegView, reduceOnly bool) map[Leg]LegView {
	newest := make(map[Leg]LegView, 2)
	for _, leg := range legs {
		if leg.ReduceOnly != reduceOnly {
			continue
		}
		if previous, exists := newest[leg.Leg]; exists && previous.CreatedAt.After(leg.CreatedAt) {
			continue
		}
		newest[leg.Leg] = leg
	}
	return newest
}

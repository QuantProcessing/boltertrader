package manual

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
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/shopspring/decimal"
)

type GatewayState string

const (
	GatewayStateSubmitted       GatewayState = "submitted"
	GatewayStatePartiallyFilled GatewayState = "partially_filled"
	GatewayStateFilled          GatewayState = "filled"
	GatewayStateCanceled        GatewayState = "canceled"
	GatewayStateDenied          GatewayState = "denied"
)

type GatewayRequest struct {
	CommandID, IdempotencyKey, ReservationID string
	AccountID, InstrumentID, Side            string
	Quantity, OrderType, LimitPrice          string
	TimeInForce                              string
	Mode                                     orders.Mode
	ReduceOnly                               bool
	DesiredStateRevision, RuntimeGeneration  int64
	CredentialVersion                        int64
}

type GatewayCancelRequest struct {
	CommandID, AccountID, InstrumentID, GatewayOrderID string
	Mode                                               orders.Mode
}

type FillFact struct {
	FillID, CommandID, AccountID, InstrumentID            string
	Mode                                                  orders.Mode
	Side                                                  portfolio.Side
	Quantity, Price, Fee, FeeCurrency, SettlementCurrency string
	OccurredAt                                            time.Time
}

type GatewayResult struct {
	CommandID, AccountID string
	Mode                 orders.Mode
	State                GatewayState
	DenialCode           string
	Fills                []FillFact
	Handoff              orders.HandoffResult
}

type Gateway interface {
	Execute(context.Context, GatewayRequest) (GatewayResult, error)
}

type CancelGateway interface {
	Cancel(context.Context, GatewayCancelRequest) (GatewayResult, error)
}

type Service struct {
	commands   *orders.Service
	portfolio  *portfolio.Projection
	ledger     *ledger.Service
	gateway    Gateway
	previewKey []byte
	now        func() time.Time
	mode       orders.Mode
	liveWrites bool
}

type placeIntent struct {
	InstrumentID         string `json:"instrumentId"`
	Side                 string `json:"side"`
	Quantity             string `json:"quantity"`
	OrderType            string `json:"orderType"`
	LimitPrice           string `json:"limitPrice,omitempty"`
	TimeInForce          string `json:"timeInForce,omitempty"`
	ReduceOnly           bool   `json:"reduceOnly,omitempty"`
	DesiredStateRevision int64  `json:"desiredStateRevision,omitempty"`
	RuntimeGeneration    int64  `json:"runtimeGeneration,omitempty"`
	CredentialVersion    int64  `json:"credentialVersion,omitempty"`
}

// NewService remains as a compile-compatible transition point until app wiring
// supplies the required Gateway adapter through NewGatewayService.
func NewService(commands *orders.Service, portfolioProjection *portfolio.Projection, ledgerService *ledger.Service, now func() time.Time) *Service {
	return NewGatewayService(commands, portfolioProjection, ledgerService, nil, now)
}

func NewGatewayService(commands *orders.Service, portfolioProjection *portfolio.Projection, ledgerService *ledger.Service, gateway Gateway, now func() time.Time) *Service {
	return NewGatewayServiceWithPreviewKey(commands, portfolioProjection, ledgerService, gateway, now, nil)
}

func NewGatewayServiceWithPreviewKey(commands *orders.Service, portfolioProjection *portfolio.Projection, ledgerService *ledger.Service, gateway Gateway, now func() time.Time, previewKey []byte) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{commands: commands, portfolio: portfolioProjection, ledger: ledgerService, gateway: gateway, previewKey: bytes.Clone(previewKey), now: now, mode: orders.ModeTest}
}

func (service *Service) SetExecutionAdmission(mode orders.Mode, liveWrites bool) error {
	if service == nil || (mode != orders.ModeTest && mode != orders.ModeLive) || (liveWrites && mode != orders.ModeLive) {
		return fmt.Errorf("manual execution admission is invalid")
	}
	service.mode = mode
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

func (service *Service) SealPreview(principal string, mode orders.Mode, payload []byte) (string, error) {
	if service == nil || len(service.previewKey) < sha256.Size || principal == "" || (mode != orders.ModeTest && mode != orders.ModeLive) || len(payload) == 0 {
		return "", fmt.Errorf("manual preview signing dependencies are required")
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := service.previewSignature(principal, mode, encoded)
	return "manual_preview_" + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (service *Service) OpenPreview(principal string, mode orders.Mode, token string) ([]byte, error) {
	if service == nil || len(service.previewKey) < sha256.Size || principal == "" || (mode != orders.ModeTest && mode != orders.ModeLive) {
		return nil, fmt.Errorf("manual preview signing dependencies are required")
	}
	value := strings.TrimPrefix(token, "manual_preview_")
	if value == token {
		return nil, fmt.Errorf("invalid manual preview token")
	}
	encoded, signatureText, found := strings.Cut(value, ".")
	if !found || encoded == "" || signatureText == "" {
		return nil, fmt.Errorf("invalid manual preview token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signatureText)
	if err != nil || !hmac.Equal(signature, service.previewSignature(principal, mode, encoded)) {
		return nil, fmt.Errorf("invalid manual preview token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) == 0 {
		return nil, fmt.Errorf("invalid manual preview token")
	}
	return payload, nil
}

func (service *Service) previewSignature(principal string, mode orders.Mode, encoded string) []byte {
	mac := hmac.New(sha256.New, service.previewKey)
	_, _ = mac.Write([]byte(principal))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(mode))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(encoded))
	return mac.Sum(nil)
}

func (service *Service) Execute(ctx context.Context, command orders.Command) (orders.Command, error) {
	if !service.Ready() {
		return command, fmt.Errorf("manual execution dependencies are required")
	}
	if command.ProductID != "manual" || command.CommandKind != "manual.place" {
		return command, nil
	}
	if command.Mode != service.mode || (command.Mode == orders.ModeLive && !service.liveWrites) {
		return command, orders.ErrLiveWriteDisabled
	}

	current, err := service.commands.Get(ctx, command.CommandID)
	if err != nil {
		return command, err
	}
	if current.State == orders.StateFilled || current.State == orders.StateCanceled || current.State == orders.StateFailed {
		return current, nil
	}

	intent, err := decodePlaceIntent(current.Payload)
	if err != nil {
		failed, transitionErr := service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateFailed})
		if transitionErr != nil {
			return current, fmt.Errorf("manual intent is invalid: %w", err)
		}
		return failed, fmt.Errorf("manual intent is invalid: %w", err)
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
	return service.recordGatewayResult(ctx, current, request, result)
}

func (service *Service) Cancel(ctx context.Context, command orders.Command) (orders.Command, error) {
	if !service.Ready() {
		return command, fmt.Errorf("manual execution dependencies are required")
	}
	if command.ProductID != "manual" || command.CommandKind != "manual.place" {
		return command, fmt.Errorf("manual cancel requires an order command")
	}
	if command.Mode != service.mode || (command.Mode == orders.ModeLive && !service.liveWrites) {
		return command, orders.ErrLiveWriteDisabled
	}
	current, err := service.commands.Get(ctx, command.CommandID)
	if err != nil {
		return command, err
	}
	if current.State == orders.StateFilled || current.State == orders.StateCanceled || current.State == orders.StateFailed {
		return current, nil
	}
	if current.ExecutionEvidence.CommandID != current.CommandID || current.ExecutionEvidence.GatewayOrderID == "" {
		return current, fmt.Errorf("manual order has no authoritative Gateway identity")
	}
	intent, err := decodePlaceIntent(current.Payload)
	if err != nil {
		return current, fmt.Errorf("manual intent is invalid: %w", err)
	}
	authority := gatewayRequest(current, intent)
	gateway, ok := service.gateway.(CancelGateway)
	if !ok {
		return current, fmt.Errorf("manual Gateway cancellation is unavailable")
	}
	request := GatewayCancelRequest{
		CommandID: current.CommandID, AccountID: current.AccountID,
		InstrumentID: authority.InstrumentID, GatewayOrderID: current.ExecutionEvidence.GatewayOrderID, Mode: current.Mode,
	}
	result, err := gateway.Cancel(ctx, request)
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
	return service.recordGatewayResult(ctx, current, authority, result)
}

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
	result := GatewayResult{CommandID: current.CommandID, AccountID: current.AccountID, Mode: current.Mode, State: reconciledGatewayState(handoff.Evidence.State), Handoff: handoff}
	for _, fill := range handoff.Evidence.Fills {
		accountID := fill.AccountID
		if accountID == "" {
			accountID = current.AccountID
		}
		result.Fills = append(result.Fills, FillFact{FillID: fill.FillID, CommandID: current.CommandID, AccountID: accountID, InstrumentID: fill.InstrumentID, Mode: current.Mode, Side: portfolio.Side(fill.Side), Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency, SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt})
	}
	if result.State == GatewayStateDenied {
		result.DenialCode = "gateway_rejected"
	}
	if err := validateGatewayResult(request, result); err != nil {
		return current, err
	}
	return service.recordGatewayResult(ctx, current, request, result)
}

func reconciledGatewayState(state string) GatewayState {
	switch strings.ToLower(state) {
	case "filled":
		return GatewayStateFilled
	case "partially_filled":
		return GatewayStatePartiallyFilled
	case "canceled", "cancelled":
		return GatewayStateCanceled
	case "rejected", "failed":
		return GatewayStateDenied
	default:
		return GatewayStateSubmitted
	}
}

func (service *Service) recordGatewayResult(ctx context.Context, current orders.Command, request GatewayRequest, result GatewayResult) (orders.Command, error) {
	switch result.State {
	case GatewayStateDenied:
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateFailed, Evidence: result.Handoff.Evidence})
	case GatewayStateSubmitted:
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateSubmitted, Evidence: result.Handoff.Evidence})
	case GatewayStatePartiallyFilled:
		if err := service.applyFills(ctx, current, result.Fills); err != nil {
			return current, err
		}
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateSubmitted, Evidence: result.Handoff.Evidence})
	case GatewayStateFilled:
		if err := service.applyFills(ctx, current, result.Fills); err != nil {
			updated, transitionErr := service.markUncertain(ctx, current, orders.ReconciliationEvidenceInvalid, result.Handoff.Evidence)
			if transitionErr != nil {
				return updated, errors.Join(err, transitionErr)
			}
			return updated, err
		}
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateFilled, Evidence: result.Handoff.Evidence})
	case GatewayStateCanceled:
		if err := service.applyFills(ctx, current, result.Fills); err != nil {
			updated, transitionErr := service.markUncertain(ctx, current, orders.ReconciliationEvidenceInvalid, result.Handoff.Evidence)
			if transitionErr != nil {
				return updated, errors.Join(err, transitionErr)
			}
			return updated, err
		}
		return service.commands.Transition(ctx, orders.TransitionRequest{CommandID: current.CommandID, State: orders.StateCanceled, Evidence: result.Handoff.Evidence})
	default:
		panic("validated Gateway state is unhandled")
	}
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
	quantity, err := decimal.NewFromString(intent.Quantity)
	if intent.InstrumentID == "" || (intent.Side != string(portfolio.SideBuy) && intent.Side != string(portfolio.SideSell)) || err != nil || !quantity.IsPositive() {
		return intent, fmt.Errorf("instrument, side, and positive quantity are required")
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

func gatewayRequest(command orders.Command, intent placeIntent) GatewayRequest {
	request := GatewayRequest{
		CommandID: command.CommandID, IdempotencyKey: command.IdempotencyKey, AccountID: command.AccountID, Mode: command.Mode,
		InstrumentID: intent.InstrumentID, Side: intent.Side, Quantity: intent.Quantity, OrderType: intent.OrderType,
		LimitPrice: intent.LimitPrice, TimeInForce: intent.TimeInForce, ReduceOnly: intent.ReduceOnly,
		DesiredStateRevision: intent.DesiredStateRevision, RuntimeGeneration: intent.RuntimeGeneration, CredentialVersion: intent.CredentialVersion,
	}
	if command.ReservationID != nil {
		request.ReservationID = *command.ReservationID
	}
	return request
}

func validateGatewayResult(request GatewayRequest, result GatewayResult) error {
	if result.CommandID != request.CommandID || result.AccountID != request.AccountID || result.Mode != request.Mode {
		return fmt.Errorf("command authority identity mismatch")
	}
	if result.Handoff.Outcome != "" && (result.Handoff.Outcome != orders.HandoffAccepted || result.Handoff.Evidence.CommandID == "" || result.Handoff.Evidence.GatewayOrderID == "" || result.Handoff.Evidence.ClientOrderID == "" || result.Handoff.Evidence.ResourceVersion < 1) {
		return fmt.Errorf("Gateway execution evidence is invalid")
	}
	switch result.State {
	case GatewayStateDenied:
		if result.DenialCode == "" || len(result.Fills) != 0 {
			return fmt.Errorf("denial must have a code and no fill facts")
		}
	case GatewayStateSubmitted:
		if result.DenialCode != "" || len(result.Fills) != 0 {
			return fmt.Errorf("submitted result cannot contain denial or fill facts")
		}
	case GatewayStatePartiallyFilled:
		if result.DenialCode != "" || len(result.Fills) == 0 {
			return fmt.Errorf("partially filled result requires fill facts")
		}
	case GatewayStateFilled:
		if result.DenialCode != "" || len(result.Fills) == 0 {
			return fmt.Errorf("filled result requires fill facts")
		}
	case GatewayStateCanceled:
		if result.DenialCode != "" {
			return fmt.Errorf("canceled result cannot contain denial")
		}
	default:
		return fmt.Errorf("unknown Gateway state")
	}

	seen := make(map[string]struct{}, len(result.Fills))
	for _, fill := range result.Fills {
		if fill.CommandID != request.CommandID || fill.AccountID != request.AccountID || fill.Mode != request.Mode || fill.InstrumentID != request.InstrumentID || string(fill.Side) != request.Side {
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

func validateFillFact(fill FillFact) error {
	quantity, quantityErr := decimal.NewFromString(fill.Quantity)
	price, priceErr := decimal.NewFromString(fill.Price)
	fee, feeErr := decimal.NewFromString(fill.Fee)
	if fill.FillID == "" || fill.FeeCurrency == "" || fill.SettlementCurrency == "" || fill.OccurredAt.IsZero() || quantityErr != nil || priceErr != nil || feeErr != nil || !quantity.IsPositive() || !price.IsPositive() || fee.IsNegative() {
		return fmt.Errorf("fill facts are incomplete or invalid")
	}
	return nil
}

func (service *Service) applyFills(ctx context.Context, command orders.Command, fills []FillFact) error {
	for _, fact := range fills {
		_, result, err := service.portfolio.ApplyFill(ctx, portfolio.Fill{
			PrincipalID: command.PrincipalID, FillID: fact.FillID, CommandID: fact.CommandID, LineageID: command.LineageID, ProductID: command.ProductID,
			Mode: portfolio.Mode(fact.Mode), AccountID: fact.AccountID, InstrumentID: fact.InstrumentID, Side: fact.Side,
			Quantity: fact.Quantity, Price: fact.Price, Fee: fact.Fee, FeeCurrency: fact.FeeCurrency, SettlementCurrency: fact.SettlementCurrency, OccurredAt: fact.OccurredAt,
		})
		if err != nil {
			return fmt.Errorf("apply authoritative fill %q: %w", fact.FillID, err)
		}
		if err := service.ledger.Record(ctx, ledger.Entry{
			PrincipalID: command.PrincipalID, EntryID: fact.FillID + ":fee", Mode: ledger.Mode(fact.Mode), LineageID: command.LineageID, ProductID: command.ProductID,
			AccountID: fact.AccountID, InstrumentID: fact.InstrumentID, Kind: ledger.KindFee, Amount: result.Fee,
			Currency: fact.FeeCurrency, OccurredAt: fact.OccurredAt,
		}); err != nil {
			return fmt.Errorf("record authoritative fill fee %q: %w", fact.FillID, err)
		}
		if result.RealizedPnL != "0" {
			if err := service.ledger.Record(ctx, ledger.Entry{
				PrincipalID: command.PrincipalID, EntryID: fact.FillID + ":realized_pnl", Mode: ledger.Mode(fact.Mode), LineageID: command.LineageID, ProductID: command.ProductID,
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

package executionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/shopspring/decimal"
)

const gatewayAdapterPageLimit = 500

type GatewayAdapter struct {
	client *boltertrader.GatewayClient
}

func NewGatewayAdapter(client *boltertrader.GatewayClient) *GatewayAdapter {
	return &GatewayAdapter{client: client}
}

func (adapter *GatewayAdapter) PlaceOrder(ctx context.Context, request executionservice.GatewayOrderRequest) (executionservice.GatewayOrderResult, error) {
	placeRequest := boltertrader.PlaceOrderRequest{
		AccountID:      request.AccountID,
		InstrumentID:   request.InstrumentID,
		IdempotencyKey: request.IdempotencyKey,
		Side:           string(request.Side),
		Type:           "market",
		Quantity:       request.Quantity.String(),
		ReduceOnly:     request.ReduceOnly,
	}
	result, err := adapter.client.PlaceOrder(ctx, placeRequest)
	if err != nil && shouldRetryAmbiguousPlace(ctx, err) {
		replayed, replayErr := adapter.client.PlaceOrder(ctx, placeRequest)
		if replayErr == nil {
			return orderResult(request.GatewayID, replayed), nil
		}
		err = replayErr
	}
	if err != nil {
		return adapter.placeErrorResult(ctx, request, err), nil
	}
	return orderResult(request.GatewayID, result), nil
}

func (adapter *GatewayAdapter) CancelOrder(ctx context.Context, request executionservice.GatewayCancelRequest) (executionservice.GatewayOrderResult, error) {
	order, err := adapter.resolveOrder(ctx, request.AccountID, "", request.GatewayID)
	if err != nil {
		return executionservice.GatewayOrderResult{
			Outcome:   executionservice.GatewayAmbiguous,
			GatewayID: request.GatewayID,
			Error:     gatewaySafeError("GATEWAY_LOOKUP_UNCERTAIN", "gateway order lookup is uncertain", true),
		}, nil
	}
	if !order.found {
		return executionservice.GatewayOrderResult{
			Outcome:   executionservice.GatewayAmbiguous,
			GatewayID: request.GatewayID,
			Error:     gatewaySafeError("GATEWAY_ORDER_NOT_FOUND", "gateway order ownership could not be proven", true),
		}, nil
	}
	result, err := adapter.client.CancelOrder(ctx, boltertrader.CancelOrderRequest{
		AccountID: request.AccountID, GatewayOrderID: order.result.GatewayOrderID,
	})
	if err != nil {
		return classifyGatewayError(request.GatewayID, err), nil
	}
	mapped := orderResult(request.GatewayID, result)
	if mapped.OrderID == "" {
		mapped.OrderID = order.result.GatewayOrderID
	}
	return mapped, nil
}

func (adapter *GatewayAdapter) GetOrder(ctx context.Context, accountID, gatewayID string) (executionservice.OrderObservation, error) {
	order, err := adapter.resolveOrder(ctx, accountID, "", gatewayID)
	if err != nil {
		return executionservice.OrderObservation{}, fmt.Errorf("%w: gateway order lookup failed", executionservice.ErrServiceUnavailable)
	}
	if !order.found {
		return executionservice.OrderObservation{Found: false, GatewayID: gatewayID}, nil
	}
	return executionservice.OrderObservation{
		Found: true, GatewayID: gatewayID, OrderID: order.result.GatewayOrderID,
		Status: mapOrderStatus(order.result.State),
		Error:  errorForOrderState(order.result.State),
	}, nil
}

func (adapter *GatewayAdapter) Fills(ctx context.Context, accountID, instrumentID, gatewayID string) ([]executionservice.FillObservation, error) {
	order, err := adapter.resolveOrder(ctx, accountID, instrumentID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("%w: gateway order lookup failed", executionservice.ErrServiceUnavailable)
	}
	if !order.found {
		return nil, fmt.Errorf("%w: gateway order not found for fills lookup", executionservice.ErrServiceUnavailable)
	}
	resolvedGatewayOrderID := strings.TrimSpace(order.result.GatewayOrderID)
	if resolvedGatewayOrderID == "" {
		return nil, fmt.Errorf("%w: gateway order identity missing for fills lookup", executionservice.ErrServiceUnavailable)
	}
	resources, err := adapter.listFills(ctx, accountID, instrumentID, resolvedGatewayOrderID)
	if err != nil {
		return nil, fmt.Errorf("%w: gateway fills lookup failed", executionservice.ErrServiceUnavailable)
	}
	identities := orderIdentities(order.result)
	fills := make([]executionservice.FillObservation, 0, len(resources))
	for index, resource := range resources {
		if resource.AccountID != accountID || resource.InstrumentID != instrumentID {
			continue
		}
		wrapperGatewayOrderID := strings.TrimSpace(resource.GatewayOrderID)
		trustedByWrapper := false
		if wrapperGatewayOrderID != "" {
			if wrapperGatewayOrderID != resolvedGatewayOrderID {
				continue
			}
			trustedByWrapper = true
		}
		fill, ok := mapFill(resource.Fill, identities, gatewayID, index, trustedByWrapper)
		if ok {
			fills = append(fills, fill)
		}
	}
	return fills, nil
}

func (adapter *GatewayAdapter) listFills(ctx context.Context, accountID, instrumentID, gatewayOrderID string) ([]boltertrader.FillResource, error) {
	cursor := ""
	seen := make(map[string]struct{})
	var resources []boltertrader.FillResource
	for {
		page, err := adapter.client.FillsPage(ctx, boltertrader.GatewayPageRequest{
			AccountID: accountID, InstrumentID: instrumentID, GatewayOrderID: gatewayOrderID, Cursor: cursor, Limit: gatewayAdapterPageLimit,
		})
		if err != nil {
			return nil, err
		}
		resources = append(resources, page.Items...)
		if page.NextCursor == "" {
			return resources, nil
		}
		if page.NextCursor == cursor {
			return nil, fmt.Errorf("gateway fills pagination repeated cursor")
		}
		if _, exists := seen[page.NextCursor]; exists {
			return nil, fmt.Errorf("gateway fills pagination cursor cycle")
		}
		seen[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func (adapter *GatewayAdapter) Positions(ctx context.Context, accountID, instrumentID string) ([]executionservice.PositionObservation, error) {
	resources, err := adapter.client.Positions(ctx, accountID, instrumentID)
	if err != nil {
		return nil, fmt.Errorf("%w: gateway positions lookup failed", executionservice.ErrServiceUnavailable)
	}
	positions := make([]executionservice.PositionObservation, 0, len(resources))
	for _, resource := range resources {
		if resource.AccountID != accountID || resource.InstrumentID != instrumentID {
			return nil, fmt.Errorf("%w: gateway position identity mismatch", executionservice.ErrServiceUnavailable)
		}
		quantity, ok := decimalFromJSON(resource.Position, "quantity", "size", "amount")
		if !ok {
			continue
		}
		positions = append(positions, executionservice.PositionObservation{
			AccountID: accountID, InstrumentID: instrumentID, Quantity: quantity,
		})
	}
	return positions, nil
}

type resolvedOrder struct {
	found  bool
	result boltertrader.OrderResult
}

func (adapter *GatewayAdapter) resolveOrder(ctx context.Context, accountID, instrumentID, gatewayID string) (resolvedOrder, error) {
	cursor := ""
	seen := make(map[string]struct{})
	for {
		page, err := adapter.client.ListOrdersPage(ctx, boltertrader.GatewayPageRequest{
			AccountID: accountID, InstrumentID: instrumentID, Cursor: cursor, Limit: gatewayAdapterPageLimit,
		})
		if err != nil {
			return resolvedOrder{}, err
		}
		for _, item := range page.Items {
			if orderMatches(item, gatewayID) {
				return resolvedOrder{found: true, result: item}, nil
			}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			return resolvedOrder{}, fmt.Errorf("gateway order pagination repeated cursor")
		}
		if _, exists := seen[page.NextCursor]; exists {
			return resolvedOrder{}, fmt.Errorf("gateway order pagination cursor cycle")
		}
		seen[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return resolvedOrder{}, nil
}

func (adapter *GatewayAdapter) placeErrorResult(ctx context.Context, request executionservice.GatewayOrderRequest, err error) executionservice.GatewayOrderResult {
	var httpErr *boltertrader.GatewayHTTPError
	if errors.As(err, &httpErr) && definitivePreAcceptancePlaceRejection(httpErr) {
		return executionservice.GatewayOrderResult{
			Outcome:   executionservice.GatewayRejected,
			GatewayID: request.GatewayID,
			Error:     gatewaySafeError(httpErr.Code, "gateway did not accept the order", httpErr.Retryable),
		}
	}
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict && httpErr.ExistingCommandID != "" {
		if !existingCommandMatchesRequest(httpErr.ExistingCommandID, request) {
			return executionservice.GatewayOrderResult{
				Outcome:   executionservice.GatewayAmbiguous,
				GatewayID: request.GatewayID,
				Error:     gatewaySafeError("GATEWAY_IDEMPOTENCY_CONFLICT", "gateway idempotency conflict requires reconciliation", true),
			}
		}
		if order, lookupErr := adapter.resolveOrder(ctx, request.AccountID, request.InstrumentID, httpErr.ExistingCommandID); lookupErr == nil && order.found {
			return executionservice.GatewayOrderResult{
				Outcome: executionservice.GatewayAccepted, GatewayID: request.GatewayID, OrderID: order.result.GatewayOrderID,
			}
		}
		return executionservice.GatewayOrderResult{
			Outcome:   executionservice.GatewayAmbiguous,
			GatewayID: request.GatewayID,
			Error:     gatewaySafeError("GATEWAY_IDEMPOTENCY_CONFLICT", "gateway idempotency conflict requires reconciliation", true),
		}
	}
	return classifyGatewayError(request.GatewayID, err)
}

func existingCommandMatchesRequest(existingCommandID string, request executionservice.GatewayOrderRequest) bool {
	existingCommandID = strings.TrimSpace(existingCommandID)
	if existingCommandID == "" {
		return false
	}
	return existingCommandID == strings.TrimSpace(request.IdempotencyKey) ||
		existingCommandID == strings.TrimSpace(request.GatewayID)
}

func shouldRetryAmbiguousPlace(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var httpErr *boltertrader.GatewayHTTPError
	if errors.As(err, &httpErr) {
		if definitivePreAcceptancePlaceRejection(httpErr) {
			return false
		}
		return httpErr.Retryable || httpErr.StatusCode >= http.StatusInternalServerError
	}
	return !errors.Is(err, boltertrader.ErrGatewayWriteBlocked) &&
		!errors.Is(err, boltertrader.ErrGatewayEnvironment) &&
		!errors.Is(err, boltertrader.ErrGatewayAccountNotReady) &&
		!errors.Is(err, boltertrader.ErrGatewayOperationBlocked) &&
		!errors.Is(err, boltertrader.ErrGatewayOrderOwnership)
}

func definitivePreAcceptancePlaceRejection(err *boltertrader.GatewayHTTPError) bool {
	return err != nil &&
		err.StatusCode == http.StatusServiceUnavailable &&
		strings.EqualFold(strings.TrimSpace(err.Code), "temporarily_unavailable")
}

func classifyGatewayError(gatewayID string, err error) executionservice.GatewayOrderResult {
	var httpErr *boltertrader.GatewayHTTPError
	if errors.As(err, &httpErr) {
		info := gatewaySafeError(httpErr.Code, "gateway rejected the request", httpErr.Retryable)
		switch {
		case httpErr.Retryable || httpErr.StatusCode >= http.StatusInternalServerError:
			info.SafeMessage = "gateway write result is uncertain"
			info.Retryable = true
			return executionservice.GatewayOrderResult{Outcome: executionservice.GatewayAmbiguous, GatewayID: gatewayID, Error: info}
		default:
			return executionservice.GatewayOrderResult{Outcome: executionservice.GatewayRejected, GatewayID: gatewayID, Error: info}
		}
	}
	if errors.Is(err, boltertrader.ErrGatewayWriteBlocked) ||
		errors.Is(err, boltertrader.ErrGatewayEnvironment) ||
		errors.Is(err, boltertrader.ErrGatewayAccountNotReady) ||
		errors.Is(err, boltertrader.ErrGatewayOperationBlocked) ||
		errors.Is(err, boltertrader.ErrGatewayOrderOwnership) {
		return executionservice.GatewayOrderResult{
			Outcome:   executionservice.GatewayRejected,
			GatewayID: gatewayID,
			Error:     gatewaySafeError("GATEWAY_WRITE_REJECTED", "gateway write is not permitted", false),
		}
	}
	return executionservice.GatewayOrderResult{
		Outcome:   executionservice.GatewayAmbiguous,
		GatewayID: gatewayID,
		Error:     gatewaySafeError("GATEWAY_WRITE_UNCERTAIN", "gateway write result is uncertain", true),
	}
}

func orderResult(commandID string, result boltertrader.OrderResult) executionservice.GatewayOrderResult {
	mapped := executionservice.GatewayOrderResult{
		Outcome:   outcomeForOrderState(result.State),
		GatewayID: commandID,
		OrderID:   result.GatewayOrderID,
		Error:     errorForOrderState(result.State),
	}
	if mapped.Outcome == "" {
		mapped.Outcome = executionservice.GatewayAmbiguous
		mapped.Error = gatewaySafeError("GATEWAY_ORDER_STATE_UNKNOWN", "gateway order state is uncertain", true)
	}
	return mapped
}

func outcomeForOrderState(state string) executionservice.GatewayOutcome {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "prepared", "dispatching", "accepted", "open", "partially_filled", "partial", "filled", "canceled", "cancelled":
		return executionservice.GatewayAccepted
	case "rejected", "failed", "expired":
		return executionservice.GatewayRejected
	case "uncertain", "unknown":
		return executionservice.GatewayAmbiguous
	default:
		return ""
	}
}

func mapOrderStatus(state string) execution.LegStatus {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "":
		return execution.LegStatusWorking
	case "filled":
		return execution.LegStatusFilled
	case "partially_filled", "partial":
		return execution.LegStatusPartial
	case "canceled", "cancelled":
		return execution.LegStatusCanceled
	case "rejected", "failed", "expired":
		return execution.LegStatusRejected
	case "uncertain", "unknown":
		return execution.LegStatusUncertain
	default:
		return execution.LegStatusUncertain
	}
}

func errorForOrderState(state string) execution.ErrorInfo {
	switch mapOrderStatus(state) {
	case execution.LegStatusRejected:
		return gatewaySafeError("GATEWAY_ORDER_REJECTED", "gateway order was rejected", false)
	case execution.LegStatusUncertain:
		if outcomeForOrderState(state) == "" {
			return gatewaySafeError("GATEWAY_ORDER_STATE_UNKNOWN", "gateway order state is uncertain", true)
		}
		return gatewaySafeError("GATEWAY_ORDER_UNCERTAIN", "gateway order state is uncertain", true)
	default:
		return execution.ErrorInfo{}
	}
}

func orderMatches(order boltertrader.OrderResult, identity string) bool {
	identity = strings.TrimSpace(identity)
	return identity != "" && (order.CommandID == identity || order.ClientOrderID == identity || order.GatewayOrderID == identity)
}

func orderIdentities(order boltertrader.OrderResult) map[string]struct{} {
	values := map[string]struct{}{}
	for _, value := range []string{order.CommandID, order.ClientOrderID, order.GatewayOrderID} {
		if strings.TrimSpace(value) != "" {
			values[value] = struct{}{}
		}
	}
	return values
}

func mapFill(fill any, orderIDs map[string]struct{}, fallbackGatewayID string, index int, trustedByWrapper bool) (executionservice.FillObservation, bool) {
	fields, ok := jsonFields(fill)
	if !ok {
		return executionservice.FillObservation{}, false
	}
	if !trustedByWrapper && !fillMatchesOrder(fields, orderIDs) {
		return executionservice.FillObservation{}, false
	}
	quantity, ok := decimalFromMap(fields, "quantity", "qty", "size", "amount")
	if !ok || !quantity.IsPositive() {
		return executionservice.FillObservation{}, false
	}
	price, _ := decimalFromMap(fields, "price", "fill_price", "avg_price")
	fee, _ := decimalFromMap(fields, "fee", "fee_amount", "commission")
	currency := firstString(fields, "fee_currency", "fee_asset", "commission_asset")
	fillID := firstString(fields, "fill_id", "id", "trade_id")
	gatewayFillID := firstString(fields, "gateway_fill_id", "fill_id", "id", "trade_id")
	if fillID == "" {
		fillID = stableFillID(fallbackGatewayID, fields, index)
	}
	if gatewayFillID == "" {
		gatewayFillID = fillID
	}
	return executionservice.FillObservation{
		FillID: fillID, GatewayFillID: gatewayFillID, Quantity: quantity, Price: price,
		Fee: execution.Money{Amount: fee, Currency: currency}, OccurredAt: parseTimeField(fields),
	}, true
}

func fillMatchesOrder(fields map[string]any, orderIDs map[string]struct{}) bool {
	for _, key := range []string{"gateway_order_id", "order_id", "client_order_id", "command_id"} {
		value := firstString(fields, key)
		if _, ok := orderIDs[value]; ok && value != "" {
			return true
		}
	}
	return false
}

func jsonFields(value any) (map[string]any, bool) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, false
	}
	return fields, true
}

func decimalFromJSON(value any, keys ...string) (decimal.Decimal, bool) {
	fields, ok := jsonFields(value)
	if !ok {
		return decimal.Zero, false
	}
	return decimalFromMap(fields, keys...)
}

func decimalFromMap(fields map[string]any, keys ...string) (decimal.Decimal, bool) {
	for _, key := range keys {
		value, exists := fields[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case string:
			parsed, err := decimal.NewFromString(typed)
			return parsed, err == nil
		case float64:
			return decimal.NewFromFloat(typed), true
		case json.Number:
			parsed, err := decimal.NewFromString(typed.String())
			return parsed, err == nil
		}
	}
	return decimal.Zero, false
}

func firstString(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		value, exists := fields[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed)
		case json.Number:
			return strings.TrimSpace(typed.String())
		case float64:
			return decimal.NewFromFloat(typed).String()
		}
	}
	return ""
}

func parseTimeField(fields map[string]any) time.Time {
	for _, key := range []string{"occurred_at", "time", "timestamp", "created_at"} {
		value := firstString(fields, key)
		if value == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func stableFillID(gatewayID string, fields map[string]any, index int) string {
	body, _ := json.Marshal(fields)
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("%s:%d:", gatewayID, index)), body...))
	return "fill-" + hex.EncodeToString(sum[:8])
}

func gatewaySafeError(code, message string, retryable bool) execution.ErrorInfo {
	code = safeGatewayCode(code)
	return execution.ErrorInfo{Code: code, SafeMessage: message, Retryable: retryable}
}

func safeGatewayCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 {
		return "GATEWAY_ERROR"
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return "GATEWAY_ERROR"
	}
	return code
}

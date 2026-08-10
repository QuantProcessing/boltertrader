package boltertrader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
)

const (
	defaultGatewayTimeout = 10 * time.Second
	defaultGatewayBodyMax = 2 << 20
	gatewayMaxPageLimit   = 500
)

var (
	ErrGatewayWriteBlocked     = errors.New("gateway write blocked")
	ErrGatewayEnvironment      = errors.New("gateway environment mismatch")
	ErrGatewayAccountNotReady  = errors.New("gateway account not write-ready")
	ErrGatewayOperationBlocked = errors.New("gateway operation not available")
	ErrGatewayResnapshotNeeded = errors.New("gateway resnapshot required")
	ErrGatewayOrderOwnership   = errors.New("gateway order ownership not proven")
	ErrGatewayPageLimit        = errors.New("gateway page limit is invalid")
	ErrGatewayEventInvalid     = errors.New("gateway private event is invalid")
)

type GatewayClient struct {
	baseURL                 *url.URL
	token                   string
	httpClient              *http.Client
	bodyLimit               int64
	mode                    config.Mode
	productionWritesEnabled bool
}

type GatewayClientOptions struct {
	BaseURL                 string
	Token                   string
	HTTPClient              *http.Client
	Timeout                 time.Duration
	BodyLimit               int64
	Mode                    config.Mode
	ProductionWritesEnabled bool
}

func NewGatewayClient(options GatewayClientOptions) (*GatewayClient, error) {
	baseURL, err := url.Parse(options.BaseURL)
	if err != nil || options.BaseURL == "" || !baseURL.IsAbs() || baseURL.Host == "" || baseURL.User != nil ||
		(baseURL.Scheme != "http" && baseURL.Scheme != "https") {
		return nil, fmt.Errorf("gateway base url is invalid")
	}
	if strings.TrimSpace(options.Token) == "" {
		return nil, fmt.Errorf("gateway token is required")
	}
	switch options.Mode {
	case config.Test, config.Live:
	default:
		return nil, fmt.Errorf("funding-trader mode is invalid")
	}
	if err := config.ValidateGatewayTransport(options.Mode, baseURL); err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultGatewayTimeout
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	} else if httpClient.Timeout <= 0 {
		copyClient := *httpClient
		copyClient.Timeout = timeout
		httpClient = &copyClient
	}
	bodyLimit := options.BodyLimit
	if bodyLimit <= 0 {
		bodyLimit = defaultGatewayBodyMax
	}
	clean := *baseURL
	clean.RawQuery = ""
	clean.Fragment = ""
	return &GatewayClient{
		baseURL: &clean, token: options.Token, httpClient: httpClient, bodyLimit: bodyLimit,
		mode: options.Mode, productionWritesEnabled: options.ProductionWritesEnabled,
	}, nil
}

func (client *GatewayClient) Live(ctx context.Context) (GatewayLiveness, error) {
	var response GatewayLiveness
	err := client.doJSON(ctx, http.MethodGet, "/v1/health/live", nil, "", &response)
	return response, err
}

func (client *GatewayClient) Ready(ctx context.Context) (GatewayReadiness, error) {
	var response GatewayReadiness
	err := client.doJSON(ctx, http.MethodGet, "/v1/health/ready", nil, "", &response)
	return response, err
}

func (client *GatewayClient) ListAccounts(ctx context.Context) ([]AccountResource, error) {
	var response GatewayCollection[AccountResource]
	values := url.Values{}
	values.Set("pagination.limit", fmt.Sprintf("%d", gatewayMaxPageLimit))
	err := client.doJSON(ctx, http.MethodGet, withQuery("/v1/accounts", values), nil, "", &response)
	return response.Items, err
}

func (client *GatewayClient) GetAccount(ctx context.Context, accountID string) (AccountStatus, error) {
	var response AccountStatus
	err := client.doJSON(ctx, http.MethodGet, "/v1/accounts/"+url.PathEscape(accountID), nil, "", &response)
	return response, err
}

func (client *GatewayClient) ListCapabilities(ctx context.Context, accountID string) ([]AccountCapability, error) {
	var response GatewayCollection[AccountCapability]
	values := url.Values{}
	if accountID != "" {
		values.Set("account_id", accountID)
	}
	err := client.doJSON(ctx, http.MethodGet, withQuery("/v1/capabilities", values), nil, "", &response)
	return response.Items, err
}

func (client *GatewayClient) ListInstruments(ctx context.Context, accountID string) ([]InstrumentResource, error) {
	var response GatewayCollection[InstrumentResource]
	values := url.Values{}
	if accountID != "" {
		values.Set("account_id", accountID)
	}
	values.Set("pagination.limit", fmt.Sprintf("%d", gatewayMaxPageLimit))
	err := client.doJSON(ctx, http.MethodGet, withQuery("/v1/instruments", values), nil, "", &response)
	return response.Items, err
}

func (client *GatewayClient) GetInstrument(ctx context.Context, instrumentID string) (InstrumentResource, error) {
	var response InstrumentResource
	err := client.doJSON(ctx, http.MethodGet, "/v1/instruments/"+url.PathEscape(instrumentID), nil, "", &response)
	return response, err
}

func (client *GatewayClient) ListRules(ctx context.Context, accountID string) ([]InstrumentRulesResource, error) {
	var response GatewayCollection[InstrumentRulesResource]
	values := url.Values{}
	if accountID != "" {
		values.Set("account_id", accountID)
	}
	err := client.doJSON(ctx, http.MethodGet, withQuery("/v1/rules", values), nil, "", &response)
	return response.Items, err
}

func (client *GatewayClient) RiskLimits(ctx context.Context, accountID string) (RiskLimitReference, error) {
	var response RiskLimitReference
	values := url.Values{}
	if accountID != "" {
		values.Set("account_id", accountID)
	}
	err := client.doJSON(ctx, http.MethodGet, withQuery("/v1/risk-limits", values), nil, "", &response)
	return response, err
}

func (client *GatewayClient) ListOrders(ctx context.Context, accountID, instrumentID string) ([]OrderResult, error) {
	page, err := client.ListOrdersPage(ctx, GatewayPageRequest{AccountID: accountID, InstrumentID: instrumentID})
	return page.Items, err
}

func (client *GatewayClient) OpenOrders(ctx context.Context, accountID, instrumentID string) ([]OrderResult, error) {
	page, err := client.OpenOrdersPage(ctx, GatewayPageRequest{AccountID: accountID, InstrumentID: instrumentID})
	return page.Items, err
}

func (client *GatewayClient) ListOrdersPage(ctx context.Context, request GatewayPageRequest) (OrderPage, error) {
	return client.listOrdersPage(ctx, "/v1/orders", request)
}

func (client *GatewayClient) OpenOrdersPage(ctx context.Context, request GatewayPageRequest) (OrderPage, error) {
	return client.listOrdersPage(ctx, "/v1/open-orders", request)
}

func (client *GatewayClient) listOrdersPage(ctx context.Context, endpoint string, request GatewayPageRequest) (OrderPage, error) {
	var response GatewayCollection[OrderResult]
	values, err := pageQuery(request)
	if err != nil {
		return OrderPage{}, err
	}
	err = client.doJSON(ctx, http.MethodGet, withQuery(endpoint, values), nil, "", &response)
	return OrderPage{Items: response.Items, NextCursor: response.NextCursor}, err
}

func pageQuery(request GatewayPageRequest) (url.Values, error) {
	if request.Limit < 0 || request.Limit > gatewayMaxPageLimit {
		return nil, fmt.Errorf("%w: must be between 0 and %d", ErrGatewayPageLimit, gatewayMaxPageLimit)
	}
	values := url.Values{}
	if request.AccountID != "" {
		values.Set("account_id", request.AccountID)
	}
	if request.InstrumentID != "" {
		values.Set("instrument_id", request.InstrumentID)
	}
	if request.GatewayOrderID != "" {
		values.Set("gateway_order_id", request.GatewayOrderID)
	}
	if request.Cursor != "" {
		values.Set("pagination.cursor", request.Cursor)
	}
	if request.Limit > 0 {
		values.Set("pagination.limit", fmt.Sprintf("%d", request.Limit))
	}
	return values, nil
}

func (client *GatewayClient) GetOrder(ctx context.Context, gatewayOrderID string) (OrderResult, error) {
	var response OrderResult
	err := client.doJSON(ctx, http.MethodGet, "/v1/orders/"+url.PathEscape(gatewayOrderID), nil, "", &response)
	return response, err
}

func (client *GatewayClient) Fills(ctx context.Context, accountID, instrumentID string) ([]FillResource, error) {
	page, err := client.FillsPage(ctx, GatewayPageRequest{AccountID: accountID, InstrumentID: instrumentID})
	return page.Items, err
}

func (client *GatewayClient) FillsPage(ctx context.Context, request GatewayPageRequest) (FillPage, error) {
	var response GatewayCollection[FillResource]
	values, err := pageQuery(request)
	if err != nil {
		return FillPage{}, err
	}
	err = client.doJSON(ctx, http.MethodGet, withQuery("/v1/fills", values), nil, "", &response)
	return FillPage{Items: response.Items, NextCursor: response.NextCursor}, err
}

func (client *GatewayClient) Balances(ctx context.Context, accountID string) ([]BalanceResource, error) {
	var response GatewayCollection[BalanceResource]
	values := url.Values{}
	if accountID != "" {
		values.Set("account_id", accountID)
	}
	values.Set("pagination.limit", fmt.Sprintf("%d", gatewayMaxPageLimit))
	err := client.doJSON(ctx, http.MethodGet, withQuery("/v1/balances", values), nil, "", &response)
	return response.Items, err
}

func (client *GatewayClient) Positions(ctx context.Context, accountID, instrumentID string) ([]PositionResource, error) {
	var response GatewayCollection[PositionResource]
	values := url.Values{}
	if accountID != "" {
		values.Set("account_id", accountID)
	}
	if instrumentID != "" {
		values.Set("instrument_id", instrumentID)
	}
	values.Set("pagination.limit", fmt.Sprintf("%d", gatewayMaxPageLimit))
	err := client.doJSON(ctx, http.MethodGet, withQuery("/v1/positions", values), nil, "", &response)
	return response.Items, err
}

func (client *GatewayClient) FundingRates(ctx context.Context, accountID, instrumentID string) (FundingRateResource, error) {
	var response FundingRateResource
	values := url.Values{}
	values.Set("account_id", accountID)
	values.Set("instrument_id", instrumentID)
	err := client.doJSON(ctx, http.MethodGet, withQuery("/v1/funding-rates", values), nil, "", &response)
	return response, err
}

func (client *GatewayClient) FundingPayments(ctx context.Context, request GatewayFundingPaymentRequest) (FundingPaymentPageResource, error) {
	var response FundingPaymentPageResource
	values := url.Values{}
	values.Set("account_id", request.AccountID)
	if request.InstrumentID != "" {
		values.Set("instrument_id", request.InstrumentID)
	}
	if request.SettlementAsset != "" {
		values.Set("settlement_asset", request.SettlementAsset)
	}
	if !request.Start.IsZero() {
		values.Set("start", request.Start.UTC().Format(time.RFC3339Nano))
	}
	if !request.End.IsZero() {
		values.Set("end", request.End.UTC().Format(time.RFC3339Nano))
	}
	if request.Cursor != "" {
		values.Set("pagination.cursor", request.Cursor)
	}
	if request.Limit > 0 {
		values.Set("pagination.limit", fmt.Sprintf("%d", request.Limit))
	}
	err := client.doJSON(ctx, http.MethodGet, withQuery("/v1/funding-payments", values), nil, "", &response)
	return response, err
}

func (client *GatewayClient) PlaceOrder(ctx context.Context, request PlaceOrderRequest) (OrderResult, error) {
	if err := client.authorizeWrite(ctx, request.AccountID, "place"); err != nil {
		return OrderResult{}, err
	}
	var response OrderResult
	err := client.doJSON(ctx, http.MethodPost, "/v1/orders", request, request.IdempotencyKey, &response)
	return response, err
}

func (client *GatewayClient) CancelOrder(ctx context.Context, request CancelOrderRequest) (OrderResult, error) {
	if err := client.authorizeWrite(ctx, request.AccountID, "cancel"); err != nil {
		return OrderResult{}, err
	}
	if err := client.ensureOrderOwned(ctx, request.AccountID, request.GatewayOrderID); err != nil {
		return OrderResult{}, err
	}
	var response OrderResult
	err := client.doJSON(ctx, http.MethodDelete, "/v1/orders/"+url.PathEscape(request.GatewayOrderID), nil, "", &response)
	return response, err
}

func (client *GatewayClient) ensureOrderOwned(ctx context.Context, accountID, gatewayOrderID string) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(gatewayOrderID) == "" {
		return ErrGatewayOrderOwnership
	}
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		page, err := client.ListOrdersPage(ctx, GatewayPageRequest{
			AccountID: accountID,
			Cursor:    cursor,
			Limit:     gatewayMaxPageLimit,
		})
		if err != nil {
			return err
		}
		for _, order := range page.Items {
			if order.GatewayOrderID == gatewayOrderID {
				return nil
			}
		}
		if page.NextCursor == "" {
			return ErrGatewayOrderOwnership
		}
		if page.NextCursor == cursor {
			return fmt.Errorf("%w: repeated pagination cursor", ErrGatewayOrderOwnership)
		}
		if _, exists := seenCursors[page.NextCursor]; exists {
			return fmt.Errorf("%w: pagination cursor cycle", ErrGatewayOrderOwnership)
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func (client *GatewayClient) DiscoverAccounts(ctx context.Context) (AccountDiscovery, error) {
	accounts, err := client.ListAccounts(ctx)
	if err != nil {
		return AccountDiscovery{}, err
	}
	capabilities, err := client.ListCapabilities(ctx, "")
	if err != nil {
		return AccountDiscovery{}, err
	}
	byAccount := make(map[string]CapabilityRow, len(capabilities))
	for _, capability := range capabilities {
		byAccount[capability.AccountID] = capability.Capability
	}
	result := AccountDiscovery{Mode: client.mode, Accounts: make([]DiscoveredAccount, 0, len(accounts)), Ready: true}
	environments := make(map[VenueEnvironment]struct{})
	for _, account := range accounts {
		if !eligibleVenueEnvironment(client.mode, account.VenueEnvironment) {
			continue
		}
		environments[account.VenueEnvironment] = struct{}{}
		reason := account.Reason
		if reason == "" && !account.ReadReady {
			reason = "read-not-ready"
		}
		if !account.ReadReady {
			result.Ready = false
			result.Reasons = append(result.Reasons, reason)
		}
		result.Accounts = append(result.Accounts, DiscoveredAccount{
			AccountID: account.AccountID, Venue: account.Venue, Product: account.Product,
			VenueEnvironment: account.VenueEnvironment, Settlement: account.Settlement,
			ReadReady: account.ReadReady, WriteReady: account.WriteReady, Reason: reason,
			Capability: byAccount[account.AccountID],
		})
	}
	if len(environments) > 1 {
		result.Mixed = true
	}
	if len(result.Accounts) == 0 {
		result.Ready = false
		result.Reasons = append(result.Reasons, "no-accounts-for-mode")
	}
	sort.Strings(result.Reasons)
	return result, nil
}

func (client *GatewayClient) authorizeWrite(ctx context.Context, accountID string, operation string) error {
	if client.mode == config.Live && !client.productionWritesEnabled {
		return fmt.Errorf("%w: live writes disabled", ErrGatewayWriteBlocked)
	}
	account, err := client.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account.AccountID != accountID {
		return fmt.Errorf("%w: account identity mismatch", ErrGatewayEnvironment)
	}
	if !eligibleVenueEnvironment(client.mode, account.VenueEnvironment) {
		return fmt.Errorf("%w: account environment mismatch", ErrGatewayEnvironment)
	}
	if !account.WriteReady {
		return fmt.Errorf("%w: %s", ErrGatewayAccountNotReady, account.Reason)
	}
	capabilities, err := client.ListCapabilities(ctx, accountID)
	if err != nil {
		return err
	}
	var matched *CapabilityRow
	for index := range capabilities {
		if capabilities[index].AccountID == accountID {
			matched = &capabilities[index].Capability
			break
		}
	}
	if matched == nil {
		return fmt.Errorf("%w: account capability missing", ErrGatewayOperationBlocked)
	}
	switch operation {
	case "place":
		if !matched.Operations.Place {
			return fmt.Errorf("%w: place", ErrGatewayOperationBlocked)
		}
	case "cancel":
		if !matched.Operations.Cancel {
			return fmt.Errorf("%w: cancel", ErrGatewayOperationBlocked)
		}
	default:
		return fmt.Errorf("%w: unknown operation", ErrGatewayOperationBlocked)
	}
	return nil
}

func eligibleVenueEnvironment(mode config.Mode, environment VenueEnvironment) bool {
	return mode.AllowsVenueEnvironment(string(environment))
}

func (client *GatewayClient) doJSON(ctx context.Context, method, endpoint string, body any, idempotencyKey string, destination any) error {
	if client == nil {
		return fmt.Errorf("gateway client is nil")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode gateway request failed")
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.endpoint(endpoint), reader)
	if err != nil {
		return fmt.Errorf("build gateway request failed")
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("gateway transport failed")
	}
	defer response.Body.Close()
	if !jsonContent(response.Header.Get("Content-Type")) {
		return &GatewayHTTPError{Method: method, Path: request.URL.EscapedPath(), StatusCode: response.StatusCode, Code: "invalid_content_type"}
	}
	limited := io.LimitReader(response.Body, client.bodyLimit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read gateway response failed")
	}
	if int64(len(raw)) > client.bodyLimit {
		return &GatewayHTTPError{Method: method, Path: request.URL.EscapedPath(), StatusCode: response.StatusCode, Code: "response_too_large"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope GatewayErrorEnvelope
		code := "gateway_error"
		retryable := false
		correlationID := ""
		existingCommandID := ""
		if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Code != "" {
			code = safeGatewayToken(envelope.Error.Code, "gateway_error")
			retryable = envelope.Error.Retryable
			correlationID = safeGatewayToken(envelope.Error.CorrelationID, "")
			existingCommandID = safeGatewayToken(envelope.ExistingCommandID, "")
		}
		return &GatewayHTTPError{
			Method: method, Path: request.URL.EscapedPath(), StatusCode: response.StatusCode,
			Code: code, Retryable: retryable, CorrelationID: correlationID, ExistingCommandID: existingCommandID,
		}
	}
	if destination == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode gateway response failed")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("decode gateway response failed")
	}
	return nil
}

func (client *GatewayClient) endpoint(endpoint string) string {
	target := *client.baseURL
	relative, err := url.Parse(endpoint)
	if err != nil {
		relative = &url.URL{Path: endpoint}
	}
	target.Path = path.Join(client.baseURL.Path, relative.Path)
	if strings.HasSuffix(relative.Path, "/") && !strings.HasSuffix(target.Path, "/") {
		target.Path += "/"
	}
	target.RawQuery = relative.RawQuery
	return target.String()
}

func withQuery(endpoint string, values url.Values) string {
	if len(values) == 0 {
		return endpoint
	}
	return endpoint + "?" + values.Encode()
}

func jsonContent(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

type GatewayHTTPError struct {
	Method            string
	Path              string
	StatusCode        int
	Code              string
	Retryable         bool
	CorrelationID     string
	ExistingCommandID string
}

func (err *GatewayHTTPError) Error() string {
	code := safeGatewayToken(err.Code, "gateway_error")
	message := fmt.Sprintf("gateway %s %s failed: status=%d code=%s retryable=%t", err.Method, err.Path, err.StatusCode, code, err.Retryable)
	if err.CorrelationID != "" {
		message += " correlation_id=" + safeGatewayToken(err.CorrelationID, "")
	}
	if err.ExistingCommandID != "" {
		message += " existing_command_id=" + safeGatewayToken(err.ExistingCommandID, "")
	}
	return message
}

func safeGatewayToken(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return fallback
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return fallback
	}
	return value
}

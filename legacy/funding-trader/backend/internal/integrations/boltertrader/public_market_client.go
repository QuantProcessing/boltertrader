package boltertrader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/coder/websocket"
	"github.com/shopspring/decimal"
)

const (
	publicMarketDefaultTimeout = 10 * time.Second
	publicMarketDefaultBodyMax = 2 << 20
	publicMarketDefaultBook    = 20
)

type PublicMarketClientOptions struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	Timeout    time.Duration
	BodyLimit  int64
	Mode       config.Mode
}

// PublicMarketClient has no private REST methods and its HTTP implementation
// rejects every path outside the normalized public-market allowlist.
type PublicMarketClient struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
	bodyLimit  int64
}

func NewPublicMarketClient(options PublicMarketClientOptions) (*PublicMarketClient, error) {
	baseURL, err := url.Parse(options.BaseURL)
	if err != nil || options.BaseURL == "" || !baseURL.IsAbs() || baseURL.Host == "" || baseURL.User != nil ||
		(baseURL.Scheme != "http" && baseURL.Scheme != "https") {
		return nil, fmt.Errorf("public market base url is invalid")
	}
	if strings.TrimSpace(options.Token) == "" {
		return nil, fmt.Errorf("public market gateway token is required")
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
		timeout = publicMarketDefaultTimeout
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
		bodyLimit = publicMarketDefaultBodyMax
	}
	clean := *baseURL
	clean.RawQuery = ""
	clean.Fragment = ""
	return &PublicMarketClient{
		baseURL: &clean, token: options.Token, httpClient: httpClient, bodyLimit: bodyLimit,
	}, nil
}

func (client *PublicMarketClient) CurrentFunding(
	ctx context.Context,
	instrument marketdata.Instrument,
) (marketdata.FundingRate, error) {
	values, err := publicScopeQuery(instrument)
	if err != nil {
		return marketdata.FundingRate{}, err
	}
	var response gatewayFundingResponse
	if err := client.getJSON(ctx, withQuery("/v1/funding-rates", values), &response); err != nil {
		return marketdata.FundingRate{}, err
	}
	if err := validatePublicScope(instrument, response.AccountID, response.InstrumentID); err != nil {
		return marketdata.FundingRate{}, err
	}
	if response.FundingRate == nil {
		return marketdata.FundingRate{}, invalidPublicData("missing funding rate")
	}
	return publicFundingRate(instrument, *response.FundingRate, true)
}

func (client *PublicMarketClient) FundingHistory(
	ctx context.Context,
	request marketdata.FundingRateHistoryRequest,
) (marketdata.FundingRatePage, error) {
	if !request.Start.IsZero() && !request.End.IsZero() && request.Start.After(request.End) {
		return marketdata.FundingRatePage{}, invalidPublicData("funding history window")
	}
	if request.Limit < 0 || request.Limit > marketdata.MaxPageLimit {
		return marketdata.FundingRatePage{}, invalidPublicData("funding history limit")
	}
	values, err := publicScopeQuery(request.Instrument)
	if err != nil {
		return marketdata.FundingRatePage{}, err
	}
	if !request.Start.IsZero() {
		values.Set("start", request.Start.Format(time.RFC3339Nano))
	}
	if !request.End.IsZero() {
		values.Set("end", request.End.Format(time.RFC3339Nano))
	}
	if request.Cursor != "" {
		values.Set("pagination.cursor", request.Cursor)
	}
	if request.Limit > 0 {
		values.Set("pagination.limit", strconv.Itoa(request.Limit))
	}
	var response gatewayFundingHistoryResponse
	if err := client.getJSON(ctx, withQuery("/v1/funding-rates/history", values), &response); err != nil {
		return marketdata.FundingRatePage{}, err
	}
	if err := validatePublicScope(request.Instrument, response.AccountID, response.InstrumentID); err != nil {
		return marketdata.FundingRatePage{}, err
	}
	if response.Items == nil {
		return marketdata.FundingRatePage{}, invalidPublicData("missing funding history items")
	}
	rates := make([]marketdata.FundingRate, len(*response.Items))
	for index, projection := range *response.Items {
		rate, convertErr := publicFundingRate(request.Instrument, projection, false)
		if convertErr != nil {
			return marketdata.FundingRatePage{}, convertErr
		}
		rates[index] = rate
	}
	return marketdata.FundingRatePage{Rates: rates, NextCursor: response.NextCursor}, nil
}

func (client *PublicMarketClient) OrderBook(
	ctx context.Context,
	request marketdata.OrderBookRequest,
) (marketdata.OrderBook, error) {
	limit := request.Limit
	if limit == 0 {
		limit = publicMarketDefaultBook
	}
	if limit < 1 || limit > marketdata.MaxPageLimit {
		return marketdata.OrderBook{}, invalidPublicData("order book limit")
	}
	values, err := publicScopeQuery(request.Instrument)
	if err != nil {
		return marketdata.OrderBook{}, err
	}
	values.Set("limit", strconv.Itoa(limit))
	var response gatewayOrderBookResponse
	if err := client.getJSON(ctx, withQuery("/v1/order-book", values), &response); err != nil {
		return marketdata.OrderBook{}, err
	}
	if err := validatePublicScope(request.Instrument, response.AccountID, response.InstrumentID); err != nil {
		return marketdata.OrderBook{}, err
	}
	return publicOrderBookSnapshot(request.Instrument, response)
}

func (client *PublicMarketClient) BBO(
	ctx context.Context,
	instrument marketdata.Instrument,
) (marketdata.BBO, error) {
	values, err := publicScopeQuery(instrument)
	if err != nil {
		return marketdata.BBO{}, err
	}
	var response gatewayBBOResponse
	if err := client.getJSON(ctx, withQuery("/v1/bbo", values), &response); err != nil {
		return marketdata.BBO{}, err
	}
	return publicBBO(instrument, response)
}

func (client *PublicMarketClient) MarkPrice(
	ctx context.Context,
	instrument marketdata.Instrument,
) (marketdata.MarkPrice, error) {
	values, err := publicScopeQuery(instrument)
	if err != nil {
		return marketdata.MarkPrice{}, err
	}
	var response gatewayMarkPriceResponse
	if err := client.getJSON(ctx, withQuery("/v1/mark-price", values), &response); err != nil {
		return marketdata.MarkPrice{}, err
	}
	return publicMarkPrice(instrument, response)
}

func (client *PublicMarketClient) WatchOrderBook(
	ctx context.Context,
	instrument marketdata.Instrument,
) (marketdata.Subscription[marketdata.OrderBook], error) {
	return newPublicMarketSubscription(ctx, client, instrument, publicMarketKindOrderBook, decodeOrderBookPayload)
}

func (client *PublicMarketClient) WatchBBO(
	ctx context.Context,
	instrument marketdata.Instrument,
) (marketdata.Subscription[marketdata.BBO], error) {
	return newPublicMarketSubscription(ctx, client, instrument, publicMarketKindBBO, decodeBBOPayload)
}

func (client *PublicMarketClient) WatchMarkPrice(
	ctx context.Context,
	instrument marketdata.Instrument,
) (marketdata.Subscription[marketdata.MarkPrice], error) {
	return newPublicMarketSubscription(ctx, client, instrument, publicMarketKindMarkPrice, decodeMarkPricePayload)
}

func (client *PublicMarketClient) WatchFundingRate(
	ctx context.Context,
	instrument marketdata.Instrument,
) (marketdata.Subscription[marketdata.FundingRate], error) {
	return newPublicMarketSubscription(ctx, client, instrument, publicMarketKindFundingRate, decodeFundingPayload)
}

func (client *PublicMarketClient) getJSON(ctx context.Context, endpoint string, destination any) error {
	if client == nil {
		return fmt.Errorf("public market client is nil")
	}
	relative, err := url.Parse(endpoint)
	if err != nil || !publicMarketRESTPathAllowed(relative.Path) {
		return fmt.Errorf("public market endpoint is not allowed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint(endpoint), nil)
	if err != nil {
		return fmt.Errorf("build public market request failed")
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("public market transport failed")
	}
	defer response.Body.Close()
	if !publicJSONContent(response.Header.Get("Content-Type")) {
		return &GatewayHTTPError{
			Method: http.MethodGet, Path: request.URL.EscapedPath(),
			StatusCode: response.StatusCode, Code: "invalid_content_type",
		}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, client.bodyLimit+1))
	if err != nil {
		return fmt.Errorf("read public market response failed")
	}
	if int64(len(raw)) > client.bodyLimit {
		return &GatewayHTTPError{
			Method: http.MethodGet, Path: request.URL.EscapedPath(),
			StatusCode: response.StatusCode, Code: "response_too_large",
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return publicGatewayHTTPError(request, response.StatusCode, raw)
	}
	if destination == nil {
		return nil
	}
	if err := decodeStrictPublicJSON(raw, destination); err != nil {
		return fmt.Errorf("decode public market response failed")
	}
	return nil
}

func (client *PublicMarketClient) endpoint(endpoint string) string {
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

func (client *PublicMarketClient) websocketTarget(
	instrument marketdata.Instrument,
	kind publicMarketKind,
) (string, http.Header, error) {
	if client == nil {
		return "", nil, fmt.Errorf("public market client is nil")
	}
	if !kind.valid() {
		return "", nil, invalidPublicData("public stream kind")
	}
	values, err := publicScopeQuery(instrument)
	if err != nil {
		return "", nil, err
	}
	values.Set("kind", string(kind))
	target, err := url.Parse(client.endpoint(withQuery("/v1/public/ws", values)))
	if err != nil {
		return "", nil, fmt.Errorf("build public market websocket url failed")
	}
	switch target.Scheme {
	case "http":
		target.Scheme = "ws"
	case "https":
		target.Scheme = "wss"
	default:
		return "", nil, fmt.Errorf("public market websocket scheme is invalid")
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+client.token)
	return target.String(), headers, nil
}

func publicMarketRESTPathAllowed(endpoint string) bool {
	switch endpoint {
	case "/v1/funding-rates", "/v1/funding-rates/history", "/v1/order-book", "/v1/bbo", "/v1/mark-price":
		return true
	default:
		return false
	}
}

func publicJSONContent(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func publicGatewayHTTPError(request *http.Request, statusCode int, raw []byte) error {
	var envelope GatewayErrorEnvelope
	code := "gateway_error"
	retryable := false
	correlationID := ""
	existingCommandID := ""
	if decodeStrictPublicJSON(raw, &envelope) == nil && envelope.Error.Code != "" {
		code = safeGatewayToken(envelope.Error.Code, "gateway_error")
		retryable = envelope.Error.Retryable
		correlationID = safeGatewayToken(envelope.Error.CorrelationID, "")
		existingCommandID = safeGatewayToken(envelope.ExistingCommandID, "")
	}
	return &GatewayHTTPError{
		Method: http.MethodGet, Path: request.URL.EscapedPath(), StatusCode: statusCode,
		Code: code, Retryable: retryable, CorrelationID: correlationID,
		ExistingCommandID: existingCommandID,
	}
}

func publicScopeQuery(instrument marketdata.Instrument) (url.Values, error) {
	if err := domain.AccountID(instrument.AccountID).Validate(); err != nil {
		return nil, invalidPublicData("account id")
	}
	if err := domain.InstrumentID(instrument.InstrumentID).Validate(); err != nil {
		return nil, invalidPublicData("instrument id")
	}
	values := url.Values{}
	values.Set("account_id", instrument.AccountID)
	values.Set("instrument_id", instrument.InstrumentID)
	return values, nil
}

func validatePublicScope(instrument marketdata.Instrument, accountID, instrumentID string) error {
	if accountID != instrument.AccountID || instrumentID != instrument.InstrumentID {
		return invalidPublicData("response scope")
	}
	return nil
}

func invalidPublicData(reason string) error {
	return fmt.Errorf("%w: %s", marketdata.ErrInvalidMarketData, reason)
}

func decodeStrictPublicJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing json value")
	}
	return nil
}

type gatewayOptionalDecimal struct {
	Value decimal.Decimal `json:"value"`
	Valid bool            `json:"valid"`
}

type gatewayFundingProjection struct {
	Rate            decimal.Decimal         `json:"rate"`
	MarkPrice       *gatewayOptionalDecimal `json:"mark_price"`
	ObservedAt      time.Time               `json:"observed_at"`
	FundingTime     time.Time               `json:"funding_time"`
	NextFundingTime time.Time               `json:"next_funding_time"`
}

type gatewayFundingResponse struct {
	AccountID    string                    `json:"account_id"`
	InstrumentID string                    `json:"instrument_id"`
	FundingRate  *gatewayFundingProjection `json:"funding_rate"`
}

type gatewayFundingHistoryResponse struct {
	AccountID    string                      `json:"account_id"`
	InstrumentID string                      `json:"instrument_id"`
	Items        *[]gatewayFundingProjection `json:"items"`
	NextCursor   string                      `json:"next_cursor"`
}

type gatewayLevel struct {
	Price    decimal.Decimal `json:"price"`
	Quantity decimal.Decimal `json:"quantity"`
}

type gatewayPageInfo struct {
	Cursor       string    `json:"cursor"`
	Limit        int       `json:"limit"`
	WindowStart  time.Time `json:"window_start"`
	WindowEnd    time.Time `json:"window_end"`
	HasMoreKnown bool      `json:"has_more_known"`
	HasMore      bool      `json:"has_more"`
}

type gatewayOrderBookResponse struct {
	AccountID    string           `json:"account_id"`
	InstrumentID string           `json:"instrument_id"`
	Bids         *[]gatewayLevel  `json:"bids"`
	Asks         *[]gatewayLevel  `json:"asks"`
	ObservedAt   time.Time        `json:"observed_at"`
	Sequence     string           `json:"sequence"`
	Page         *gatewayPageInfo `json:"page"`
}

type gatewayBBOResponse struct {
	AccountID    string        `json:"account_id"`
	InstrumentID string        `json:"instrument_id"`
	Bid          *gatewayLevel `json:"bid"`
	Ask          *gatewayLevel `json:"ask"`
	ObservedAt   time.Time     `json:"observed_at"`
}

type gatewayMarkPriceResponse struct {
	AccountID    string          `json:"account_id"`
	InstrumentID string          `json:"instrument_id"`
	Price        decimal.Decimal `json:"price"`
	ObservedAt   time.Time       `json:"observed_at"`
}

func publicFundingRate(
	instrument marketdata.Instrument,
	projection gatewayFundingProjection,
	requireObservedAt bool,
) (marketdata.FundingRate, error) {
	missingCurrentTime := requireObservedAt && projection.ObservedAt.IsZero()
	missingHistoryTime := !requireObservedAt && projection.ObservedAt.IsZero() && projection.FundingTime.IsZero()
	if projection.MarkPrice == nil || missingCurrentTime || missingHistoryTime {
		return marketdata.FundingRate{}, invalidPublicData("funding rate fields")
	}
	mark := marketdata.OptionalDecimal{
		Value: projection.MarkPrice.Value,
		Valid: projection.MarkPrice.Valid,
	}
	if mark.Valid && !mark.Value.IsPositive() {
		return marketdata.FundingRate{}, invalidPublicData("funding mark price")
	}
	return marketdata.FundingRate{
		Instrument: instrument, Rate: projection.Rate, MarkPrice: mark,
		ObservedAt: projection.ObservedAt, FundingTime: projection.FundingTime,
		NextFundingTime: projection.NextFundingTime,
	}, nil
}

func publicOrderBookSnapshot(
	instrument marketdata.Instrument,
	response gatewayOrderBookResponse,
) (marketdata.OrderBook, error) {
	if response.Bids == nil || response.Asks == nil || response.Page == nil || response.ObservedAt.IsZero() {
		return marketdata.OrderBook{}, invalidPublicData("order book fields")
	}
	bids, err := publicLevels(*response.Bids)
	if err != nil {
		return marketdata.OrderBook{}, err
	}
	asks, err := publicLevels(*response.Asks)
	if err != nil {
		return marketdata.OrderBook{}, err
	}
	return marketdata.OrderBook{
		Instrument: instrument, Kind: "snapshot", Bids: bids, Asks: asks,
		ObservedAt: response.ObservedAt, Sequence: response.Sequence,
		Page: marketdata.PageInfo{
			Cursor: response.Page.Cursor, Limit: response.Page.Limit,
			WindowStart: response.Page.WindowStart, WindowEnd: response.Page.WindowEnd,
			HasMoreKnown: response.Page.HasMoreKnown, HasMore: response.Page.HasMore,
		},
	}, nil
}

func publicBBO(instrument marketdata.Instrument, response gatewayBBOResponse) (marketdata.BBO, error) {
	if err := validatePublicScope(instrument, response.AccountID, response.InstrumentID); err != nil {
		return marketdata.BBO{}, err
	}
	if response.Bid == nil || response.Ask == nil || response.ObservedAt.IsZero() {
		return marketdata.BBO{}, invalidPublicData("bbo fields")
	}
	bid, err := publicLevel(*response.Bid)
	if err != nil {
		return marketdata.BBO{}, err
	}
	ask, err := publicLevel(*response.Ask)
	if err != nil {
		return marketdata.BBO{}, err
	}
	if !bid.Quantity.IsPositive() || !ask.Quantity.IsPositive() || bid.Price.GreaterThan(ask.Price) {
		return marketdata.BBO{}, invalidPublicData("crossed bbo")
	}
	return marketdata.BBO{Instrument: instrument, Bid: bid, Ask: ask, ObservedAt: response.ObservedAt}, nil
}

func publicMarkPrice(
	instrument marketdata.Instrument,
	response gatewayMarkPriceResponse,
) (marketdata.MarkPrice, error) {
	if err := validatePublicScope(instrument, response.AccountID, response.InstrumentID); err != nil {
		return marketdata.MarkPrice{}, err
	}
	if !response.Price.IsPositive() || response.ObservedAt.IsZero() {
		return marketdata.MarkPrice{}, invalidPublicData("mark price fields")
	}
	return marketdata.MarkPrice{
		Instrument: instrument, Price: response.Price, ObservedAt: response.ObservedAt,
	}, nil
}

func publicLevels(source []gatewayLevel) ([]marketdata.Level, error) {
	result := make([]marketdata.Level, len(source))
	for index, level := range source {
		converted, err := publicLevel(level)
		if err != nil {
			return nil, err
		}
		result[index] = converted
	}
	return result, nil
}

func publicLevel(source gatewayLevel) (marketdata.Level, error) {
	if !source.Price.IsPositive() || source.Quantity.IsNegative() {
		return marketdata.Level{}, invalidPublicData("book level")
	}
	return marketdata.Level{Price: source.Price, Quantity: source.Quantity}, nil
}

type publicMarketKind string

const (
	publicMarketKindOrderBook   publicMarketKind = "order_book"
	publicMarketKindBBO         publicMarketKind = "bbo"
	publicMarketKindMarkPrice   publicMarketKind = "mark_price"
	publicMarketKindFundingRate publicMarketKind = "funding_rate"
)

func (kind publicMarketKind) valid() bool {
	switch kind {
	case publicMarketKindOrderBook, publicMarketKindBBO, publicMarketKindMarkPrice, publicMarketKindFundingRate:
		return true
	default:
		return false
	}
}

type gatewayPublicEnvelope struct {
	BootID       string           `json:"boot_id"`
	Sequence     uint64           `json:"sequence"`
	EventID      string           `json:"event_id"`
	AccountID    string           `json:"account_id"`
	InstrumentID string           `json:"instrument_id"`
	Kind         publicMarketKind `json:"kind"`
	EventType    string           `json:"event_type"`
	OccurredAt   time.Time        `json:"occurred_at"`
	Payload      json.RawMessage  `json:"payload"`
}

type gatewayPublicResync struct {
	Reason    string    `json:"reason"`
	Resources *[]string `json:"resources"`
}

type PublicResnapshotRequiredError struct {
	Reason string
}

func (err *PublicResnapshotRequiredError) Error() string {
	if err == nil || err.Reason == "" {
		return marketdata.ErrResnapshotRequired.Error()
	}
	return marketdata.ErrResnapshotRequired.Error() + ": " + safeResyncReason(err.Reason)
}

func (err *PublicResnapshotRequiredError) Unwrap() error {
	return marketdata.ErrResnapshotRequired
}

type publicPayloadDecoder[T any] func(
	marketdata.Instrument,
	marketdata.StreamEventType,
	json.RawMessage,
) (T, error)

type publicMarketSubscription[T any] struct {
	connection *websocket.Conn
	instrument marketdata.Instrument
	kind       publicMarketKind
	decode     publicPayloadDecoder[T]

	readMu       sync.Mutex
	mu           sync.Mutex
	bootID       string
	sequence     uint64
	started      bool
	resyncReason string
	closeOnce    sync.Once
	closeErr     error
}

func newPublicMarketSubscription[T any](
	ctx context.Context,
	client *PublicMarketClient,
	instrument marketdata.Instrument,
	kind publicMarketKind,
	decode publicPayloadDecoder[T],
) (marketdata.Subscription[T], error) {
	target, headers, err := client.websocketTarget(instrument, kind)
	if err != nil {
		return nil, err
	}
	connection, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		HTTPClient: client.httpClient, HTTPHeader: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("public market websocket dial failed")
	}
	connection.SetReadLimit(client.bodyLimit)
	return &publicMarketSubscription[T]{
		connection: connection, instrument: instrument, kind: kind, decode: decode,
	}, nil
}

func (subscription *publicMarketSubscription[T]) Next(
	ctx context.Context,
) (marketdata.StreamEvent[T], error) {
	var zero marketdata.StreamEvent[T]
	if subscription == nil || subscription.connection == nil {
		return zero, fmt.Errorf("public market subscription is closed")
	}
	subscription.readMu.Lock()
	defer subscription.readMu.Unlock()
	subscription.mu.Lock()
	if subscription.resyncReason != "" {
		reason := subscription.resyncReason
		subscription.mu.Unlock()
		return zero, &PublicResnapshotRequiredError{Reason: reason}
	}
	subscription.mu.Unlock()

	messageType, raw, err := subscription.connection.Read(ctx)
	if err != nil {
		return zero, fmt.Errorf("public market websocket read failed")
	}
	if messageType != websocket.MessageText {
		_ = subscription.Close()
		return zero, invalidPublicData("websocket message type")
	}
	var envelope gatewayPublicEnvelope
	if err := decodeStrictPublicJSON(raw, &envelope); err != nil {
		_ = subscription.Close()
		return zero, invalidPublicData("websocket envelope")
	}
	eventType, err := subscription.acceptEnvelope(envelope)
	if err != nil {
		_ = subscription.Close()
		return zero, err
	}
	if envelope.EventType == "resync_required" {
		reason, decodeErr := decodePublicResync(envelope.Payload, subscription.kind)
		if decodeErr != nil {
			_ = subscription.Close()
			return zero, decodeErr
		}
		subscription.mu.Lock()
		subscription.resyncReason = reason
		subscription.mu.Unlock()
		_ = subscription.Close()
		return zero, &PublicResnapshotRequiredError{Reason: reason}
	}
	data, err := subscription.decode(subscription.instrument, eventType, envelope.Payload)
	if err != nil {
		_ = subscription.Close()
		return zero, err
	}
	return marketdata.StreamEvent[T]{
		BootID: envelope.BootID, Sequence: envelope.Sequence, EventID: envelope.EventID,
		Type: eventType, OccurredAt: envelope.OccurredAt,
		Instrument: subscription.instrument, Data: data,
	}, nil
}

func (subscription *publicMarketSubscription[T]) acceptEnvelope(
	envelope gatewayPublicEnvelope,
) (marketdata.StreamEventType, error) {
	if err := domain.BootID(envelope.BootID).Validate(); err != nil {
		return "", invalidPublicData("stream boot id")
	}
	if envelope.Sequence == 0 {
		return "", invalidPublicData("stream sequence")
	}
	if err := domain.EventID(envelope.EventID).Validate(); err != nil {
		return "", invalidPublicData("stream event id")
	}
	if envelope.AccountID != subscription.instrument.AccountID ||
		envelope.InstrumentID != subscription.instrument.InstrumentID {
		return "", invalidPublicData("stream scope")
	}
	if envelope.Kind != subscription.kind || envelope.OccurredAt.IsZero() ||
		len(envelope.Payload) == 0 || !json.Valid(envelope.Payload) {
		return "", invalidPublicData("stream envelope")
	}
	switch envelope.EventType {
	case "snapshot", "update", "resync_required":
	default:
		return "", invalidPublicData("stream event type")
	}

	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	if !subscription.started {
		subscription.started = true
		subscription.bootID = envelope.BootID
		subscription.sequence = envelope.Sequence
		if envelope.EventType == "resync_required" {
			return "", nil
		}
		if envelope.Sequence != 1 || envelope.EventType != "snapshot" {
			subscription.resyncReason = "missing-snapshot"
			return "", &PublicResnapshotRequiredError{Reason: subscription.resyncReason}
		}
		return marketdata.StreamSnapshot, nil
	}
	if envelope.BootID != subscription.bootID {
		subscription.resyncReason = "process-restarted"
		return "", &PublicResnapshotRequiredError{Reason: subscription.resyncReason}
	}
	if envelope.Sequence != subscription.sequence+1 {
		subscription.resyncReason = "sequence-gap"
		return "", &PublicResnapshotRequiredError{Reason: subscription.resyncReason}
	}
	subscription.sequence = envelope.Sequence
	if envelope.EventType == "snapshot" {
		subscription.resyncReason = "unexpected-snapshot"
		return "", &PublicResnapshotRequiredError{Reason: subscription.resyncReason}
	}
	if envelope.EventType == "resync_required" {
		return "", nil
	}
	return marketdata.StreamUpdate, nil
}

func (subscription *publicMarketSubscription[T]) Close() error {
	if subscription == nil || subscription.connection == nil {
		return nil
	}
	subscription.closeOnce.Do(func() {
		subscription.closeErr = subscription.connection.Close(websocket.StatusNormalClosure, "client_closed")
	})
	return subscription.closeErr
}

type gatewayBookUpdate struct {
	Kind       string          `json:"kind"`
	Sequence   string          `json:"sequence"`
	Previous   string          `json:"previous"`
	Resync     bool            `json:"resync"`
	Bids       *[]gatewayLevel `json:"bids"`
	Asks       *[]gatewayLevel `json:"asks"`
	ObservedAt time.Time       `json:"observed_at"`
}

type gatewayFundingUpdate struct {
	Rate            decimal.Decimal `json:"rate"`
	ObservedAt      time.Time       `json:"observed_at"`
	FundingTime     time.Time       `json:"funding_time"`
	NextFundingTime time.Time       `json:"next_funding_time"`
}

func decodeOrderBookPayload(
	instrument marketdata.Instrument,
	eventType marketdata.StreamEventType,
	raw json.RawMessage,
) (marketdata.OrderBook, error) {
	if eventType == marketdata.StreamSnapshot {
		var response gatewayOrderBookResponse
		if err := decodeStrictPublicJSON(raw, &response); err != nil {
			return marketdata.OrderBook{}, invalidPublicData("order book snapshot")
		}
		if err := validatePublicScope(instrument, response.AccountID, response.InstrumentID); err != nil {
			return marketdata.OrderBook{}, err
		}
		return publicOrderBookSnapshot(instrument, response)
	}
	var update gatewayBookUpdate
	if err := decodeStrictPublicJSON(raw, &update); err != nil {
		return marketdata.OrderBook{}, invalidPublicData("order book update")
	}
	if update.Resync {
		return marketdata.OrderBook{}, &PublicResnapshotRequiredError{Reason: "stream-gap"}
	}
	if update.Bids == nil || update.Asks == nil || update.ObservedAt.IsZero() {
		return marketdata.OrderBook{}, invalidPublicData("order book update fields")
	}
	bids, err := publicLevels(*update.Bids)
	if err != nil {
		return marketdata.OrderBook{}, err
	}
	asks, err := publicLevels(*update.Asks)
	if err != nil {
		return marketdata.OrderBook{}, err
	}
	return marketdata.OrderBook{
		Instrument: instrument, Kind: update.Kind, Sequence: update.Sequence,
		Previous: update.Previous, Bids: bids, Asks: asks, ObservedAt: update.ObservedAt,
	}, nil
}

func decodeBBOPayload(
	instrument marketdata.Instrument,
	_ marketdata.StreamEventType,
	raw json.RawMessage,
) (marketdata.BBO, error) {
	var response gatewayBBOResponse
	if err := decodeStrictPublicJSON(raw, &response); err != nil {
		return marketdata.BBO{}, invalidPublicData("bbo payload")
	}
	return publicBBO(instrument, response)
}

func decodeMarkPricePayload(
	instrument marketdata.Instrument,
	_ marketdata.StreamEventType,
	raw json.RawMessage,
) (marketdata.MarkPrice, error) {
	var response gatewayMarkPriceResponse
	if err := decodeStrictPublicJSON(raw, &response); err != nil {
		return marketdata.MarkPrice{}, invalidPublicData("mark price payload")
	}
	return publicMarkPrice(instrument, response)
}

func decodeFundingPayload(
	instrument marketdata.Instrument,
	eventType marketdata.StreamEventType,
	raw json.RawMessage,
) (marketdata.FundingRate, error) {
	if eventType == marketdata.StreamSnapshot {
		var projection gatewayFundingProjection
		if err := decodeStrictPublicJSON(raw, &projection); err != nil {
			return marketdata.FundingRate{}, invalidPublicData("funding snapshot")
		}
		return publicFundingRate(instrument, projection, true)
	}
	var update gatewayFundingUpdate
	if err := decodeStrictPublicJSON(raw, &update); err != nil {
		return marketdata.FundingRate{}, invalidPublicData("funding update")
	}
	if update.ObservedAt.IsZero() {
		return marketdata.FundingRate{}, invalidPublicData("funding update fields")
	}
	return marketdata.FundingRate{
		Instrument: instrument, Rate: update.Rate, ObservedAt: update.ObservedAt,
		FundingTime: update.FundingTime, NextFundingTime: update.NextFundingTime,
	}, nil
}

func decodePublicResync(raw json.RawMessage, kind publicMarketKind) (string, error) {
	var payload gatewayPublicResync
	if err := decodeStrictPublicJSON(raw, &payload); err != nil {
		return "", invalidPublicData("resync payload")
	}
	if payload.Resources == nil || len(*payload.Resources) != 1 || (*payload.Resources)[0] != string(kind) {
		return "", invalidPublicData("resync resources")
	}
	reason := safeResyncReason(payload.Reason)
	if reason == "resync-required" && strings.TrimSpace(payload.Reason) == "" {
		return "", invalidPublicData("resync reason")
	}
	return reason, nil
}

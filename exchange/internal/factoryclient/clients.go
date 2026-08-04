package factoryclient

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type clientMeta struct {
	venue            exchange.Venue
	product          exchange.Product
	catalog          exchange.InstrumentCatalog
	catalogScope     exchange.CatalogScope
	sourceDescriptor exchange.SourceDescriptor
}

func clientMetaFromSettings(venue exchange.Venue, product exchange.Product, settings Settings) clientMeta {
	return clientMeta{
		venue:            venue,
		product:          product,
		catalog:          settings.InstrumentCatalog,
		catalogScope:     settings.CatalogScope,
		sourceDescriptor: settings.SourceDescriptor,
	}
}

func (meta clientMeta) redactedString() string {
	return fmt.Sprintf("exchange/factory.Client{venue:%q, product:%q, credentials:redacted}", meta.venue, meta.product)
}

func withExchangeOperation(err error, operation string) error {
	if err == nil {
		return nil
	}
	var normalized *exchange.Error
	if !errors.As(err, &normalized) {
		return err
	}
	details := normalized.Details()
	details.Operation = operation
	return exchange.NewError(normalized.Kind(), details)
}

func (meta clientMeta) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	if err := meta.catalogContextErr(ctx, "ResolveInstrument"); err != nil {
		return exchange.Instrument{}, err
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return exchange.Instrument{}, meta.catalogInvalidRequest("ResolveInstrument", "instrument symbol is required")
	}
	if meta.catalog == nil || meta.catalogScope == (exchange.CatalogScope{}) {
		return exchange.Instrument{}, meta.catalogMetadataResolution("ResolveInstrument", "instrument catalog is not configured")
	}
	instrument, err := meta.catalog.Resolve(ctx, meta.catalogScope, exchange.InstrumentQuery{Symbol: symbol})
	if err == nil {
		return instrument, nil
	}
	if !isCatalogMetadataNotFound(err) {
		return exchange.Instrument{}, withExchangeOperation(err, "ResolveInstrument")
	}
	instrument, err = meta.catalog.Resolve(ctx, meta.catalogScope, exchange.InstrumentQuery{NativeSymbol: symbol})
	return instrument, withExchangeOperation(err, "ResolveInstrument")
}

func (meta clientMeta) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := meta.catalogContextErr(ctx, "RefreshInstruments"); err != nil {
		return nil, err
	}
	if meta.catalog == nil || meta.catalogScope == (exchange.CatalogScope{}) {
		return nil, meta.catalogMetadataResolution("RefreshInstruments", "instrument catalog is not configured")
	}
	snapshot, err := meta.catalog.Refresh(ctx, meta.catalogScope)
	instruments := append([]exchange.Instrument(nil), snapshot.Instruments...)
	if err != nil {
		return instruments, withExchangeOperation(err, "RefreshInstruments")
	}
	return instruments, nil
}

func (meta clientMeta) catalogContextErr(ctx context.Context, operation string) error {
	if ctx == nil {
		return meta.catalogInvalidRequest(operation, "context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		kind := exchange.KindCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			kind = exchange.KindDeadlineExceeded
		}
		return exchange.NewError(kind, exchange.ErrorDetails{
			Venue:       meta.venue,
			Product:     meta.product,
			Operation:   operation,
			SafeMessage: "request context ended",
		})
	}
	return nil
}

func (meta clientMeta) catalogInvalidRequest(operation, message string) error {
	return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{
		Venue:       meta.venue,
		Product:     meta.product,
		Operation:   operation,
		SafeMessage: message,
	})
}

func (meta clientMeta) catalogMetadataResolution(operation, message string) error {
	return exchange.NewError(exchange.KindMetadataResolution, exchange.ErrorDetails{
		Venue:       meta.venue,
		Product:     meta.product,
		Operation:   operation,
		SafeMessage: message,
	})
}

func isCatalogMetadataNotFound(err error) bool {
	var exchangeErr *exchange.Error
	return errors.As(err, &exchangeErr) &&
		exchangeErr.Kind() == exchange.KindMetadataResolution &&
		exchangeErr.Details().Operation == "InstrumentCatalog.Resolve" &&
		exchangeErr.Details().SafeMessage == "instrument metadata not found"
}

func boundedOrderPage(orders []exchange.Order, limit int, cursor string) exchange.OrderPage {
	if limit > 0 && len(orders) > limit {
		orders = orders[:limit]
	}
	return exchange.OrderPage{
		Orders: orders,
		Page: exchange.PageInfo{
			Cursor: cursor,
			Limit:  limit,
		},
	}
}

func historyPage(cursor string, limit int, start, end time.Time) exchange.PageInfo {
	return exchange.PageInfo{Cursor: cursor, Limit: limit, WindowStart: start, WindowEnd: end}
}

func validateBoundedHistory(cursor string, limit int, start, end time.Time) error {
	if limit < 0 {
		return fmt.Errorf("limit must be non-negative")
	}
	if !start.IsZero() && !end.IsZero() && !start.Before(end) {
		return fmt.Errorf("start must be before end")
	}
	return nil
}

func optionalMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

func withinHalfOpen(value, start, end time.Time) bool {
	if !start.IsZero() && value.Before(start) {
		return false
	}
	if !end.IsZero() && !value.Before(end) {
		return false
	}
	return true
}

func fundingPaymentCompletionStatus(items int, hasMore bool, start, end time.Time, retained bool, observedAt time.Time) exchange.FundingPaymentStatus {
	if hasMore || start.IsZero() || end.IsZero() || observedAt.IsZero() || end.After(observedAt) {
		return exchange.FundingPaymentsIncomplete
	}
	if !retained {
		return exchange.FundingPaymentsIncomplete
	}
	if items == 0 {
		return exchange.FundingPaymentsCompleteEmpty
	}
	return exchange.FundingPaymentsComplete
}

func fundingPaymentRetainedSince(start, asOf time.Time, months int) bool {
	if months <= 0 || start.IsZero() {
		return true
	}
	return !start.UTC().Before(subtractCalendarMonths(asOf.UTC(), months))
}

func subtractCalendarMonths(value time.Time, months int) time.Time {
	year, month, day := value.Date()
	hour, minute, second := value.Clock()
	target := time.Date(year, month-time.Month(months), 1, hour, minute, second, value.Nanosecond(), value.Location())
	lastDay := time.Date(target.Year(), target.Month()+1, 0, hour, minute, second, value.Nanosecond(), value.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(target.Year(), target.Month(), day, hour, minute, second, value.Nanosecond(), value.Location())
}

func fundingPaymentWatermark(cursor string, end time.Time, status exchange.FundingPaymentStatus, observedAt time.Time) exchange.FundingPaymentWatermark {
	verified := (status == exchange.FundingPaymentsComplete || status == exchange.FundingPaymentsCompleteEmpty) &&
		!observedAt.IsZero() && !end.After(observedAt)
	watermark := exchange.FundingPaymentWatermark{
		Cursor:     cursor,
		Revision:   1,
		ObservedAt: observedAt,
		Verified:   verified,
	}
	if verified {
		watermark.EffectiveThrough = end
		watermark.CompleteThrough = end
	}
	return watermark
}

func fundingPaymentCompleteThrough(end time.Time, status exchange.FundingPaymentStatus) time.Time {
	if status == exchange.FundingPaymentsComplete || status == exchange.FundingPaymentsCompleteEmpty {
		return end
	}
	return time.Time{}
}

func stableFundingPaymentFactID(parts ...any) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%v\n", part)
	}
	return "funding-payment:" + hex.EncodeToString(hash.Sum(nil))[:32]
}

type fundingPaymentCursorEnvelope struct {
	Version          int    `json:"version"`
	ScopeDigest      string `json:"scope_digest"`
	VenueCursor      string `json:"venue_cursor,omitempty"`
	Page             int    `json:"page,omitempty"`
	ItemsSeen        int    `json:"items_seen,omitempty"`
	SliceStartMillis int64  `json:"slice_start_millis,omitempty"`
	TraversalStarted bool   `json:"traversal_started"`
}

type fundingPaymentCursorToken struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

func fundingPaymentCursorKey(parts ...string) []byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte("boltertrader:funding-payment-cursor-key:v1\n"))
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hash.Sum(nil)
}

func encodeFundingPaymentCursor(key []byte, envelope fundingPaymentCursorEnvelope) string {
	envelope.Version = 1
	envelope.ScopeDigest = fundingPaymentCursorScopeTokenDigest(key, envelope.ScopeDigest)
	envelope.TraversalStarted = true
	payload, _ := json.Marshal(envelope)
	payloadText := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payloadText))
	token, _ := json.Marshal(fundingPaymentCursorToken{
		Payload:   payloadText,
		Signature: base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	})
	return "fp:v1:" + base64.RawURLEncoding.EncodeToString(token)
}

func decodeFundingPaymentCursor(cursor string, key []byte, expected fundingPaymentCursorEnvelope) (fundingPaymentCursorEnvelope, error) {
	if cursor == "" {
		expected.ScopeDigest = expected.scopeDigest()
		expected.TraversalStarted = true
		return expected, nil
	}
	if !strings.HasPrefix(cursor, "fp:v1:") {
		return fundingPaymentCursorEnvelope{}, errors.New("cursor is not a funding-payment traversal cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "fp:v1:"))
	if err != nil {
		return fundingPaymentCursorEnvelope{}, err
	}
	var token fundingPaymentCursorToken
	if err := json.Unmarshal(raw, &token); err != nil {
		return fundingPaymentCursorEnvelope{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(token.Signature)
	if err != nil {
		return fundingPaymentCursorEnvelope{}, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(token.Payload))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return fundingPaymentCursorEnvelope{}, errors.New("cursor signature is invalid")
	}
	raw, err = base64.RawURLEncoding.DecodeString(token.Payload)
	if err != nil {
		return fundingPaymentCursorEnvelope{}, err
	}
	var envelope fundingPaymentCursorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fundingPaymentCursorEnvelope{}, err
	}
	if envelope.Version != 1 || !envelope.TraversalStarted ||
		envelope.ScopeDigest != fundingPaymentCursorScopeTokenDigest(key, expected.scopeDigest()) {
		return fundingPaymentCursorEnvelope{}, errors.New("cursor scope does not match funding-payment request")
	}
	return envelope, nil
}

func fundingPaymentCursorScopeTokenDigest(key []byte, scopeDigest string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("boltertrader:funding-payment-cursor-scope:v1\n"))
	_, _ = mac.Write([]byte(scopeDigest))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func fundingPaymentCursorScope(instrument *exchange.Instrument, settlementAsset string, start, end time.Time, limit int) fundingPaymentCursorEnvelope {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "scope:v1\nsettlement=%s\nstart=%d\nend=%d\nlimit=%d\n", settlementAsset, start.UTC().UnixNano(), end.UTC().UnixNano(), limit)
	if instrument != nil {
		_, _ = fmt.Fprintf(hash, "instrument=%s\x00%s\n", instrument.Identity.NativeSymbol, instrument.Identity.NativeID)
	} else {
		_, _ = hash.Write([]byte("instrument=all\n"))
	}
	return fundingPaymentCursorEnvelope{ScopeDigest: hex.EncodeToString(hash.Sum(nil))}
}

func (envelope fundingPaymentCursorEnvelope) scopeDigest() string {
	return envelope.ScopeDigest
}

func closeSpotWebSocket(socket exchange.SpotWebSocket) error {
	if socket == nil {
		return nil
	}
	return socket.Close()
}

func closePerpWebSocket(socket exchange.PerpWebSocket) error {
	if socket == nil {
		return nil
	}
	return socket.Close()
}

type spotClient struct {
	meta clientMeta
}

func (client *spotClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	if client == nil {
		return exchange.Instrument{}, clientMeta{product: exchange.ProductSpot}.catalogMetadataResolution("ResolveInstrument", "client is not configured")
	}
	return client.meta.ResolveInstrument(ctx, symbol)
}

func (client *spotClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if client == nil {
		return nil, clientMeta{product: exchange.ProductSpot}.catalogMetadataResolution("RefreshInstruments", "client is not configured")
	}
	return client.meta.RefreshInstruments(ctx)
}

func (client *spotClient) String() string {
	if client == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return client.meta.redactedString()
}

func (client *spotClient) GoString() string {
	return client.String()
}

type perpClient struct {
	meta clientMeta
}

func (client *perpClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	if client == nil {
		return exchange.Instrument{}, clientMeta{product: exchange.ProductPerp}.catalogMetadataResolution("ResolveInstrument", "client is not configured")
	}
	return client.meta.ResolveInstrument(ctx, symbol)
}

func (client *perpClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if client == nil {
		return nil, clientMeta{product: exchange.ProductPerp}.catalogMetadataResolution("RefreshInstruments", "client is not configured")
	}
	return client.meta.RefreshInstruments(ctx)
}

func (client *perpClient) String() string {
	if client == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return client.meta.redactedString()
}

func (client *perpClient) GoString() string {
	return client.String()
}

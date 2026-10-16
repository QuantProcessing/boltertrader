package factoryclient

import (
	"errors"
	"strings"
	"unicode"

	"github.com/QuantProcessing/boltertrader/exchange"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	lighter "github.com/QuantProcessing/boltertrader/sdk/lighter"
	nado "github.com/QuantProcessing/boltertrader/sdk/nado"
	okx "github.com/QuantProcessing/boltertrader/sdk/okx"
)

const maxVenueMessage = 240

// venueMessage extracts the venue's own rejection text from a decoded,
// typed venue error. Transport, context and decoding errors are never
// sources: only fields the venue put in its application error body are.
func venueMessage(err error) string {
	if err == nil {
		return ""
	}
	var candidates []string
	if e := new(binanceperp.APIError); errors.As(err, &e) {
		candidates = append(candidates, e.Message)
	}
	if e := new(binancespot.APIError); errors.As(err, &e) {
		candidates = append(candidates, e.Message)
	}
	if e := new(okx.APIError); errors.As(err, &e) {
		candidates = append(candidates, e.Message)
	}
	if e := new(bybit.ResponseError); errors.As(err, &e) {
		candidates = append(candidates, e.Message)
	}
	if e := new(bitget.ResponseError); errors.As(err, &e) {
		candidates = append(candidates, e.Message)
	}
	if e := new(gate.APIError); errors.As(err, &e) {
		candidates = append(candidates, e.Message, e.Label)
	}
	if e := new(hyperliquid.OrderRejectedError); errors.As(err, &e) {
		candidates = append(candidates, e.Reason)
	}
	var hlAPI hyperliquid.APIError // implements error on the value type
	if errors.As(err, &hlAPI) {
		candidates = append(candidates, hlAPI.Message)
	}
	if hlPointer := (*hyperliquid.APIError)(nil); errors.As(err, &hlPointer) && hlPointer != nil {
		candidates = append(candidates, hlPointer.Message)
	}
	if e := new(lighter.APIError); errors.As(err, &e) {
		candidates = append(candidates, e.Message)
	}
	if e := new(nado.GatewayApplicationError); errors.As(err, &e) {
		candidates = append(candidates, e.Message)
	}
	if e := new(astercommon.VenueError); errors.As(err, &e) {
		candidates = append(candidates, e.Message())
	}
	for _, candidate := range candidates {
		if cleaned := cleanVenueMessage(candidate); cleaned != "" {
			return cleaned
		}
	}
	return ""
}

func cleanVenueMessage(message string) string {
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > maxVenueMessage {
		message = strings.TrimSpace(message[:maxVenueMessage]) + "…"
	}
	return message
}

// withVenueMessage attaches the venue's rejection text to a normalized error
// and acknowledgement. It is applied only to definitive venue rejections.
func withVenueMessage(mapped error, ack *exchange.OrderAcknowledgement, original error) error {
	message := venueMessage(original)
	if message == "" {
		return mapped
	}
	var normalized *exchange.Error
	if !errors.As(mapped, &normalized) {
		return mapped
	}
	// Only definitive application rejections carry venue text. Rate-limit,
	// transport and ambiguous outcomes may have bodies written by proxies or
	// load balancers rather than the venue, so they never do.
	switch normalized.Kind() {
	case exchange.KindVenueRejected, exchange.KindInvalidRequest, exchange.KindPermission,
		exchange.KindAuthentication, exchange.KindNotFound:
	default:
		return mapped
	}
	if details := normalized.Details(); details.HTTPStatus >= 500 || details.HTTPStatus == 429 || details.HTTPStatus == 418 || details.HTTPStatus == 408 {
		return mapped
	}
	details := normalized.Details()
	details.VenueMessage = message
	if ack != nil && ack.State == exchange.AckRejected {
		ack.VenueMessage = message
	}
	return exchange.NewError(normalized.Kind(), details)
}

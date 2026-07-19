package factoryclient

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	lighter "github.com/QuantProcessing/boltertrader/sdk/lighter"
	okx "github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestAllVenueQueryNormalizersRedactUnderlyingErrors(t *testing.T) {
	const canary = "SECRET-QUERY-CANARY"
	raw := errors.New(canary)
	tests := []struct {
		name string
		err  error
	}{
		{name: "binance spot", err: binanceSpotNormalizeErr(raw, "Balances")},
		{name: "binance perp", err: binancePerpNormalizeError("Positions", raw)},
		{name: "okx spot", err: okxNormalizeErr(exchange.ProductSpot, "Balances", raw)},
		{name: "okx perp", err: okxNormalizeErr(exchange.ProductPerp, "Positions", raw)},
		{name: "lighter spot", err: lighterNormalizeErr(exchange.ProductSpot, "Balances", raw)},
		{name: "lighter perp", err: lighterNormalizeErr(exchange.ProductPerp, "Positions", raw)},
		{name: "hyperliquid spot", err: hlNormalizeQueryErr(exchange.ProductSpot, "Balances", raw, nil)},
		{name: "hyperliquid perp", err: hlNormalizeQueryErr(exchange.ProductPerp, "Positions", raw, nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertExchangeErrorRedacts(t, test.err, canary)
		})
	}
}

func TestAllVenueMutationNormalizersRedactVenueMessages(t *testing.T) {
	const canary = "SECRET-MUTATION-CANARY"
	type outcome struct {
		ack exchange.OrderAcknowledgement
		err error
	}
	tests := []struct {
		name string
		run  func() outcome
	}{
		{
			name: "binance spot",
			run: func() outcome {
				ack, err := binanceSpotCommandAck(
					"BTC-USDT",
					exchange.OrderOperationPlace,
					"PlaceOrder",
					"",
					"client-1",
					&binancespot.APIError{Code: -1013, Message: canary, HTTPStatus: http.StatusBadRequest},
				)
				return outcome{ack: ack, err: err}
			},
		},
		{
			name: "binance perp",
			run: func() outcome {
				ack, err := binancePerpCommandErrorAck(
					"PlaceOrder",
					binancePerpAck(exchange.OrderOperationPlace, "BTC-USDT", "", "client-1"),
					&binanceperp.APIError{Code: -1013, Message: canary, HTTPStatus: http.StatusBadRequest},
				)
				return outcome{ack: ack, err: err}
			},
		},
		{
			name: "okx",
			run: func() outcome {
				ack, err := okxCommandTransportAck(
					exchange.ProductPerp,
					exchange.OrderOperationPlace,
					"BTC-USDT",
					"",
					"client-1",
					&okx.APIError{Code: "51000", Message: canary, Details: canary},
				)
				return outcome{ack: ack, err: err}
			},
		},
		{
			name: "lighter",
			run: func() outcome {
				ack, err := lighterCommandErr(
					exchange.ProductPerp,
					exchange.OrderOperationPlace,
					"BTC-USDT",
					"",
					"client-1",
					&lighter.APIError{Code: http.StatusBadRequest, Message: canary},
					nil,
					nil,
				)
				return outcome{ack: ack, err: err}
			},
		},
		{
			name: "hyperliquid",
			run: func() outcome {
				ack, err := hlMutationErr(
					exchange.ProductPerp,
					exchange.OrderOperationPlace,
					"BTC-USDT",
					"",
					"client-1",
					&hyperliquid.OrderRejectedError{Reason: canary},
					nil,
				)
				return outcome{ack: ack, err: err}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.run()
			assertExchangeErrorRedacts(t, result.err, canary)
			for _, rendered := range []string{
				result.ack.VenueMessage,
				fmt.Sprintf("%v", result.ack),
				fmt.Sprintf("%+v", result.ack),
			} {
				if strings.Contains(rendered, canary) {
					t.Fatalf("ack leaked canary: %s", rendered)
				}
			}
		})
	}
}

func assertExchangeErrorRedacts(t *testing.T, err error, canary string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected normalized exchange error")
	}
	var normalized *exchange.Error
	if !errors.As(err, &normalized) {
		t.Fatalf("error type = %T, want *exchange.Error", err)
	}
	for _, rendered := range []string{
		err.Error(),
		normalized.Details().SafeMessage,
		fmt.Sprintf("%v", err),
		fmt.Sprintf("%+v", err),
		fmt.Sprintf("%#v", err),
	} {
		if strings.Contains(rendered, canary) {
			t.Fatalf("normalized error leaked canary: %s", rendered)
		}
	}
}

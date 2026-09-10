package factoryclient

import (
	"context"
	"errors"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestActiveVenueHTTPFailuresKeepRateLimitAndServerEvidence(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			builders := map[string]func(Settings) exchange.InstrumentLoader{
				"binance": func(s Settings) exchange.InstrumentLoader { return NewBinanceInstrumentLoader(exchange.ProductSpot, s) },
				"bybit":   func(s Settings) exchange.InstrumentLoader { return NewBybitInstrumentLoader(exchange.ProductSpot, s) },
				"bitget": func(s Settings) exchange.InstrumentLoader {
					return NewBitgetInstrumentLoader(exchange.ProductSpot, "SPOT", s)
				},
				"okx":   func(s Settings) exchange.InstrumentLoader { return NewOKXInstrumentLoader(exchange.ProductSpot, s) },
				"gate":  func(s Settings) exchange.InstrumentLoader { return NewGateInstrumentLoader(exchange.ProductSpot, s) },
				"aster": func(s Settings) exchange.InstrumentLoader { return NewAsterInstrumentLoader(exchange.ProductSpot, s) },
				"hyperliquid": func(s Settings) exchange.InstrumentLoader {
					return NewHyperliquidInstrumentLoader(exchange.ProductSpot, s)
				},
				"nado":    func(s Settings) exchange.InstrumentLoader { return NewNadoInstrumentLoader(exchange.ProductSpot, s) },
				"lighter": func(s Settings) exchange.InstrumentLoader { return errorTestLighterLoader{settings: s} },
			}
			for venue, build := range builders {
				t.Run(venue, func(t *testing.T) {
					calls := 0
					transport, err := ratelimit.NewTransport(ratelimit.NewCoordinator(nil), roundTripFunc(func(req *http.Request) (*http.Response, error) {
						calls++
						if venuehttp.RequestVenue(req) != venue {
							t.Errorf("venue=%s", venuehttp.RequestVenue(req))
						}
						return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"13"}}, Body: io.NopCloser(strings.NewReader(`{"message":"SECRET_RESPONSE"}`))}, nil
					}))
					if err != nil {
						t.Fatal(err)
					}
					client := build(Settings{HTTPClient: &http.Client{Transport: transport}})

					_, err = client.LoadInstruments(context.Background(), exchange.CatalogScope{Venue: exchange.Venue(venue), Product: exchange.ProductSpot})
					var normalized *exchange.Error
					if !errors.As(err, &normalized) || calls != 1 {
						t.Fatalf("calls=%d err=%v", calls, err)
					}
					details := normalized.Details()
					if details.HTTPStatus != status || details.RetryAfter != 13*time.Second {
						t.Fatalf("details=%+v err=%v", details, err)
					}
					want := exchange.ErrRateLimit
					if status == 503 {
						want = exchange.ErrTransport
					}
					if !errors.Is(err, want) {
						t.Fatalf("err=%v want=%v", err, want)
					}
					assertExchangeErrorRedacts(t, err, "SECRET_RESPONSE")
				})
			}
		})
	}
}
func TestBybitAndBitgetPublicEnvelopeRateLimitRemainsTyped(t *testing.T) {
	for _, venue := range []string{"bybit", "bitget"} {
		t.Run(venue, func(t *testing.T) {
			body := `{"retCode":10006,"retMsg":"SECRET_RESPONSE","result":{}}`
			if venue == "bitget" {
				body = `{"code":"42900","msg":"SECRET_RESPONSE","data":{}}`
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Retry-After": []string{"4"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			var err error
			if venue == "bybit" {
				_, err = bybit.NewClient().WithHTTPClient(client).GetTickers(context.Background(), "linear")
			} else {
				_, err = bitget.NewClient().WithHTTPClient(client).GetTickers(context.Background(), "USDT-FUTURES")
			}
			normalized := normErr(clientMeta{venue: exchange.Venue(venue), product: exchange.ProductPerp}, "Tickers", err)
			var structured *exchange.Error
			if !errors.As(normalized, &structured) || !errors.Is(normalized, exchange.ErrRateLimit) || structured.Details().HTTPStatus != 200 || structured.Details().RetryAfter != 4*time.Second {
				t.Fatal(normalized)
			}
			assertExchangeErrorRedacts(t, normalized, "SECRET_RESPONSE")
		})
	}
}
func TestCommandMetadataDistinguishesBlockedPlaceFromBlockedFollowUpRead(t *testing.T) {
	meta := clientMeta{venue: exchange.VenueBybit, product: exchange.ProductSpot}
	for _, action := range []string{"place", "read"} {
		_, err := commandAck(meta, "PlaceOrder", exchange.OrderOperationPlace, "BTCUSDT", "", "1", &venuehttp.LocalRateLimitError{RetryAfter: time.Second, Action: action})
		var normalized *exchange.Error
		if !errors.As(err, &normalized) || normalized.Details().CommandNotSent != (action == "place") || !errors.Is(err, exchange.ErrRateLimit) {
			t.Fatalf("action=%s err=%v", action, err)
		}
	}
}
func TestServerStatusOverridesNativeLookingCommandRejection(t *testing.T) {
	raw := venuehttp.WithHTTPResponse(&bybit.ResponseError{Code: 10003, Message: "SECRET"}, &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": []string{"2"}}})
	ack, err := commandAck(clientMeta{venue: exchange.VenueBybit, product: exchange.ProductSpot}, "PlaceOrder", exchange.OrderOperationPlace, "BTCUSDT", "", "1", raw)
	if ack.State != exchange.AckAmbiguous || !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatal(ack, err)
	}
	var normalized *exchange.Error
	if !errors.As(err, &normalized) || normalized.Details().HTTPStatus != 503 || normalized.Details().RetryAfter != 2*time.Second {
		t.Fatal(err)
	}
	assertExchangeErrorRedacts(t, err, "SECRET")
}

type errorTestLighterLoader struct{ settings Settings }

func (l errorTestLighterLoader) LoadInstruments(ctx context.Context, _ exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	c := NewLighterSpot("", 0, 0, l.settings)
	defer c.Close()
	_, err := c.Instruments(ctx)
	return exchange.InstrumentSnapshot{}, err
}

func TestEveryVenueCommandAdapterPreservesHTTPOutcomeEvidence(t *testing.T) {
	instrument := exchange.Instrument{Identity: exchange.InstrumentIdentity{NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT"}}
	meta := func(venue exchange.Venue) clientMeta { return clientMeta{venue: venue, product: exchange.ProductSpot} }
	mutations := map[string]func(error) (exchange.OrderAcknowledgement, error){
		"binance spot": func(err error) (exchange.OrderAcknowledgement, error) {
			return binanceSpotCommandAck(instrument, exchange.OrderOperationPlace, "PlaceOrder", "", "1", err)
		},
		"binance perp": func(err error) (exchange.OrderAcknowledgement, error) {
			return binancePerpCommandErrorAck("PlaceOrder", exchange.OrderAcknowledgement{ClientOrderID: "1"}, err)
		},
		"bybit": func(err error) (exchange.OrderAcknowledgement, error) {
			return commandAck(meta(exchange.VenueBybit), "PlaceOrder", exchange.OrderOperationPlace, "BTCUSDT", "", "1", err)
		},
		"bitget": func(err error) (exchange.OrderAcknowledgement, error) {
			return commandAck(meta(exchange.VenueBitget), "PlaceOrder", exchange.OrderOperationPlace, "BTCUSDT", "", "1", err)
		},
		"okx": func(err error) (exchange.OrderAcknowledgement, error) {
			return okxCommandTransportAck(exchange.ProductSpot, exchange.OrderOperationPlace, instrument, "", "1", err)
		},
		"gate": func(err error) (exchange.OrderAcknowledgement, error) {
			return gateCommandErr(meta(exchange.VenueGate), exchange.OrderOperationPlace, instrument, "", "1", err)
		},
		"aster": func(err error) (exchange.OrderAcknowledgement, error) {
			return asterCommandAck(exchange.ProductSpot, exchange.OrderOperationPlace, "BTCUSDT", "", "1", err)
		},
		"lighter": func(err error) (exchange.OrderAcknowledgement, error) {
			return lighterCommandErr(exchange.ProductSpot, exchange.OrderOperationPlace, instrument, "", "1", err, nil, nil)
		},
		"hyperliquid": func(err error) (exchange.OrderAcknowledgement, error) {
			return hlMutationErr(exchange.ProductSpot, exchange.OrderOperationPlace, instrument, "", "1", err, nil)
		},
		"nado": func(err error) (exchange.OrderAcknowledgement, error) {
			ack := exchange.OrderAcknowledgement{ClientOrderID: "1"}
			mapped := (&nadoBase{meta: meta(exchange.VenueNado)}).mutationError("PlaceOrder", err, &ack)
			return ack, mapped
		},
	}
	for venue, mutate := range mutations {
		t.Run(venue, func(t *testing.T) {
			raw := venuehttp.WithHTTPResponse(errors.New("SECRET_MUTATION"), &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": []string{"3"}}})
			ack, err := mutate(raw)
			var normalized *exchange.Error
			if ack.State != exchange.AckAmbiguous || !errors.Is(err, exchange.ErrAmbiguousOutcome) || !errors.As(err, &normalized) || normalized.Details().HTTPStatus != 503 || normalized.Details().RetryAfter != 3*time.Second {
				t.Fatalf("ack=%+v err=%v", ack, err)
			}
			assertExchangeErrorRedacts(t, err, "SECRET_MUTATION")
			for _, action := range []string{"place", "read"} {
				_, err = mutate(&venuehttp.LocalRateLimitError{Action: action, RetryAfter: time.Second})
				if !errors.As(err, &normalized) || !errors.Is(err, exchange.ErrRateLimit) || normalized.Details().CommandNotSent != (action == "place") {
					t.Fatalf("action=%s err=%v", action, err)
				}
			}
		})
	}
}

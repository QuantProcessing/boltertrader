package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

const (
	testLighterPrivateKey     = "00000000000000000000000000000000000000000000000000000000000000000000000000000001"
	testHyperliquidPrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"
	testHyperliquidOwner      = "0x1111111111111111111111111111111111111111"
)

type countingTransport struct {
	requests atomic.Int64
}

func (transport *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.requests.Add(1)
	return nil, errors.New("unexpected request")
}

type hyperliquidAccountCaptureTransport struct {
	user string
}

func (transport *hyperliquidAccountCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var payload map[string]string
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	transport.user = payload["user"]
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"balances":[]}`)),
	}, nil
}

func TestTwentyKnownConfigsInferAndConstructProductClients(t *testing.T) {
	transport := new(countingTransport)
	httpClient := &http.Client{Transport: transport}
	options := []Option{
		WithEndpoint("http://exchange.test"),
		WithWebSocketEndpoint("ws://exchange.test/ws"),
		WithHTTPClient(httpClient),
	}

	var spot exchange.SpotClient
	var perp exchange.PerpClient
	var err error

	spot, err = New(BinanceSpotConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(BinanceUSDPerpConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(OKXSpotConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(OKXUSDTPerpConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(LighterSpotConfig(testLighterPrivateKey, 1, 2, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(LighterPerpConfig(testLighterPrivateKey, 1, 2, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(HyperliquidSpotConfig(testHyperliquidPrivateKey, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(HyperliquidPerpConfig(testHyperliquidPrivateKey, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(BybitSpotConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(BybitUSDTPerpConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	perp, err = New(BybitUSDCPerpConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(BitgetSpotConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(BitgetUSDTPerpConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	perp, err = New(BitgetUSDCPerpConfig("key", "secret", "passphrase", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(GateSpotConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(GateUSDTPerpConfig("key", "secret", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(AsterSpotConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(AsterUSDTPerpConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)
	spot, err = New(NadoSpotConfig(testEVMPrivateKey, "default", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, spot, err)
	perp, err = New(NadoUSDT0PerpConfig(testEVMPrivateKey, "default", optionsWithEnvironment(options, EnvironmentLive)...))
	requireConstructed(t, perp, err)

	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("factory construction performed %d HTTP requests", got)
	}
}

func TestHyperliquidAccountAddressOptionIsExplicitAndValidatedLocally(t *testing.T) {
	transport := new(countingTransport)
	common := []Option{
		WithEnvironment(EnvironmentTestnet),
		WithHTTPClient(&http.Client{Transport: transport}),
		WithAccountAddress(testHyperliquidOwner),
	}
	spot, err := New(HyperliquidSpotConfig(testHyperliquidPrivateKey, common...))
	requireConstructed(t, spot, err)
	perp, err := New(HyperliquidPerpConfig(testHyperliquidPrivateKey, common...))
	requireConstructed(t, perp, err)

	for _, address := range []string{"", "0x1234", "not-an-address", "0x0000000000000000000000000000000000000000"} {
		t.Run(address, func(t *testing.T) {
			_, err := New(HyperliquidSpotConfig(
				testHyperliquidPrivateKey,
				WithEnvironment(EnvironmentTestnet),
				WithAccountAddress(address),
			))
			assertInvalidConfig(t, err)
		})
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("account-address option validation performed %d HTTP requests", got)
	}
}

func TestHyperliquidAccountAddressOptionReachesAccountScopedSDKRequests(t *testing.T) {
	transport := new(hyperliquidAccountCaptureTransport)
	client, err := New(HyperliquidSpotConfig(
		testHyperliquidPrivateKey,
		WithEnvironment(EnvironmentTestnet),
		WithHTTPClient(&http.Client{Transport: transport}),
		WithAccountAddress(testHyperliquidOwner),
	))
	if err != nil {
		t.Fatalf("New Hyperliquid Spot: %v", err)
	}
	if _, err := client.Balances(context.Background()); err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if transport.user != testHyperliquidOwner {
		t.Fatalf("account-scoped SDK user=%q, want explicit owner", transport.user)
	}
}

func TestAccountAddressOptionIsRejectedByConfigsWithoutAddressIdentity(t *testing.T) {
	option := WithAccountAddress(testHyperliquidOwner)
	environment := WithEnvironment(EnvironmentLive)
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "Binance Spot", run: func() error {
			_, err := New(BinanceSpotConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Binance Perp", run: func() error {
			_, err := New(BinanceUSDPerpConfig("key", "secret", environment, option))
			return err
		}},
		{name: "OKX Spot", run: func() error {
			_, err := New(OKXSpotConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "OKX Perp", run: func() error {
			_, err := New(OKXUSDTPerpConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "Lighter Spot", run: func() error {
			_, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, environment, option))
			return err
		}},
		{name: "Lighter Perp", run: func() error {
			_, err := New(LighterPerpConfig(testLighterPrivateKey, 1, 2, environment, option))
			return err
		}},
		{name: "Bybit Spot", run: func() error {
			_, err := New(BybitSpotConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Bybit USDT Perp", run: func() error {
			_, err := New(BybitUSDTPerpConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Bybit USDC Perp", run: func() error {
			_, err := New(BybitUSDCPerpConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Bitget Spot", run: func() error {
			_, err := New(BitgetSpotConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "Bitget USDT Perp", run: func() error {
			_, err := New(BitgetUSDTPerpConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "Bitget USDC Perp", run: func() error {
			_, err := New(BitgetUSDCPerpConfig("key", "secret", "passphrase", environment, option))
			return err
		}},
		{name: "Gate Spot", run: func() error {
			_, err := New(GateSpotConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Gate Perp", run: func() error {
			_, err := New(GateUSDTPerpConfig("key", "secret", environment, option))
			return err
		}},
		{name: "Aster Spot", run: func() error {
			_, err := New(AsterSpotConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, environment, option))
			return err
		}},
		{name: "Aster Perp", run: func() error {
			_, err := New(AsterUSDTPerpConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, environment, option))
			return err
		}},
		{name: "Nado Spot", run: func() error {
			_, err := New(NadoSpotConfig(testEVMPrivateKey, "default", environment, option))
			return err
		}},
		{name: "Nado Perp", run: func() error {
			_, err := New(NadoUSDT0PerpConfig(testEVMPrivateKey, "default", environment, option))
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertInvalidConfig(t, test.run())
		})
	}
}

func TestZeroAndInvalidConfigsFailLocally(t *testing.T) {
	_, err := New(Config[exchange.SpotClient]{})
	assertInvalidConfig(t, err)

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "binance missing key",
			run: func() error {
				_, err := New(BinanceSpotConfig("", "secret", WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "okx missing passphrase",
			run: func() error {
				_, err := New(OKXUSDTPerpConfig("key", "secret", "", WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "lighter negative account",
			run: func() error {
				_, err := New(LighterPerpConfig(testLighterPrivateKey, -1, 2, WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "hyperliquid invalid key",
			run: func() error {
				_, err := New(HyperliquidSpotConfig("not-a-private-key", WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "hyperliquid zero scalar",
			run: func() error {
				_, err := New(HyperliquidPerpConfig(strings.Repeat("0", 64), WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "hyperliquid scalar outside curve",
			run: func() error {
				_, err := New(HyperliquidPerpConfig(strings.Repeat("f", 64), WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "lighter wrong key length",
			run: func() error {
				_, err := New(LighterSpotConfig(strings.Repeat("1", 66), 1, 2, WithEnvironment(EnvironmentLive)))
				return err
			},
		},
		{
			name: "endpoint user info",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithEndpoint("https://user:password@example.com"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "endpoint query",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithEndpoint("https://example.com?credential=leak"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "endpoint fragment",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithEndpoint("https://example.com#fragment"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "websocket endpoint uses HTTP scheme",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithWebSocketEndpoint("https://example.com/ws"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "websocket endpoint user info",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithWebSocketEndpoint("wss://user:password@example.com/ws"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "websocket endpoint query",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithWebSocketEndpoint("wss://example.com/ws?token=leak"),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
		{
			name: "nil http client",
			run: func() error {
				_, err := New(BinanceSpotConfig(
					"key",
					"secret",
					WithHTTPClient(nil),
					WithEnvironment(EnvironmentLive),
				))
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertInvalidConfig(t, test.run())
		})
	}
}

func TestConfigFormattingRedactsCredentials(t *testing.T) {
	canaries := []string{
		"API-KEY-CANARY",
		"SECRET-CANARY",
		"PASSPHRASE-CANARY",
	}
	config := OKXSpotConfig(canaries[0], canaries[1], canaries[2])
	for _, formatted := range []string{
		fmt.Sprintf("%s", config),
		fmt.Sprintf("%v", config),
		fmt.Sprintf("%+v", config),
		fmt.Sprintf("%#v", config),
	} {
		for _, canary := range canaries {
			if strings.Contains(formatted, canary) {
				t.Fatalf("config formatting leaked %q: %s", canary, formatted)
			}
		}
	}
}

func TestConstructedClientFormattingRedactsCredentials(t *testing.T) {
	const (
		lighterPrivateKey     = "0000000000000000000000000000000000000000000000000000000000000000cafebabedeadbeef"
		hyperliquidPrivateKey = "00000000000000000000000000000000000000000000000000000000cafebabe"
	)

	tests := []struct {
		name     string
		build    func() (any, error)
		want     string
		canaries []string
	}{
		{
			name: "binance spot",
			build: func() (any, error) {
				return New(BinanceSpotConfig(
					"BINANCE-SPOT-KEY-CANARY",
					"BINANCE-SPOT-SECRET-CANARY",
					WithEnvironment(EnvironmentLive),
				))
			},
			want:     `exchange/factory.Client{venue:"binance", product:"spot", credentials:redacted}`,
			canaries: []string{"BINANCE-SPOT-KEY-CANARY", "BINANCE-SPOT-SECRET-CANARY"},
		},
		{
			name: "binance perp",
			build: func() (any, error) {
				return New(BinanceUSDPerpConfig(
					"BINANCE-PERP-KEY-CANARY",
					"BINANCE-PERP-SECRET-CANARY",
					WithEnvironment(EnvironmentLive),
				))
			},
			want:     `exchange/factory.Client{venue:"binance", product:"perp", credentials:redacted}`,
			canaries: []string{"BINANCE-PERP-KEY-CANARY", "BINANCE-PERP-SECRET-CANARY"},
		},
		{
			name: "okx spot",
			build: func() (any, error) {
				return New(OKXSpotConfig(
					"OKX-SPOT-KEY-CANARY",
					"OKX-SPOT-SECRET-CANARY",
					"OKX-SPOT-PASSPHRASE-CANARY",
					WithEnvironment(EnvironmentLive),
				))
			},
			want:     `exchange/factory.Client{venue:"okx", product:"spot", credentials:redacted}`,
			canaries: []string{"OKX-SPOT-KEY-CANARY", "OKX-SPOT-SECRET-CANARY", "OKX-SPOT-PASSPHRASE-CANARY"},
		},
		{
			name: "okx perp",
			build: func() (any, error) {
				return New(OKXUSDTPerpConfig(
					"OKX-PERP-KEY-CANARY",
					"OKX-PERP-SECRET-CANARY",
					"OKX-PERP-PASSPHRASE-CANARY",
					WithEnvironment(EnvironmentLive),
				))
			},
			want:     `exchange/factory.Client{venue:"okx", product:"perp", credentials:redacted}`,
			canaries: []string{"OKX-PERP-KEY-CANARY", "OKX-PERP-SECRET-CANARY", "OKX-PERP-PASSPHRASE-CANARY"},
		},
		{
			name: "lighter spot",
			build: func() (any, error) {
				return New(LighterSpotConfig(lighterPrivateKey, 1, 2, WithEnvironment(EnvironmentLive)))
			},
			want:     `exchange/factory.Client{venue:"lighter", product:"spot", credentials:redacted}`,
			canaries: []string{lighterPrivateKey, "cafebabedeadbeef"},
		},
		{
			name: "lighter perp",
			build: func() (any, error) {
				return New(LighterPerpConfig(lighterPrivateKey, 1, 2, WithEnvironment(EnvironmentLive)))
			},
			want:     `exchange/factory.Client{venue:"lighter", product:"perp", credentials:redacted}`,
			canaries: []string{lighterPrivateKey, "cafebabedeadbeef"},
		},
		{
			name: "hyperliquid spot",
			build: func() (any, error) {
				return New(HyperliquidSpotConfig(hyperliquidPrivateKey, WithEnvironment(EnvironmentLive)))
			},
			want:     `exchange/factory.Client{venue:"hyperliquid", product:"spot", credentials:redacted}`,
			canaries: []string{hyperliquidPrivateKey, "cafebabe"},
		},
		{
			name: "hyperliquid perp",
			build: func() (any, error) {
				return New(HyperliquidPerpConfig(hyperliquidPrivateKey, WithEnvironment(EnvironmentLive)))
			},
			want:     `exchange/factory.Client{venue:"hyperliquid", product:"perp", credentials:redacted}`,
			canaries: []string{hyperliquidPrivateKey, "cafebabe"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := test.build()
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			formatted := map[string]string{
				"%s":  fmt.Sprintf("%s", client),
				"%v":  fmt.Sprintf("%v", client),
				"%+v": fmt.Sprintf("%+v", client),
				"%#v": fmt.Sprintf("%#v", client),
			}
			for verb, output := range formatted {
				if output != test.want {
					t.Fatalf("%s formatting = %s, want %s", verb, output, test.want)
				}
				for _, canary := range test.canaries {
					if strings.Contains(output, canary) {
						t.Fatalf("%s formatting leaked %q: %s", verb, canary, output)
					}
				}
			}
		})
	}
}

func TestVenueEnvironmentValidation(t *testing.T) {
	valid := []struct {
		name string
		run  func() error
	}{
		{
			name: "binance spot demo",
			run: func() error {
				_, err := New(BinanceSpotConfig("key", "secret", WithEnvironment(EnvironmentDemo)))
				return err
			},
		},
		{
			name: "binance perp demo",
			run: func() error {
				_, err := New(BinanceUSDPerpConfig("key", "secret", WithEnvironment(EnvironmentDemo)))
				return err
			},
		},
		{
			name: "okx spot demo",
			run: func() error {
				_, err := New(OKXSpotConfig("key", "secret", "passphrase", WithEnvironment(EnvironmentDemo)))
				return err
			},
		},
		{
			name: "okx perp demo",
			run: func() error {
				_, err := New(OKXUSDTPerpConfig("key", "secret", "passphrase", WithEnvironment(EnvironmentDemo)))
				return err
			},
		},
		{
			name: "lighter spot testnet",
			run: func() error {
				_, err := New(LighterSpotConfig(
					testLighterPrivateKey,
					1,
					2,
					WithEnvironment(EnvironmentTestnet),
				))
				return err
			},
		},
		{
			name: "lighter perp testnet",
			run: func() error {
				_, err := New(LighterPerpConfig(
					testLighterPrivateKey,
					1,
					2,
					WithEnvironment(EnvironmentTestnet),
				))
				return err
			},
		},
		{
			name: "hyperliquid spot testnet",
			run: func() error {
				_, err := New(HyperliquidSpotConfig(
					testHyperliquidPrivateKey,
					WithEnvironment(EnvironmentTestnet),
				))
				return err
			},
		},
		{
			name: "hyperliquid perp testnet",
			run: func() error {
				_, err := New(HyperliquidPerpConfig(
					testHyperliquidPrivateKey,
					WithEnvironment(EnvironmentTestnet),
				))
				return err
			},
		},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err != nil {
				t.Fatal(err)
			}
		})
	}

	invalid := []struct {
		name string
		run  func() error
	}{
		{
			name: "binance rejects testnet label",
			run: func() error {
				_, err := New(BinanceSpotConfig("key", "secret", WithEnvironment(EnvironmentTestnet)))
				return err
			},
		},
		{
			name: "lighter rejects demo",
			run: func() error {
				_, err := New(LighterPerpConfig(
					testLighterPrivateKey,
					1,
					2,
					WithEnvironment(EnvironmentDemo),
				))
				return err
			},
		},
		{
			name: "unknown environment",
			run: func() error {
				_, err := New(HyperliquidPerpConfig(
					testHyperliquidPrivateKey,
					WithEnvironment(Environment("mars")),
				))
				return err
			},
		},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			assertInvalidConfig(t, test.run())
		})
	}
}

func TestOmittedEnvironmentFailsLocallyBeforeIO(t *testing.T) {
	transport := new(countingTransport)
	_, err := New(BinanceSpotConfig(
		"key",
		"secret",
		WithHTTPClient(&http.Client{Transport: transport}),
	))
	assertInvalidConfig(t, err)
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("omitted environment performed %d HTTP requests", got)
	}
}

func TestConcurrentConstructionHasNoSharedClientState(t *testing.T) {
	config := BinanceSpotConfig(
		"key",
		"secret",
		WithEndpoint("http://exchange.test"),
		WithHTTPClient(&http.Client{Transport: new(countingTransport)}),
		WithEnvironment(EnvironmentLive),
	)

	const workers = 32
	var wait sync.WaitGroup
	errorsFound := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			client, err := New(config)
			if err != nil {
				errorsFound <- err
				return
			}
			if client == nil {
				errorsFound <- errors.New("nil client")
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func requireConstructed[C any](t *testing.T, client C, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if any(client) == nil {
		t.Fatal("factory returned nil client")
	}
}

func assertInvalidConfig(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, exchange.ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
	if _, ok := err.(*exchange.Error); !ok {
		t.Fatalf("error type = %T, want *exchange.Error", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("invalid config error unexpectedly unwraps: %v", errors.Unwrap(err))
	}
}

func optionsWithEnvironment(options []Option, environment Environment) []Option {
	result := make([]Option, 0, len(options)+1)
	result = append(result, options...)
	return append(result, WithEnvironment(environment))
}

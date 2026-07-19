package factory

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestAllEightConfigsRejectMissingCredentialsLocally(t *testing.T) {
	transport := new(countingTransport)
	option := WithHTTPClient(&http.Client{Transport: transport})
	environment := WithEnvironment(EnvironmentLive)
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "BNS",
			run: func() error {
				_, err := New(BinanceSpotConfig("", "secret", option, environment))
				return err
			},
		},
		{
			name: "BNP",
			run: func() error {
				_, err := New(BinanceUSDPerpConfig("key", "", option, environment))
				return err
			},
		},
		{
			name: "OXS",
			run: func() error {
				_, err := New(OKXSpotConfig("", "secret", "passphrase", option, environment))
				return err
			},
		},
		{
			name: "OXP",
			run: func() error {
				_, err := New(OKXUSDTPerpConfig("key", "secret", "", option, environment))
				return err
			},
		},
		{
			name: "LIS",
			run: func() error {
				_, err := New(LighterSpotConfig("", 1, 2, option, environment))
				return err
			},
		},
		{
			name: "LIP",
			run: func() error {
				_, err := New(LighterPerpConfig("", 1, 2, option, environment))
				return err
			},
		},
		{
			name: "HLS",
			run: func() error {
				_, err := New(HyperliquidSpotConfig("", option, environment))
				return err
			},
		},
		{
			name: "HLP",
			run: func() error {
				_, err := New(HyperliquidPerpConfig("", option, environment))
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if !errors.Is(err, exchange.ErrInvalidConfig) {
				t.Fatalf("error = %v, want ErrInvalidConfig", err)
			}
		})
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("invalid config validation performed %d HTTP requests", got)
	}
}

func TestAllEightConfigsConstructConcurrentlyWithoutIO(t *testing.T) {
	transport := new(countingTransport)
	option := WithHTTPClient(&http.Client{Transport: transport})
	environment := WithEnvironment(EnvironmentLive)
	builders := []struct {
		name string
		run  func() error
	}{
		{
			name: "BNS",
			run: func() error {
				client, err := New(BinanceSpotConfig("key", "secret", option, environment))
				return constructionResult(client, err)
			},
		},
		{
			name: "BNP",
			run: func() error {
				client, err := New(BinanceUSDPerpConfig("key", "secret", option, environment))
				return constructionResult(client, err)
			},
		},
		{
			name: "OXS",
			run: func() error {
				client, err := New(OKXSpotConfig("key", "secret", "passphrase", option, environment))
				return constructionResult(client, err)
			},
		},
		{
			name: "OXP",
			run: func() error {
				client, err := New(OKXUSDTPerpConfig("key", "secret", "passphrase", option, environment))
				return constructionResult(client, err)
			},
		},
		{
			name: "LIS",
			run: func() error {
				client, err := New(LighterSpotConfig(testLighterPrivateKey, 1, 2, option, environment))
				return constructionResult(client, err)
			},
		},
		{
			name: "LIP",
			run: func() error {
				client, err := New(LighterPerpConfig(testLighterPrivateKey, 1, 2, option, environment))
				return constructionResult(client, err)
			},
		},
		{
			name: "HLS",
			run: func() error {
				client, err := New(HyperliquidSpotConfig(testHyperliquidPrivateKey, option, environment))
				return constructionResult(client, err)
			},
		},
		{
			name: "HLP",
			run: func() error {
				client, err := New(HyperliquidPerpConfig(testHyperliquidPrivateKey, option, environment))
				return constructionResult(client, err)
			},
		},
	}

	const copies = 8
	var wait sync.WaitGroup
	failures := make(chan error, len(builders)*copies)
	for _, builder := range builders {
		builder := builder
		for copyIndex := 0; copyIndex < copies; copyIndex++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if err := builder.run(); err != nil {
					failures <- errors.New(builder.name + ": " + err.Error())
				}
			}()
		}
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("concurrent factory construction performed %d HTTP requests", got)
	}
}

func TestAllEightConfigsExposeLazyWebSocketFacets(t *testing.T) {
	options := []Option{
		WithEnvironment(EnvironmentLive),
		WithWebSocketEndpoint("ws://127.0.0.1:1/ws"),
	}
	spotConfigs := []Config[exchange.SpotClient]{
		BinanceSpotConfig("key", "secret", options...),
		OKXSpotConfig("key", "secret", "passphrase", options...),
		LighterSpotConfig(testLighterPrivateKey, 1, 2, options...),
		HyperliquidSpotConfig(testHyperliquidPrivateKey, options...),
	}
	perpConfigs := []Config[exchange.PerpClient]{
		BinanceUSDPerpConfig("key", "secret", options...),
		OKXUSDTPerpConfig("key", "secret", "passphrase", options...),
		LighterPerpConfig(testLighterPrivateKey, 1, 2, options...),
		HyperliquidPerpConfig(testHyperliquidPrivateKey, options...),
	}
	for _, config := range spotConfigs {
		client, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		if client.WebSocket() == nil {
			t.Fatalf("%s returned a nil Spot WebSocket facet", config)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, config := range perpConfigs {
		client, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		if client.WebSocket() == nil {
			t.Fatalf("%s returned a nil Perp WebSocket facet", config)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func constructionResult[C any](client C, err error) error {
	if err != nil {
		return err
	}
	if any(client) == nil {
		return errors.New("nil client")
	}
	return nil
}

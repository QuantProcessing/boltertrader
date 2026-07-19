package factory

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

const (
	testEVMPrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"
	testEVMAddress    = "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf"
)

func TestTwentyKnownConfigsConstructWithoutIO(t *testing.T) {
	transport := new(countingTransport)
	common := []Option{
		WithEnvironment(EnvironmentLive),
		WithEndpoint("http://exchange.test"),
		WithWebSocketEndpoint("ws://exchange.test/ws"),
		WithHTTPClient(&http.Client{Transport: transport}),
	}

	spotConfigs := []Config[exchange.SpotClient]{
		BinanceSpotConfig("key", "secret", common...),
		OKXSpotConfig("key", "secret", "passphrase", common...),
		LighterSpotConfig(testLighterPrivateKey, 1, 2, common...),
		HyperliquidSpotConfig(testHyperliquidPrivateKey, common...),
		BybitSpotConfig("key", "secret", common...),
		BitgetSpotConfig("key", "secret", "passphrase", common...),
		GateSpotConfig("key", "secret", common...),
		AsterSpotConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, common...),
		NadoSpotConfig(testEVMPrivateKey, "default", common...),
	}
	perpConfigs := []Config[exchange.PerpClient]{
		BinanceUSDPerpConfig("key", "secret", common...),
		OKXUSDTPerpConfig("key", "secret", "passphrase", common...),
		LighterPerpConfig(testLighterPrivateKey, 1, 2, common...),
		HyperliquidPerpConfig(testHyperliquidPrivateKey, common...),
		BybitUSDTPerpConfig("key", "secret", common...),
		BybitUSDCPerpConfig("key", "secret", common...),
		BitgetUSDTPerpConfig("key", "secret", "passphrase", common...),
		BitgetUSDCPerpConfig("key", "secret", "passphrase", common...),
		GateUSDTPerpConfig("key", "secret", common...),
		AsterUSDTPerpConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, common...),
		NadoUSDT0PerpConfig(testEVMPrivateKey, "default", common...),
	}

	for _, config := range spotConfigs {
		client, err := New(config)
		requireConstructed(t, client, err)
		if client.WebSocket() == nil {
			t.Fatalf("%s returned nil Spot WebSocket facet", config)
		}
		if err := client.Close(); err != nil {
			t.Fatalf("%s close: %v", config, err)
		}
	}
	for _, config := range perpConfigs {
		client, err := New(config)
		requireConstructed(t, client, err)
		if client.WebSocket() == nil {
			t.Fatalf("%s returned nil Perp WebSocket facet", config)
		}
		if err := client.Close(); err != nil {
			t.Fatalf("%s close: %v", config, err)
		}
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("factory construction performed %d HTTP requests", got)
	}
}

func TestExpandedVenueConstantsAreStable(t *testing.T) {
	tests := map[exchange.Venue]string{
		exchange.VenueBybit:  "bybit",
		exchange.VenueBitget: "bitget",
		exchange.VenueGate:   "gate",
		exchange.VenueAster:  "aster",
		exchange.VenueNado:   "nado",
	}
	for venue, want := range tests {
		if string(venue) != want {
			t.Fatalf("venue %q = %q, want %q", want, venue, want)
		}
	}
}

func TestExpandedConfigsValidateCredentialsAndEnvironmentLocally(t *testing.T) {
	valid := []struct {
		name string
		run  func() error
	}{
		{"bybit spot demo", func() error {
			_, err := New(BybitSpotConfig("key", "secret", WithEnvironment(EnvironmentDemo)))
			return err
		}},
		{"bybit usdc perp testnet", func() error {
			_, err := New(BybitUSDCPerpConfig("key", "secret", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
		{"bitget spot demo", func() error {
			_, err := New(BitgetSpotConfig("key", "secret", "passphrase", WithEnvironment(EnvironmentDemo)))
			return err
		}},
		{"gate perp testnet", func() error {
			_, err := New(GateUSDTPerpConfig("key", "secret", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
		{"aster spot testnet", func() error {
			_, err := New(AsterSpotConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, WithEnvironment(EnvironmentTestnet)))
			return err
		}},
		{"nado perp testnet", func() error {
			_, err := New(NadoUSDT0PerpConfig(testEVMPrivateKey, "default", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
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
		{"bitget rejects testnet label", func() error {
			_, err := New(BitgetSpotConfig("key", "secret", "passphrase", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
		{"gate rejects demo label", func() error {
			_, err := New(GateSpotConfig("key", "secret", WithEnvironment(EnvironmentDemo)))
			return err
		}},
		{"aster signer mismatch", func() error {
			_, err := New(AsterSpotConfig(testEVMAddress, testEVMPrivateKey, "0x1111111111111111111111111111111111111111", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
		{"aster zero user", func() error {
			_, err := New(AsterUSDTPerpConfig("0x0000000000000000000000000000000000000000", testEVMPrivateKey, "", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
		{"nado long subaccount", func() error {
			_, err := New(NadoSpotConfig(testEVMPrivateKey, "thirteen-bytes", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
		{"nado empty subaccount", func() error {
			_, err := New(NadoSpotConfig(testEVMPrivateKey, "", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
		{"nado whitespace subaccount", func() error {
			_, err := New(NadoUSDT0PerpConfig(testEVMPrivateKey, " \t ", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
		{"nado invalid key", func() error {
			_, err := New(NadoUSDT0PerpConfig("not-a-key", "default", WithEnvironment(EnvironmentTestnet)))
			return err
		}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if !errors.Is(err, exchange.ErrInvalidConfig) {
				t.Fatalf("error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestExpandedConstructedClientFormattingRedactsCredentials(t *testing.T) {
	tests := []struct {
		name     string
		build    func() (any, error)
		want     string
		canaries []string
	}{
		{"bybit spot", func() (any, error) {
			return New(BybitSpotConfig("BYBIT-KEY-CANARY", "BYBIT-SECRET-CANARY", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"bybit", product:"spot", credentials:redacted}`, []string{"BYBIT-KEY-CANARY", "BYBIT-SECRET-CANARY"}},
		{"bybit usdt perp", func() (any, error) {
			return New(BybitUSDTPerpConfig("BYBIT-KEY-CANARY", "BYBIT-SECRET-CANARY", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"bybit", product:"perp", credentials:redacted}`, []string{"BYBIT-KEY-CANARY", "BYBIT-SECRET-CANARY"}},
		{"bybit usdc perp", func() (any, error) {
			return New(BybitUSDCPerpConfig("BYBIT-KEY-CANARY", "BYBIT-SECRET-CANARY", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"bybit", product:"perp", credentials:redacted}`, []string{"BYBIT-KEY-CANARY", "BYBIT-SECRET-CANARY"}},
		{"bitget spot", func() (any, error) {
			return New(BitgetSpotConfig("BITGET-KEY-CANARY", "BITGET-SECRET-CANARY", "BITGET-PASS-CANARY", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"bitget", product:"spot", credentials:redacted}`, []string{"BITGET-KEY-CANARY", "BITGET-SECRET-CANARY", "BITGET-PASS-CANARY"}},
		{"bitget usdt perp", func() (any, error) {
			return New(BitgetUSDTPerpConfig("BITGET-KEY-CANARY", "BITGET-SECRET-CANARY", "BITGET-PASS-CANARY", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"bitget", product:"perp", credentials:redacted}`, []string{"BITGET-KEY-CANARY", "BITGET-SECRET-CANARY", "BITGET-PASS-CANARY"}},
		{"bitget usdc perp", func() (any, error) {
			return New(BitgetUSDCPerpConfig("BITGET-KEY-CANARY", "BITGET-SECRET-CANARY", "BITGET-PASS-CANARY", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"bitget", product:"perp", credentials:redacted}`, []string{"BITGET-KEY-CANARY", "BITGET-SECRET-CANARY", "BITGET-PASS-CANARY"}},
		{"gate spot", func() (any, error) {
			return New(GateSpotConfig("GATE-KEY-CANARY", "GATE-SECRET-CANARY", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"gate", product:"spot", credentials:redacted}`, []string{"GATE-KEY-CANARY", "GATE-SECRET-CANARY"}},
		{"gate perp", func() (any, error) {
			return New(GateUSDTPerpConfig("GATE-KEY-CANARY", "GATE-SECRET-CANARY", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"gate", product:"perp", credentials:redacted}`, []string{"GATE-KEY-CANARY", "GATE-SECRET-CANARY"}},
		{"aster spot", func() (any, error) {
			return New(AsterSpotConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"aster", product:"spot", credentials:redacted}`, []string{testEVMPrivateKey}},
		{"aster perp", func() (any, error) {
			return New(AsterUSDTPerpConfig(testEVMAddress, testEVMPrivateKey, testEVMAddress, WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"aster", product:"perp", credentials:redacted}`, []string{testEVMPrivateKey}},
		{"nado spot", func() (any, error) {
			return New(NadoSpotConfig(testEVMPrivateKey, "default", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"nado", product:"spot", credentials:redacted}`, []string{testEVMPrivateKey}},
		{"nado perp", func() (any, error) {
			return New(NadoUSDT0PerpConfig(testEVMPrivateKey, "default", WithEnvironment(EnvironmentLive)))
		}, `exchange/factory.Client{venue:"nado", product:"perp", credentials:redacted}`, []string{testEVMPrivateKey}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := test.build()
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for _, output := range []string{
				fmt.Sprintf("%s", client),
				fmt.Sprintf("%v", client),
				fmt.Sprintf("%+v", client),
				fmt.Sprintf("%#v", client),
			} {
				if output != test.want {
					t.Fatalf("formatting = %s, want %s", output, test.want)
				}
				for _, canary := range test.canaries {
					if strings.Contains(output, canary) {
						t.Fatalf("formatting leaked credential %q", canary)
					}
				}
			}
		})
	}
}

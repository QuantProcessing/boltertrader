package factory

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/internal/factoryclient"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Config is a typed construction ticket returned by the known venue/product
// config constructors in this package.
//
// The build hook is intentionally unexported: callers can hold and pass tickets
// through New, but they cannot forge a working ticket outside this package.
type Config[C any] struct {
	name     string
	validate func(settings) error
	build    func(settings) C
	options  settings
}

// New constructs the product-specific exchange client described by cfg.
// Construction is local only: it validates configuration and wires SDK clients,
// but it never performs exchange I/O.
func New[C any](cfg Config[C]) (C, error) {
	var zero C
	if cfg.validate == nil || cfg.build == nil {
		return zero, invalidConfig("unknown factory config")
	}
	if err := cfg.options.validate(); err != nil {
		return zero, err
	}
	if err := cfg.validate(cfg.options); err != nil {
		return zero, err
	}
	return cfg.build(cfg.options), nil
}

// Environment selects the venue environment used for local SDK construction.
type Environment string

const (
	EnvironmentLive    Environment = "live"
	EnvironmentDemo    Environment = "demo"
	EnvironmentTestnet Environment = "testnet"
)

// Option configures local construction of an exchange client. Options are sealed
// so new option kinds must be added in this package with validation.
type Option struct {
	apply func(*settings) error
}

// WithEndpoint overrides the SDK REST endpoint used by the constructed client.
func WithEndpoint(endpoint string) Option {
	return Option{apply: func(settings *settings) error {
		endpoint = strings.TrimSpace(endpoint)
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return invalidConfig("endpoint must be an absolute http(s) URL")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return invalidConfig("endpoint scheme must be http or https")
		}
		if parsed.User != nil {
			return invalidConfig("endpoint must not include user info")
		}
		if parsed.RawQuery != "" {
			return invalidConfig("endpoint must not include a query string")
		}
		if parsed.Fragment != "" {
			return invalidConfig("endpoint must not include a fragment")
		}
		settings.endpoint = strings.TrimRight(endpoint, "/")
		return nil
	}}
}

// WithWebSocketEndpoint overrides the public/private SDK WebSocket endpoint
// used by the constructed client. The endpoint is validated locally and no
// connection is opened during New.
func WithWebSocketEndpoint(endpoint string) Option {
	return Option{apply: func(settings *settings) error {
		endpoint = strings.TrimSpace(endpoint)
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return invalidConfig("websocket endpoint must be an absolute ws(s) URL")
		}
		if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
			return invalidConfig("websocket endpoint scheme must be ws or wss")
		}
		if parsed.User != nil {
			return invalidConfig("websocket endpoint must not include user info")
		}
		if parsed.RawQuery != "" {
			return invalidConfig("websocket endpoint must not include a query string")
		}
		if parsed.Fragment != "" {
			return invalidConfig("websocket endpoint must not include a fragment")
		}
		settings.webSocketEndpoint = strings.TrimRight(endpoint, "/")
		return nil
	}}
}

// WithEnvironment selects the venue environment. Callers must choose an
// environment explicitly; omitted or zero environments are invalid.
func WithEnvironment(environment Environment) Option {
	return Option{apply: func(settings *settings) error {
		switch environment {
		case EnvironmentLive, EnvironmentDemo, EnvironmentTestnet:
			settings.environment = environment
			return nil
		default:
			return invalidConfig("unknown environment")
		}
	}}
}

// WithHTTPClient installs the HTTP client to be used by the underlying SDK
// client. The client is not used during New.
func WithHTTPClient(client *http.Client) Option {
	return Option{apply: func(settings *settings) error {
		if client == nil {
			return invalidConfig("http client must not be nil")
		}
		settings.httpClient = client
		return nil
	}}
}

// WithAccountAddress explicitly selects the account identity queried by
// account-scoped venue APIs. Hyperliquid Spot and Perp use it for agent-wallet
// owner accounts; when omitted, the signer address remains the account identity.
func WithAccountAddress(accountAddress string) Option {
	return Option{apply: func(settings *settings) error {
		accountAddress = strings.TrimSpace(accountAddress)
		if !ethcommon.IsHexAddress(accountAddress) {
			return invalidConfig("account address must be a 20-byte hex address")
		}
		normalized := ethcommon.HexToAddress(accountAddress)
		if normalized == (ethcommon.Address{}) {
			return invalidConfig("account address must not be the zero address")
		}
		settings.accountAddress = normalized.Hex()
		return nil
	}}
}

// BinanceSpotConfig returns a typed ticket for a Binance Spot client.
func BinanceSpotConfig(apiKey, secretKey string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("binance-spot", options, func(settings settings) error {
		if err := requireEnvironment("binance spot", settings.environment, EnvironmentLive, EnvironmentDemo); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("binance spot", apiKey, secretKey)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewBinanceSpot(apiKey, secretKey, factorySettings(settings))
	})
}

// BinanceUSDPerpConfig returns a typed ticket for a Binance USD-M Perp client.
func BinanceUSDPerpConfig(apiKey, secretKey string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("binance-usd-perp", options, func(settings settings) error {
		if err := requireEnvironment("binance usd perp", settings.environment, EnvironmentLive, EnvironmentDemo); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("binance usd perp", apiKey, secretKey)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewBinanceUSDPerp(apiKey, secretKey, factorySettings(settings))
	})
}

// OKXSpotConfig returns a typed ticket for an OKX Spot client.
func OKXSpotConfig(apiKey, secretKey, passphrase string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("okx-spot", options, func(settings settings) error {
		if err := requireEnvironment("okx spot", settings.environment, EnvironmentLive, EnvironmentDemo); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("okx spot", apiKey, secretKey, passphrase)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewOKXSpot(apiKey, secretKey, passphrase, factorySettings(settings))
	})
}

// OKXUSDTPerpConfig returns a typed ticket for an OKX USDT-linear SWAP client.
func OKXUSDTPerpConfig(apiKey, secretKey, passphrase string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("okx-usdt-perp", options, func(settings settings) error {
		if err := requireEnvironment("okx usdt perp", settings.environment, EnvironmentLive, EnvironmentDemo); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("okx usdt perp", apiKey, secretKey, passphrase)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewOKXUSDTPerp(apiKey, secretKey, passphrase, factorySettings(settings))
	})
}

// LighterSpotConfig returns a typed ticket for a Lighter Spot client.
func LighterSpotConfig(privateKey string, accountIndex int64, keyIndex uint8, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("lighter-spot", options, func(settings settings) error {
		if err := requireEnvironment("lighter spot", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		if err := requireLighterPrivateKey("lighter spot", privateKey); err != nil {
			return err
		}
		if accountIndex < 0 {
			return invalidConfig("lighter spot account index must be non-negative")
		}
		return nil
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewLighterSpot(privateKey, accountIndex, keyIndex, factorySettings(settings))
	})
}

// LighterPerpConfig returns a typed ticket for a Lighter Perp client.
func LighterPerpConfig(privateKey string, accountIndex int64, keyIndex uint8, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("lighter-perp", options, func(settings settings) error {
		if err := requireEnvironment("lighter perp", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		if err := requireLighterPrivateKey("lighter perp", privateKey); err != nil {
			return err
		}
		if accountIndex < 0 {
			return invalidConfig("lighter perp account index must be non-negative")
		}
		return nil
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewLighterPerp(privateKey, accountIndex, keyIndex, factorySettings(settings))
	})
}

// HyperliquidSpotConfig returns a typed ticket for a Hyperliquid Spot client.
func HyperliquidSpotConfig(privateKey string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("hyperliquid-spot", options, func(settings settings) error {
		if err := requireEnvironment("hyperliquid spot", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		return requireHyperliquidPrivateKey("hyperliquid spot", privateKey)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewHyperliquidSpot(privateKey, factorySettings(settings))
	})
}

// HyperliquidPerpConfig returns a typed ticket for a Hyperliquid standard Perp client.
func HyperliquidPerpConfig(privateKey string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("hyperliquid-perp", options, func(settings settings) error {
		if err := requireEnvironment("hyperliquid perp", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		return requireHyperliquidPrivateKey("hyperliquid perp", privateKey)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewHyperliquidPerp(privateKey, factorySettings(settings))
	})
}

// BybitSpotConfig returns a typed ticket for a Bybit Spot client.
func BybitSpotConfig(apiKey, secretKey string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("bybit-spot", options, func(settings settings) error {
		if err := requireEnvironment("bybit spot", settings.environment, EnvironmentLive, EnvironmentDemo, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("bybit spot", apiKey, secretKey)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewBybitSpot(apiKey, secretKey, factorySettings(settings))
	})
}

// BybitUSDTPerpConfig returns a typed ticket for a Bybit USDT-linear Perp client.
func BybitUSDTPerpConfig(apiKey, secretKey string, options ...Option) Config[exchange.PerpClient] {
	return bybitPerpConfig("bybit-usdt-perp", "USDT", apiKey, secretKey, options)
}

// BybitUSDCPerpConfig returns a typed ticket for a Bybit USDC-linear Perp client.
func BybitUSDCPerpConfig(apiKey, secretKey string, options ...Option) Config[exchange.PerpClient] {
	return bybitPerpConfig("bybit-usdc-perp", "USDC", apiKey, secretKey, options)
}

func bybitPerpConfig(name, settleCoin, apiKey, secretKey string, options []Option) Config[exchange.PerpClient] {
	return perpConfig(name, options, func(settings settings) error {
		if err := requireEnvironment(name, settings.environment, EnvironmentLive, EnvironmentDemo, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials(name, apiKey, secretKey)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewBybitLinearPerp(apiKey, secretKey, settleCoin, factorySettings(settings))
	})
}

// BitgetSpotConfig returns a typed ticket for a Bitget Spot client.
func BitgetSpotConfig(apiKey, secretKey, passphrase string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("bitget-spot", options, func(settings settings) error {
		if err := requireEnvironment("bitget spot", settings.environment, EnvironmentLive, EnvironmentDemo); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("bitget spot", apiKey, secretKey, passphrase)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewBitgetSpot(apiKey, secretKey, passphrase, factorySettings(settings))
	})
}

// BitgetUSDTPerpConfig returns a typed ticket for a Bitget USDT-linear Perp client.
func BitgetUSDTPerpConfig(apiKey, secretKey, passphrase string, options ...Option) Config[exchange.PerpClient] {
	return bitgetPerpConfig("bitget-usdt-perp", "USDT-FUTURES", apiKey, secretKey, passphrase, options)
}

// BitgetUSDCPerpConfig returns a typed ticket for a Bitget USDC-linear Perp client.
func BitgetUSDCPerpConfig(apiKey, secretKey, passphrase string, options ...Option) Config[exchange.PerpClient] {
	return bitgetPerpConfig("bitget-usdc-perp", "USDC-FUTURES", apiKey, secretKey, passphrase, options)
}

func bitgetPerpConfig(name, productType, apiKey, secretKey, passphrase string, options []Option) Config[exchange.PerpClient] {
	return perpConfig(name, options, func(settings settings) error {
		if err := requireEnvironment(name, settings.environment, EnvironmentLive, EnvironmentDemo); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials(name, apiKey, secretKey, passphrase)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewBitgetPerp(apiKey, secretKey, passphrase, productType, factorySettings(settings))
	})
}

// GateSpotConfig returns a typed ticket for a Gate Spot client.
func GateSpotConfig(apiKey, secretKey string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("gate-spot", options, func(settings settings) error {
		if err := requireEnvironment("gate spot", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("gate spot", apiKey, secretKey)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewGateSpot(apiKey, secretKey, factorySettings(settings))
	})
}

// GateUSDTPerpConfig returns a typed ticket for a Gate USDT-settled Perp client.
func GateUSDTPerpConfig(apiKey, secretKey string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("gate-usdt-perp", options, func(settings settings) error {
		if err := requireEnvironment("gate usdt perp", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("gate usdt perp", apiKey, secretKey)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewGateUSDTPerp(apiKey, secretKey, factorySettings(settings))
	})
}

// AsterSpotConfig returns a typed ticket for an Aster Spot client.
func AsterSpotConfig(userAddress, apiWalletPrivateKey, expectedSigner string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("aster-spot", options, func(settings settings) error {
		if err := requireEnvironment("aster spot", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireAsterCredentials("aster spot", userAddress, apiWalletPrivateKey, expectedSigner)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewAsterSpot(userAddress, apiWalletPrivateKey, expectedSigner, factorySettings(settings))
	})
}

// AsterUSDTPerpConfig returns a typed ticket for an Aster USDT-linear Perp client.
func AsterUSDTPerpConfig(userAddress, apiWalletPrivateKey, expectedSigner string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("aster-usdt-perp", options, func(settings settings) error {
		if err := requireEnvironment("aster usdt perp", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireAsterCredentials("aster usdt perp", userAddress, apiWalletPrivateKey, expectedSigner)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewAsterUSDTPerp(userAddress, apiWalletPrivateKey, expectedSigner, factorySettings(settings))
	})
}

// NadoSpotConfig returns a typed ticket for a Nado USDT0 Spot client.
func NadoSpotConfig(privateKey, subaccount string, options ...Option) Config[exchange.SpotClient] {
	subaccount = strings.TrimSpace(subaccount)
	return spotConfig("nado-spot", options, func(settings settings) error {
		if err := requireEnvironment("nado spot", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNadoCredentials("nado spot", privateKey, subaccount)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewNadoSpot(privateKey, subaccount, factorySettings(settings))
	})
}

// NadoUSDT0PerpConfig returns a typed ticket for a Nado USDT0-settled Perp client.
func NadoUSDT0PerpConfig(privateKey, subaccount string, options ...Option) Config[exchange.PerpClient] {
	subaccount = strings.TrimSpace(subaccount)
	return perpConfig("nado-usdt0-perp", options, func(settings settings) error {
		if err := requireEnvironment("nado usdt0 perp", settings.environment, EnvironmentLive, EnvironmentTestnet); err != nil {
			return err
		}
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNadoCredentials("nado usdt0 perp", privateKey, subaccount)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewNadoUSDT0Perp(privateKey, subaccount, factorySettings(settings))
	})
}

func spotConfig(name string, options []Option, validate func(settings) error, build func(settings) exchange.SpotClient) Config[exchange.SpotClient] {
	return Config[exchange.SpotClient]{
		name:     name,
		validate: validate,
		build:    build,
		options:  newSettings(options),
	}
}

func perpConfig(name string, options []Option, validate func(settings) error, build func(settings) exchange.PerpClient) Config[exchange.PerpClient] {
	return Config[exchange.PerpClient]{
		name:     name,
		validate: validate,
		build:    build,
		options:  newSettings(options),
	}
}

func (cfg Config[C]) String() string {
	if cfg.name == "" {
		return "exchange/factory.Config{redacted, invalid}"
	}
	return fmt.Sprintf("exchange/factory.Config{name:%q, credentials:redacted}", cfg.name)
}

func (cfg Config[C]) GoString() string {
	return cfg.String()
}

func requireNonEmptyCredentials(scope string, values ...string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return invalidConfig(scope + " credentials are required")
		}
	}
	return nil
}

func rejectUnsupportedAccountAddress(settings settings) error {
	if settings.accountAddress != "" {
		return invalidConfig("account address is only supported by Hyperliquid configs")
	}
	return nil
}

func requireEnvironment(scope string, environment Environment, allowed ...Environment) error {
	for _, candidate := range allowed {
		if environment == candidate {
			return nil
		}
	}
	return invalidConfig(fmt.Sprintf("%s does not support %s environment", scope, environment))
}

func requireLighterPrivateKey(scope, privateKey string) error {
	privateKey = strings.TrimPrefix(strings.TrimSpace(privateKey), "0x")
	if err := requireHexScalar(scope, privateKey, 80); err != nil {
		return err
	}
	return nil
}

func requireHyperliquidPrivateKey(scope, privateKey string) error {
	privateKey = strings.TrimPrefix(strings.TrimSpace(privateKey), "0x")
	if err := requireHexScalar(scope, privateKey, 64); err != nil {
		return err
	}
	if _, err := crypto.HexToECDSA(privateKey); err != nil {
		return invalidConfig(scope + " private key must be a valid secp256k1 scalar")
	}
	return nil
}

func requireAsterCredentials(scope, userAddress, privateKey, expectedSigner string) error {
	if !ethcommon.IsHexAddress(strings.TrimSpace(userAddress)) ||
		ethcommon.HexToAddress(userAddress) == (ethcommon.Address{}) {
		return invalidConfig(scope + " user address must be a non-zero 20-byte hex address")
	}
	if err := requireHyperliquidPrivateKey(scope, privateKey); err != nil {
		return err
	}
	if strings.TrimSpace(expectedSigner) == "" {
		return nil
	}
	if !ethcommon.IsHexAddress(strings.TrimSpace(expectedSigner)) ||
		ethcommon.HexToAddress(expectedSigner) == (ethcommon.Address{}) {
		return invalidConfig(scope + " expected signer must be a non-zero 20-byte hex address")
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKey), "0x"))
	if err != nil {
		return invalidConfig(scope + " private key must be a valid secp256k1 scalar")
	}
	if crypto.PubkeyToAddress(key.PublicKey) != ethcommon.HexToAddress(expectedSigner) {
		return invalidConfig(scope + " derived signer does not match expected signer")
	}
	return nil
}

func requireNadoCredentials(scope, privateKey, subaccount string) error {
	if err := requireHyperliquidPrivateKey(scope, privateKey); err != nil {
		return err
	}
	if subaccount == "" {
		return invalidConfig(scope + " subaccount is required")
	}
	if len([]byte(subaccount)) > 12 {
		return invalidConfig(scope + " subaccount must not exceed 12 bytes")
	}
	return nil
}

func requireHexScalar(scope, privateKey string, length int) error {
	if len(privateKey) != length {
		return invalidConfig(fmt.Sprintf("%s private key must be exactly %d hex characters", scope, length))
	}
	nonZero := false
	for _, char := range privateKey {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return invalidConfig(scope + " private key must be hex encoded")
		}
		if char != '0' {
			nonZero = true
		}
	}
	if !nonZero {
		return invalidConfig(scope + " private key must not be all zero")
	}
	return nil
}

func invalidConfig(message string) error {
	return exchange.NewError(exchange.KindInvalidConfig, exchange.ErrorDetails{
		Operation:   "factory.New",
		SafeMessage: message,
	})
}

func factorySettings(settings settings) factoryclient.Settings {
	return factoryclient.Settings{
		Endpoint:          settings.endpoint,
		WebSocketEndpoint: settings.webSocketEndpoint,
		HTTPClient:        settings.httpClient,
		Environment:       string(settings.environment),
		AccountAddress:    settings.accountAddress,
	}
}

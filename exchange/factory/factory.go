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
	if err := registerInstrumentSource(cfg.name, cfg.options); err != nil {
		return zero, err
	}
	return cfg.build(cfg.options), nil
}

// Environment selects the venue environment used for local SDK construction.
//
// There is one value. The type survives its own collapse because it is what
// gateway/account/factory.go's factoryEnvironment converts a stored
// venue_environment string into: a row still saying "demo" has to be refused
// somewhere, and a typed target is what makes the refusal a conversion failure
// rather than a string that happens not to match.
type Environment string

const EnvironmentLive Environment = "live"

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
//
// The explicitness is worth more now than when there were three values to
// choose between. A caller that drops the option gets "environment is required"
// from validate(), which is a far better failure than silently constructing a
// client against whatever the zero value happened to mean.
func WithEnvironment(environment Environment) Option {
	return Option{apply: func(settings *settings) error {
		if environment != EnvironmentLive {
			return invalidConfig("unknown environment")
		}
		settings.environment = environment
		return nil
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

// WithInstrumentCatalog injects the catalog used for this constructed client.
// When omitted, factory.New registers public metadata sources on the process
// default catalog. Registration is local and does not load metadata.
func WithInstrumentCatalog(catalog exchange.InstrumentCatalog) Option {
	return Option{apply: func(settings *settings) error {
		if catalog == nil {
			return invalidConfig("instrument catalog must not be nil")
		}
		settings.catalog = catalog
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
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("binance spot", apiKey, secretKey)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewBinanceSpot(apiKey, secretKey, factorySettingsForConfig("binance-spot", settings))
	})
}

// BinanceUSDPerpConfig returns a typed ticket for a Binance USD-M Perp client.
func BinanceUSDPerpConfig(apiKey, secretKey string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("binance-usd-perp", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("binance usd perp", apiKey, secretKey)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewBinanceUSDPerp(apiKey, secretKey, factorySettingsForConfig("binance-usd-perp", settings))
	})
}

// OKXSpotConfig returns a typed ticket for an OKX Spot client.
func OKXSpotConfig(apiKey, secretKey, passphrase string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("okx-spot", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("okx spot", apiKey, secretKey, passphrase)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewOKXSpot(apiKey, secretKey, passphrase, factorySettingsForConfig("okx-spot", settings))
	})
}

// OKXUSDTPerpConfig returns a typed ticket for an OKX USDT-linear SWAP client.
func OKXUSDTPerpConfig(apiKey, secretKey, passphrase string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("okx-usdt-perp", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("okx usdt perp", apiKey, secretKey, passphrase)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewOKXUSDTPerp(apiKey, secretKey, passphrase, factorySettingsForConfig("okx-usdt-perp", settings))
	})
}

// LighterSpotConfig returns a typed ticket for a Lighter Spot client.
func LighterSpotConfig(privateKey string, accountIndex int64, keyIndex uint8, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("lighter-spot", options, func(settings settings) error {
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
		return factoryclient.NewLighterSpot(privateKey, accountIndex, keyIndex, factorySettingsForConfig("lighter-spot", settings))
	})
}

// LighterPerpConfig returns a typed ticket for a Lighter Perp client.
func LighterPerpConfig(privateKey string, accountIndex int64, keyIndex uint8, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("lighter-perp", options, func(settings settings) error {
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
		return factoryclient.NewLighterPerp(privateKey, accountIndex, keyIndex, factorySettingsForConfig("lighter-perp", settings))
	})
}

// HyperliquidSpotConfig returns a typed ticket for a Hyperliquid Spot client.
func HyperliquidSpotConfig(privateKey string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("hyperliquid-spot", options, func(settings settings) error {
		return requireHyperliquidPrivateKey("hyperliquid spot", privateKey)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewHyperliquidSpot(privateKey, factorySettingsForConfig("hyperliquid-spot", settings))
	})
}

// HyperliquidPerpConfig returns a typed ticket for a Hyperliquid standard Perp client.
func HyperliquidPerpConfig(privateKey string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("hyperliquid-perp", options, func(settings settings) error {
		return requireHyperliquidPrivateKey("hyperliquid perp", privateKey)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewHyperliquidPerp(privateKey, factorySettingsForConfig("hyperliquid-perp", settings))
	})
}

// BybitSpotConfig returns a typed ticket for a Bybit Spot client.
func BybitSpotConfig(apiKey, secretKey string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("bybit-spot", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("bybit spot", apiKey, secretKey)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewBybitSpot(apiKey, secretKey, factorySettingsForConfig("bybit-spot", settings))
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
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials(name, apiKey, secretKey)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewBybitLinearPerp(apiKey, secretKey, settleCoin, factorySettingsForConfig(name, settings))
	})
}

// BitgetSpotConfig returns a typed ticket for a Bitget Spot client.
func BitgetSpotConfig(apiKey, secretKey, passphrase string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("bitget-spot", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("bitget spot", apiKey, secretKey, passphrase)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewBitgetSpot(apiKey, secretKey, passphrase, factorySettingsForConfig("bitget-spot", settings))
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
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials(name, apiKey, secretKey, passphrase)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewBitgetPerp(apiKey, secretKey, passphrase, productType, factorySettingsForConfig(name, settings))
	})
}

// GateSpotConfig returns a typed ticket for a Gate Spot client.
func GateSpotConfig(apiKey, secretKey string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("gate-spot", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("gate spot", apiKey, secretKey)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewGateSpot(apiKey, secretKey, factorySettingsForConfig("gate-spot", settings))
	})
}

// GateUSDTPerpConfig returns a typed ticket for a Gate USDT-settled Perp client.
func GateUSDTPerpConfig(apiKey, secretKey string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("gate-usdt-perp", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNonEmptyCredentials("gate usdt perp", apiKey, secretKey)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewGateUSDTPerp(apiKey, secretKey, factorySettingsForConfig("gate-usdt-perp", settings))
	})
}

// AsterSpotConfig returns a typed ticket for an Aster Spot client.
func AsterSpotConfig(userAddress, apiWalletPrivateKey, expectedSigner string, options ...Option) Config[exchange.SpotClient] {
	return spotConfig("aster-spot", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireAsterCredentials("aster spot", userAddress, apiWalletPrivateKey, expectedSigner)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewAsterSpot(userAddress, apiWalletPrivateKey, expectedSigner, factorySettingsForConfig("aster-spot", settings))
	})
}

// AsterUSDTPerpConfig returns a typed ticket for an Aster USDT-linear Perp client.
func AsterUSDTPerpConfig(userAddress, apiWalletPrivateKey, expectedSigner string, options ...Option) Config[exchange.PerpClient] {
	return perpConfig("aster-usdt-perp", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireAsterCredentials("aster usdt perp", userAddress, apiWalletPrivateKey, expectedSigner)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewAsterUSDTPerp(userAddress, apiWalletPrivateKey, expectedSigner, factorySettingsForConfig("aster-usdt-perp", settings))
	})
}

// NadoSpotConfig returns a typed ticket for a Nado USDT0 Spot client.
func NadoSpotConfig(privateKey, subaccount string, options ...Option) Config[exchange.SpotClient] {
	subaccount = strings.TrimSpace(subaccount)
	return spotConfig("nado-spot", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNadoCredentials("nado spot", privateKey, subaccount)
	}, func(settings settings) exchange.SpotClient {
		return factoryclient.NewNadoSpot(privateKey, subaccount, factorySettingsForConfig("nado-spot", settings))
	})
}

// NadoUSDT0PerpConfig returns a typed ticket for a Nado USDT0-settled Perp client.
func NadoUSDT0PerpConfig(privateKey, subaccount string, options ...Option) Config[exchange.PerpClient] {
	subaccount = strings.TrimSpace(subaccount)
	return perpConfig("nado-usdt0-perp", options, func(settings settings) error {
		if err := rejectUnsupportedAccountAddress(settings); err != nil {
			return err
		}
		return requireNadoCredentials("nado usdt0 perp", privateKey, subaccount)
	}, func(settings settings) exchange.PerpClient {
		return factoryclient.NewNadoUSDT0Perp(privateKey, subaccount, factorySettingsForConfig("nado-usdt0-perp", settings))
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

func registerInstrumentSource(configName string, settings settings) error {
	catalog := settings.catalog
	if catalog == nil {
		catalog = exchange.DefaultInstrumentCatalog()
	}
	for _, sourceName := range catalogRegistrationNames(configName) {
		scope, descriptor, ok := catalogSourceForConfig(sourceName, settings)
		if !ok {
			continue
		}
		loader := instrumentLoaderForConfig(sourceName, settings)
		if loader == nil {
			continue
		}
		if err := catalog.RegisterSource(scope, descriptor, loader); err != nil {
			return err
		}
	}
	return nil
}

func catalogRegistrationNames(configName string) []string {
	switch configName {
	case "nado-spot", "nado-usdt0-perp":
		return []string{"nado-spot", "nado-usdt0-perp"}
	default:
		return []string{configName}
	}
}

func catalogSourceForConfig(configName string, settings settings) (exchange.CatalogScope, exchange.SourceDescriptor, bool) {
	venue, product, settlement, ok := catalogIdentityForConfig(configName)
	if !ok {
		return exchange.CatalogScope{}, exchange.SourceDescriptor{}, false
	}
	sourceName := configName
	if configName == "nado-spot" || configName == "nado-usdt0-perp" {
		sourceName = "nado"
	}
	descriptor := exchange.SourceDescriptor{
		Name:              sourceName,
		Endpoint:          settings.endpoint,
		WebSocketEndpoint: settings.webSocketEndpoint,
		Transport:         "default",
	}
	factoryclient.ApplyDefaultCatalogEndpoints(configName, &descriptor)
	if settings.httpClient != nil {
		descriptor.Transport = "custom-http-client"
	}
	scope := exchange.CatalogScope{
		Venue:               venue,
		Environment:         exchange.Environment(settings.environment),
		Product:             product,
		Settlement:          settlement,
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	return scope, descriptor, true
}

func instrumentLoaderForConfig(configName string, settings settings) exchange.InstrumentLoader {
	switch configName {
	case "binance-spot":
		return factoryclient.NewBinanceInstrumentLoader(exchange.ProductSpot, factorySettingsForConfig(configName, settings))
	case "binance-usd-perp":
		return factoryclient.NewBinanceInstrumentLoader(exchange.ProductPerp, factorySettingsForConfig(configName, settings))
	case "lighter-spot":
		return factoryclient.NewLighterInstrumentLoader(exchange.ProductSpot, factorySettingsForConfig(configName, settings))
	case "lighter-perp":
		return factoryclient.NewLighterInstrumentLoader(exchange.ProductPerp, factorySettingsForConfig(configName, settings))
	case "okx-spot":
		return factoryclient.NewOKXInstrumentLoader(exchange.ProductSpot, factorySettingsForConfig(configName, settings))
	case "okx-usdt-perp":
		return factoryclient.NewOKXInstrumentLoader(exchange.ProductPerp, factorySettingsForConfig(configName, settings))
	case "hyperliquid-spot":
		return factoryclient.NewHyperliquidInstrumentLoader(exchange.ProductSpot, factorySettingsForConfig(configName, settings))
	case "hyperliquid-perp":
		return factoryclient.NewHyperliquidInstrumentLoader(exchange.ProductPerp, factorySettingsForConfig(configName, settings))
	case "aster-spot":
		return factoryclient.NewAsterInstrumentLoader(exchange.ProductSpot, factorySettingsForConfig(configName, settings))
	case "aster-usdt-perp":
		return factoryclient.NewAsterInstrumentLoader(exchange.ProductPerp, factorySettingsForConfig(configName, settings))
	case "bybit-spot":
		return factoryclient.NewBybitInstrumentLoader(exchange.ProductSpot, factorySettingsForConfig(configName, settings))
	case "bybit-usdt-perp", "bybit-usdc-perp":
		return factoryclient.NewBybitInstrumentLoader(exchange.ProductPerp, factorySettingsForConfig(configName, settings))
	case "bitget-spot":
		return factoryclient.NewBitgetInstrumentLoader(exchange.ProductSpot, "", factorySettingsForConfig(configName, settings))
	case "bitget-usdt-perp":
		return factoryclient.NewBitgetInstrumentLoader(exchange.ProductPerp, "USDT-FUTURES", factorySettingsForConfig(configName, settings))
	case "bitget-usdc-perp":
		return factoryclient.NewBitgetInstrumentLoader(exchange.ProductPerp, "USDC-FUTURES", factorySettingsForConfig(configName, settings))
	case "gate-spot":
		return factoryclient.NewGateInstrumentLoader(exchange.ProductSpot, factorySettingsForConfig(configName, settings))
	case "gate-usdt-perp":
		return factoryclient.NewGateInstrumentLoader(exchange.ProductPerp, factorySettingsForConfig(configName, settings))
	case "nado-spot":
		return factoryclient.NewNadoInstrumentLoader(exchange.ProductSpot, factorySettingsForConfig(configName, settings))
	case "nado-usdt0-perp":
		return factoryclient.NewNadoInstrumentLoader(exchange.ProductPerp, factorySettingsForConfig(configName, settings))
	default:
		return nil
	}
}

// derivativeSnapshotLoaderForConfig mirrors instrumentLoaderForConfig for the
// credential-free funding surface.
//
// Only perpetual configs appear: spot has no funding. Every configured perp
// venue, including Aster's public v3 premium-index surface, is represented.
func derivativeSnapshotLoaderForConfig(configName string, settings settings) factoryclient.DerivativeSnapshotLoader {
	switch configName {
	case "binance-usd-perp":
		return factoryclient.NewBinanceDerivativeSnapshotLoader("USDT", factorySettingsForConfig(configName, settings))
	case "aster-usdt-perp":
		return factoryclient.NewAsterDerivativeSnapshotLoader("USDT", factorySettingsForConfig(configName, settings))
	case "okx-usdt-perp":
		return factoryclient.NewOKXDerivativeSnapshotLoader("USDT", factorySettingsForConfig(configName, settings))
	case "lighter-perp":
		return factoryclient.NewLighterDerivativeSnapshotLoader(factorySettingsForConfig(configName, settings))
	case "hyperliquid-perp":
		return factoryclient.NewHyperliquidDerivativeSnapshotLoader(factorySettingsForConfig(configName, settings))
	case "bybit-usdt-perp":
		return factoryclient.NewBybitDerivativeSnapshotLoader("USDT", factorySettingsForConfig(configName, settings))
	case "bybit-usdc-perp":
		return factoryclient.NewBybitDerivativeSnapshotLoader("USDC", factorySettingsForConfig(configName, settings))
	case "bitget-usdt-perp":
		return factoryclient.NewBitgetDerivativeSnapshotLoader("USDT-FUTURES", factorySettingsForConfig(configName, settings))
	case "bitget-usdc-perp":
		return factoryclient.NewBitgetDerivativeSnapshotLoader("USDC-FUTURES", factorySettingsForConfig(configName, settings))
	case "gate-usdt-perp":
		return factoryclient.NewGateDerivativeSnapshotLoader(factorySettingsForConfig(configName, settings))
	case "nado-usdt0-perp":
		return factoryclient.NewNadoDerivativeSnapshotLoader(factorySettingsForConfig(configName, settings))
	default:
		return nil
	}
}

// fundingHistoryLoaderForConfig is the per-instrument counterpart of
// derivativeSnapshotLoaderForConfig.
//
// It covers ten of the board's eleven route configs. Nado is deliberately
// absent: it publishes the current funding rate but no settled series through
// the supported public API, so there is no loader to build rather than one
// that answers with an empty series. A caller
// gets nil and the connector turns that into a configuration error, which is
// the difference between "this venue keeps no history" and "this instrument was
// paid nothing" — indistinguishable to the reader of an empty chart, and the
// first is permanent where the second is news.
func fundingHistoryLoaderForConfig(configName string, settings settings) exchange.FundingHistoryLoader {
	switch configName {
	case "binance-usd-perp":
		return factoryclient.NewBinanceFundingHistoryLoader(factorySettingsForConfig(configName, settings))
	case "aster-usdt-perp":
		return factoryclient.NewAsterFundingHistoryLoader(factorySettingsForConfig(configName, settings))
	case "okx-usdt-perp":
		return factoryclient.NewOKXFundingHistoryLoader(factorySettingsForConfig(configName, settings))
	case "lighter-perp":
		return factoryclient.NewLighterFundingHistoryLoader(factorySettingsForConfig(configName, settings))
	case "hyperliquid-perp":
		return factoryclient.NewHyperliquidFundingHistoryLoader(factorySettingsForConfig(configName, settings))
	case "bybit-usdt-perp", "bybit-usdc-perp":
		// One constructor for both settlements, where the board needed two:
		// Bybit's history endpoint is addressed by symbol inside a single
		// linear category, so there is no mixed surface left to filter.
		return factoryclient.NewBybitFundingHistoryLoader(factorySettingsForConfig(configName, settings))
	case "bitget-usdt-perp":
		return factoryclient.NewBitgetFundingHistoryLoader("USDT-FUTURES", factorySettingsForConfig(configName, settings))
	case "bitget-usdc-perp":
		return factoryclient.NewBitgetFundingHistoryLoader("USDC-FUTURES", factorySettingsForConfig(configName, settings))
	case "gate-usdt-perp":
		return factoryclient.NewGateFundingHistoryLoader(factorySettingsForConfig(configName, settings))
	default:
		return nil
	}
}

func factorySettings(settings settings) factoryclient.Settings {
	catalog := settings.catalog
	if catalog == nil {
		catalog = exchange.DefaultInstrumentCatalog()
	}
	return factoryclient.Settings{
		Endpoint:          settings.endpoint,
		WebSocketEndpoint: settings.webSocketEndpoint,
		HTTPClient:        settings.httpClient,
		AccountAddress:    settings.accountAddress,
		InstrumentCatalog: catalog,
	}
}

func factorySettingsForConfig(configName string, settings settings) factoryclient.Settings {
	out := factorySettings(settings)
	if scope, descriptor, ok := catalogSourceForConfig(configName, settings); ok {
		out.CatalogScope = scope
		out.SourceDescriptor = descriptor
	}
	return out
}

package testenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/shopspring/decimal"
)

const (
	BinanceDemoAPIKeyEnv      = "BINANCE_DEMO_API_KEY"
	BinanceDemoAPISecretEnv   = "BINANCE_DEMO_API_SECRET"
	BinanceDemoEnableWriteEnv = "BOLTER_ENABLE_BINANCE_DEMO_WRITES"

	OKXDemoAPIKeyEnv           = "OKX_DEMO_API_KEY"
	OKXDemoAPISecretEnv        = "OKX_DEMO_API_SECRET"
	OKXDemoAPIPassphraseEnv    = "OKX_DEMO_API_PASSPHRASE"
	OKXDemoEnableWriteEnv      = "BOLTER_ENABLE_OKX_DEMO_WRITES"
	OKXDemoAllowCustomWriteEnv = "BOLTER_ALLOW_OKX_DEMO_CUSTOM_WRITES"

	OKXDemoMaxNotionalUSDTEnv = "OKX_DEMO_MAX_NOTIONAL_USDT"
	OKXDemoSpotSymbolEnv      = "OKX_DEMO_SPOT_SYMBOL"
	OKXDemoPerpSymbolEnv      = "OKX_DEMO_PERP_SYMBOL"
	OKXDemoHostProfileEnv     = "OKX_DEMO_HOST_PROFILE"
	OKXDemoRESTBaseURLEnv     = "OKX_DEMO_REST_BASE_URL"
	OKXDemoWSBaseURLEnv       = "OKX_DEMO_WS_BASE_URL"

	OKXDemoDefaultMaxNotionalUSDT = "100"
	OKXDemoDefaultSpotSymbol      = "ETH-USDT"
	OKXDemoDefaultPerpSymbol      = "ETH-USDT-SWAP"

	OKXDemoHostProfileGlobal = "global"
	OKXDemoHostProfileEEA    = "eea"
	OKXDemoHostProfileCustom = "custom"

	BybitDemoAPIKeyEnv          = "BYBIT_DEMO_API_KEY"
	BybitDemoAPISecretEnv       = "BYBIT_DEMO_API_SECRET"
	BybitDemoEnableWriteEnv     = "BOLTER_ENABLE_BYBIT_DEMO_WRITES"
	BybitDemoSpotSymbolEnv      = "BYBIT_DEMO_SYMBOL"
	BybitDemoUSDTPerpSymbolEnv  = "BYBIT_DEMO_USDT_PERP_SYMBOL"
	BybitDemoUSDCPerpSymbolEnv  = "BYBIT_DEMO_USDC_PERP_SYMBOL"
	BybitDemoMaxNotionalUSDTEnv = "BYBIT_DEMO_MAX_NOTIONAL_USDT"
	BybitDemoMaxNotionalUSDCEnv = "BYBIT_DEMO_MAX_NOTIONAL_USDC"

	BybitTestnetAPIKeyEnv          = "BYBIT_TESTNET_API_KEY"
	BybitTestnetAPISecretEnv       = "BYBIT_TESTNET_API_SECRET"
	BybitTestnetEnableWriteEnv     = "BOLTER_ENABLE_BYBIT_TESTNET_WRITES"
	BybitTestnetSpotSymbolEnv      = "BYBIT_TESTNET_SYMBOL"
	BybitTestnetUSDTPerpSymbolEnv  = "BYBIT_TESTNET_USDT_PERP_SYMBOL"
	BybitTestnetUSDCPerpSymbolEnv  = "BYBIT_TESTNET_USDC_PERP_SYMBOL"
	BybitTestnetMaxNotionalUSDTEnv = "BYBIT_TESTNET_MAX_NOTIONAL_USDT"
	BybitTestnetMaxNotionalUSDCEnv = "BYBIT_TESTNET_MAX_NOTIONAL_USDC"

	BybitDefaultMaxNotionalUSDT = "100"
	BybitDefaultMaxNotionalUSDC = "100"
	BybitDefaultSpotSymbol      = "BTCUSDT"
	BybitDefaultUSDTPerpSymbol  = "BTCUSDT"
	BybitDefaultUSDCPerpSymbol  = "BTCPERP"

	BitgetDemoAPIKeyEnv           = "BITGET_DEMO_API_KEY"
	BitgetDemoAPISecretEnv        = "BITGET_DEMO_SECRET_KEY"
	BitgetDemoPassphraseEnv       = "BITGET_DEMO_PASSPHRASE"
	BitgetDemoEnableWriteEnv      = "BOLTER_ENABLE_BITGET_DEMO_WRITES"
	BitgetDemoAllowCustomWriteEnv = "BOLTER_ALLOW_BITGET_DEMO_CUSTOM_WRITES"
	BitgetDemoSpotSymbolEnv       = "BITGET_DEMO_SYMBOL"
	BitgetDemoUSDTPerpSymbolEnv   = "BITGET_DEMO_USDT_PERP_SYMBOL"
	BitgetDemoUSDCPerpSymbolEnv   = "BITGET_DEMO_USDC_PERP_SYMBOL"
	BitgetDemoMaxNotionalUSDTEnv  = "BITGET_DEMO_MAX_NOTIONAL_USDT"
	BitgetDemoMaxNotionalUSDCEnv  = "BITGET_DEMO_MAX_NOTIONAL_USDC"
	BitgetDemoRESTBaseURLEnv      = "BITGET_DEMO_REST_BASE_URL"
	BitgetDemoPublicWSURLEnv      = "BITGET_DEMO_PUBLIC_WS_URL"
	BitgetDemoPrivateWSURLEnv     = "BITGET_DEMO_PRIVATE_WS_URL"

	// Deprecated: use the matching BITGET_DEMO_* environment variables.
	BitgetLegacyTestnetAPIKeyEnv          = "BITGET_TESTNET_API_KEY"
	BitgetLegacyTestnetAPISecretEnv       = "BITGET_TESTNET_SECRET_KEY"
	BitgetLegacyTestnetPassphraseEnv      = "BITGET_TESTNET_PASSPHRASE"
	BitgetLegacyTestnetSpotSymbolEnv      = "BITGET_TESTNET_SYMBOL"
	BitgetLegacyTestnetUSDTPerpSymbolEnv  = "BITGET_TESTNET_USDT_PERP_SYMBOL"
	BitgetLegacyTestnetUSDCPerpSymbolEnv  = "BITGET_TESTNET_USDC_PERP_SYMBOL"
	BitgetLegacyTestnetMaxNotionalUSDTEnv = "BITGET_TESTNET_MAX_NOTIONAL_USDT"
	BitgetLegacyTestnetMaxNotionalUSDCEnv = "BITGET_TESTNET_MAX_NOTIONAL_USDC"
	BitgetLegacyTestnetRESTBaseURLEnv     = "BITGET_TESTNET_REST_BASE_URL"
	BitgetLegacyTestnetPublicWSURLEnv     = "BITGET_TESTNET_PUBLIC_WS_URL"
	BitgetLegacyTestnetPrivateWSURLEnv    = "BITGET_TESTNET_PRIVATE_WS_URL"

	BitgetDefaultMaxNotionalUSDT = "100"
	BitgetDefaultMaxNotionalUSDC = "100"
	BitgetDefaultSpotSymbol      = "BTCUSDT"
	BitgetDefaultUSDTPerpSymbol  = "BTCUSDT"
	BitgetDefaultUSDCPerpSymbol  = "BTCPERP"

	GateTestnetAPIKeyEnv           = "GATE_TESTNET_API_KEY"
	GateTestnetAPISecretEnv        = "GATE_TESTNET_API_SECRET"
	GateTestnetEnableWriteEnv      = "BOLTER_ENABLE_GATE_TESTNET_WRITES"
	GateTestnetSpotSymbolEnv       = "GATE_TESTNET_SPOT_SYMBOL"
	GateTestnetUSDTPerpSymbolEnv   = "GATE_TESTNET_USDT_PERP_SYMBOL"
	GateTestnetMaxNotionalUSDTEnv  = "GATE_TESTNET_MAX_NOTIONAL_USDT"
	GateTestnetRESTBaseURLEnv      = "GATE_TESTNET_REST_BASE_URL"
	GateTestnetSpotWSURLEnv        = "GATE_TESTNET_SPOT_WS_URL"
	GateTestnetUSDTFuturesWSURLEnv = "GATE_TESTNET_USDT_FUTURES_WS_URL"
	// Deprecated: use GateTestnetUSDTFuturesWSURLEnv.
	GateTestnetFuturesUSDTWSURLEnv    = "GATE_TESTNET_FUTURES_USDT_WS_URL"
	GateTestnetDefaultMaxNotionalUSDT = "100"
	GateTestnetDefaultSpotSymbol      = "ETH_USDT"
	GateTestnetDefaultUSDTPerpSymbol  = "BTC_USDT"

	HyperliquidTestnetPrivateKeyEnv      = "HYPERLIQUID_TESTNET_PK"
	HyperliquidTestnetEnableWriteEnv     = "BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES"
	HyperliquidTestnetAccountAddressEnv  = "HYPERLIQUID_ACCOUNT_ADDRESS"
	HyperliquidTestnetVaultEnv           = "HYPERLIQUID_TESTNET_VAULT"
	HyperliquidTestnetMaxNotionalUSDCEnv = "HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC"
	HyperliquidTestnetSpotSymbolEnv      = "HYPERLIQUID_TESTNET_SPOT_SYMBOL"
	HyperliquidTestnetPerpSymbolEnv      = "HYPERLIQUID_TESTNET_PERP_SYMBOL"
	HyperliquidTestnetHIP3SymbolEnv      = "HYPERLIQUID_TESTNET_HIP3_SYMBOL"

	HyperliquidTestnetDefaultMaxNotionalUSDC = "100"

	LighterTestnetPrivateKeyEnv      = "LIGHTER_TESTNET_PRIVATE_KEY"
	LighterTestnetAccountIndexEnv    = "LIGHTER_TESTNET_ACCOUNT_INDEX"
	LighterTestnetAPIKeyIndexEnv     = "LIGHTER_TESTNET_API_KEY_INDEX"
	LighterTestnetEnableWriteEnv     = "BOLTER_ENABLE_LIGHTER_TESTNET_WRITES"
	LighterTestnetMaxNotionalUSDCEnv = "LIGHTER_TESTNET_MAX_NOTIONAL_USDC"
	LighterTestnetSpotSymbolEnv      = "LIGHTER_TESTNET_SPOT_SYMBOL"
	LighterTestnetPerpSymbolEnv      = "LIGHTER_TESTNET_PERP_SYMBOL"

	LighterTestnetDefaultMaxNotionalUSDC = "100"
	LighterTestnetDefaultSpotSymbol      = "ETH-USDC"
	LighterTestnetDefaultPerpSymbol      = "ETH"

	AsterTestnetUserAddressEnv           = "ASTER_TESTNET_USER_ADDRESS"
	AsterTestnetSignerPrivateKeyEnv      = "ASTER_TESTNET_SIGNER_PRIVATE_KEY"
	AsterTestnetExpectedSignerAddressEnv = "ASTER_TESTNET_EXPECTED_SIGNER_ADDRESS"
	AsterTestnetEnableWriteEnv           = "BOLTER_ENABLE_ASTER_TESTNET_WRITES"
	AsterTestnetMaxNotionalUSDTEnv       = "ASTER_TESTNET_MAX_NOTIONAL_USDT"
	AsterTestnetSpotSymbolEnv            = "ASTER_TESTNET_SPOT_SYMBOL"
	AsterTestnetPerpSymbolEnv            = "ASTER_TESTNET_PERP_SYMBOL"
	AsterTestnetSpotRESTURLEnv           = "ASTER_TESTNET_SPOT_REST_URL"
	AsterTestnetSpotPublicWSURLEnv       = "ASTER_TESTNET_SPOT_WS_URL"
	AsterTestnetSpotUserWSURLEnv         = "ASTER_TESTNET_SPOT_USER_WS_URL"
	AsterTestnetPerpRESTURLEnv           = "ASTER_TESTNET_PERP_REST_URL"
	AsterTestnetPerpPublicWSURLEnv       = "ASTER_TESTNET_PERP_WS_URL"
	AsterTestnetPerpUserWSURLEnv         = "ASTER_TESTNET_PERP_USER_WS_URL"

	AsterTestnetDefaultMaxNotionalUSDT = "100"
	AsterTestnetDefaultSpotSymbol      = "BTCUSDT"
	AsterTestnetDefaultPerpSymbol      = "BTCUSDT"

	NadoTestnetPrivateKeyEnv         = "NADO_TESTNET_PRIVATE_KEY"
	NadoTestnetSubaccountNameEnv     = "NADO_TESTNET_SUBACCOUNT_NAME"
	NadoTestnetEnableWriteEnv        = "BOLTER_ENABLE_NADO_TESTNET_WRITES"
	NadoTestnetMaxNotionalUSDT0Env   = "NADO_TESTNET_MAX_NOTIONAL_USDT0"
	NadoTestnetSpotSymbolEnv         = "NADO_TESTNET_SPOT_SYMBOL"
	NadoTestnetPerpSymbolEnv         = "NADO_TESTNET_PERP_SYMBOL"
	NadoTestnetGatewayV1URLEnv       = "NADO_TESTNET_GATEWAY_URL"
	NadoTestnetGatewayV2URLEnv       = "NADO_TESTNET_GATEWAY_V2_URL"
	NadoTestnetArchiveV1URLEnv       = "NADO_TESTNET_ARCHIVE_URL"
	NadoTestnetArchiveV2URLEnv       = "NADO_TESTNET_ARCHIVE_V2_URL"
	NadoTestnetGatewayWSURLEnv       = "NADO_TESTNET_GATEWAY_WS_URL"
	NadoTestnetSubscriptionsWSURLEnv = "NADO_TESTNET_WS_URL"
	NadoTestnetTriggerURLEnv         = "NADO_TESTNET_TRIGGER_URL"

	NadoTestnetDefaultSubaccount       = "default"
	NadoTestnetDefaultMaxNotionalUSDT0 = "100"
	NadoTestnetDefaultSpotSymbol       = "USDC-USDT0"
	NadoTestnetDefaultPerpSymbol       = "ETH-PERP-USDT0"
)

type OKXDemoConfig struct {
	APIKey          string
	APISecret       string
	Passphrase      string
	MaxNotionalUSDT decimal.Decimal
	SpotSymbol      string
	PerpSymbol      string
	HostProfile     string
	RESTBaseURL     string
	WSBaseURL       string
	ProxyURL        string
}

type BybitEndpointProfile struct {
	RESTBaseURL       string
	PublicSpotWSURL   string
	PublicLinearWSURL string
	PrivateWSURL      string
	TradeWSURL        string
	SupportsWSTrade   bool
}

type BybitDemoConfig struct {
	APIKey          string
	APISecret       string
	MaxNotionalUSDT decimal.Decimal
	MaxNotionalUSDC decimal.Decimal
	SpotSymbol      string
	USDTPerpSymbol  string
	USDCPerpSymbol  string
	Profile         BybitEndpointProfile
	ProxyURL        string
}

type BybitTestnetConfig struct {
	APIKey          string
	APISecret       string
	MaxNotionalUSDT decimal.Decimal
	MaxNotionalUSDC decimal.Decimal
	SpotSymbol      string
	USDTPerpSymbol  string
	USDCPerpSymbol  string
	Profile         BybitEndpointProfile
	ProxyURL        string
}

type BitgetEndpointProfile struct {
	RESTBaseURL     string
	PublicWSURL     string
	PrivateWSURL    string
	PAPTrading      bool
	OfficialTestnet bool
}

type BitgetDemoConfig struct {
	APIKey          string
	APISecret       string
	Passphrase      string
	MaxNotionalUSDT decimal.Decimal
	MaxNotionalUSDC decimal.Decimal
	SpotSymbol      string
	USDTPerpSymbol  string
	USDCPerpSymbol  string
	Profile         BitgetEndpointProfile
	ProxyURL        string
}

// Deprecated: use BitgetDemoConfig.
type BitgetTestnetConfig = BitgetDemoConfig

type GateEndpointProfile struct {
	RESTBaseURL      string
	SpotWSURL        string
	FuturesUSDTWSURL string
	OfficialTestnet  bool
}

type GateTestnetConfig struct {
	APIKey          string
	APISecret       string
	MaxNotionalUSDT decimal.Decimal
	SpotSymbol      string
	USDTPerpSymbol  string
	Profile         GateEndpointProfile
	ProxyURL        string
}

type BlockedReleaseError struct {
	Venue       string
	Environment string
	Product     string
	Capability  string
	Evidence    string
}

type HyperliquidTestnetConfig struct {
	PrivateKey      string
	AccountAddress  string
	VaultAddress    string
	MaxNotionalUSDC decimal.Decimal
	SpotSymbol      string
	PerpSymbol      string
	HIP3Symbol      string
	ProxyURL        string
}

type LighterTestnetConfig struct {
	PrivateKey      string
	AccountIndex    int64
	APIKeyIndex     uint8
	MaxNotionalUSDC decimal.Decimal
	SpotSymbol      string
	PerpSymbol      string
	ProxyURL        string
}

type AsterTestnetConfig struct {
	UserAddress           string
	SignerPrivateKey      string
	ExpectedSignerAddress string
	MaxNotionalUSDT       decimal.Decimal
	SpotSymbol            string
	PerpSymbol            string
	SpotProfile           AsterEndpointProfile
	PerpProfile           AsterEndpointProfile
	ProxyURL              string
}

type AsterEndpointProfile struct {
	RESTURL     string
	PublicWSURL string
	UserWSURL   string
	ChainID     int64
}

type NadoTestnetConfig struct {
	PrivateKey       string
	Subaccount       string
	MaxNotionalUSDT0 decimal.Decimal
	SpotSymbol       string
	PerpSymbol       string
	Profile          NadoEndpointProfile
	ProxyURL         string
}

type NadoEndpointProfile struct {
	GatewayV1URL       string
	GatewayV2URL       string
	ArchiveV1URL       string
	ArchiveV2URL       string
	GatewayWSURL       string
	SubscriptionsWSURL string
	TriggerURL         string
	ChainID            int64
}

func (c HyperliquidTestnetConfig) String() string {
	return fmt.Sprintf(
		"HyperliquidTestnetConfig{PrivateKey:%s AccountAddress:%q VaultAddress:%q MaxNotionalUSDC:%s SpotSymbol:%q PerpSymbol:%q HIP3Symbol:%q ProxyURL:%q}",
		redactSecret(c.PrivateKey),
		c.AccountAddress,
		redactSecret(c.VaultAddress),
		c.MaxNotionalUSDC.String(),
		c.SpotSymbol,
		c.PerpSymbol,
		c.HIP3Symbol,
		redactURL(c.ProxyURL),
	)
}

func (c LighterTestnetConfig) String() string {
	return fmt.Sprintf(
		"LighterTestnetConfig{PrivateKey:%s AccountIndex:%d APIKeyIndex:%d MaxNotionalUSDC:%s SpotSymbol:%q PerpSymbol:%q ProxyURL:%q}",
		redactSecret(c.PrivateKey),
		c.AccountIndex,
		c.APIKeyIndex,
		c.MaxNotionalUSDC.String(),
		c.SpotSymbol,
		c.PerpSymbol,
		redactURL(c.ProxyURL),
	)
}

func (c AsterTestnetConfig) String() string {
	return fmt.Sprintf(
		"AsterTestnetConfig{UserAddress:%q SignerPrivateKey:%s ExpectedSignerAddress:%q MaxNotionalUSDT:%s SpotSymbol:%q PerpSymbol:%q SpotProfile:%+v PerpProfile:%+v ProxyURL:%q}",
		c.UserAddress,
		redactSecret(c.SignerPrivateKey),
		c.ExpectedSignerAddress,
		c.MaxNotionalUSDT.String(),
		c.SpotSymbol,
		c.PerpSymbol,
		redactAsterEndpointProfile(c.SpotProfile),
		redactAsterEndpointProfile(c.PerpProfile),
		redactURL(c.ProxyURL),
	)
}

func (c AsterTestnetConfig) GoString() string { return c.String() }

func (c NadoTestnetConfig) String() string {
	return fmt.Sprintf(
		"NadoTestnetConfig{PrivateKey:%s Subaccount:%q MaxNotionalUSDT0:%s SpotSymbol:%q PerpSymbol:%q Profile:%+v ProxyURL:%q}",
		redactSecret(c.PrivateKey),
		c.Subaccount,
		c.MaxNotionalUSDT0.String(),
		c.SpotSymbol,
		c.PerpSymbol,
		redactNadoEndpointProfile(c.Profile),
		redactURL(c.ProxyURL),
	)
}

func (c NadoTestnetConfig) GoString() string { return c.String() }

func (c LighterTestnetConfig) GoString() string {
	return c.String()
}

func (c HyperliquidTestnetConfig) GoString() string {
	return c.String()
}

func (c OKXDemoConfig) String() string {
	return fmt.Sprintf(
		"OKXDemoConfig{APIKey:%s APISecret:%s Passphrase:%s MaxNotionalUSDT:%s SpotSymbol:%q PerpSymbol:%q HostProfile:%q RESTBaseURL:%q WSBaseURL:%q ProxyURL:%q}",
		redactSecret(c.APIKey),
		redactSecret(c.APISecret),
		redactSecret(c.Passphrase),
		c.MaxNotionalUSDT.String(),
		c.SpotSymbol,
		c.PerpSymbol,
		c.HostProfile,
		redactURL(c.RESTBaseURL),
		redactURL(c.WSBaseURL),
		redactURL(c.ProxyURL),
	)
}

func (c OKXDemoConfig) GoString() string {
	return c.String()
}

func (c BybitDemoConfig) String() string {
	return fmt.Sprintf(
		"BybitDemoConfig{APIKey:%s APISecret:%s MaxNotionalUSDT:%s MaxNotionalUSDC:%s SpotSymbol:%q USDTPerpSymbol:%q USDCPerpSymbol:%q Profile:%+v ProxyURL:%q}",
		redactSecret(c.APIKey),
		redactSecret(c.APISecret),
		c.MaxNotionalUSDT.String(),
		c.MaxNotionalUSDC.String(),
		c.SpotSymbol,
		c.USDTPerpSymbol,
		c.USDCPerpSymbol,
		redactBybitEndpointProfile(c.Profile),
		redactURL(c.ProxyURL),
	)
}

func (c BybitDemoConfig) GoString() string {
	return c.String()
}

func (c BybitTestnetConfig) String() string {
	return fmt.Sprintf(
		"BybitTestnetConfig{APIKey:%s APISecret:%s MaxNotionalUSDT:%s MaxNotionalUSDC:%s SpotSymbol:%q USDTPerpSymbol:%q USDCPerpSymbol:%q Profile:%+v ProxyURL:%q}",
		redactSecret(c.APIKey),
		redactSecret(c.APISecret),
		c.MaxNotionalUSDT,
		c.MaxNotionalUSDC,
		c.SpotSymbol,
		c.USDTPerpSymbol,
		c.USDCPerpSymbol,
		redactBybitEndpointProfile(c.Profile),
		redactURL(c.ProxyURL),
	)
}

func (c BybitTestnetConfig) GoString() string {
	return c.String()
}

func (c BitgetDemoConfig) String() string {
	return fmt.Sprintf(
		"BitgetDemoConfig{APIKey:%s APISecret:%s Passphrase:%s MaxNotionalUSDT:%s MaxNotionalUSDC:%s SpotSymbol:%q USDTPerpSymbol:%q USDCPerpSymbol:%q Profile:%+v ProxyURL:%q}",
		redactSecret(c.APIKey),
		redactSecret(c.APISecret),
		redactSecret(c.Passphrase),
		c.MaxNotionalUSDT.String(),
		c.MaxNotionalUSDC.String(),
		c.SpotSymbol,
		c.USDTPerpSymbol,
		c.USDCPerpSymbol,
		redactBitgetEndpointProfile(c.Profile),
		redactURL(c.ProxyURL),
	)
}

func (c BitgetDemoConfig) GoString() string {
	return c.String()
}

func (c GateTestnetConfig) String() string {
	return fmt.Sprintf(
		"GateTestnetConfig{APIKey:%s APISecret:%s MaxNotionalUSDT:%s SpotSymbol:%q USDTPerpSymbol:%q Profile:%+v ProxyURL:%q}",
		redactSecret(c.APIKey),
		redactSecret(c.APISecret),
		c.MaxNotionalUSDT.String(),
		c.SpotSymbol,
		c.USDTPerpSymbol,
		redactGateEndpointProfile(c.Profile),
		redactURL(c.ProxyURL),
	)
}

func (c GateTestnetConfig) GoString() string {
	return c.String()
}

func redactBybitEndpointProfile(profile BybitEndpointProfile) BybitEndpointProfile {
	profile.RESTBaseURL = redactURL(profile.RESTBaseURL)
	profile.PublicSpotWSURL = redactURL(profile.PublicSpotWSURL)
	profile.PublicLinearWSURL = redactURL(profile.PublicLinearWSURL)
	profile.PrivateWSURL = redactURL(profile.PrivateWSURL)
	profile.TradeWSURL = redactURL(profile.TradeWSURL)
	return profile
}

func redactBitgetEndpointProfile(profile BitgetEndpointProfile) BitgetEndpointProfile {
	profile.RESTBaseURL = redactURL(profile.RESTBaseURL)
	profile.PublicWSURL = redactURL(profile.PublicWSURL)
	profile.PrivateWSURL = redactURL(profile.PrivateWSURL)
	return profile
}

func redactGateEndpointProfile(profile GateEndpointProfile) GateEndpointProfile {
	profile.RESTBaseURL = redactURL(profile.RESTBaseURL)
	profile.SpotWSURL = redactURL(profile.SpotWSURL)
	profile.FuturesUSDTWSURL = redactURL(profile.FuturesUSDTWSURL)
	return profile
}

func redactAsterEndpointProfile(profile AsterEndpointProfile) AsterEndpointProfile {
	profile.RESTURL = redactURL(profile.RESTURL)
	profile.PublicWSURL = redactURL(profile.PublicWSURL)
	profile.UserWSURL = redactURL(profile.UserWSURL)
	return profile
}

func redactNadoEndpointProfile(profile NadoEndpointProfile) NadoEndpointProfile {
	profile.GatewayV1URL = redactURL(profile.GatewayV1URL)
	profile.GatewayV2URL = redactURL(profile.GatewayV2URL)
	profile.ArchiveV1URL = redactURL(profile.ArchiveV1URL)
	profile.ArchiveV2URL = redactURL(profile.ArchiveV2URL)
	profile.GatewayWSURL = redactURL(profile.GatewayWSURL)
	profile.SubscriptionsWSURL = redactURL(profile.SubscriptionsWSURL)
	profile.TriggerURL = redactURL(profile.TriggerURL)
	return profile
}

func (e *BlockedReleaseError) Error() string {
	return fmt.Sprintf(
		"blocked-release: venue=%s environment=%s product=%s capability=%s evidence=%s",
		e.Venue,
		e.Environment,
		e.Product,
		e.Capability,
		e.Evidence,
	)
}

func IsBlockedRelease(err error) bool {
	var blocked *BlockedReleaseError
	return errors.As(err, &blocked)
}

// LoadRepoEnv loads the repo-root .env into the current process without
// overriding shell-exported environment variables. Execution gates are never
// imported from the file: live reads, writes, unsafe overrides, and extended
// test modes must be enabled explicitly in the process environment.
func LoadRepoEnv() error {
	root, err := findRepoRoot()
	if err != nil {
		return err
	}

	values, err := godotenv.Read(filepath.Join(root, ".env"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for key, value := range values {
		if isRepoEnvExecutionGate(key) {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}

	applyLegacyAliases()

	return nil
}

func isRepoEnvExecutionGate(key string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	if strings.HasPrefix(key, "RUN_") || strings.HasSuffix(key, "_REALTIME_WS") {
		return true
	}
	return strings.HasPrefix(key, "ENABLE_") || strings.Contains(key, "_ENABLE_") ||
		strings.HasPrefix(key, "ALLOW_") || strings.Contains(key, "_ALLOW_")
}

func RequireEnv(t testing.TB, vars ...string) {
	t.Helper()

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}

	var missing []string
	for _, key := range vars {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Skipf("skipping: missing required env %s", strings.Join(missing, ", "))
	}
}

func RequireLiveCredentials(t testing.TB, vars ...string) {
	t.Helper()

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	RequireEnv(t, vars...)
}

func RequireLiveRead(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: live read test excluded by -short")
	}
	if os.Getenv("BOLTER_ENABLE_LIVE_READ_TESTS") != "1" {
		t.Skip("skipping live read test: set BOLTER_ENABLE_LIVE_READ_TESTS=1 to enable real exchange read execution")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	RequireEnv(t, vars...)
}

func RequireLiveWrite(t testing.TB, enableVar string, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: live write test excluded by -short")
	}
	if os.Getenv(enableVar) != "1" {
		t.Skipf("skipping live write test: set %s=1 to enable real exchange write execution", enableVar)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(vars...); len(missing) > 0 {
		t.Fatalf("live write gate %s enabled but required env is missing: %s", enableVar, strings.Join(missing, ", "))
	}
}

func RequireBinanceDemoWrite(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Binance Demo write test excluded by -short")
	}
	if os.Getenv(BinanceDemoEnableWriteEnv) != "1" {
		t.Skipf("skipping Binance Demo write test: set %s=1 to enable real Demo writes", BinanceDemoEnableWriteEnv)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(BinanceDemoAPIKeyEnv, BinanceDemoAPISecretEnv); len(missing) > 0 {
		t.Fatalf("Binance Demo write gate enabled but required env is missing: %s", strings.Join(missing, ", "))
	}
}

func RequireBinanceDemoRead(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Binance Demo read test excluded by -short")
	}
	RequireLiveRead(t, BinanceDemoAPIKeyEnv, BinanceDemoAPISecretEnv)
}

func RequireOKXDemoRead(t testing.TB) OKXDemoConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: OKX Demo read test excluded by -short")
	}
	RequireLiveRead(t, OKXDemoAPIKeyEnv, OKXDemoAPISecretEnv, OKXDemoAPIPassphraseEnv)
	cfg, err := OKXDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("OKX Demo read env: %v", err)
	}
	return cfg
}

func RequireOKXDemoWrite(t testing.TB) OKXDemoConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: OKX Demo write test excluded by -short")
	}
	if os.Getenv(OKXDemoEnableWriteEnv) != "1" {
		t.Skipf("skipping OKX Demo write test: set %s=1 to enable real Demo writes", OKXDemoEnableWriteEnv)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(OKXDemoAPIKeyEnv, OKXDemoAPISecretEnv, OKXDemoAPIPassphraseEnv); len(missing) > 0 {
		t.Fatalf("OKX Demo write gate enabled but required env is missing: %s", strings.Join(missing, ", "))
	}
	cfg, err := OKXDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("OKX Demo env: %v", err)
	}
	if err := validateOKXDemoWriteProfile(cfg); err != nil {
		t.Fatalf("OKX Demo write safety: %v", err)
	}
	return cfg
}

func RequireBybitDemoRead(t testing.TB) BybitDemoConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Bybit Demo read test excluded by -short")
	}
	RequireLiveRead(t, BybitDemoAPIKeyEnv, BybitDemoAPISecretEnv)
	cfg, err := BybitDemoConfigFromEnv()
	if err != nil {
		t.Skipf("skipping Bybit Demo read test: %v", err)
	}
	return cfg
}

func RequireBybitDemoWrite(t testing.TB) BybitDemoConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Bybit Demo write test excluded by -short")
	}
	if os.Getenv(BybitDemoEnableWriteEnv) != "1" {
		t.Skipf("skipping Bybit Demo write test: set %s=1 to enable real Demo writes", BybitDemoEnableWriteEnv)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	cfg, err := BybitDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("Bybit Demo write gate enabled but environment is invalid: %v", err)
	}
	return cfg
}

func RequireBybitTestnetWrite(t testing.TB) BybitTestnetConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Bybit Testnet write test excluded by -short")
	}
	if os.Getenv(BybitTestnetEnableWriteEnv) != "1" {
		t.Skipf("skipping Bybit Testnet write test: set %s=1 to enable real Testnet writes", BybitTestnetEnableWriteEnv)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	cfg, err := BybitTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("Bybit Testnet write gate enabled but environment is invalid: %v", err)
	}
	if !cfg.Profile.SupportsWSTrade || cfg.Profile.TradeWSURL == "" {
		t.Fatalf("Bybit Testnet write profile must support WS Trade: %+v", cfg.Profile)
	}
	return cfg
}

func RequireBitgetDemoRead(t testing.TB) BitgetDemoConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Bitget Demo read test excluded by -short")
	}
	RequireLiveRead(t, BitgetDemoAPIKeyEnv, BitgetDemoAPISecretEnv, BitgetDemoPassphraseEnv)
	cfg, err := BitgetDemoConfigFromEnv()
	if err != nil {
		if IsBlockedRelease(err) {
			t.Skip(err.Error())
		}
		t.Fatalf("Bitget Demo read env: %v", err)
	}
	return cfg
}

func RequireBitgetDemoWrite(t testing.TB) BitgetDemoConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Bitget Demo write test excluded by -short")
	}
	if os.Getenv(BitgetDemoEnableWriteEnv) != "1" {
		t.Skipf("skipping Bitget Demo write test: set %s=1 to enable real Demo writes", BitgetDemoEnableWriteEnv)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(BitgetDemoAPIKeyEnv, BitgetDemoAPISecretEnv, BitgetDemoPassphraseEnv); len(missing) > 0 {
		t.Fatalf("Bitget Demo write gate enabled but required env is missing: %s", strings.Join(missing, ", "))
	}
	cfg, err := BitgetDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("Bitget Demo env: %v", err)
	}
	if err := validateBitgetDemoWriteProfile(cfg.Profile); err != nil {
		t.Fatalf("Bitget Demo write safety: %v", err)
	}
	return cfg
}

// Deprecated: use RequireBitgetDemoWrite.
func RequireBitgetTestnetWrite(t testing.TB) BitgetTestnetConfig {
	t.Helper()
	return RequireBitgetDemoWrite(t)
}

func RequireGateTestnetWrite(t testing.TB) GateTestnetConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Gate Testnet write test excluded by -short")
	}
	if os.Getenv(GateTestnetEnableWriteEnv) != "1" {
		t.Skipf("skipping Gate Testnet write test: set %s=1 to enable real testnet writes", GateTestnetEnableWriteEnv)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(GateTestnetAPIKeyEnv, GateTestnetAPISecretEnv); len(missing) > 0 {
		t.Fatalf("Gate Testnet write gate enabled but required env is missing: %s", strings.Join(missing, ", "))
	}
	cfg, err := GateTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("Gate Testnet env: %v", err)
	}
	if err := validateGateTestnetWriteProfile(cfg.Profile); err != nil {
		t.Fatalf("Gate Testnet write safety: %v", err)
	}
	return cfg
}

func RequireGateTestnetRead(t testing.TB) GateTestnetConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Gate Testnet read test excluded by -short")
	}
	if os.Getenv("BOLTER_ENABLE_LIVE_READ_TESTS") != "1" {
		t.Skip("skipping Gate Testnet read test: set BOLTER_ENABLE_LIVE_READ_TESTS=1 to enable real testnet reads")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(GateTestnetAPIKeyEnv, GateTestnetAPISecretEnv); len(missing) > 0 {
		t.Skipf("skipping Gate Testnet read test: missing required env %s", strings.Join(missing, ", "))
	}
	cfg, err := GateTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("Gate Testnet read env: %v", err)
	}
	return cfg
}

func RequireHyperliquidTestnetWrite(t testing.TB) HyperliquidTestnetConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Hyperliquid Testnet write test excluded by -short")
	}
	if os.Getenv(HyperliquidTestnetEnableWriteEnv) != "1" {
		t.Skipf("skipping Hyperliquid Testnet write test: set %s=1 to enable real testnet writes", HyperliquidTestnetEnableWriteEnv)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(HyperliquidTestnetPrivateKeyEnv); len(missing) > 0 {
		t.Fatalf("Hyperliquid Testnet write gate enabled but required env is missing: %s", strings.Join(missing, ", "))
	}
	cfg, err := HyperliquidTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("Hyperliquid Testnet env: %v", err)
	}
	return cfg
}

func RequireHyperliquidTestnetRead(t testing.TB) HyperliquidTestnetConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Hyperliquid Testnet read test excluded by -short")
	}
	if os.Getenv("BOLTER_ENABLE_LIVE_READ_TESTS") != "1" {
		t.Skip("skipping Hyperliquid Testnet read test: set BOLTER_ENABLE_LIVE_READ_TESTS=1 to enable real testnet reads")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	cfg, err := HyperliquidTestnetReadConfigFromEnv()
	if err != nil {
		t.Fatalf("Hyperliquid Testnet read env: %v", err)
	}
	return cfg
}

func RequireLighterTestnetRead(t testing.TB) LighterTestnetConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Lighter Testnet read test excluded by -short")
	}
	if os.Getenv("BOLTER_ENABLE_LIVE_READ_TESTS") != "1" {
		t.Skip("skipping Lighter Testnet read test: set BOLTER_ENABLE_LIVE_READ_TESTS=1 to enable real testnet reads")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	cfg, err := LighterTestnetReadConfigFromEnv()
	if err != nil {
		t.Fatalf("Lighter Testnet read env: %v", err)
	}
	return cfg
}

func RequireLighterTestnetWrite(t testing.TB) LighterTestnetConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Lighter Testnet write test excluded by -short")
	}
	if os.Getenv(LighterTestnetEnableWriteEnv) != "1" {
		t.Skipf("skipping Lighter Testnet write test: set %s=1 to enable real testnet writes", LighterTestnetEnableWriteEnv)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(LighterTestnetPrivateKeyEnv, LighterTestnetAccountIndexEnv, LighterTestnetAPIKeyIndexEnv); len(missing) > 0 {
		t.Fatalf("Lighter Testnet write gate enabled but required env is missing: %s", strings.Join(missing, ", "))
	}
	cfg, err := LighterTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("Lighter Testnet env: %v", err)
	}
	return cfg
}

func RequireAsterTestnetRead(t testing.TB) AsterTestnetConfig {
	t.Helper()
	RequireLiveRead(t, AsterTestnetUserAddressEnv, AsterTestnetSignerPrivateKeyEnv)
	cfg, err := AsterTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("Aster Testnet read env: %v", err)
	}
	return cfg
}

func RequireAsterTestnetPublicRead(t testing.TB) AsterTestnetConfig {
	t.Helper()
	RequireLiveRead(t)
	cfg, err := AsterTestnetReadConfigFromEnv()
	if err != nil {
		t.Fatalf("Aster Testnet public read env: %v", err)
	}
	return cfg
}

func RequireAsterTestnetWrite(t testing.TB) AsterTestnetConfig {
	t.Helper()
	RequireLiveWrite(t, AsterTestnetEnableWriteEnv, AsterTestnetUserAddressEnv, AsterTestnetSignerPrivateKeyEnv)
	cfg, err := AsterTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("Aster Testnet write env: %v", err)
	}
	return cfg
}

func RequireNadoTestnetRead(t testing.TB) NadoTestnetConfig {
	t.Helper()
	RequireLiveRead(t, NadoTestnetPrivateKeyEnv)
	cfg, err := NadoTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("Nado Testnet read env: %v", err)
	}
	return cfg
}

func RequireNadoTestnetPublicRead(t testing.TB) NadoTestnetConfig {
	t.Helper()
	RequireLiveRead(t)
	cfg, err := NadoTestnetReadConfigFromEnv()
	if err != nil {
		t.Fatalf("Nado Testnet public read env: %v", err)
	}
	return cfg
}

func RequireNadoTestnetWrite(t testing.TB) NadoTestnetConfig {
	t.Helper()
	RequireLiveWrite(t, NadoTestnetEnableWriteEnv, NadoTestnetPrivateKeyEnv)
	cfg, err := NadoTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("Nado Testnet write env: %v", err)
	}
	return cfg
}

func AsterTestnetConfigFromEnv() (AsterTestnetConfig, error) {
	return asterTestnetConfigFromEnv(true)
}

func AsterTestnetReadConfigFromEnv() (AsterTestnetConfig, error) {
	return asterTestnetConfigFromEnv(false)
}

func NadoTestnetConfigFromEnv() (NadoTestnetConfig, error) {
	return nadoTestnetConfigFromEnv(true)
}

func NadoTestnetReadConfigFromEnv() (NadoTestnetConfig, error) {
	return nadoTestnetConfigFromEnv(false)
}

func asterTestnetConfigFromEnv(requireCredentials bool) (AsterTestnetConfig, error) {
	if requireCredentials {
		if missing := missingEnv(AsterTestnetUserAddressEnv, AsterTestnetSignerPrivateKeyEnv); len(missing) > 0 {
			return AsterTestnetConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
		}
	}
	spotProfile := AsterEndpointProfile{
		RESTURL:     "https://sapi.asterdex-testnet.com",
		PublicWSURL: "wss://sstream.asterdex-testnet.com",
		UserWSURL:   "wss://sstream.asterdex-testnet.com",
		ChainID:     714,
	}
	perpProfile := AsterEndpointProfile{
		RESTURL:     "https://fapi.asterdex-testnet.com",
		PublicWSURL: "wss://fstream5.asterdex-testnet.com",
		UserWSURL:   "wss://fstream.asterdex-testnet.com",
		ChainID:     714,
	}
	for _, override := range []struct {
		env      string
		expected string
	}{
		{AsterTestnetSpotRESTURLEnv, spotProfile.RESTURL},
		{AsterTestnetSpotPublicWSURLEnv, spotProfile.PublicWSURL},
		{AsterTestnetSpotUserWSURLEnv, spotProfile.UserWSURL},
		{AsterTestnetPerpRESTURLEnv, perpProfile.RESTURL},
		{AsterTestnetPerpPublicWSURLEnv, perpProfile.PublicWSURL},
		{AsterTestnetPerpUserWSURLEnv, perpProfile.UserWSURL},
	} {
		if err := validateEndpointOverride(override.env, override.expected); err != nil {
			return AsterTestnetConfig{}, err
		}
	}
	maxNotional, err := parsePositiveDecimalEnv(AsterTestnetMaxNotionalUSDTEnv, AsterTestnetDefaultMaxNotionalUSDT)
	if err != nil {
		return AsterTestnetConfig{}, err
	}
	proxyURL, err := proxyURLFromEnv()
	if err != nil {
		return AsterTestnetConfig{}, err
	}
	return AsterTestnetConfig{
		UserAddress:           strings.TrimSpace(os.Getenv(AsterTestnetUserAddressEnv)),
		SignerPrivateKey:      strings.TrimSpace(os.Getenv(AsterTestnetSignerPrivateKeyEnv)),
		ExpectedSignerAddress: strings.TrimSpace(os.Getenv(AsterTestnetExpectedSignerAddressEnv)),
		MaxNotionalUSDT:       maxNotional,
		SpotSymbol:            envOrDefault(AsterTestnetSpotSymbolEnv, AsterTestnetDefaultSpotSymbol),
		PerpSymbol:            envOrDefault(AsterTestnetPerpSymbolEnv, AsterTestnetDefaultPerpSymbol),
		SpotProfile:           spotProfile,
		PerpProfile:           perpProfile,
		ProxyURL:              proxyURL,
	}, nil
}

func nadoTestnetConfigFromEnv(requireCredentials bool) (NadoTestnetConfig, error) {
	if requireCredentials {
		if missing := missingEnv(NadoTestnetPrivateKeyEnv); len(missing) > 0 {
			return NadoTestnetConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
		}
	}
	profile := NadoEndpointProfile{
		GatewayV1URL:       "https://gateway.test.nado.xyz/v1",
		GatewayV2URL:       "https://gateway.test.nado.xyz/v2",
		ArchiveV1URL:       "https://archive.test.nado.xyz/v1",
		ArchiveV2URL:       "https://archive.test.nado.xyz/v2",
		GatewayWSURL:       "wss://gateway.test.nado.xyz/v1/ws",
		SubscriptionsWSURL: "wss://gateway.test.nado.xyz/v1/subscribe",
		TriggerURL:         "https://trigger.test.nado.xyz/v1",
		ChainID:            763373,
	}
	for _, override := range []struct {
		env      string
		expected string
	}{
		{NadoTestnetGatewayV1URLEnv, profile.GatewayV1URL},
		{NadoTestnetGatewayV2URLEnv, profile.GatewayV2URL},
		{NadoTestnetArchiveV1URLEnv, profile.ArchiveV1URL},
		{NadoTestnetArchiveV2URLEnv, profile.ArchiveV2URL},
		{NadoTestnetGatewayWSURLEnv, profile.GatewayWSURL},
		{NadoTestnetSubscriptionsWSURLEnv, profile.SubscriptionsWSURL},
		{NadoTestnetTriggerURLEnv, profile.TriggerURL},
	} {
		if err := validateEndpointOverride(override.env, override.expected); err != nil {
			return NadoTestnetConfig{}, err
		}
	}
	maxNotional, err := parsePositiveDecimalEnv(NadoTestnetMaxNotionalUSDT0Env, NadoTestnetDefaultMaxNotionalUSDT0)
	if err != nil {
		return NadoTestnetConfig{}, err
	}
	proxyURL, err := proxyURLFromEnv()
	if err != nil {
		return NadoTestnetConfig{}, err
	}
	return NadoTestnetConfig{
		PrivateKey:       strings.TrimSpace(os.Getenv(NadoTestnetPrivateKeyEnv)),
		Subaccount:       envOrDefault(NadoTestnetSubaccountNameEnv, NadoTestnetDefaultSubaccount),
		MaxNotionalUSDT0: maxNotional,
		SpotSymbol:       envOrDefault(NadoTestnetSpotSymbolEnv, NadoTestnetDefaultSpotSymbol),
		PerpSymbol:       envOrDefault(NadoTestnetPerpSymbolEnv, NadoTestnetDefaultPerpSymbol),
		Profile:          profile,
		ProxyURL:         proxyURL,
	}, nil
}

func validateEndpointOverride(env, expected string) error {
	rawURL := strings.TrimSpace(os.Getenv(env))
	if rawURL == "" {
		return nil
	}
	if rawURL != expected {
		return fmt.Errorf("%s is not an official Testnet endpoint", env)
	}
	return nil
}

func HyperliquidTestnetConfigFromEnv() (HyperliquidTestnetConfig, error) {
	return hyperliquidTestnetConfigFromEnv(true)
}

func LighterTestnetConfigFromEnv() (LighterTestnetConfig, error) {
	return lighterTestnetConfigFromEnv(true)
}

func LighterTestnetReadConfigFromEnv() (LighterTestnetConfig, error) {
	return lighterTestnetConfigFromEnv(false)
}

func BybitDemoConfigFromEnv() (BybitDemoConfig, error) {
	missing := missingEnv(BybitDemoAPIKeyEnv, BybitDemoAPISecretEnv)
	if len(missing) > 0 {
		if hasAnyEnv(BybitTestnetAPIKeyEnv, BybitTestnetAPISecretEnv) {
			return BybitDemoConfig{}, fmt.Errorf("missing required env %s; BYBIT_TESTNET_* credentials are a separate Bybit Testnet scope and are not accepted for Bybit Demo Trading, use %s and %s", strings.Join(missing, ", "), BybitDemoAPIKeyEnv, BybitDemoAPISecretEnv)
		}
		return BybitDemoConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
	}
	maxUSDT, err := parsePositiveDecimalEnv(BybitDemoMaxNotionalUSDTEnv, BybitDefaultMaxNotionalUSDT)
	if err != nil {
		return BybitDemoConfig{}, err
	}
	maxUSDC, err := parsePositiveDecimalEnv(BybitDemoMaxNotionalUSDCEnv, BybitDefaultMaxNotionalUSDC)
	if err != nil {
		return BybitDemoConfig{}, err
	}
	proxyURL, err := proxyURLFromEnv()
	if err != nil {
		return BybitDemoConfig{}, err
	}
	return BybitDemoConfig{
		APIKey:          os.Getenv(BybitDemoAPIKeyEnv),
		APISecret:       os.Getenv(BybitDemoAPISecretEnv),
		MaxNotionalUSDT: maxUSDT,
		MaxNotionalUSDC: maxUSDC,
		SpotSymbol:      envOrDefault(BybitDemoSpotSymbolEnv, BybitDefaultSpotSymbol),
		USDTPerpSymbol:  envOrDefault(BybitDemoUSDTPerpSymbolEnv, BybitDefaultUSDTPerpSymbol),
		USDCPerpSymbol:  envOrDefault(BybitDemoUSDCPerpSymbolEnv, BybitDefaultUSDCPerpSymbol),
		Profile: BybitEndpointProfile{
			RESTBaseURL:       "https://api-demo.bybit.com",
			PublicSpotWSURL:   "wss://stream.bybit.com/v5/public/spot",
			PublicLinearWSURL: "wss://stream.bybit.com/v5/public/linear",
			PrivateWSURL:      "wss://stream-demo.bybit.com/v5/private",
			SupportsWSTrade:   false,
		},
		ProxyURL: proxyURL,
	}, nil
}

func BybitTestnetConfigFromEnv() (BybitTestnetConfig, error) {
	missing := missingEnv(BybitTestnetAPIKeyEnv, BybitTestnetAPISecretEnv)
	if len(missing) > 0 {
		if hasAnyEnv(BybitDemoAPIKeyEnv, BybitDemoAPISecretEnv) {
			return BybitTestnetConfig{}, fmt.Errorf("missing required env %s; BYBIT_DEMO_* credentials are a separate Bybit Demo Trading scope and are not accepted for Bybit Testnet, use %s and %s", strings.Join(missing, ", "), BybitTestnetAPIKeyEnv, BybitTestnetAPISecretEnv)
		}
		return BybitTestnetConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
	}
	maxUSDT, err := parsePositiveDecimalEnv(BybitTestnetMaxNotionalUSDTEnv, BybitDefaultMaxNotionalUSDT)
	if err != nil {
		return BybitTestnetConfig{}, err
	}
	maxUSDC, err := parsePositiveDecimalEnv(BybitTestnetMaxNotionalUSDCEnv, BybitDefaultMaxNotionalUSDC)
	if err != nil {
		return BybitTestnetConfig{}, err
	}
	proxyURL, err := proxyURLFromEnv()
	if err != nil {
		return BybitTestnetConfig{}, err
	}
	return BybitTestnetConfig{
		APIKey:          os.Getenv(BybitTestnetAPIKeyEnv),
		APISecret:       os.Getenv(BybitTestnetAPISecretEnv),
		MaxNotionalUSDT: maxUSDT,
		MaxNotionalUSDC: maxUSDC,
		SpotSymbol:      envOrDefault(BybitTestnetSpotSymbolEnv, BybitDefaultSpotSymbol),
		USDTPerpSymbol:  envOrDefault(BybitTestnetUSDTPerpSymbolEnv, BybitDefaultUSDTPerpSymbol),
		USDCPerpSymbol:  envOrDefault(BybitTestnetUSDCPerpSymbolEnv, BybitDefaultUSDCPerpSymbol),
		Profile: BybitEndpointProfile{
			RESTBaseURL:       "https://api-testnet.bybit.com",
			PublicSpotWSURL:   "wss://stream-testnet.bybit.com/v5/public/spot",
			PublicLinearWSURL: "wss://stream-testnet.bybit.com/v5/public/linear",
			PrivateWSURL:      "wss://stream-testnet.bybit.com/v5/private",
			TradeWSURL:        "wss://stream-testnet.bybit.com/v5/trade",
			SupportsWSTrade:   true,
		},
		ProxyURL: proxyURL,
	}, nil
}

func BitgetDemoConfigFromEnv() (BitgetDemoConfig, error) {
	applyLegacyAliases()
	missing := missingEnv(BitgetDemoAPIKeyEnv, BitgetDemoAPISecretEnv, BitgetDemoPassphraseEnv)
	if len(missing) > 0 {
		return BitgetDemoConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
	}
	restBaseURL := strings.TrimSpace(os.Getenv(BitgetDemoRESTBaseURLEnv))
	publicWSURL := strings.TrimSpace(os.Getenv(BitgetDemoPublicWSURLEnv))
	privateWSURL := strings.TrimSpace(os.Getenv(BitgetDemoPrivateWSURLEnv))
	if restBaseURL == "" && publicWSURL == "" && privateWSURL == "" {
		restBaseURL = "https://api.bitget.com"
		publicWSURL = "wss://wspap.bitget.com/v3/ws/public"
		privateWSURL = "wss://wspap.bitget.com/v3/ws/private"
	}
	if restBaseURL == "" || publicWSURL == "" || privateWSURL == "" {
		return BitgetDemoConfig{}, fmt.Errorf("%s, %s, and %s must be set together", BitgetDemoRESTBaseURLEnv, BitgetDemoPublicWSURLEnv, BitgetDemoPrivateWSURLEnv)
	}
	if err := validateURL(restBaseURL, BitgetDemoRESTBaseURLEnv, "http", "https"); err != nil {
		return BitgetDemoConfig{}, err
	}
	if err := validateBitgetDemoWSURL(publicWSURL, BitgetDemoPublicWSURLEnv); err != nil {
		return BitgetDemoConfig{}, err
	}
	if err := validateBitgetDemoWSURL(privateWSURL, BitgetDemoPrivateWSURLEnv); err != nil {
		return BitgetDemoConfig{}, err
	}
	maxUSDT, err := parsePositiveDecimalEnv(BitgetDemoMaxNotionalUSDTEnv, BitgetDefaultMaxNotionalUSDT)
	if err != nil {
		return BitgetDemoConfig{}, err
	}
	maxUSDC, err := parsePositiveDecimalEnv(BitgetDemoMaxNotionalUSDCEnv, BitgetDefaultMaxNotionalUSDC)
	if err != nil {
		return BitgetDemoConfig{}, err
	}
	proxyURL, err := proxyURLFromEnv()
	if err != nil {
		return BitgetDemoConfig{}, err
	}
	return BitgetDemoConfig{
		APIKey:          os.Getenv(BitgetDemoAPIKeyEnv),
		APISecret:       os.Getenv(BitgetDemoAPISecretEnv),
		Passphrase:      os.Getenv(BitgetDemoPassphraseEnv),
		MaxNotionalUSDT: maxUSDT,
		MaxNotionalUSDC: maxUSDC,
		SpotSymbol:      envOrDefault(BitgetDemoSpotSymbolEnv, BitgetDefaultSpotSymbol),
		USDTPerpSymbol:  envOrDefault(BitgetDemoUSDTPerpSymbolEnv, BitgetDefaultUSDTPerpSymbol),
		USDCPerpSymbol:  envOrDefault(BitgetDemoUSDCPerpSymbolEnv, BitgetDefaultUSDCPerpSymbol),
		Profile: BitgetEndpointProfile{
			RESTBaseURL:  restBaseURL,
			PublicWSURL:  publicWSURL,
			PrivateWSURL: privateWSURL,
			PAPTrading:   true,
		},
		ProxyURL: proxyURL,
	}, nil
}

// Deprecated: use BitgetDemoConfigFromEnv.
func BitgetTestnetConfigFromEnv() (BitgetTestnetConfig, error) {
	return BitgetDemoConfigFromEnv()
}

func GateTestnetConfigFromEnv() (GateTestnetConfig, error) {
	if missing := missingEnv(GateTestnetAPIKeyEnv, GateTestnetAPISecretEnv); len(missing) > 0 {
		return GateTestnetConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
	}
	restBaseURL := envOrDefault(GateTestnetRESTBaseURLEnv, "https://api-testnet.gateapi.io/api/v4")
	spotWSURL := envOrDefault(GateTestnetSpotWSURLEnv, "wss://ws-testnet.gate.com/v4/ws/spot")
	futuresUSDTWSURL := envOrDefaultAny([]string{GateTestnetUSDTFuturesWSURLEnv, GateTestnetFuturesUSDTWSURLEnv}, "wss://ws-testnet.gate.com/v4/ws/futures/usdt")
	if err := validateGateTestnetURL(restBaseURL, GateTestnetRESTBaseURLEnv, "http", "https"); err != nil {
		return GateTestnetConfig{}, err
	}
	if err := validateGateTestnetURL(spotWSURL, GateTestnetSpotWSURLEnv, "ws", "wss"); err != nil {
		return GateTestnetConfig{}, err
	}
	if err := validateGateTestnetURL(futuresUSDTWSURL, GateTestnetUSDTFuturesWSURLEnv, "ws", "wss"); err != nil {
		return GateTestnetConfig{}, err
	}
	maxUSDT, err := parsePositiveDecimalEnv(GateTestnetMaxNotionalUSDTEnv, GateTestnetDefaultMaxNotionalUSDT)
	if err != nil {
		return GateTestnetConfig{}, err
	}
	proxyURL, err := proxyURLFromEnv()
	if err != nil {
		return GateTestnetConfig{}, err
	}
	return GateTestnetConfig{
		APIKey:          os.Getenv(GateTestnetAPIKeyEnv),
		APISecret:       os.Getenv(GateTestnetAPISecretEnv),
		MaxNotionalUSDT: maxUSDT,
		SpotSymbol:      envOrDefault(GateTestnetSpotSymbolEnv, GateTestnetDefaultSpotSymbol),
		USDTPerpSymbol:  envOrDefault(GateTestnetUSDTPerpSymbolEnv, GateTestnetDefaultUSDTPerpSymbol),
		Profile: GateEndpointProfile{
			RESTBaseURL:      restBaseURL,
			SpotWSURL:        spotWSURL,
			FuturesUSDTWSURL: futuresUSDTWSURL,
			OfficialTestnet:  isKnownGateTestnetProfile(restBaseURL, spotWSURL, futuresUSDTWSURL),
		},
		ProxyURL: proxyURL,
	}, nil
}

func isKnownGateTestnetProfile(restBaseURL, spotWSURL, futuresUSDTWSURL string) bool {
	return strings.TrimRight(restBaseURL, "/") == "https://api-testnet.gateapi.io/api/v4" &&
		strings.TrimRight(spotWSURL, "/") == "wss://ws-testnet.gate.com/v4/ws/spot" &&
		strings.TrimRight(futuresUSDTWSURL, "/") == "wss://ws-testnet.gate.com/v4/ws/futures/usdt"
}

func validateGateTestnetWriteProfile(profile GateEndpointProfile) error {
	if !profile.OfficialTestnet || !isKnownGateTestnetProfile(profile.RESTBaseURL, profile.SpotWSURL, profile.FuturesUSDTWSURL) {
		return fmt.Errorf("Gate Testnet writes require the known official REST, Spot WS, and USDT Futures WS endpoints")
	}
	return nil
}

func lighterTestnetConfigFromEnv(requirePrivateKey bool) (LighterTestnetConfig, error) {
	required := []string{LighterTestnetAccountIndexEnv, LighterTestnetAPIKeyIndexEnv}
	if requirePrivateKey {
		required = append(required, LighterTestnetPrivateKeyEnv)
	}
	if missing := missingEnv(required...); len(missing) > 0 {
		return LighterTestnetConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
	}
	accountIndex, err := parseInt64Env(LighterTestnetAccountIndexEnv)
	if err != nil {
		return LighterTestnetConfig{}, err
	}
	apiKeyIndex64, err := parseUint8Env(LighterTestnetAPIKeyIndexEnv)
	if err != nil {
		return LighterTestnetConfig{}, err
	}
	maxNotional, err := decimal.NewFromString(envOrDefault(LighterTestnetMaxNotionalUSDCEnv, LighterTestnetDefaultMaxNotionalUSDC))
	if err != nil {
		return LighterTestnetConfig{}, fmt.Errorf("%s must be a decimal: %w", LighterTestnetMaxNotionalUSDCEnv, err)
	}
	if !maxNotional.IsPositive() {
		return LighterTestnetConfig{}, fmt.Errorf("%s must be positive", LighterTestnetMaxNotionalUSDCEnv)
	}
	proxyURL := strings.TrimSpace(os.Getenv("PROXY"))
	if proxyURL != "" {
		if err := validateURL(proxyURL, "PROXY", "http", "https", "socks5"); err != nil {
			return LighterTestnetConfig{}, err
		}
	}
	return LighterTestnetConfig{
		PrivateKey:      os.Getenv(LighterTestnetPrivateKeyEnv),
		AccountIndex:    accountIndex,
		APIKeyIndex:     apiKeyIndex64,
		MaxNotionalUSDC: maxNotional,
		SpotSymbol:      envOrDefault(LighterTestnetSpotSymbolEnv, LighterTestnetDefaultSpotSymbol),
		PerpSymbol:      envOrDefault(LighterTestnetPerpSymbolEnv, LighterTestnetDefaultPerpSymbol),
		ProxyURL:        proxyURL,
	}, nil
}

func HyperliquidTestnetReadConfigFromEnv() (HyperliquidTestnetConfig, error) {
	cfg, err := hyperliquidTestnetConfigFromEnv(false)
	if err != nil {
		return HyperliquidTestnetConfig{}, err
	}
	if strings.TrimSpace(cfg.PrivateKey) == "" && strings.TrimSpace(cfg.AccountAddress) == "" {
		return HyperliquidTestnetConfig{}, fmt.Errorf("missing Hyperliquid read identity: set %s or %s", HyperliquidTestnetPrivateKeyEnv, HyperliquidTestnetAccountAddressEnv)
	}
	return cfg, nil
}

func hyperliquidTestnetConfigFromEnv(requirePrivateKey bool) (HyperliquidTestnetConfig, error) {
	if requirePrivateKey {
		missing := missingEnv(HyperliquidTestnetPrivateKeyEnv)
		if len(missing) > 0 {
			return HyperliquidTestnetConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
		}
	}
	maxNotional, err := decimal.NewFromString(envOrDefault(HyperliquidTestnetMaxNotionalUSDCEnv, HyperliquidTestnetDefaultMaxNotionalUSDC))
	if err != nil {
		return HyperliquidTestnetConfig{}, fmt.Errorf("%s must be a decimal: %w", HyperliquidTestnetMaxNotionalUSDCEnv, err)
	}
	if !maxNotional.IsPositive() {
		return HyperliquidTestnetConfig{}, fmt.Errorf("%s must be positive", HyperliquidTestnetMaxNotionalUSDCEnv)
	}

	proxyURL := strings.TrimSpace(os.Getenv("PROXY"))
	if proxyURL != "" {
		if err := validateURL(proxyURL, "PROXY", "http", "https", "socks5"); err != nil {
			return HyperliquidTestnetConfig{}, err
		}
	}

	return HyperliquidTestnetConfig{
		PrivateKey:      os.Getenv(HyperliquidTestnetPrivateKeyEnv),
		AccountAddress:  strings.TrimSpace(os.Getenv(HyperliquidTestnetAccountAddressEnv)),
		VaultAddress:    strings.TrimSpace(os.Getenv(HyperliquidTestnetVaultEnv)),
		MaxNotionalUSDC: maxNotional,
		SpotSymbol:      strings.TrimSpace(os.Getenv(HyperliquidTestnetSpotSymbolEnv)),
		PerpSymbol:      strings.TrimSpace(os.Getenv(HyperliquidTestnetPerpSymbolEnv)),
		HIP3Symbol:      strings.TrimSpace(os.Getenv(HyperliquidTestnetHIP3SymbolEnv)),
		ProxyURL:        proxyURL,
	}, nil
}

func HyperliquidTestnetHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

func BinanceDemoHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

func LighterTestnetHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

func proxiedHTTPClient(timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	if proxy := strings.TrimSpace(os.Getenv("PROXY")); proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid PROXY configuration")
		}
		if err := validateURL(proxy, "PROXY", "http", "https", "socks5"); err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, nil
}

func OKXDemoConfigFromEnv() (OKXDemoConfig, error) {
	missing := missingEnv(OKXDemoAPIKeyEnv, OKXDemoAPISecretEnv, OKXDemoAPIPassphraseEnv)
	if len(missing) > 0 {
		return OKXDemoConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
	}

	maxNotional, err := decimal.NewFromString(envOrDefault(OKXDemoMaxNotionalUSDTEnv, OKXDemoDefaultMaxNotionalUSDT))
	if err != nil {
		return OKXDemoConfig{}, fmt.Errorf("%s must be a decimal: %w", OKXDemoMaxNotionalUSDTEnv, err)
	}
	if !maxNotional.IsPositive() {
		return OKXDemoConfig{}, fmt.Errorf("%s must be positive", OKXDemoMaxNotionalUSDTEnv)
	}

	hostProfile := strings.ToLower(strings.TrimSpace(envOrDefault(OKXDemoHostProfileEnv, OKXDemoHostProfileGlobal)))
	switch hostProfile {
	case OKXDemoHostProfileGlobal, OKXDemoHostProfileEEA, OKXDemoHostProfileCustom:
	default:
		return OKXDemoConfig{}, fmt.Errorf("%s must be one of %q, %q, or %q", OKXDemoHostProfileEnv, OKXDemoHostProfileGlobal, OKXDemoHostProfileEEA, OKXDemoHostProfileCustom)
	}

	restBaseURL := strings.TrimSpace(os.Getenv(OKXDemoRESTBaseURLEnv))
	wsBaseURL := strings.TrimSpace(os.Getenv(OKXDemoWSBaseURLEnv))
	if hostProfile == OKXDemoHostProfileCustom && (restBaseURL == "" || wsBaseURL == "") {
		return OKXDemoConfig{}, fmt.Errorf("%s=%s requires both %s and %s", OKXDemoHostProfileEnv, OKXDemoHostProfileCustom, OKXDemoRESTBaseURLEnv, OKXDemoWSBaseURLEnv)
	}
	if restBaseURL != "" {
		if err := validateURL(restBaseURL, OKXDemoRESTBaseURLEnv, "http", "https"); err != nil {
			return OKXDemoConfig{}, err
		}
	}
	if wsBaseURL != "" {
		if err := validateURL(wsBaseURL, OKXDemoWSBaseURLEnv, "ws", "wss"); err != nil {
			return OKXDemoConfig{}, err
		}
	}

	proxyURL := strings.TrimSpace(os.Getenv("PROXY"))
	if proxyURL != "" {
		if err := validateURL(proxyURL, "PROXY", "http", "https", "socks5"); err != nil {
			return OKXDemoConfig{}, err
		}
	}

	return OKXDemoConfig{
		APIKey:          os.Getenv(OKXDemoAPIKeyEnv),
		APISecret:       os.Getenv(OKXDemoAPISecretEnv),
		Passphrase:      os.Getenv(OKXDemoAPIPassphraseEnv),
		MaxNotionalUSDT: maxNotional,
		SpotSymbol:      envOrDefault(OKXDemoSpotSymbolEnv, OKXDemoDefaultSpotSymbol),
		PerpSymbol:      envOrDefault(OKXDemoPerpSymbolEnv, OKXDemoDefaultPerpSymbol),
		HostProfile:     hostProfile,
		RESTBaseURL:     restBaseURL,
		WSBaseURL:       wsBaseURL,
		ProxyURL:        proxyURL,
	}, nil
}

func validateOKXDemoWriteProfile(cfg OKXDemoConfig) error {
	profile := strings.ToLower(strings.TrimSpace(cfg.HostProfile))
	if profile == "" {
		profile = OKXDemoHostProfileGlobal
	}
	switch profile {
	case OKXDemoHostProfileGlobal, OKXDemoHostProfileEEA:
		if strings.TrimSpace(cfg.RESTBaseURL) != "" || strings.TrimSpace(cfg.WSBaseURL) != "" {
			return fmt.Errorf("official OKX Demo host profile %q does not permit endpoint overrides for credentialed writes", profile)
		}
		return nil
	case OKXDemoHostProfileCustom:
		if os.Getenv(OKXDemoAllowCustomWriteEnv) != "1" {
			return fmt.Errorf("custom OKX Demo write endpoints require explicit %s=1 opt-in", OKXDemoAllowCustomWriteEnv)
		}
	default:
		return fmt.Errorf("unknown OKX Demo host profile %q", cfg.HostProfile)
	}

	rest := strings.TrimSpace(cfg.RESTBaseURL)
	ws := strings.TrimSpace(cfg.WSBaseURL)
	if rest == "" || ws == "" {
		return fmt.Errorf("custom OKX Demo writes require both REST and WebSocket endpoint overrides")
	}
	if err := validateURL(rest, OKXDemoRESTBaseURLEnv, "https"); err != nil {
		return err
	}
	if err := validateURL(ws, OKXDemoWSBaseURLEnv, "wss"); err != nil {
		return err
	}

	restURL, _ := url.Parse(rest)
	if strings.EqualFold(strings.TrimSuffix(restURL.Hostname(), "."), "www.okx.com") {
		return fmt.Errorf("%s must not point credentialed Demo writes at the OKX website host", OKXDemoRESTBaseURLEnv)
	}
	wsURL, _ := url.Parse(ws)
	wsHost := strings.ToLower(strings.TrimSuffix(wsURL.Hostname(), "."))
	if strings.HasSuffix(wsHost, ".okx.com") && strings.HasPrefix(wsHost, "ws") && !strings.Contains(wsHost, "pap") {
		return fmt.Errorf("%s must not point credentialed Demo writes at production WebSocket host %s", OKXDemoWSBaseURLEnv, wsHost)
	}
	return nil
}

func OKXDemoHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

func BybitDemoHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

func BybitTestnetHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

func BitgetDemoHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

func GateTestnetHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

func AsterTestnetHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

func NadoTestnetHTTPClient(timeout time.Duration) (*http.Client, error) {
	return proxiedHTTPClient(timeout)
}

// Deprecated: use BitgetDemoHTTPClient.
func BitgetTestnetHTTPClient(timeout time.Duration) (*http.Client, error) {
	return BitgetDemoHTTPClient(timeout)
}

func SkipIfTransientLiveNetworkError(t testing.TB, err error, context string) {
	t.Helper()
	if IsTransientLiveNetworkError(err) {
		if context == "" {
			context = "live exchange endpoint"
		}
		t.Skipf("skipping: %s unavailable during live test: %v", context, err)
	}
}

func IsTransientLiveNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "client.timeout exceeded while awaiting headers") ||
		strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "tls handshake timeout") ||
		strings.Contains(lower, "failed to verify certificate") ||
		strings.Contains(lower, "x509: certificate is valid for") ||
		strings.Contains(lower, "connection reset by peer") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "host is down") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "no route to host") ||
		strings.Contains(lower, "no such host") ||
		strings.TrimSpace(lower) == "eof"
}

func RequireFull(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: full verification test excluded by -short")
	}
	if os.Getenv("RUN_FULL") != "1" {
		t.Skip("skipping: set RUN_FULL=1 to run full verification tests")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	RequireEnv(t, vars...)
}

func RequireSoak(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: soak verification test excluded by -short")
	}
	if os.Getenv("RUN_SOAK") != "1" {
		t.Skip("skipping: set RUN_SOAK=1 to run soak verification tests")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	RequireEnv(t, vars...)
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func applyLegacyAliases() {
	for legacy, canonical := range map[string]string{
		"EDGEX_PRIVATE_KEY":                   "EDGEX_STARK_PRIVATE_KEY",
		"NADO_SUB_ACCOUNT_NAME":               "NADO_SUBACCOUNT_NAME",
		"OKX_SECRET_KEY":                      "OKX_API_SECRET",
		"OKX_PASSPHRASE":                      "OKX_API_PASSPHRASE",
		BitgetLegacyTestnetAPIKeyEnv:          BitgetDemoAPIKeyEnv,
		BitgetLegacyTestnetAPISecretEnv:       BitgetDemoAPISecretEnv,
		BitgetLegacyTestnetPassphraseEnv:      BitgetDemoPassphraseEnv,
		BitgetLegacyTestnetSpotSymbolEnv:      BitgetDemoSpotSymbolEnv,
		BitgetLegacyTestnetUSDTPerpSymbolEnv:  BitgetDemoUSDTPerpSymbolEnv,
		BitgetLegacyTestnetUSDCPerpSymbolEnv:  BitgetDemoUSDCPerpSymbolEnv,
		BitgetLegacyTestnetMaxNotionalUSDTEnv: BitgetDemoMaxNotionalUSDTEnv,
		BitgetLegacyTestnetMaxNotionalUSDCEnv: BitgetDemoMaxNotionalUSDCEnv,
		BitgetLegacyTestnetRESTBaseURLEnv:     BitgetDemoRESTBaseURLEnv,
		BitgetLegacyTestnetPublicWSURLEnv:     BitgetDemoPublicWSURLEnv,
		BitgetLegacyTestnetPrivateWSURLEnv:    BitgetDemoPrivateWSURLEnv,
	} {
		if _, exists := os.LookupEnv(canonical); exists {
			continue
		}
		if value, exists := os.LookupEnv(legacy); exists {
			_ = os.Setenv(canonical, value)
		}
	}
}

func missingEnv(vars ...string) []string {
	var missing []string
	for _, key := range vars {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func hasAnyEnv(vars ...string) bool {
	for _, key := range vars {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

func envOrDefault(key, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	return value
}

func envOrDefaultAny(keys []string, defaultValue string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			return value
		}
	}
	return defaultValue
}

func parsePositiveDecimalEnv(key, defaultValue string) (decimal.Decimal, error) {
	value := envOrDefault(key, defaultValue)
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%s must be a decimal: %w", key, err)
	}
	if !parsed.IsPositive() {
		return decimal.Decimal{}, fmt.Errorf("%s must be positive", key)
	}
	return parsed, nil
}

func proxyURLFromEnv() (string, error) {
	proxyURL := strings.TrimSpace(os.Getenv("PROXY"))
	if proxyURL == "" {
		return "", nil
	}
	if err := validateURL(proxyURL, "PROXY", "http", "https", "socks5"); err != nil {
		return "", err
	}
	return proxyURL, nil
}

func parseInt64Env(key string) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func parseUint8Env(key string) (uint8, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	parsed, err := strconv.ParseUint(value, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned 8-bit integer: %w", key, err)
	}
	return uint8(parsed), nil
}

func validateURL(raw, envName string, allowedSchemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s has invalid URL", envName)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must include URL scheme and host", envName)
	}
	scheme := strings.ToLower(parsed.Scheme)
	for _, allowed := range allowedSchemes {
		if scheme == allowed {
			return nil
		}
	}
	return fmt.Errorf("%s must use one of schemes: %s", envName, strings.Join(allowedSchemes, ", "))
}

func validateBitgetDemoWSURL(raw, envName string) error {
	if err := validateURL(raw, envName, "ws", "wss"); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s has invalid URL", envName)
	}
	if strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "ws.bitget.com") {
		return fmt.Errorf("%s must not point Bitget Demo acceptance at production host %s", envName, parsed.Hostname())
	}
	return nil
}

func validateBitgetDemoWriteProfile(profile BitgetEndpointProfile) error {
	if !profile.PAPTrading {
		return fmt.Errorf("Bitget Demo credentialed writes require paptrading mode")
	}
	if isKnownBitgetDemoProfile(profile) {
		return nil
	}
	if os.Getenv(BitgetDemoAllowCustomWriteEnv) != "1" {
		return fmt.Errorf("custom Bitget Demo write endpoints require explicit %s=1 opt-in", BitgetDemoAllowCustomWriteEnv)
	}
	if err := validateURL(profile.RESTBaseURL, BitgetDemoRESTBaseURLEnv, "https"); err != nil {
		return err
	}
	if err := validateURL(profile.PublicWSURL, BitgetDemoPublicWSURLEnv, "wss"); err != nil {
		return err
	}
	if err := validateURL(profile.PrivateWSURL, BitgetDemoPrivateWSURLEnv, "wss"); err != nil {
		return err
	}
	if err := validateBitgetDemoWSURL(profile.PublicWSURL, BitgetDemoPublicWSURLEnv); err != nil {
		return err
	}
	return validateBitgetDemoWSURL(profile.PrivateWSURL, BitgetDemoPrivateWSURLEnv)
}

func isKnownBitgetDemoProfile(profile BitgetEndpointProfile) bool {
	return profile.PAPTrading &&
		profile.RESTBaseURL == "https://api.bitget.com" &&
		profile.PublicWSURL == "wss://wspap.bitget.com/v3/ws/public" &&
		profile.PrivateWSURL == "wss://wspap.bitget.com/v3/ws/private"
}

func validateGateTestnetURL(raw, envName string, allowedSchemes ...string) error {
	if err := validateURL(raw, envName, allowedSchemes...); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s has invalid URL", envName)
	}
	host := strings.ToLower(parsed.Hostname())
	for _, productionHost := range []string{"api.gateio.ws", "fx-ws.gateio.ws"} {
		if host == productionHost {
			return fmt.Errorf("%s must not point Gate Testnet acceptance at production host %s", envName, productionHost)
		}
	}
	return nil
}

func redactSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return "****"
	}
	return value[:2] + "****" + value[len(value)-2:]
}

func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<redacted-url>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

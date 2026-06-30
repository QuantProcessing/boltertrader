package spot

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

const (
	hyperliquidLiveWriteFlag = "HYPERLIQUID_ENABLE_LIVE_WRITE_TESTS"
	hyperliquidSpotCoin      = "PURR/USDC"
)

func newLiveClient() *Client {
	return NewClient(hyperliquid.NewClient())
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveCredentials(t, "HYPERLIQUID_PRIVATE_KEY", "HYPERLIQUID_ACCOUNT_ADDR")
	vault := os.Getenv("HYPERLIQUID_VAULT")
	base := hyperliquid.NewClient().
		WithCredentials(os.Getenv("HYPERLIQUID_PRIVATE_KEY"), &vault).
		WithAccount(os.Getenv("HYPERLIQUID_ACCOUNT_ADDR"))
	return NewClient(base)
}

func requireHyperliquidLiveWrite(t *testing.T, vars ...string) *Client {
	t.Helper()
	required := append([]string{"HYPERLIQUID_PRIVATE_KEY", "HYPERLIQUID_ACCOUNT_ADDR"}, vars...)
	testenv.RequireLiveWrite(t, hyperliquidLiveWriteFlag, required...)
	return newLivePrivateClient(t)
}

func hyperliquidEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func hyperliquidPrivateKeyForLocalSigning() string {
	return strings.Repeat("01", 32)
}

func TestNewClient(t *testing.T) {
	base := hyperliquid.NewClient()
	client := NewClient(base)

	if client == nil || client.Client != base {
		t.Fatalf("unexpected client: %+v", client)
	}
}

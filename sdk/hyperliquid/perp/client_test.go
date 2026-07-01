package perp

import (
	"os"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

const hyperliquidLiveWriteFlag = "HYPERLIQUID_ENABLE_LIVE_WRITE_TESTS"

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t, "HYPERLIQUID_PRIVATE_KEY", "HYPERLIQUID_ACCOUNT_ADDR")
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
	vault := os.Getenv("HYPERLIQUID_VAULT")
	base := hyperliquid.NewClient().
		WithCredentials(os.Getenv("HYPERLIQUID_PRIVATE_KEY"), &vault).
		WithAccount(os.Getenv("HYPERLIQUID_ACCOUNT_ADDR"))
	return NewClient(base)
}

func TestClientCompanion_NewClient(t *testing.T) {
	base := hyperliquid.NewClient()
	client := NewClient(base)
	if client.Client != base {
		t.Fatal("expected base client to be embedded")
	}
}

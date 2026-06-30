package lighter

import (
	"os"
	"strconv"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

const (
	lighterLiveWriteFlag = "LIGHTER_ENABLE_LIVE_WRITE_TESTS"
	lighterTestMarketID  = 0
)

func newLiveClient() *Client {
	return NewClient()
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveCredentials(t, "LIGHTER_PRIVATE_KEY", "LIGHTER_ACCOUNT_INDEX", "LIGHTER_KEY_INDEX")
	accountIndex, err := strconv.ParseInt(os.Getenv("LIGHTER_ACCOUNT_INDEX"), 10, 64)
	if err != nil {
		t.Fatalf("parse LIGHTER_ACCOUNT_INDEX: %v", err)
	}
	keyIndex64, err := strconv.ParseUint(os.Getenv("LIGHTER_KEY_INDEX"), 10, 8)
	if err != nil {
		t.Fatalf("parse LIGHTER_KEY_INDEX: %v", err)
	}
	return NewClient().WithCredentials(os.Getenv("LIGHTER_PRIVATE_KEY"), accountIndex, uint8(keyIndex64))
}

func requireLighterLiveWrite(t *testing.T, vars ...string) *Client {
	t.Helper()
	required := append([]string{"LIGHTER_PRIVATE_KEY", "LIGHTER_ACCOUNT_INDEX", "LIGHTER_KEY_INDEX"}, vars...)
	testenv.RequireLiveWrite(t, lighterLiveWriteFlag, required...)
	return newLivePrivateClient(t)
}

func lighterEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func lighterIntEnv(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", key, err)
	}
	return value
}

func lighterInt64Env(t *testing.T, key string, fallback int64) int64 {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("parse %s: %v", key, err)
	}
	return value
}

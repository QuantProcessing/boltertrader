package okx

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantProcessing/exchanges/internal/testenv"
	"github.com/stretchr/testify/require"
)

func TestWSClient_PlaceOrderWS(t *testing.T) {
	client := newLiveWriteOKXWSClient(t, "OKX_TEST_ORDER_SIZE", "OKX_TEST_ORDER_PRICE")
	instID := okxEnvOrDefault("OKX_TEST_ORDER_INST_ID", okxSwapInstID)
	instIDCode := okxLiveWSInstIDCode(t, instID)
	price := os.Getenv("OKX_TEST_ORDER_PRICE")
	side := okxEnvOrDefault("OKX_TEST_ORDER_SIDE", "buy")

	order, err := client.PlaceOrderWS(&OrderRequest{
		InstId:     instID,
		InstIdCode: &instIDCode,
		Side:       side,
		Sz:         os.Getenv("OKX_TEST_ORDER_SIZE"),
		Px:         &price,
		TdMode:     okxEnvOrDefault("OKX_TEST_TD_MODE", "isolated"),
		OrdType:    "limit",
	})
	require.NoError(t, err)
	require.NotNil(t, order)
}

func TestWSClient_CancelOrderWS(t *testing.T) {
	client := newLiveWriteOKXWSClient(t, "OKX_TEST_ORDER_ID")
	instID := okxEnvOrDefault("OKX_TEST_ORDER_INST_ID", okxSwapInstID)
	instIDCode := okxLiveWSInstIDCode(t, instID)
	orderID := os.Getenv("OKX_TEST_ORDER_ID")

	order, err := client.CancelOrderWS(instIDCode, &orderID, nil)
	require.NoError(t, err)
	require.NotNil(t, order)
}

func TestWSClient_ModifyOrderWS(t *testing.T) {
	client := newLiveWriteOKXWSClient(t, "OKX_TEST_ORDER_ID", "OKX_TEST_ORDER_PRICE")
	instID := okxEnvOrDefault("OKX_TEST_ORDER_INST_ID", okxSwapInstID)
	instIDCode := okxLiveWSInstIDCode(t, instID)
	orderID := os.Getenv("OKX_TEST_ORDER_ID")
	newPrice := os.Getenv("OKX_TEST_ORDER_PRICE")

	order, err := client.ModifyOrderWS(&ModifyOrderRequest{
		InstId:     instID,
		InstIdCode: &instIDCode,
		OrdId:      &orderID,
		NewPx:      &newPrice,
	})
	require.NoError(t, err)
	require.NotNil(t, order)
}

func TestWSClient_CancelOrdersWS(t *testing.T) {
	client := newLiveWriteOKXWSClient(t, "OKX_TEST_ORDER_ID")
	instID := okxEnvOrDefault("OKX_TEST_ORDER_INST_ID", okxSwapInstID)
	instIDCode := okxLiveWSInstIDCode(t, instID)
	orderID := os.Getenv("OKX_TEST_ORDER_ID")

	orders, err := client.CancelOrdersWS([]CancelOrderRequest{{InstIdCode: &instIDCode, OrdId: &orderID}})
	require.NoError(t, err)
	require.NotEmpty(t, orders)
}

func newLiveWriteOKXWSClient(t *testing.T, extraVars ...string) *WSClient {
	t.Helper()
	required := append([]string{"OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE"}, extraVars...)
	testenv.RequireLiveWrite(t, okxLiveWriteFlag, required...)
	ctx, cancel := context.WithCancel(context.Background())
	client := NewWSClient(ctx).WithCredentials(os.Getenv("OKX_API_KEY"), os.Getenv("OKX_API_SECRET"), os.Getenv("OKX_API_PASSPHRASE"))
	require.NoError(t, client.Connect())
	t.Cleanup(func() {
		cancel()
		if client.Conn != nil {
			_ = client.Conn.Close()
		}
	})
	return client
}

func okxLiveWSInstIDCode(t *testing.T, instID string) int64 {
	t.Helper()
	if raw := os.Getenv("OKX_TEST_INST_ID_CODE"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		require.NoError(t, err)
		return value
	}

	instType := "SPOT"
	if strings.Contains(instID, "-SWAP") {
		instType = "SWAP"
	}
	insts, err := newLiveClient().GetInstruments(context.Background(), instType)
	require.NoError(t, err)
	for _, inst := range insts {
		if inst.InstId == instID && inst.InstIdCode != nil {
			return *inst.InstIdCode
		}
	}
	t.Fatalf("instIdCode not found for %s", instID)
	return 0
}

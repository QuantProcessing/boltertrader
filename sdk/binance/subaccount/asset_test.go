package subaccount

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/QuantProcessing/exchanges/internal/testenv"
)

func TestClient_GetAssetsV4(t *testing.T) {
	email := os.Getenv("BINANCE_SUBACCOUNT_EMAIL")
	if email == "" {
		t.Skip("BINANCE_SUBACCOUNT_EMAIL is required for sub-account asset live test")
	}
	got, err := newLivePrivateClient(t).GetAssetsV4(context.Background(), email)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance sub-account assets endpoint")
		t.Fatalf("GetAssetsV4: %v", err)
	}
	if got == nil {
		t.Fatal("expected sub-account assets")
	}
}

func TestClient_GetSpotAssetsSummary(t *testing.T) {
	got, err := newLivePrivateClient(t).GetSpotAssetsSummary(
		context.Background(),
		os.Getenv("BINANCE_SUBACCOUNT_EMAIL"),
		1,
		10,
	)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance sub-account spot summary endpoint")
		t.Fatalf("GetSpotAssetsSummary: %v", err)
	}
	if got == nil {
		t.Fatal("expected sub-account spot assets summary")
	}
}

func TestClient_FuturesTransfer(t *testing.T) {
	client := requireBinanceSubAccountLiveWrite(t,
		"BINANCE_SUBACCOUNT_EMAIL",
		"BINANCE_SUBACCOUNT_TRANSFER_ASSET",
		"BINANCE_SUBACCOUNT_TRANSFER_AMOUNT",
		"BINANCE_SUBACCOUNT_FUTURES_TRANSFER_TYPE",
	)

	var transferType int
	if _, err := fmt.Sscanf(os.Getenv("BINANCE_SUBACCOUNT_FUTURES_TRANSFER_TYPE"), "%d", &transferType); err != nil {
		t.Fatalf("parse BINANCE_SUBACCOUNT_FUTURES_TRANSFER_TYPE: %v", err)
	}
	got, err := client.FuturesTransfer(
		context.Background(),
		os.Getenv("BINANCE_SUBACCOUNT_EMAIL"),
		os.Getenv("BINANCE_SUBACCOUNT_TRANSFER_ASSET"),
		os.Getenv("BINANCE_SUBACCOUNT_TRANSFER_AMOUNT"),
		transferType,
	)
	if err != nil {
		t.Fatalf("FuturesTransfer: %v", err)
	}
	if got == nil || got.TxnID == "" {
		t.Fatalf("unexpected futures transfer response: %+v", got)
	}
}

func TestClient_UniversalTransfer(t *testing.T) {
	client := requireBinanceSubAccountLiveWrite(t,
		"BINANCE_SUBACCOUNT_TRANSFER_ASSET",
		"BINANCE_SUBACCOUNT_TRANSFER_AMOUNT",
		"BINANCE_SUBACCOUNT_FROM_ACCOUNT_TYPE",
		"BINANCE_SUBACCOUNT_TO_ACCOUNT_TYPE",
	)
	got, err := client.UniversalTransfer(context.Background(), UniversalTransferRequest{
		FromEmail:       os.Getenv("BINANCE_SUBACCOUNT_FROM_EMAIL"),
		ToEmail:         os.Getenv("BINANCE_SUBACCOUNT_TO_EMAIL"),
		FromAccountType: os.Getenv("BINANCE_SUBACCOUNT_FROM_ACCOUNT_TYPE"),
		ToAccountType:   os.Getenv("BINANCE_SUBACCOUNT_TO_ACCOUNT_TYPE"),
		ClientTranID:    os.Getenv("BINANCE_SUBACCOUNT_CLIENT_TRAN_ID"),
		Symbol:          os.Getenv("BINANCE_SUBACCOUNT_TRANSFER_SYMBOL"),
		Asset:           os.Getenv("BINANCE_SUBACCOUNT_TRANSFER_ASSET"),
		Amount:          os.Getenv("BINANCE_SUBACCOUNT_TRANSFER_AMOUNT"),
	})
	if err != nil {
		t.Fatalf("UniversalTransfer: %v", err)
	}
	if got == nil || got.TranID == 0 {
		t.Fatalf("unexpected universal transfer response: %+v", got)
	}
}

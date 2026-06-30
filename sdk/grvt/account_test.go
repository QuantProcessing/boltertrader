package grvt

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func requireFullEnv(t *testing.T) {
	t.Helper()
	testenv.RequireFull(t, "GRVT_API_KEY", "GRVT_SUB_ACCOUNT_ID", "GRVT_PRIVATE_KEY")
}

func GetEnv() (string, string, string) {
	apiKey := os.Getenv("GRVT_API_KEY")
	subaccount := os.Getenv("GRVT_SUB_ACCOUNT_ID")
	privateKey := os.Getenv("GRVT_PRIVATE_KEY")
	return apiKey, subaccount, privateKey
}

func TestGetFundingAccountSummary(t *testing.T) {
	requireFullEnv(t)
	apiKey, subAccountID, privateKey := GetEnv()
	client := newLiveClient().WithCredentials(apiKey, subAccountID, privateKey)
	var fundingAccountSummary *GetFundingAccountSummaryResponse
	retryGRVTLive(t, "GetFundingAccountSummary", func() error {
		var err error
		fundingAccountSummary, err = client.GetFundingAccountSummary(context.Background())
		return err
	})
	fmt.Printf("%+v\n", fundingAccountSummary)
}

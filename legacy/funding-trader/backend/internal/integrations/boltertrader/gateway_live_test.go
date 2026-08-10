package boltertrader

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
)

func TestGatewayReadOnlyLive(t *testing.T) {
	if os.Getenv("RUN_GATEWAY_READ") != "1" {
		t.Skip("set RUN_GATEWAY_READ=1 for the explicit read-only Gateway check")
	}

	mode := configuredProductMode()
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: os.Getenv("FT_GATEWAY_BASE_URL"),
		Token:   liveGatewayToken(t),
		Mode:    mode,
	})
	if err != nil {
		t.Fatalf("construct Gateway client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ready, err := client.Ready(ctx)
	if err != nil {
		t.Fatalf("Gateway readiness: %v", err)
	}
	if !ready.Ready {
		t.Fatalf("Gateway is not ready: %v", ready.Reasons)
	}

	discovery, err := client.DiscoverAccounts(ctx)
	if err != nil {
		t.Fatalf("discover accounts: %v", err)
	}
	if !discovery.Ready {
		if len(discovery.Reasons) == 0 {
			t.Fatal("degraded account discovery has no stable reason")
		}
		t.Logf("configured mode account discovery is degraded: %v", discovery.Reasons)
	}

	perps := make([]DiscoveredAccount, 0, len(discovery.Accounts))
	for index := range discovery.Accounts {
		account := discovery.Accounts[index]
		if account.Product == "perp" && account.ReadReady && account.Capability.Operations.Funding {
			perps = append(perps, account)
		}
	}
	if len(perps) == 0 {
		t.Fatal("no read-ready perpetual funding account for configured mode")
	}
	for _, perp := range perps {
		perp := perp
		t.Run(perp.AccountID, func(t *testing.T) {
			assertGatewayPrivateReads(t, ctx, client, perp)
		})
	}
}

func assertGatewayPrivateReads(t *testing.T, ctx context.Context, client *GatewayClient, perp DiscoveredAccount) {
	t.Helper()
	status, err := client.GetAccount(ctx, perp.AccountID)
	if err != nil {
		t.Fatalf("get account status: %v", err)
	}
	if status.AccountID != perp.AccountID ||
		status.VenueEnvironment != perp.VenueEnvironment ||
		!status.ReadReady {
		t.Fatalf("invalid account status projection for account %q: %+v", perp.AccountID, status)
	}

	instruments, err := client.ListInstruments(ctx, perp.AccountID)
	if err != nil {
		t.Fatalf("list instruments: %v", err)
	}
	if len(instruments) == 0 {
		t.Fatal("Gateway returned no perpetual instruments")
	}

	var instrument *InstrumentResource
	for index := range instruments {
		candidate := &instruments[index]
		if candidate.Product == "perp" && candidate.VenueEnvironment == perp.VenueEnvironment &&
			strings.EqualFold(candidate.BaseAsset, "BTC") {
			instrument = candidate
			break
		}
	}
	if instrument == nil {
		for index := range instruments {
			candidate := &instruments[index]
			if candidate.Product == "perp" && candidate.VenueEnvironment == perp.VenueEnvironment {
				instrument = candidate
				break
			}
		}
	}
	if instrument == nil {
		t.Fatal("Gateway returned no matching perpetual instrument")
	}

	funding, err := client.FundingRates(ctx, perp.AccountID, instrument.InstrumentID)
	if err != nil {
		t.Fatalf("read current funding: %v", err)
	}
	if funding.AccountID != perp.AccountID || funding.InstrumentID != instrument.InstrumentID {
		t.Fatalf("invalid funding projection for account %q", perp.AccountID)
	}

	balances, err := client.Balances(ctx, perp.AccountID)
	if err != nil {
		t.Fatalf("read balances: %v", err)
	}
	positions, err := client.Positions(ctx, perp.AccountID, "")
	if err != nil {
		t.Fatalf("read all positions: %v", err)
	}
	for _, position := range positions {
		if position.AccountID != perp.AccountID || position.InstrumentID == "" || position.Position.Quantity.IsZero() {
			t.Fatalf("invalid non-zero position projection: %+v", position)
		}
	}
	openOrders, err := client.OpenOrders(ctx, perp.AccountID, instrument.InstrumentID)
	if err != nil {
		t.Fatalf("read open orders: %v", err)
	}
	fills, err := client.FillsPage(ctx, GatewayPageRequest{
		AccountID: perp.AccountID, InstrumentID: instrument.InstrumentID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("read fills: %v", err)
	}
	for _, fill := range fills.Items {
		if fill.AccountID != perp.AccountID || fill.InstrumentID != instrument.InstrumentID ||
			!fill.Fill.Price.IsPositive() || !fill.Fill.Quantity.IsPositive() || fill.Fill.FillID == "" {
			t.Fatalf("invalid fill/fee projection: %+v", fill)
		}
	}

	risk, err := client.RiskLimits(ctx, perp.AccountID)
	if err != nil {
		t.Fatalf("read risk-limit reference: %v", err)
	}
	if risk.Available || strings.TrimSpace(risk.Reason) == "" {
		t.Fatalf("Gateway V1 risk-limit gap was not represented explicitly: %+v", risk)
	}

	fundingPayments := 0
	if perp.Capability.Operations.AccountFundingPayments {
		end := time.Now().UTC()
		page, err := client.FundingPayments(ctx, GatewayFundingPaymentRequest{
			AccountID: perp.AccountID, InstrumentID: instrument.InstrumentID,
			SettlementAsset: perp.Settlement, Start: end.Add(-30 * 24 * time.Hour), End: end, Limit: 100,
		})
		if err != nil {
			t.Fatalf("read actual funding payments: %v", err)
		}
		if page.AccountID != perp.AccountID || page.InstrumentID != instrument.InstrumentID ||
			page.SettlementAsset != perp.Settlement || len(page.Items) > 100 || page.Status == "" {
			t.Fatalf("invalid funding-payment page: %+v", page)
		}
		for _, payment := range page.Items {
			if payment.AccountID != perp.AccountID || payment.InstrumentID != instrument.InstrumentID ||
				payment.SettlementAsset != perp.Settlement || payment.FactID == "" {
				t.Fatalf("invalid funding-payment fact: %+v", payment)
			}
		}
		fundingPayments = len(page.Items)
	}

	t.Logf(
		"account=%s environment=%s settlement=%s balances=%d positions=%d openOrders=%d fills=%d fundingPayments=%d riskLimitsAvailable=%t",
		perp.AccountID,
		perp.VenueEnvironment,
		perp.Settlement,
		len(balances),
		len(positions),
		len(openOrders),
		len(fills.Items),
		fundingPayments,
		risk.Available,
	)
}

func TestGatewayTwoLegWriteAcceptanceLive(t *testing.T) {
	if os.Getenv("RUN_GATEWAY_ACCEPTANCE_GATE") != "1" {
		t.Skip("set RUN_GATEWAY_ACCEPTANCE_GATE=1 for the explicit write-readiness gate")
	}
	mode := configuredProductMode()
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: os.Getenv("FT_GATEWAY_BASE_URL"),
		Token:   liveGatewayToken(t),
		Mode:    mode,
	})
	if err != nil {
		t.Fatalf("construct Gateway client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	discovery, err := client.DiscoverAccounts(ctx)
	if err != nil {
		t.Fatalf("discover configured mode accounts: %v", err)
	}
	acceptance := EvaluateTwoLegWriteAcceptance(discovery)
	t.Logf(
		"mode=%s discoveredAccounts=%d discoveryReady=%t discoveryReasons=%v twoLegWriteReady=%t reason=%s readyPerpAccounts=%d readyPerpVenues=%d eligibleVenues=%d compatibleSettlementVenues=%d requiredVenues=2",
		mode,
		len(discovery.Accounts),
		discovery.Ready,
		discovery.Reasons,
		acceptance.Ready,
		acceptance.Reason,
		acceptance.ReadyPerpAccountCount,
		acceptance.ReadyPerpVenueCount,
		acceptance.EligibleVenueCount,
		acceptance.CompatibleSettlementVenueCount,
	)
}

func configuredProductMode() config.Mode {
	mode := config.Mode(strings.TrimSpace(os.Getenv("FT_MODE")))
	if mode == "" {
		return config.Test
	}
	return mode
}

func liveGatewayToken(t *testing.T) string {
	t.Helper()
	token := os.Getenv("FT_GATEWAY_TOKEN")
	if tokenPath := os.Getenv("FT_GATEWAY_TOKEN_FILE"); token == "" && tokenPath != "" {
		raw, err := os.ReadFile(tokenPath)
		if err != nil {
			t.Fatalf("read Gateway token file: %v", err)
		}
		token = strings.TrimSpace(string(raw))
	}
	return token
}

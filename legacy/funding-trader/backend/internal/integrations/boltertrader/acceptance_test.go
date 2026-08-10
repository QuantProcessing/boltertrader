package boltertrader

import (
	"context"
	"errors"
	"testing"
)

func TestEvaluateTwoLegWriteAcceptance(t *testing.T) {
	tests := []struct {
		name        string
		discovery   AccountDiscovery
		wantReady   bool
		wantReason  string
		wantVenues  int
		wantAccount int
	}{
		{
			name: "one eligible perp account remains explicitly blocked",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				readyExecutionAccount("binance-perp", "binance"),
			}},
			wantReason:  TwoLegWriteReadyReasonInsufficientPerpVenues,
			wantVenues:  1,
			wantAccount: 1,
		},
		{
			name: "two eligible accounts on same venue remain blocked",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				readyExecutionAccount("binance-perp-a", "binance"),
				readyExecutionAccount("binance-perp-b", "binance"),
			}},
			wantReason:  TwoLegWriteReadyReasonInsufficientPerpVenues,
			wantVenues:  1,
			wantAccount: 2,
		},
		{
			name:       "catalog unavailable when no accounts are discoverable",
			discovery:  AccountDiscovery{},
			wantReason: TwoLegWriteReadyReasonCatalogUnavailable,
		},
		{
			name: "catalog unavailable when gateway reports unavailable catalog only",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				{
					AccountID: "binance-perp", Venue: "binance", Product: "perp",
					ReadReady: false, WriteReady: false, Reason: "catalog_unavailable",
				},
			}},
			wantReason: TwoLegWriteReadyReasonCatalogUnavailable,
		},
		{
			name: "catalog unavailable explains the missing second perp venue",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				readyExecutionAccount("binance-perp", "binance"),
				{
					AccountID: "lighter-perp", Venue: "lighter", Product: "perp",
					ReadReady: false, WriteReady: false, Reason: "catalog_unavailable",
				},
			}},
			wantReason:  TwoLegWriteReadyReasonCatalogUnavailable,
			wantVenues:  1,
			wantAccount: 1,
		},
		{
			name: "missing execution capability blocks otherwise ready distinct venues",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				readyExecutionAccount("binance-perp", "binance"),
				accountWithOperations("lighter-perp", "lighter", CapabilityOperations{
					Place: true, Cancel: true, OpenOrders: true, OrderHistory: true,
					Fills: true, Account: true, Positions: true, PrivateStream: false,
				}),
			}},
			wantReason:  TwoLegWriteReadyReasonMissingExecutionCaps,
			wantVenues:  1,
			wantAccount: 1,
		},
		{
			name: "missing account funding-payment capability blocks otherwise ready distinct venues",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				readyExecutionAccount("binance-perp", "binance"),
				func() DiscoveredAccount {
					account := readyExecutionAccount("lighter-perp", "lighter")
					account.Capability.Operations.AccountFundingPayments = false
					return account
				}(),
			}},
			wantReason:  TwoLegWriteReadyReasonMissingFundingPaymentCapability,
			wantVenues:  1,
			wantAccount: 1,
		},
		{
			name: "two eligible distinct venues pass",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				readyExecutionAccount("binance-perp", "binance"),
				readyExecutionAccount("lighter-perp", "lighter"),
			}},
			wantReady:   true,
			wantVenues:  2,
			wantAccount: 2,
		},
		{
			name: "distinct venues with different settlement assets remain blocked",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				readyExecutionAccount("binance-perp", "binance"),
				func() DiscoveredAccount {
					account := readyExecutionAccount("lighter-perp", "lighter")
					account.Settlement = "USDC"
					return account
				}(),
			}},
			wantReason:  "incompatible-settlement-assets",
			wantVenues:  2,
			wantAccount: 2,
		},
		{
			name: "spot account does not satisfy execution gate",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				readyExecutionAccount("binance-perp", "binance"),
				func() DiscoveredAccount {
					account := readyExecutionAccount("okx-spot", "okx")
					account.Product = "spot"
					return account
				}(),
			}},
			wantReason:  TwoLegWriteReadyReasonInsufficientPerpVenues,
			wantVenues:  1,
			wantAccount: 1,
		},
		{
			name: "missing account identity cannot satisfy execution gate",
			discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
				readyExecutionAccount("binance-perp", "binance"),
				readyExecutionAccount("", "lighter"),
			}},
			wantReason:  TwoLegWriteReadyReasonInsufficientPerpVenues,
			wantVenues:  1,
			wantAccount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateTwoLegWriteAcceptance(tt.discovery)
			if got.Ready != tt.wantReady || got.Reason != tt.wantReason ||
				got.EligibleVenueCount != tt.wantVenues || len(got.EligibleAccountIDs) != tt.wantAccount {
				t.Fatalf("acceptance=%+v", got)
			}
		})
	}
}

func TestCheckTwoLegWriteAcceptanceUsesLiveContextAwareDiscovery(t *testing.T) {
	discoverer := &fakeAccountDiscoverer{
		discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
			readyExecutionAccount("binance-perp", "binance"),
			readyExecutionAccount("lighter-perp", "lighter"),
		}},
	}
	got := CheckTwoLegWriteAcceptance(context.Background(), discoverer)
	if !got.Ready || discoverer.calls != 1 {
		t.Fatalf("acceptance=%+v calls=%d", got, discoverer.calls)
	}

	discoverer.err = errors.New("gateway down")
	got = CheckTwoLegWriteAcceptance(context.Background(), discoverer)
	if got.Ready || got.Reason != TwoLegWriteReadyReasonCatalogUnavailable || discoverer.calls != 2 {
		t.Fatalf("acceptance after error=%+v calls=%d", got, discoverer.calls)
	}
}

func TestEvaluateTwoLegWriteAcceptanceScopesPrivateContinuityToCompatibleSettlementGroups(t *testing.T) {
	discovery := AccountDiscovery{Accounts: []DiscoveredAccount{
		readyExecutionAccount("binance-perp", "binance"),
		readyExecutionAccount("gate-perp", "gate"),
		func() DiscoveredAccount {
			account := readyExecutionAccount("lighter-perp", "lighter")
			account.Settlement = "USDC"
			return account
		}(),
	}}

	got := EvaluateTwoLegWriteAcceptance(discovery)
	if !got.Ready {
		t.Fatalf("acceptance=%+v", got)
	}
	if len(got.EligibleAccountIDs) != 3 {
		t.Fatalf("eligible accounts=%v", got.EligibleAccountIDs)
	}
	want := []string{"binance-perp", "gate-perp"}
	if len(got.CompatibleAccountIDs) != len(want) {
		t.Fatalf("compatible accounts=%v want=%v", got.CompatibleAccountIDs, want)
	}
	for index := range want {
		if got.CompatibleAccountIDs[index] != want[index] {
			t.Fatalf("compatible accounts=%v want=%v", got.CompatibleAccountIDs, want)
		}
	}
}

func TestTwoLegWriteComponentReportsNonRequiredDetails(t *testing.T) {
	component := TwoLegWriteComponent(&fakeAccountDiscoverer{
		discovery: AccountDiscovery{Accounts: []DiscoveredAccount{
			readyExecutionAccount("binance-perp", "binance"),
		}},
	})(context.Background())
	if component.Ready || component.Reason != TwoLegWriteReadyReasonInsufficientPerpVenues {
		t.Fatalf("component=%+v", component)
	}
	if component.Details["eligibleVenues"] != "1" || component.Details["requiredDistinctVenues"] != "2" {
		t.Fatalf("details=%+v", component.Details)
	}
	if _, exposed := component.Details["eligibleAccountIds"]; exposed {
		t.Fatalf("public readiness exposed Gateway account identities: %+v", component.Details)
	}
}

func readyExecutionAccount(accountID, venue string) DiscoveredAccount {
	return accountWithOperations(accountID, venue, CapabilityOperations{
		Place: true, Cancel: true, OpenOrders: true, OrderHistory: true,
		Fills: true, Balances: true, Account: true, Positions: true,
		Funding: true, AccountFundingPayments: true, PrivateStream: true,
	})
}

func accountWithOperations(accountID, venue string, operations CapabilityOperations) DiscoveredAccount {
	return DiscoveredAccount{
		AccountID: accountID, Venue: venue, Product: "perp",
		VenueEnvironment: GatewayDemo, Settlement: "USDT", ReadReady: true, WriteReady: true,
		Capability: CapabilityRow{Operations: operations},
	}
}

type fakeAccountDiscoverer struct {
	discovery AccountDiscovery
	err       error
	calls     int
}

func (discoverer *fakeAccountDiscoverer) DiscoverAccounts(context.Context) (AccountDiscovery, error) {
	discoverer.calls++
	if discoverer.err != nil {
		return AccountDiscovery{}, discoverer.err
	}
	return discoverer.discovery, nil
}

package boltertrader

import (
	"context"
	"sort"
	"strings"

	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
)

const (
	TwoLegWriteReadyReasonCatalogUnavailable              = "catalog-unavailable"
	TwoLegWriteReadyReasonInsufficientPerpVenues          = "insufficient-ready-perp-venues"
	TwoLegWriteReadyReasonIncompatibleSettlementAssets    = "incompatible-settlement-assets"
	TwoLegWriteReadyReasonMissingExecutionCaps            = "missing-execution-capability"
	TwoLegWriteReadyReasonMissingFundingPaymentCapability = "missing-funding-payment-capability"
	twoLegRequiredReadyPerpVenueCount                     = 2
)

// AccountDiscoverer is the minimum Gateway catalog surface needed to prove
// that two-leg private execution can be safely attempted for the current
// environment. Implementations must perform a live, context-aware discovery.
type AccountDiscoverer interface {
	DiscoverAccounts(context.Context) (AccountDiscovery, error)
}

type TwoLegWriteAcceptance struct {
	Ready                          bool
	Reason                         string
	EligibleAccountIDs             []string
	CompatibleAccountIDs           []string
	EligibleVenueCount             int
	CompatibleSettlementVenueCount int
	ReadyPerpVenueCount            int
	ReadyPerpAccountCount          int
}

func CheckTwoLegWriteAcceptance(ctx context.Context, discoverer AccountDiscoverer) TwoLegWriteAcceptance {
	if discoverer == nil {
		return TwoLegWriteAcceptance{Reason: TwoLegWriteReadyReasonCatalogUnavailable}
	}
	discovery, err := discoverer.DiscoverAccounts(ctx)
	if err != nil {
		return TwoLegWriteAcceptance{Reason: TwoLegWriteReadyReasonCatalogUnavailable}
	}
	return EvaluateTwoLegWriteAcceptance(discovery)
}

func EvaluateTwoLegWriteAcceptance(discovery AccountDiscovery) TwoLegWriteAcceptance {
	if len(discovery.Accounts) == 0 {
		return TwoLegWriteAcceptance{Reason: TwoLegWriteReadyReasonCatalogUnavailable}
	}

	readyPerpVenues := make(map[string]struct{})
	eligibleVenues := make(map[string]struct{})
	settlementVenues := make(map[string]map[string]struct{})
	settlementAccounts := make(map[string][]string)
	eligibleAccounts := make([]string, 0, len(discovery.Accounts))
	readyPerpAccountCount := 0
	hasCapabilityGap := false
	hasFundingPaymentCapabilityGap := false
	hasCatalogUnavailable := false

	for _, account := range discovery.Accounts {
		if !strings.EqualFold(strings.TrimSpace(account.Product), "perp") {
			continue
		}
		if !isReadyPerpAccount(account) {
			if stableReason(account.Reason) == TwoLegWriteReadyReasonCatalogUnavailable {
				hasCatalogUnavailable = true
			}
			continue
		}
		readyPerpAccountCount++
		venue := stableVenue(account.Venue)
		if venue == "" {
			hasCapabilityGap = true
			continue
		}
		readyPerpVenues[venue] = struct{}{}
		if !hasTwoLegExecutionCapabilities(account.Capability.Operations) {
			hasCapabilityGap = true
			continue
		}
		if !account.Capability.Operations.AccountFundingPayments {
			hasFundingPaymentCapabilityGap = true
			continue
		}
		eligibleVenues[venue] = struct{}{}
		eligibleAccounts = append(eligibleAccounts, account.AccountID)
		if settlement := stableSettlement(account.Settlement); settlement != "" {
			if settlementVenues[settlement] == nil {
				settlementVenues[settlement] = make(map[string]struct{})
			}
			settlementVenues[settlement][venue] = struct{}{}
			settlementAccounts[settlement] = append(settlementAccounts[settlement], account.AccountID)
		}
	}

	sort.Strings(eligibleAccounts)
	compatibleAccounts := compatibleSettlementAccounts(settlementVenues, settlementAccounts)
	result := TwoLegWriteAcceptance{
		EligibleAccountIDs:             eligibleAccounts,
		CompatibleAccountIDs:           compatibleAccounts,
		EligibleVenueCount:             len(eligibleVenues),
		CompatibleSettlementVenueCount: maxSettlementVenueCount(settlementVenues),
		ReadyPerpVenueCount:            len(readyPerpVenues),
		ReadyPerpAccountCount:          readyPerpAccountCount,
	}
	if result.CompatibleSettlementVenueCount >= twoLegRequiredReadyPerpVenueCount {
		result.Ready = true
		return result
	}
	switch {
	case hasCapabilityGap && result.ReadyPerpVenueCount >= twoLegRequiredReadyPerpVenueCount:
		result.Reason = TwoLegWriteReadyReasonMissingExecutionCaps
	case hasFundingPaymentCapabilityGap && result.ReadyPerpVenueCount >= twoLegRequiredReadyPerpVenueCount:
		result.Reason = TwoLegWriteReadyReasonMissingFundingPaymentCapability
	case result.EligibleVenueCount >= twoLegRequiredReadyPerpVenueCount:
		result.Reason = TwoLegWriteReadyReasonIncompatibleSettlementAssets
	case hasCatalogUnavailable:
		result.Reason = TwoLegWriteReadyReasonCatalogUnavailable
	default:
		result.Reason = TwoLegWriteReadyReasonInsufficientPerpVenues
	}
	return result
}

func compatibleSettlementAccounts(
	settlementVenues map[string]map[string]struct{},
	settlementAccounts map[string][]string,
) []string {
	result := make([]string, 0)
	for settlement, venues := range settlementVenues {
		if len(venues) < twoLegRequiredReadyPerpVenueCount {
			continue
		}
		result = append(result, settlementAccounts[settlement]...)
	}
	sort.Strings(result)
	return result
}

func TwoLegWriteComponent(discoverer AccountDiscoverer) readiness.ComponentCheck {
	return func(ctx context.Context) readiness.Component {
		return CheckTwoLegWriteAcceptance(ctx, discoverer).ReadinessComponent()
	}
}

func (acceptance TwoLegWriteAcceptance) ReadinessComponent() readiness.Component {
	details := map[string]string{
		"eligibleAccounts":           stringInt(len(acceptance.EligibleAccountIDs)),
		"compatibleAccounts":         stringInt(len(acceptance.CompatibleAccountIDs)),
		"eligibleVenues":             stringInt(acceptance.EligibleVenueCount),
		"compatibleSettlementVenues": stringInt(acceptance.CompatibleSettlementVenueCount),
		"readyPerpAccounts":          stringInt(acceptance.ReadyPerpAccountCount),
		"readyPerpVenues":            stringInt(acceptance.ReadyPerpVenueCount),
		"requiredDistinctVenues":     stringInt(twoLegRequiredReadyPerpVenueCount),
	}
	return readiness.Component{
		Ready:   acceptance.Ready,
		Reason:  acceptance.Reason,
		Details: details,
	}
}

func isReadyPerpAccount(account DiscoveredAccount) bool {
	return strings.TrimSpace(account.AccountID) != "" &&
		strings.EqualFold(strings.TrimSpace(account.Product), "perp") &&
		account.ReadReady &&
		account.WriteReady
}

func hasTwoLegExecutionCapabilities(operations CapabilityOperations) bool {
	return operations.Place &&
		operations.Cancel &&
		operations.OpenOrders &&
		operations.OrderHistory &&
		operations.Fills &&
		operations.Balances &&
		operations.Account &&
		operations.Positions &&
		operations.Funding &&
		operations.PrivateStream
}

func stableVenue(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || strings.ContainsAny(value, "\n\r\t /:") {
		return ""
	}
	return value
}

func stableSettlement(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" || strings.ContainsAny(value, "\n\r\t /:") {
		return ""
	}
	return value
}

func maxSettlementVenueCount(groups map[string]map[string]struct{}) int {
	maxCount := 0
	for _, venues := range groups {
		if len(venues) > maxCount {
			maxCount = len(venues)
		}
	}
	return maxCount
}

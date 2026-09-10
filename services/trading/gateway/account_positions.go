package gateway

import (
	"context"
	"fmt"

	"strings"

	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
)

// The login boundary belongs to Auth Edge; Gateway authorizes the calling service.
func (client *Client) ListAccounts(ctx context.Context, principal string) ([]accountpositions.Account, error) {
	if strings.TrimSpace(principal) == "" {
		return nil, fmt.Errorf("Gateway account directory scope is invalid")
	}
	sources, err := client.protocol.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	accounts := make([]accountpositions.Account, 0, len(sources))
	for _, source := range sources {
		routes := make([]accountpositions.AccountRoute, 0, len(source.Routes))
		for _, route := range source.Routes {
			routes = append(routes, accountpositions.AccountRoute{
				RouteID: route.RouteID, Product: route.Product,
				SettlementAsset: route.SettlementAsset,
			})
		}
		label := strings.TrimSpace(source.DisplayName)
		if label == "" {
			label = source.ID
		}
		accounts = append(accounts, accountpositions.Account{
			AccountID: source.ID, Label: label, Venue: source.Venue,
			Environment: source.NativeEnvironment, Status: source.Status, Routes: routes,
		})
	}
	return accounts, nil
}

func (client *Client) PositionSnapshot(ctx context.Context, principal, accountID string) (accountpositions.SourceSnapshot, error) {
	if strings.TrimSpace(principal) == "" || strings.TrimSpace(accountID) == "" {
		return accountpositions.SourceSnapshot{}, fmt.Errorf("Gateway position snapshot scope is invalid")
	}
	snapshot, err := client.protocol.PositionSnapshot(ctx, accountID)
	if err != nil {
		return accountpositions.SourceSnapshot{}, err
	}
	result := accountpositions.SourceSnapshot{AccountID: snapshot.AccountID, Freshness: accountpositions.Freshness(snapshot.Freshness), Reason: snapshot.Reason, VerifiedAt: snapshot.VerifiedAt, Items: make([]accountpositions.SourceItem, 0, len(snapshot.Items))}
	for _, item := range snapshot.Items {
		p := item.Position
		result.Items = append(result.Items, accountpositions.SourceItem{AccountID: item.AccountID, RouteID: item.RouteID, InstrumentID: item.InstrumentID, PositionSlot: item.PositionSlot, ResourceVersion: item.ResourceVersion, ObservedAt: item.ObservedAt, VerifiedAt: item.VerifiedAt,
			Position: accountpositions.SourcePosition{NativeSymbol: p.NativeSymbol, NativeID: p.NativeID, PositionSlot: p.PositionSlot, Side: p.Side, Quantity: p.Quantity, EntryPrice: p.EntryPrice, MarkPrice: p.MarkPrice, UnrealizedPnL: p.UnrealizedPnL, LiquidationPrice: accountpositions.OptionalDecimal(p.LiquidationPrice), Leverage: accountpositions.OptionalDecimal(p.Leverage), MarginUsed: accountpositions.OptionalDecimal(p.MarginUsed)},
		})
	}
	return result, nil
}

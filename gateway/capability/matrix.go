package capability

import "github.com/QuantProcessing/boltertrader/routeprofile"

type Matrix struct {
	Rows []Row `json:"rows"`
}

type Row struct {
	RowID                          string     `json:"row_id"`
	Venue                          string     `json:"venue"`
	Product                        string     `json:"product"`
	Settlement                     string     `json:"settlement,omitempty"`
	TradingSupported               bool       `json:"trading_supported"`
	ExactLookupStrategy            string     `json:"exact_lookup_strategy"`
	HistoryPaginationProvesAbsence bool       `json:"history_pagination_proves_absence"`
	Operations                     Operations `json:"operations"`
}

type Operations struct {
	Place                  bool `json:"place"`
	Amend                  bool `json:"amend"`
	Cancel                 bool `json:"cancel"`
	OpenOrders             bool `json:"open_orders"`
	OrderHistory           bool `json:"order_history"`
	Fills                  bool `json:"fills"`
	Balances               bool `json:"balances"`
	Account                bool `json:"account"`
	Positions              bool `json:"positions"`
	Funding                bool `json:"funding"`
	AccountFundingPayments bool `json:"account_funding_payments"`
	Leverage               bool `json:"leverage"`
	PrivateStream          bool `json:"private_stream"`
	MarginMode             bool `json:"margin_mode"`
	PositionMode           bool `json:"position_mode"`
	FundsTransfer          bool `json:"funds_transfer"`
}

// StaticMatrix describes implemented Gateway operations, not credential
// permissions, test results, connection health or readiness to send an order.
func StaticMatrix() Matrix {
	rows := make([]Row, 0)
	for _, profile := range routeprofile.DefaultProfiles() {
		rows = append(rows, profileRow(profile))
	}
	return Matrix{Rows: rows}
}

func Resolve(venue, product, settlement string) (Row, bool) {
	profile, err := routeprofile.DefaultRegistry().ResolveLegacy(venue, product, settlement)
	if err != nil {
		return Row{}, false
	}
	return profileRow(profile), true
}

func profileRow(profile routeprofile.Profile) Row {
	id := profile.Venue + ":" + string(profile.Product)
	if profile.SettlementAsset != "" {
		id += ":" + profile.SettlementAsset
	}
	trading := !(profile.Venue == "lighter" && profile.Product == routeprofile.ProductSpot)
	operations := Operations{Place: trading, Cancel: trading, OpenOrders: true, OrderHistory: true, Fills: true, Balances: true, Account: true, PrivateStream: true}
	if profile.Product == routeprofile.ProductPerp {
		operations.Positions = true
		operations.Funding = true
		switch profile.Venue {
		case "binance", "lighter", "gate":
			operations.AccountFundingPayments = true
		case "bybit":
			operations.AccountFundingPayments = profile.SettlementAsset == "USDT"
		}
	}
	strategy := "client_or_venue_order_id_exact_readback"
	if profile.Venue == "lighter" {
		strategy = "active_plus_first_inactive_page_scan_with_unconfirmed_absence"
	}
	return Row{RowID: id, Venue: profile.Venue, Product: string(profile.Product), Settlement: profile.SettlementAsset, TradingSupported: trading, ExactLookupStrategy: strategy, Operations: operations}
}

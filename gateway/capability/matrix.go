package capability

type Matrix struct {
	Rows []Row `json:"rows"`
}

type Row struct {
	Code                           string     `json:"code"`
	RowID                          string     `json:"row_id"`
	Venue                          string     `json:"venue"`
	Product                        string     `json:"product"`
	Settlement                     string     `json:"settlement,omitempty"`
	WriteReadyDefault              bool       `json:"write_ready_default"`
	ExactLookup                    string     `json:"exact_lookup"`
	ExactLookupStrategy            string     `json:"exact_lookup_strategy"`
	ExactFixture                   string     `json:"exact_fixture"`
	PromotionEvidenceRequired      string     `json:"promotion_evidence_required"`
	HistoryPaginationProvesAbsence bool       `json:"history_pagination_proves_absence"`
	Operations                     Operations `json:"operations"`
}

type Operations struct {
	Place         bool `json:"place"`
	Amend         bool `json:"amend"`
	Cancel        bool `json:"cancel"`
	OpenOrders    bool `json:"open_orders"`
	OrderHistory  bool `json:"order_history"`
	Fills         bool `json:"fills"`
	Balances      bool `json:"balances"`
	Account       bool `json:"account"`
	Positions     bool `json:"positions"`
	Funding       bool `json:"funding"`
	Leverage      bool `json:"leverage"`
	PrivateStream bool `json:"private_stream"`
	MarginMode    bool `json:"margin_mode"`
	PositionMode  bool `json:"position_mode"`
	FundsTransfer bool `json:"funds_transfer"`
}

func StaticMatrix() Matrix {
	rows := []Row{
		spot("BNS", "binance"), perp("BNP", "binance", "USDT"),
		spot("OXS", "okx"), perp("OXP", "okx", "USDT"),
		spot("LIS", "lighter"), perp("LIP", "lighter", "USDC"),
		spot("HLS", "hyperliquid"), perp("HLP", "hyperliquid", "USDC"),
		spot("BYS", "bybit"), perp("BYU", "bybit", "USDT"), perp("BYC", "bybit", "USDC"),
		spot("BGS", "bitget"), perp("BGU", "bitget", "USDT"), perp("BGC", "bitget", "USDC"),
		spot("GTS", "gate"), perp("GTU", "gate", "USDT"),
		spot("ATS", "aster"), perp("ATP", "aster", "USDT"),
		spotSettled("NDS", "nado", "USDT0"), perp("NDP", "nado", "USDT0"),
	}
	return Matrix{Rows: rows}
}

func spot(code, venue string) Row {
	return row(code, venue, "spot", "", Operations{
		Place: true, Cancel: true, OpenOrders: true, OrderHistory: true, Fills: true,
		Balances: true, Account: true, PrivateStream: true,
	})
}

func spotSettled(code, venue, settlement string) Row {
	result := spot(code, venue)
	result.Settlement = settlement
	result.RowID = venue + ":spot:" + settlement
	return result
}

func perp(code, venue, settlement string) Row {
	operations := Operations{
		Place: true, Cancel: true, OpenOrders: true, OrderHistory: true, Fills: true,
		Balances: true, Account: true, Positions: true, Funding: true,
		PrivateStream: true,
	}
	return row(code, venue, "perp", settlement, operations)
}

func isPromoted(code string) bool {
	switch code {
	case "BNS", "BNP", "OXS", "OXP", "LIP", "HLS", "HLP",
		"BYS", "BYU", "BYC", "BGS", "BGU", "BGC",
		"GTS", "GTU", "ATS", "ATP", "NDS", "NDP":
		return true
	default:
		return false
	}
}

func exactLookupStrategy(code string) string {
	switch code {
	case "LIS", "LIP":
		return "active_plus_first_inactive_page_scan_with_unconfirmed_absence"
	case "BNS", "BNP", "OXS", "OXP", "HLS", "HLP",
		"BYS", "BYU", "BYC", "BGS", "BGU", "BGC",
		"GTS", "GTU", "ATS", "ATP", "NDS", "NDP":
		return "client_or_venue_order_id_exact_readback"
	default:
		return "requires_fixture_promotion"
	}
}

func row(code, venue, product, settlement string, operations Operations) Row {
	promoted := isPromoted(code)
	exactLookup := "requires_fixture_promotion"
	exactFixture := "required_before_write_promotion"
	writeReadyDefault := false
	if promoted {
		exactLookup = "promoted"
		exactFixture = "passed"
		writeReadyDefault = true
	} else if code == "LIS" {
		exactLookup = "fixture_only"
		exactFixture = "passed"
	}

	id := venue + ":" + product
	if settlement != "" {
		id += ":" + settlement
	}
	promotionEvidenceRequired := "fixture_and_demo_testnet_exact_lookup"
	if code == "LIS" {
		promotionEvidenceRequired = "demo_testnet_two_sided_liquidity_and_exact_lookup"
	}
	return Row{
		Code:                           code,
		RowID:                          id,
		Venue:                          venue,
		Product:                        product,
		Settlement:                     settlement,
		WriteReadyDefault:              writeReadyDefault,
		ExactLookup:                    exactLookup,
		ExactLookupStrategy:            exactLookupStrategy(code),
		ExactFixture:                   exactFixture,
		PromotionEvidenceRequired:      promotionEvidenceRequired,
		HistoryPaginationProvesAbsence: false,
		Operations:                     operations,
	}
}

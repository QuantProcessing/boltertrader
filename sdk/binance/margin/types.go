package margin

// MarginAccount represents the response from /sapi/v1/margin/account
type MarginAccount struct {
	BorrowEnabled       bool        `json:"borrowEnabled"`
	MarginLevel         string      `json:"marginLevel"`
	TotalAssetOfBtc     string      `json:"totalAssetOfBtc"`
	TotalLiabilityOfBtc string      `json:"totalLiabilityOfBtc"`
	TotalNetAssetOfBtc  string      `json:"totalNetAssetOfBtc"`
	TradeEnabled        bool        `json:"tradeEnabled"`
	TransferEnabled     bool        `json:"transferEnabled"`
	UserAssets          []UserAsset `json:"userAssets"`
}

type UserAsset struct {
	Asset    string `json:"asset"`
	Borrowed string `json:"borrowed"`
	Free     string `json:"free"`
	Interest string `json:"interest"`
	Locked   string `json:"locked"`
	NetAsset string `json:"netAsset"`
}

type IsolatedMarginAsset struct {
	Asset         string `json:"asset"`
	BorrowEnabled bool   `json:"borrowEnabled"`
	Borrowed      string `json:"borrowed"`
	Free          string `json:"free"`
	Interest      string `json:"interest"`
	Locked        string `json:"locked"`
	NetAsset      string `json:"netAsset"`
	NetAssetOfBtc string `json:"netAssetOfBtc"`
	RepayEnabled  bool   `json:"repayEnabled"`
	TotalAsset    string `json:"totalAsset"`
}

type IsolatedMarginSymbol struct {
	BaseAsset         IsolatedMarginAsset `json:"baseAsset"`
	QuoteAsset        IsolatedMarginAsset `json:"quoteAsset"`
	Symbol            string              `json:"symbol"`
	IsolatedCreated   bool                `json:"isolatedCreated"`
	Enabled           bool                `json:"enabled"`
	MarginLevel       string              `json:"marginLevel"`
	MarginLevelStatus string              `json:"marginLevelStatus"`
	MarginRatio       string              `json:"marginRatio"`
	IndexPrice        string              `json:"indexPrice"`
	LiquidatePrice    string              `json:"liquidatePrice"`
	LiquidateRate     string              `json:"liquidateRate"`
	TradeEnabled      bool                `json:"tradeEnabled"`
}

type IsolatedMarginAccount struct {
	Assets              []IsolatedMarginSymbol `json:"assets"`
	TotalAssetOfBtc     string                 `json:"totalAssetOfBtc"`
	TotalLiabilityOfBtc string                 `json:"totalLiabilityOfBtc"`
	TotalNetAssetOfBtc  string                 `json:"totalNetAssetOfBtc"`
}

// TransactionResult represents the response for Borrow/Repay
type TransactionResult struct {
	TranId int64 `json:"tranId"`
}

// MarginOrder represents the response from order placement
type MarginOrder struct {
	Symbol              string `json:"symbol"`
	OrderID             int64  `json:"orderId"`
	ClientOrderID       string `json:"clientOrderId"`
	TransactTime        int64  `json:"transactTime"`
	Price               string `json:"price"`
	OrigQty             string `json:"origQty"`
	ExecutedQty         string `json:"executedQty"`
	CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
	Status              string `json:"status"`
	TimeInForce         string `json:"timeInForce"`
	Type                string `json:"type"`
	Side                string `json:"side"`
	IsIsolated          bool   `json:"isIsolated"`
}

type OrderResponseFull struct {
	MarginOrder
	Fills []struct {
		Price           string `json:"price"`
		Qty             string `json:"qty"`
		Commission      string `json:"commission"`
		CommissionAsset string `json:"commissionAsset"`
	} `json:"fills"`
}

type Trade struct {
	Symbol          string `json:"symbol"`
	ID              int64  `json:"id"`
	OrderID         int64  `json:"orderId"`
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	QuoteQty        string `json:"quoteQty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	Time            int64  `json:"time"`
	IsBuyer         bool   `json:"isBuyer"`
	IsMaker         bool   `json:"isMaker"`
	IsBestMatch     bool   `json:"isBestMatch"`
	IsIsolated      bool   `json:"isIsolated"`
}

type PlaceOrderParams struct {
	Symbol           string
	Side             string
	Type             string
	TimeInForce      string // Optional
	Quantity         float64
	QuoteOrderQty    float64 // Optional
	Price            float64 // Optional
	NewClientOrderID string  // Optional
	SideEffectType   string  // NO_SIDE_EFFECT, MARGIN_BUY, AUTO_REPAY
	IsIsolated       bool
}

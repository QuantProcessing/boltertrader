package subaccount

type AssetsResponse struct {
	Balances []AssetBalance `json:"balances"`
}

type AssetBalance struct {
	Asset       string `json:"asset"`
	Free        string `json:"free"`
	Locked      string `json:"locked"`
	Freeze      string `json:"freeze"`
	Withdrawing string `json:"withdrawing"`
}

type SpotAssetsSummary struct {
	TotalCount                  int                    `json:"totalCount"`
	MasterAccountTotalAsset     string                 `json:"masterAccountTotalAsset"`
	SpotSubUserAssetBTCVOList   []SpotSubAccountAsset  `json:"spotSubUserAssetBtcVoList"`
	SpotSubUserAssetBtcVOList   []SpotSubAccountAsset  `json:"spotSubUserAssetBTCVoList"`
	SpotSubUserAssetBTCVoListV2 []SpotSubAccountAsset  `json:"spotSubUserAssetBtcVoListV2"`
	Extra                       map[string]interface{} `json:"-"`
}

type SpotSubAccountAsset struct {
	Email      string `json:"email"`
	TotalAsset string `json:"totalAsset"`
}

type FuturesTransferResponse struct {
	TxnID string `json:"txnId"`
}

type UniversalTransferRequest struct {
	FromEmail       string
	ToEmail         string
	FromAccountType string
	ToAccountType   string
	ClientTranID    string
	Symbol          string
	Asset           string
	Amount          string
}

type UniversalTransferResponse struct {
	TranID       int64  `json:"tranId"`
	ClientTranID string `json:"clientTranId"`
}

package perp

import "encoding/json"

// WebSocket Event Types

type StringOrNumber string

func (v *StringOrNumber) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*v = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*v = StringOrNumber(text)
		return nil
	}
	*v = StringOrNumber(string(data))
	return nil
}

func (v StringOrNumber) String() string {
	return string(v)
}

type WsMetadataEvent struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Content struct {
		DataType string `json:"dataType"`
		Channel  string `json:"channel"`
		Data     []struct {
			Coin     []Coin     `json:"coin"`
			Contract []Contract `json:"contract"`
		} `json:"data"`
	} `json:"content"`
}

type WsTickerEvent struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Content struct {
		DataType string `json:"dataType"`
		Channel  string `json:"channel"`
		Data     []struct {
			ContractId         string `json:"contractId"`
			PriceChange        string `json:"priceChange"`
			PriceChangePercent string `json:"priceChangePercent"`
			Trades             string `json:"trades"`
			Size               string `json:"size"`
			Value              string `json:"value"`
			High               string `json:"high"`
			Low                string `json:"low"`
			Open               string `json:"open"`
			Close              string `json:"close"`
			HighTime           string `json:"highTime"`
			LowTime            string `json:"lowTime"`
			StartTime          string `json:"startTime"`
			EndTime            string `json:"endTime"`
			LastPrice          string `json:"lastPrice"`
		} `json:"data"`
	} `json:"content"`
}

type WsKlineEvent struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Content struct {
		DataType string `json:"dataType"`
		Channel  string `json:"channel"`
		Data     []struct {
			KlineId       string `json:"klineId"`
			ContractId    string `json:"contractId"`
			KlineType     string `json:"klineType"`
			KlineTime     string `json:"klineTime"`
			Trades        string `json:"trades"`
			Size          string `json:"size"`
			Value         string `json:"value"`
			High          string `json:"high"`
			Low           string `json:"low"`
			Open          string `json:"open"`
			Close         string `json:"close"`
			MakerBuySize  string `json:"makerBuySize"`
			MakerBuyValue string `json:"makerBuyValue"`
		} `json:"data"`
	} `json:"content"`
}

// WsDepthEvent matches the documentation
type WsDepthEvent struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Content struct {
		DataType string `json:"dataType"`
		Channel  string `json:"channel"`
		Data     []struct {
			StartVersion string `json:"startVersion"`
			EndVersion   string `json:"endVersion"`
			Level        int    `json:"level"`
			ContractId   string `json:"contractId"`
			DepthType    string `json:"depthType"`
			Bids         []Bids `json:"bids"`
			Asks         []Asks `json:"asks"`
		} `json:"data"`
	} `json:"content"`
}

type Asks struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type Bids struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

// WsTradeEvent matches the documentation
type WsTradeEvent struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Content struct {
		DataType string `json:"dataType"`
		Channel  string `json:"channel"`
		Data     []struct {
			TicketId       string `json:"ticketId"`
			Time           string `json:"time"`
			Price          string `json:"price"`
			Size           string `json:"size"`
			Value          string `json:"value"`
			TakerOrderId   string `json:"takerOrderId"`
			MakerOrderId   string `json:"makerOrderId"`
			TakerAccountId string `json:"takerAccountId"`
			MakerAccountId string `json:"makerAccountId"`
			ContractId     string `json:"contractId"`
			IsBestMatch    bool   `json:"isBestMatch"`
			IsBuyerMaker   bool   `json:"isBuyerMaker"`
		} `json:"data"`
	} `json:"content"`
}

type WsFundingRateEvent struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Content struct {
		DataType string `json:"dataType"`
		Channel  string `json:"channel"`
		Data     []struct {
			ContractId      StringOrNumber `json:"contractId"`
			FundingRate     string         `json:"fundingRate"`
			NextFundingTime StringOrNumber `json:"nextFundingTime"`
			Timestamp       StringOrNumber `json:"timestamp"`
		} `json:"data"`
	} `json:"content"`
}

type WsUserDataEvent struct {
	Type    string `json:"type"`
	Content struct {
		Event   string `json:"event"`
		Version int64  `json:"version"`
		Data    struct {
			Account               []AccountInfo     `json:"account"`
			Collateral            []Collateral      `json:"collateral"`
			CollateralTransaction []json.RawMessage `json:"collateralTransaction"`
			Position              []PositionInfo    `json:"position"`
			PositionTransaction   []json.RawMessage `json:"positionTransaction"`
			Deposit               []json.RawMessage `json:"deposit"`
			Withdraw              []json.RawMessage `json:"withdraw"`
			TransferIn            []json.RawMessage `json:"transferIn"`
			TransferOut           []json.RawMessage `json:"transferOut"`
			Order                 []Order           `json:"order"`
			OrderFillTransaction  []json.RawMessage `json:"orderFillTransaction"`
		} `json:"data"`
	} `json:"content"`
}

type OrderBookDepth int

const (
	OrderBookDepth15  OrderBookDepth = 15
	OrderBookDepth200 OrderBookDepth = 200
)

type KlineInterval string

const (
	KlineInterval1m  KlineInterval = "MINUTE_1"
	KlineInterval5m  KlineInterval = "MINUTE_5"
	KlineInterval15m KlineInterval = "MINUTE_15"
	KlineInterval30m KlineInterval = "MINUTE_30"
	KlineInterval1h  KlineInterval = "HOUR_1"
	KlineInterval2h  KlineInterval = "HOUR_2"
	KlineInterval4h  KlineInterval = "HOUR_4"
	KlineInterval6h  KlineInterval = "HOUR_6"
	KlineInterval8h  KlineInterval = "HOUR_8"
	KlineInterval12h KlineInterval = "HOUR_12"
	KlineInterval1d  KlineInterval = "DAY_1"
	KlineInterval1w  KlineInterval = "WEEK_1"
	KlineInterval1M  KlineInterval = "MONTH_1"
)

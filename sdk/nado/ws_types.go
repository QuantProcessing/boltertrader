package nado

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// WebSocket Message Types

type WsMessage struct {
	Type      string          `json:"type"` // Used for inference if available
	Channel   string          `json:"channel,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Status    string          `json:"status,omitempty"`
	Error     string          `json:"error,omitempty"`
	ErrorCode int             `json:"error_code,omitempty"`
	// Auth specific
	Method string `json:"method,omitempty"`
	Id     int64  `json:"id,omitempty"`
}

type WsResponse struct {
	Id          *int64          `json:"id,omitempty"`
	Signature   *string         `json:"signature,omitempty"`
	Status      string          `json:"status"`
	Error       string          `json:"error,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	RequestType string          `json:"request_type,omitempty"`
}

type WsAuthRequest struct {
	Method    string       `json:"method"` // "authenticate"
	Id        int64        `json:"id"`
	Tx        TxStreamAuth `json:"tx"`
	Signature string       `json:"signature"`
}

type SubscriptionRequest struct {
	Method string       `json:"method"` // "subscribe" or "unsubscribe"
	Stream StreamParams `json:"stream"`
	Id     int64        `json:"id"`
}

type StreamParams struct {
	Type        string `json:"type"`
	ProductId   *int64 `json:"product_id,omitempty"`
	Subaccount  string `json:"subaccount,omitempty"`
	Granularity int32  `json:"granularity,omitempty"`
}

// Data Payloads

type OrderUpdate struct {
	Type      string            `json:"type"`
	Timestamp string            `json:"timestamp"`
	ProductId int64             `json:"product_id"`
	Digest    string            `json:"digest"`
	Id        string            `json:"id,omitempty"`
	Amount    string            `json:"amount"`
	Reason    OrderUpdateReason `json:"reason"`
}

// Use Ticker struct from types.go for BestBidOffer if compatible
// Use Trade struct from types.go
// Use OrderBook struct from types.go

type Fill struct {
	Type          string `json:"type"`
	Timestamp     string `json:"timestamp"`
	ProductId     int64  `json:"product_id"`
	Subaccount    string `json:"subaccount"`
	OrderDigest   string `json:"order_digest"`
	FilledQty     string `json:"filled_qty"`
	RemainingQty  string `json:"remaining_qty"`
	OriginalQty   string `json:"original_qty"`
	Price         string `json:"price"`
	IsTaker       bool   `json:"is_taker"`
	IsBid         bool   `json:"is_bid"`
	Fee           string `json:"fee"`
	SubmissionIdx string `json:"submission_idx"`
	Id            string `json:"id,omitempty"`
	Appendix      string `json:"appendix"`
}

func (f *Fill) UnmarshalJSON(data []byte) error {
	var aux struct {
		Type          string          `json:"type"`
		Timestamp     string          `json:"timestamp"`
		ProductId     int64           `json:"product_id"`
		Subaccount    string          `json:"subaccount"`
		OrderDigest   string          `json:"order_digest"`
		FilledQty     string          `json:"filled_qty"`
		RemainingQty  string          `json:"remaining_qty"`
		OriginalQty   string          `json:"original_qty"`
		Price         string          `json:"price"`
		IsTaker       bool            `json:"is_taker"`
		IsBid         bool            `json:"is_bid"`
		Fee           string          `json:"fee"`
		SubmissionIdx json.RawMessage `json:"submission_idx"`
		Id            json.RawMessage `json:"id,omitempty"`
		Appendix      string          `json:"appendix"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	submissionIdx, err := decodeStringOrNumber(aux.SubmissionIdx, "submission_idx")
	if err != nil {
		return err
	}
	id, err := decodeStringOrNumber(aux.Id, "id")
	if err != nil {
		return err
	}
	*f = Fill{
		Type:          aux.Type,
		Timestamp:     aux.Timestamp,
		ProductId:     aux.ProductId,
		Subaccount:    aux.Subaccount,
		OrderDigest:   aux.OrderDigest,
		FilledQty:     aux.FilledQty,
		RemainingQty:  aux.RemainingQty,
		OriginalQty:   aux.OriginalQty,
		Price:         aux.Price,
		IsTaker:       aux.IsTaker,
		IsBid:         aux.IsBid,
		Fee:           aux.Fee,
		SubmissionIdx: submissionIdx,
		Id:            id,
		Appendix:      aux.Appendix,
	}
	return nil
}

func decodeStringOrNumber(raw json.RawMessage, field string) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("nado ws %s: %w", field, err)
		}
		return value, nil
	}
	for _, b := range raw {
		if b < '0' || b > '9' {
			return "", fmt.Errorf("nado ws %s must be a string or unsigned integer", field)
		}
	}
	return string(raw), nil
}

type PositionChange struct {
	Type         string               `json:"type"`
	Timestamp    string               `json:"timestamp"`
	ProductId    int64                `json:"product_id"`
	Subaccount   string               `json:"subaccount"`
	Amount       string               `json:"amount"`
	VQuoteAmount string               `json:"v_quote_amount"`
	Reason       PositionChangeReason `json:"reason"`
	Isolated     bool                 `json:"isolated"`
}

type FundingPayment struct {
	Type                      string `json:"type"`
	Timestamp                 string `json:"timestamp"`
	ProductId                 int64  `json:"product_id"`
	PaymentAmount             string `json:"payment_amount"`
	OpenInterest              string `json:"open_interest"`
	CumulativeFundingLongX18  string `json:"cumulative_funding_long_x18"`
	CumulativeFundingShortX18 string `json:"cumulative_funding_short_x18"`
	Dt                        string `json:"dt"`
}

type FundingRate struct {
	Type           string `json:"type"`
	Timestamp      string `json:"timestamp"`
	ProductId      int64  `json:"product_id"`
	FundingRateX18 string `json:"funding_rate_x18"`
	UpdateTime     string `json:"update_time"`
}

type Candlestick struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	ProductId   int64  `json:"product_id"`
	Granularity int32  `json:"granularity"`
	OpenX18     string `json:"open_x18"`
	HighX18     string `json:"high_x18"`
	LowX18      string `json:"low_x18"`
	CloseX18    string `json:"close_x18"`
	Volume      string `json:"volume"`
}

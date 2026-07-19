package exchange

import "github.com/shopspring/decimal"

type OrderAckState string

const (
	AckAcceptedPending   OrderAckState = "accepted_pending"
	AckResting           OrderAckState = "resting"
	AckPartiallyFilled   OrderAckState = "partially_filled"
	AckImmediatelyFilled OrderAckState = "immediately_filled"
	AckCanceled          OrderAckState = "canceled"
	AckRejected          OrderAckState = "rejected"
	AckAmbiguous         OrderAckState = "ambiguous"
)

type OrderOperation string

const (
	OrderOperationPlace  OrderOperation = "place"
	OrderOperationCancel OrderOperation = "cancel"
)

type OrderAcknowledgement struct {
	Venue            Venue           `json:"venue,omitempty"`
	Product          Product         `json:"product,omitempty"`
	Operation        OrderOperation  `json:"operation,omitempty"`
	State            OrderAckState   `json:"state,omitempty"`
	Instrument       string          `json:"instrument,omitempty"`
	OrderType        OrderType       `json:"order_type,omitempty"`
	OrderID          string          `json:"order_id,omitempty"`
	ClientOrderID    string          `json:"client_order_id,omitempty"`
	TransactionHash  string          `json:"transaction_hash,omitempty"`
	FilledQuantity   decimal.Decimal `json:"filled_quantity"`
	AverageFillPrice OptionalDecimal `json:"average_fill_price"`
	VenueCode        string          `json:"venue_code,omitempty"`
	VenueMessage     string          `json:"venue_message,omitempty"`
}

func (ack OrderAcknowledgement) Validate() error {
	if ack.Venue == "" || ack.Product == "" || ack.Operation == "" ||
		ack.State == "" || ack.Instrument == "" {
		return ackMalformed("missing acknowledgement metadata")
	}

	switch ack.State {
	case AckAcceptedPending:
		if ack.OrderID == "" && ack.ClientOrderID == "" && ack.TransactionHash == "" {
			return ackMalformed("accepted-pending acknowledgement has no order reference")
		}
	case AckResting:
		if ack.OrderType == OrderTypeMarket {
			return ackMalformed("market order acknowledgement cannot be resting")
		}
		if ack.OrderID == "" {
			return ackMalformed("final exchange acknowledgement has no order id")
		}
	case AckPartiallyFilled:
		if ack.OrderID == "" {
			return ackMalformed("partial-fill acknowledgement has no order id")
		}
		if !ack.FilledQuantity.IsPositive() {
			return ackMalformed("partial-fill acknowledgement has no positive filled quantity")
		}
	case AckImmediatelyFilled:
		if ack.OrderID == "" {
			return ackMalformed("final exchange acknowledgement has no order id")
		}
	case AckCanceled:
		if ack.OrderID == "" && ack.ClientOrderID == "" && ack.TransactionHash == "" {
			return ackMalformed("canceled acknowledgement has no order reference")
		}
	case AckRejected:
		if ack.VenueCode == "" && ack.VenueMessage == "" {
			return ackMalformed("rejected acknowledgement has no venue rejection details")
		}
	case AckAmbiguous:
		if ack.OrderID == "" && ack.ClientOrderID == "" && ack.TransactionHash == "" {
			return ackMalformed("ambiguous acknowledgement has no correlation reference")
		}
	default:
		return ackMalformed("unknown acknowledgement state")
	}
	return nil
}

func ackMalformed(message string) error {
	return NewError(KindMalformedResponse, ErrorDetails{
		Operation:   "OrderAcknowledgement.Validate",
		SafeMessage: message,
	})
}

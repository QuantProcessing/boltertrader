package hyperliquid

// --- Action Wire Types (Shared between Perp and Spot) ---

// create order
type OrderWire struct {
	Asset      int           `json:"a"           msgpack:"a"`           // 1st
	IsBuy      bool          `json:"b"           msgpack:"b"`           // 2nd
	LimitPx    string        `json:"p"           msgpack:"p"`           // 3rd
	Size       string        `json:"s"           msgpack:"s"`           // 4th
	ReduceOnly bool          `json:"r"           msgpack:"r"`           // 5th
	OrderType  OrderTypeWire `json:"t"           msgpack:"t"`           // 6th
	Cloid      *string       `json:"c,omitempty" msgpack:"c,omitempty"` // 7th (optional)
}

type OrderTypeWire struct {
	Limit   *OrderTypeWireLimit   `json:"limit,omitempty"   msgpack:"limit,omitempty"`
	Trigger *OrderTypeWireTrigger `json:"trigger,omitempty" msgpack:"trigger,omitempty"`
}

type OrderTypeWireLimit struct {
	Tif Tif `json:"tif" msgpack:"tif"`
}

type OrderTypeWireTrigger struct {
	IsMarket  bool   `json:"isMarket"  msgpack:"isMarket"`  // 1st
	TriggerPx string `json:"triggerPx" msgpack:"triggerPx"` // 2nd - Must be string for msgpack serialization
	Tpsl      Tpsl   `json:"tpsl"      msgpack:"tpsl"`      // 3rd - "tp" or "sl"
}

type CreateOrderAction struct {
	Type     string      `json:"type" msgpack:"type"`
	Orders   []OrderWire `json:"orders" msgpack:"orders"`
	Grouping string      `json:"grouping,omitempty" msgpack:"grouping,omitempty"`
	Builder  *Builder    `json:"builder,omitempty" msgpack:"builder,omitempty"`
}

type Builder struct {
	Builder string `json:"b" msgpack:"b"`
	Fee     int    `json:"f" msgpack:"f"`
}

// cancel order
type CancelOrderAction struct {
	Type    string            `json:"type" msgpack:"type"`
	Cancels []CancelOrderWire `json:"cancels" msgpack:"cancels"`
}

type CancelOrderWire struct {
	Asset   int   `json:"a" msgpack:"a"`
	OrderId int64 `json:"o" msgpack:"o"`
}

// modify order
type ModifyOrderAction struct {
	Type  string    `json:"type,omitempty" msgpack:"type,omitempty"`
	Oid   any       `json:"oid"            msgpack:"oid"`
	Order OrderWire `json:"order"          msgpack:"order"`
}

type BatchModifyAction struct {
	Type     string              `json:"type"     msgpack:"type"`
	Modifies []ModifyOrderAction `json:"modifies" msgpack:"modifies"`
}

// update leverage
type UpdateLeverageAction struct {
	Type     string `json:"type"     msgpack:"type"`
	Asset    int    `json:"asset"    msgpack:"asset"`
	IsCross  bool   `json:"isCross"  msgpack:"isCross"`
	Leverage int    `json:"leverage" msgpack:"leverage"`
}

// update isolated margin
type UpdateIsolatedMarginAction struct {
	Type  string `json:"type"  msgpack:"type"`
	Asset int    `json:"asset" msgpack:"asset"`
	IsBuy bool   `json:"isBuy" msgpack:"isBuy"`
	Ntli  int    `json:"ntli"  msgpack:"ntli"`
}

type UsdClassTransferAction struct {
	Type   string  `json:"type"   msgpack:"type"`
	Amount float64 `json:"amount" msgpack:"amount"`
	ToPerp bool    `json:"toPerp" msgpack:"toPerp"`
}

type UserOutcomeAction struct {
	Type          string                      `json:"type" msgpack:"type"`
	SplitOutcome  *OutcomeSplitAction         `json:"splitOutcome,omitempty" msgpack:"splitOutcome,omitempty"`
	MergeOutcome  *OutcomeMergeAction         `json:"mergeOutcome,omitempty" msgpack:"mergeOutcome,omitempty"`
	MergeQuestion *OutcomeMergeQuestionAction `json:"mergeQuestion,omitempty" msgpack:"mergeQuestion,omitempty"`
	NegateOutcome *OutcomeNegateAction        `json:"negateOutcome,omitempty" msgpack:"negateOutcome,omitempty"`
}

type OutcomeSplitAction struct {
	Outcome int    `json:"outcome" msgpack:"outcome"`
	Amount  string `json:"amount"  msgpack:"amount"`
}

type OutcomeMergeAction struct {
	Outcome int     `json:"outcome" msgpack:"outcome"`
	Amount  *string `json:"amount"  msgpack:"amount"`
}

type OutcomeMergeQuestionAction struct {
	Question int     `json:"question" msgpack:"question"`
	Amount   *string `json:"amount"   msgpack:"amount"`
}

type OutcomeNegateAction struct {
	Question int    `json:"question" msgpack:"question"`
	Outcome  int    `json:"outcome"  msgpack:"outcome"`
	Amount   string `json:"amount"   msgpack:"amount"`
}

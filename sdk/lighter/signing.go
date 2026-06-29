package lighter

import (
	g "github.com/elliottech/poseidon_crypto/field/goldilocks"
	p2 "github.com/elliottech/poseidon_crypto/hash/poseidon2_goldilocks"
)

// CreateOrderInfo contains information needed to sign a create order transaction
type CreateOrderInfo struct {
	Nonce            int64
	ExpiredAt        int64
	AccountIndex     int64
	ApiKeyIndex      uint32
	MarketIndex      uint32
	ClientOrderIndex int64
	BaseAmount       int64
	Price            uint32
	IsAsk            uint32
	Type             uint32
	TimeInForce      uint32
	ReduceOnly       uint32
	TriggerPrice     uint32
	OrderExpiry      int64

	// For WebSocket operations
	Sig        []byte `json:"Sig,omitempty"`
	SignedHash string `json:"-"`
}

// CancelOrderInfo contains information needed to sign a cancel order transaction
type CancelOrderInfo struct {
	Nonce        int64
	ExpiredAt    int64
	AccountIndex int64
	ApiKeyIndex  uint32
	MarketIndex  uint32
	Index        int64

	// For WebSocket operations
	Sig        []byte `json:"Sig,omitempty"`
	SignedHash string `json:"-"`
}

// ModifyOrderInfo contains information needed to sign a modify order transaction
type ModifyOrderInfo struct {
	Nonce        int64
	ExpiredAt    int64
	AccountIndex int64
	ApiKeyIndex  uint32
	MarketIndex  uint32
	Index        int64
	BaseAmount   int64
	Price        uint32
	TriggerPrice uint32

	// For WebSocket operations
	Sig        []byte `json:"Sig,omitempty"`
	SignedHash string `json:"-"`
}

// HashCreateOrder computes the hash for a CreateOrder transaction.
func HashCreateOrder(lighterChainId uint32, info *CreateOrderInfo) ([]byte, error) {
	elems := make([]g.Element, 0, 16)
	elems = append(elems, g.FromUint32(lighterChainId))
	elems = append(elems, g.FromUint32(TxTypeCreateOrder))
	elems = append(elems, g.FromInt64(info.Nonce))
	elems = append(elems, g.FromInt64(info.ExpiredAt))

	elems = append(elems, g.FromInt64(info.AccountIndex))
	elems = append(elems, g.FromUint32(uint32(info.ApiKeyIndex)))
	elems = append(elems, g.FromUint32(uint32(info.MarketIndex)))
	elems = append(elems, g.FromInt64(info.ClientOrderIndex))
	elems = append(elems, g.FromInt64(info.BaseAmount))
	elems = append(elems, g.FromUint32(info.Price))
	elems = append(elems, g.FromUint32(uint32(info.IsAsk)))
	elems = append(elems, g.FromUint32(uint32(info.Type)))
	elems = append(elems, g.FromUint32(uint32(info.TimeInForce)))
	elems = append(elems, g.FromUint32(uint32(info.ReduceOnly)))
	elems = append(elems, g.FromUint32(info.TriggerPrice))
	elems = append(elems, g.FromInt64(info.OrderExpiry))

	return p2.HashToQuinticExtension(elems).ToLittleEndianBytes(), nil
}

// HashCancelOrder computes the hash for a CancelOrder transaction.
func HashCancelOrder(lighterChainId uint32, info *CancelOrderInfo) ([]byte, error) {
	elems := make([]g.Element, 0, 7)

	elems = append(elems, g.FromUint32(lighterChainId))
	elems = append(elems, g.FromUint32(TxTypeCancelOrder))
	elems = append(elems, g.FromInt64(info.Nonce))
	elems = append(elems, g.FromInt64(info.ExpiredAt))

	elems = append(elems, g.FromInt64(info.AccountIndex))
	elems = append(elems, g.FromUint32(uint32(info.ApiKeyIndex)))
	elems = append(elems, g.FromUint32(uint32(info.MarketIndex)))
	elems = append(elems, g.FromInt64(info.Index))

	return p2.HashToQuinticExtension(elems).ToLittleEndianBytes(), nil
}

// HashModifyOrder computes the hash for a ModifyOrder transaction.
func HashModifyOrder(lighterChainId uint32, info *ModifyOrderInfo) ([]byte, error) {
	elems := make([]g.Element, 0, 13)
	elems = append(elems, g.FromUint32(lighterChainId))
	elems = append(elems, g.FromUint32(TxTypeModifyOrder))
	elems = append(elems, g.FromInt64(info.Nonce))
	elems = append(elems, g.FromInt64(info.ExpiredAt))

	elems = append(elems, g.FromInt64(info.AccountIndex))
	elems = append(elems, g.FromUint32(uint32(info.ApiKeyIndex)))
	elems = append(elems, g.FromUint32(uint32(info.MarketIndex)))
	elems = append(elems, g.FromInt64(info.Index))
	elems = append(elems, g.FromInt64(info.BaseAmount))
	elems = append(elems, g.FromUint32(info.Price))
	elems = append(elems, g.FromUint32(info.TriggerPrice))

	return p2.HashToQuinticExtension(elems).ToLittleEndianBytes(), nil
}

// UpdateLeverageInfo contains information needed to sign an update leverage transaction
type UpdateLeverageInfo struct {
	Nonce                 int64
	ExpiredAt             int64
	AccountIndex          int64
	ApiKeyIndex           uint32
	MarketIndex           uint32
	InitialMarginFraction uint16
	MarginMode            uint8

	// For WebSocket operations
	Sig        []byte `json:"Sig,omitempty"`
	SignedHash string `json:"-"`
}

// HashUpdateLeverage computes the hash for an UpdateLeverage transaction.
func HashUpdateLeverage(lighterChainId uint32, info *UpdateLeverageInfo) ([]byte, error) {
	elems := make([]g.Element, 0, 8)
	elems = append(elems, g.FromUint32(lighterChainId))
	elems = append(elems, g.FromUint32(TxTypeUpdateLeverage))
	elems = append(elems, g.FromInt64(info.Nonce))
	elems = append(elems, g.FromInt64(info.ExpiredAt))

	elems = append(elems, g.FromInt64(info.AccountIndex))
	elems = append(elems, g.FromUint32(uint32(info.ApiKeyIndex)))
	elems = append(elems, g.FromUint32(uint32(info.MarketIndex)))
	elems = append(elems, g.FromUint32(uint32(info.InitialMarginFraction)))
	elems = append(elems, g.FromUint32(uint32(info.MarginMode)))

	return p2.HashToQuinticExtension(elems).ToLittleEndianBytes(), nil
}

// CancelAllOrdersInfo contains information needed to sign a cancel all orders transaction
// Based on official SDK: https://github.com/elliottech/lighter-go/blob/main/types/txtypes/cancel_all_orders.go
type CancelAllOrdersInfo struct {
	Nonce        int64
	ExpiredAt    int64
	AccountIndex int64
	ApiKeyIndex  uint32
	TimeInForce  uint32 // 0=ImmediateCancelAll, 1=ScheduledCancelAll, 2=AbortScheduledCancelAll
	Time         int64  // 0 for Immediate, scheduled time for Scheduled, 0 for Abort

	// For WebSocket operations
	Sig        []byte `json:"Sig,omitempty"`
	SignedHash string `json:"-"`
}

// HashCancelAllOrders computes the hash for a CancelAllOrders transaction.
// Based on official SDK hash order: chainId, txType, nonce, expiredAt, accountIndex, apiKeyIndex, timeInForce, time
func HashCancelAllOrders(lighterChainId uint32, info *CancelAllOrdersInfo) ([]byte, error) {
	elems := make([]g.Element, 0, 8)
	elems = append(elems, g.FromUint32(lighterChainId))
	elems = append(elems, g.FromUint32(TxTypeCancelAllOrders))
	elems = append(elems, g.FromInt64(info.Nonce))
	elems = append(elems, g.FromInt64(info.ExpiredAt))

	elems = append(elems, g.FromInt64(info.AccountIndex))
	elems = append(elems, g.FromUint32(info.ApiKeyIndex))
	elems = append(elems, g.FromUint32(info.TimeInForce))
	elems = append(elems, g.FromInt64(info.Time))

	return p2.HashToQuinticExtension(elems).ToLittleEndianBytes(), nil
}

type L2UpdateMarginTxInfo struct {
	AccountIndex int64
	ApiKeyIndex  uint8

	MarketIndex int16
	USDCAmount  int64
	Direction   uint8

	ExpiredAt int64
	Nonce     int64

	// For WebSocket operations
	Sig        []byte `json:"Sig,omitempty"`
	SignedHash string `json:"-"`
}

func (txInfo *L2UpdateMarginTxInfo) Hash(lighterChainId uint32, extra ...g.Element) (msgHash []byte, err error) {
	elems := make([]g.Element, 0, 10)

	elems = append(elems, g.FromUint32(lighterChainId))
	elems = append(elems, g.FromUint32(TxTypeL2UpdateMargin))
	elems = append(elems, g.FromInt64(txInfo.Nonce))
	elems = append(elems, g.FromInt64(txInfo.ExpiredAt))

	elems = append(elems, g.FromInt64(txInfo.AccountIndex))
	elems = append(elems, g.FromUint32(uint32(txInfo.ApiKeyIndex)))
	elems = append(elems, g.FromInt64(int64(txInfo.MarketIndex)))
	elems = append(elems, g.FromUint64(uint64(txInfo.USDCAmount)&0xFFFFFFFF)) //nolint:gosec
	elems = append(elems, g.FromUint64(uint64(txInfo.USDCAmount)>>32))        //nolint:gosec
	elems = append(elems, g.FromUint32(uint32(txInfo.Direction)))

	return p2.HashToQuinticExtension(elems).ToLittleEndianBytes(), nil
}

type L2CreateGroupedOrdersTxInfo struct {
	AccountIndex int64
	ApiKeyIndex  uint8
	GroupingType uint8

	Orders []*CreateOrderInfo

	ExpiredAt int64
	Nonce     int64

	// For WebSocket operations
	Sig        []byte `json:"Sig,omitempty"`
	SignedHash string `json:"-"`
}

func (txInfo *L2CreateGroupedOrdersTxInfo) Hash(lighterChainId uint32, extra ...g.Element) (msgHash []byte, err error) {
	elems := make([]g.Element, 0, 11)
	elems = append(elems, g.FromUint32(lighterChainId))
	elems = append(elems, g.FromUint32(TxTypeL2CreateGroupedOrders))
	elems = append(elems, g.FromInt64(txInfo.Nonce))
	elems = append(elems, g.FromInt64(txInfo.ExpiredAt))

	elems = append(elems, g.FromInt64(txInfo.AccountIndex))
	elems = append(elems, g.FromUint32(uint32(txInfo.ApiKeyIndex)))
	elems = append(elems, g.FromUint32(uint32(txInfo.GroupingType)))

	aggregatedOrderHash := p2.EmptyHashOut()
	for index, order := range txInfo.Orders {
		orderHash := p2.HashNoPad([]g.Element{
			g.FromUint32(uint32(order.MarketIndex)),
			g.FromInt64(order.ClientOrderIndex),
			g.FromInt64(order.BaseAmount),
			g.FromUint32(order.Price),
			g.FromUint32(uint32(order.IsAsk)),
			g.FromUint32(uint32(order.Type)),
			g.FromUint32(uint32(order.TimeInForce)),
			g.FromUint32(uint32(order.ReduceOnly)),
			g.FromUint32(order.TriggerPrice),
			g.FromInt64(order.OrderExpiry),
		})
		if index == 0 {
			aggregatedOrderHash = orderHash
		} else {
			aggregatedOrderHash = p2.HashNToOne([]p2.HashOut{aggregatedOrderHash, orderHash})
		}
	}
	elems = append(elems, aggregatedOrderHash[:]...)

	return p2.HashToQuinticExtension(elems).ToLittleEndianBytes(), nil
}

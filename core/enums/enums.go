// Package enums defines the venue-neutral vocabularies the trading runtime and
// strategies speak in. Values are integer constants so the runtime can switch
// over them exhaustively with compiler help. Each type ships a String() method
// for logging only — these strings are NEVER used for venue I/O. All mapping
// between a venue's native representation and these enums lives exclusively in
// the adapter layer; this package depends on no SDK.
package enums

// OrderSide is the direction of an order or the aggressor side of a trade.
//
// Venue mapping (performed in adapters):
//   - Binance: "BUY" / "SELL"
//   - OKX:     "buy" / "sell"
//   - Hyperliquid: OrderWire.IsBuy bool (true -> SideBuy)
type OrderSide uint8

const (
	SideUnknown OrderSide = iota
	SideBuy
	SideSell
)

func (s OrderSide) String() string {
	switch s {
	case SideBuy:
		return "BUY"
	case SideSell:
		return "SELL"
	default:
		return "UNKNOWN"
	}
}

// OrderType is the normalized order kind. Trigger-based types (stop / take
// touched) carry their trigger price in model.OrderRequest.TriggerPrice.
// Trailing stops carry their optional activation price and callback offset in
// model.OrderRequest.ActivationPrice and TrailingOffsetBps.
//
// Venue mapping (performed in adapters):
//   - Binance: flat string "LIMIT" / "MARKET" / "STOP_MARKET" / "STOP" /
//     "TAKE_PROFIT_MARKET" / "TAKE_PROFIT" / "TRAILING_STOP_MARKET".
//   - OKX: ordType "limit" / "market" (post_only/fok/ioc are folded TIF — see
//     TimeInForce). Trigger families are separate algo endpoints:
//     "trigger" / "move_order_stop".
//   - Hyperliquid: a STRUCT OrderType{Limit{Tif} | Trigger{IsMarket,TriggerPx,Tpsl}}:
//     TypeLimit            -> Limit{Tif}
//     TypeMarket           -> Limit{Tif:Ioc} with an aggressive price (HL has
//     no native market type)
//     TypeStopMarket       -> Trigger{IsMarket:true,  Tpsl:"sl"}
//     TypeStopLimit        -> Trigger{IsMarket:false, Tpsl:"sl"}
//     TypeMarketIfTouched  -> Trigger{IsMarket:true,  Tpsl:"tp"}
//     TypeLimitIfTouched   -> Trigger{IsMarket:false, Tpsl:"tp"}
type OrderType uint8

const (
	TypeUnknown OrderType = iota
	TypeMarket
	TypeLimit
	TypeStopMarket
	TypeStopLimit
	TypeMarketIfTouched
	TypeLimitIfTouched
	TypeTrailingStopMarket

	// Deprecated aliases retained for older adapter/runtime callers. NT names
	// these "market-if-touched" and "limit-if-touched"; venue adapters still
	// translate them to Binance TAKE_PROFIT* strings where applicable.
	TypeTakeProfitMarket = TypeMarketIfTouched
	TypeTakeProfitLimit  = TypeLimitIfTouched
)

func (t OrderType) String() string {
	switch t {
	case TypeMarket:
		return "MARKET"
	case TypeLimit:
		return "LIMIT"
	case TypeStopMarket:
		return "STOP_MARKET"
	case TypeStopLimit:
		return "STOP_LIMIT"
	case TypeMarketIfTouched:
		return "MARKET_IF_TOUCHED"
	case TypeLimitIfTouched:
		return "LIMIT_IF_TOUCHED"
	case TypeTrailingStopMarket:
		return "TRAILING_STOP_MARKET"
	default:
		return "UNKNOWN"
	}
}

// TimeInForce normalizes order lifetime semantics. TifGTX is post-only
// (add-liquidity-only).
//
// Venue mapping (performed in adapters):
//   - Binance: explicit field GTC / IOC / FOK / GTX.
//   - OKX: NO separate field — folded into ordType ("limit"=GTC, "ioc"=IOC,
//     "fok"=FOK, "post_only"=GTX). Adapter splits/merges.
//   - Hyperliquid: Limit.Tif "Gtc" / "Ioc"; GTX -> "Alo". FOK is not
//     accepted by the venue wire API and adapters return ErrNotSupported.
//
// An unsupported (Type, TIF) combination on a given venue must surface as
// contract.ErrNotSupported in the adapter, never be silently coerced.
type TimeInForce uint8

const (
	TifUnknown TimeInForce = iota
	TifGTC
	TifIOC
	TifFOK
	TifGTX
)

func (t TimeInForce) String() string {
	switch t {
	case TifGTC:
		return "GTC"
	case TifIOC:
		return "IOC"
	case TifFOK:
		return "FOK"
	case TifGTX:
		return "GTX"
	default:
		return "UNKNOWN"
	}
}

// OrderStatus is the normalized lifecycle state of an order. StatusPendingNew
// (submitted locally, no venue ack yet) and StatusTriggered (stop/tp activated)
// are synthesized by adapters where the venue lacks an explicit state.
//
// Venue mapping (performed in adapters):
//   - Binance: NEW / PARTIALLY_FILLED / FILLED / CANCELED / EXPIRED / REJECTED.
//   - OKX: live / partially_filled / filled / canceled.
//   - Hyperliquid: derived from order updates + fills (open / filled / canceled
//     / triggered / rejected and the many *Canceled variants).
type OrderStatus uint8

const (
	StatusUnknown OrderStatus = iota
	StatusPendingNew
	StatusNew
	StatusPartiallyFilled
	StatusFilled
	StatusCanceled
	StatusRejected
	StatusExpired
	StatusTriggered
)

func (s OrderStatus) String() string {
	switch s {
	case StatusPendingNew:
		return "PENDING_NEW"
	case StatusNew:
		return "NEW"
	case StatusPartiallyFilled:
		return "PARTIALLY_FILLED"
	case StatusFilled:
		return "FILLED"
	case StatusCanceled:
		return "CANCELED"
	case StatusRejected:
		return "REJECTED"
	case StatusExpired:
		return "EXPIRED"
	case StatusTriggered:
		return "TRIGGERED"
	default:
		return "UNKNOWN"
	}
}

// PositionSide distinguishes hedge-mode long/short legs from one-way net
// positions. This is the one deliberate venue concept promoted into the domain
// model, because hedge mode is first-class and portable on Binance and OKX.
// Net-only venues (Hyperliquid) validate PosNet and return contract.ErrNotSupported
// for an explicit long/short side.
//
// Venue mapping (performed in adapters):
//   - Binance: LONG / SHORT / BOTH (BOTH == net).
//   - OKX: long / short / net.
//   - Hyperliquid: always net (no field).
type PositionSide uint8

const (
	PosNet PositionSide = iota
	PosLong
	PosShort
)

func (p PositionSide) String() string {
	switch p {
	case PosLong:
		return "LONG"
	case PosShort:
		return "SHORT"
	default:
		return "NET"
	}
}

// LiquiditySide records whether a fill added (maker) or removed (taker)
// liquidity. It drives fee interpretation downstream.
//
// Venue mapping (performed in adapters):
//   - Binance: fill flag "m" (isMaker bool).
//   - OKX: execType "M" / "T".
//   - Hyperliquid: fill "crossed" bool (crossed == taker).
type LiquiditySide uint8

const (
	LiqUnknown LiquiditySide = iota
	LiqMaker
	LiqTaker
)

func (l LiquiditySide) String() string {
	switch l {
	case LiqMaker:
		return "MAKER"
	case LiqTaker:
		return "TAKER"
	default:
		return "UNKNOWN"
	}
}

// InstrumentKind is the product class of an instrument.
type InstrumentKind uint8

const (
	KindUnknown InstrumentKind = iota
	KindSpot
	KindPerp
	KindFuture
)

func (k InstrumentKind) String() string {
	switch k {
	case KindSpot:
		return "SPOT"
	case KindPerp:
		return "PERP"
	case KindFuture:
		return "FUTURE"
	default:
		return "UNKNOWN"
	}
}

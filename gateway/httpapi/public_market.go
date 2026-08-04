package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/shopspring/decimal"
)

const (
	defaultOrderBookLimit       = 20
	publicMarketSnapshotTimeout = 10 * time.Second
	publicMarketWriteTimeout    = 10 * time.Second
)

var errPublicStreamResync = errors.New("public market stream requires resnapshot")

type publicMarketKind string

const (
	publicKindOrderBook   publicMarketKind = "order_book"
	publicKindBBO         publicMarketKind = "bbo"
	publicKindMarkPrice   publicMarketKind = "mark_price"
	publicKindFundingRate publicMarketKind = "funding_rate"
)

type publicMarketSelection struct {
	accountID    domain.AccountID
	instrumentID domain.InstrumentID
	status       account.Status
	instrument   exchange.Instrument
}

type orderBookProjection struct {
	AccountID    domain.AccountID     `json:"account_id"`
	InstrumentID domain.InstrumentID  `json:"instrument_id"`
	Bids         []exchange.BookLevel `json:"bids"`
	Asks         []exchange.BookLevel `json:"asks"`
	ObservedAt   time.Time            `json:"observed_at"`
	Sequence     string               `json:"sequence,omitempty"`
	Page         exchange.PageInfo    `json:"page"`
}

type bboProjection struct {
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Bid          exchange.BookLevel  `json:"bid"`
	Ask          exchange.BookLevel  `json:"ask"`
	ObservedAt   time.Time           `json:"observed_at"`
}

type markPriceProjection struct {
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Price        decimal.Decimal     `json:"price"`
	ObservedAt   time.Time           `json:"observed_at"`
}

type fundingRateProjection struct {
	Rate            decimal.Decimal          `json:"rate"`
	MarkPrice       exchange.OptionalDecimal `json:"mark_price"`
	ObservedAt      time.Time                `json:"observed_at,omitempty"`
	FundingTime     time.Time                `json:"funding_time,omitempty"`
	NextFundingTime time.Time                `json:"next_funding_time,omitempty"`
}

type fundingHistoryProjection struct {
	AccountID    domain.AccountID        `json:"account_id"`
	InstrumentID domain.InstrumentID     `json:"instrument_id"`
	Items        []fundingRateProjection `json:"items"`
	NextCursor   string                  `json:"next_cursor,omitempty"`
}

type bookEventProjection struct {
	Kind       exchange.EventKind   `json:"kind,omitempty"`
	Sequence   string               `json:"sequence,omitempty"`
	Previous   string               `json:"previous,omitempty"`
	Resync     bool                 `json:"resync,omitempty"`
	Bids       []exchange.BookLevel `json:"bids"`
	Asks       []exchange.BookLevel `json:"asks"`
	ObservedAt time.Time            `json:"observed_at"`
}

type fundingEventProjection struct {
	Rate            decimal.Decimal `json:"rate"`
	ObservedAt      time.Time       `json:"observed_at"`
	FundingTime     time.Time       `json:"funding_time,omitempty"`
	NextFundingTime time.Time       `json:"next_funding_time,omitempty"`
}

type publicMarketEvent struct {
	BootID       domain.BootID       `json:"boot_id"`
	Sequence     uint64              `json:"sequence"`
	EventID      domain.EventID      `json:"event_id"`
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Kind         publicMarketKind    `json:"kind"`
	EventType    string              `json:"event_type"`
	OccurredAt   time.Time           `json:"occurred_at"`
	Payload      json.RawMessage     `json:"payload"`
}

type publicResyncPayload struct {
	Reason    string   `json:"reason"`
	Resources []string `json:"resources"`
}

func (handler *Handler) getOrderBook(w http.ResponseWriter, r *http.Request) {
	selection, err := handler.selectPublicMarket(r)
	if err != nil {
		handler.invalidOrFail(w, err)
		return
	}
	limit, err := orderBookLimit(r)
	if err != nil {
		handler.invalid(w)
		return
	}
	client, err := handler.publicRESTClient(selection)
	if err != nil {
		handler.fail(w, err)
		return
	}
	book, err := client.OrderBook(r.Context(), exchange.OrderBookRequest{
		Instrument: selection.instrument,
		Limit:      limit,
	})
	if err != nil {
		handler.fail(w, err)
		return
	}
	projection, err := projectOrderBook(selection, book)
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projection)
}

func (handler *Handler) getBBO(w http.ResponseWriter, r *http.Request) {
	selection, err := handler.selectPublicMarket(r)
	if err != nil {
		handler.invalidOrFail(w, err)
		return
	}
	client, err := handler.publicRESTClient(selection)
	if err != nil {
		handler.fail(w, err)
		return
	}
	book, err := client.OrderBook(r.Context(), exchange.OrderBookRequest{
		Instrument: selection.instrument,
		Limit:      defaultOrderBookLimit,
	})
	if err != nil {
		handler.fail(w, err)
		return
	}
	projection, err := projectBBO(selection, book)
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projection)
}

func (handler *Handler) getMarkPrice(w http.ResponseWriter, r *http.Request) {
	selection, err := handler.selectPublicMarket(r)
	if err != nil {
		handler.invalidOrFail(w, err)
		return
	}
	if selection.status.Product != exchange.ProductPerp {
		handler.fail(w, account.ErrProductMismatch)
		return
	}
	client, err := handler.accounts.Perp(selection.accountID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	if restClient, ok := client.(exchange.PerpMarkPriceREST); ok {
		event, err := restClient.MarkPrice(r.Context(), selection.instrument)
		if err == nil {
			projection, _, err := projectMarkPriceEvent(selection, event)
			if err != nil {
				handler.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, projection)
			return
		}
		if !errors.Is(err, exchange.ErrUnsupported) {
			handler.fail(w, err)
			return
		}
	}
	socket := client.WebSocket()
	if socket == nil {
		handler.fail(w, exchange.ErrUnsupported)
		return
	}
	subscription, err := socket.WatchMarkPrice(r.Context(), exchange.WatchRequest{Instrument: selection.instrument})
	if err != nil {
		handler.fail(w, err)
		return
	}
	if subscription == nil {
		handler.fail(w, exchange.ErrUnsupported)
		return
	}
	defer subscription.Close()
	projection, err := awaitMarkPriceSnapshot(r.Context(), selection, subscription)
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projection)
}

func (handler *Handler) listFundingRateHistory(w http.ResponseWriter, r *http.Request) {
	selection, err := handler.selectPublicMarket(r)
	if err != nil {
		handler.invalidOrFail(w, err)
		return
	}
	if err := handler.requireFundingCapability(selection.accountID); err != nil {
		handler.fail(w, err)
		return
	}
	if selection.status.Product != exchange.ProductPerp {
		handler.fail(w, account.ErrProductMismatch)
		return
	}
	start, err := optionalRFC3339(r.URL.Query().Get("start"))
	if err != nil {
		handler.invalid(w)
		return
	}
	end, err := optionalRFC3339(r.URL.Query().Get("end"))
	if err != nil || !start.IsZero() && !end.IsZero() && start.After(end) {
		handler.invalid(w)
		return
	}
	limit, err := listLimit(r)
	if err != nil {
		handler.invalid(w)
		return
	}
	client, err := handler.accounts.Perp(selection.accountID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	page, err := client.FundingRateHistory(r.Context(), exchange.FundingRateHistoryRequest{
		Instrument: selection.instrument,
		Start:      start,
		End:        end,
		Cursor:     r.URL.Query().Get("pagination.cursor"),
		Limit:      limit,
	})
	if err != nil {
		handler.fail(w, err)
		return
	}
	items := make([]fundingRateProjection, len(page.Rates))
	for index, rate := range page.Rates {
		if err := validateFundingRateInstrument(selection.instrument, rate); err != nil {
			handler.fail(w, err)
			return
		}
		items[index] = projectFundingRate(rate)
	}
	writeJSON(w, http.StatusOK, fundingHistoryProjection{
		AccountID: selection.accountID, InstrumentID: selection.instrumentID,
		Items: items, NextCursor: page.Page.Cursor,
	})
}

func (handler *Handler) publicMarketEvents(w http.ResponseWriter, r *http.Request) {
	kind, err := parsePublicMarketKind(r.URL.Query().Get("kind"))
	if err != nil {
		handler.invalid(w)
		return
	}
	selection, err := handler.selectPublicMarket(r)
	if err != nil {
		handler.invalidOrFail(w, err)
		return
	}
	switch kind {
	case publicKindOrderBook:
		handler.startOrderBookStream(w, r, selection)
	case publicKindBBO:
		handler.startBBOStream(w, r, selection)
	case publicKindMarkPrice:
		handler.startMarkPriceStream(w, r, selection)
	case publicKindFundingRate:
		handler.startFundingRateStream(w, r, selection)
	default:
		handler.invalid(w)
	}
}

func (handler *Handler) startOrderBookStream(w http.ResponseWriter, r *http.Request, selection publicMarketSelection) {
	client, err := handler.publicRESTClient(selection)
	if err != nil {
		handler.fail(w, err)
		return
	}
	request := exchange.WatchRequest{Instrument: selection.instrument}
	var subscription exchange.Subscription[exchange.BookEvent]
	switch selection.status.Product {
	case exchange.ProductSpot:
		spot, clientErr := handler.accounts.Spot(selection.accountID)
		if clientErr != nil {
			handler.fail(w, clientErr)
			return
		}
		socket := spot.WebSocket()
		if socket == nil {
			handler.fail(w, exchange.ErrUnsupported)
			return
		}
		subscription, err = socket.WatchOrderBook(r.Context(), request)
	case exchange.ProductPerp:
		perp, clientErr := handler.accounts.Perp(selection.accountID)
		if clientErr != nil {
			handler.fail(w, clientErr)
			return
		}
		socket := perp.WebSocket()
		if socket == nil {
			handler.fail(w, exchange.ErrUnsupported)
			return
		}
		subscription, err = socket.WatchOrderBook(r.Context(), request)
	default:
		err = account.ErrProductMismatch
	}
	if err != nil {
		handler.fail(w, err)
		return
	}
	if subscription == nil {
		handler.fail(w, exchange.ErrUnsupported)
		return
	}
	book, err := client.OrderBook(r.Context(), exchange.OrderBookRequest{
		Instrument: selection.instrument,
		Limit:      defaultOrderBookLimit,
	})
	if err != nil {
		_ = subscription.Close()
		handler.fail(w, err)
		return
	}
	snapshot, err := projectOrderBook(selection, book)
	if err != nil {
		_ = subscription.Close()
		handler.fail(w, err)
		return
	}
	servePublicSubscription(handler, w, r, selection, publicKindOrderBook, subscription,
		snapshot, func(event exchange.BookEvent) (any, time.Time, error) {
			if err := validatePublicEventInstrument(selection, event.Instrument); err != nil {
				return nil, time.Time{}, err
			}
			if event.Resync {
				return nil, time.Time{}, errPublicStreamResync
			}
			observedAt := observedTime(event.Time)
			return bookEventProjection{
				Kind: event.Kind, Sequence: event.Sequence, Previous: event.Previous, Resync: event.Resync,
				Bids: nonNil(event.Bids), Asks: nonNil(event.Asks), ObservedAt: observedAt,
			}, observedAt, nil
		})
}

func (handler *Handler) startBBOStream(w http.ResponseWriter, r *http.Request, selection publicMarketSelection) {
	client, err := handler.publicRESTClient(selection)
	if err != nil {
		handler.fail(w, err)
		return
	}
	request := exchange.WatchRequest{Instrument: selection.instrument}
	var subscription exchange.Subscription[exchange.BBOEvent]
	switch selection.status.Product {
	case exchange.ProductSpot:
		spot, clientErr := handler.accounts.Spot(selection.accountID)
		if clientErr != nil {
			handler.fail(w, clientErr)
			return
		}
		socket := spot.WebSocket()
		if socket == nil {
			handler.fail(w, exchange.ErrUnsupported)
			return
		}
		subscription, err = socket.WatchBBO(r.Context(), request)
	case exchange.ProductPerp:
		perp, clientErr := handler.accounts.Perp(selection.accountID)
		if clientErr != nil {
			handler.fail(w, clientErr)
			return
		}
		socket := perp.WebSocket()
		if socket == nil {
			handler.fail(w, exchange.ErrUnsupported)
			return
		}
		subscription, err = socket.WatchBBO(r.Context(), request)
	default:
		err = account.ErrProductMismatch
	}
	if err != nil {
		handler.fail(w, err)
		return
	}
	if subscription == nil {
		handler.fail(w, exchange.ErrUnsupported)
		return
	}
	book, err := client.OrderBook(r.Context(), exchange.OrderBookRequest{
		Instrument: selection.instrument,
		Limit:      defaultOrderBookLimit,
	})
	if err != nil {
		_ = subscription.Close()
		handler.fail(w, err)
		return
	}
	snapshot, err := projectBBO(selection, book)
	if err != nil {
		_ = subscription.Close()
		handler.fail(w, err)
		return
	}
	servePublicSubscription(handler, w, r, selection, publicKindBBO, subscription,
		snapshot, func(event exchange.BBOEvent) (any, time.Time, error) {
			if err := validatePublicEventInstrument(selection, event.Instrument); err != nil {
				return nil, time.Time{}, err
			}
			if err := validBBO(event.Bid, event.Ask); err != nil {
				return nil, time.Time{}, err
			}
			observedAt := observedTime(event.Time)
			return bboProjection{
				AccountID: selection.accountID, InstrumentID: selection.instrumentID,
				Bid: event.Bid, Ask: event.Ask, ObservedAt: observedAt,
			}, observedAt, nil
		})
}

func (handler *Handler) startMarkPriceStream(w http.ResponseWriter, r *http.Request, selection publicMarketSelection) {
	if selection.status.Product != exchange.ProductPerp {
		handler.fail(w, account.ErrProductMismatch)
		return
	}
	client, err := handler.accounts.Perp(selection.accountID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	socket := client.WebSocket()
	if socket == nil {
		handler.fail(w, exchange.ErrUnsupported)
		return
	}
	subscription, err := socket.WatchMarkPrice(r.Context(), exchange.WatchRequest{Instrument: selection.instrument})
	if err != nil {
		handler.fail(w, err)
		return
	}
	if subscription == nil {
		handler.fail(w, exchange.ErrUnsupported)
		return
	}
	snapshot, err := awaitMarkPriceSnapshot(r.Context(), selection, subscription)
	if err != nil {
		_ = subscription.Close()
		handler.fail(w, err)
		return
	}
	servePublicSubscription(handler, w, r, selection, publicKindMarkPrice, subscription,
		snapshot, func(event exchange.MarkPriceEvent) (any, time.Time, error) {
			projection, observedAt, projectErr := projectMarkPriceEvent(selection, event)
			return projection, observedAt, projectErr
		})
}

func (handler *Handler) startFundingRateStream(w http.ResponseWriter, r *http.Request, selection publicMarketSelection) {
	if err := handler.requireFundingCapability(selection.accountID); err != nil {
		handler.fail(w, err)
		return
	}
	if selection.status.Product != exchange.ProductPerp {
		handler.fail(w, account.ErrProductMismatch)
		return
	}
	client, err := handler.accounts.Perp(selection.accountID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	socket := client.WebSocket()
	if socket == nil {
		handler.fail(w, exchange.ErrUnsupported)
		return
	}
	subscription, err := socket.WatchFundingRate(r.Context(), exchange.WatchRequest{Instrument: selection.instrument})
	if err != nil {
		handler.fail(w, err)
		return
	}
	if subscription == nil {
		handler.fail(w, exchange.ErrUnsupported)
		return
	}
	rate, err := client.FundingRate(r.Context(), exchange.FundingRateRequest{Instrument: selection.instrument})
	if err != nil {
		_ = subscription.Close()
		handler.fail(w, err)
		return
	}
	if err := validateFundingRateInstrument(selection.instrument, rate); err != nil {
		_ = subscription.Close()
		handler.fail(w, err)
		return
	}
	servePublicSubscription(handler, w, r, selection, publicKindFundingRate, subscription,
		projectFundingRate(rate), func(event exchange.FundingRateEvent) (any, time.Time, error) {
			if err := validatePublicEventInstrument(selection, event.Instrument); err != nil {
				return nil, time.Time{}, err
			}
			observedAt := observedTime(event.EffectiveAt)
			return fundingEventProjection{
				Rate: event.Rate, ObservedAt: observedAt,
				FundingTime: event.EffectiveAt, NextFundingTime: event.NextAt,
			}, observedAt, nil
		})
}

func servePublicSubscription[T any](
	handler *Handler,
	w http.ResponseWriter,
	r *http.Request,
	selection publicMarketSelection,
	kind publicMarketKind,
	subscription exchange.Subscription[T],
	snapshot any,
	project func(T) (any, time.Time, error),
) {
	if subscription == nil {
		handler.fail(w, fmt.Errorf("public subscription is unavailable"))
		return
	}
	defer subscription.Close()
	bootID, err := handler.identity.BootID()
	if err != nil {
		handler.fail(w, err)
		return
	}
	snapshotPayload, err := handler.publicPayload(snapshot)
	if err != nil {
		handler.fail(w, err)
		return
	}
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(handler.bodyLimit)
	streamContext := connection.CloseRead(r.Context())
	ping := time.NewTicker(websocketPingPeriod)
	defer ping.Stop()
	sequence := uint64(0)
	writeEvent := func(eventType string, occurredAt time.Time, payload json.RawMessage) error {
		eventID, eventErr := handler.identity.EventID()
		if eventErr != nil {
			return eventErr
		}
		sequence++
		writeContext, cancel := context.WithTimeout(streamContext, publicMarketWriteTimeout)
		defer cancel()
		return wsjson.Write(writeContext, connection, publicMarketEvent{
			BootID: bootID, Sequence: sequence, EventID: eventID,
			AccountID: selection.accountID, InstrumentID: selection.instrumentID,
			Kind: kind, EventType: eventType, OccurredAt: observedTime(occurredAt), Payload: payload,
		})
	}
	if err := writeEvent("snapshot", time.Now().UTC(), snapshotPayload); err != nil {
		return
	}
	events := subscription.Events()
	statuses := subscription.Status()
	streamErrors := subscription.Errors()
	for events != nil || statuses != nil || streamErrors != nil {
		select {
		case <-streamContext.Done():
			return
		case <-ping.C:
			pingContext, cancel := context.WithTimeout(streamContext, 10*time.Second)
			err := connection.Ping(pingContext)
			cancel()
			if err != nil {
				return
			}
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			payload, occurredAt, projectErr := project(event)
			if projectErr != nil {
				reason := "malformed-update"
				if errors.Is(projectErr, errPublicStreamResync) {
					reason = "stream-gap"
				}
				writePublicResync(handler, connection, writeEvent, kind, reason)
				return
			}
			raw, payloadErr := handler.publicPayload(payload)
			if payloadErr != nil {
				writePublicResync(handler, connection, writeEvent, kind, "payload-too-large")
				return
			}
			if writeEvent("update", occurredAt, raw) != nil {
				return
			}
		case status, open := <-statuses:
			if !open {
				statuses = nil
				continue
			}
			if status.State == exchange.SubscriptionGap || status.State == exchange.SubscriptionResyncing ||
				status.Phase == exchange.GapStarted {
				writePublicResync(handler, connection, writeEvent, kind, "stream-gap")
				return
			}
		case _, open := <-streamErrors:
			if !open {
				streamErrors = nil
				continue
			}
			writePublicResync(handler, connection, writeEvent, kind, "stream-error")
			return
		}
	}
	_ = connection.Close(websocket.StatusNormalClosure, "public_stream_complete")
}

func writePublicResync(
	handler *Handler,
	connection *websocket.Conn,
	writeEvent func(string, time.Time, json.RawMessage) error,
	kind publicMarketKind,
	reason string,
) {
	payload, err := handler.publicPayload(publicResyncPayload{
		Reason: reason, Resources: []string{string(kind)},
	})
	if err == nil {
		_ = writeEvent("resync_required", time.Now().UTC(), payload)
	}
	_ = connection.Close(websocket.StatusPolicyViolation, "public_stream_resnapshot_required")
}

func (handler *Handler) selectPublicMarket(r *http.Request) (publicMarketSelection, error) {
	accountID, err := parseAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		return publicMarketSelection{}, err
	}
	instrumentID, err := parseInstrumentID(r.URL.Query().Get("instrument_id"))
	if err != nil {
		return publicMarketSelection{}, err
	}
	status, err := handler.accounts.Status(accountID)
	if err != nil {
		return publicMarketSelection{}, err
	}
	if status.AccountID != accountID || !status.ReadReady {
		return publicMarketSelection{}, fmt.Errorf("%w: account is not read-ready", account.ErrManagerClosed)
	}
	instrument, err := handler.accounts.Resolve(r.Context(), accountID, instrumentID)
	if err != nil {
		return publicMarketSelection{}, err
	}
	if instrument.Identity.Product != status.Product || instrument.Identity.Venue != status.Venue {
		return publicMarketSelection{}, fmt.Errorf("%w: resolved instrument scope mismatch", exchange.ErrInvalidRequest)
	}
	return publicMarketSelection{
		accountID: accountID, instrumentID: instrumentID, status: status, instrument: instrument,
	}, nil
}

func (handler *Handler) publicRESTClient(selection publicMarketSelection) (exchange.MarketREST, error) {
	switch selection.status.Product {
	case exchange.ProductSpot:
		return handler.accounts.Spot(selection.accountID)
	case exchange.ProductPerp:
		return handler.accounts.Perp(selection.accountID)
	default:
		return nil, account.ErrProductMismatch
	}
}

func (handler *Handler) currentFunding(ctx context.Context, selection publicMarketSelection) (exchange.FundingRate, error) {
	if err := handler.requireFundingCapability(selection.accountID); err != nil {
		return exchange.FundingRate{}, err
	}
	if selection.status.Product != exchange.ProductPerp {
		return exchange.FundingRate{}, account.ErrProductMismatch
	}
	client, err := handler.accounts.Perp(selection.accountID)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	rate, err := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: selection.instrument})
	if err != nil {
		return exchange.FundingRate{}, err
	}
	if err := validateFundingRateInstrument(selection.instrument, rate); err != nil {
		return exchange.FundingRate{}, err
	}
	return rate, nil
}

func (handler *Handler) requireFundingCapability(accountID domain.AccountID) error {
	row, err := handler.accounts.Capability(accountID)
	if err != nil {
		return err
	}
	if !row.Operations.Funding {
		return exchange.ErrUnsupported
	}
	return nil
}

func (handler *Handler) publicPayload(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil || int64(len(raw)) > handler.bodyLimit {
		return nil, fmt.Errorf("public market payload is invalid")
	}
	return raw, nil
}

func (handler *Handler) invalidOrFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, account.ErrUnknownAccount), errors.Is(err, account.ErrManagerClosed),
		errors.Is(err, exchange.ErrNotFound), errors.Is(err, exchange.ErrUnsupported),
		errors.Is(err, exchange.ErrTransport), errors.Is(err, exchange.ErrRateLimit):
		handler.fail(w, err)
	default:
		handler.invalid(w)
	}
}

func projectOrderBook(selection publicMarketSelection, book exchange.OrderBook) (orderBookProjection, error) {
	if err := validatePublicEventInstrument(selection, book.Instrument); err != nil {
		return orderBookProjection{}, err
	}
	return orderBookProjection{
		AccountID: selection.accountID, InstrumentID: selection.instrumentID,
		Bids: nonNil(book.Bids), Asks: nonNil(book.Asks), ObservedAt: observedTime(book.Time),
		Sequence: book.Sequence, Page: book.Page,
	}, nil
}

func projectBBO(selection publicMarketSelection, book exchange.OrderBook) (bboProjection, error) {
	if err := validatePublicEventInstrument(selection, book.Instrument); err != nil {
		return bboProjection{}, err
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		return bboProjection{}, fmt.Errorf("%w: two-sided bbo unavailable", exchange.ErrUnsupported)
	}
	if err := validBBO(book.Bids[0], book.Asks[0]); err != nil {
		return bboProjection{}, err
	}
	return bboProjection{
		AccountID: selection.accountID, InstrumentID: selection.instrumentID,
		Bid: book.Bids[0], Ask: book.Asks[0], ObservedAt: observedTime(book.Time),
	}, nil
}

func validBBO(bid, ask exchange.BookLevel) error {
	if !bid.Price.IsPositive() || !bid.Quantity.IsPositive() ||
		!ask.Price.IsPositive() || !ask.Quantity.IsPositive() || bid.Price.GreaterThan(ask.Price) {
		return fmt.Errorf("%w: invalid bbo", exchange.ErrUnsupported)
	}
	return nil
}

func validatePublicEventInstrument(selection publicMarketSelection, instrument exchange.Instrument) error {
	if instrument.Identity != selection.instrument.Identity {
		return fmt.Errorf("%w: public event instrument mismatch", exchange.ErrMalformedResponse)
	}
	return nil
}

func validateFundingRateInstrument(instrument exchange.Instrument, rate exchange.FundingRate) error {
	identity := instrument.Identity
	if rate.NativeSymbol != identity.NativeSymbol ||
		rate.NativeID != "" && rate.NativeID != identity.NativeID {
		return fmt.Errorf("%w: public funding instrument mismatch", exchange.ErrMalformedResponse)
	}
	return nil
}

func projectMarkPriceEvent(
	selection publicMarketSelection,
	event exchange.MarkPriceEvent,
) (markPriceProjection, time.Time, error) {
	if err := validatePublicEventInstrument(selection, event.Instrument); err != nil {
		return markPriceProjection{}, time.Time{}, err
	}
	if !event.Price.IsPositive() {
		return markPriceProjection{}, time.Time{}, fmt.Errorf("%w: mark price unavailable", exchange.ErrUnsupported)
	}
	observedAt := observedTime(event.Time)
	return markPriceProjection{
		AccountID: selection.accountID, InstrumentID: selection.instrumentID,
		Price: event.Price, ObservedAt: observedAt,
	}, observedAt, nil
}

func awaitMarkPriceSnapshot(
	ctx context.Context,
	selection publicMarketSelection,
	subscription exchange.Subscription[exchange.MarkPriceEvent],
) (markPriceProjection, error) {
	if subscription == nil {
		return markPriceProjection{}, exchange.ErrUnsupported
	}
	snapshotContext, cancel := context.WithTimeout(ctx, publicMarketSnapshotTimeout)
	defer cancel()
	events := subscription.Events()
	statuses := subscription.Status()
	streamErrors := subscription.Errors()
	for events != nil || statuses != nil || streamErrors != nil {
		select {
		case <-snapshotContext.Done():
			return markPriceProjection{}, fmt.Errorf("%w: mark price snapshot deadline", exchange.ErrTransport)
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			projection, _, err := projectMarkPriceEvent(selection, event)
			return projection, err
		case status, open := <-statuses:
			if !open {
				statuses = nil
				continue
			}
			if status.State == exchange.SubscriptionGap || status.State == exchange.SubscriptionResyncing ||
				status.State == exchange.SubscriptionClosed || status.Phase == exchange.GapStarted {
				return markPriceProjection{}, fmt.Errorf("%w: mark price stream unavailable", exchange.ErrTransport)
			}
		case _, open := <-streamErrors:
			if !open {
				streamErrors = nil
				continue
			}
			return markPriceProjection{}, fmt.Errorf("%w: mark price stream error", exchange.ErrTransport)
		}
	}
	return markPriceProjection{}, fmt.Errorf("%w: mark price stream closed", exchange.ErrTransport)
}

func projectFundingRate(rate exchange.FundingRate) fundingRateProjection {
	return fundingRateProjection{
		Rate: rate.Rate, MarkPrice: rate.MarkPrice, ObservedAt: rate.ObservedAt,
		FundingTime: rate.FundingTime, NextFundingTime: rate.NextFundingTime,
	}
}

func observedTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func orderBookLimit(r *http.Request) (int, error) {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return defaultOrderBookLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > maxListLimit {
		return 0, fmt.Errorf("order book limit out of range")
	}
	return limit, nil
}

func optionalRFC3339(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("time is invalid")
	}
	return parsed.UTC(), nil
}

func parsePublicMarketKind(value string) (publicMarketKind, error) {
	kind := publicMarketKind(value)
	switch kind {
	case publicKindOrderBook, publicKindBBO, publicKindMarkPrice, publicKindFundingRate:
		return kind, nil
	default:
		return "", fmt.Errorf("public market kind is invalid")
	}
}

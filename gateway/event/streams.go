package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

type StreamAccounts interface {
	Status(domain.AccountID) (account.Status, error)
	Instruments(domain.AccountID) (catalog.Snapshot, error)
	Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error)
	Spot(domain.AccountID) (exchange.SpotClient, error)
	Perp(domain.AccountID) (exchange.PerpClient, error)
	SetPrivateStreamState(domain.AccountID, bool, string) error
}

type StreamReconciler interface {
	SuspendAccount(domain.AccountID) error
	RecoverAccount(context.Context, domain.AccountID) (order.RecoveryReport, error)
	ObserveVenueOrder(context.Context, order.VenueOrderObservation) (order.Result, error)
}

type StreamOptions struct {
	Accounts           StreamAccounts
	Reconciler         StreamReconciler
	Hub                *Hub
	PrincipalID        domain.PrincipalID
	ReconnectBackoff   time.Duration
	RecoveryTimeout    time.Duration
	SubscriptionBuffer int
}

type FillPayload struct {
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Fill         exchange.Fill       `json:"fill"`
}

type BalancePayload struct {
	AccountID domain.AccountID `json:"account_id"`
	Balance   exchange.Balance `json:"balance"`
}

type PositionPayload struct {
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Position     exchange.Position   `json:"position"`
}

type AccountHealthPayload struct {
	AccountID  domain.AccountID `json:"account_id"`
	ReadReady  bool             `json:"read_ready"`
	WriteReady bool             `json:"write_ready"`
	Reason     string           `json:"reason,omitempty"`
}

type StreamManager struct {
	accounts           StreamAccounts
	reconciler         StreamReconciler
	hub                *Hub
	principal          domain.PrincipalID
	reconnectBackoff   time.Duration
	recoveryTimeout    time.Duration
	subscriptionBuffer int

	mu       sync.Mutex
	versions map[string]domain.ResourceVersion
	cancel   context.CancelFunc
	wait     sync.WaitGroup
	started  bool
	closed   bool
}

func NewStreamManager(options StreamOptions) (*StreamManager, error) {
	if options.Accounts == nil || options.Reconciler == nil || options.Hub == nil {
		return nil, fmt.Errorf("stream manager dependencies are incomplete")
	}
	if err := options.PrincipalID.Validate(); err != nil {
		return nil, err
	}
	if options.ReconnectBackoff <= 0 {
		options.ReconnectBackoff = 5 * time.Second
	}
	if options.RecoveryTimeout <= 0 {
		options.RecoveryTimeout = 30 * time.Second
	}
	if options.SubscriptionBuffer <= 0 {
		options.SubscriptionBuffer = 64
	}
	return &StreamManager{
		accounts: options.Accounts, reconciler: options.Reconciler, hub: options.Hub,
		principal: options.PrincipalID, reconnectBackoff: options.ReconnectBackoff,
		recoveryTimeout: options.RecoveryTimeout, subscriptionBuffer: options.SubscriptionBuffer,
		versions: make(map[string]domain.ResourceVersion),
	}, nil
}

func (manager *StreamManager) Start(ctx context.Context, accountIDs []domain.AccountID) error {
	if ctx == nil {
		return fmt.Errorf("stream manager context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	seen := make(map[domain.AccountID]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if err := accountID.Validate(); err != nil {
			return err
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrClosed
	}
	if manager.started {
		return nil
	}
	runContext, cancel := context.WithCancel(ctx)
	manager.cancel = cancel
	manager.started = true
	for accountID := range seen {
		if err := manager.reconciler.SuspendAccount(accountID); err != nil {
			cancel()
			return fmt.Errorf("suspend account before private stream failed")
		}
		if err := manager.accounts.SetPrivateStreamState(accountID, false, account.ReasonPrivateStreamStarting); err != nil {
			cancel()
			return fmt.Errorf("mark private stream starting failed")
		}
		manager.wait.Add(1)
		go manager.superviseAccount(runContext, accountID)
	}
	return nil
}

func (manager *StreamManager) Close() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return
	}
	manager.closed = true
	cancel := manager.cancel
	manager.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	manager.wait.Wait()
}

func (manager *StreamManager) superviseAccount(ctx context.Context, accountID domain.AccountID) {
	defer manager.wait.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		streams, err := manager.openAccountStreams(ctx, accountID)
		if err != nil {
			manager.unavailable(accountID, "private_stream_connect_failed", true)
			if !waitContext(ctx, manager.reconnectBackoff) {
				return
			}
			continue
		}

		recoveryContext, cancel := context.WithTimeout(ctx, manager.recoveryTimeout)
		report, recoveryErr := manager.reconciler.RecoverAccount(recoveryContext, accountID)
		cancel()
		if recoveryErr != nil || !report.WriteReady {
			streams.close()
			manager.unavailable(accountID, "private_stream_recovery_failed", true)
			if !waitContext(ctx, manager.reconnectBackoff) {
				return
			}
			continue
		}
		if err := manager.accounts.SetPrivateStreamState(accountID, true, ""); err != nil {
			streams.close()
			return
		}
		manager.publishHealth(accountID)

		select {
		case <-ctx.Done():
			streams.close()
			return
		case <-streams.faults:
			streams.close()
			if !waitContext(ctx, manager.reconnectBackoff) {
				return
			}
		}
	}
}

func (manager *StreamManager) unavailable(accountID domain.AccountID, reason string, resync bool) {
	_ = manager.reconciler.SuspendAccount(accountID)
	_ = manager.accounts.SetPrivateStreamState(accountID, false, reason)
	manager.publishHealth(accountID)
	if resync {
		_, _ = manager.hub.PublishResync(manager.principal, accountID, reason)
	}
}

func (manager *StreamManager) publishHealth(accountID domain.AccountID) {
	status, err := manager.accounts.Status(accountID)
	if err != nil {
		return
	}
	payload := AccountHealthPayload{
		AccountID: accountID, ReadReady: status.ReadReady, WriteReady: status.WriteReady, Reason: status.Reason,
	}
	manager.publish(accountID, KindAccountHealth, "account", time.Time{}, payload)
}

type accountStreams struct {
	cancel    context.CancelFunc
	faults    chan struct{}
	mu        sync.Mutex
	closers   []func() error
	once      sync.Once
	faultOnce sync.Once
	onFault   func()
}

func (streams *accountStreams) add(close func() error) {
	streams.mu.Lock()
	defer streams.mu.Unlock()
	streams.closers = append(streams.closers, close)
}

func (streams *accountStreams) fault() {
	streams.faultOnce.Do(func() {
		if streams.onFault != nil {
			streams.onFault()
		}
	})
	select {
	case streams.faults <- struct{}{}:
	default:
	}
}

func (streams *accountStreams) close() {
	streams.once.Do(func() {
		streams.cancel()
		streams.mu.Lock()
		closers := append([]func() error(nil), streams.closers...)
		streams.mu.Unlock()
		for index := len(closers) - 1; index >= 0; index-- {
			_ = closers[index]()
		}
	})
}

func (manager *StreamManager) openAccountStreams(ctx context.Context, accountID domain.AccountID) (*accountStreams, error) {
	status, err := manager.accounts.Status(accountID)
	if err != nil {
		return nil, err
	}
	snapshot, err := manager.accounts.Instruments(accountID)
	if err != nil || len(snapshot.Instruments) == 0 {
		return nil, fmt.Errorf("private stream instruments unavailable")
	}
	streamContext, cancel := context.WithCancel(ctx)
	streams := &accountStreams{
		cancel: cancel, faults: make(chan struct{}, 1),
		onFault: func() {
			manager.unavailable(accountID, account.ReasonPrivateStreamGap, true)
		},
	}
	closeOnError := func(err error) (*accountStreams, error) {
		streams.close()
		return nil, err
	}

	switch status.Product {
	case exchange.ProductSpot:
		client, clientErr := manager.accounts.Spot(accountID)
		if clientErr != nil {
			return closeOnError(clientErr)
		}
		socket := client.WebSocket()
		for _, public := range snapshot.Instruments {
			instrument, resolveErr := manager.accounts.Resolve(streamContext, accountID, public.ID)
			if resolveErr != nil {
				return closeOnError(resolveErr)
			}
			if err := manager.openSpotInstrument(streamContext, streams, socket, accountID, public.ID, instrument); err != nil {
				return closeOnError(err)
			}
		}
		if err := manager.openBalances(streamContext, streams, socket, accountID); err != nil {
			return closeOnError(err)
		}
	case exchange.ProductPerp:
		client, clientErr := manager.accounts.Perp(accountID)
		if clientErr != nil {
			return closeOnError(clientErr)
		}
		socket := client.WebSocket()
		for _, public := range snapshot.Instruments {
			instrument, resolveErr := manager.accounts.Resolve(streamContext, accountID, public.ID)
			if resolveErr != nil {
				return closeOnError(resolveErr)
			}
			if err := manager.openPerpInstrument(streamContext, streams, socket, accountID, public.ID, instrument); err != nil {
				return closeOnError(err)
			}
		}
		if err := manager.openBalances(streamContext, streams, socket, accountID); err != nil {
			return closeOnError(err)
		}
	default:
		return closeOnError(fmt.Errorf("private stream product unsupported"))
	}
	return streams, nil
}

func (manager *StreamManager) openSpotInstrument(
	ctx context.Context,
	streams *accountStreams,
	socket exchange.SpotWebSocket,
	accountID domain.AccountID,
	instrumentID domain.InstrumentID,
	instrument exchange.Instrument,
) error {
	request := exchange.WatchRequest{
		Instrument: instrument, Options: exchange.WatchOptions{Buffer: manager.subscriptionBuffer},
	}
	orders, err := socket.WatchOrders(ctx, request)
	if err != nil {
		return err
	}
	monitorSubscription(ctx, streams, orders, func(event exchange.OrderEvent) error {
		_, observeErr := manager.reconciler.ObserveVenueOrder(ctx, order.VenueOrderObservation{
			AccountID: accountID, InstrumentID: instrumentID,
			Product: instrument.Identity.Product, Order: event.Order,
		})
		if errors.Is(observeErr, store.ErrNotFound) {
			return nil
		}
		return observeErr
	})
	fills, err := socket.WatchFills(ctx, request)
	if err != nil {
		return err
	}
	monitorSubscription(ctx, streams, fills, func(event exchange.FillEvent) error {
		return manager.publishFill(accountID, instrumentID, event)
	})
	return nil
}

func (manager *StreamManager) openPerpInstrument(
	ctx context.Context,
	streams *accountStreams,
	socket exchange.PerpWebSocket,
	accountID domain.AccountID,
	instrumentID domain.InstrumentID,
	instrument exchange.Instrument,
) error {
	if err := manager.openSpotInstrument(ctx, streams, socket, accountID, instrumentID, instrument); err != nil {
		return err
	}
	positions, err := socket.WatchPositions(ctx, exchange.WatchRequest{
		Instrument: instrument, Options: exchange.WatchOptions{Buffer: manager.subscriptionBuffer},
	})
	if err != nil {
		return err
	}
	monitorSubscription(ctx, streams, positions, func(event exchange.PositionEvent) error {
		return manager.publishPositions(accountID, instrumentID, event)
	})
	return nil
}

func (manager *StreamManager) openBalances(
	ctx context.Context,
	streams *accountStreams,
	socket exchange.SpotWebSocket,
	accountID domain.AccountID,
) error {
	balances, err := socket.WatchBalances(ctx, exchange.WatchAccountRequest{
		Options: exchange.WatchOptions{Buffer: manager.subscriptionBuffer},
	})
	if err != nil {
		return err
	}
	monitorSubscription(ctx, streams, balances, func(event exchange.BalanceEvent) error {
		return manager.publishBalances(accountID, event)
	})
	return nil
}

func monitorSubscription[T any](
	ctx context.Context,
	streams *accountStreams,
	subscription exchange.Subscription[T],
	onEvent func(T) error,
) {
	streams.add(subscription.Close)
	go func() {
		events := subscription.Events()
		statuses := subscription.Status()
		errorsChannel := subscription.Errors()
		for {
			select {
			case <-ctx.Done():
				return
			case event, open := <-events:
				if !open {
					streams.fault()
					return
				}
				if err := onEvent(event); err != nil {
					streams.fault()
					return
				}
			case status, open := <-statuses:
				if !open {
					statuses = nil
					continue
				}
				if status.State == exchange.SubscriptionGap || status.State == exchange.SubscriptionClosed ||
					status.Phase == exchange.GapStarted {
					streams.fault()
					return
				}
			case streamErr, open := <-errorsChannel:
				if !open {
					errorsChannel = nil
					continue
				}
				if streamErr != nil {
					streams.fault()
					return
				}
			}
			if events == nil && statuses == nil && errorsChannel == nil {
				streams.fault()
				return
			}
		}
	}()
}

func (manager *StreamManager) publishFill(accountID domain.AccountID, instrumentID domain.InstrumentID, event exchange.FillEvent) error {
	resource := event.Fill.FillID
	if resource == "" {
		resource = event.Fill.OrderID + ":" + event.Fill.NativeSymbol
	}
	return manager.publish(accountID, KindFill, resource, event.Fill.Time, FillPayload{
		AccountID: accountID, InstrumentID: instrumentID, Fill: event.Fill,
	})
}

func (manager *StreamManager) publishBalances(accountID domain.AccountID, event exchange.BalanceEvent) error {
	for _, balance := range event.Balances {
		if err := manager.publish(accountID, KindBalance, balance.Asset, event.Time, BalancePayload{
			AccountID: accountID, Balance: balance,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (manager *StreamManager) publishPositions(accountID domain.AccountID, instrumentID domain.InstrumentID, event exchange.PositionEvent) error {
	for _, position := range event.Positions {
		resource := position.NativeID
		if resource == "" {
			resource = position.NativeSymbol
		}
		if err := manager.publish(accountID, KindPosition, resource, event.Time, PositionPayload{
			AccountID: accountID, InstrumentID: instrumentID, Position: position,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (manager *StreamManager) publish(accountID domain.AccountID, kind Kind, resource string, occurredAt time.Time, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	eventID, err := manager.hub.identity.EventID()
	if err != nil {
		return err
	}
	version := manager.nextVersion(accountID, kind, resource)
	_, err = manager.hub.Publish(PublishRequest{
		PrincipalID: manager.principal, AccountID: accountID, EventID: eventID,
		ResourceVersion: version, Kind: kind, OccurredAt: occurredAt, Payload: encoded,
	})
	return err
}

func (manager *StreamManager) nextVersion(accountID domain.AccountID, kind Kind, resource string) domain.ResourceVersion {
	key := string(accountID) + "\x00" + string(kind) + "\x00" + resource
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.versions[key]++
	return manager.versions[key]
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

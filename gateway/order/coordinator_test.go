package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/shopspring/decimal"
)

func TestCoordinatorConcurrentIdempotencyCallsDriverOnce(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if report, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil || !report.WriteReady {
		t.Fatalf("initial recovery = %+v, err=%v", report, err)
	}

	request := fixture.placeRequest()
	const callers = 16
	start := make(chan struct{})
	results := make(chan Result, callers)
	errs := make(chan error, callers)
	for index := 0; index < callers; index++ {
		go func() {
			<-start
			result, err := fixture.coordinator.Place(context.Background(), request)
			results <- result
			errs <- err
		}()
	}
	close(start)

	var commandID domain.CommandID
	var orderID domain.GatewayOrderID
	for index := 0; index < callers; index++ {
		result, err := <-results, <-errs
		if err != nil {
			t.Fatalf("Place %d: %v", index, err)
		}
		if commandID == "" {
			commandID, orderID = result.CommandID, result.GatewayOrderID
		}
		if result.CommandID != commandID || result.GatewayOrderID != orderID || result.ClientOrderID != "1" {
			t.Fatalf("idempotent identity changed: %+v", result)
		}
	}
	if got := fixture.driver.PlaceCalls(); got != 1 {
		t.Fatalf("driver place calls = %d, want 1", got)
	}

	changed := request
	changed.Quantity = decimal.RequireFromString("0.002")
	result, err := fixture.coordinator.Place(context.Background(), changed)
	if !errors.Is(err, ErrIdempotencyConflict) || result.CommandID != commandID {
		t.Fatalf("payload conflict result=%+v err=%v", result, err)
	}
	if got := fixture.driver.PlaceCalls(); got != 1 {
		t.Fatalf("payload conflict reached driver: %d calls", got)
	}
}

func TestCoordinatorAppliesDurablePerpLeverageBeforePlacement(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.instrument.Identity.Product = exchange.ProductPerp
	fixture.router.route.Instrument = fixture.instrument
	fixture.router.route.Product = exchange.ProductPerp
	fixture.driver.placeAck.Product = exchange.ProductPerp
	fixture.driver.lookupFallback = notFoundExactResult(fixture.instrument, "1")
	if report, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil || !report.WriteReady {
		t.Fatalf("initial recovery = %+v, err=%v", report, err)
	}
	request := fixture.placeRequest()
	request.RouteID = "perp-usdt"
	request.InstrumentID = "binance:live:perp:USDT:BTC-USDT"
	request.Leverage = 5
	if _, err := fixture.coordinator.Place(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.driver.mu.Lock()
	defer fixture.driver.mu.Unlock()
	if fixture.driver.leverageCalls != 1 || fixture.driver.leverage != 5 || fixture.driver.sequence != "leverage,order" {
		t.Fatalf("leverage calls=%d value=%d sequence=%q", fixture.driver.leverageCalls, fixture.driver.leverage, fixture.driver.sequence)
	}
}

func TestCoordinatorRevalidatesWriteFenceImmediatelyBeforeVenueIO(t *testing.T) {
	for _, race := range []string{"rotation", "drain"} {
		t.Run(race, func(t *testing.T) {
			fixture := newCoordinatorFixture(t)
			active := true
			current := account.WriteFence{AccountID: fixture.accountID, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1}
			fixture.router.route.CheckWriteFence = func(fence account.WriteFence) error {
				if !active || fence != current {
					return account.ErrStaleWriteFence
				}
				return nil
			}
			hook := func(context.Context, store.StoredCommand) error {
				if race == "rotation" {
					current = account.WriteFence{AccountID: fixture.accountID, DesiredStateRevision: 2, RuntimeGeneration: 2, CredentialVersion: 2}
				} else {
					active = false
				}
				return nil
			}
			fixture.coordinator = newTestCoordinator(t, fixture.store, fixture.router, fixture.publisher, Hooks{AfterDispatching: hook})
			if report, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil || !report.WriteReady {
				t.Fatalf("initial recovery=%+v err=%v", report, err)
			}
			request := fixture.placeRequest()
			request.WriteFence = account.WriteFence{AccountID: fixture.accountID, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1}
			if _, err := fixture.coordinator.Place(context.Background(), request); !errors.Is(err, ErrWriteNotReady) {
				t.Fatalf("Place error=%v, want ErrWriteNotReady", err)
			}
			if calls := fixture.driver.PlaceCalls(); calls != 0 {
				t.Fatalf("stale %s fence made %d venue placement calls", race, calls)
			}
		})
	}
}

func TestPromotionWaitsWhileOldGenerationHandoffIsBetweenFenceAndVenue(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if report, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil || !report.WriteReady {
		t.Fatalf("initial recovery=%+v err=%v", report, err)
	}
	admission := newTestHandoffAdmission()
	blocked := &blockingDriver{Driver: fixture.driver, entered: make(chan struct{}), release: make(chan struct{})}
	fixture.router.route.Admission = admission
	fixture.router.route.Driver = blocked

	placed := make(chan error, 1)
	go func() {
		_, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest())
		placed <- err
	}()
	<-blocked.entered
	fixture.store.mu.Lock()
	if len(fixture.store.handoffs) != 1 {
		fixture.store.mu.Unlock()
		t.Fatalf("durable started handoffs=%d want=1", len(fixture.store.handoffs))
	}
	for _, handoff := range fixture.store.handoffs {
		if handoff.State != store.OrderHandoffStarted || handoff.ResolvedAt != nil {
			fixture.store.mu.Unlock()
			t.Fatalf("handoff before venue release=%+v", handoff)
		}
	}
	fixture.store.mu.Unlock()

	promoted := make(chan error, 1)
	go func() { promoted <- admission.CloseAndWait(context.Background()) }()
	select {
	case err := <-promoted:
		t.Fatalf("promotion crossed in-flight handoff: %v", err)
	default:
	}
	close(blocked.release)
	if err := <-placed; err != nil {
		t.Fatal(err)
	}
	if err := <-promoted; err != nil {
		t.Fatal(err)
	}
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	for _, handoff := range fixture.store.handoffs {
		if handoff.State != store.OrderHandoffAccepted || handoff.ResolvedAt == nil {
			t.Fatalf("resolved handoff=%+v", handoff)
		}
	}
}

func TestCoordinatorRejectsIncompleteWriteFenceBeforeReservation(t *testing.T) {
	tests := []struct {
		name  string
		fence account.WriteFence
	}{
		{name: "zero"},
		{name: "missing desired revision", fence: account.WriteFence{AccountID: "binance-main", RuntimeGeneration: 1, CredentialVersion: 1}},
		{name: "missing runtime generation", fence: account.WriteFence{AccountID: "binance-main", DesiredStateRevision: 1, CredentialVersion: 1}},
		{name: "missing credential version", fence: account.WriteFence{AccountID: "binance-main", DesiredStateRevision: 1, RuntimeGeneration: 1}},
		{name: "wrong account", fence: account.WriteFence{AccountID: "binance-other", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCoordinatorFixture(t)
			if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
				t.Fatalf("RecoverAccount: %v", err)
			}
			request := fixture.placeRequest()
			request.WriteFence = test.fence
			if _, err := fixture.coordinator.Place(context.Background(), request); err == nil {
				t.Fatal("Place accepted incomplete write fence")
			}
			if got := fixture.store.commandCount(); got != 0 {
				t.Fatalf("incomplete fence reserved %d commands", got)
			}
			if got := fixture.driver.PlaceCalls(); got != 0 {
				t.Fatalf("incomplete fence made %d venue calls", got)
			}
		})
	}
}

func TestCoordinatorRequiresFinalWriteFenceCheckerBeforeReservation(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.router.route.CheckWriteFence = nil
	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatalf("RecoverAccount: %v", err)
	}
	request := fixture.placeRequest()
	request.WriteFence = account.WriteFence{AccountID: fixture.accountID, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1}
	if _, err := fixture.coordinator.Place(context.Background(), request); !errors.Is(err, ErrWriteNotReady) {
		t.Fatalf("Place error=%v, want ErrWriteNotReady", err)
	}
	if got := fixture.store.commandCount(); got != 0 {
		t.Fatalf("missing checker reserved %d commands", got)
	}
	if got := fixture.driver.PlaceCalls(); got != 0 {
		t.Fatalf("missing checker made %d venue calls", got)
	}
}

func TestCoordinatorRecoveryRejectsPreparedCommandWithoutCompleteFence(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	payload, err := json.Marshal(durablePlacePayload{
		AccountID: fixture.accountID, InstrumentID: "binance:live:spot:BTC-USDT",
		Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: "0.001",
		LimitPrice: "100", LimitPolicy: exchange.LimitPolicyResting,
	})
	if err != nil {
		t.Fatal(err)
	}
	payloadHash, err := domain.CanonicalPayloadHash(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ReserveCommand(context.Background(), store.ReserveCommandRequest{
		PrincipalID: "principal-a", AccountID: fixture.accountID,
		InstrumentID: "binance:live:spot:BTC-USDT", IdempotencyKey: "strategy-a-1",
		CorrelationID: "018f0000-0000-7000-8000-000000000001",
		PayloadHash:   payloadHash, Payload: payload,
	}); err != nil {
		t.Fatalf("ReserveCommand: %v", err)
	}

	report, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID)
	if !errors.Is(err, ErrRecoveryIncomplete) || report.Unresolved != 1 {
		t.Fatalf("RecoverAccount report=%+v err=%v, want one unresolved command", report, err)
	}
	if got := fixture.store.onlyCommand(t).State; got != domain.StatePrepared {
		t.Fatalf("incomplete recovered fence advanced state to %s", got)
	}
	if got := fixture.driver.PlaceCalls(); got != 0 {
		t.Fatalf("incomplete recovered fence made %d venue calls", got)
	}
}

func TestCoordinatorRecoveryRoutesDurableCommandToRecordedRetiringGeneration(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	request := fixture.placeRequest()
	request.RouteID = "spot-retiring"
	request.WriteFence = account.WriteFence{
		AccountID: fixture.accountID, DesiredStateRevision: 7,
		RuntimeGeneration: 11, CredentialVersion: 3,
	}
	payload, payloadHash, err := canonicalPlacePayload(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ReserveCommand(context.Background(), store.ReserveCommandRequest{
		PrincipalID: request.PrincipalID, AccountID: request.AccountID,
		InstrumentID: request.InstrumentID, IdempotencyKey: request.IdempotencyKey,
		CorrelationID: "018f0000-0000-7000-8000-000000000001",
		PayloadHash:   payloadHash, Payload: payload,
	}); err != nil {
		t.Fatalf("ReserveCommand: %v", err)
	}

	activeDriver := &fakeDriver{}
	recoveryRoute := fixture.router.route
	recoveryRoute.CheckWriteFence = func(fence account.WriteFence) error {
		if fence != request.WriteFence {
			return account.ErrStaleWriteFence
		}
		return nil
	}
	router := &recordingFencedRecoveryRouter{
		activeRoute: Route{
			Driver: activeDriver, Instrument: fixture.instrument,
			Product: exchange.ProductSpot, WriteReady: true,
			CheckWriteFence: func(account.WriteFence) error { return nil },
		},
		recoveryRoute: recoveryRoute,
	}
	coordinator := newTestCoordinator(t, fixture.store, router, fixture.publisher, Hooks{})
	report, err := coordinator.RecoverAccount(context.Background(), fixture.accountID)
	if err != nil || !report.WriteReady || report.Resolved != 1 {
		t.Fatalf("RecoverAccount report=%+v err=%v", report, err)
	}

	accountID, instrumentID, routeID, fence, calls := router.RecoveryRequest()
	wantFence := domain.AccountFence{
		AccountID: request.AccountID, DesiredStateRevision: request.WriteFence.DesiredStateRevision,
		RuntimeGeneration: request.WriteFence.RuntimeGeneration, CredentialVersion: request.WriteFence.CredentialVersion,
	}
	if calls != 1 || accountID != request.AccountID || instrumentID != request.InstrumentID || routeID != request.RouteID || fence != wantFence {
		t.Fatalf("recovery request calls=%d account=%s instrument=%s route=%s fence=%+v", calls, accountID, instrumentID, routeID, fence)
	}
	if got := fixture.driver.PlaceCalls(); got != 1 {
		t.Fatalf("retiring generation place calls=%d want=1", got)
	}
	if got := activeDriver.PlaceCalls(); got != 0 {
		t.Fatalf("active generation received recovered command: %d calls", got)
	}
}

func TestCoordinatorSuspendAccountClosesWritesUntilRecovery(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatalf("initial recovery: %v", err)
	}
	if err := fixture.coordinator.SuspendAccount(fixture.accountID); err != nil {
		t.Fatalf("SuspendAccount: %v", err)
	}
	if _, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest()); !errors.Is(err, ErrWriteNotReady) {
		t.Fatalf("Place error=%v, want ErrWriteNotReady", err)
	}
	if fixture.driver.PlaceCalls() != 0 {
		t.Fatalf("suspended account reached venue %d times", fixture.driver.PlaceCalls())
	}
	if report, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil || !report.WriteReady {
		t.Fatalf("recovery=%+v err=%v", report, err)
	}
	if _, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest()); err != nil {
		t.Fatalf("Place after recovery: %v", err)
	}
}

func TestCoordinatorReconcileDueRecoversClosedGateWithoutClaimedTasks(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatalf("initial recovery: %v", err)
	}
	if err := fixture.coordinator.SuspendAccount(fixture.accountID); err != nil {
		t.Fatalf("SuspendAccount: %v", err)
	}

	report, err := fixture.coordinator.ReconcileDue(context.Background(), fixture.accountID)
	if err != nil || !report.WriteReady || report.Claimed != 0 {
		t.Fatalf("ReconcileDue report=%+v err=%v", report, err)
	}
	if _, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest()); err != nil {
		t.Fatalf("Place after idle recovery: %v", err)
	}
}

func TestCoordinatorPersistsOwnedPrivateOrderEventsBeforePublishing(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatal(err)
	}
	placed, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest())
	if err != nil {
		t.Fatal(err)
	}
	beforePublish := len(fixture.publisher.Commands())
	observed, err := fixture.coordinator.ObserveVenueOrder(context.Background(), VenueOrderObservation{
		AccountID: fixture.accountID, InstrumentID: "binance:live:spot:BTC-USDT",
		Product: exchange.ProductSpot,
		Order: exchange.Order{
			NativeSymbol: "BTCUSDT", OrderID: "venue-1", ClientOrderID: placed.ClientOrderID,
			Status: string(domain.StateFilled),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed.State != domain.StateFilled || observed.ResourceVersion <= placed.ResourceVersion {
		t.Fatalf("observed=%+v placed=%+v", observed, placed)
	}
	commands := fixture.publisher.Commands()
	if len(commands) != beforePublish+1 || commands[len(commands)-1].ResourceVersion != observed.ResourceVersion ||
		commands[len(commands)-1].State != domain.StateFilled {
		t.Fatalf("published commands=%+v", commands)
	}
	snapshot, err := fixture.coordinator.Get(context.Background(), fixture.placeRequest().PrincipalID, observed.GatewayOrderID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != observed.State || snapshot.ResourceVersion != observed.ResourceVersion {
		t.Fatalf("REST snapshot=%+v event projection=%+v", snapshot, observed)
	}
	if duplicate, err := fixture.coordinator.ObserveVenueOrder(context.Background(), VenueOrderObservation{
		AccountID: fixture.accountID, InstrumentID: "binance:live:spot:BTC-USDT",
		Product: exchange.ProductSpot,
		Order: exchange.Order{
			NativeSymbol: "BTCUSDT", OrderID: "venue-1", ClientOrderID: placed.ClientOrderID,
			Status: string(domain.StateFilled),
		},
	}); err != nil || duplicate.ResourceVersion != observed.ResourceVersion ||
		len(fixture.publisher.Commands()) != len(commands) {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
}

func TestCoordinatorSurfacesPrivateOrderPublishFailureAfterDurableCommit(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatal(err)
	}
	placed, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest())
	if err != nil {
		t.Fatal(err)
	}
	fixture.publisher.SetError(errors.New("event hub unavailable"))
	observed, err := fixture.coordinator.ObserveVenueOrder(context.Background(), VenueOrderObservation{
		AccountID: fixture.accountID, InstrumentID: "binance:live:spot:BTC-USDT",
		Product: exchange.ProductSpot,
		Order: exchange.Order{
			NativeSymbol: "BTCUSDT", OrderID: "venue-1", ClientOrderID: placed.ClientOrderID,
			Status: string(domain.StateFilled),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "publish committed order event failed") {
		t.Fatalf("ObserveVenueOrder error=%v", err)
	}
	if observed.State != placed.State {
		t.Fatalf("failed publish returned non-current result=%+v", observed)
	}
	snapshot, loadErr := fixture.coordinator.Get(context.Background(), fixture.placeRequest().PrincipalID, placed.GatewayOrderID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if snapshot.State != domain.StateFilled || snapshot.ResourceVersion <= placed.ResourceVersion {
		t.Fatalf("durable snapshot after publish failure=%+v", snapshot)
	}
}

func TestCoordinatorPostCommitPublishIgnoresRequestCancellation(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := store.StoredCommand{
		PrincipalID: "principal-a", AccountID: fixture.accountID,
		State: domain.StateOpen, ResourceVersion: 1,
	}
	if err := fixture.coordinator.publish(ctx, command); err != nil {
		t.Fatalf("publish after request cancellation: %v", err)
	}
	if err := fixture.publisher.LastContextError(); err != nil {
		t.Fatalf("publisher inherited canceled request context: %v", err)
	}
}

func TestCoordinatorShutdownRejectsNewWritesAndWaitsForInFlight(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	blocking := &blockingDriver{
		Driver:  fixture.driver,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	fixture.router.route.Driver = blocking
	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatalf("RecoverAccount: %v", err)
	}

	placeDone := make(chan error, 1)
	go func() {
		_, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest())
		placeDone <- err
	}()
	<-blocking.entered

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- fixture.coordinator.Shutdown(context.Background())
	}()

	deadline := time.Now().Add(time.Second)
	for {
		_, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest())
		if errors.Is(err, ErrCoordinatorClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new Place error = %v, want ErrCoordinatorClosed", err)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before in-flight place completed: %v", err)
	default:
	}

	close(blocking.release)
	if err := <-placeDone; err != nil {
		t.Fatalf("in-flight Place: %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := fixture.coordinator.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

func TestCoordinatorReconcileDueUsesExactReadOnlyEvidenceAndNeverPlacesAgain(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.driver.placeErr = exchange.ErrAmbiguousOutcome
	fixture.driver.recordAccepted = false
	coordinator, err := NewCoordinator(Options{
		Store: fixture.store, Router: fixture.router,
		Identity:                  domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
		Now:                       time.Now,
		ReconciliationDelay:       time.Nanosecond,
		ReconciliationLease:       time.Second,
		ReconciliationLimit:       10,
		MaxReconciliationAttempts: 3,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	fixture.coordinator = coordinator
	if _, err := coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatalf("RecoverAccount: %v", err)
	}
	result, err := coordinator.Place(context.Background(), fixture.placeRequest())
	if !errors.Is(err, ErrOutcomeUncertain) || result.State != domain.StateUncertain {
		t.Fatalf("ambiguous Place result=%+v err=%v", result, err)
	}
	if got := fixture.driver.PlaceCalls(); got != 1 {
		t.Fatalf("initial driver place calls=%d, want 1", got)
	}

	fixture.driver.mu.Lock()
	fixture.driver.placeErr = nil
	fixture.driver.orders[result.ClientOrderID] = foundExactResult(
		fixture.instrument, result.ClientOrderID, "venue-recovered", domain.StateOpen,
	)
	fixture.driver.mu.Unlock()
	time.Sleep(time.Millisecond)

	report, err := coordinator.ReconcileDue(context.Background(), fixture.accountID)
	if err != nil || report.Claimed != 1 || report.Resolved != 1 || report.Unresolved != 0 {
		t.Fatalf("ReconcileDue report=%+v err=%v", report, err)
	}
	if got := fixture.driver.PlaceCalls(); got != 1 {
		t.Fatalf("reconciliation repeated venue write: %d calls", got)
	}
	if got := fixture.store.taskCount(); got != 0 {
		t.Fatalf("resolved reconciliation tasks=%d, want 0", got)
	}
}

func TestCoordinatorPeriodicallyReconcilesAccountRegisteredAfterZeroAccountStart(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.driver.placeErr = exchange.ErrAmbiguousOutcome
	fixture.driver.recordAccepted = false
	coordinator, err := NewCoordinator(Options{
		Store: fixture.store, Router: fixture.router,
		Identity:                  domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
		Now:                       time.Now,
		ReconciliationDelay:       time.Nanosecond,
		ReconciliationInterval:    time.Millisecond,
		ReconciliationLease:       time.Second,
		ReconciliationLimit:       10,
		MaxReconciliationAttempts: 3,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	t.Cleanup(func() {
		if err := coordinator.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	if err := coordinator.Start(t.Context(), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := coordinator.RegisterAccount(fixture.accountID); err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}
	if _, err := coordinator.RecoverAccount(t.Context(), fixture.accountID); err != nil {
		t.Fatalf("RecoverAccount: %v", err)
	}

	result, err := coordinator.Place(t.Context(), fixture.placeRequest())
	if !errors.Is(err, ErrOutcomeUncertain) || result.State != domain.StateUncertain {
		t.Fatalf("ambiguous Place result=%+v err=%v", result, err)
	}
	fixture.driver.mu.Lock()
	fixture.driver.placeErr = nil
	fixture.driver.orders[result.ClientOrderID] = foundExactResult(
		fixture.instrument, result.ClientOrderID, "venue-recovered", domain.StateOpen,
	)
	fixture.driver.mu.Unlock()

	deadline := time.Now().Add(time.Second)
	for fixture.store.taskCount() != 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := fixture.store.taskCount(); got != 0 {
		t.Fatalf("dynamic account reconciliation tasks=%d, want 0", got)
	}
	if got := fixture.driver.PlaceCalls(); got != 1 {
		t.Fatalf("periodic reconciliation repeated venue write: %d calls", got)
	}
}

func TestCoordinatorStartRejectsInvalidAccountListWithoutPartialRegistration(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if err := fixture.coordinator.Start(t.Context(), []domain.AccountID{fixture.accountID, ""}); err == nil {
		t.Fatal("Start accepted invalid account list")
	}
	if got := fixture.coordinator.reconciliationAccountIDs(); len(got) != 0 {
		t.Fatalf("failed Start retained reconciliation accounts=%v", got)
	}
}

func TestCoordinatorPrincipalScopedGetListAndCancelAreDurableAndIdempotent(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatal(err)
	}
	placed, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest())
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	got, err := fixture.coordinator.Get(context.Background(), "principal-a", placed.GatewayOrderID)
	if err != nil || got.GatewayOrderID != placed.GatewayOrderID || got.State != domain.StateOpen {
		t.Fatalf("Get result=%+v err=%v", got, err)
	}
	if _, err := fixture.coordinator.Get(context.Background(), "principal-b", placed.GatewayOrderID); err == nil {
		t.Fatal("another principal read the order")
	}
	list, err := fixture.coordinator.List(context.Background(), ListRequest{
		PrincipalID: "principal-a", AccountID: fixture.accountID, OpenOnly: true, Limit: 10,
	})
	if err != nil || len(list) != 1 || list[0].GatewayOrderID != placed.GatewayOrderID {
		t.Fatalf("List result=%+v err=%v", list, err)
	}

	canceled, err := fixture.coordinator.Cancel(context.Background(), CancelRequest{
		PrincipalID: "principal-a", GatewayOrderID: placed.GatewayOrderID,
	})
	if err != nil || canceled.State != domain.StateCanceled {
		t.Fatalf("Cancel result=%+v err=%v", canceled, err)
	}
	if got := fixture.driver.CancelCalls(); got != 1 {
		t.Fatalf("cancel driver calls=%d, want 1", got)
	}
	replayed, err := fixture.coordinator.Cancel(context.Background(), CancelRequest{
		PrincipalID: "principal-a", GatewayOrderID: placed.GatewayOrderID,
	})
	if err != nil || replayed.State != domain.StateCanceled {
		t.Fatalf("second Cancel result=%+v err=%v", replayed, err)
	}
	if got := fixture.driver.CancelCalls(); got != 1 {
		t.Fatalf("terminal cancel repeated venue call: %d", got)
	}
}

func TestCoordinatorTerminalPrivateEventReopensGateAfterAmbiguousCancel(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatal(err)
	}
	placed, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest())
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	fixture.router.mu.Lock()
	fixture.router.route.Driver = ambiguousCancelDriver{Driver: fixture.driver}
	fixture.router.mu.Unlock()

	result, err := fixture.coordinator.Cancel(context.Background(), CancelRequest{
		PrincipalID: "principal-a", GatewayOrderID: placed.GatewayOrderID,
	})
	if !errors.Is(err, ErrOutcomeUncertain) || result.State != domain.StateUncertain {
		t.Fatalf("ambiguous Cancel result=%+v err=%v", result, err)
	}
	observed, err := fixture.coordinator.ObserveVenueOrder(context.Background(), VenueOrderObservation{
		AccountID: fixture.accountID, InstrumentID: "binance:live:spot:BTC-USDT",
		Product: exchange.ProductSpot,
		Order: exchange.Order{
			NativeSymbol: "BTCUSDT", OrderID: "venue-1", ClientOrderID: placed.ClientOrderID,
			Status: string(domain.StateCanceled),
		},
	})
	if err != nil || observed.State != domain.StateCanceled {
		t.Fatalf("ObserveVenueOrder result=%+v err=%v", observed, err)
	}

	next := fixture.placeRequest()
	next.IdempotencyKey = "strategy-a-2"
	if _, err := fixture.coordinator.Place(context.Background(), next); err != nil {
		t.Fatalf("Place after exact terminal private event: %v", err)
	}
}

func TestCoordinatorRestartWindowsNeverDuplicateVenueWrite(t *testing.T) {
	tests := []struct {
		name           string
		hooks          Hooks
		wantCallsFirst int
		wantCallsTotal int
		wantFinal      domain.OrderState
	}{
		{
			name:           "prepared committed before dispatch claim",
			hooks:          Hooks{AfterPrepared: stopHook("after prepared")},
			wantCallsFirst: 0,
			wantCallsTotal: 1,
			wantFinal:      domain.StateOpen,
		},
		{
			name:           "dispatching committed before venue byte",
			hooks:          Hooks{AfterDispatching: stopHook("after dispatching")},
			wantCallsFirst: 0,
			wantCallsTotal: 0,
			wantFinal:      domain.StateRejected,
		},
		{
			name:           "venue accepted before result commit",
			hooks:          Hooks{AfterVenue: stopHook("after venue")},
			wantCallsFirst: 1,
			wantCallsTotal: 1,
			wantFinal:      domain.StateOpen,
		},
		{
			name:           "result committed before event publication",
			hooks:          Hooks{AfterCommitted: stopHook("after committed")},
			wantCallsFirst: 1,
			wantCallsTotal: 1,
			wantFinal:      domain.StateOpen,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCoordinatorFixture(t)
			fixture.coordinator = newTestCoordinator(t, fixture.store, fixture.router, fixture.publisher, test.hooks)
			if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
				t.Fatal(err)
			}
			result, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest())
			if err == nil || !errors.Is(err, errInjectedStop) {
				t.Fatalf("Place result=%+v err=%v, want injected stop", result, err)
			}
			if got := fixture.driver.PlaceCalls(); got != test.wantCallsFirst {
				t.Fatalf("first process driver calls=%d, want %d", got, test.wantCallsFirst)
			}

			restarted := newTestCoordinator(t, fixture.store, fixture.router, fixture.publisher, Hooks{})
			report, recoverErr := restarted.RecoverAccount(context.Background(), fixture.accountID)
			if recoverErr != nil {
				t.Fatalf("RecoverAccount: %v", recoverErr)
			}
			stored := fixture.store.onlyCommand(t)
			if stored.State != test.wantFinal {
				t.Fatalf("recovered state=%s, want %s; report=%+v", stored.State, test.wantFinal, report)
			}
			if got := fixture.driver.PlaceCalls(); got != test.wantCallsTotal {
				t.Fatalf("total driver place calls=%d, want %d", got, test.wantCallsTotal)
			}
			if !report.WriteReady {
				t.Fatalf("resolved recovery did not restore readiness: %+v", report)
			}
		})
	}
}

func TestCoordinatorPostReservationFailureClosesAccountWriteGate(t *testing.T) {
	tests := []struct {
		name  string
		hooks Hooks
	}{
		{name: "after prepared", hooks: Hooks{AfterPrepared: stopHook("after prepared")}},
		{name: "after dispatching", hooks: Hooks{AfterDispatching: stopHook("after dispatching")}},
		{name: "after venue", hooks: Hooks{AfterVenue: stopHook("after venue")}},
		{name: "after committed", hooks: Hooks{AfterCommitted: stopHook("after committed")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCoordinatorFixture(t)
			fixture.coordinator = newTestCoordinator(t, fixture.store, fixture.router, fixture.publisher, test.hooks)
			if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest()); !errors.Is(err, errInjectedStop) {
				t.Fatalf("fault injection error = %v", err)
			}
			next := fixture.placeRequest()
			next.IdempotencyKey = "strategy-a-2"
			if _, err := fixture.coordinator.Place(context.Background(), next); !errors.Is(err, ErrWriteNotReady) {
				t.Fatalf("post-fault write gate error = %v, want ErrWriteNotReady", err)
			}
		})
	}
}

func TestCoordinatorAmbiguousOutcomeStaysReservedUntilExactEvidence(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatal(err)
	}
	fixture.driver.mu.Lock()
	fixture.driver.placeAck.State = exchange.AckAmbiguous
	fixture.driver.placeAck.OrderID = ""
	fixture.driver.placeErr = exchange.ErrAmbiguousOutcome
	fixture.driver.recordAccepted = false
	fixture.driver.lookupFallback = unconfirmedExactResult(fixture.instrument)
	fixture.driver.mu.Unlock()

	result, err := fixture.coordinator.Place(context.Background(), fixture.placeRequest())
	if !errors.Is(err, ErrOutcomeUncertain) || result.State != domain.StateUncertain {
		t.Fatalf("ambiguous Place result=%+v err=%v", result, err)
	}
	if got := fixture.driver.PlaceCalls(); got != 1 {
		t.Fatalf("driver calls=%d, want 1", got)
	}
	task := fixture.store.onlyTask(t)
	if task.Command.GatewayOrderID != result.GatewayOrderID ||
		task.Reason != store.ReconciliationAmbiguousHandoff || task.Attempts != 0 {
		t.Fatalf("initial reconciliation task = %+v", task)
	}

	restarted := newTestCoordinator(t, fixture.store, fixture.router, fixture.publisher, Hooks{})
	report, err := restarted.RecoverAccount(context.Background(), fixture.accountID)
	if err == nil || report.WriteReady || report.Unresolved != 1 {
		t.Fatalf("unconfirmed recovery report=%+v err=%v", report, err)
	}
	if _, err := restarted.Place(context.Background(), fixture.placeRequest()); !errors.Is(err, ErrWriteNotReady) {
		t.Fatalf("idempotency reservation became writable during uncertainty: %v", err)
	}
	if got := fixture.driver.PlaceCalls(); got != 1 {
		t.Fatalf("uncertain replay called driver again: %d", got)
	}
	if task := fixture.store.onlyTask(t); task.Reason != store.ReconciliationEvidenceUnconfirmed {
		t.Fatalf("unconfirmed task reason = %q", task.Reason)
	}

	fixture.driver.mu.Lock()
	fixture.driver.lookupFallback = foundExactResult(fixture.instrument, "1", "venue-1", domain.StateOpen)
	fixture.driver.mu.Unlock()
	report, err = restarted.RecoverAccount(context.Background(), fixture.accountID)
	if err != nil || !report.WriteReady || report.Unresolved != 0 {
		t.Fatalf("exact recovery report=%+v err=%v", report, err)
	}
	if got := fixture.store.onlyCommand(t).State; got != domain.StateOpen {
		t.Fatalf("exact recovery state=%s, want open", got)
	}
	if got := fixture.driver.PlaceCalls(); got != 1 {
		t.Fatalf("exact recovery duplicated write: %d", got)
	}
	if got := fixture.store.taskCount(); got != 0 {
		t.Fatalf("resolved reconciliation tasks = %d, want 0", got)
	}
}

func TestCoordinatorFailsBeforeDriverWhenRouteOrReservationIsUnavailable(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	request := fixture.placeRequest()
	if _, err := fixture.coordinator.Place(context.Background(), request); !errors.Is(err, ErrWriteNotReady) {
		t.Fatalf("Place before recovery error=%v, want ErrWriteNotReady", err)
	}
	if fixture.driver.PlaceCalls() != 0 {
		t.Fatal("write-before-recovery reached driver")
	}

	if _, err := fixture.coordinator.RecoverAccount(context.Background(), fixture.accountID); err != nil {
		t.Fatal(err)
	}
	fixture.router.mu.Lock()
	fixture.router.route.WriteReady = false
	fixture.router.route.Reason = "capability_not_promoted"
	fixture.router.mu.Unlock()
	if _, err := fixture.coordinator.Place(context.Background(), request); !errors.Is(err, ErrWriteNotReady) {
		t.Fatalf("capability gate error=%v", err)
	}
	if fixture.driver.PlaceCalls() != 0 {
		t.Fatal("capability failure reached driver")
	}

	fixture.router.mu.Lock()
	fixture.router.route.WriteReady = true
	fixture.router.mu.Unlock()
	fixture.store.mu.Lock()
	fixture.store.reserveErr = errors.New("postgres unavailable with secret canary")
	fixture.store.mu.Unlock()
	if _, err := fixture.coordinator.Place(context.Background(), request); err == nil || stringsContainCanary(err.Error()) {
		t.Fatalf("reservation failure error=%v", err)
	}
	if fixture.driver.PlaceCalls() != 0 {
		t.Fatal("reservation failure reached driver")
	}
}

func TestCanonicalPayloadNormalizesDecimalSpellingsAndExcludesSecrets(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	left := fixture.placeRequest()
	right := left
	left.Quantity = decimal.RequireFromString("1.000")
	right.Quantity = decimal.RequireFromString("1")
	left.LimitPrice = decimal.RequireFromString("100.00")
	right.LimitPrice = decimal.RequireFromString("100")

	leftPayload, leftHash, err := canonicalPlacePayload(left)
	if err != nil {
		t.Fatal(err)
	}
	rightPayload, rightHash, err := canonicalPlacePayload(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash || string(leftPayload) != string(rightPayload) {
		t.Fatalf("semantic decimals changed payload: %s/%s vs %s/%s", leftPayload, leftHash, rightPayload, rightHash)
	}
	if stringsContainCanary(string(leftPayload)) {
		t.Fatalf("durable payload contains secret canary: %s", leftPayload)
	}
	var decoded map[string]any
	if err := json.Unmarshal(leftPayload, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["principal_id"]; ok {
		t.Fatalf("durable order payload contains principal: %s", leftPayload)
	}
}

var errInjectedStop = errors.New("injected process stop")

func stopHook(_ string) func(context.Context, store.StoredCommand) error {
	return func(context.Context, store.StoredCommand) error { return errInjectedStop }
}

type coordinatorFixture struct {
	accountID   domain.AccountID
	instrument  exchange.Instrument
	store       *memoryCommandStore
	driver      *fakeDriver
	router      *fakeRouter
	publisher   *recordingPublisher
	coordinator *Coordinator
}

func newCoordinatorFixture(t *testing.T) *coordinatorFixture {
	t.Helper()
	instrument := exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: exchange.ProductSpot, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement: decimal.RequireFromString("0.01"), QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity: decimal.RequireFromString("0.001"),
		},
	}
	driver := &fakeDriver{
		orders: make(map[string]ExactResult),
		placeAck: exchange.OrderAcknowledgement{
			Venue: exchange.VenueBinance, Product: exchange.ProductSpot, Operation: exchange.OrderOperationPlace,
			State: exchange.AckResting, NativeSymbol: "BTCUSDT", OrderType: exchange.OrderTypeLimit,
			OrderID: "venue-1", ClientOrderID: "1",
		},
		recordAccepted: true,
		lookupFallback: notFoundExactResult(instrument, "1"),
	}
	router := &fakeRouter{route: Route{
		Driver: driver, Instrument: instrument, Product: exchange.ProductSpot, WriteReady: true,
		CheckWriteFence: func(account.WriteFence) error { return nil },
	}}
	store := newMemoryCommandStore()
	publisher := &recordingPublisher{}
	fixture := &coordinatorFixture{
		accountID: "binance-main", instrument: instrument, store: store,
		driver: driver, router: router, publisher: publisher,
	}
	fixture.coordinator = newTestCoordinator(t, store, router, publisher, Hooks{})
	return fixture
}

func (fixture *coordinatorFixture) placeRequest() PlaceRequest {
	return PlaceRequest{
		PrincipalID: "principal-a", AccountID: fixture.accountID,
		RouteID:      "spot",
		InstrumentID: "binance:live:spot:BTC-USDT", IdempotencyKey: "strategy-a-1",
		Side: exchange.SideBuy, Type: exchange.OrderTypeLimit,
		Quantity: decimal.RequireFromString("0.001"), LimitPrice: decimal.RequireFromString("100"),
		LimitPolicy: exchange.LimitPolicyResting,
		WriteFence:  account.WriteFence{AccountID: fixture.accountID, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
	}
}

func newTestCoordinator(t *testing.T, commandStore store.CommandStore, router Router, publisher Publisher, hooks Hooks) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(Options{
		Store: commandStore, Router: router, Publisher: publisher,
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}), Hooks: hooks,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return coordinator
}

type fakeRouter struct {
	mu    sync.Mutex
	route Route
	err   error
}

func (router *fakeRouter) Route(context.Context, domain.AccountID, domain.InstrumentID) (Route, error) {
	router.mu.Lock()
	defer router.mu.Unlock()
	return router.route, router.err
}

type recordingFencedRecoveryRouter struct {
	mu            sync.Mutex
	activeRoute   Route
	recoveryRoute Route
	accountID     domain.AccountID
	instrumentID  domain.InstrumentID
	routeID       domain.RouteID
	fence         domain.AccountFence
	recoveryCalls int
}

func (router *recordingFencedRecoveryRouter) Route(context.Context, domain.AccountID, domain.InstrumentID) (Route, error) {
	return router.activeRoute, nil
}

func (router *recordingFencedRecoveryRouter) RouteForRecoveryCommand(
	_ context.Context,
	accountID domain.AccountID,
	instrumentID domain.InstrumentID,
	routeID domain.RouteID,
	fence domain.AccountFence,
) (Route, error) {
	router.mu.Lock()
	defer router.mu.Unlock()
	router.accountID = accountID
	router.instrumentID = instrumentID
	router.routeID = routeID
	router.fence = fence
	router.recoveryCalls++
	return router.recoveryRoute, nil
}

func (router *recordingFencedRecoveryRouter) RecoveryRequest() (
	domain.AccountID,
	domain.InstrumentID,
	domain.RouteID,
	domain.AccountFence,
	int,
) {
	router.mu.Lock()
	defer router.mu.Unlock()
	return router.accountID, router.instrumentID, router.routeID, router.fence, router.recoveryCalls
}

type fakeDriver struct {
	mu             sync.Mutex
	placeCalls     int
	leverageCalls  int
	leverage       int
	sequence       string
	cancelCalls    int
	placeAck       exchange.OrderAcknowledgement
	placeErr       error
	recordAccepted bool
	orders         map[string]ExactResult
	lookupFallback ExactResult
	lookupErr      error
}

type blockingDriver struct {
	Driver
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type testHandoffAdmission struct {
	mu       sync.Mutex
	closed   bool
	inflight int
	drained  chan struct{}
}

type testHandoffLease struct {
	once    sync.Once
	release func()
}

func (lease *testHandoffLease) Release() { lease.once.Do(lease.release) }

func newTestHandoffAdmission() *testHandoffAdmission {
	return &testHandoffAdmission{drained: make(chan struct{})}
}

func (admission *testHandoffAdmission) Acquire(domain.WriteFence) (account.HandoffLease, error) {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if admission.closed {
		return nil, account.ErrRuntimeBlocked
	}
	admission.inflight++
	return &testHandoffLease{release: func() {
		admission.mu.Lock()
		defer admission.mu.Unlock()
		admission.inflight--
		if admission.closed && admission.inflight == 0 {
			close(admission.drained)
		}
	}}, nil
}

func (admission *testHandoffAdmission) CloseAndWait(ctx context.Context) error {
	admission.mu.Lock()
	admission.closed = true
	if admission.inflight == 0 {
		close(admission.drained)
	}
	drained := admission.drained
	admission.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type ambiguousCancelDriver struct {
	Driver
}

func (driver ambiguousCancelDriver) CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return exchange.OrderAcknowledgement{}, exchange.ErrAmbiguousOutcome
}

func (driver *blockingDriver) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	driver.once.Do(func() { close(driver.entered) })
	select {
	case <-ctx.Done():
		return exchange.OrderAcknowledgement{}, ctx.Err()
	case <-driver.release:
		return driver.Driver.PlaceOrder(ctx, request)
	}
}

func (driver *fakeDriver) PlaceOrder(_ context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	driver.placeCalls++
	if driver.sequence != "" {
		driver.sequence += ","
	}
	driver.sequence += "order"
	ack := driver.placeAck
	ack.ClientOrderID = request.ClientOrderID
	if driver.recordAccepted {
		driver.orders[request.ClientOrderID] = foundExactResult(request.Instrument, request.ClientOrderID, ack.OrderID, domain.StateOpen)
	}
	return ack, driver.placeErr
}

func (driver *fakeDriver) SetLeverage(_ context.Context, _ exchange.Instrument, leverage int) error {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	driver.leverageCalls++
	driver.leverage = leverage
	if driver.sequence != "" {
		driver.sequence += ","
	}
	driver.sequence += "leverage"
	return nil
}

func (driver *fakeDriver) CancelOrder(_ context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	driver.cancelCalls++
	return exchange.OrderAcknowledgement{
		Venue: exchange.VenueBinance, Product: exchange.ProductSpot,
		Operation: exchange.OrderOperationCancel, State: exchange.AckCanceled,
		NativeSymbol: request.Instrument.Identity.NativeSymbol, OrderID: request.OrderID,
	}, nil
}

func (driver *fakeDriver) CancelCalls() int {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.cancelCalls
}

func (driver *fakeDriver) LookupExactOrder(_ context.Context, request exchange.ExactOrderLookupRequest) (ExactResult, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.lookupErr != nil {
		return ExactResult{}, driver.lookupErr
	}
	if result, ok := driver.orders[request.ClientOrderID]; ok {
		return result, nil
	}
	if request.VenueOrderID != "" {
		for _, result := range driver.orders {
			if result.Order.OrderID == request.VenueOrderID {
				return result, nil
			}
		}
	}
	return driver.lookupFallback, nil
}

func (driver *fakeDriver) PlaceCalls() int {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.placeCalls
}

func foundExactResult(instrument exchange.Instrument, clientID, venueID string, state domain.OrderState) ExactResult {
	return ExactResult{
		Status: exchange.ExactOrderFound,
		State:  state,
		Order:  exchange.Order{NativeSymbol: instrument.Identity.NativeSymbol, OrderID: venueID, ClientOrderID: clientID, Status: string(state)},
		Evidence: exchange.ExactOrderEvidence{
			Scope:  exchange.ExactOrderScope{Venue: instrument.Identity.Venue, Product: instrument.Identity.Product, Instrument: &instrument},
			Method: exchange.ExactOrderEvidenceByClientOrderID, Authoritative: true, Complete: true,
			ObservedClientOrderID: clientID,
		},
	}
}

func notFoundExactResult(instrument exchange.Instrument, clientID string) ExactResult {
	return ExactResult{
		Status: exchange.ExactOrderNotFound,
		Evidence: exchange.ExactOrderEvidence{
			Scope:  exchange.ExactOrderScope{Venue: instrument.Identity.Venue, Product: instrument.Identity.Product, Instrument: &instrument},
			Method: exchange.ExactOrderEvidenceByClientOrderID, Authoritative: true, Complete: true,
			ObservedClientOrderID: clientID,
		},
	}
}

func unconfirmedExactResult(instrument exchange.Instrument) ExactResult {
	return ExactResult{
		Status: exchange.ExactOrderUnconfirmed,
		Evidence: exchange.ExactOrderEvidence{
			Scope:  exchange.ExactOrderScope{Venue: instrument.Identity.Venue, Product: instrument.Identity.Product, Instrument: &instrument},
			Method: exchange.ExactOrderEvidenceByOpenOrders,
		},
	}
}

type recordingPublisher struct {
	mu         sync.Mutex
	commands   []store.StoredCommand
	err        error
	contextErr error
}

func (publisher *recordingPublisher) PublishCommitted(ctx context.Context, command store.StoredCommand) error {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.contextErr = ctx.Err()
	if publisher.err != nil {
		return publisher.err
	}
	publisher.commands = append(publisher.commands, command)
	return nil
}

func (publisher *recordingPublisher) Commands() []store.StoredCommand {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return append([]store.StoredCommand(nil), publisher.commands...)
}

func (publisher *recordingPublisher) SetError(err error) {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.err = err
}

func (publisher *recordingPublisher) LastContextError() error {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return publisher.contextErr
}

type memoryCommandStore struct {
	mu         sync.Mutex
	seq        int
	byKey      map[string]*memoryRecord
	byCommand  map[domain.CommandID]*memoryRecord
	tasks      map[domain.GatewayOrderID]store.ReconciliationTask
	handoffs   map[domain.EventID]store.OrderHandoff
	reserveErr error
}

type memoryRecord struct {
	principal domain.PrincipalID
	key       domain.IdempotencyKey
	hash      string
	command   store.StoredCommand
}

func newMemoryCommandStore() *memoryCommandStore {
	return &memoryCommandStore{
		byKey: make(map[string]*memoryRecord), byCommand: make(map[domain.CommandID]*memoryRecord),
		tasks:    make(map[domain.GatewayOrderID]store.ReconciliationTask),
		handoffs: make(map[domain.EventID]store.OrderHandoff),
	}
}

func (memory *memoryCommandStore) BeginOrderHandoff(_ context.Context, request store.BeginOrderHandoffRequest) (store.OrderHandoff, error) {
	if err := request.Validate(); err != nil {
		return store.OrderHandoff{}, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if existing, ok := memory.handoffs[request.HandoffID]; ok {
		return existing, nil
	}
	handoff := store.OrderHandoff{BeginOrderHandoffRequest: request, State: store.OrderHandoffStarted, StartedAt: time.Now()}
	memory.handoffs[request.HandoffID] = handoff
	return handoff, nil
}

func (memory *memoryCommandStore) ResolveOrderHandoff(_ context.Context, request store.ResolveOrderHandoffRequest) (store.OrderHandoff, error) {
	if err := request.Validate(); err != nil {
		return store.OrderHandoff{}, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	handoff, ok := memory.handoffs[request.HandoffID]
	if !ok {
		return store.OrderHandoff{}, store.ErrNotFound
	}
	now := time.Now()
	handoff.State, handoff.ResolvedAt = request.State, &now
	memory.handoffs[request.HandoffID] = handoff
	return handoff, nil
}

func (memory *memoryCommandStore) ReserveCommand(_ context.Context, request store.ReserveCommandRequest) (store.ReservedCommand, error) {
	if err := request.Validate(); err != nil {
		return store.ReservedCommand{}, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.reserveErr != nil {
		return store.ReservedCommand{}, errors.New("reserve command failed")
	}
	key := string(request.PrincipalID) + "\x00" + string(request.IdempotencyKey)
	if existing := memory.byKey[key]; existing != nil {
		conflict := store.ConflictNone
		switch {
		case existing.command.AccountID != request.AccountID:
			conflict = store.ConflictAccount
		case existing.command.InstrumentID != request.InstrumentID:
			conflict = store.ConflictInstrument
		case existing.hash != request.PayloadHash:
			conflict = store.ConflictPayload
		}
		return storedReservation(existing.command, false, conflict), nil
	}
	memory.seq++
	commandID := domain.CommandID(fmt.Sprintf("018f0000-0000-7000-8000-%012x", memory.seq))
	orderID := domain.GatewayOrderID(fmt.Sprintf("018f0000-0000-7000-9000-%012x", memory.seq))
	command := store.StoredCommand{
		CommandID: commandID, GatewayOrderID: orderID, AccountID: request.AccountID,
		InstrumentID: request.InstrumentID, ClientOrderID: fmt.Sprintf("%d", memory.seq),
		State: domain.StatePrepared, ResourceVersion: 0, Payload: append(json.RawMessage(nil), request.Payload...),
	}
	record := &memoryRecord{principal: request.PrincipalID, key: request.IdempotencyKey, hash: request.PayloadHash, command: command}
	memory.byKey[key], memory.byCommand[commandID] = record, record
	return storedReservation(command, true, store.ConflictNone), nil
}

func (memory *memoryCommandStore) MarkDispatching(_ context.Context, request store.MarkDispatchingRequest) (store.StoredCommand, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	record := memory.byCommand[request.CommandID]
	if record == nil || record.command.State != domain.StatePrepared {
		return store.StoredCommand{}, errors.New("command conflict")
	}
	record.command.State = domain.StateDispatching
	record.command.ResourceVersion++
	record.command.EventID = request.EventID
	return cloneStored(record.command), nil
}

func (memory *memoryCommandStore) RecordVenueResult(_ context.Context, request store.RecordVenueResultRequest) (store.StoredCommand, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	record := memory.byCommand[request.CommandID]
	if record == nil || record.command.ResourceVersion != request.ResourceVersion {
		return store.StoredCommand{}, errors.New("command conflict")
	}
	if err := domain.ValidateOrderTransition(record.command.State, request.NextState); err != nil {
		return store.StoredCommand{}, err
	}
	if request.NextState == domain.StateUncertain {
		if request.Reconciliation == nil || request.Reconciliation.Validate() != nil {
			return store.StoredCommand{}, errors.New("uncertain transition requires reconciliation plan")
		}
	} else if request.Reconciliation != nil {
		return store.StoredCommand{}, errors.New("unexpected reconciliation plan")
	}
	record.command.State = request.NextState
	record.command.ResourceVersion++
	record.command.EventID = request.EventID
	if request.VenueOrderID != "" {
		record.command.VenueOrderID = request.VenueOrderID
	}
	if request.NextState == domain.StateUncertain {
		memory.tasks[record.command.GatewayOrderID] = store.ReconciliationTask{
			TaskID: request.Reconciliation.TaskID, Command: cloneStored(record.command),
			Reason: request.Reconciliation.Reason, NextAttemptAt: request.Reconciliation.NextAttemptAt,
		}
	} else {
		delete(memory.tasks, record.command.GatewayOrderID)
	}
	return cloneStored(record.command), nil
}

func (memory *memoryCommandStore) LoadRecoverableCommands(_ context.Context, request store.LoadRecoverableCommandsRequest) ([]store.StoredCommand, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	result := make([]store.StoredCommand, 0)
	for _, record := range memory.byCommand {
		if record.command.AccountID == request.AccountID && record.command.State != domain.StateFilled && record.command.State != domain.StateCanceled && record.command.State != domain.StateRejected {
			result = append(result, cloneStored(record.command))
		}
	}
	return result, nil
}

func (memory *memoryCommandStore) LoadOrder(_ context.Context, request store.LoadOrderRequest) (store.StoredCommand, error) {
	if err := request.Validate(); err != nil {
		return store.StoredCommand{}, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	for _, record := range memory.byCommand {
		if record.command.GatewayOrderID == request.GatewayOrderID && record.principal == request.PrincipalID {
			command := cloneStored(record.command)
			command.PrincipalID = record.principal
			return command, nil
		}
	}
	return store.StoredCommand{}, errors.New("order not found")
}

func (memory *memoryCommandStore) LoadOrderByVenueIdentity(_ context.Context, request store.LoadOrderByVenueIdentityRequest) (store.StoredCommand, error) {
	if err := request.Validate(); err != nil {
		return store.StoredCommand{}, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	for _, record := range memory.byCommand {
		parts := strings.Split(string(record.command.InstrumentID), ":")
		product := ""
		if len(parts) >= 3 {
			product = parts[2]
		}
		if record.command.AccountID != request.AccountID || product != request.Product {
			continue
		}
		if request.ClientOrderID != "" && record.command.ClientOrderID != request.ClientOrderID {
			continue
		}
		if request.ClientOrderID == "" && record.command.VenueOrderID != request.VenueOrderID {
			continue
		}
		command := cloneStored(record.command)
		command.PrincipalID = record.principal
		return command, nil
	}
	return store.StoredCommand{}, store.ErrNotFound
}

func (memory *memoryCommandStore) ListOrders(_ context.Context, request store.ListOrdersRequest) ([]store.StoredCommand, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	result := make([]store.StoredCommand, 0, request.Limit)
	for _, record := range memory.byCommand {
		command := record.command
		if record.principal != request.PrincipalID ||
			request.AccountID != "" && command.AccountID != request.AccountID ||
			request.InstrumentID != "" && command.InstrumentID != request.InstrumentID ||
			request.OpenOnly && (command.State == domain.StateFilled || command.State == domain.StateCanceled || command.State == domain.StateRejected) {
			continue
		}
		command.PrincipalID = record.principal
		result = append(result, cloneStored(command))
		if len(result) == request.Limit {
			break
		}
	}
	return result, nil
}

func (memory *memoryCommandStore) RescheduleReconciliation(_ context.Context, request store.RescheduleReconciliationRequest) (store.ReconciliationTask, error) {
	if err := request.Validate(); err != nil {
		return store.ReconciliationTask{}, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	task, ok := memory.tasks[request.GatewayOrderID]
	if !ok {
		return store.ReconciliationTask{}, errors.New("reconciliation task not found")
	}
	task.Reason = request.Plan.Reason
	task.NextAttemptAt = request.Plan.NextAttemptAt
	memory.tasks[request.GatewayOrderID] = task
	return cloneTask(task), nil
}

func (memory *memoryCommandStore) ClaimDueReconciliationTasks(_ context.Context, request store.ClaimDueReconciliationTasksRequest) ([]store.ReconciliationTask, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	tasks := make([]store.ReconciliationTask, 0, request.Limit)
	for orderID, task := range memory.tasks {
		if len(tasks) >= request.Limit {
			break
		}
		if task.Command.AccountID != request.AccountID || task.NextAttemptAt.After(request.Now) || task.Attempts >= request.MaxAttempts {
			continue
		}
		task.Attempts++
		task.NextAttemptAt = request.LeaseUntil
		memory.tasks[orderID] = task
		tasks = append(tasks, cloneTask(task))
	}
	return tasks, nil
}

func (memory *memoryCommandStore) onlyCommand(t *testing.T) store.StoredCommand {
	t.Helper()
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if len(memory.byCommand) != 1 {
		t.Fatalf("stored commands=%d, want 1", len(memory.byCommand))
	}
	for _, record := range memory.byCommand {
		return cloneStored(record.command)
	}
	return store.StoredCommand{}
}

func (memory *memoryCommandStore) commandCount() int {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return len(memory.byCommand)
}

func (memory *memoryCommandStore) onlyTask(t *testing.T) store.ReconciliationTask {
	t.Helper()
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if len(memory.tasks) != 1 {
		t.Fatalf("reconciliation tasks=%d, want 1", len(memory.tasks))
	}
	for _, task := range memory.tasks {
		return cloneTask(task)
	}
	return store.ReconciliationTask{}
}

func (memory *memoryCommandStore) taskCount() int {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return len(memory.tasks)
}

func storedReservation(command store.StoredCommand, fresh bool, conflict store.IdempotencyConflict) store.ReservedCommand {
	return store.ReservedCommand{
		CommandID: command.CommandID, GatewayOrderID: command.GatewayOrderID,
		ClientOrderID: command.ClientOrderID, State: command.State,
		ResourceVersion: command.ResourceVersion, VenueOrderID: command.VenueOrderID,
		New: fresh, Conflict: conflict,
	}
}

func cloneStored(command store.StoredCommand) store.StoredCommand {
	command.Payload = append(json.RawMessage(nil), command.Payload...)
	return command
}

func cloneTask(task store.ReconciliationTask) store.ReconciliationTask {
	task.Command = cloneStored(task.Command)
	return task
}

func stringsContainCanary(value string) bool {
	return value == "secret canary" || len(value) >= len("secret canary") && contains(value, "secret canary")
}

func contains(value, needle string) bool {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

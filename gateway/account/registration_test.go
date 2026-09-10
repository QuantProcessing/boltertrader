package account

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

const registrationBundle = `{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"offline-api-key","apiSecret":"offline-api-secret"}}`

type registrationMemory struct {
	mu      sync.Mutex
	records map[domain.AccountID]RegistrationRecord
	saveErr error
}

func (memory *registrationMemory) CreateRegistration(_ context.Context, item RegistrationRecord) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.saveErr != nil {
		return memory.saveErr
	}
	item.CredentialBundle = append([]byte(nil), item.CredentialBundle...)
	memory.records[item.ID] = item
	return nil
}
func (memory *registrationMemory) GetRegistration(_ context.Context, id domain.AccountID) (RegistrationRecord, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	item, ok := memory.records[id]
	if !ok {
		return RegistrationRecord{}, ErrRegistrationNotFound
	}
	item.CredentialBundle = append([]byte(nil), item.CredentialBundle...)
	return item, nil
}
func (memory *registrationMemory) ListRegistrations(context.Context) ([]Registration, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	out := []Registration{}
	for _, item := range memory.records {
		out = append(out, item.Registration)
	}
	return out, nil
}
func (memory *registrationMemory) DeleteRegistration(_ context.Context, id domain.AccountID) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	delete(memory.records, id)
	return nil
}

type registrationRoute struct {
	probes, closes int
	err            error
	closeErr       error
}

func (route *registrationRoute) Probe(context.Context) error { route.probes++; return route.err }
func (route *registrationRoute) Close() error                { route.closes++; return route.closeErr }

type registrationBuilder struct {
	routes []*registrationRoute
	err    error
}

func (builder *registrationBuilder) BuildRoute(context.Context, ConnectionSpec, RouteDefinition, *credential.Decoded) (ConnectionRoute, error) {
	route := &registrationRoute{err: builder.err}
	builder.routes = append(builder.routes, route)
	return route, nil
}
func registrationFixture(t *testing.T) (*Registrations, *Manager, *registrationMemory, *registrationBuilder) {
	t.Helper()
	cat, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	builder := &registrationBuilder{}
	manager, err := NewManager(context.Background(), nil, Options{GatewayCatalog: cat, HTTPClient: &http.Client{}, RuntimeGroupBuilder: builder, AllowLiveWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	memory := &registrationMemory{records: map[domain.AccountID]RegistrationRecord{}}
	service, err := NewRegistrations(memory, manager)
	if err != nil {
		t.Fatal(err)
	}
	return service, manager, memory, builder
}
func registrationRequest() AddRegistration {
	return AddRegistration{Venue: "binance", DisplayName: "Personal", CredentialBundle: []byte(registrationBundle)}
}
func TestRegistrationBalanceProbeSavesThenConnectsAndReplays(t *testing.T) {
	service, manager, memory, builder := registrationFixture(t)
	item, err := service.Add(context.Background(), registrationRequest(), "request-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(memory.records) != 1 || item.ValidatedAt.IsZero() || manager.ActiveSnapshot(item.ID) == nil {
		t.Fatal("validated connection was not published")
	}
	probes := 0
	for _, route := range builder.routes {
		probes += route.probes
	}
	if probes != 1 {
		t.Fatalf("balance probes=%d", probes)
	}
	replay, err := service.Add(context.Background(), registrationRequest(), "request-one")
	if err != nil || replay.ID != item.ID || len(builder.routes) != len(RegistrationRoutes("binance")) {
		t.Fatalf("replay created another connection: %v", err)
	}
	changed := registrationRequest()
	changed.DisplayName = "Different"
	if _, err := service.Add(context.Background(), changed, "request-one"); !errors.Is(err, ErrRegistrationConflict) {
		t.Fatal("reused request accepted different input")
	}
}
func TestRegistrationReturnsVenueFailureAndDoesNotStoreCredential(t *testing.T) {
	service, manager, memory, builder := registrationFixture(t)
	builder.err = exchange.NewError(exchange.KindPermission, exchange.ErrorDetails{Venue: exchange.VenueBinance, Code: "-2015", SafeMessage: "API key has no access"})
	_, err := service.Add(context.Background(), registrationRequest(), "denied")
	var venueErr *exchange.Error
	if !errors.As(err, &venueErr) || venueErr.Details().Code != "-2015" {
		t.Fatalf("venue failure lost: %v", err)
	}
	if len(memory.records) != 0 || len(manager.AccountIDs()) != 0 {
		t.Fatal("failed credential was saved")
	}
	for _, route := range builder.routes {
		if route.closes != 1 {
			t.Fatal("failed connection leaked")
		}
	}
}
func TestRegistrationStoreFailureClosesUnpublishedConnection(t *testing.T) {
	service, manager, memory, builder := registrationFixture(t)
	memory.saveErr = errors.New("database unavailable")
	if _, err := service.Add(context.Background(), registrationRequest(), "db-failed"); err == nil {
		t.Fatal("store failure accepted")
	}
	if len(manager.AccountIDs()) != 0 {
		t.Fatal("unsaved account became routable")
	}
	for _, route := range builder.routes {
		if route.closes != 1 {
			t.Fatal("unsaved connection leaked")
		}
	}
}
func TestRegistrationDeletionWaitsForInflightCallAndNeverQueriesInventory(t *testing.T) {
	service, manager, memory, builder := registrationFixture(t)
	item, err := service.Add(context.Background(), registrationRequest(), "delete-me")
	if err != nil {
		t.Fatal(err)
	}
	route, admission, err := manager.ActiveRoute(item.ID, "spot", "")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := admission.Acquire(domain.WriteFence{AccountFence: manager.ActiveSnapshot(item.ID).Fence, RouteID: route})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := service.Remove(ctx, item.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("in-flight call not respected: %v", err)
	}
	if len(memory.records) != 1 {
		t.Fatal("credential deleted before in-flight call completed")
	}
	lease.Release()
	if err := service.Remove(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	if len(memory.records) != 0 || manager.ActiveSnapshot(item.ID) != nil {
		t.Fatal("account still exists")
	}
	for _, route := range builder.routes {
		if route.closes != 1 || route.probes > 1 {
			t.Fatal("delete performed unexpected remote work")
		}
	}
	if err := service.Remove(context.Background(), item.ID); err != nil {
		t.Fatal("delete is not idempotent")
	}
}
func TestRegistrationRestoreUsesSavedCredentialWithoutHistoricalOrdersOrNewBalanceGate(t *testing.T) {
	service, manager, _, builder := registrationFixture(t)
	item, err := service.Add(context.Background(), registrationRequest(), "restart")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DisconnectRegistration(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	builder.err = errors.New("venue temporarily offline")
	if failed := service.Restore(context.Background()); len(failed) != 0 {
		t.Fatalf("restore incorrectly required venue revalidation: %v", failed)
	}
	if manager.ActiveSnapshot(item.ID) == nil {
		t.Fatal("saved account not restored")
	}
}

func TestRegistrationRemovalCanRetryFailedConnectionCleanup(t *testing.T) {
	service, manager, memory, builder := registrationFixture(t)
	item, err := service.Add(t.Context(), registrationRequest(), "cleanup-retry")
	if err != nil {
		t.Fatal(err)
	}
	builder.routes[0].closeErr = errors.New("local connection close failed")
	if err := service.Remove(t.Context(), item.ID); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if len(memory.records) != 1 || manager.ActiveSnapshot(item.ID) != nil {
		t.Fatal("failed cleanup must retain record but stop routing")
	}
	builder.routes[0].closeErr = nil
	if err := service.Remove(t.Context(), item.ID); err != nil {
		t.Fatal(err)
	}
	if len(memory.records) != 0 || builder.routes[0].closes != 2 {
		t.Fatal("cleanup retry did not finish")
	}
	for _, route := range builder.routes[1:] {
		if route.closes != 1 {
			t.Fatal("already closed handle closed again")
		}
	}
}

func TestRegistrationConnectionRejectsWritesAfterRemovalOrShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "remove"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			service, manager, _, builder := registrationFixture(t)
			item, err := service.Add(t.Context(), registrationRequest(), "write-fence")
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := manager.Runtime(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			fence := runtime.Fence()
			if err := manager.CheckWriteFence(fence); err != nil {
				t.Fatalf("active connection rejected: %v", err)
			}
			wrongConnection := fence
			wrongConnection.RuntimeID = "another-connection"
			if err := manager.CheckWriteFence(wrongConnection); !errors.Is(err, ErrStaleWriteFence) {
				t.Fatalf("wrong connection accepted: %v", err)
			}
			if available, total, ready := manager.RuntimeReadiness(); !available || total != 1 || ready != 1 {
				t.Fatalf("connected readiness=(%t,%d,%d)", available, total, ready)
			}
			routeID, admission, err := manager.ActiveRoute(item.ID, "spot", "")
			if err != nil {
				t.Fatal(err)
			}
			routeFence := domain.WriteFence{AccountFence: manager.ActiveSnapshot(item.ID).Fence, RouteID: routeID}
			if shutdown {
				err = manager.Close()
			} else {
				err = service.Remove(t.Context(), item.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.CheckWriteFence(fence); err == nil {
				t.Fatal("disconnected account still accepts writes")
			}
			if lease, err := admission.Acquire(routeFence); err == nil {
				lease.Release()
				t.Fatal("previously captured route still accepts calls")
			}
			if available, total, ready := manager.RuntimeReadiness(); available == shutdown || total != 0 || ready != 0 {
				t.Fatalf("disconnected readiness=(%t,%d,%d)", available, total, ready)
			}
			for _, route := range builder.routes {
				if route.closes != 1 {
					t.Fatalf("connection closes=%d", route.closes)
				}
			}
		})
	}
}

package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/google/uuid"
)

func RegistrationRoutes(venue string) []RouteDefinition {
	var routes []RouteDefinition
	for _, profile := range routeprofile.DefaultRegistry().Profiles() {
		if profile.Venue != venue {
			continue
		}
		id := string(profile.Product)
		if profile.SettlementAsset != "" {
			id += "-" + strings.ToLower(profile.SettlementAsset)
		}
		routes = append(routes, RouteDefinition{RouteID: domain.RouteID(id), Product: string(profile.Product), SettlementAsset: profile.SettlementAsset, RouteProfileID: profile.ID})
	}
	return routes
}

type registrationConnection struct {
	manager   *Manager
	group     *RuntimeGroup
	activated bool
}

// PrepareRegistration has no durable desired state or credential operation.
// The credential is decoded once and one successful balance query is enough.
func (manager *Manager) PrepareRegistration(ctx context.Context, record RegistrationRecord) (RegistrationConnection, error) {
	if ctx == nil || manager.groupBuilder == nil {
		return nil, errors.New("account connection factory unavailable")
	}
	decoded, err := credential.DefaultRegistry().Decode(record.Venue, record.CredentialBundle)
	if err != nil {
		return nil, ErrInvalidRegistration
	}
	defer decoded.Clear()
	routes := RegistrationRoutes(record.Venue)
	if len(routes) == 0 {
		return nil, exchange.ErrUnsupported
	}
	snapshot := ConnectionSpec{AccountID: record.ID, Venue: record.Venue, NativeEnvironment: "live", Routes: routes,
		Fence: domain.AccountFence{AccountID: record.ID, RuntimeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("connection:"+string(record.ID))).String()}}
	if snapshot.Fence.Validate() != nil {
		return nil, ErrInvalidRegistration
	}
	group := &RuntimeGroup{Snapshot: snapshot, Routes: map[domain.RouteID]*RouteRuntime{}, Admission: newGenerationAdmission(snapshot), State: GroupPrepared}
	for _, definition := range routes {
		route := &RouteRuntime{Definition: definition}
		group.Routes[definition.RouteID] = route
		if manager.publicConnectors != nil {
			lease, err := manager.publicConnectors.Acquire(ctx, routeprofile.Key{Venue: record.Venue, RouteProfileID: definition.RouteProfileID})
			if err != nil {
				group.destroy()
				return nil, err
			}
			route.publicLease = lease
			route.PublicConnectorID = lease.ConnectorID()
		}
		handle, err := manager.groupBuilder.BuildRoute(ctx, snapshot, definition, decoded)
		if err != nil {
			group.destroy()
			return nil, err
		}
		if handle == nil {
			group.destroy()
			return nil, errors.New("account connection unavailable")
		}
		route.Handle = handle
	}
	var probeErr error
	stopProbe := false
	verified := !record.ValidatedAt.IsZero()
	// Prefer spot, but a derivatives-only credential may legitimately reject
	// that endpoint. This is connectivity verification, not permission discovery.
	for _, product := range []string{string(exchange.ProductSpot), string(exchange.ProductPerp)} {
		if verified {
			break
		}
		for _, definition := range routes {
			if definition.Product != product {
				continue
			}
			probeErr = group.Routes[definition.RouteID].Handle.Probe(ctx)
			if probeErr == nil {
				verified = true
				break
			}
			if ctx.Err() != nil {
				stopProbe = true
				break
			}
			var venueErr *exchange.Error
			if !errors.As(probeErr, &venueErr) || (venueErr.Kind() != exchange.KindPermission && venueErr.Kind() != exchange.KindAuthentication) {
				stopProbe = true
				break
			}
		}
		if verified || stopProbe || ctx.Err() != nil {
			break
		}
	}
	if !verified {
		group.destroy()
		return nil, probeErr
	}
	return &registrationConnection{manager: manager, group: group}, nil
}

func (connection *registrationConnection) Activate() error {
	manager, group := connection.manager, connection.group
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()
	manager.mu.RLock()
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return ErrManagerClosed
	}
	manager.groupMu.Lock()
	runtime := manager.groups[group.Snapshot.AccountID]
	if runtime != nil && runtime.activeGroup != nil {
		manager.groupMu.Unlock()
		return ErrRuntimeExists
	}
	if runtime == nil {
		runtime = &accountRuntime{}
		manager.groups[group.Snapshot.AccountID] = runtime
	}
	views := map[domain.RouteID]RouteView{}
	for id, route := range group.Routes {
		views[id] = RouteView{RouteID: id, Product: route.Definition.Product, SettlementAsset: route.Definition.SettlementAsset, RouteProfileID: route.Definition.RouteProfileID}
	}
	group.State = GroupActive
	runtime.activeGroup = group
	runtime.active.Store(&RoutingSnapshot{Fence: group.Snapshot.Fence, Routes: views})
	manager.groupMu.Unlock()
	manager.mu.Lock()
	manager.runtimes[group.Snapshot.AccountID] = RuntimeAccount{
		AccountID: group.Snapshot.AccountID, RuntimeID: group.Snapshot.Fence.RuntimeID, Venue: group.Snapshot.Venue, VenueEnvironment: "live",
		Ready: true, ObservedAt: time.Now().UTC(),
	}
	manager.mu.Unlock()
	connection.activated = true
	// Catalog loads are public and do not form an account permission check.
	for _, route := range group.Routes {
		if route.publicLease != nil {
			go func(route *RouteRuntime) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_, _ = manager.publicConnectors.Refresh(ctx, routeprofile.Key{Venue: group.Snapshot.Venue, RouteProfileID: route.Definition.RouteProfileID})
			}(route)
		}
	}
	if err := manager.notifyAccountConnected(group.Snapshot.AccountID); err != nil {
		slog.Warn("account notification startup failed", "account_id", group.Snapshot.AccountID)
	}
	return nil
}

func (connection *registrationConnection) Close() error {
	if !connection.activated {
		connection.group.destroy()
	}
	return nil
}

// DisconnectRegistration fences only local in-flight calls. Venue positions
// and open orders are not blockers and no venue mutation is performed here.
func (manager *Manager) DisconnectRegistration(ctx context.Context, id domain.AccountID) error {
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()
	manager.groupMu.Lock()
	runtime := manager.groups[id]
	manager.groupMu.Unlock()
	if runtime == nil || runtime.activeGroup == nil {
		return nil
	}
	group := runtime.activeGroup
	if err := group.Admission.CloseAndWait(ctx); err != nil {
		group.Admission.reopen()
		return err
	}
	manager.notifyAccountDisconnected(id)
	manager.groupMu.Lock()
	runtime.active.Store(nil)
	manager.groupMu.Unlock()
	manager.mu.Lock()
	delete(manager.runtimes, id)
	manager.mu.Unlock()
	if err := group.destroyConfirmed(ctx); err != nil {
		return fmt.Errorf("close account connection: %w", err)
	}
	// Keep failed cleanup reachable so a retry can finish closing its handles.
	manager.groupMu.Lock()
	delete(manager.groups, id)
	manager.groupMu.Unlock()
	return nil
}

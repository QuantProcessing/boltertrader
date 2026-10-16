// Package account owns exchange accounts: credential registration, live
// clients, order commands and the position/balance snapshot pushed to
// subscribers. It keeps no order ledger; the venue is the source of truth.
package account

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
)

var (
	ErrNotFound         = errors.New("account not found")
	ErrCredentialFailed = errors.New("credential verification failed")
)

type Manager struct {
	store       *store.Store
	catalog     *market.Catalog
	httpClient  *http.Client
	credentials *credential.Registry

	mu       sync.RWMutex
	runtimes map[string]*runtime
}

func NewManager(accounts *store.Store, catalog *market.Catalog, httpClient *http.Client) *Manager {
	return &Manager{store: accounts, catalog: catalog, httpClient: httpClient, credentials: credential.DefaultRegistry(), runtimes: map[string]*runtime{}}
}

// Start builds clients for every stored account. A failing account is logged
// and skipped; it stays listed and can be deleted.
func (manager *Manager) Start() {
	for _, account := range manager.store.List() {
		if err := manager.activate(account); err != nil {
			slog.Error("account could not be activated", "account", account.ID, "venue", account.Venue, "error", err)
		}
	}
}

func (manager *Manager) activate(account store.Account) error {
	_, bundle, err := manager.store.Get(account.ID)
	if err != nil {
		return err
	}
	defer clear(bundle)
	decoded, err := manager.credentials.Decode(account.Venue, bundle)
	if err != nil {
		return err
	}
	defer decoded.Clear()
	clients, err := buildClients(account.ID, decoded, manager.httpClient)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	manager.runtimes[account.ID] = newRuntime(account.ID, exchange.Venue(account.Venue), clients, manager.catalog)
	manager.mu.Unlock()
	return nil
}

// AccountView is an account as returned to callers. It never contains
// credential material.
type AccountView struct {
	store.Account
	Markets []string `json:"markets"`
	Active  bool     `json:"active"`
}

func (manager *Manager) List() []AccountView {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	accounts := manager.store.List()
	views := make([]AccountView, 0, len(accounts))
	for _, account := range accounts {
		view := AccountView{Account: account, Markets: []string{}}
		if rt := manager.runtimes[account.ID]; rt != nil {
			view.Active = true
			for _, client := range rt.clients {
				view.Markets = append(view.Markets, client.market.String())
			}
		}
		views = append(views, view)
	}
	return views
}

// CreateRequest registers a new account. Credentials follow the field schema
// returned by /v1/venues for the venue (see credential.Schema).
type CreateRequest struct {
	Venue       string         `json:"venue"`
	Label       string         `json:"label"`
	Credentials map[string]any `json:"credentials"`
}

// Create verifies credentials against the venue (balance read on every
// market), then persists them encrypted.
func (manager *Manager) Create(ctx context.Context, request CreateRequest) (AccountView, error) {
	venueName := strings.ToLower(strings.TrimSpace(request.Venue))
	if !venue.Supported(exchange.Venue(venueName)) {
		return AccountView{}, fmt.Errorf("%w: unsupported venue", ErrInvalidRequest)
	}
	label := strings.TrimSpace(request.Label)
	if len(label) > 64 {
		return AccountView{}, fmt.Errorf("%w: label is too long", ErrInvalidRequest)
	}
	bundle, err := manager.bundle(venueName, request.Credentials)
	if err != nil {
		return AccountView{}, err
	}
	defer clear(bundle)
	decoded, err := manager.credentials.Decode(venueName, bundle)
	if err != nil {
		return AccountView{}, fmt.Errorf("%w: credentials do not match the venue's fields", ErrInvalidRequest)
	}
	defer decoded.Clear()

	id, err := newAccountID()
	if err != nil {
		return AccountView{}, err
	}
	clients, err := buildClients(id, decoded, manager.httpClient)
	if err != nil {
		return AccountView{}, fmt.Errorf("%w: %s", ErrInvalidRequest, safeMessage(err))
	}
	rt := newRuntime(id, exchange.Venue(venueName), clients, manager.catalog)
	verifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, client := range clients {
		if _, err := rt.fetchBalances(verifyCtx, client); err != nil {
			rt.close()
			failure := Classify(err)
			slog.Warn("account verification failed", "venue", venueName, "market", client.market.String(), "category", failure.Category, "error", err)
			if failure.Category == CategoryCredentials {
				return AccountView{}, fmt.Errorf("%w: %w", ErrCredentialFailed, failure)
			}
			// Not a verdict on the key: the venue was unreachable or answered
			// unexpectedly. Report that, so the user retries rather than re-typing keys.
			return AccountView{}, failure
		}
	}
	if label == "" {
		label = venueName
	}
	account := store.Account{ID: id, Venue: venueName, Label: label, CreatedAt: time.Now().UTC()}
	if err := manager.store.Create(account, bundle); err != nil {
		rt.close()
		return AccountView{}, err
	}
	manager.mu.Lock()
	manager.runtimes[id] = rt
	manager.mu.Unlock()
	slog.Info("account created", "account", id, "venue", venueName)
	view := AccountView{Account: account, Active: true}
	for _, client := range clients {
		view.Markets = append(view.Markets, client.market.String())
	}
	return view, nil
}

// bundle wraps flat credential fields into the credential package's envelope,
// splitting metadata and secret fields by the venue schema.
func (manager *Manager) bundle(venueName string, fields map[string]any) ([]byte, error) {
	schema, err := manager.credentials.Schema(venueName)
	if err != nil {
		return nil, fmt.Errorf("%w: unsupported venue", ErrInvalidRequest)
	}
	metadata, secrets := map[string]any{}, map[string]any{}
	known := map[string]bool{}
	for _, field := range schema.Fields {
		known[field.Key] = true
		value, present := fields[field.Key]
		if text, isText := value.(string); isText {
			value = strings.TrimSpace(text)
			present = present && text != ""
		}
		if !present {
			if field.Required {
				return nil, fmt.Errorf("%w: %s is required", ErrInvalidRequest, field.Label)
			}
			continue
		}
		if field.ValueType == "integer" {
			number, err := toInteger(value)
			if err != nil {
				return nil, fmt.Errorf("%w: %s must be an integer", ErrInvalidRequest, field.Label)
			}
			value = number
		}
		if field.Metadata {
			metadata[field.Key] = value
		} else {
			secrets[field.Key] = value
		}
	}
	for key := range fields {
		if !known[key] {
			return nil, fmt.Errorf("%w: unknown credential field %q", ErrInvalidRequest, key)
		}
	}
	return json.Marshal(credential.BundleV1{
		SchemaVersion: 1, Venue: venueName, AuthKind: credential.AuthKind(schema.AuthKind),
		Metadata: mustJSON(metadata), Secrets: mustJSON(secrets),
	})
}

func toInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int64(typed)) {
			return 0, fmt.Errorf("not an integer")
		}
		return int64(typed), nil
	case string:
		var number int64
		_, err := fmt.Sscan(typed, &number)
		return number, err
	case json.Number:
		return typed.Int64()
	}
	return 0, fmt.Errorf("not an integer")
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func (manager *Manager) Delete(id string) error {
	if err := manager.store.Delete(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	manager.mu.Lock()
	rt := manager.runtimes[id]
	delete(manager.runtimes, id)
	manager.mu.Unlock()
	if rt != nil {
		rt.close()
	}
	slog.Info("account deleted", "account", id)
	return nil
}

func (manager *Manager) runtime(id string) (*runtime, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	rt := manager.runtimes[id]
	if rt == nil {
		return nil, ErrNotFound
	}
	return rt, nil
}

// Venues describes supported venues, their markets and credential fields.
type VenueInfo struct {
	Venue   string             `json:"venue"`
	Markets []string           `json:"markets"`
	Fields  []credential.Field `json:"fields"`
}

func (manager *Manager) Venues() []VenueInfo {
	var result []VenueInfo
	for _, name := range venue.Venues() {
		info := VenueInfo{Venue: string(name)}
		for _, market := range venue.MarketsOf(name) {
			info.Markets = append(info.Markets, market.String())
		}
		if schema, err := manager.credentials.Schema(string(name)); err == nil {
			info.Fields = schema.Fields
		}
		result = append(result, info)
	}
	return result
}

// ---- operations delegated to the account runtime ----

func (manager *Manager) Positions(ctx context.Context, id string) ([]Position, error) {
	rt, err := manager.runtime(id)
	if err != nil {
		return nil, err
	}
	positions, err := rt.Positions(ctx)
	if positions == nil && err == nil {
		positions = []Position{}
	}
	return positions, err
}

func (manager *Manager) Balances(ctx context.Context, id string) ([]Balance, error) {
	rt, err := manager.runtime(id)
	if err != nil {
		return nil, err
	}
	balances, err := rt.Balances(ctx)
	if balances == nil && err == nil {
		balances = []Balance{}
	}
	return balances, err
}

func (manager *Manager) Place(ctx context.Context, id string, request PlaceRequest) (PlaceResult, error) {
	rt, err := manager.runtime(id)
	if err != nil {
		return PlaceResult{}, err
	}
	instrument, err := manager.catalog.Get(request.Instrument)
	if err != nil {
		return PlaceResult{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err)
	}
	return rt.Place(ctx, instrument, request)
}

func (manager *Manager) Lookup(ctx context.Context, id string, instrumentID venue.InstrumentID, clientOrderID, orderID string) (LookupResult, error) {
	rt, instrument, err := manager.resolve(id, instrumentID)
	if err != nil {
		return LookupResult{}, err
	}
	return rt.Lookup(ctx, instrument, clientOrderID, orderID)
}

func (manager *Manager) Cancel(ctx context.Context, id string, instrumentID venue.InstrumentID, orderID string) error {
	rt, instrument, err := manager.resolve(id, instrumentID)
	if err != nil {
		return err
	}
	return rt.Cancel(ctx, instrument, orderID)
}

func (manager *Manager) OpenOrders(ctx context.Context, id string, instrumentID venue.InstrumentID) ([]Order, error) {
	rt, instrument, err := manager.resolveOptional(id, instrumentID)
	if err != nil {
		return nil, err
	}
	return rt.OpenOrders(ctx, instrument)
}

func (manager *Manager) Fills(ctx context.Context, id string, instrumentID venue.InstrumentID, start time.Time, limit int) ([]Fill, error) {
	rt, instrument, err := manager.resolveOptional(id, instrumentID)
	if err != nil {
		return nil, err
	}
	return rt.Fills(ctx, instrument, start, limit)
}

func (manager *Manager) SetLeverage(ctx context.Context, id string, instrumentID venue.InstrumentID, leverage int) (int, error) {
	rt, instrument, err := manager.resolve(id, instrumentID)
	if err != nil {
		return 0, err
	}
	return rt.SetLeverage(ctx, instrument, leverage)
}

func (manager *Manager) resolve(id string, instrumentID venue.InstrumentID) (*runtime, market.Instrument, error) {
	rt, err := manager.runtime(id)
	if err != nil {
		return nil, market.Instrument{}, err
	}
	instrument, err := manager.catalog.Get(instrumentID)
	if err != nil {
		return nil, market.Instrument{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err)
	}
	return rt, instrument, nil
}

func (manager *Manager) resolveOptional(id string, instrumentID venue.InstrumentID) (*runtime, *market.Instrument, error) {
	if instrumentID == "" {
		rt, err := manager.runtime(id)
		return rt, nil, err
	}
	rt, instrument, err := manager.resolve(id, instrumentID)
	if err != nil {
		return nil, nil, err
	}
	return rt, &instrument, nil
}

// Source is the hub source for the per-account "account" topic.
func (manager *Manager) Source(ctx context.Context, key hub.Key, emit hub.Emit) error {
	rt, err := manager.runtime(key.Account)
	if err != nil {
		return err
	}
	return rt.stream(ctx, emit)
}

// ValidKey reports whether key names an existing account topic.
func (manager *Manager) ValidKey(key hub.Key) error {
	_, err := manager.runtime(key.Account)
	return err
}

func (manager *Manager) Close() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for id, rt := range manager.runtimes {
		rt.close()
		delete(manager.runtimes, id)
	}
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func newAccountID() (string, error) {
	var entropy [6]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return "acc_" + idEncoding.EncodeToString(entropy[:]), nil
}

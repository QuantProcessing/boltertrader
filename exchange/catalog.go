package exchange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type CatalogScope struct {
	Venue               Venue       `json:"venue"`
	Environment         Environment `json:"environment"`
	Product             Product     `json:"product"`
	Settlement          string      `json:"settlement,omitempty"`
	Namespace           string      `json:"namespace,omitempty"`
	EndpointFingerprint string      `json:"endpoint_fingerprint,omitempty"`
}

type InstrumentQuery struct {
	Symbol       string
	NativeSymbol string
	NativeID     string
}

type InstrumentSnapshot struct {
	SchemaVersion int
	Scope         CatalogScope
	Revision      string
	Generation    uint64
	LoadedAt      time.Time
	Instruments   []Instrument
	Indexes       CatalogIndexes
}

type CatalogIndexes struct {
	BySymbol       map[string]int
	ByNativeSymbol map[string]int
	ByNativeID     map[string]int
}

type CatalogStatus struct {
	Scope      CatalogScope
	Revision   string
	Generation uint64
	LoadedAt   time.Time
	LastError  string
}

type SourceDescriptor struct {
	Name              string `json:"name"`
	Endpoint          string `json:"endpoint,omitempty"`
	WebSocketEndpoint string `json:"websocket_endpoint,omitempty"`
	Transport         string `json:"transport,omitempty"`
}

func (descriptor SourceDescriptor) Fingerprint() string {
	normalized := SourceDescriptor{
		Name:              strings.TrimSpace(descriptor.Name),
		Endpoint:          strings.TrimRight(strings.TrimSpace(descriptor.Endpoint), "/"),
		WebSocketEndpoint: strings.TrimRight(strings.TrimSpace(descriptor.WebSocketEndpoint), "/"),
		Transport:         strings.TrimSpace(descriptor.Transport),
	}
	if normalized.Transport == "" {
		normalized.Transport = "default"
	}
	data, _ := json.Marshal(normalized)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type InstrumentLoader interface {
	LoadInstruments(context.Context, CatalogScope) (InstrumentSnapshot, error)
}

type InstrumentStore interface {
	Load(context.Context, CatalogScope) (InstrumentSnapshot, bool, error)
	Save(context.Context, InstrumentSnapshot) error
}

type InstrumentSourceRegistrar interface {
	RegisterSource(CatalogScope, SourceDescriptor, InstrumentLoader) error
}

type InstrumentCatalog interface {
	InstrumentSourceRegistrar
	Resolve(context.Context, CatalogScope, InstrumentQuery) (Instrument, error)
	Snapshot(context.Context, CatalogScope) (InstrumentSnapshot, error)
	Refresh(context.Context, CatalogScope) (InstrumentSnapshot, error)
	Status(CatalogScope) CatalogStatus
}

type StaticInstrumentLoader struct {
	Snapshot InstrumentSnapshot
	Err      error
}

func (loader StaticInstrumentLoader) LoadInstruments(context.Context, CatalogScope) (InstrumentSnapshot, error) {
	if loader.Err != nil {
		return InstrumentSnapshot{}, loader.Err
	}
	return loader.Snapshot, nil
}

type MemoryCatalog struct {
	loader InstrumentLoader
	store  InstrumentStore

	group singleflight.Group
	mu    sync.RWMutex
	data  map[CatalogScope]InstrumentSnapshot
	errs  map[CatalogScope]string
	srcs  map[CatalogScope]registeredInstrumentSource
	next  uint64
}

type registeredInstrumentSource struct {
	descriptor  SourceDescriptor
	fingerprint string
	loader      InstrumentLoader
}

func NewMemoryCatalog(loader InstrumentLoader, store InstrumentStore) *MemoryCatalog {
	return &MemoryCatalog{
		loader: loader,
		store:  store,
		data:   make(map[CatalogScope]InstrumentSnapshot),
		errs:   make(map[CatalogScope]string),
		srcs:   make(map[CatalogScope]registeredInstrumentSource),
	}
}

func NewDefaultMemoryCatalog() *MemoryCatalog {
	return NewMemoryCatalog(nil, nil)
}

var defaultInstrumentCatalog = NewDefaultMemoryCatalog()

func DefaultInstrumentCatalog() InstrumentCatalog {
	return defaultInstrumentCatalog
}

func (catalog *MemoryCatalog) RegisterSource(scope CatalogScope, descriptor SourceDescriptor, loader InstrumentLoader) error {
	if loader == nil {
		return invalidCatalog("instrument source loader is required")
	}
	normalized, err := normalizeSourceDescriptor(scope, descriptor)
	if err != nil {
		return err
	}
	fingerprint := normalized.Fingerprint()
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if current, ok := catalog.srcs[scope]; ok {
		if current.fingerprint == fingerprint {
			return nil
		}
		return invalidCatalog("instrument source registration conflicts with existing scope")
	}
	catalog.srcs[scope] = registeredInstrumentSource{
		descriptor:  normalized,
		fingerprint: fingerprint,
		loader:      loader,
	}
	return nil
}

func (catalog *MemoryCatalog) Resolve(ctx context.Context, scope CatalogScope, query InstrumentQuery) (Instrument, error) {
	snapshot, err := catalog.Snapshot(ctx, scope)
	if err != nil {
		return Instrument{}, err
	}
	index, err := resolveIndex(scope, snapshot.Indexes, query)
	if err != nil {
		return Instrument{}, err
	}
	return cloneInstrument(snapshot.Instruments[index]), nil
}

func (catalog *MemoryCatalog) Snapshot(ctx context.Context, scope CatalogScope) (InstrumentSnapshot, error) {
	catalog.mu.RLock()
	snapshot, ok := catalog.data[scope]
	catalog.mu.RUnlock()
	if ok {
		return cloneSnapshot(snapshot), nil
	}
	value, err, _ := catalog.group.Do(scopeKey(scope), func() (any, error) {
		return catalog.loadAndPublish(ctx, scope, false)
	})
	if err != nil {
		return InstrumentSnapshot{}, err
	}
	return cloneSnapshot(value.(InstrumentSnapshot)), nil
}

func (catalog *MemoryCatalog) Refresh(ctx context.Context, scope CatalogScope) (InstrumentSnapshot, error) {
	snapshot, err := catalog.loadAndPublish(ctx, scope, true)
	if err != nil {
		return cloneSnapshot(snapshot), err
	}
	return cloneSnapshot(snapshot), nil
}

func (catalog *MemoryCatalog) Status(scope CatalogScope) CatalogStatus {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	snapshot := catalog.data[scope]
	return CatalogStatus{
		Scope:      scope,
		Revision:   snapshot.Revision,
		Generation: snapshot.Generation,
		LoadedAt:   snapshot.LoadedAt,
		LastError:  catalog.errs[scope],
	}
}

func (catalog *MemoryCatalog) loadAndPublish(ctx context.Context, scope CatalogScope, force bool) (InstrumentSnapshot, error) {
	if !force && catalog.store != nil {
		if stored, ok, err := catalog.store.Load(ctx, scope); err != nil {
			catalog.rememberError(scope, err)
		} else if ok {
			return catalog.publish(scope, stored)
		}
	}
	loader := catalog.loader
	if loader == nil {
		catalog.mu.RLock()
		source, ok := catalog.srcs[scope]
		catalog.mu.RUnlock()
		if ok {
			loader = source.loader
		}
	}
	if loader == nil {
		err := metadataResolutionError(scope, "instrument loader is not configured")
		catalog.rememberError(scope, err)
		return catalog.lastKnownGoodOrError(scope, err)
	}
	loaded, err := loader.LoadInstruments(ctx, scope)
	if err != nil {
		catalog.rememberError(scope, err)
		return catalog.lastKnownGoodOrError(scope, err)
	}
	published, err := catalog.publish(scope, loaded)
	if err != nil {
		catalog.rememberError(scope, err)
		return catalog.lastKnownGoodOrError(scope, err)
	}
	if catalog.store != nil {
		if err := catalog.store.Save(ctx, published); err != nil {
			catalog.rememberError(scope, err)
		}
	}
	return published, nil
}

func (catalog *MemoryCatalog) publish(scope CatalogScope, snapshot InstrumentSnapshot) (InstrumentSnapshot, error) {
	if snapshot.Scope != scope {
		return InstrumentSnapshot{}, invalidCatalog("snapshot scope does not match requested scope")
	}
	if snapshot.SchemaVersion == 0 {
		snapshot.SchemaVersion = 1
	}
	indexes, err := buildCatalogIndexes(scope, snapshot.Instruments)
	if err != nil {
		return InstrumentSnapshot{}, err
	}
	snapshot.Indexes = indexes
	snapshot.Instruments = cloneInstruments(snapshot.Instruments)
	if snapshot.LoadedAt.IsZero() {
		snapshot.LoadedAt = time.Now().UTC()
	}
	snapshot.Revision = snapshotRevision(snapshot)

	catalog.mu.Lock()
	catalog.next++
	snapshot.Generation = catalog.next
	catalog.data[scope] = cloneSnapshot(snapshot)
	delete(catalog.errs, scope)
	catalog.mu.Unlock()

	return snapshot, nil
}

func (catalog *MemoryCatalog) rememberError(scope CatalogScope, err error) {
	catalog.mu.Lock()
	catalog.errs[scope] = safeCatalogError(err)
	catalog.mu.Unlock()
}

func (catalog *MemoryCatalog) lastKnownGoodOrError(scope CatalogScope, err error) (InstrumentSnapshot, error) {
	catalog.mu.RLock()
	snapshot, ok := catalog.data[scope]
	catalog.mu.RUnlock()
	if ok {
		return cloneSnapshot(snapshot), err
	}
	return InstrumentSnapshot{}, err
}

func normalizeSourceDescriptor(scope CatalogScope, descriptor SourceDescriptor) (SourceDescriptor, error) {
	normalized := SourceDescriptor{
		Name:              strings.TrimSpace(descriptor.Name),
		Endpoint:          strings.TrimRight(strings.TrimSpace(descriptor.Endpoint), "/"),
		WebSocketEndpoint: strings.TrimRight(strings.TrimSpace(descriptor.WebSocketEndpoint), "/"),
		Transport:         strings.TrimSpace(descriptor.Transport),
	}
	if normalized.Name == "" {
		return SourceDescriptor{}, invalidCatalog("instrument source name is required")
	}
	if normalized.Transport == "" {
		return SourceDescriptor{}, invalidCatalog("instrument source transport is required")
	}
	if normalized.Endpoint != "" {
		endpoint, err := normalizeCatalogURL(normalized.Endpoint, "http", "https")
		if err != nil {
			return SourceDescriptor{}, err
		}
		normalized.Endpoint = endpoint
	}
	if normalized.WebSocketEndpoint != "" {
		endpoint, err := normalizeCatalogURL(normalized.WebSocketEndpoint, "ws", "wss")
		if err != nil {
			return SourceDescriptor{}, err
		}
		normalized.WebSocketEndpoint = endpoint
	}
	if normalized.Endpoint == "" && normalized.WebSocketEndpoint == "" {
		return SourceDescriptor{}, invalidCatalog("instrument source endpoint is required")
	}
	if scope.EndpointFingerprint != "" && scope.EndpointFingerprint != normalized.Fingerprint() {
		return SourceDescriptor{}, invalidCatalog("catalog scope endpoint fingerprint does not match source descriptor")
	}
	return normalized, nil
}

func normalizeCatalogURL(raw string, schemes ...string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", invalidCatalog("instrument source endpoint must be absolute")
	}
	allowed := false
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", invalidCatalog("instrument source endpoint has unsupported scheme")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", invalidCatalog("instrument source endpoint must not include user info, query, or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func buildCatalogIndexes(scope CatalogScope, instruments []Instrument) (CatalogIndexes, error) {
	indexes := CatalogIndexes{
		BySymbol:       make(map[string]int, len(instruments)),
		ByNativeSymbol: make(map[string]int, len(instruments)),
		ByNativeID:     make(map[string]int),
	}
	for index, instrument := range instruments {
		if err := instrument.Validate(); err != nil {
			return CatalogIndexes{}, err
		}
		identity := instrument.Identity
		if identity.Venue != scope.Venue || identity.Product != scope.Product {
			return CatalogIndexes{}, invalidCatalog("instrument identity does not match catalog scope")
		}
		if scope.Settlement != "" && identity.SettleAsset != "" && identity.SettleAsset != scope.Settlement {
			return CatalogIndexes{}, invalidCatalog("instrument settlement does not match catalog scope")
		}
		if err := addCatalogIndex(indexes.BySymbol, identity.Symbol, index, "duplicate canonical symbol"); err != nil {
			return CatalogIndexes{}, err
		}
		if err := addCatalogIndex(indexes.ByNativeSymbol, identity.NativeSymbol, index, "duplicate native symbol"); err != nil {
			return CatalogIndexes{}, err
		}
		if identity.NativeID != "" {
			if err := addCatalogIndex(indexes.ByNativeID, identity.NativeID, index, "duplicate native id"); err != nil {
				return CatalogIndexes{}, err
			}
		}
	}
	return indexes, nil
}

func addCatalogIndex(index map[string]int, key string, value int, message string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return invalidCatalog("empty catalog index key")
	}
	if _, exists := index[key]; exists {
		return invalidCatalog(message)
	}
	index[key] = value
	return nil
}

func resolveIndex(scope CatalogScope, indexes CatalogIndexes, query InstrumentQuery) (int, error) {
	symbol := strings.TrimSpace(query.Symbol)
	nativeSymbol := strings.TrimSpace(query.NativeSymbol)
	nativeID := strings.TrimSpace(query.NativeID)
	keys := 0
	if symbol != "" {
		keys++
	}
	if nativeSymbol != "" {
		keys++
	}
	if nativeID != "" {
		keys++
	}
	if keys == 0 {
		return 0, invalidRequest("instrument query requires exactly one identity key")
	}
	if keys > 1 {
		return 0, invalidRequest("instrument query is ambiguous")
	}
	if symbol != "" {
		index, ok := indexes.BySymbol[symbol]
		if !ok {
			return 0, metadataResolutionError(scope, "instrument metadata not found")
		}
		if nativeIndex, ok := indexes.ByNativeSymbol[symbol]; ok && nativeIndex != index {
			return 0, metadataResolutionError(scope, "instrument query key is ambiguous across indexes")
		}
		if nativeIDIndex, ok := indexes.ByNativeID[symbol]; ok && nativeIDIndex != index {
			return 0, metadataResolutionError(scope, "instrument query key is ambiguous across indexes")
		}
		return index, nil
	}
	if nativeSymbol != "" {
		index, ok := indexes.ByNativeSymbol[nativeSymbol]
		if !ok {
			return 0, metadataResolutionError(scope, "instrument metadata not found")
		}
		if symbolIndex, ok := indexes.BySymbol[nativeSymbol]; ok && symbolIndex != index {
			return 0, metadataResolutionError(scope, "instrument query key is ambiguous across indexes")
		}
		if nativeIDIndex, ok := indexes.ByNativeID[nativeSymbol]; ok && nativeIDIndex != index {
			return 0, metadataResolutionError(scope, "instrument query key is ambiguous across indexes")
		}
		return index, nil
	}
	index, ok := indexes.ByNativeID[nativeID]
	if !ok {
		return 0, metadataResolutionError(scope, "instrument metadata not found")
	}
	if symbolIndex, ok := indexes.BySymbol[nativeID]; ok && symbolIndex != index {
		return 0, metadataResolutionError(scope, "instrument query key is ambiguous across indexes")
	}
	if nativeSymbolIndex, ok := indexes.ByNativeSymbol[nativeID]; ok && nativeSymbolIndex != index {
		return 0, metadataResolutionError(scope, "instrument query key is ambiguous across indexes")
	}
	return index, nil
}

func cloneSnapshot(snapshot InstrumentSnapshot) InstrumentSnapshot {
	snapshot.Instruments = cloneInstruments(snapshot.Instruments)
	snapshot.Indexes = cloneIndexes(snapshot.Indexes)
	return snapshot
}

func cloneInstruments(instruments []Instrument) []Instrument {
	if instruments == nil {
		return nil
	}
	out := make([]Instrument, len(instruments))
	copy(out, instruments)
	return out
}

func cloneInstrument(instrument Instrument) Instrument {
	return instrument
}

func cloneIndexes(indexes CatalogIndexes) CatalogIndexes {
	return CatalogIndexes{
		BySymbol:       cloneIndex(indexes.BySymbol),
		ByNativeSymbol: cloneIndex(indexes.ByNativeSymbol),
		ByNativeID:     cloneIndex(indexes.ByNativeID),
	}
}

func cloneIndex(index map[string]int) map[string]int {
	if index == nil {
		return nil
	}
	out := make(map[string]int, len(index))
	for key, value := range index {
		out[key] = value
	}
	return out
}

func snapshotRevision(snapshot InstrumentSnapshot) string {
	body := struct {
		SchemaVersion int
		Scope         CatalogScope
		Instruments   []Instrument
	}{
		SchemaVersion: snapshot.SchemaVersion,
		Scope:         snapshot.Scope,
		Instruments:   snapshot.Instruments,
	}
	data, _ := json.Marshal(body)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func scopeKey(scope CatalogScope) string {
	data, _ := json.Marshal(scope)
	return string(data)
}

func invalidCatalog(message string) error {
	return NewError(KindInvalidConfig, ErrorDetails{
		Operation:   "InstrumentCatalog",
		SafeMessage: message,
	})
}

func metadataResolutionError(scope CatalogScope, message string) error {
	return NewError(KindMetadataResolution, ErrorDetails{
		Venue:       scope.Venue,
		Product:     scope.Product,
		Operation:   "InstrumentCatalog.Resolve",
		SafeMessage: message,
	})
}

func safeCatalogError(err error) string {
	if err == nil {
		return ""
	}
	var exchangeErr *Error
	if errors.As(err, &exchangeErr) {
		return exchangeErr.Error()
	}
	return fmt.Sprintf("%T", err)
}

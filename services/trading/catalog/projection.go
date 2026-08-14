package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

const (
	ReasonUnavailable  = "catalog_unavailable"
	ReasonInvalidFacts = "catalog_invalid_gateway_facts"
	ReasonStale        = "catalog_projection_stale"
)

var ErrUnavailable = errors.New("catalog projection unavailable")

type Venue struct {
	Venue             string           `json:"venue"`
	Environment       string           `json:"environment"`
	NativeEnvironment string           `json:"nativeEnvironment"`
	Ready             bool             `json:"ready"`
	Products          []string         `json:"products"`
	CredentialSchema  CredentialSchema `json:"credentialSchema"`
	RouteProfiles     []RouteProfile   `json:"routeProfiles"`
}

type CredentialField struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Metadata  bool   `json:"metadata,omitempty"`
	Required  bool   `json:"required"`
	ValueType string `json:"valueType"`
}

type CredentialSchema struct {
	SchemaVersion int               `json:"schemaVersion"`
	AuthKind      string            `json:"authKind"`
	Fields        []CredentialField `json:"fields"`
}

type RouteProfile struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
	Label           string `json:"label"`
}

type Instrument struct {
	InstrumentID string     `json:"instrumentId"`
	Venue        string     `json:"venue"`
	Environment  string     `json:"environment"`
	Product      string     `json:"product"`
	Settlement   string     `json:"settlement,omitempty"`
	Symbol       string     `json:"symbol"`
	BaseAsset    string     `json:"baseAsset"`
	QuoteAsset   string     `json:"quoteAsset"`
	Rules        TradeRules `json:"rules"`
	Ready        bool       `json:"ready"`
}

type TradeRules struct {
	PriceIncrement    string `json:"priceIncrement"`
	QuantityIncrement string `json:"quantityIncrement"`
	MinQuantity       string `json:"minQuantity"`
	MinNotional       string `json:"minNotional,omitempty"`
}

type Snapshot struct {
	Revision    string       `json:"revision"`
	ObservedAt  time.Time    `json:"observedAt"`
	Venues      []Venue      `json:"venues"`
	Instruments []Instrument `json:"instruments"`
}

type Status struct {
	Ready      bool          `json:"ready"`
	Reason     string        `json:"reason"`
	Revision   string        `json:"revision,omitempty"`
	ObservedAt time.Time     `json:"observedAt,omitempty"`
	Age        time.Duration `json:"-"`
}

type Source interface {
	Fetch(context.Context) (Snapshot, error)
}

type Projection struct {
	source Source
	ttl    time.Duration
	now    func() time.Time

	mu       sync.Mutex
	snapshot Snapshot
	loaded   bool
	reason   string
	inFlight chan struct{}
}

func NewProjection(source Source, ttl time.Duration, now func() time.Time) (*Projection, error) {
	if source == nil {
		return nil, fmt.Errorf("catalog Gateway source is required")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("catalog projection ttl must be positive")
	}
	if now == nil {
		return nil, fmt.Errorf("catalog clock is required")
	}
	return &Projection{source: source, ttl: ttl, now: now, reason: ReasonUnavailable}, nil
}

func (projection *Projection) Snapshot(ctx context.Context) (Snapshot, error) {
	return projection.load(ctx, false)
}

// Refresh renews the projection whether or not it has expired, and is how a
// caller keeps it warm rather than merely reading it.
//
// Snapshot cannot serve that purpose: it refetches only once the facts are
// already older than the TTL, so a loop built on it renews the projection
// strictly after the window in which Status reports it stale. The two methods
// therefore answer different questions — Snapshot asks for facts and accepts
// cached ones, Refresh asks for the fetch itself.
func (projection *Projection) Refresh(ctx context.Context) error {
	_, err := projection.load(ctx, true)
	return err
}

func (projection *Projection) load(ctx context.Context, force bool) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, fmt.Errorf("catalog context is required")
	}
	for {
		projection.mu.Lock()
		if !force && projection.loaded && projection.now().UTC().Sub(projection.snapshot.ObservedAt) <= projection.ttl {
			snapshot := clone(projection.snapshot)
			projection.mu.Unlock()
			return snapshot, nil
		}
		if projection.inFlight != nil {
			wait := projection.inFlight
			projection.mu.Unlock()
			select {
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			case <-wait:
				// Whoever we waited on has just fetched, which is as fresh as
				// our own fetch would have been. Dropping force here is what
				// lets concurrent refreshes coalesce: holding it would send
				// each waiter off to fetch again in turn, so N refreshes that
				// arrive together would cost N round trips instead of one.
				force = false
				continue
			}
		}
		projection.inFlight = make(chan struct{})
		wait := projection.inFlight
		projection.mu.Unlock()

		snapshot, err := projection.source.Fetch(ctx)
		if err == nil {
			err = validate(snapshot)
			if err == nil {
				snapshot, err = enrichBrowserVenues(snapshot)
			}
			age := projection.now().UTC().Sub(snapshot.ObservedAt)
			if err == nil && (age > projection.ttl || age < 0) {
				err = fmt.Errorf("Gateway catalog facts are stale or future-dated")
			}
		}
		projection.mu.Lock()
		if err == nil {
			snapshot.ObservedAt = snapshot.ObservedAt.UTC()
			projection.snapshot = clone(snapshot)
			projection.loaded = true
			projection.reason = "ready"
		} else if errors.Is(err, ErrUnavailable) {
			projection.reason = ReasonUnavailable
		} else {
			projection.reason = ReasonInvalidFacts
		}
		close(wait)
		projection.inFlight = nil
		projection.mu.Unlock()
		if err != nil {
			return Snapshot{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return clone(snapshot), nil
	}
}

type credentialVariant struct {
	authKind credential.AuthKind
	fields   []CredentialField
	metadata map[string]any
	secrets  map[string]any
}

var browserCredentialVariants = []credentialVariant{
	{credential.AuthAPIKeySecret, []CredentialField{{Key: "apiKey", Label: "API key", Required: true, ValueType: "string"}, {Key: "apiSecret", Label: "API secret", Required: true, ValueType: "string"}}, map[string]any{}, map[string]any{"apiKey": "probe", "apiSecret": "probe"}},
	{credential.AuthAPIKeySecretPassphrase, []CredentialField{{Key: "apiKey", Label: "API key", Required: true, ValueType: "string"}, {Key: "apiSecret", Label: "API secret", Required: true, ValueType: "string"}, {Key: "passphrase", Label: "Passphrase", Required: true, ValueType: "string"}}, map[string]any{}, map[string]any{"apiKey": "probe", "apiSecret": "probe", "passphrase": "probe"}},
	{credential.AuthLighterSigner, []CredentialField{{Key: "accountIndex", Label: "Account index", Metadata: true, Required: true, ValueType: "integer"}, {Key: "keyIndex", Label: "Key index", Metadata: true, Required: true, ValueType: "integer"}, {Key: "privateKey", Label: "Private key", Required: true, ValueType: "string"}}, map[string]any{"accountIndex": 0, "keyIndex": 0}, map[string]any{"privateKey": "probe"}},
	{credential.AuthEVMPrivateKey, []CredentialField{{Key: "accountAddress", Label: "Account address", Metadata: true, ValueType: "string"}, {Key: "privateKey", Label: "Private key", Required: true, ValueType: "string"}}, map[string]any{"accountAddress": "0x1111111111111111111111111111111111111111"}, map[string]any{"privateKey": "probe"}},
	{credential.AuthAsterAPIWallet, []CredentialField{{Key: "userAddress", Label: "User address", Metadata: true, Required: true, ValueType: "string"}, {Key: "expectedSigner", Label: "Expected signer", Metadata: true, ValueType: "string"}, {Key: "apiWalletPrivateKey", Label: "API wallet private key", Required: true, ValueType: "string"}}, map[string]any{"userAddress": "0x1111111111111111111111111111111111111111", "expectedSigner": "0x2222222222222222222222222222222222222222"}, map[string]any{"apiWalletPrivateKey": "probe"}},
	{credential.AuthNadoSigner, []CredentialField{{Key: "subaccount", Label: "Subaccount", Metadata: true, Required: true, ValueType: "string"}, {Key: "privateKey", Label: "Private key", Required: true, ValueType: "string"}}, map[string]any{"subaccount": "default"}, map[string]any{"privateKey": "probe"}},
}

func enrichBrowserVenues(snapshot Snapshot) (Snapshot, error) {
	for index := range snapshot.Venues {
		venue := &snapshot.Venues[index]
		environment := routeprofile.NativeEnvironment(venue.Environment)
		mode := routeprofile.ModeTest
		if environment == routeprofile.EnvironmentLive {
			mode = routeprofile.ModeLive
		}
		profiles := make([]RouteProfile, 0)
		for _, product := range []routeprofile.Product{routeprofile.ProductSpot, routeprofile.ProductPerp} {
			for _, settlement := range []string{"", "USDT", "USDC", "USDT0"} {
				profile, err := routeprofile.DefaultRegistry().ResolveLegacy(mode, venue.Venue, string(product), settlement, environment)
				if err != nil {
					continue
				}
				routeID := string(profile.Product)
				label := strings.ToUpper(string(profile.Product[:1])) + string(profile.Product[1:])
				if profile.SettlementAsset != "" {
					routeID += "-" + strings.ToLower(profile.SettlementAsset)
					label += " / " + profile.SettlementAsset
				}
				profiles = append(profiles, RouteProfile{RouteID: routeID, Product: string(profile.Product), SettlementAsset: profile.SettlementAsset, RouteProfileID: profile.ID, Label: label})
			}
		}
		if len(profiles) == 0 {
			return Snapshot{}, fmt.Errorf("Gateway venue has no executable route profiles")
		}
		schema, err := browserCredentialSchema(venue.Venue)
		if err != nil {
			return Snapshot{}, err
		}
		venue.NativeEnvironment = venue.Environment
		venue.Products = []string{"manual"}
		venue.CredentialSchema = schema
		venue.RouteProfiles = profiles
	}
	return snapshot, nil
}

func browserCredentialSchema(venue string) (CredentialSchema, error) {
	var matched *credentialVariant
	for index := range browserCredentialVariants {
		variant := &browserCredentialVariants[index]
		payload, err := json.Marshal(map[string]any{"schemaVersion": 1, "venue": venue, "authKind": variant.authKind, "metadata": variant.metadata, "secrets": variant.secrets})
		if err != nil {
			return CredentialSchema{}, err
		}
		decoded, err := credential.DefaultRegistry().Decode(venue, payload)
		clear(payload)
		if err != nil {
			continue
		}
		decoded.Clear()
		if matched != nil {
			return CredentialSchema{}, fmt.Errorf("credential registry is ambiguous for venue")
		}
		matched = variant
	}
	if matched == nil {
		return CredentialSchema{}, fmt.Errorf("Gateway venue has no executable credential schema")
	}
	return CredentialSchema{SchemaVersion: 1, AuthKind: string(matched.authKind), Fields: append([]CredentialField(nil), matched.fields...)}, nil
}

func (projection *Projection) Status() Status {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if !projection.loaded {
		return Status{Reason: projection.reason}
	}
	age := projection.now().UTC().Sub(projection.snapshot.ObservedAt)
	if age < 0 {
		age = 0
	}
	ready := age <= projection.ttl
	reason := projection.reason
	if !ready {
		reason = ReasonStale
	}
	return Status{Ready: ready, Reason: reason, Revision: projection.snapshot.Revision, ObservedAt: projection.snapshot.ObservedAt, Age: age}
}

func validate(snapshot Snapshot) error {
	if strings.TrimSpace(snapshot.Revision) == "" {
		return fmt.Errorf("Gateway catalog revision is required")
	}
	if snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("Gateway catalog observation time is required")
	}
	venues := make(map[string]struct{}, len(snapshot.Venues))
	for _, venue := range snapshot.Venues {
		if strings.TrimSpace(venue.Venue) == "" || strings.TrimSpace(venue.Environment) == "" {
			return fmt.Errorf("Gateway venue identity is incomplete")
		}
		key := venue.Venue + "\x00" + venue.Environment
		if _, exists := venues[key]; exists {
			return fmt.Errorf("duplicate Gateway venue %q environment %q", venue.Venue, venue.Environment)
		}
		venues[key] = struct{}{}
	}
	instruments := make(map[string]struct{}, len(snapshot.Instruments))
	for _, instrument := range snapshot.Instruments {
		if strings.TrimSpace(instrument.InstrumentID) == "" || strings.TrimSpace(instrument.Venue) == "" ||
			strings.TrimSpace(instrument.Environment) == "" || strings.TrimSpace(instrument.Product) == "" ||
			strings.TrimSpace(instrument.Symbol) == "" || strings.TrimSpace(instrument.BaseAsset) == "" || strings.TrimSpace(instrument.QuoteAsset) == "" {
			return fmt.Errorf("Gateway instrument identity is incomplete")
		}
		if _, ok := venues[instrument.Venue+"\x00"+instrument.Environment]; !ok {
			return fmt.Errorf("instrument references unknown Gateway venue")
		}
		if instrument.Product != "spot" && instrument.Product != "perp" {
			return fmt.Errorf("Gateway instrument product is unsupported")
		}
		if err := validateRules(instrument.Rules); err != nil {
			return err
		}
		key := strings.Join([]string{instrument.Venue, instrument.Environment, instrument.Product, instrument.InstrumentID}, "\x00")
		if _, exists := instruments[key]; exists {
			return fmt.Errorf("duplicate Gateway instrument %q", instrument.InstrumentID)
		}
		instruments[key] = struct{}{}
	}
	return nil
}

func validateRules(rules TradeRules) error {
	for name, value := range map[string]string{
		"price increment": rules.PriceIncrement, "quantity increment": rules.QuantityIncrement, "minimum quantity": rules.MinQuantity,
	} {
		parsed, err := decimal.NewFromString(value)
		if err != nil || !parsed.IsPositive() {
			return fmt.Errorf("Gateway instrument %s is invalid", name)
		}
	}
	if rules.MinNotional != "" {
		parsed, err := decimal.NewFromString(rules.MinNotional)
		if err != nil || !parsed.IsPositive() {
			return fmt.Errorf("Gateway instrument minimum notional is invalid")
		}
	}
	return nil
}

func clone(snapshot Snapshot) Snapshot {
	snapshot.Venues = append([]Venue(nil), snapshot.Venues...)
	for index := range snapshot.Venues {
		snapshot.Venues[index].Products = append([]string(nil), snapshot.Venues[index].Products...)
		snapshot.Venues[index].CredentialSchema.Fields = append([]CredentialField(nil), snapshot.Venues[index].CredentialSchema.Fields...)
		snapshot.Venues[index].RouteProfiles = append([]RouteProfile(nil), snapshot.Venues[index].RouteProfiles...)
	}
	snapshot.Instruments = append([]Instrument(nil), snapshot.Instruments...)
	return snapshot
}

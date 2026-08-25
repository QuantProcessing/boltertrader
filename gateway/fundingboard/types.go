// Package fundingboard projects normalized derivative facts into the funding
// table and delegates settled-history reads to venue connectors. It owns no
// exchange market-data connection.
package fundingboard

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	exchangefactory "github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

var ErrBoardUnavailable = errors.New("funding board unavailable")

var (
	ErrHistoryUnsupported       = errors.New("funding history unsupported")
	ErrHistoryUnknownInstrument = errors.New("funding history instrument unknown")
	ErrHistoryWindow            = errors.New("funding history window unsupported")
)

const (
	defaultMaxAge       = 2 * time.Minute
	defaultVenueTimeout = 10 * time.Second
	maxHistorySpan      = 31 * 24 * time.Hour
)

type HistoryConnector interface {
	Load(context.Context, exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error)
	Close() error
}

type HistoryFactory interface {
	BuildHistory(routeprofile.Profile) (HistoryConnector, error)
}

type HistoryQuery struct {
	RouteProfileID string
	NativeSymbol   string
	NativeID       string
	Since          time.Time
	Until          time.Time
}

type Snapshot struct {
	ObservedAt time.Time
	Venues     []VenueSnapshot
}

type VenueState string

const (
	VenueUnavailable VenueState = "unavailable"
	VenueDegraded    VenueState = "degraded"
	VenueLive        VenueState = "live"
)

type VenueSnapshot struct {
	Venue          string
	Environment    string
	Product        string
	Settlement     string
	RouteProfileID string
	ObservedAt     time.Time
	Stale          bool
	Unavailable    bool
	Status         VenueState
	LastSuccessAt  time.Time
	LastError      string
	Reconnects     uint64
	Entries        []exchange.FundingBoardEntry
}

type ChangeKind string

const ChangeUpsert ChangeKind = "upsert"

type Change struct {
	Sequence uint64
	Kind     ChangeKind
}

type ExchangeFactory struct {
	httpClient *http.Client
	requests   exchange.MarketRequestCoordinator
}

func NewExchangeFactory(httpClient *http.Client) (*ExchangeFactory, error) {
	if httpClient == nil {
		return nil, ErrBoardUnavailable
	}
	return &ExchangeFactory{httpClient: httpClient}, nil
}

func (factory *ExchangeFactory) SetMarketRequestCoordinator(requests exchange.MarketRequestCoordinator) {
	if factory != nil {
		factory.requests = requests
	}
}

func (factory *ExchangeFactory) BuildDerivativeFeed(profiles []routeprofile.Profile, capabilities []exchange.MarketCapability, resolver exchange.DerivativeInstrumentResolver) (exchange.DerivativeFeed, error) {
	if factory == nil || factory.httpClient == nil || len(profiles) == 0 || len(capabilities) == 0 {
		return nil, ErrBoardUnavailable
	}
	profile := profiles[0]
	routeProfileIDs := make([]string, 0, len(profiles))
	for _, projection := range profiles {
		routeProfileIDs = append(routeProfileIDs, projection.ID)
	}
	feed, err := exchangefactory.NewPublicDerivativeFeed(
		exchange.Venue(profile.Venue), exchange.Product(profile.Product), profile.SettlementAsset,
		exchangefactory.EnvironmentLive, factory.httpClient, routeProfileIDs,
		capabilities[0].NativeMarketFamily, resolver, capabilities, factory.requests,
	)
	if err != nil {
		return nil, ErrBoardUnavailable
	}
	return feed, nil
}

func (factory *ExchangeFactory) BuildHistory(profile routeprofile.Profile) (HistoryConnector, error) {
	if factory == nil || factory.httpClient == nil {
		return nil, ErrBoardUnavailable
	}
	connector, err := exchangefactory.NewPublicFundingHistoryConnector(
		exchange.Venue(profile.Venue), exchange.Product(profile.Product), profile.SettlementAsset,
		exchangefactory.EnvironmentLive, factory.httpClient,
	)
	if err != nil {
		return nil, ErrHistoryUnsupported
	}
	return connector, nil
}

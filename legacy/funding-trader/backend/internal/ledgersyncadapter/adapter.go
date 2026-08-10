package ledgersyncadapter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgersync"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
)

var (
	ErrResolverStoreRequired = errors.New("ledger sync resolver store is required")
	ErrPublisherInvalid      = errors.New("ledger sync publisher configuration is invalid")
	ErrPublisherTopicInvalid = errors.New("ledger sync publisher topic is invalid")
)

type FundingFactStore interface {
	ResolveFundingFactID(context.Context, ledgerfacts.Scope, string) (string, bool, error)
}

type FundingFactResolver struct {
	store FundingFactStore
}

func NewFundingFactResolver(store FundingFactStore) (*FundingFactResolver, error) {
	if store == nil {
		return nil, ErrResolverStoreRequired
	}
	return &FundingFactResolver{store: store}, nil
}

func (resolver *FundingFactResolver) ResolveGatewayFundingFactID(
	ctx context.Context,
	scope ledgerfacts.Scope,
	upstreamRef string,
) (string, bool, error) {
	if resolver == nil || resolver.store == nil {
		return "", false, ErrResolverStoreRequired
	}
	return resolver.store.ResolveFundingFactID(ctx, scope, upstreamRef)
}

type StreamPublisher interface {
	Publish(context.Context, uistream.EventInput) (uistream.StoredEvent, error)
}

type PublisherConfig struct {
	Stream      StreamPublisher
	PrincipalID string
	Mode        string
}

type Publisher struct {
	stream      StreamPublisher
	principalID string
	mode        string
}

func NewPublisher(config PublisherConfig) (*Publisher, error) {
	principalID := strings.TrimSpace(config.PrincipalID)
	mode := strings.TrimSpace(config.Mode)
	if config.Stream == nil || principalID == "" || (mode != "test" && mode != "live") {
		return nil, ErrPublisherInvalid
	}
	return &Publisher{
		stream:      config.Stream,
		principalID: principalID,
		mode:        mode,
	}, nil
}

func (publisher *Publisher) Publish(ctx context.Context, event ledgersync.Event) error {
	if publisher == nil || publisher.stream == nil ||
		publisher.principalID == "" || (publisher.mode != "test" && publisher.mode != "live") {
		return ErrPublisherInvalid
	}
	topic := strings.TrimSpace(event.Topic)
	if topic == "" {
		return ErrPublisherTopicInvalid
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	_, err = publisher.stream.Publish(ctx, uistream.EventInput{
		Scope:       "all",
		PrincipalID: publisher.principalID,
		Mode:        publisher.mode,
		Kind:        topic,
		Data:        payload,
	})
	return err
}

var (
	_ ledgersync.Resolver  = (*FundingFactResolver)(nil)
	_ ledgersync.Publisher = (*Publisher)(nil)
)

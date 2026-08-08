package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

type gatewayAuthority struct {
	mu      sync.Mutex
	store   store.DesiredStateStore
	manager *account.Manager
	reads   authorityReadSource
	mode    account.RuntimeMode
	now     func() time.Time
}

type authorityVenue struct {
	Venue       string `json:"venue"`
	Environment string `json:"environment"`
	Ready       bool   `json:"ready"`
}

type authorityInstrument struct {
	InstrumentID string              `json:"instrumentId"`
	Venue        string              `json:"venue"`
	Environment  string              `json:"environment"`
	Product      string              `json:"product"`
	Settlement   string              `json:"settlement,omitempty"`
	Symbol       string              `json:"symbol"`
	BaseAsset    string              `json:"baseAsset"`
	QuoteAsset   string              `json:"quoteAsset"`
	Rules        authorityTradeRules `json:"rules"`
	Ready        bool                `json:"ready"`
}

type authorityCatalogInstruments struct {
	Revision    string                `json:"revision"`
	ObservedAt  time.Time             `json:"observedAt"`
	Instruments []authorityInstrument `json:"instruments"`
}

type authorityRuntimeStatus struct {
	Mode     account.RuntimeMode      `json:"mode"`
	Accounts []account.RuntimeAccount `json:"accounts"`
}

type authorityTradeRules struct {
	PriceIncrement    string `json:"priceIncrement"`
	QuantityIncrement string `json:"quantityIncrement"`
	MinQuantity       string `json:"minQuantity"`
	MinNotional       string `json:"minNotional,omitempty"`
}

type authorityMarketData struct {
	Venue             string                  `json:"venue"`
	Environment       string                  `json:"environment"`
	Product           string                  `json:"product"`
	InstrumentID      string                  `json:"instrumentId"`
	BestBid           string                  `json:"bestBid"`
	BestBidSize       string                  `json:"bestBidSize"`
	BestAsk           string                  `json:"bestAsk"`
	BestAskSize       string                  `json:"bestAskSize"`
	Bids              []authorityLevel        `json:"bids"`
	Asks              []authorityLevel        `json:"asks"`
	MarkPrice         string                  `json:"markPrice,omitempty"`
	FundingRate       string                  `json:"fundingRate,omitempty"`
	FundingObservedAt time.Time               `json:"fundingObservedAt,omitempty"`
	FundingTime       time.Time               `json:"fundingTime,omitempty"`
	NextFundingTime   time.Time               `json:"nextFundingTime,omitempty"`
	FundingHistory    []authorityFundingPoint `json:"fundingHistory,omitempty"`
	OpenInterest      string                  `json:"openInterest,omitempty"`
	ObservedAt        time.Time               `json:"observedAt"`
}

type authorityLevel struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

type authorityFundingPoint struct {
	Rate        string    `json:"rate"`
	ObservedAt  time.Time `json:"observedAt,omitempty"`
	FundingTime time.Time `json:"fundingTime"`
}

const (
	authorityDepthLimit          = 20
	authorityFundingHistoryLimit = 20
)

type authorityReadSource interface {
	Venues() []authorityVenue
	Instruments() (authorityCatalogInstruments, error)
	MarketData(context.Context, string, string, string, string) (authorityMarketData, error)
}

type authorityAccountSource interface {
	Accounts() []account.Resource
	Instruments(domain.AccountID) (catalog.Snapshot, error)
	Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error)
	Spot(domain.AccountID) (exchange.SpotClient, error)
	Perp(domain.AccountID) (exchange.PerpClient, error)
	Capability(domain.AccountID) (capability.Row, error)
}

type managerAuthorityReads struct{ manager authorityAccountSource }

type opaqueRuntimeLifecycle struct {
	resolve    func(context.Context, secret.OpaqueRef) (string, error)
	activate   func(context.Context, account.DesiredRuntime, string) error
	deactivate func(context.Context, domain.AccountID) error
	blockers   func(context.Context, domain.AccountID) (account.RuntimeBlockers, error)
}

func newOpaqueRuntimeLifecycle(
	resolve func(context.Context, secret.OpaqueRef) (string, error),
	activate func(context.Context, account.DesiredRuntime, string) error,
	deactivate func(context.Context, domain.AccountID) error,
	blockers func(context.Context, domain.AccountID) (account.RuntimeBlockers, error),
) (*opaqueRuntimeLifecycle, error) {
	if resolve == nil || activate == nil || deactivate == nil || blockers == nil {
		return nil, errors.New("runtime lifecycle dependencies are incomplete")
	}
	return &opaqueRuntimeLifecycle{resolve: resolve, activate: activate, deactivate: deactivate, blockers: blockers}, nil
}

func (lifecycle *opaqueRuntimeLifecycle) Validate(ctx context.Context, desired account.DesiredRuntime) error {
	material, err := lifecycle.resolve(ctx, desired.SecretRef)
	if err != nil || material == "" {
		return errors.New("runtime secret resolution failed")
	}
	material = ""
	return nil
}

func (lifecycle *opaqueRuntimeLifecycle) Activate(ctx context.Context, desired account.DesiredRuntime) error {
	material, err := lifecycle.resolve(ctx, desired.SecretRef)
	if err != nil || material == "" {
		return errors.New("runtime secret resolution failed")
	}
	defer func() { material = "" }()
	if err := lifecycle.activate(ctx, desired, material); err != nil {
		return errors.New("runtime activation failed")
	}
	return nil
}

func (lifecycle *opaqueRuntimeLifecycle) Deactivate(ctx context.Context, accountID domain.AccountID) error {
	if err := lifecycle.deactivate(ctx, accountID); err != nil {
		return errors.New("runtime deactivation failed")
	}
	return nil
}

func (lifecycle *opaqueRuntimeLifecycle) Blockers(ctx context.Context, accountID domain.AccountID) (account.RuntimeBlockers, error) {
	blockers, err := lifecycle.blockers(ctx, accountID)
	if err != nil {
		return account.RuntimeBlockers{}, errors.New("runtime blocker lookup failed")
	}
	return blockers, nil
}

func newGatewayAuthority(ctx context.Context, desiredStore store.DesiredStateStore, manager *account.Manager, mode account.RuntimeMode) (*gatewayAuthority, error) {
	if ctx == nil || desiredStore == nil || manager == nil {
		return nil, errors.New("gateway authority dependencies are incomplete")
	}
	if mode == "" {
		mode = account.RuntimeModeTest
	}
	authority := &gatewayAuthority{
		store: desiredStore, manager: manager, reads: managerAuthorityReads{manager: manager}, mode: mode, now: time.Now,
	}
	if err := authority.restore(ctx); err != nil {
		return nil, errors.New("restore gateway authority failed")
	}
	return authority, nil
}

func (authority *gatewayAuthority) Execute(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if ctx == nil {
		return httpapi.AuthorityResponse{}, authorityError(errors.New("context is required"))
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()

	var response httpapi.AuthorityResponse
	var err error
	switch request.Operation {
	case httpapi.AuthorityCreateDesired:
		response, err = authority.create(ctx, request.Desired)
	case httpapi.AuthorityListDesired:
		response, err = authority.listDesired(ctx)
	case httpapi.AuthorityGetDesired:
		response, err = authority.getDesired(ctx, request.AccountID)
	case httpapi.AuthorityValidateDesired:
		response, err = authority.validate(ctx, request)
	case httpapi.AuthorityRotateDesired:
		response, err = authority.rotate(ctx, request)
	case httpapi.AuthorityUpdateDesired:
		response, err = authority.update(ctx, request)
	case httpapi.AuthorityDisableDesired, httpapi.AuthorityDrainDesired:
		response, err = authority.transition(ctx, request)
	case httpapi.AuthorityDeleteDesired:
		err = authority.remove(ctx, request)
	case httpapi.AuthorityListRuntimeAccounts:
		response.Body = authority.manager.Runtimes()
	case httpapi.AuthorityGetRuntimeAccount:
		response.Body, err = authority.manager.Runtime(domain.AccountID(request.AccountID))
	case httpapi.AuthorityRuntimeStatus:
		response.Body = authorityRuntimeStatus{Mode: authority.mode, Accounts: authority.manager.Runtimes()}
	case httpapi.AuthorityRuntimeReadiness:
		response.Body = httpapi.RuntimeReadiness{Ready: authority.manager.RuntimeReady()}
	case httpapi.AuthorityCatalogVenues:
		response.Body = authority.reads.Venues()
	case httpapi.AuthorityCatalogInstruments:
		response.Body, err = authority.reads.Instruments()
	case httpapi.AuthorityMarketData:
		venue, environment, product, instrumentID := request.Query.Get("venue"), request.Query.Get("environment"), request.Query.Get("product"), request.Query.Get("instrumentId")
		if strings.TrimSpace(venue) == "" || strings.TrimSpace(environment) == "" || strings.TrimSpace(product) == "" || strings.TrimSpace(instrumentID) == "" {
			err = errors.New("market-data composite identity is required")
			break
		}
		var fact authorityMarketData
		fact, err = authority.reads.MarketData(ctx, venue, environment, product, instrumentID)
		if err == nil && (fact.Venue != venue || fact.Environment != environment || fact.Product != product || fact.InstrumentID != instrumentID) {
			err = errors.New("market-data composite identity mismatch")
		} else {
			response.Body = fact
		}
	case httpapi.AuthorityMarketDataReadiness:
		if len(request.Query) != 0 {
			err = errors.New("market-data readiness query is not allowed")
		} else {
			var catalog authorityCatalogInstruments
			catalog, err = authority.reads.Instruments()
			ready := false
			for _, instrument := range catalog.Instruments {
				if instrument.Ready {
					ready = true
					break
				}
			}
			response.Body = map[string]bool{"ready": ready}
		}
	case httpapi.AuthorityRuntimeOpenOrders, httpapi.AuthorityRuntimePositions,
		httpapi.AuthorityRuntimeUncertain, httpapi.AuthorityRuntimeEvents,
		httpapi.AuthorityPrivateEvents:
		response.Body = []any{}
	default:
		err = errors.New("unsupported authority operation")
	}
	if err != nil {
		return httpapi.AuthorityResponse{}, authorityError(err)
	}
	return response, nil
}

func (reads managerAuthorityReads) Venues() []authorityVenue {
	resources := reads.manager.Accounts()
	venues := make([]authorityVenue, 0, len(resources))
	indexes := make(map[string]int, len(resources))
	for _, resource := range resources {
		key := string(resource.Venue) + "\x00" + string(resource.Environment)
		if index, ok := indexes[key]; ok {
			venues[index].Ready = venues[index].Ready || resource.ReadReady
			continue
		}
		indexes[key] = len(venues)
		venues = append(venues, authorityVenue{
			Venue: string(resource.Venue), Environment: string(resource.Environment), Ready: resource.ReadReady,
		})
	}
	sort.Slice(venues, func(i, j int) bool {
		if venues[i].Venue == venues[j].Venue {
			return venues[i].Environment < venues[j].Environment
		}
		return venues[i].Venue < venues[j].Venue
	})
	return venues
}

func (reads managerAuthorityReads) Instruments() (authorityCatalogInstruments, error) {
	resources := reads.manager.Accounts()
	instruments := make([]authorityInstrument, 0)
	indexes := make(map[string]int)
	contributors := make([]string, 0, len(resources))
	var observedAt time.Time
	for _, resource := range resources {
		snapshot, err := reads.manager.Instruments(resource.AccountID)
		if err != nil {
			return authorityCatalogInstruments{}, fmt.Errorf("read configured account catalog: %w", err)
		}
		if strings.TrimSpace(snapshot.Revision) == "" || snapshot.LoadedAt.IsZero() {
			return authorityCatalogInstruments{}, errors.New("configured account catalog authority is incomplete")
		}
		contributors = append(contributors, strings.Join([]string{string(resource.Venue), string(resource.Environment), string(resource.Product), resource.Settlement, snapshot.Revision}, "\x00"))
		if observedAt.IsZero() || snapshot.LoadedAt.Before(observedAt) {
			observedAt = snapshot.LoadedAt.UTC()
		}
		for _, instrument := range snapshot.Instruments {
			key := strings.Join([]string{string(instrument.Venue), string(instrument.Environment), string(instrument.Product), string(instrument.ID)}, "\x00")
			if index, ok := indexes[key]; ok {
				instruments[index].Ready = instruments[index].Ready || resource.ReadReady
				continue
			}
			minNotional := ""
			if instrument.Rules.MinNotional.Valid {
				minNotional = instrument.Rules.MinNotional.Value.String()
			}
			indexes[key] = len(instruments)
			instruments = append(instruments, authorityInstrument{
				InstrumentID: string(instrument.ID), Venue: string(instrument.Venue), Environment: string(instrument.Environment), Product: string(instrument.Product),
				Settlement: instrument.Settlement, Symbol: instrument.Symbol, BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
				Rules: authorityTradeRules{PriceIncrement: instrument.Rules.PriceIncrement.String(), QuantityIncrement: instrument.Rules.QuantityIncrement.String(), MinQuantity: instrument.Rules.MinQuantity.String(), MinNotional: minNotional},
				Ready: resource.ReadReady,
			})
		}
	}
	sort.Slice(instruments, func(i, j int) bool {
		if instruments[i].Venue == instruments[j].Venue {
			if instruments[i].Environment == instruments[j].Environment {
				if instruments[i].Product == instruments[j].Product {
					return instruments[i].InstrumentID < instruments[j].InstrumentID
				}
				return instruments[i].Product < instruments[j].Product
			}
			return instruments[i].Environment < instruments[j].Environment
		}
		return instruments[i].Venue < instruments[j].Venue
	})
	sort.Strings(contributors)
	fingerprint, err := json.Marshal(contributors)
	if err != nil {
		return authorityCatalogInstruments{}, errors.New("catalog revision aggregation failed")
	}
	revision := sha256.Sum256(fingerprint)
	return authorityCatalogInstruments{Revision: fmt.Sprintf("gateway-%x", revision[:16]), ObservedAt: observedAt, Instruments: instruments}, nil
}

func (reads managerAuthorityReads) MarketData(ctx context.Context, venue, environment, product, instrumentID string) (authorityMarketData, error) {
	venue = strings.TrimSpace(venue)
	environment = strings.TrimSpace(environment)
	product = strings.TrimSpace(product)
	instrumentID = strings.TrimSpace(instrumentID)
	if venue == "" || environment == "" || instrumentID == "" || (product != string(exchange.ProductSpot) && product != string(exchange.ProductPerp)) {
		return authorityMarketData{}, errors.New("market-data venue, environment, product, and instrumentId are required")
	}
	for _, resource := range reads.manager.Accounts() {
		if string(resource.Venue) != venue || string(resource.Environment) != environment || string(resource.Product) != product || !resource.ReadReady {
			continue
		}
		instrument, err := reads.manager.Resolve(ctx, resource.AccountID, domain.InstrumentID(instrumentID))
		if err != nil {
			continue
		}
		result := authorityMarketData{Venue: venue, Environment: environment, Product: product, InstrumentID: instrumentID}
		var market exchange.MarketREST
		switch resource.Product {
		case exchange.ProductSpot:
			market, err = reads.manager.Spot(resource.AccountID)
		case exchange.ProductPerp:
			var client exchange.PerpClient
			client, err = reads.manager.Perp(resource.AccountID)
			market = client
			if err == nil {
				row, capabilityErr := reads.manager.Capability(resource.AccountID)
				if capabilityErr != nil {
					return authorityMarketData{}, fmt.Errorf("read market-data capability: %w", capabilityErr)
				}
				if marks, ok := any(client).(exchange.PerpMarkPriceREST); ok {
					mark, markErr := marks.MarkPrice(ctx, instrument)
					if markErr != nil {
						return authorityMarketData{}, fmt.Errorf("read mark price: %w", markErr)
					}
					result.MarkPrice = mark.Price.String()
					result.ObservedAt = mark.Time.UTC()
				}
				if !row.Operations.Funding {
					return authorityMarketData{}, errors.New("configured perp funding capability is unavailable")
				}
				if row.Operations.Funding {
					funding, fundingErr := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: instrument})
					if fundingErr != nil {
						return authorityMarketData{}, fmt.Errorf("read funding rate: %w", fundingErr)
					}
					result.FundingRate = funding.Rate.String()
					result.FundingObservedAt = funding.ObservedAt.UTC()
					result.FundingTime = funding.FundingTime.UTC()
					result.NextFundingTime = funding.NextFundingTime.UTC()
					if result.FundingObservedAt.IsZero() || result.FundingTime.IsZero() || result.NextFundingTime.IsZero() {
						return authorityMarketData{}, errors.New("current funding timestamps are unavailable")
					}
					if funding.ObservedAt.After(result.ObservedAt) {
						result.ObservedAt = funding.ObservedAt.UTC()
					}
					history, historyErr := client.FundingRateHistory(ctx, exchange.FundingRateHistoryRequest{Instrument: instrument, Limit: authorityFundingHistoryLimit})
					if historyErr != nil {
						return authorityMarketData{}, fmt.Errorf("read funding history: %w", historyErr)
					}
					if len(history.Rates) > authorityFundingHistoryLimit {
						return authorityMarketData{}, errors.New("funding history exceeds requested bound")
					}
					result.FundingHistory = make([]authorityFundingPoint, len(history.Rates))
					for index, rate := range history.Rates {
						if rate.FundingTime.IsZero() {
							return authorityMarketData{}, errors.New("funding history timestamp is unavailable")
						}
						result.FundingHistory[index] = authorityFundingPoint{Rate: rate.Rate.String(), ObservedAt: rate.ObservedAt.UTC(), FundingTime: rate.FundingTime.UTC()}
					}
				}
			}
		default:
			continue
		}
		if err != nil || market == nil {
			continue
		}
		book, err := market.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument, Limit: authorityDepthLimit})
		if err != nil {
			return authorityMarketData{}, fmt.Errorf("read order book: %w", err)
		}
		if book.Time.IsZero() || len(book.Bids) == 0 || len(book.Asks) == 0 || len(book.Bids) > authorityDepthLimit || len(book.Asks) > authorityDepthLimit {
			return authorityMarketData{}, errors.New("bounded two-sided order book is unavailable")
		}
		result.Bids, err = projectAuthorityLevels(book.Bids)
		if err != nil {
			return authorityMarketData{}, err
		}
		result.Asks, err = projectAuthorityLevels(book.Asks)
		if err != nil {
			return authorityMarketData{}, err
		}
		result.BestBid, result.BestBidSize = result.Bids[0].Price, result.Bids[0].Quantity
		result.BestAsk, result.BestAskSize = result.Asks[0].Price, result.Asks[0].Quantity
		if result.ObservedAt.IsZero() || book.Time.After(result.ObservedAt) {
			result.ObservedAt = book.Time.UTC()
		}
		if result.ObservedAt.IsZero() {
			return authorityMarketData{}, errors.New("market-data observation time is unavailable")
		}
		return result, nil
	}
	return authorityMarketData{}, errors.New("configured market-data capability is unavailable")
}

func projectAuthorityLevels(levels []exchange.BookLevel) ([]authorityLevel, error) {
	result := make([]authorityLevel, len(levels))
	for index, level := range levels {
		if !level.Price.IsPositive() || !level.Quantity.IsPositive() {
			return nil, errors.New("order-book level is invalid")
		}
		result[index] = authorityLevel{Price: level.Price.String(), Quantity: level.Quantity.String()}
	}
	return result, nil
}

func (authority *gatewayAuthority) create(ctx context.Context, input *httpapi.DesiredAccount) (httpapi.AuthorityResponse, error) {
	if input == nil {
		return httpapi.AuthorityResponse{}, errors.New("desired account is required")
	}
	desired, err := desiredFromHTTP(*input, authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	created, err := authority.store.CreateDesiredAccount(ctx, desired)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(created), nil
}

func (authority *gatewayAuthority) listDesired(ctx context.Context) (httpapi.AuthorityResponse, error) {
	items, err := authority.store.ListDesiredAccounts(ctx)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	result := make([]httpapi.DesiredAccount, len(items))
	for index := range items {
		result[index] = desiredToHTTP(items[index])
	}
	return httpapi.AuthorityResponse{Body: result}, nil
}

func (authority *gatewayAuthority) getDesired(ctx context.Context, accountID string) (httpapi.AuthorityResponse, error) {
	desired, err := authority.store.LoadDesiredAccount(ctx, domain.AccountID(accountID))
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(desired), nil
}

func (authority *gatewayAuthority) validate(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil || request.Mutation.DesiredStateRevision != current.DesiredStateRevision ||
		request.Mutation.RuntimeGeneration != current.RuntimeGeneration || request.Mutation.CredentialVersion != current.CredentialVersion {
		return httpapi.AuthorityResponse{}, store.ErrDesiredStateRevisionConflict
	}
	desired, err := authority.runtimeDesired(current, current.SecretRef, account.RuntimeStateActive)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if err := authority.manager.ValidateRuntime(ctx, desired); err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return httpapi.AuthorityResponse{Body: map[string]any{"ready": true}, DesiredStateRevision: current.DesiredStateRevision}, nil
}

func (authority *gatewayAuthority) rotate(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil || request.Mutation.RotationFence == nil {
		return httpapi.AuthorityResponse{}, errors.New("rotation mutation is incomplete")
	}
	next, err := desiredFromMutation(current, *request.Mutation, store.DesiredRotating, authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	fence := next.RotationFence
	candidate := next
	candidate.RuntimeGeneration = fence.ToRuntimeGeneration
	candidate.CredentialVersion = fence.ToCredentialVersion
	candidate.RequestedState = store.DesiredActive
	desired, err := authority.runtimeDesired(candidate, next.SecretRef, account.RuntimeStateActive)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if err := authority.manager.ValidateRuntime(ctx, desired); err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	stored, err := authority.store.ApplyDesiredAccount(ctx, request.ExpectedRevision, next)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(stored), nil
}

func (authority *gatewayAuthority) update(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil {
		return httpapi.AuthorityResponse{}, errors.New("desired mutation is required")
	}
	next, err := desiredFromMutation(current, *request.Mutation, store.DesiredAccountState(request.Mutation.RequestedState), authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	stored, err := authority.store.ApplyDesiredAccount(ctx, request.ExpectedRevision, next)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}

	switch {
	case current.RequestedState == store.DesiredCreating && next.RequestedState == store.DesiredActive:
		runtimeDesired, convertErr := authority.runtimeDesired(next, next.SecretRef, account.RuntimeStateActive)
		if convertErr != nil {
			return httpapi.AuthorityResponse{}, convertErr
		}
		if _, err := authority.manager.CreateRuntime(ctx, runtimeDesired); err != nil {
			return httpapi.AuthorityResponse{}, err
		}
	case current.RequestedState == store.DesiredRotating && next.RequestedState == store.DesiredActive:
		runtimeDesired, convertErr := authority.runtimeDesired(next, next.SecretRef, account.RuntimeStateActive)
		if convertErr != nil {
			return httpapi.AuthorityResponse{}, convertErr
		}
		if _, err := authority.manager.RotateRuntime(ctx, runtimeDesired, uint64(request.ExpectedRevision)); err != nil {
			return httpapi.AuthorityResponse{}, err
		}
	}
	return desiredResponse(stored), nil
}

func (authority *gatewayAuthority) transition(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil {
		return httpapi.AuthorityResponse{}, errors.New("desired mutation is required")
	}
	nextState := store.DesiredDisabled
	runtimeState := account.RuntimeStateDisabled
	if request.Operation == httpapi.AuthorityDrainDesired {
		nextState = store.DesiredDraining
		runtimeState = account.RuntimeStateDraining
	}
	next, err := desiredFromMutation(current, *request.Mutation, nextState, authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	runtime, err := authority.manager.Runtime(current.AccountID)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if runtimeState == account.RuntimeStateDraining {
		_, err = authority.manager.DrainRuntime(ctx, current.AccountID, runtime.Desired.DesiredStateRevision, uint64(next.DesiredStateRevision))
	} else {
		_, err = authority.manager.DisableRuntime(ctx, current.AccountID, runtime.Desired.DesiredStateRevision, uint64(next.DesiredStateRevision))
	}
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	stored, err := authority.store.ApplyDesiredAccount(ctx, request.ExpectedRevision, next)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(stored), nil
}

func (authority *gatewayAuthority) remove(ctx context.Context, request httpapi.AuthorityRequest) error {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return err
	}
	if current.RequestedState != store.DesiredRemoved {
		return store.ErrInvalidAccountTransition
	}
	if runtime, runtimeErr := authority.manager.Runtime(current.AccountID); runtimeErr == nil {
		if err := authority.manager.RemoveRuntime(ctx, current.AccountID, runtime.Desired.DesiredStateRevision); err != nil {
			return err
		}
	} else if !errors.Is(runtimeErr, account.ErrUnknownAccount) {
		return runtimeErr
	}
	return authority.store.DeleteDesiredAccount(ctx, current.AccountID, request.ExpectedRevision)
}

func (authority *gatewayAuthority) loadExpected(ctx context.Context, accountID string, revision int64) (store.DesiredAccount, error) {
	desired, err := authority.store.LoadDesiredAccount(ctx, domain.AccountID(accountID))
	if err != nil {
		return store.DesiredAccount{}, err
	}
	if revision <= 0 || desired.DesiredStateRevision != revision {
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	return desired, nil
}

func (authority *gatewayAuthority) restore(ctx context.Context) error {
	items, err := authority.store.ListRecoverableDesiredAccounts(ctx)
	if err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	runtimes := make([]account.DesiredRuntime, 0, len(items))
	for _, desired := range items {
		if desired.RequestedState == store.DesiredCreating {
			continue
		}
		secretRef := desired.SecretRef
		state := account.RuntimeState(desired.RequestedState)
		if desired.RequestedState == store.DesiredRotating {
			secretRef = desired.PreviousSecretRef
			state = account.RuntimeStateActive
		}
		runtimeDesired, convertErr := authority.runtimeDesired(desired, secretRef, state)
		if convertErr != nil {
			return convertErr
		}
		runtimes = append(runtimes, runtimeDesired)
	}
	return authority.manager.RestoreRuntimes(ctx, runtimes)
}

func (authority *gatewayAuthority) runtimeDesired(desired store.DesiredAccount, refValue string, state account.RuntimeState) (account.DesiredRuntime, error) {
	ref, err := secret.ParseOpaqueRef(refValue)
	if err != nil {
		return account.DesiredRuntime{}, err
	}
	rotationFence := ""
	if desired.RotationFence != nil {
		rotationFence = desired.RotationFence.ID
	}
	return account.DesiredRuntime{
		AccountID: desired.AccountID, Venue: desired.Venue, VenueEnvironment: desired.VenueEnvironment,
		Mode: authority.mode, SecretRef: ref, DesiredStateRevision: uint64(desired.DesiredStateRevision),
		RuntimeGeneration: uint64(desired.RuntimeGeneration), CredentialVersion: uint64(desired.CredentialVersion),
		RequestedState: state, RotationFence: rotationFence,
	}, nil
}

func desiredFromHTTP(input httpapi.DesiredAccount, now time.Time) (store.DesiredAccount, error) {
	desired := store.DesiredAccount{
		AccountID: domain.AccountID(input.AccountID), Venue: input.Venue, VenueEnvironment: input.VenueEnvironment,
		SecretRef: input.SecretRef, DesiredStateRevision: input.DesiredStateRevision,
		RuntimeGeneration: input.RuntimeGeneration, CredentialVersion: input.CredentialVersion,
		RequestedState: store.DesiredAccountState(input.RequestedState), UpdatedAt: now.UTC(),
	}
	if input.RotationFence != nil {
		desired.RotationFence = rotationFenceFromHTTP(*input.RotationFence, now)
	}
	if err := desired.Validate(); err != nil {
		return store.DesiredAccount{}, err
	}
	return desired, nil
}

func desiredFromMutation(current store.DesiredAccount, mutation httpapi.DesiredMutation, state store.DesiredAccountState, now time.Time) (store.DesiredAccount, error) {
	next := current
	if mutation.SecretRef != "" {
		next.SecretRef = mutation.SecretRef
	}
	next.DesiredStateRevision = mutation.DesiredStateRevision
	next.RuntimeGeneration = mutation.RuntimeGeneration
	next.CredentialVersion = mutation.CredentialVersion
	next.RequestedState = state
	next.UpdatedAt = now.UTC()
	if mutation.RotationFence != nil {
		next.RotationFence = rotationFenceFromHTTP(*mutation.RotationFence, now)
	} else {
		next.RotationFence = nil
	}
	if err := next.Validate(); err != nil {
		return store.DesiredAccount{}, err
	}
	return next, nil
}

func rotationFenceFromHTTP(input httpapi.RotationFence, now time.Time) *store.RotationFence {
	fence := &store.RotationFence{
		ID: input.RotationFenceID, State: store.RotationFenceState(input.State),
		FromCredentialVersion: input.FromCredentialVersion, ToCredentialVersion: input.ToCredentialVersion,
		FromRuntimeGeneration: input.FromRuntimeGeneration, ToRuntimeGeneration: input.ToRuntimeGeneration,
		ArmedAt: now.UTC(),
	}
	if fence.State == store.RotationReleased {
		released := now.UTC()
		fence.ReleasedAt = &released
	}
	return fence
}

func desiredResponse(desired store.DesiredAccount) httpapi.AuthorityResponse {
	return httpapi.AuthorityResponse{Body: desiredToHTTP(desired), DesiredStateRevision: desired.DesiredStateRevision}
}

func desiredToHTTP(desired store.DesiredAccount) httpapi.DesiredAccount {
	result := httpapi.DesiredAccount{
		AccountID: string(desired.AccountID), Venue: desired.Venue, VenueEnvironment: desired.VenueEnvironment,
		SecretRef: "redacted", DesiredStateRevision: desired.DesiredStateRevision,
		RuntimeGeneration: desired.RuntimeGeneration, CredentialVersion: desired.CredentialVersion,
		RequestedState: string(desired.RequestedState),
	}
	if desired.RotationFence != nil {
		result.RotationFence = &httpapi.RotationFence{
			RotationFenceID: desired.RotationFence.ID, State: string(desired.RotationFence.State),
			FromCredentialVersion: desired.RotationFence.FromCredentialVersion,
			ToCredentialVersion:   desired.RotationFence.ToCredentialVersion,
			FromRuntimeGeneration: desired.RotationFence.FromRuntimeGeneration,
			ToRuntimeGeneration:   desired.RotationFence.ToRuntimeGeneration,
		}
	}
	return result
}

func authorityError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, account.ErrUnknownAccount):
		return httpapi.AuthorityError{Status: http.StatusNotFound, Code: "DESIRED_ACCOUNT_NOT_FOUND"}
	case errors.Is(err, store.ErrDesiredStateRevisionConflict), errors.Is(err, account.ErrRuntimeRevision):
		return httpapi.AuthorityError{Status: http.StatusPreconditionFailed, Code: "DESIRED_STATE_REVISION_CONFLICT", Retryable: true}
	case errors.Is(err, store.ErrInvalidAccountTransition):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "INVALID_ACCOUNT_TRANSITION"}
	case errors.Is(err, store.ErrStaleRuntimeGeneration), errors.Is(err, account.ErrStaleWriteFence):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "STALE_RUNTIME_FENCE", Retryable: true}
	case errors.Is(err, account.ErrRuntimeBlocked):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "ACCOUNT_RUNTIME_BLOCKED"}
	case errors.Is(err, account.ErrRuntimeModeMismatch):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "RUNTIME_MODE_MISMATCH"}
	case errors.Is(err, account.ErrLiveWriteDisabled):
		return httpapi.AuthorityError{Status: http.StatusForbidden, Code: "LIVE_PRIVATE_WRITE_DISABLED"}
	default:
		return httpapi.AuthorityError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Retryable: true}
	}
}

package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

type fillResource struct {
	AccountID       domain.AccountID      `json:"account_id"`
	InstrumentID    domain.InstrumentID   `json:"instrument_id,omitempty"`
	GatewayOrderID  domain.GatewayOrderID `json:"gateway_order_id,omitempty"`
	SettlementAsset string                `json:"settlement_asset"`
	Fill            exchange.Fill         `json:"fill"`
}

type balanceResource struct {
	AccountID       domain.AccountID `json:"account_id"`
	Product         exchange.Product `json:"product"`
	SettlementAsset string           `json:"settlement_asset,omitempty"`
	AvailableMargin string           `json:"available_margin,omitempty"`
	Balance         exchange.Balance `json:"balance"`
}

type positionResource struct {
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id,omitempty"`
	Position     exchange.Position   `json:"position"`
}

type fundingResource struct {
	AccountID    domain.AccountID      `json:"account_id"`
	InstrumentID domain.InstrumentID   `json:"instrument_id"`
	FundingRate  fundingRateProjection `json:"funding_rate"`
}

type fundingPaymentPageResource struct {
	Items               []fundingPaymentResource         `json:"items"`
	NextCursor          string                           `json:"next_cursor,omitempty"`
	Scope               fundingPaymentScopeResource      `json:"scope"`
	AccountID           domain.AccountID                 `json:"account_id"`
	InstrumentID        domain.InstrumentID              `json:"instrument_id,omitempty"`
	SettlementAsset     string                           `json:"settlement_asset,omitempty"`
	PaginationTraversal fundingPaymentTraversalResource  `json:"pagination_traversal"`
	Start               time.Time                        `json:"start,omitempty"`
	End                 time.Time                        `json:"end,omitempty"`
	Page                exchange.PageInfo                `json:"page"`
	Watermark           exchange.FundingPaymentWatermark `json:"watermark"`
	Status              exchange.FundingPaymentStatus    `json:"status"`
	AsOf                time.Time                        `json:"as_of"`
	CompleteThrough     time.Time                        `json:"complete_through,omitempty"`
	Revision            int64                            `json:"revision,omitempty"`
}

type fundingPaymentScopeResource struct {
	AccountID       domain.AccountID    `json:"account_id"`
	InstrumentID    domain.InstrumentID `json:"instrument_id,omitempty"`
	AllInstruments  bool                `json:"all_instruments"`
	SettlementAsset string              `json:"settlement_asset,omitempty"`
	Start           time.Time           `json:"start"`
	End             time.Time           `json:"end"`
}

type fundingPaymentTraversalResource struct {
	Cursor       string `json:"cursor,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	HasMoreKnown bool   `json:"has_more_known"`
	HasMore      bool   `json:"has_more"`
}

type fundingPaymentResource struct {
	PaymentID        string               `json:"payment_id,omitempty"`
	FactID           string               `json:"fact_id"`
	SupersedesFactID string               `json:"supersedes_fact_id,omitempty"`
	AccountID        domain.AccountID     `json:"account_id"`
	InstrumentID     domain.InstrumentID  `json:"instrument_id"`
	Venue            exchange.Venue       `json:"venue"`
	VenueEnvironment exchange.Environment `json:"venue_environment"`
	Product          exchange.Product     `json:"product"`
	SettlementAsset  string               `json:"settlement_asset"`
	Amount           string               `json:"amount"`
	EffectiveAt      time.Time            `json:"effective_at,omitempty"`
	ObservedAt       time.Time            `json:"observed_at,omitempty"`
	Revision         int64                `json:"revision,omitempty"`
	Version          string               `json:"version,omitempty"`
	SourceRef        string               `json:"source_ref,omitempty"`
	FormulaVersion   string               `json:"formula_version,omitempty"`
}

type leverageBody struct {
	Leverage int `json:"leverage"`
}

func (handler *Handler) listFills(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.Principal(r.Context())
	gatewayOrderID, err := optionalGatewayOrderID(r.URL.Query().Get("gateway_order_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	var exactOrder order.Result
	var selection resourceSelection
	if gatewayOrderID != "" {
		accountID, parseErr := optionalAccountID(r.URL.Query().Get("account_id"))
		if parseErr != nil {
			handler.invalid(w)
			return
		}
		instrumentID, parseErr := optionalInstrumentID(r.URL.Query().Get("instrument_id"))
		if parseErr != nil {
			handler.invalid(w)
			return
		}
		limit, parseErr := listLimit(r)
		if parseErr != nil {
			handler.invalid(w)
			return
		}
		exactOrder, err = handler.orders.Get(r.Context(), principal, gatewayOrderID)
		if err != nil {
			handler.fail(w, err)
			return
		}
		if exactOrder.VenueOrderID == "" ||
			(accountID != "" && exactOrder.AccountID != accountID) ||
			(instrumentID != "" && exactOrder.InstrumentID != instrumentID) {
			handler.fail(w, store.ErrNotFound)
			return
		}
		resource, resourceErr := handler.accountRoute(
			exactOrder.AccountID,
			exchange.Product(exactOrder.InstrumentID.Product()),
			exactOrder.InstrumentID.Settlement(),
		)
		if resourceErr != nil {
			handler.fail(w, resourceErr)
			return
		}
		selection = resourceSelection{
			accounts: []account.Resource{resource}, accountID: exactOrder.AccountID,
			instrumentID: exactOrder.InstrumentID, cursor: r.URL.Query().Get("pagination.cursor"), limit: limit,
		}
	} else {
		selection, err = handler.resourceSelection(r, true)
		if err != nil {
			handler.invalid(w)
			return
		}
	}
	items := make([]fillResource, 0)
	var nextCursor string
	for _, resource := range selection.accounts {
		if gatewayOrderID != "" && resource.AccountID != exactOrder.AccountID {
			continue
		}
		if len(items) == selection.limit {
			break
		}
		instrument, err := handler.optionalResolvedInstrument(r.Context(), resource.AccountID, selection.instrumentID)
		if err != nil {
			handler.fail(w, err)
			return
		}
		instruments, err := handler.fundingPaymentInstrumentMap(r.Context(), resource.AccountID, instrument)
		if err != nil {
			handler.fail(w, err)
			return
		}
		request := exchange.FillsRequest{
			Instrument: instrument, Cursor: selection.cursor, Limit: selection.limit - len(items),
		}
		var page exchange.FillPage
		switch resource.Product {
		case exchange.ProductSpot:
			client, clientErr := handler.accounts.SpotRoute(resource.AccountID, resource.Settlement)
			if clientErr != nil {
				handler.fail(w, clientErr)
				return
			}
			page, err = client.Fills(r.Context(), request)
		case exchange.ProductPerp:
			client, clientErr := handler.accounts.PerpRoute(resource.AccountID, resource.Settlement)
			if clientErr != nil {
				handler.fail(w, clientErr)
				return
			}
			page, err = client.Fills(r.Context(), request)
		default:
			err = account.ErrProductMismatch
		}
		if err != nil {
			handler.fail(w, err)
			return
		}
		for _, fill := range page.Fills {
			resolvedInstrument, resolvedInstrumentID, resolveErr := resolveNativeInstrument(
				instrument, selection.instrumentID, instruments, fill.NativeSymbol, fill.NativeID,
			)
			if resolveErr != nil {
				handler.fail(w, resolveErr)
				return
			}
			settlementAsset := fillSettlementAsset(&resolvedInstrument)
			if settlementAsset == "" {
				handler.fail(w, exchange.ErrMalformedResponse)
				return
			}
			rowGatewayOrderID := domain.GatewayOrderID("")
			if gatewayOrderID != "" {
				if fill.OrderID != exactOrder.VenueOrderID {
					continue
				}
				fill.OrderID = ""
				rowGatewayOrderID = gatewayOrderID
			}
			items = append(items, fillResource{
				AccountID: resource.AccountID, InstrumentID: resolvedInstrumentID,
				GatewayOrderID: rowGatewayOrderID, SettlementAsset: settlementAsset, Fill: fill,
			})
			if len(items) == selection.limit {
				break
			}
		}
		if page.Page.HasMoreKnown && page.Page.HasMore && page.Page.Cursor != "" {
			nextCursor = page.Page.Cursor
		}
	}
	writeJSON(w, http.StatusOK, collection[fillResource]{Items: items, NextCursor: nextCursor})
}

func fillSettlementAsset(instrument *exchange.Instrument) string {
	if instrument == nil {
		return ""
	}
	if instrument.Identity.SettleAsset != "" {
		return instrument.Identity.SettleAsset
	}
	return instrument.Identity.QuoteAsset
}

func (handler *Handler) listBalances(w http.ResponseWriter, r *http.Request) {
	selection, err := handler.resourceSelection(r, false)
	if err != nil {
		handler.invalid(w)
		return
	}
	items := make([]balanceResource, 0)
	for _, resource := range selection.accounts {
		if len(items) == selection.limit {
			break
		}
		var balances []exchange.Balance
		var availableMargin string
		switch resource.Product {
		case exchange.ProductSpot:
			client, clientErr := handler.accounts.SpotRoute(resource.AccountID, resource.Settlement)
			if clientErr != nil {
				handler.fail(w, clientErr)
				return
			}
			balances, err = client.Balances(r.Context())
		case exchange.ProductPerp:
			client, clientErr := handler.accounts.PerpRoute(resource.AccountID, resource.Settlement)
			if clientErr != nil {
				handler.fail(w, clientErr)
				return
			}
			var accountSnapshot exchange.PerpAccount
			accountSnapshot, err = client.PerpAccount(r.Context())
			balances = accountSnapshot.Balances
			if accountSnapshot.Available.Valid {
				availableMargin = accountSnapshot.Available.Value.String()
			}
		default:
			err = account.ErrProductMismatch
		}
		if err != nil {
			handler.fail(w, err)
			return
		}
		for _, balance := range balances {
			// Venue account endpoints commonly return every asset they have ever
			// listed. Binance alone contributes hundreds of all-zero rows, which
			// are not account state and can crowd real balances out of the bounded
			// response. Keep every row carrying money; omit only exact zeroes.
			if balance.Available.IsZero() && balance.Locked.IsZero() && balance.Total.IsZero() {
				continue
			}
			items = append(items, balanceResource{
				AccountID: resource.AccountID, Product: resource.Product,
				SettlementAsset: resource.Settlement, AvailableMargin: availableMargin, Balance: balance,
			})
			if len(items) == selection.limit {
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, collection[balanceResource]{Items: items})
}

func (handler *Handler) listPositions(w http.ResponseWriter, r *http.Request) {
	selection, err := handler.resourceSelection(r, true)
	if err != nil {
		handler.invalid(w)
		return
	}
	items := make([]positionResource, 0)
	// A named account with no perpetual route at all is a spot account being
	// asked a perpetual question, and that stays an error. But an account that
	// merely happens to also hold a spot route answers for its perpetual ones,
	// so the verdict waits until every route has been looked at.
	perpRoutes := 0
	for _, resource := range selection.accounts {
		if resource.Product == exchange.ProductSpot {
			continue
		}
		perpRoutes++
		if len(items) == selection.limit {
			break
		}
		instrument, err := handler.optionalResolvedInstrument(r.Context(), resource.AccountID, selection.instrumentID)
		if err != nil {
			handler.fail(w, err)
			return
		}
		client, err := handler.accounts.PerpRoute(resource.AccountID, resource.Settlement)
		if err != nil {
			handler.fail(w, err)
			return
		}
		positions, err := client.Positions(r.Context(), exchange.PositionsRequest{Instrument: instrument})
		if err != nil {
			handler.fail(w, fmt.Errorf("load positions for route %s: %w", resource.Settlement, err))
			return
		}
		var instruments map[string]fundingPaymentInstrumentRef
		if instrument != nil {
			instruments, err = handler.fundingPaymentInstrumentMap(r.Context(), resource.AccountID, instrument)
		} else {
			instruments, err = handler.positionInstrumentMap(r.Context(), resource, positions)
		}
		if err != nil {
			handler.fail(w, fmt.Errorf("resolve position instruments for route %s: %w", resource.Settlement, err))
			return
		}
		for _, position := range positions {
			_, instrumentID, err := resolveNativeInstrument(
				instrument, selection.instrumentID, instruments, position.NativeSymbol, position.NativeID,
			)
			if err != nil {
				handler.fail(w, err)
				return
			}
			items = append(items, positionResource{
				AccountID: resource.AccountID, InstrumentID: instrumentID, Position: position,
			})
			if len(items) == selection.limit {
				break
			}
		}
	}
	if selection.accountID != "" && perpRoutes == 0 {
		handler.fail(w, account.ErrProductMismatch)
		return
	}
	writeJSON(w, http.StatusOK, collection[positionResource]{Items: items})
}

type activePositionRouteSource interface {
	ActiveRoute(domain.AccountID, string, string) (domain.RouteID, *account.GenerationAdmission, error)
}

// getPositionSnapshot returns one complete account projection beside the
// exact replay cursor from the same Gateway event Hub. A transient venue or
// routing failure is an error, never an empty list: only a successfully loaded
// complete snapshot may prove that an account has no positions.
func (handler *Handler) getPositionSnapshot(w http.ResponseWriter, r *http.Request) {
	if handler.positions == nil {
		writeError(w, http.StatusServiceUnavailable, "POSITION_PROJECTION_UNAVAILABLE", "position projection is unavailable", "", true)
		return
	}
	if len(r.URL.Query()) != 1 {
		handler.invalid(w)
		return
	}
	accountID, err := parseAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	principal, ok := auth.Principal(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized", "", false)
		return
	}
	selection, err := handler.resourceSelection(requestWithPositionScope(r, accountID), true)
	if err != nil {
		handler.fail(w, err)
		return
	}
	perpRoutes := 0
	for _, resource := range selection.accounts {
		if resource.Product == exchange.ProductPerp {
			perpRoutes++
		}
	}
	if perpRoutes == 0 {
		handler.fail(w, account.ErrProductMismatch)
		return
	}
	snapshot, err := handler.positions.SynchronizeAccount(principal, accountID, func() ([]gatewayevent.PositionSeed, error) {
		seeds := make([]gatewayevent.PositionSeed, 0)
		for _, resource := range selection.accounts {
			if resource.Product == exchange.ProductSpot {
				continue
			}
			routeID, loadErr := handler.positionRouteID(resource)
			if loadErr != nil {
				return nil, loadErr
			}
			client, loadErr := handler.accounts.PerpRoute(resource.AccountID, resource.Settlement)
			if loadErr != nil {
				return nil, loadErr
			}
			positions, loadErr := client.Positions(r.Context(), exchange.PositionsRequest{})
			if loadErr != nil {
				return nil, fmt.Errorf("load positions for route %s: %w", routeID, loadErr)
			}
			instruments, loadErr := handler.positionInstrumentMap(r.Context(), resource, positions)
			if loadErr != nil {
				return nil, fmt.Errorf("resolve position instruments for route %s: %w", routeID, loadErr)
			}
			for _, position := range positions {
				_, instrumentID, resolveErr := resolveNativeInstrument(nil, "", instruments, position.NativeSymbol, position.NativeID)
				if resolveErr != nil {
					return nil, fmt.Errorf("resolve position identity for route %s: %w", routeID, resolveErr)
				}
				seeds = append(seeds, gatewayevent.PositionSeed{
					RouteID: routeID, InstrumentID: instrumentID, Position: position,
				})
			}
		}
		return seeds, nil
	})
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": snapshot})
}

func requestWithPositionScope(r *http.Request, accountID domain.AccountID) *http.Request {
	clone := r.Clone(r.Context())
	query := clone.URL.Query()
	query.Set("account_id", string(accountID))
	query.Set("pagination.limit", "500")
	clone.URL.RawQuery = query.Encode()
	return clone
}

func (handler *Handler) positionRouteID(resource account.Resource) (domain.RouteID, error) {
	if routes, ok := handler.accounts.(activePositionRouteSource); ok {
		routeID, _, err := routes.ActiveRoute(resource.AccountID, string(resource.Product), resource.Settlement)
		return routeID, err
	}
	// Tests and the source-compatibility account manager predate dynamic route
	// identities. Keep their snapshots deterministic without claiming this
	// synthetic value is a production route.
	value := "static-" + string(resource.Product)
	if resource.Settlement != "" {
		value += "-" + strings.ToLower(resource.Settlement)
	}
	routeID := domain.RouteID(value)
	if err := routeID.Validate(); err != nil {
		return "", err
	}
	return routeID, nil
}

func (handler *Handler) listFundingPayments(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	instrumentID, err := optionalInstrumentID(r.URL.Query().Get("instrument_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	start, end, err := parseFundingPaymentWindow(r)
	if err != nil {
		handler.invalid(w)
		return
	}
	limit, err := listLimit(r)
	if err != nil {
		handler.invalid(w)
		return
	}
	resource, err := handler.accountRoute(accountID, "", r.URL.Query().Get("settlement_asset"))
	if err != nil {
		handler.fail(w, err)
		return
	}
	if resource.Product != exchange.ProductPerp {
		writeJSON(w, http.StatusOK, emptyFundingPaymentPage(accountID, instrumentID, r.URL.Query().Get("settlement_asset"), start, end, exchange.FundingPaymentsUnsupported))
		return
	}
	row, err := handler.accounts.RouteCapability(accountID, string(resource.Product), resource.Settlement)
	if err != nil {
		handler.fail(w, err)
		return
	}
	if !row.Operations.AccountFundingPayments {
		writeJSON(w, http.StatusOK, emptyFundingPaymentPage(accountID, instrumentID, r.URL.Query().Get("settlement_asset"), start, end, exchange.FundingPaymentsUnsupported))
		return
	}
	var instrument *exchange.Instrument
	if instrumentID != "" {
		resolved, resolveErr := handler.accounts.Resolve(r.Context(), accountID, instrumentID)
		if resolveErr != nil {
			handler.fail(w, resolveErr)
			return
		}
		instrument = &resolved
	}
	client, err := handler.accounts.PerpRoute(accountID, resource.Settlement)
	if err != nil {
		handler.fail(w, err)
		return
	}
	facet, ok := client.(exchange.PerpFundingPaymentREST)
	if !ok {
		writeJSON(w, http.StatusOK, emptyFundingPaymentPage(accountID, instrumentID, r.URL.Query().Get("settlement_asset"), start, end, exchange.FundingPaymentsUnsupported))
		return
	}
	settlementAsset := r.URL.Query().Get("settlement_asset")
	page, err := facet.FundingPayments(r.Context(), exchange.FundingPaymentHistoryRequest{
		Instrument:      instrument,
		Start:           start,
		End:             end,
		Cursor:          r.URL.Query().Get("pagination.cursor"),
		Limit:           limit,
		SettlementAsset: settlementAsset,
	})
	if err != nil {
		if errors.Is(err, exchange.ErrInvalidRequest) {
			handler.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, fundingPaymentErrorPage(accountID, instrumentID, settlementAsset, start, end, err))
		return
	}
	instruments, err := handler.fundingPaymentInstrumentMap(r.Context(), accountID, instrument)
	if err != nil {
		handler.fail(w, err)
		return
	}
	projected, err := projectFundingPaymentPage(accountID, instrumentID, settlementAsset, resource, instrument, instruments, start, end, page)
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projected)
}

func (handler *Handler) listFundingRates(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	instrumentID, err := parseInstrumentID(r.URL.Query().Get("instrument_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	row, err := handler.accounts.RouteCapability(accountID, instrumentID.Product(), instrumentID.Settlement())
	if err != nil {
		handler.fail(w, err)
		return
	}
	if !row.Operations.Funding {
		handler.fail(w, exchange.ErrUnsupported)
		return
	}
	instrument, err := handler.accounts.Resolve(r.Context(), accountID, instrumentID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	client, err := handler.accounts.PerpRoute(accountID, instrument.Identity.SettleAsset)
	if err != nil {
		handler.fail(w, err)
		return
	}
	rate, err := client.FundingRate(r.Context(), exchange.FundingRateRequest{Instrument: instrument})
	if err != nil {
		handler.fail(w, err)
		return
	}
	if err := validateFundingRateInstrument(instrument, rate); err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fundingResource{
		AccountID: accountID, InstrumentID: instrumentID, FundingRate: projectFundingRate(rate),
	})
}

func (handler *Handler) setLeverage(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseAccountID(r.PathValue("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	instrumentID, err := parseInstrumentID(r.PathValue("instrument_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	var body leverageBody
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &body); err != nil || body.Leverage <= 0 {
		handler.invalid(w)
		return
	}
	if _, err := handler.accounts.RouteCapability(accountID, instrumentID.Product(), instrumentID.Settlement()); err != nil {
		handler.fail(w, err)
		return
	}
	principal, _ := auth.Principal(r.Context())
	correlationID, err := handler.nextCorrelationID()
	if err != nil {
		handler.fail(w, err)
		return
	}
	if err := handler.auditor.AppendAudit(r.Context(), store.AuditRecord{
		PrincipalID: principal, Action: store.AuditSetLeverage,
		ResourceID: string(instrumentID), Outcome: store.AuditUnsupported,
		CorrelationID: correlationID, AccountID: accountID, InstrumentID: instrumentID,
	}); err != nil {
		handler.failWithCorrelation(w, err, correlationID)
		return
	}
	writeError(w, http.StatusNotImplemented, "unsupported_capability", "durable leverage commands are not supported in Gateway V1", string(correlationID), false)
}

type resourceSelection struct {
	accounts     []account.Resource
	accountID    domain.AccountID
	instrumentID domain.InstrumentID
	cursor       string
	limit        int
}

func (handler *Handler) resourceSelection(r *http.Request, allowInstrument bool) (resourceSelection, error) {
	accountID, err := optionalAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		return resourceSelection{}, err
	}
	instrumentID, err := optionalInstrumentID(r.URL.Query().Get("instrument_id"))
	if err != nil {
		return resourceSelection{}, err
	}
	if !allowInstrument && instrumentID != "" {
		return resourceSelection{}, fmt.Errorf("instrument filter is not supported")
	}
	if instrumentID != "" && accountID == "" {
		return resourceSelection{}, fmt.Errorf("instrument filter requires account_id")
	}
	limit, err := listLimit(r)
	if err != nil {
		return resourceSelection{}, err
	}
	resources := handler.accounts.Accounts()
	if accountID != "" {
		// Every route of the account, not the first one found: an account that
		// settles perpetuals in two assets keeps two books, and a caller asking
		// for its balances means both of them.
		//
		// An instrument filter narrows that back down. It has to: the two books
		// hold different symbols, so asking the USDC route about a USDT symbol
		// is not a smaller question but a wrong one, and the venue answers it
		// with an error rather than an empty page.
		matched := make([]account.Resource, 0, 2)
		for _, resource := range resources {
			if resource.AccountID != accountID {
				continue
			}
			if instrumentID != "" {
				if product := instrumentID.Product(); product != "" && string(resource.Product) != product {
					continue
				}
				if settlement := instrumentID.Settlement(); settlement != "" && resource.Settlement != settlement {
					continue
				}
			}
			matched = append(matched, resource)
		}
		if len(matched) == 0 {
			return resourceSelection{}, account.ErrUnknownAccount
		}
		resources = matched
	}
	return resourceSelection{
		accounts: resources, accountID: accountID, instrumentID: instrumentID,
		cursor: r.URL.Query().Get("pagination.cursor"), limit: limit,
	}, nil
}

func (handler *Handler) optionalResolvedInstrument(ctx context.Context, accountID domain.AccountID, instrumentID domain.InstrumentID) (*exchange.Instrument, error) {
	if instrumentID == "" {
		return nil, nil
	}
	instrument, err := handler.accounts.Resolve(ctx, accountID, instrumentID)
	if err != nil {
		return nil, err
	}
	return &instrument, nil
}

// accountRoute names one route of one account. An account id alone is not
// always a route: bybit settles perpetuals in both USDT and USDC under a single
// set of credentials, and those two books hold different positions and different
// balances. product and settlement narrow the search and either may be empty,
// so a single-route account is still findable by id alone; when the hints leave
// two routes standing this reports ErrProductMismatch rather than picking one,
// because picking one would answer a question the caller did not ask.
func (handler *Handler) accountRoute(accountID domain.AccountID, product exchange.Product, settlement string) (account.Resource, error) {
	var matched account.Resource
	found := false
	for _, resource := range handler.accounts.Accounts() {
		if resource.AccountID != accountID {
			continue
		}
		if product != "" && resource.Product != product {
			continue
		}
		if settlement != "" && resource.Settlement != settlement {
			continue
		}
		if found {
			return account.Resource{}, account.ErrProductMismatch
		}
		matched, found = resource, true
	}
	if !found {
		return account.Resource{}, account.ErrUnknownAccount
	}
	return matched, nil
}

func parseFundingPaymentWindow(r *http.Request) (time.Time, time.Time, error) {
	start, err := parseOptionalRFC3339(r.URL.Query().Get("start"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := parseOptionalRFC3339(r.URL.Query().Get("end"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid funding payment window")
	}
	return start, end, nil
}

func parseOptionalRFC3339(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func emptyFundingPaymentPage(accountID domain.AccountID, instrumentID domain.InstrumentID, settlementAsset string, start, end time.Time, status exchange.FundingPaymentStatus) fundingPaymentPageResource {
	return fundingPaymentPageResource{
		Items:               []fundingPaymentResource{},
		Scope:               fundingPaymentScope(accountID, instrumentID, settlementAsset, start, end),
		AccountID:           accountID,
		InstrumentID:        instrumentID,
		SettlementAsset:     settlementAsset,
		PaginationTraversal: fundingPaymentTraversal(exchange.PageInfo{WindowStart: start, WindowEnd: end, HasMoreKnown: true}),
		Start:               start,
		End:                 end,
		Status:              status,
		AsOf:                time.Now().UTC(),
		Page:                exchange.PageInfo{WindowStart: start, WindowEnd: end, HasMoreKnown: true},
		Watermark:           exchange.FundingPaymentWatermark{ObservedAt: time.Now().UTC(), Verified: false},
	}
}

func fundingPaymentErrorPage(accountID domain.AccountID, instrumentID domain.InstrumentID, settlementAsset string, start, end time.Time, err error) fundingPaymentPageResource {
	status := exchange.FundingPaymentsTemporarilyUnavailable
	if errors.Is(err, exchange.ErrUnsupported) {
		status = exchange.FundingPaymentsUnsupported
	}
	return emptyFundingPaymentPage(accountID, instrumentID, settlementAsset, start, end, status)
}

func projectFundingPaymentPage(
	accountID domain.AccountID,
	instrumentID domain.InstrumentID,
	settlementAsset string,
	resource account.Resource,
	requested *exchange.Instrument,
	instruments map[string]fundingPaymentInstrumentRef,
	start time.Time,
	end time.Time,
	page exchange.FundingPaymentPage,
) (fundingPaymentPageResource, error) {
	status := page.Status
	if status == "" {
		status = exchange.FundingPaymentsIncomplete
	}
	if err := validateFundingPaymentStatusProof(status, page, start, end); err != nil {
		return fundingPaymentPageResource{}, err
	}
	if page.Page.Cursor != "" || (page.Page.HasMoreKnown && page.Page.HasMore) {
		status = exchange.FundingPaymentsIncomplete
	}
	items := make([]fundingPaymentResource, 0, len(page.Items))
	for _, item := range page.Items {
		instrument, id, err := fundingPaymentInstrument(requested, instrumentID, instruments, item)
		if err != nil {
			return fundingPaymentPageResource{}, err
		}
		if settlementAsset != "" && item.SettlementAsset != settlementAsset {
			return fundingPaymentPageResource{}, exchange.ErrMalformedResponse
		}
		if !fundingPaymentEffectiveAtInScope(item.EffectiveAt, start, end) {
			return fundingPaymentPageResource{}, exchange.ErrMalformedResponse
		}
		factID := item.FactID
		if factID == "" {
			factID = httpFundingPaymentFactID(accountID, id, item.SourceRef, item.PaymentID, item.Amount.String(), item.EffectiveAt)
		}
		items = append(items, fundingPaymentResource{
			PaymentID: item.PaymentID, FactID: factID, SupersedesFactID: item.SupersedesFactID,
			AccountID: accountID, InstrumentID: id, Venue: resource.Venue, VenueEnvironment: resource.Environment,
			Product: resource.Product, SettlementAsset: item.SettlementAsset, Amount: item.Amount.String(),
			EffectiveAt: item.EffectiveAt, ObservedAt: item.ObservedAt, Revision: item.Revision,
			Version: item.Version, SourceRef: item.SourceRef, FormulaVersion: item.FormulaVersion,
		})
		_ = instrument
	}
	return fundingPaymentPageResource{
		Items: items, NextCursor: page.Page.Cursor, Scope: fundingPaymentScope(accountID, instrumentID, settlementAsset, start, end),
		AccountID: accountID, InstrumentID: instrumentID,
		SettlementAsset: settlementAsset, PaginationTraversal: fundingPaymentTraversal(page.Page),
		Start: start, End: end, Page: page.Page, Watermark: page.Watermark, Status: status,
		AsOf: time.Now().UTC(), CompleteThrough: page.CompleteThrough, Revision: page.Revision,
	}, nil
}

func validateFundingPaymentStatusProof(status exchange.FundingPaymentStatus, page exchange.FundingPaymentPage, start, end time.Time) error {
	if !page.Page.WindowStart.Equal(start) || !page.Page.WindowEnd.Equal(end) {
		return exchange.ErrMalformedResponse
	}
	if status != exchange.FundingPaymentsComplete && status != exchange.FundingPaymentsCompleteEmpty {
		if page.Watermark.Verified {
			return exchange.ErrMalformedResponse
		}
		return nil
	}
	if status == exchange.FundingPaymentsCompleteEmpty && len(page.Items) > 0 {
		return exchange.ErrMalformedResponse
	}
	if page.Page.Cursor != "" || !page.Page.HasMoreKnown || page.Page.HasMore {
		return exchange.ErrMalformedResponse
	}
	if !page.Watermark.Verified ||
		page.Watermark.ObservedAt.IsZero() || page.Watermark.ObservedAt.Before(end) ||
		page.Watermark.EffectiveThrough.IsZero() || page.Watermark.EffectiveThrough.Before(end) ||
		page.Watermark.CompleteThrough.IsZero() || page.Watermark.CompleteThrough.Before(end) ||
		page.CompleteThrough.IsZero() || page.CompleteThrough.Before(end) {
		return exchange.ErrMalformedResponse
	}
	if page.Revision <= 0 || page.Watermark.Revision <= 0 {
		return exchange.ErrMalformedResponse
	}
	if page.Watermark.Revision != page.Revision {
		return exchange.ErrMalformedResponse
	}
	return nil
}

func fundingPaymentEffectiveAtInScope(effectiveAt time.Time, start, end time.Time) bool {
	return !effectiveAt.IsZero() &&
		(start.IsZero() || !effectiveAt.Before(start)) &&
		(end.IsZero() || effectiveAt.Before(end))
}

func fundingPaymentScope(accountID domain.AccountID, instrumentID domain.InstrumentID, settlementAsset string, start, end time.Time) fundingPaymentScopeResource {
	return fundingPaymentScopeResource{
		AccountID: accountID, InstrumentID: instrumentID, AllInstruments: instrumentID == "",
		SettlementAsset: settlementAsset, Start: start, End: end,
	}
}

func fundingPaymentTraversal(page exchange.PageInfo) fundingPaymentTraversalResource {
	return fundingPaymentTraversalResource{
		Cursor: page.Cursor, Limit: page.Limit, HasMoreKnown: page.HasMoreKnown, HasMore: page.HasMore,
	}
}

type fundingPaymentInstrumentRef struct {
	id         domain.InstrumentID
	instrument exchange.Instrument
}

func fundingPaymentInstrument(requested *exchange.Instrument, instrumentID domain.InstrumentID, instruments map[string]fundingPaymentInstrumentRef, item exchange.FundingPayment) (exchange.Instrument, domain.InstrumentID, error) {
	return resolveNativeInstrument(requested, instrumentID, instruments, item.NativeSymbol, item.NativeID)
}

func resolveNativeInstrument(requested *exchange.Instrument, instrumentID domain.InstrumentID, instruments map[string]fundingPaymentInstrumentRef, nativeSymbol, nativeID string) (exchange.Instrument, domain.InstrumentID, error) {
	if requested == nil {
		ref, ok := instruments[nativeInstrumentKey(nativeSymbol, nativeID)]
		if !ok {
			return exchange.Instrument{}, "", exchange.ErrMalformedResponse
		}
		return ref.instrument, ref.id, nil
	}
	if nativeSymbol != "" && nativeSymbol != requested.Identity.NativeSymbol {
		return exchange.Instrument{}, "", exchange.ErrMalformedResponse
	}
	if nativeID != "" && nativeID != requested.Identity.NativeID {
		return exchange.Instrument{}, "", exchange.ErrMalformedResponse
	}
	return *requested, instrumentID, nil
}

func (handler *Handler) fundingPaymentInstrumentMap(ctx context.Context, accountID domain.AccountID, requested *exchange.Instrument) (map[string]fundingPaymentInstrumentRef, error) {
	result := make(map[string]fundingPaymentInstrumentRef)
	if requested != nil {
		result[nativeInstrumentKey(requested.Identity.NativeSymbol, requested.Identity.NativeID)] = fundingPaymentInstrumentRef{instrument: *requested}
		return result, nil
	}
	snapshot, err := handler.accounts.Instruments(accountID)
	if err != nil {
		return nil, err
	}
	for _, item := range snapshot.Instruments {
		resolved, resolveErr := handler.accounts.Resolve(ctx, accountID, item.ID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		key := nativeInstrumentKey(resolved.Identity.NativeSymbol, resolved.Identity.NativeID)
		if _, exists := result[key]; exists {
			return nil, exchange.ErrMalformedResponse
		}
		ref := fundingPaymentInstrumentRef{id: item.ID, instrument: resolved}
		result[key] = ref
		if resolved.Identity.NativeID != "" {
			symbolOnlyKey := nativeInstrumentKey(resolved.Identity.NativeSymbol, "")
			if existing, exists := result[symbolOnlyKey]; exists && existing.id != item.ID {
				return nil, exchange.ErrMalformedResponse
			}
			result[symbolOnlyKey] = ref
		}
	}
	return result, nil
}

// positionInstrumentMap resolves only the native identities needed by the
// venue's non-zero position response. Account catalogs can contain inactive or
// temporarily unresolved instruments; those unrelated rows must not make a
// complete position snapshot fail. A missing or ambiguous identity that the
// venue actually returned remains a malformed response.
func (handler *Handler) positionInstrumentMap(ctx context.Context, resource account.Resource, positions []exchange.Position) (map[string]fundingPaymentInstrumentRef, error) {
	result := make(map[string]fundingPaymentInstrumentRef)
	if len(positions) == 0 {
		return result, nil
	}
	snapshot, err := handler.accounts.Instruments(resource.AccountID)
	if err != nil {
		return nil, err
	}
	ambiguous := make(map[string]struct{})
	add := func(key string, ref fundingPaymentInstrumentRef) {
		if _, invalid := ambiguous[key]; invalid {
			return
		}
		if existing, exists := result[key]; exists && existing.id != ref.id {
			delete(result, key)
			ambiguous[key] = struct{}{}
			return
		}
		result[key] = ref
	}
	for _, item := range snapshot.Instruments {
		// Native symbols are only unique inside a venue route. Binance spot and
		// perpetual BTC-USDT, for example, are both BTCUSDT; mixing them here
		// would make a valid perpetual position appear ambiguous.
		if item.Product != resource.Product || (resource.Settlement != "" && item.Settlement != resource.Settlement) {
			continue
		}
		resolved, resolveErr := handler.accounts.Resolve(ctx, resource.AccountID, item.ID)
		if resolveErr != nil {
			continue
		}
		ref := fundingPaymentInstrumentRef{id: item.ID, instrument: resolved}
		add(nativeInstrumentKey(resolved.Identity.NativeSymbol, resolved.Identity.NativeID), ref)
		if resolved.Identity.NativeID != "" {
			add(nativeInstrumentKey(resolved.Identity.NativeSymbol, ""), ref)
		}
	}
	for _, position := range positions {
		if _, ok := result[nativeInstrumentKey(position.NativeSymbol, position.NativeID)]; !ok {
			return nil, exchange.ErrMalformedResponse
		}
	}
	return result, nil
}

func nativeInstrumentKey(symbol, id string) string {
	return symbol + "\x00" + id
}

func httpFundingPaymentFactID(parts ...any) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%v\n", part)
	}
	return "funding-payment:" + hex.EncodeToString(hash.Sum(nil))[:32]
}

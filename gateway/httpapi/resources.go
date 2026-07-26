package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

type fillResource struct {
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id,omitempty"`
	Fill         exchange.Fill       `json:"fill"`
}

type balanceResource struct {
	AccountID domain.AccountID `json:"account_id"`
	Balance   exchange.Balance `json:"balance"`
}

type positionResource struct {
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id,omitempty"`
	Position     exchange.Position   `json:"position"`
}

type fundingResource struct {
	AccountID    domain.AccountID     `json:"account_id"`
	InstrumentID domain.InstrumentID  `json:"instrument_id"`
	FundingRate  exchange.FundingRate `json:"funding_rate"`
}

type leverageBody struct {
	Leverage int `json:"leverage"`
}

func (handler *Handler) listFills(w http.ResponseWriter, r *http.Request) {
	selection, err := handler.resourceSelection(r, true)
	if err != nil {
		handler.invalid(w)
		return
	}
	items := make([]fillResource, 0)
	var nextCursor string
	for _, resource := range selection.accounts {
		if len(items) == selection.limit {
			break
		}
		instrument, err := handler.optionalResolvedInstrument(r.Context(), resource.AccountID, selection.instrumentID)
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
			client, clientErr := handler.accounts.Spot(resource.AccountID)
			if clientErr != nil {
				handler.fail(w, clientErr)
				return
			}
			page, err = client.Fills(r.Context(), request)
		case exchange.ProductPerp:
			client, clientErr := handler.accounts.Perp(resource.AccountID)
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
			items = append(items, fillResource{
				AccountID: resource.AccountID, InstrumentID: selection.instrumentID, Fill: fill,
			})
			if len(items) == selection.limit {
				break
			}
		}
		if page.Page.Cursor != "" {
			nextCursor = page.Page.Cursor
		}
	}
	writeJSON(w, http.StatusOK, collection[fillResource]{Items: items, NextCursor: nextCursor})
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
		switch resource.Product {
		case exchange.ProductSpot:
			client, clientErr := handler.accounts.Spot(resource.AccountID)
			if clientErr != nil {
				handler.fail(w, clientErr)
				return
			}
			balances, err = client.Balances(r.Context())
		case exchange.ProductPerp:
			client, clientErr := handler.accounts.Perp(resource.AccountID)
			if clientErr != nil {
				handler.fail(w, clientErr)
				return
			}
			balances, err = client.Balances(r.Context())
		default:
			err = account.ErrProductMismatch
		}
		if err != nil {
			handler.fail(w, err)
			return
		}
		for _, balance := range balances {
			items = append(items, balanceResource{AccountID: resource.AccountID, Balance: balance})
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
	for _, resource := range selection.accounts {
		if resource.Product == exchange.ProductSpot {
			if selection.accountID != "" {
				handler.fail(w, account.ErrProductMismatch)
				return
			}
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
		client, err := handler.accounts.Perp(resource.AccountID)
		if err != nil {
			handler.fail(w, err)
			return
		}
		positions, err := client.Positions(r.Context(), exchange.PositionsRequest{Instrument: instrument})
		if err != nil {
			handler.fail(w, err)
			return
		}
		for _, position := range positions {
			items = append(items, positionResource{
				AccountID: resource.AccountID, InstrumentID: selection.instrumentID, Position: position,
			})
			if len(items) == selection.limit {
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, collection[positionResource]{Items: items})
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
	row, err := handler.accounts.Capability(accountID)
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
	client, err := handler.accounts.Perp(accountID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	rate, err := client.FundingRate(r.Context(), exchange.FundingRateRequest{Instrument: instrument})
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fundingResource{
		AccountID: accountID, InstrumentID: instrumentID, FundingRate: rate,
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
	if _, err := handler.accounts.Capability(accountID); err != nil {
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
		found := false
		for _, resource := range resources {
			if resource.AccountID == accountID {
				resources = []account.Resource{resource}
				found = true
				break
			}
		}
		if !found {
			return resourceSelection{}, account.ErrUnknownAccount
		}
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

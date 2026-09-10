package httpapi

import (
	"encoding/json"
	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Use the same catalog rows consumed by the independent Go client.
type catalogSnapshot = wire.CatalogInstruments
type catalogInstrumentIdentity = wire.CatalogInstrument

func (handler *InternalHandler) marketOverview(w http.ResponseWriter, r *http.Request) {
	if !queryExactly(r.URL.Query(), "instrumentId") {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	instrument, ok := handler.resolveCatalogInstrument(w, r, "MARKET_INSTRUMENT_UNKNOWN")
	if !ok {
		return
	}
	query := url.Values{
		"venue": {instrument.Venue}, "environment": {instrument.Environment},
		"product": {instrument.Product}, "instrumentId": {instrument.InstrumentID},
	}
	response, ok := handler.executeAuthority(w, r, AuthorityRequest{Operation: AuthorityMarketData, Query: query})
	if ok {
		writeJSON(w, http.StatusOK, map[string]any{"data": response.Body})
	}
}

func (handler *InternalHandler) instrumentCandles(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !queryWithOptional(query, []string{"instrumentId", "interval"}, "limit") {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	limit := strings.TrimSpace(query.Get("limit"))
	if limit != "" {
		parsed, err := strconv.Atoi(limit)
		if err != nil || parsed < 1 || parsed > maxListLimit {
			handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
			return
		}
	}
	instrument, ok := handler.resolveCatalogInstrument(w, r, "MARKET_INSTRUMENT_UNKNOWN")
	if !ok {
		return
	}
	authorityQuery := url.Values{
		"venue": {instrument.Venue}, "environment": {instrument.Environment},
		"product": {instrument.Product}, "instrumentId": {instrument.InstrumentID},
		"interval": {query.Get("interval")},
	}
	if limit != "" {
		authorityQuery.Set("limit", limit)
	}
	response, ok := handler.executeAuthority(w, r, AuthorityRequest{Operation: AuthorityCandles, Query: authorityQuery})
	if ok {
		writeJSON(w, http.StatusOK, map[string]any{"data": response.Body})
	}
}

func (handler *InternalHandler) resolveCatalogInstrument(w http.ResponseWriter, r *http.Request, notFoundCode string) (catalogInstrumentIdentity, bool) {
	catalog, ok := handler.readCatalog(w, r)
	if !ok {
		return catalogInstrumentIdentity{}, false
	}
	want := strings.TrimSpace(r.URL.Query().Get("instrumentId"))
	for _, instrument := range catalog.Instruments {
		if instrument.InstrumentID == "" || instrument.Venue == "" || instrument.Environment != "live" || (instrument.Product != "spot" && instrument.Product != "perp") {
			handler.writeError(w, r, http.StatusBadGateway, "CATALOG_RESPONSE_INVALID", true)
			return catalogInstrumentIdentity{}, false
		}
		if instrument.InstrumentID == want && instrument.Ready {
			return instrument, true
		}
	}
	handler.writeError(w, r, http.StatusNotFound, notFoundCode, false)
	return catalogInstrumentIdentity{}, false
}

func (handler *InternalHandler) readCatalog(w http.ResponseWriter, r *http.Request) (catalogSnapshot, bool) {
	response, ok := handler.executeAuthority(w, r, AuthorityRequest{Operation: AuthorityCatalogInstruments})
	if !ok {
		return catalogSnapshot{}, false
	}
	encoded, err := json.Marshal(response.Body)
	if err != nil {
		handler.writeError(w, r, http.StatusBadGateway, "CATALOG_RESPONSE_INVALID", true)
		return catalogSnapshot{}, false
	}
	var snapshot catalogSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil || strings.TrimSpace(snapshot.Revision) == "" || snapshot.ObservedAt.IsZero() || snapshot.Instruments == nil {
		handler.writeError(w, r, http.StatusBadGateway, "CATALOG_RESPONSE_INVALID", true)
		return catalogSnapshot{}, false
	}
	return snapshot, true
}

func queryExactly(query url.Values, required ...string) bool {
	if len(query) != len(required) {
		return false
	}
	for _, field := range required {
		values, ok := query[field]
		if !ok || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return false
		}
	}
	return true
}

func queryWithOptional(query url.Values, required []string, optional string) bool {
	if len(query) < len(required) || len(query) > len(required)+1 {
		return false
	}
	allowed := make(map[string]struct{}, len(required)+1)
	for _, field := range required {
		allowed[field] = struct{}{}
	}
	allowed[optional] = struct{}{}
	for field, values := range query {
		if _, ok := allowed[field]; !ok || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return false
		}
	}
	for _, field := range required {
		if strings.TrimSpace(query.Get(field)) == "" {
			return false
		}
	}
	return true
}

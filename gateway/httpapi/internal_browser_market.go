package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// browserCatalogSnapshot is the narrow part of the catalog the browser
// adapter needs. Instruments stay as raw JSON so the Gateway returns the
// authority's canonical rows unchanged while still resolving route coordinates
// from instrumentId at this boundary.
type browserCatalogSnapshot struct {
	Revision    string            `json:"revision"`
	ObservedAt  time.Time         `json:"observedAt"`
	Instruments []json.RawMessage `json:"instruments"`
}

type browserInstrumentIdentity struct {
	InstrumentID string `json:"instrumentId"`
	Venue        string `json:"venue"`
	Environment  string `json:"environment"`
	Product      string `json:"product"`
	Ready        bool   `json:"ready"`
}

func (handler *InternalHandler) browserCatalogVenues(w http.ResponseWriter, r *http.Request) {
	if !browserQueryExactly(r.URL.Query()) {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	catalog, ok := handler.browserCatalog(w, r)
	if !ok {
		return
	}
	response, ok := handler.executeAuthority(w, r, AuthorityRequest{Operation: AuthorityCatalogVenues})
	if !ok {
		return
	}
	handler.writeBrowserProjection(w, response.Body, catalog)
}

func (handler *InternalHandler) browserCatalogInstruments(w http.ResponseWriter, r *http.Request) {
	if !browserQueryExactly(r.URL.Query()) {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	catalog, ok := handler.browserCatalog(w, r)
	if !ok {
		return
	}
	handler.writeBrowserProjection(w, catalog.Instruments, catalog)
}

func (handler *InternalHandler) writeBrowserProjection(w http.ResponseWriter, data any, catalog browserCatalogSnapshot) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data":       data,
		"projection": map[string]any{"revision": catalog.Revision, "observedAt": catalog.ObservedAt},
	})
}

func (handler *InternalHandler) browserMarketData(w http.ResponseWriter, r *http.Request) {
	if !browserQueryExactly(r.URL.Query(), "instrumentId") {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	instrument, ok := handler.resolveBrowserInstrument(w, r, "MARKET_INSTRUMENT_UNKNOWN")
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

func (handler *InternalHandler) browserCandles(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !browserQueryWithOptional(query, []string{"instrumentId", "interval"}, "limit") {
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
	instrument, ok := handler.resolveBrowserInstrument(w, r, "MARKET_INSTRUMENT_UNKNOWN")
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

func (handler *InternalHandler) browserFundingHistory(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !browserQueryExactly(query, "instrumentId", "window") {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	var span time.Duration
	switch query.Get("window") {
	case "7d":
		span = 7 * 24 * time.Hour
	case "30d":
		span = 30 * 24 * time.Hour
	default:
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	until := handler.now().UTC().Truncate(time.Second)
	release := handler.trackFundingHistory(r.Context(), query.Get("instrumentId"))
	defer release()
	authorityQuery := url.Values{
		"instrumentId": {query.Get("instrumentId")},
		"since":        {until.Add(-span).Format(time.RFC3339)},
		"until":        {until.Format(time.RFC3339)},
	}
	response, ok := handler.executeAuthority(w, r, AuthorityRequest{Operation: AuthorityFundingHistory, Query: authorityQuery})
	if ok {
		writeJSON(w, http.StatusOK, map[string]any{"data": response.Body})
	}
}

func (handler *InternalHandler) resolveBrowserInstrument(w http.ResponseWriter, r *http.Request, notFoundCode string) (browserInstrumentIdentity, bool) {
	catalog, ok := handler.browserCatalog(w, r)
	if !ok {
		return browserInstrumentIdentity{}, false
	}
	want := strings.TrimSpace(r.URL.Query().Get("instrumentId"))
	for _, raw := range catalog.Instruments {
		var instrument browserInstrumentIdentity
		if err := json.Unmarshal(raw, &instrument); err != nil {
			handler.writeError(w, r, http.StatusBadGateway, "CATALOG_RESPONSE_INVALID", true)
			return browserInstrumentIdentity{}, false
		}
		if instrument.InstrumentID == want && instrument.Ready {
			return instrument, true
		}
	}
	handler.writeError(w, r, http.StatusNotFound, notFoundCode, false)
	return browserInstrumentIdentity{}, false
}

func (handler *InternalHandler) browserCatalog(w http.ResponseWriter, r *http.Request) (browserCatalogSnapshot, bool) {
	response, ok := handler.executeAuthority(w, r, AuthorityRequest{Operation: AuthorityCatalogInstruments})
	if !ok {
		return browserCatalogSnapshot{}, false
	}
	encoded, err := json.Marshal(response.Body)
	if err != nil {
		handler.writeError(w, r, http.StatusBadGateway, "CATALOG_RESPONSE_INVALID", true)
		return browserCatalogSnapshot{}, false
	}
	var snapshot browserCatalogSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil || strings.TrimSpace(snapshot.Revision) == "" || snapshot.ObservedAt.IsZero() || snapshot.Instruments == nil {
		handler.writeError(w, r, http.StatusBadGateway, "CATALOG_RESPONSE_INVALID", true)
		return browserCatalogSnapshot{}, false
	}
	return snapshot, true
}

func browserQueryExactly(query url.Values, required ...string) bool {
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

func browserQueryWithOptional(query url.Values, required []string, optional string) bool {
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

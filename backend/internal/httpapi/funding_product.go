package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
)

type FundingOpportunity struct {
	OpportunityID     string `json:"opportunityId"`
	CanonicalMarketID string `json:"canonicalMarketId"`
	LongVenue         string `json:"longVenue,omitempty"`
	ShortVenue        string `json:"shortVenue,omitempty"`
	LongFundingRate   string `json:"longFundingRate,omitempty"`
	ShortFundingRate  string `json:"shortFundingRate,omitempty"`
}

type FundingPreviewRequest struct {
	OpportunityID         string `json:"opportunityId"`
	LongAccountID         string `json:"longAccountId"`
	ShortAccountID        string `json:"shortAccountId"`
	CanonicalMarketID     string `json:"canonicalMarketId"`
	NotionalPerLeg        string `json:"notionalPerLeg"`
	MaxSlippageBps        int    `json:"maxSlippageBps"`
	HoldThroughSettlement bool   `json:"holdThroughSettlement"`
	Mode                  string `json:"mode"`
}

type FundingExecutionPreview struct {
	PreviewID       string           `json:"previewId"`
	OpportunityID   string           `json:"opportunityId"`
	CanSubmit       bool             `json:"canSubmit"`
	Blockers        []string         `json:"blockers"`
	Warnings        []string         `json:"warnings"`
	Mode            string           `json:"mode"`
	ExpiresAt       time.Time        `json:"expiresAt"`
	CatalogRevision string           `json:"catalogRevision"`
	FactsRevision   string           `json:"factsRevision"`
	Plan            calculation.Plan `json:"plan"`
	PrincipalID     string           `json:"-"`
	Facts           []work.Fact      `json:"-"`
	AccountFences   json.RawMessage  `json:"-"`
	WorkPayload     json.RawMessage  `json:"-"`
}

type FundingProjection interface {
	ListOpportunities(context.Context, string, string) ([]FundingOpportunity, error)
	GetOpportunity(context.Context, string, string, string) (FundingOpportunity, error)
	CreatePreview(context.Context, string, FundingPreviewRequest) (FundingExecutionPreview, error)
	GetPreview(context.Context, string, string) (FundingExecutionPreview, error)
	CreateCloseAuthority(context.Context, string, string, string) (FundingExecutionPreview, error)
}

type FailClosedFundingProjection struct{}

func NewFailClosedFundingProjection() FundingProjection { return FailClosedFundingProjection{} }
func (FailClosedFundingProjection) ListOpportunities(context.Context, string, string) ([]FundingOpportunity, error) {
	return []FundingOpportunity{}, nil
}
func (FailClosedFundingProjection) GetOpportunity(context.Context, string, string, string) (FundingOpportunity, error) {
	return FundingOpportunity{}, work.ErrDependencyUnavailable
}
func (FailClosedFundingProjection) CreatePreview(context.Context, string, FundingPreviewRequest) (FundingExecutionPreview, error) {
	return FundingExecutionPreview{}, work.ErrDependencyUnavailable
}
func (FailClosedFundingProjection) GetPreview(context.Context, string, string) (FundingExecutionPreview, error) {
	return FundingExecutionPreview{}, work.ErrDependencyUnavailable
}
func (FailClosedFundingProjection) CreateCloseAuthority(context.Context, string, string, string) (FundingExecutionPreview, error) {
	return FundingExecutionPreview{}, work.ErrDependencyUnavailable
}

type FundingRoutesOptions struct {
	Projection        FundingProjection
	Work              *work.Service
	Principal         func(*http.Request) (principalID, mode string, err error)
	AuthorizeMutation func(*http.Request) error
	Executions        work.FundingExecutionStore
}

type fundingProductHandler struct{ options FundingRoutesOptions }

func RegisterFundingRoutes(mux *http.ServeMux, options FundingRoutesOptions) error {
	if mux == nil || options.Projection == nil || options.Work == nil || options.Principal == nil || options.AuthorizeMutation == nil {
		return configErr("Funding route dependencies are incomplete")
	}
	h := &fundingProductHandler{options: options}
	mux.HandleFunc("GET /api/v1/products/funding/opportunities", h.listOpportunities)
	mux.HandleFunc("GET /api/v1/products/funding/opportunities/{opportunityId}", h.getOpportunity)
	mux.HandleFunc("POST /api/v1/products/funding/execution-previews", h.createPreview)
	mux.HandleFunc("POST /api/v1/products/funding/commands", h.createCommand)
	mux.HandleFunc("GET /api/v1/products/funding/commands/{commandId}", h.getCommand)
	mux.HandleFunc("GET /api/v1/products/funding/lineage/{lineageId}", h.getLineage)
	return nil
}

func (h *fundingProductHandler) listOpportunities(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := h.identity(w, r)
	if !ok {
		return
	}
	items, err := h.options.Projection.ListOpportunities(r.Context(), principal, mode)
	if err != nil {
		fundingError(w, http.StatusServiceUnavailable, "FUNDING_DATA_NOT_READY")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": items, "principalScope": principal}})
}

func (h *fundingProductHandler) getOpportunity(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := h.identity(w, r)
	if !ok {
		return
	}
	item, err := h.options.Projection.GetOpportunity(r.Context(), principal, mode, r.PathValue("opportunityId"))
	if err != nil {
		fundingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": item})
}

func (h *fundingProductHandler) createPreview(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := h.identity(w, r)
	if !ok || !h.authorize(w, r) {
		return
	}
	var request FundingPreviewRequest
	if !decodeTrading(w, r, &request) {
		return
	}
	if request.Mode != mode || request.OpportunityID == "" || request.LongAccountID == "" || request.ShortAccountID == "" || request.LongAccountID == request.ShortAccountID || request.CanonicalMarketID == "" || request.NotionalPerLeg == "" || request.MaxSlippageBps < 0 {
		fundingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	preview, err := h.options.Projection.CreatePreview(r.Context(), principal, request)
	if err != nil {
		fundingError(w, http.StatusBadRequest, "FUNDING_PREVIEW_REJECTED")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": preview})
}

type fundingCommandRequest struct {
	PreviewID      string `json:"previewId"`
	Action         string `json:"action"`
	LineageID      string `json:"lineageId"`
	IdempotencyKey string `json:"idempotencyKey"`
}

func (h *fundingProductHandler) createCommand(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := h.identity(w, r)
	if !ok || !h.authorize(w, r) {
		return
	}
	var request fundingCommandRequest
	if !decodeTrading(w, r, &request) {
		return
	}
	if request.PreviewID == "" || request.Action == "" || request.LineageID == "" || request.IdempotencyKey == "" {
		fundingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	kind := strings.TrimSpace(request.Action)
	if kind != "open" && kind != "close" {
		fundingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	var preview FundingExecutionPreview
	var err error
	if kind == "close" {
		preview, err = h.options.Projection.CreateCloseAuthority(r.Context(), principal, request.PreviewID, request.LineageID)
	} else {
		preview, err = h.options.Projection.GetPreview(r.Context(), principal, request.PreviewID)
	}
	if err != nil || !preview.CanSubmit || preview.Mode != mode || len(preview.Facts) == 0 {
		fundingError(w, http.StatusConflict, "FUNDING_PREVIEW_STALE")
		return
	}
	item, replay, err := h.options.Work.Enqueue(r.Context(), work.EnqueueRequest{PrincipalID: principal, IdempotencyKey: request.IdempotencyKey, WorkKind: kind, LineageID: request.LineageID, ProductID: "funding", Mode: mode, FactsCursor: preview.Facts[0].Cursor, Facts: preview.Facts, AccountFences: preview.AccountFences, Payload: preview.WorkPayload})
	if err != nil || item.ProductID != "funding" {
		status := http.StatusBadRequest
		code := "VALIDATION_ERROR"
		if errors.Is(err, work.ErrIdempotencyConflict) {
			status, code = http.StatusConflict, "IDEMPOTENCY_CONFLICT"
		}
		if errors.Is(err, work.ErrLiveWriteDisabled) {
			status, code = http.StatusForbidden, "LIVE_PRIVATE_WRITE_DISABLED"
		}
		fundingError(w, status, code)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"meta": meta(r), "data": publicFundingWorkItem(item), "replayed": replay})
}

func (h *fundingProductHandler) getCommand(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := h.identity(w, r)
	if !ok {
		return
	}
	item, err := h.options.Work.GetOwnedWork(r.Context(), principal, mode, r.PathValue("commandId"))
	if err != nil || item.ProductID != "funding" {
		fundingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": publicFundingWorkItem(item)})
}

func (h *fundingProductHandler) getLineage(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := h.identity(w, r)
	if !ok {
		return
	}
	items, err := h.options.Work.ListOwnedWork(r.Context(), principal, mode, r.PathValue("lineageId"))
	if err != nil {
		fundingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	fundingItems := items[:0]
	for _, item := range items {
		if item.ProductID == "funding" {
			fundingItems = append(fundingItems, item)
		}
	}
	items = fundingItems
	if len(items) == 0 {
		fundingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	publicItems := make([]work.WorkItem, len(items))
	for index, item := range items {
		publicItems[index] = publicFundingWorkItem(item)
	}
	data := map[string]any{"lineageId": r.PathValue("lineageId"), "workItems": publicItems}
	if h.options.Executions != nil {
		if execution, executionErr := h.options.Executions.GetFundingExecution(r.Context(), principal, orders.Mode(mode), r.PathValue("lineageId")); executionErr == nil {
			data["execution"] = execution
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": data})
}

func publicFundingWorkItem(item work.WorkItem) work.WorkItem {
	if item.Status == "open" {
		item.Status = "pending"
	}
	return item
}

func (h *fundingProductHandler) identity(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	principal, mode, err := h.options.Principal(r)
	if err != nil || principal == "" || (mode != "test" && mode != "live") {
		fundingError(w, http.StatusUnauthorized, "AUTH_REQUIRED")
		return "", "", false
	}
	return principal, mode, true
}
func (h *fundingProductHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if err := h.options.AuthorizeMutation(r); err != nil {
		fundingError(w, http.StatusForbidden, "AUTH_REQUIRED")
		return false
	}
	return true
}
func fundingError(w http.ResponseWriter, status int, code string) {
	writeError(w, status, code, "request rejected", nil, false, "")
}

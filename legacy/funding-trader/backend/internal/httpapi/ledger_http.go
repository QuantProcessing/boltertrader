package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

type ledgerFundingPaymentQuery struct {
	PrincipalID  string
	Mode         string
	AccountID    string
	InstrumentID string
	PositionID   string
	From         time.Time
	To           time.Time
	Cursor       string
	Limit        int
}

type ledgerAPI interface {
	FundingPayments(context.Context, ledgerFundingPaymentQuery) (fundingPaymentPageDTO, error)
	Portfolio(context.Context, readmodel.Scope, string) (portfolioLedgerDTO, bool, error)
	ClosedPositions(context.Context, readmodel.Scope, string) ([]closedPositionLedgerDTO, error)
	ClosedPosition(context.Context, readmodel.Scope, string) (closedPositionLedgerDTO, bool, error)
	Verification(context.Context, readmodel.Scope) (verificationStatusDTO, error)
}

type tradingReadinessAPI interface {
	CheckTrading(context.Context) readiness.TradingReadinessResult
}

func (s *Server) listFundingPayments(w http.ResponseWriter, r *http.Request) {
	if !s.requireLedger(w, r) {
		return
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	instrumentID := strings.TrimSpace(r.URL.Query().Get("instrumentId"))
	if accountID == "" || instrumentID == "" {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "accountId and instrumentId are required", false, map[string]any{"fields": []string{"accountId", "instrumentId"}})
		return
	}
	page, ok := s.parsePageQuery(w, r)
	if !ok {
		return
	}
	from, ok := s.parseOptionalTime(w, r, "from")
	if !ok {
		return
	}
	to, ok := s.parseOptionalTime(w, r, "to")
	if !ok {
		return
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "funding payment time range is invalid", false, map[string]any{"fields": []string{"from", "to"}})
		return
	}
	scope := s.readScope(r)
	result, err := s.cfg.Ledger.FundingPayments(r.Context(), ledgerFundingPaymentQuery{
		PrincipalID: scope.PrincipalID, Mode: scope.Mode,
		AccountID: accountID, InstrumentID: instrumentID,
		PositionID: strings.TrimSpace(r.URL.Query().Get("positionId")),
		From:       from, To: to, Cursor: page.Cursor, Limit: page.Limit,
	})
	if err != nil {
		s.writeLedgerError(w, r, "funding-payments", err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": result, "meta": meta(r)})
}

func (s *Server) portfolioLedger(w http.ResponseWriter, r *http.Request) {
	if !s.requireLedger(w, r) {
		return
	}
	asset := strings.TrimSpace(r.URL.Query().Get("reportingAsset"))
	if asset == "" {
		asset = "USDT"
	}
	item, ok, err := s.cfg.Ledger.Portfolio(r.Context(), s.readScope(r), asset)
	if err != nil {
		s.writeLedgerError(w, r, "portfolio", err)
		return
	}
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "ledger_not_found", "portfolio ledger is not available", false, map[string]any{"resource": "portfolio"})
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": item, "meta": meta(r)})
}

func (s *Server) verification(w http.ResponseWriter, r *http.Request) {
	if !s.requireLedger(w, r) {
		return
	}
	item, err := s.cfg.Ledger.Verification(r.Context(), s.readScope(r))
	if err != nil {
		s.writeLedgerError(w, r, "verification", err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": item, "meta": meta(r)})
}

func (s *Server) closedPositions(w http.ResponseWriter, r *http.Request) {
	if !s.requireLedger(w, r) {
		return
	}
	asset := strings.TrimSpace(r.URL.Query().Get("reportingAsset"))
	if asset == "" {
		asset = "USDT"
	}
	items, err := s.cfg.Ledger.ClosedPositions(r.Context(), s.readScope(r), asset)
	if err != nil {
		s.writeLedgerError(w, r, "closed-positions", err)
		return
	}
	if items == nil {
		items = []closedPositionLedgerDTO{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"data": items,
		"meta": map[string]any{"requestId": requestID(r), "count": len(items), "hasMore": false, "nextCursor": ""},
	})
}

func (s *Server) position(w http.ResponseWriter, r *http.Request) {
	positionID := strings.TrimSpace(r.PathValue("positionId"))
	if positionID == "" {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "position id is required", false, map[string]any{"field": "positionId"})
		return
	}
	if s.cfg.Ledger != nil {
		item, ok, err := s.cfg.Ledger.ClosedPosition(r.Context(), s.readScope(r), positionID)
		if err != nil {
			s.writeLedgerError(w, r, "position", err)
			return
		}
		if ok {
			s.writeJSON(w, r, http.StatusOK, map[string]any{"data": item, "meta": meta(r)})
			return
		}
	}
	reader, ok := s.cfg.ReadModel.(interface {
		Position(context.Context, readmodel.Scope, string) (json.RawMessage, error)
	})
	if ok {
		payload, err := reader.Position(r.Context(), s.readScope(r), positionID)
		if err == nil {
			s.writeJSON(w, r, http.StatusOK, map[string]any{"data": payload, "meta": meta(r)})
			return
		}
		if !errors.Is(err, readmodel.ErrNotFound) {
			s.writeReadModelError(w, r, "positions", err)
			return
		}
	}
	payload, found, err := s.visibleOpenPosition(r.Context(), s.readScope(r), positionID)
	if err != nil {
		s.writeReadModelError(w, r, "positions", err)
		return
	}
	if !found {
		s.writeError(w, r, http.StatusNotFound, "not_found", "position was not found", false, map[string]any{"resource": "positions"})
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": payload, "meta": meta(r)})
}

func (s *Server) visibleOpenPosition(ctx context.Context, scope readmodel.Scope, positionID string) (json.RawMessage, bool, error) {
	if s.cfg.ReadModel == nil {
		return nil, false, nil
	}
	cursor := ""
	for {
		page, err := s.cfg.ReadModel.ListPositions(ctx, scope, readmodel.PositionQuery{
			PageQuery: readmodel.PageQuery{Cursor: cursor, Limit: 100},
			States:    []string{"open", "attention"},
		})
		if err != nil {
			return nil, false, err
		}
		for _, item := range page.Items {
			if positionPayloadMatches(item, positionID) {
				return item, true, nil
			}
		}
		if strings.TrimSpace(page.NextCursor) == "" {
			return nil, false, nil
		}
		cursor = page.NextCursor
	}
}

func positionPayloadMatches(payload json.RawMessage, positionID string) bool {
	var identity struct {
		ID         string `json:"id"`
		PositionID string `json:"positionId"`
	}
	if err := json.Unmarshal(payload, &identity); err != nil {
		return false
	}
	positionID = strings.TrimSpace(positionID)
	return strings.TrimSpace(identity.PositionID) == positionID ||
		strings.TrimSpace(identity.ID) == positionID
}

func (s *Server) requireLedger(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.Ledger != nil {
		return true
	}
	s.writeError(w, r, http.StatusServiceUnavailable, "ledger_unavailable", "ledger service is unavailable", true, nil)
	return false
}

func (s *Server) writeLedgerError(w http.ResponseWriter, r *http.Request, resource string, err error) {
	if errors.Is(err, readmodel.ErrNotFound) {
		s.writeError(w, r, http.StatusNotFound, "ledger_not_found", "ledger item was not found", false, map[string]any{"resource": resource})
		return
	}
	if errors.Is(err, readmodel.ErrInvalidCursor) {
		s.writeError(w, r, http.StatusBadRequest, "invalid_cursor", "cursor is invalid for this request", false, map[string]any{"resource": resource})
		return
	}
	s.cfg.Logger.Error("ledger_query_failed", "resource", resource, "requestId", requestID(r), "errorType", errorTypeName(err))
	s.writeError(w, r, http.StatusServiceUnavailable, "ledger_unavailable", "ledger query failed", true, map[string]any{"resource": resource})
}

func errorTypeName(err error) string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", err)
}

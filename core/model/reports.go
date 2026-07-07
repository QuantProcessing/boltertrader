package model

import (
	"fmt"
	"time"
)

type ReportID string
type ReconciliationID string
type EventID string

type ReportWarning struct {
	Code    string
	Message string
}

type OrderStatusReportQuery struct {
	InstrumentID InstrumentID
	AccountID    string
	ClientID     string
	VenueOrderID string
	OpenOnly     bool
	Since        time.Time
	Until        time.Time
	Limit        int
}

type SingleOrderStatusQuery struct {
	InstrumentID InstrumentID
	AccountID    string
	ClientID     string
	VenueOrderID string
}

type FillReportQuery struct {
	InstrumentID InstrumentID
	AccountID    string
	ClientID     string
	VenueOrderID string
	Since        time.Time
	Until        time.Time
	Limit        int
}

type PositionReportQuery struct {
	InstrumentID InstrumentID
	AccountID    string
	Since        time.Time
	Until        time.Time
}

type MassStatusQuery struct {
	Venue            string
	AccountID        string
	ClientID         string
	Since            time.Time
	Until            time.Time
	Lookback         time.Duration
	IncludeFills     bool
	IncludePositions bool
}

type OrderStatusReport struct {
	ReportID   ReportID
	Venue      string
	AccountID  string
	Order      Order
	ReportedAt time.Time

	// OverfillAllowed records an explicit venue/adapter exception. Without it,
	// validation rejects terminal reports whose filled quantity exceeds the
	// requested quantity.
	OverfillAllowed bool
}

func OrderMatchesStatusQuery(o Order, query OrderStatusReportQuery) bool {
	if query.AccountID != "" && o.Request.AccountID != "" && o.Request.AccountID != query.AccountID {
		return false
	}
	if query.InstrumentID.Symbol != "" && o.Request.InstrumentID != query.InstrumentID {
		return false
	}
	if query.ClientID != "" && o.Request.ClientID != query.ClientID {
		return false
	}
	if query.VenueOrderID != "" && o.VenueOrderID != query.VenueOrderID {
		return false
	}
	return true
}

func FillMatchesReportQuery(fill Fill, query FillReportQuery) bool {
	if query.AccountID != "" && fill.AccountID != "" && fill.AccountID != query.AccountID {
		return false
	}
	if query.InstrumentID.Symbol != "" && fill.InstrumentID != query.InstrumentID {
		return false
	}
	if query.ClientID != "" && fill.ClientID != query.ClientID {
		return false
	}
	if query.VenueOrderID != "" && fill.VenueOrderID != query.VenueOrderID {
		return false
	}
	return true
}

func (r OrderStatusReport) Key() string {
	if r.Order.VenueOrderID != "" {
		return r.Order.VenueOrderID
	}
	return r.Order.Request.ClientID
}

func (r OrderStatusReport) Validate() error {
	if r.Venue == "" && r.Order.Request.InstrumentID.Venue == "" {
		return fmt.Errorf("order status report: venue required")
	}
	if r.Key() == "" {
		return fmt.Errorf("order status report: client or venue order id required")
	}
	qty := r.Order.Request.Quantity
	if !r.OverfillAllowed && !qty.IsZero() && r.Order.FilledQty.GreaterThan(qty) {
		return fmt.Errorf("order status report: filled quantity %s exceeds order quantity %s", r.Order.FilledQty, qty)
	}
	return nil
}

type FillReport struct {
	ReportID   ReportID
	Venue      string
	AccountID  string
	Fill       Fill
	ReportedAt time.Time
}

func (r FillReport) Key() string {
	if r.Fill.VenueOrderID != "" {
		return r.Fill.VenueOrderID
	}
	return r.Fill.ClientID
}

func (r FillReport) Validate() error {
	if r.Venue == "" && r.Fill.InstrumentID.Venue == "" {
		return fmt.Errorf("fill report: venue required")
	}
	if r.Key() == "" {
		return fmt.Errorf("fill report: client or venue order id required")
	}
	if !r.Fill.Quantity.IsPositive() {
		return fmt.Errorf("fill report: positive quantity required")
	}
	return nil
}

type PositionReport struct {
	ReportID   ReportID
	Venue      string
	AccountID  string
	Position   Position
	ReportedAt time.Time
}

func (r PositionReport) Key() string {
	return PositionReportKey(r.AccountID, r.Position)
}

func (r PositionReport) Validate() error {
	if r.Venue == "" && r.Position.InstrumentID.Venue == "" {
		return fmt.Errorf("position report: venue required")
	}
	if r.Position.InstrumentID.Symbol == "" {
		return fmt.Errorf("position report: instrument required")
	}
	return nil
}

func PositionReportKey(accountID string, p Position) string {
	return accountID + "|" + p.InstrumentID.String() + "|" + p.Side.String()
}

type ExecutionMassStatus struct {
	ReportID        ReportID
	Venue           string
	AccountID       string
	ClientID        string
	GeneratedAt     time.Time
	Lookback        time.Duration
	OrderReports    map[string]OrderStatusReport
	FillReports     map[string][]FillReport
	PositionReports map[string][]PositionReport
	Partial         bool
	Warnings        []ReportWarning
}

func NewExecutionMassStatus(venue, accountID string, generatedAt time.Time) *ExecutionMassStatus {
	return &ExecutionMassStatus{
		ReportID:        ReportID(fmt.Sprintf("%s:%s:%d", venue, accountID, generatedAt.UnixNano())),
		Venue:           venue,
		AccountID:       accountID,
		GeneratedAt:     generatedAt,
		OrderReports:    make(map[string]OrderStatusReport),
		FillReports:     make(map[string][]FillReport),
		PositionReports: make(map[string][]PositionReport),
	}
}

func (s *ExecutionMassStatus) AddOrderReport(r OrderStatusReport) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if s.OrderReports == nil {
		s.OrderReports = make(map[string]OrderStatusReport)
	}
	s.OrderReports[r.Key()] = r
	return nil
}

func (s *ExecutionMassStatus) AddFillReport(r FillReport) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if s.FillReports == nil {
		s.FillReports = make(map[string][]FillReport)
	}
	k := r.Key()
	s.FillReports[k] = append(s.FillReports[k], r)
	return nil
}

func (s *ExecutionMassStatus) AddPositionReport(r PositionReport) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if s.PositionReports == nil {
		s.PositionReports = make(map[string][]PositionReport)
	}
	k := r.Key()
	s.PositionReports[k] = append(s.PositionReports[k], r)
	return nil
}

func (s ExecutionMassStatus) Clone() ExecutionMassStatus {
	out := s
	out.OrderReports = make(map[string]OrderStatusReport, len(s.OrderReports))
	for k, v := range s.OrderReports {
		out.OrderReports[k] = v
	}
	out.FillReports = make(map[string][]FillReport, len(s.FillReports))
	for k, v := range s.FillReports {
		out.FillReports[k] = append([]FillReport(nil), v...)
	}
	out.PositionReports = make(map[string][]PositionReport, len(s.PositionReports))
	for k, v := range s.PositionReports {
		out.PositionReports[k] = append([]PositionReport(nil), v...)
	}
	out.Warnings = append([]ReportWarning(nil), s.Warnings...)
	return out
}

func (s ExecutionMassStatus) Validate() error {
	for k, r := range s.OrderReports {
		if err := r.Validate(); err != nil {
			return err
		}
		if r.Key() != k {
			return fmt.Errorf("mass status: order report key %q does not match report key %q", k, r.Key())
		}
	}
	for k, reports := range s.FillReports {
		for _, r := range reports {
			if err := r.Validate(); err != nil {
				return err
			}
			if r.Key() != k {
				return fmt.Errorf("mass status: fill report key %q does not match report key %q", k, r.Key())
			}
		}
	}
	for k, reports := range s.PositionReports {
		for _, r := range reports {
			if err := r.Validate(); err != nil {
				return err
			}
			if r.Key() != k {
				return fmt.Errorf("mass status: position report key %q does not match report key %q", k, r.Key())
			}
		}
	}
	return nil
}

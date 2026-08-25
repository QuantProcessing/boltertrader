// Package fundingsettlement persists only venue-confirmed funding settlements
// for instruments the product is actively using. It never writes predicted
// rates as realized history.
package fundingsettlement

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

type Point struct {
	FundingTime   time.Time
	Rate          decimal.Decimal
	IntervalHours int64
}

type History interface {
	Settlements(context.Context, string, time.Time, time.Time) ([]Point, error)
}

type Record struct {
	InstrumentID          string
	RouteProfileID        string
	ActiveReasons         []string
	TrackingStartedAt     time.Time
	TargetNextFundingTime time.Time
	State                 string
	RetryAt               time.Time
	RetryCount            int
	ErrorClass            string
	UpdatedAt             time.Time
}

type SettledRate struct {
	RouteProfileID string
	InstrumentID   string
	FundingTime    time.Time
	RealizedRate   decimal.Decimal
	ObservedAt     time.Time
	Source         string
}

type Store interface {
	LoadTracking(context.Context) ([]Record, error)
	SaveTracking(context.Context, Record) error
	DeleteTracking(context.Context, string) error
	UpsertRate(context.Context, SettledRate) error
}

type Options struct {
	Store    Store
	History  History
	Now      func() time.Time
	Interval time.Duration
}

type Tracker struct {
	store    Store
	history  History
	now      func() time.Time
	interval time.Duration
	mu       sync.Mutex
	records  map[string]Record
}

func New(options Options) (*Tracker, error) {
	if options.Store == nil || options.History == nil {
		return nil, fmt.Errorf("funding settlement dependencies are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Interval <= 0 {
		options.Interval = 10 * time.Second
	}
	return &Tracker{store: options.Store, history: options.History, now: options.Now, interval: options.Interval, records: make(map[string]Record)}, nil
}

func (tracker *Tracker) Restore(ctx context.Context) error {
	records, err := tracker.store.LoadTracking(ctx)
	if err != nil {
		return err
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for _, record := range records {
		if validRecord(record) {
			tracker.records[record.InstrumentID] = normalize(record)
		}
	}
	return nil
}

func (tracker *Tracker) Track(ctx context.Context, instrumentID, routeProfileID, reason string, nextFunding time.Time) error {
	if strings.TrimSpace(instrumentID) == "" || strings.TrimSpace(routeProfileID) == "" || strings.TrimSpace(reason) == "" || nextFunding.IsZero() {
		return fmt.Errorf("funding settlement tracking request is invalid")
	}
	now := tracker.now().UTC()
	tracker.mu.Lock()
	record := tracker.records[instrumentID]
	if record.TrackingStartedAt.IsZero() {
		record.TrackingStartedAt = now
	}
	record.InstrumentID, record.RouteProfileID = instrumentID, routeProfileID
	record.ActiveReasons = addReason(record.ActiveReasons, reason)
	record.TargetNextFundingTime = nextFunding.UTC()
	record.State, record.RetryAt, record.RetryCount, record.ErrorClass, record.UpdatedAt = "active", time.Time{}, 0, "", now
	tracker.records[instrumentID] = record
	tracker.mu.Unlock()
	return tracker.store.SaveTracking(ctx, record)
}

func (tracker *Tracker) Untrack(ctx context.Context, instrumentID, reason string) error {
	tracker.mu.Lock()
	record, ok := tracker.records[instrumentID]
	if !ok {
		tracker.mu.Unlock()
		return nil
	}
	record.ActiveReasons = removeReason(record.ActiveReasons, reason)
	record.UpdatedAt = tracker.now().UTC()
	if len(record.ActiveReasons) == 0 {
		record.State = "awaiting_settlement"
	}
	tracker.records[instrumentID] = record
	tracker.mu.Unlock()
	return tracker.store.SaveTracking(ctx, record)
}

func (tracker *Tracker) Run(ctx context.Context) error {
	if err := tracker.Restore(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(tracker.interval)
	defer ticker.Stop()
	for {
		if err := tracker.ProcessDue(ctx); err != nil && ctx.Err() == nil { /* retry on next tick */
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (tracker *Tracker) ProcessDue(ctx context.Context) error {
	now := tracker.now().UTC()
	tracker.mu.Lock()
	due := make([]Record, 0)
	for _, record := range tracker.records {
		if record.State == "gap" {
			continue
		}
		at := record.TargetNextFundingTime.Add(90 * time.Second)
		if !record.RetryAt.IsZero() {
			at = record.RetryAt
		}
		if !now.Before(at) {
			due = append(due, record)
		}
	}
	tracker.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].InstrumentID < due[j].InstrumentID })
	var result error
	for _, record := range due {
		result = errors.Join(result, tracker.process(ctx, now, record))
	}
	return result
}

func (tracker *Tracker) process(ctx context.Context, now time.Time, record Record) error {
	points, err := tracker.history.Settlements(ctx, record.InstrumentID, record.TargetNextFundingTime.Add(-time.Minute), now.Add(time.Minute))
	var settled *Point
	if err == nil {
		for index := range points {
			if !points[index].FundingTime.Before(record.TargetNextFundingTime.Add(-time.Minute)) {
				settled = &points[index]
				break
			}
		}
	}
	if settled == nil {
		return tracker.retry(ctx, now, record, err)
	}
	if err := tracker.store.UpsertRate(ctx, SettledRate{RouteProfileID: record.RouteProfileID, InstrumentID: record.InstrumentID, FundingTime: settled.FundingTime.UTC(), RealizedRate: settled.Rate, ObservedAt: now, Source: "venue_history"}); err != nil {
		return tracker.retry(ctx, now, record, err)
	}
	tracker.mu.Lock()
	current := tracker.records[record.InstrumentID]
	if len(current.ActiveReasons) == 0 {
		delete(tracker.records, record.InstrumentID)
		tracker.mu.Unlock()
		return tracker.store.DeleteTracking(ctx, record.InstrumentID)
	}
	hours := settled.IntervalHours
	if hours <= 0 {
		hours = 8
	}
	current.TargetNextFundingTime = settled.FundingTime.UTC().Add(time.Duration(hours) * time.Hour)
	current.State, current.RetryAt, current.RetryCount, current.ErrorClass, current.UpdatedAt = "active", time.Time{}, 0, "", now
	tracker.records[record.InstrumentID] = current
	tracker.mu.Unlock()
	return tracker.store.SaveTracking(ctx, current)
}

var retryDelays = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}

func (tracker *Tracker) retry(ctx context.Context, now time.Time, record Record, cause error) error {
	tracker.mu.Lock()
	current := tracker.records[record.InstrumentID]
	if now.Sub(current.TargetNextFundingTime.Add(90*time.Second)) >= 2*time.Hour {
		current.State, current.RetryAt, current.ErrorClass = "gap", time.Time{}, errorClass(cause)
	} else {
		index := current.RetryCount
		if index >= len(retryDelays) {
			index = len(retryDelays) - 1
		}
		current.State, current.RetryAt, current.RetryCount, current.ErrorClass = "retrying", now.Add(retryDelays[index]), current.RetryCount+1, errorClass(cause)
	}
	current.UpdatedAt = now
	tracker.records[record.InstrumentID] = current
	tracker.mu.Unlock()
	if err := tracker.store.SaveTracking(ctx, current); err != nil {
		return err
	}
	if cause == nil {
		return fmt.Errorf("settled funding point is not available")
	}
	return cause
}

func validRecord(record Record) bool {
	return record.InstrumentID != "" && record.RouteProfileID != "" && !record.TrackingStartedAt.IsZero() && !record.TargetNextFundingTime.IsZero()
}
func normalize(record Record) Record {
	record.ActiveReasons = addReason(nil, record.ActiveReasons...)
	return record
}
func addReason(current []string, reasons ...string) []string {
	set := map[string]bool{}
	for _, value := range current {
		if strings.TrimSpace(value) != "" {
			set[value] = true
		}
	}
	for _, value := range reasons {
		if strings.TrimSpace(value) != "" {
			set[value] = true
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
func removeReason(current []string, reason string) []string {
	result := make([]string, 0, len(current))
	for _, value := range current {
		if value != reason {
			result = append(result, value)
		}
	}
	return result
}
func errorClass(err error) string {
	if err == nil {
		return "not_found"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "upstream"
}

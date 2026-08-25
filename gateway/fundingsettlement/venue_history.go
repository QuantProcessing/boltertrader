package fundingsettlement

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
)

type VenueHistory struct {
	Board   *fundingboard.Source
	Markets *publicmarket.Source
}

func (source VenueHistory) Settlements(ctx context.Context, instrumentID string, since, until time.Time) ([]Point, error) {
	instrument, err := source.resolve(ctx, instrumentID)
	if err != nil {
		return nil, err
	}
	snapshot, err := source.Board.History(ctx, fundingboard.HistoryQuery{
		RouteProfileID: instrument.RouteProfileID,
		NativeSymbol:   instrument.Canonical.Identity.NativeSymbol,
		NativeID:       instrument.Canonical.Identity.NativeID,
		Since:          since, Until: until,
	})
	if err != nil {
		return nil, err
	}
	result := make([]Point, 0, len(snapshot.Points))
	for _, point := range snapshot.Points {
		hours := int64(0)
		if point.IntervalHours.Valid {
			hours = point.IntervalHours.Value
		}
		result = append(result, Point{FundingTime: point.FundingTime.UTC(), Rate: point.Rate, IntervalHours: hours})
	}
	return result, nil
}

// TrackNextSettlement adds a transient history-query reason, then removes it
// immediately. Tracker therefore retains the instrument through exactly the
// next settlement instead of polling it forever after a chart was closed.
func (source VenueHistory) TrackNextSettlement(ctx context.Context, tracker *Tracker, instrumentID string) error {
	if tracker == nil {
		return nil
	}
	instrument, next, err := source.trackingTarget(ctx, instrumentID)
	if err != nil {
		return err
	}
	if err := tracker.Track(ctx, instrumentID, instrument.RouteProfileID, "funding_history", next); err != nil {
		return err
	}
	return tracker.Untrack(ctx, instrumentID, "funding_history")
}

func (source VenueHistory) TrackActive(ctx context.Context, tracker *Tracker, instrumentID, reason string) (func(), error) {
	if tracker == nil {
		return func() {}, nil
	}
	instrument, next, err := source.trackingTarget(ctx, instrumentID)
	if err != nil {
		return nil, err
	}
	if err := tracker.Track(ctx, instrumentID, instrument.RouteProfileID, reason, next); err != nil {
		return nil, err
	}
	return func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tracker.Untrack(stopCtx, instrumentID, reason)
	}, nil
}

func (source VenueHistory) trackingTarget(ctx context.Context, instrumentID string) (publicmarket.Instrument, time.Time, error) {
	instrument, err := source.resolve(ctx, instrumentID)
	if err != nil {
		return publicmarket.Instrument{}, time.Time{}, err
	}
	board, err := source.Board.Snapshot(ctx)
	if err != nil {
		return publicmarket.Instrument{}, time.Time{}, err
	}
	next := time.Time{}
	identity := instrument.Canonical.Identity
	for _, venue := range board.Venues {
		if venue.RouteProfileID != instrument.RouteProfileID {
			continue
		}
		for _, entry := range venue.Entries {
			matched := identity.NativeID != "" && identity.NativeID == entry.NativeID
			if !matched {
				matched = strings.EqualFold(identity.NativeSymbol, entry.NativeSymbol)
			}
			if matched {
				next = entry.NextFundingTime.UTC()
				break
			}
		}
	}
	if next.IsZero() {
		return publicmarket.Instrument{}, time.Time{}, fmt.Errorf("next funding time is unavailable")
	}
	return instrument, next, nil
}

func (source VenueHistory) resolve(ctx context.Context, instrumentID string) (publicmarket.Instrument, error) {
	if source.Board == nil || source.Markets == nil {
		return publicmarket.Instrument{}, fmt.Errorf("funding history source is unavailable")
	}
	snapshot, err := source.Markets.Snapshot(ctx)
	if err != nil {
		return publicmarket.Instrument{}, err
	}
	instrument, ok := snapshot.Find(instrumentID)
	if !ok {
		return publicmarket.Instrument{}, fmt.Errorf("funding history instrument is unknown")
	}
	return instrument, nil
}

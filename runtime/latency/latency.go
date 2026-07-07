package latency

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
)

type Chain string

const (
	ChainMarket         Chain = "market"
	ChainExecution      Chain = "execution"
	ChainAccount        Chain = "account"
	ChainCommand        Chain = "command"
	ChainReconciliation Chain = "reconciliation"
)

type Recorder interface {
	RecordEventLatency(EventLatency)
	RecordCommandLatency(CommandLatency)
	Snapshot() Snapshot
	Drops() uint64
}

type EventLatency struct {
	Chain          Chain
	EventID        model.EventID
	Venue          string
	AccountID      string
	InstrumentID   model.InstrumentID
	ClientID       string
	VenueOrderID   string
	TradeID        string
	TsVenue        time.Time
	TsAdapterRecv  time.Time
	TsAdapterEmit  time.Time
	TsBusRecv      time.Time
	TsApplied      time.Time
	TsCallbackDone time.Time
	DurationNs     int64
}

func EventFromMeta(chain Chain, meta contract.EventMeta, applied, callbackDone time.Time) EventLatency {
	lat := EventLatency{
		Chain:          chain,
		EventID:        meta.EventID,
		Venue:          meta.Venue,
		AccountID:      meta.AccountID,
		InstrumentID:   meta.InstrumentID,
		ClientID:       meta.ClientID,
		VenueOrderID:   meta.VenueOrderID,
		TradeID:        meta.TradeID,
		TsVenue:        meta.TsVenue,
		TsAdapterRecv:  meta.TsAdapterRecv,
		TsAdapterEmit:  meta.TsAdapterEmit,
		TsBusRecv:      meta.TsBusRecv,
		TsApplied:      applied,
		TsCallbackDone: callbackDone,
	}
	lat.DurationNs = durationNs(firstNonZero(lat.TsVenue, lat.TsAdapterRecv, lat.TsAdapterEmit, lat.TsBusRecv, lat.TsApplied), callbackDone)
	return lat
}

func (l EventLatency) NonNegative() bool {
	return l.DurationNs >= 0 && monotonic(l.TsVenue, l.TsAdapterRecv, l.TsAdapterEmit, l.TsBusRecv, l.TsApplied, l.TsCallbackDone)
}

type CommandLatency struct {
	Command       string
	ClientID      string
	VenueOrderID  string
	StartedAt     time.Time
	RiskStart     time.Time
	RiskEnd       time.Time
	JournalAppend time.Time
	AdapterStart  time.Time
	AdapterEnd    time.Time
	CacheApplied  time.Time
	CompletedAt   time.Time
	Err           string
	DurationNs    int64
}

func (l CommandLatency) NonNegative() bool {
	return l.DurationNs >= 0 && monotonic(l.StartedAt, l.RiskStart, l.RiskEnd, l.JournalAppend, l.AdapterStart, l.AdapterEnd, l.CacheApplied, l.CompletedAt)
}

func (l *CommandLatency) Finish(at time.Time) {
	l.CompletedAt = at
	l.DurationNs = durationNs(l.StartedAt, at)
}

type Bucket struct {
	UpperBoundNs int64
	Count        uint64
}

type Snapshot struct {
	EventsTotal          uint64
	CommandsTotal        uint64
	ReconciliationsTotal uint64
	DroppedTotal         uint64
	EventBuckets         []Bucket
	CommandBuckets       []Bucket
	RecentEvents         []EventLatency
	RecentCommands       []CommandLatency
}

type RecorderImpl struct {
	mu       sync.RWMutex
	capacity int

	events   []EventLatency
	commands []CommandLatency

	eventBuckets   []atomic.Uint64
	commandBuckets []atomic.Uint64
	eventsTotal    atomic.Uint64
	commandsTotal  atomic.Uint64
	reconcileTotal atomic.Uint64
	drops          atomic.Uint64
}

var defaultBounds = []int64{
	int64(100 * time.Microsecond),
	int64(time.Millisecond),
	int64(10 * time.Millisecond),
	int64(100 * time.Millisecond),
	int64(time.Second),
}

func NewRecorder(capacity int) *RecorderImpl {
	if capacity <= 0 {
		capacity = 1024
	}
	return &RecorderImpl{
		capacity:       capacity,
		eventBuckets:   make([]atomic.Uint64, len(defaultBounds)+1),
		commandBuckets: make([]atomic.Uint64, len(defaultBounds)+1),
	}
}

func (r *RecorderImpl) RecordEventLatency(lat EventLatency) {
	if lat.DurationNs == 0 {
		lat.DurationNs = durationNs(firstNonZero(lat.TsVenue, lat.TsAdapterRecv, lat.TsAdapterEmit, lat.TsBusRecv, lat.TsApplied), lat.TsCallbackDone)
	}
	r.eventsTotal.Add(1)
	addBucket(r.eventBuckets, lat.DurationNs)
	if !r.mu.TryLock() {
		r.drops.Add(1)
		return
	}
	defer r.mu.Unlock()
	if len(r.events) >= r.capacity {
		r.drops.Add(1)
		return
	}
	r.events = append(r.events, lat)
}

func (r *RecorderImpl) RecordCommandLatency(lat CommandLatency) {
	if lat.DurationNs == 0 {
		lat.DurationNs = durationNs(lat.StartedAt, lat.CompletedAt)
	}
	r.commandsTotal.Add(1)
	if lat.Command == string(ChainReconciliation) {
		r.reconcileTotal.Add(1)
	}
	addBucket(r.commandBuckets, lat.DurationNs)
	if !r.mu.TryLock() {
		r.drops.Add(1)
		return
	}
	defer r.mu.Unlock()
	if len(r.commands) >= r.capacity {
		r.drops.Add(1)
		return
	}
	r.commands = append(r.commands, lat)
}

func (r *RecorderImpl) Snapshot() Snapshot {
	r.mu.RLock()
	events := append([]EventLatency(nil), r.events...)
	commands := append([]CommandLatency(nil), r.commands...)
	r.mu.RUnlock()
	return Snapshot{
		EventsTotal:          r.eventsTotal.Load(),
		CommandsTotal:        r.commandsTotal.Load(),
		ReconciliationsTotal: r.reconcileTotal.Load(),
		DroppedTotal:         r.drops.Load(),
		EventBuckets:         snapshotBuckets(r.eventBuckets),
		CommandBuckets:       snapshotBuckets(r.commandBuckets),
		RecentEvents:         events,
		RecentCommands:       commands,
	}
}

func (r *RecorderImpl) Drops() uint64 { return r.drops.Load() }

func addBucket(buckets []atomic.Uint64, ns int64) {
	if ns < 0 {
		ns = 0
	}
	for i, bound := range defaultBounds {
		if ns <= bound {
			buckets[i].Add(1)
			return
		}
	}
	buckets[len(buckets)-1].Add(1)
}

func snapshotBuckets(src []atomic.Uint64) []Bucket {
	out := make([]Bucket, len(src))
	for i := range src {
		upper := int64(-1)
		if i < len(defaultBounds) {
			upper = defaultBounds[i]
		}
		out[i] = Bucket{UpperBoundNs: upper, Count: src[i].Load()}
	}
	return out
}

func durationNs(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() {
		return 0
	}
	ns := end.Sub(start).Nanoseconds()
	if ns < 0 {
		return 0
	}
	return ns
}

func firstNonZero(times ...time.Time) time.Time {
	for _, ts := range times {
		if !ts.IsZero() {
			return ts
		}
	}
	return time.Time{}
}

func monotonic(times ...time.Time) bool {
	var prev time.Time
	for _, ts := range times {
		if ts.IsZero() {
			continue
		}
		if !prev.IsZero() && ts.Before(prev) {
			return false
		}
		prev = ts
	}
	return true
}

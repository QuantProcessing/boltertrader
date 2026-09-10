// Package monitor evaluates public Gateway quotes. It owns no venue connection,
// account, order, or external notification channel. All decisions use the caller's
// clock so gaps, delayed quotes, and restart behavior can be replayed offline.
package monitor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

const (
	Insufficient = "insufficient_data"
	Normal       = "normal"
	Triggered    = "triggered"
	Interrupted  = "interrupted"
	PriceBasis   = "bbo_midpoint"
)

// Config applies one rule to a finite set of instruments. Window is a maximum
// lookback; MinSamples counts distinct UTC seconds, not websocket messages.
type Config struct {
	Source        string
	Instruments   []string
	Window        time.Duration
	ChangePercent decimal.Decimal
	MinSamples    int
	MaxGap        time.Duration
	Cooldown      time.Duration
}

func (c Config) Validate() error {
	if c.Source == "" || len(c.Instruments) < 1 || len(c.Instruments) > 64 {
		return errors.New("monitor requires a Gateway source and 1..64 instruments")
	}
	seen := map[string]bool{}
	for _, id := range c.Instruments {
		if id == "" || id == "." || id == ".." || len(id) > 512 || !utf8.ValidString(id) || strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || seen[id] {
			return errors.New("monitor instrument identities must be nonempty and unique")
		}
		seen[id] = true
	}
	if c.Window < 2*time.Second || c.Window > time.Hour || c.Window%time.Second != 0 || c.MaxGap < time.Second || c.MaxGap > c.Window {
		return errors.New("window must be 2s..1h in whole seconds; max gap must be 1s..window")
	}
	if c.MinSamples < 2 || c.MinSamples > int(c.Window/time.Second)+1 {
		return errors.New("min samples must be 2..window seconds + 1")
	}
	if c.ChangePercent.Exponent() < -8 || c.ChangePercent.Exponent() > 2 || !c.ChangePercent.IsPositive() || c.ChangePercent.GreaterThan(decimal.NewFromInt(100)) {
		return errors.New("change percent must be >0 and <=100 with at most 8 decimal places")
	}
	if c.Cooldown < time.Second || c.Cooldown > 24*time.Hour {
		return errors.New("notification cooldown must be 1s..24h")
	}
	return nil
}

type Event struct {
	ID               string    `json:"id"`
	InstrumentID     string    `json:"instrument_id"`
	Kind             string    `json:"kind"`
	State            string    `json:"state"`
	Reason           string    `json:"reason"`
	At               time.Time `json:"at"`
	PriceBasis       string    `json:"price_basis"`
	ObservedAt       time.Time `json:"observed_at,omitzero"`
	ReferenceAt      time.Time `json:"reference_at,omitzero"`
	Price            string    `json:"price,omitempty"`
	ReferencePrice   string    `json:"reference_price,omitempty"`
	ChangePercent    string    `json:"change_percent,omitempty"`
	ThresholdPercent string    `json:"threshold_percent,omitempty"`
	Samples          int       `json:"samples"`
}

type sample struct {
	At    time.Time       `json:"at"`
	Price decimal.Decimal `json:"price"`
}
type instrumentState struct {
	State        string    `json:"state"`
	StartedAt    time.Time `json:"started_at"`
	LastObserved time.Time `json:"last_observed,omitzero"`
	LastAlert    time.Time `json:"last_alert,omitzero"`
	Samples      []sample  `json:"samples"`
}
type checkpoint struct {
	Version     int                         `json:"version"`
	ConfigID    string                      `json:"config_id"`
	InstanceID  string                      `json:"instance_id"`
	Serial      uint64                      `json:"serial"`
	SavedAt     time.Time                   `json:"saved_at"`
	Instruments map[string]*instrumentState `json:"instruments"`
	Pending     []Event                     `json:"pending"`
}
type engine struct {
	config Config
	checkpoint
	now time.Time
}

func newEngine(config Config, now time.Time) (*engine, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config.Instruments = slices.Clone(config.Instruments)
	slices.Sort(config.Instruments)
	encoded, _ := json.Marshal(config)
	digest := sha256.Sum256(encoded)
	var instance [16]byte
	if _, err := rand.Read(instance[:]); err != nil {
		return nil, err
	}
	e := &engine{config: config, now: now.UTC(), checkpoint: checkpoint{Version: 1, ConfigID: hex.EncodeToString(digest[:]), InstanceID: hex.EncodeToString(instance[:]), Instruments: map[string]*instrumentState{}}}
	for _, id := range config.Instruments {
		e.Instruments[id] = &instrumentState{State: Insufficient, StartedAt: now.UTC()}
		e.emit(id, "state", "starting", sample{}, decimal.Zero)
	}
	return e, nil
}

// advance also runs before every quote: a busy socket must not hide source-time
// gaps. Invalid, duplicate and stale quotes never refresh the freshness watermark.
func (e *engine) advance(now time.Time) error {
	if now.Before(e.now) {
		return errors.New("monitor clock moved backwards")
	}
	e.now = now.UTC()
	for _, id := range e.config.Instruments {
		s := e.Instruments[id]
		last := s.LastObserved
		if last.IsZero() {
			last = s.StartedAt
		}
		if now.Sub(last) > e.config.MaxGap {
			e.interrupt(id, "data_timeout")
		}
	}
	return nil
}

func (e *engine) interrupt(id, reason string) {
	s := e.Instruments[id]
	if s.State == Interrupted {
		return
	}
	s.State, s.Samples = Interrupted, nil
	e.emit(id, "interrupted", reason, sample{}, decimal.Zero)
}

func price(raw string) (decimal.Decimal, bool) {
	if len(raw) == 0 || len(raw) > 256 {
		return decimal.Zero, false
	}
	p, err := decimal.NewFromString(raw)
	return p, err == nil && p.Exponent() >= -100 && p.Exponent() <= 100 && p.IsPositive()
}

func (e *engine) observe(id string, observed time.Time, bidRaw, askRaw string) {
	s := e.Instruments[id]
	bid, bidOK := price(bidRaw)
	ask, askOK := price(askRaw)
	if !bidOK || !askOK || bid.GreaterThan(ask) || observed.IsZero() || observed.After(e.now) || e.now.Sub(observed) > e.config.MaxGap || !observed.After(s.LastObserved) {
		return
	}
	current := sample{At: observed.UTC(), Price: bid.Add(ask).Mul(decimal.RequireFromString("0.5"))}
	wasInterrupted := s.State == Interrupted
	s.LastObserved = current.At
	cutoff := observed.Add(-e.config.Window)
	first := 0
	for first < len(s.Samples) && s.Samples[first].At.Before(cutoff) {
		first++
	}
	s.Samples = s.Samples[first:]
	// Retain the FIRST quote of each second. Every quote is still evaluated, so
	// a crash within a busy second is not lost by downsampling or changing its anchor.
	if len(s.Samples) == 0 || s.Samples[len(s.Samples)-1].At.Unix() != current.At.Unix() {
		s.Samples = append(s.Samples, current)
	}
	if wasInterrupted {
		s.State = Insufficient
		e.emit(id, "recovered", "fresh_quote", current, decimal.Zero)
	}
	if len(s.Samples) < e.config.MinSamples {
		if s.State != Insufficient {
			s.State = Insufficient
			e.emit(id, "state", "too_few_samples", current, decimal.Zero)
		}
		return
	}
	anchor := s.Samples[0].Price
	difference := current.Price.Sub(anchor).Mul(decimal.NewFromInt(100))
	// Compare before division/rounding so a just-below-threshold quote cannot
	// trigger because the displayed percentage rounds up.
	triggered := difference.Abs().GreaterThanOrEqual(anchor.Mul(e.config.ChangePercent))
	change := difference.DivRound(anchor, 8)
	if triggered {
		s.State = Triggered
		if s.LastAlert.IsZero() || e.now.Sub(s.LastAlert) >= e.config.Cooldown {
			s.LastAlert = e.now
			e.emit(id, "price_change", "threshold_reached", current, change)
		}
	} else if s.State != Normal {
		s.State = Normal
		e.emit(id, "state", "within_threshold", current, change)
	}
}

func (e *engine) emit(id, kind, reason string, current sample, change decimal.Decimal) {
	s := e.Instruments[id]
	e.Serial++
	event := Event{ID: fmt.Sprintf("%s-%016x", e.InstanceID, e.Serial), InstrumentID: id, Kind: kind, State: s.State, Reason: reason, At: e.now, PriceBasis: PriceBasis, Samples: len(s.Samples)}
	if !current.At.IsZero() {
		event.ObservedAt, event.Price = current.At, current.Price.String()
		if len(s.Samples) >= e.config.MinSamples {
			event.ReferenceAt = s.Samples[0].At
			event.ReferencePrice = s.Samples[0].Price.String()
			event.ChangePercent, event.ThresholdPercent = change.String(), e.config.ChangePercent.String()
		}
	}
	e.Pending = append(e.Pending, event)
}

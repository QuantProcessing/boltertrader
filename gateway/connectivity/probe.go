// Package connectivity owns the one lightweight reachability probe per venue.
// It is intentionally independent from funding and market-feed freshness.
package connectivity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type State string

const (
	StateConnecting State = "connecting"
	StateOnline     State = "online"
	StateDelayed    State = "delayed"
	StateOffline    State = "offline"
)

type Snapshot struct {
	Venue               exchange.Venue `json:"venue"`
	State               State          `json:"state"`
	Latency             time.Duration  `json:"latency"`
	EWMALatency         time.Duration  `json:"ewmaLatency"`
	ConsecutiveFailures int            `json:"consecutiveFailures"`
	LastSuccessAt       time.Time      `json:"lastSuccessAt,omitzero"`
	LastErrorAt         time.Time      `json:"lastErrorAt,omitzero"`
}

type Observer interface {
	ObserveVenueProbe(Snapshot)
}

type Checker interface{ Check(context.Context) error }
type CheckFunc func(context.Context) error

func (check CheckFunc) Check(ctx context.Context) error { return check(ctx) }

type machine struct {
	snapshot          Snapshot
	recent            [3]bool
	recentN           int
	recentAt          int
	recoverySuccesses int
}

func newMachine(venue exchange.Venue) *machine {
	return &machine{snapshot: Snapshot{Venue: venue, State: StateConnecting}}
}

func (current *machine) observe(at time.Time, latency time.Duration, err error) Snapshot {
	failed := err != nil
	current.recent[current.recentAt] = failed
	current.recentAt = (current.recentAt + 1) % len(current.recent)
	if current.recentN < len(current.recent) {
		current.recentN++
	}
	if failed {
		current.snapshot.ConsecutiveFailures++
		current.snapshot.LastErrorAt = at.UTC()
		current.recoverySuccesses = 0
		switch {
		case current.snapshot.ConsecutiveFailures >= 3:
			current.snapshot.State = StateOffline
		case current.snapshot.LastSuccessAt.IsZero():
			current.snapshot.State = StateConnecting
		default:
			current.snapshot.State = StateDelayed
		}
		return current.snapshot
	}

	wasOffline := current.snapshot.State == StateOffline
	current.snapshot.Latency = latency
	if current.snapshot.EWMALatency == 0 {
		current.snapshot.EWMALatency = latency
	} else {
		current.snapshot.EWMALatency = time.Duration(0.2*float64(latency) + 0.8*float64(current.snapshot.EWMALatency))
	}
	current.snapshot.LastSuccessAt = at.UTC()
	current.snapshot.ConsecutiveFailures = 0
	if wasOffline || current.recoverySuccesses > 0 {
		current.recoverySuccesses++
	}
	if current.recoverySuccesses > 0 && current.recoverySuccesses < 3 {
		current.snapshot.State = StateDelayed
		return current.snapshot
	}
	if current.snapshot.EWMALatency >= time.Second || current.hasRecentFailure() {
		current.snapshot.State = StateDelayed
	} else {
		current.snapshot.State = StateOnline
		current.recoverySuccesses = 0
	}
	return current.snapshot
}

func (current *machine) hasRecentFailure() bool {
	for index := 0; index < current.recentN; index++ {
		if current.recent[index] {
			return true
		}
	}
	return false
}

type Options struct {
	Checkers map[exchange.Venue]Checker
	Interval time.Duration
	Timeout  time.Duration
	Now      func() time.Time
	Observer Observer
}

type Manager struct {
	checkers map[exchange.Venue]Checker
	interval time.Duration
	timeout  time.Duration
	now      func() time.Time
	observer Observer
	mu       sync.RWMutex
	machines map[exchange.Venue]*machine
}

func New(options Options) (*Manager, error) {
	if len(options.Checkers) == 0 {
		return nil, fmt.Errorf("venue connectivity checkers are required")
	}
	if options.Interval <= 0 {
		options.Interval = 10 * time.Second
	}
	if options.Timeout <= 0 {
		options.Timeout = 3 * time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	manager := &Manager{checkers: make(map[exchange.Venue]Checker, len(options.Checkers)), interval: options.Interval, timeout: options.Timeout, now: options.Now, observer: options.Observer, machines: make(map[exchange.Venue]*machine)}
	for venue, checker := range options.Checkers {
		if venue == "" || checker == nil {
			return nil, fmt.Errorf("venue connectivity checker is invalid")
		}
		manager.checkers[venue] = checker
		manager.machines[venue] = newMachine(venue)
	}
	return manager, nil
}

func (manager *Manager) Run(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return fmt.Errorf("venue connectivity manager is unavailable")
	}
	manager.checkAll(ctx)
	ticker := time.NewTicker(manager.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			manager.checkAll(ctx)
		}
	}
}

func (manager *Manager) checkAll(ctx context.Context) {
	var wait sync.WaitGroup
	for venue, checker := range manager.checkers {
		wait.Add(1)
		go func(venue exchange.Venue, checker Checker) {
			defer wait.Done()
			started := manager.now()
			probeCtx, cancel := context.WithTimeout(ctx, manager.timeout)
			err := checker.Check(probeCtx)
			cancel()
			finished := manager.now()
			manager.mu.Lock()
			snapshot := manager.machines[venue].observe(finished, finished.Sub(started), err)
			manager.mu.Unlock()
			if manager.observer != nil {
				manager.observer.ObserveVenueProbe(snapshot)
			}
		}(venue, checker)
	}
	wait.Wait()
}

func (manager *Manager) Snapshot() []Snapshot {
	if manager == nil {
		return nil
	}
	manager.mu.RLock()
	result := make([]Snapshot, 0, len(manager.machines))
	for _, current := range manager.machines {
		result = append(result, current.snapshot)
	}
	manager.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Venue < result[j].Venue })
	return result
}

type HTTPCheck struct {
	Client *http.Client
	Method string
	URL    string
	Body   string
}

func (check HTTPCheck) Check(ctx context.Context) error {
	method := check.Method
	if method == "" {
		method = http.MethodGet
	}
	request, err := http.NewRequestWithContext(ctx, method, check.URL, bytes.NewBufferString(check.Body))
	if err != nil {
		return err
	}
	if check.Body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	client := check.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("venue probe returned status %d", response.StatusCode)
	}
	return nil
}

func DefaultHTTPCheckers(client *http.Client) map[exchange.Venue]Checker {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	checks := map[exchange.Venue]HTTPCheck{
		exchange.VenueBinance:     {URL: "https://api.binance.com/api/v3/ping"},
		exchange.VenueAster:       {URL: "https://fapi.asterdex.com/fapi/v1/ping"},
		exchange.VenueBybit:       {URL: "https://api.bybit.com/v5/market/time"},
		exchange.VenueBitget:      {URL: "https://api.bitget.com/api/v2/public/time"},
		exchange.VenueOKX:         {URL: "https://www.okx.com/api/v5/public/time"},
		exchange.VenueGate:        {URL: "https://api.gateio.ws/api/v4/spot/time"},
		exchange.VenueHyperliquid: {Method: http.MethodPost, URL: "https://api.hyperliquid.xyz/info", Body: `{"type":"meta"}`},
		exchange.VenueLighter:     {URL: "https://mainnet.zklighter.elliot.ai/api/v1/exchangeStats"},
		exchange.VenueNado:        {URL: "https://gateway.prod.nado.xyz/v1/query?type=status"},
	}
	result := make(map[exchange.Venue]Checker, len(checks))
	for venue, check := range checks {
		check.Client = client
		result[venue] = check
	}
	return result
}

func ValidateState(value State) error {
	switch State(strings.TrimSpace(string(value))) {
	case StateConnecting, StateOnline, StateDelayed, StateOffline:
		return nil
	default:
		return errors.New("venue connectivity state is invalid")
	}
}

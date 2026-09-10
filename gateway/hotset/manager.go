// Package hotset owns the policy that turns product demand into bounded native
// per-instrument market subscriptions. Callers acquire reasoned leases; only
// this manager decides which instruments are active at the venue boundary.
package hotset

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/markethub"
)

type Reason string

const (
	ReasonPosition       Reason = "position"
	ReasonOpenOrder      Reason = "open_order"
	ReasonExecution      Reason = "execution"
	ReasonBrowser        Reason = "browser"
	ReasonFundingHistory Reason = "funding_history"
)

var priorities = map[Reason]int{
	ReasonPosition: 600, ReasonOpenOrder: 500, ReasonExecution: 400,
	ReasonBrowser: 300, ReasonFundingHistory: 100,
}

func (reason Reason) Validate() error {
	if _, ok := priorities[reason]; !ok {
		return fmt.Errorf("hotset reason is invalid")
	}
	return nil
}

func (reason Reason) Protected() bool {
	return reason == ReasonPosition || reason == ReasonOpenOrder || reason == ReasonExecution
}

type Key struct {
	Owner        markethub.OwnerKey
	InstrumentID string
}

func (key Key) Validate() error {
	if key.Owner.Venue == "" || key.Owner.Environment == "" || strings.TrimSpace(key.Owner.NativeFamily) == "" || strings.TrimSpace(key.InstrumentID) == "" {
		return fmt.Errorf("hotset key is invalid")
	}
	return nil
}

// Activator is the only component allowed to turn a logical hotset decision
// into native topics. SetActive must return quickly; adapters own connection
// establishment and reconnect in their Feed Owner.
type Activator interface {
	SetActive(Key, bool)
}

// Observer receives logical demand transitions. Unlike Activator, it observes
// every reason whether or not topic capacity selected the instrument. This is
// used by durable side effects such as settled-funding tracking, whose lifetime
// follows product usage rather than native websocket admission.
type Observer interface {
	ReasonChanged(Key, Reason, bool)
}

type Options struct {
	Activator Activator
	Capacity  func(markethub.OwnerKey) int
	Grace     time.Duration
}

type Manager struct {
	mu        sync.Mutex
	activator Activator
	observer  Observer
	capacity  func(markethub.OwnerKey) int
	grace     time.Duration
	entries   map[Key]*entry
	closed    bool
}

// SetObserver installs the logical-demand observer and replays all currently
// active reasons. Calls are made outside the manager lock so observers may do
// bounded I/O without stalling topic admission.
func (manager *Manager) SetObserver(observer Observer) {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	manager.observer = observer
	type transition struct {
		key    Key
		reason Reason
	}
	active := make([]transition, 0)
	if observer != nil {
		for key, current := range manager.entries {
			for reason, count := range current.reasons {
				if count > 0 {
					active = append(active, transition{key: key, reason: reason})
				}
			}
		}
	}
	manager.mu.Unlock()
	for _, item := range active {
		observer.ReasonChanged(item.key, item.reason, true)
	}
}

type entry struct {
	reasons        map[Reason]int
	active         bool
	lingerPriority int
	timer          *time.Timer
}

type Snapshot struct {
	Key       Key
	Reasons   map[Reason]int
	Active    bool
	Protected bool
	Priority  int
}

func New(options Options) (*Manager, error) {
	if options.Activator == nil || options.Capacity == nil {
		return nil, fmt.Errorf("hotset dependencies are required")
	}
	if options.Grace <= 0 {
		options.Grace = 30 * time.Second
	}
	return &Manager{activator: options.Activator, capacity: options.Capacity, grace: options.Grace, entries: make(map[Key]*entry)}, nil
}

type Lease struct {
	release func()
	done    chan struct{}
	once    sync.Once
}

func (lease *Lease) Release() {
	if lease != nil {
		lease.once.Do(func() {
			close(lease.done)
			lease.release()
		})
	}
}

func (manager *Manager) Acquire(ctx context.Context, key Key, reason Reason) (*Lease, error) {
	if ctx == nil || ctx.Err() != nil || key.Validate() != nil || reason.Validate() != nil {
		return nil, fmt.Errorf("hotset lease is invalid")
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil, fmt.Errorf("hotset is closed")
	}
	current := manager.entries[key]
	if current == nil {
		current = &entry{reasons: make(map[Reason]int)}
		manager.entries[key] = current
	}
	if current.timer != nil {
		current.timer.Stop()
		current.timer = nil
	}
	current.reasons[reason]++
	first := current.reasons[reason] == 1
	manager.reconcileLocked(key.Owner)
	observer := manager.observer
	manager.mu.Unlock()
	if first && observer != nil {
		observer.ReasonChanged(key, reason, true)
	}

	lease := &Lease{release: func() { manager.release(key, reason) }, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			lease.Release()
		case <-lease.done:
		}
	}()
	return lease, nil
}

func (manager *Manager) release(key Key, reason Reason) {
	manager.mu.Lock()
	current := manager.entries[key]
	if current == nil || current.reasons[reason] == 0 {
		manager.mu.Unlock()
		return
	}
	current.lingerPriority = max(current.lingerPriority, priorities[reason])
	current.reasons[reason]--
	if current.reasons[reason] == 0 {
		delete(current.reasons, reason)
	}
	if len(current.reasons) == 0 && current.timer == nil {
		current.timer = time.AfterFunc(manager.grace, func() { manager.expire(key) })
	}
	manager.reconcileLocked(key.Owner)
	observer := manager.observer
	inactive := current.reasons[reason] == 0
	manager.mu.Unlock()
	if inactive && observer != nil {
		observer.ReasonChanged(key, reason, false)
	}
}

func (manager *Manager) expire(key Key) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[key]
	if current == nil || len(current.reasons) != 0 {
		return
	}
	if current.active {
		current.active = false
		manager.activator.SetActive(key, false)
	}
	delete(manager.entries, key)
	manager.reconcileLocked(key.Owner)
}

func (manager *Manager) reconcileLocked(owner markethub.OwnerKey) {
	capacity := manager.capacity(owner)
	if capacity < 0 {
		capacity = 0
	}
	candidates := make([]Key, 0)
	protected := 0
	for key, current := range manager.entries {
		if key.Owner != owner {
			continue
		}
		candidates = append(candidates, key)
		if entryProtected(current) {
			protected++
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := manager.entries[candidates[i]], manager.entries[candidates[j]]
		leftProtected, rightProtected := entryProtected(left), entryProtected(right)
		if leftProtected != rightProtected {
			return leftProtected
		}
		if entryPriority(left) != entryPriority(right) {
			return entryPriority(left) > entryPriority(right)
		}
		return candidates[i].InstrumentID < candidates[j].InstrumentID
	})
	// Protected demand is never evicted. If it exceeds one connection's topic
	// capacity the owner must shard across additional physical clients.
	limit := max(capacity, protected)
	selected := make(map[Key]bool, limit)
	for index, key := range candidates {
		if index < limit {
			selected[key] = true
		}
	}
	for _, key := range candidates {
		current := manager.entries[key]
		want := selected[key]
		if current.active == want {
			continue
		}
		current.active = want
		manager.activator.SetActive(key, want)
	}
}

func entryPriority(current *entry) int {
	priority := current.lingerPriority
	for reason := range current.reasons {
		priority = max(priority, priorities[reason])
	}
	return priority
}

func entryProtected(current *entry) bool {
	for reason := range current.reasons {
		if reason.Protected() {
			return true
		}
	}
	return false
}

func (manager *Manager) Snapshot() []Snapshot {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := make([]Snapshot, 0, len(manager.entries))
	for key, current := range manager.entries {
		reasons := make(map[Reason]int, len(current.reasons))
		for reason, count := range current.reasons {
			reasons[reason] = count
		}
		result = append(result, Snapshot{Key: key, Reasons: reasons, Active: current.active, Protected: entryProtected(current), Priority: entryPriority(current)})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Key.Owner != result[j].Key.Owner {
			return fmt.Sprint(result[i].Key.Owner) < fmt.Sprint(result[j].Key.Owner)
		}
		return result[i].Key.InstrumentID < result[j].Key.InstrumentID
	})
	return result
}

func (manager *Manager) Close() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return
	}
	manager.closed = true
	type transition struct {
		key    Key
		reason Reason
	}
	inactive := make([]transition, 0)
	for key, current := range manager.entries {
		if current.timer != nil {
			current.timer.Stop()
		}
		if current.active {
			manager.activator.SetActive(key, false)
		}
		for reason, count := range current.reasons {
			if count > 0 {
				inactive = append(inactive, transition{key: key, reason: reason})
			}
		}
	}
	observer := manager.observer
	manager.entries = make(map[Key]*entry)
	manager.mu.Unlock()
	if observer != nil {
		for _, item := range inactive {
			observer.ReasonChanged(item.key, item.reason, false)
		}
	}
}

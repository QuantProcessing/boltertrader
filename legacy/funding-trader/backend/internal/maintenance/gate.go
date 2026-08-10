package maintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

const (
	defaultReason     = "operator_maintenance"
	retryAfterSeconds = "5"
)

type Gate struct {
	mu sync.Mutex

	enabled      bool
	reason       string
	nextID       uint64
	active       map[uint64]context.CancelFunc
	idle         chan struct{}
	entryActive  int
	totalDrained int
}

func NewGate() *Gate {
	idle := make(chan struct{})
	close(idle)
	return &Gate{
		active: make(map[uint64]context.CancelFunc),
		idle:   idle,
	}
}

func (gate *Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if isHealthPath(request.URL.Path) {
			next.ServeHTTP(w, request)
			return
		}

		gate.mu.Lock()
		if gate.enabled {
			reason := gate.reason
			gate.mu.Unlock()
			writeMaintenance(w, reason)
			return
		}
		if len(gate.active) == 0 {
			gate.idle = make(chan struct{})
		}
		gate.nextID++
		id := gate.nextID
		ctx, cancel := context.WithCancel(request.Context())
		gate.active[id] = cancel
		gate.mu.Unlock()

		defer func() {
			cancel()
			gate.mu.Lock()
			delete(gate.active, id)
			if len(gate.active) == 0 {
				close(gate.idle)
			}
			gate.mu.Unlock()
		}()
		next.ServeHTTP(w, request.WithContext(ctx))
	})
}

func (gate *Gate) Enter(reason string) int {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = defaultReason
	}

	gate.mu.Lock()
	gate.enabled = true
	gate.reason = reason
	gate.entryActive = len(gate.active)
	cancels := make([]context.CancelFunc, 0, len(gate.active))
	for _, cancel := range gate.active {
		cancels = append(cancels, cancel)
	}
	active := gate.entryActive
	gate.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	return active
}

func (gate *Gate) Drain(ctx context.Context) (int, error) {
	gate.mu.Lock()
	idle := gate.idle
	drained := gate.entryActive
	gate.mu.Unlock()

	select {
	case <-idle:
		gate.mu.Lock()
		gate.totalDrained += drained
		gate.mu.Unlock()
		return drained, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

type Snapshot struct {
	Enabled      bool
	Reason       string
	Active       int
	TotalDrained int
}

func (gate *Gate) Snapshot() Snapshot {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return Snapshot{
		Enabled:      gate.enabled,
		Reason:       gate.reason,
		Active:       len(gate.active),
		TotalDrained: gate.totalDrained,
	}
}

func isHealthPath(path string) bool {
	return path == "/health/live" || path == "/health/ready"
}

func writeMaintenance(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", retryAfterSeconds)
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":      "service_maintenance",
			"message":   "service is under operator maintenance",
			"retryable": true,
			"details": map[string]string{
				"reason": reason,
			},
		},
	})
}

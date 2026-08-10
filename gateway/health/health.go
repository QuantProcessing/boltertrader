package health

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
)

type Dependency interface {
	Healthy() bool
}

type Monitor struct {
	mu              sync.RWMutex
	postgresHealthy bool
	postgres        Dependency
	ownership       map[string]Dependency
	components      map[string]Component
}

type Liveness struct {
	Live bool `json:"live"`
}

type Readiness struct {
	Ready      bool                 `json:"ready"`
	Reasons    []string             `json:"reasons,omitempty"`
	Components map[string]Component `json:"components"`
}

type Component struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

func NewMonitor() *Monitor {
	return &Monitor{ownership: map[string]Dependency{}, components: map[string]Component{}}
}

func (monitor *Monitor) SetComponent(name string, ready bool, reason string) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.components[name] = Component{Ready: ready, Reason: reason}
}

func (monitor *Monitor) Component(name string) Component {
	monitor.mu.RLock()
	defer monitor.mu.RUnlock()
	return monitor.components[name]
}

func (monitor *Monitor) SetPostgresHealthy(healthy bool) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.postgresHealthy = healthy
	monitor.postgres = nil
}

func (monitor *Monitor) SetPostgresDependency(dependency Dependency) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.postgres = dependency
	monitor.postgresHealthy = dependency != nil
}

func (monitor *Monitor) SetOwnership(accountID string, dependency Dependency) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	if dependency == nil {
		delete(monitor.ownership, accountID)
		return
	}
	monitor.ownership[accountID] = dependency
}

func (monitor *Monitor) Liveness() Liveness {
	return Liveness{Live: true}
}

func (monitor *Monitor) Readiness() Readiness {
	monitor.mu.RLock()
	postgresHealthy := monitor.postgresHealthy
	postgres := monitor.postgres
	ownership := make([]Dependency, 0, len(monitor.ownership))
	for _, dependency := range monitor.ownership {
		ownership = append(ownership, dependency)
	}
	components := make(map[string]Component, len(monitor.components))
	for name, component := range monitor.components {
		components[name] = component
	}
	monitor.mu.RUnlock()

	reasons := make([]string, 0, 2)
	if postgres != nil {
		postgresHealthy = postgres.Healthy()
	}
	if !postgresHealthy {
		reasons = append(reasons, "postgres_unhealthy")
	}
	for _, dependency := range ownership {
		if !dependency.Healthy() {
			reasons = append(reasons, "ownership_unhealthy")
			break
		}
	}
	sort.Strings(reasons)
	return Readiness{Ready: len(reasons) == 0, Reasons: reasons, Components: components}
}

func NewHandler(monitor *Monitor) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health/live", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, monitor.Liveness())
	})
	mux.HandleFunc("GET /v1/health/ready", func(w http.ResponseWriter, r *http.Request) {
		ready := monitor.Readiness()
		status := http.StatusOK
		if !ready.Ready {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, ready)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

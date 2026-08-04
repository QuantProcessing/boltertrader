package system

import (
	"sort"
	"sync"
	"time"
)

var RequiredComponents = []string{"process", "database", "secretStore", "gateway", "publicData", "accounts", "fundingWorker"}

type Component struct {
	Ready             bool           `json:"ready"`
	Reason            string         `json:"reason"`
	RetryAfterSeconds int            `json:"retryAfterSeconds,omitempty"`
	Details           map[string]any `json:"details,omitempty"`
	ObservedAt        time.Time      `json:"observedAt,omitempty"`
}

type Snapshot struct {
	Ready      bool                 `json:"ready"`
	Components map[string]Component `json:"components"`
}

type Readiness struct {
	mu         sync.RWMutex
	components map[string]Component
}

func NewReadiness() *Readiness { return &Readiness{components: make(map[string]Component)} }

func (service *Readiness) Update(name string, component Component) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if component.Reason == "" {
		if component.Ready {
			component.Reason = "ready"
		} else {
			component.Reason = "not_ready"
		}
	}
	component.Details = cloneDetails(component.Details)
	service.components[name] = component
}

func (service *Readiness) Status() Snapshot { return service.snapshot(false) }

func (service *Readiness) Readiness() Snapshot { return service.snapshot(true) }

func (service *Readiness) snapshot(requireAll bool) Snapshot {
	service.mu.RLock()
	defer service.mu.RUnlock()
	components := make(map[string]Component, len(RequiredComponents))
	ready := true
	for _, name := range RequiredComponents {
		component, ok := service.components[name]
		if !ok {
			component = Component{Reason: "not_reported"}
		}
		component.Details = cloneDetails(component.Details)
		components[name] = component
		if requireAll && !component.Ready {
			ready = false
		}
	}
	if !requireAll {
		ready = components["process"].Ready
	}
	return Snapshot{Ready: ready, Components: components}
}

func (snapshot Snapshot) BlockingReasons() []string {
	reasons := make([]string, 0)
	for name, component := range snapshot.Components {
		if !component.Ready {
			reasons = append(reasons, name+":"+component.Reason)
		}
	}
	sort.Strings(reasons)
	return reasons
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	cloned := make(map[string]any, len(details))
	for key, value := range details {
		cloned[key] = value
	}
	return cloned
}

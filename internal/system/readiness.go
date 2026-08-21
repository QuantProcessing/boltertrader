package system

import (
	"sort"
	"sync"
	"time"
)

var RequiredComponents = []string{"process", "database", "gateway", "publicData", "accounts"}

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
	evaluators map[string]func() Component
}

func NewReadiness() *Readiness {
	return &Readiness{components: make(map[string]Component), evaluators: make(map[string]func() Component)}
}

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

func (service *Readiness) SetEvaluator(name string, evaluator func() Component) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if evaluator == nil {
		delete(service.evaluators, name)
		return
	}
	service.evaluators[name] = evaluator
}

func (service *Readiness) Status() Snapshot { return service.snapshot(false) }

func (service *Readiness) Readiness() Snapshot { return service.snapshot(true) }

func (service *Readiness) snapshot(requireAll bool) Snapshot {
	service.mu.RLock()
	stored := make(map[string]Component, len(service.components))
	for name, component := range service.components {
		stored[name] = component
	}
	evaluators := make(map[string]func() Component, len(service.evaluators))
	for name, evaluator := range service.evaluators {
		evaluators[name] = evaluator
	}
	service.mu.RUnlock()

	// Evaluators are independent component probes. Running them serially makes
	// the readiness endpoint's latency the sum of every downstream timeout — a
	// pair of healthy, two-second-bounded probes can therefore exceed the
	// caller's five-second health-check deadline during a cold start. Capture
	// one concurrent result per evaluator, then assemble the deterministic
	// snapshot below from those values.
	evaluated := make(map[string]Component, len(evaluators))
	var evaluatedMu sync.Mutex
	var evaluatedGroup sync.WaitGroup
	for name, evaluator := range evaluators {
		evaluatedGroup.Add(1)
		go func() {
			defer evaluatedGroup.Done()
			component := evaluator()
			evaluatedMu.Lock()
			evaluated[name] = component
			evaluatedMu.Unlock()
		}()
	}
	evaluatedGroup.Wait()

	components := make(map[string]Component, len(RequiredComponents))
	ready := true
	for _, name := range RequiredComponents {
		component, ok := stored[name]
		if evaluatedComponent, exists := evaluated[name]; exists {
			component, ok = evaluatedComponent, true
		}
		if !ok {
			component = Component{Reason: "not_reported"}
		}
		component.Details = cloneDetails(component.Details)
		components[name] = component
		if requireAll && !component.Ready {
			ready = false
		}
	}
	for name, component := range stored {
		if _, ok := components[name]; ok {
			continue
		}
		if evaluatedComponent, exists := evaluated[name]; exists {
			component = evaluatedComponent
		}
		component.Details = cloneDetails(component.Details)
		components[name] = component
	}
	for name, component := range evaluated {
		if _, ok := components[name]; ok {
			continue
		}
		component.Details = cloneDetails(component.Details)
		components[name] = component
	}
	if !requireAll {
		ready = components["process"].Ready
	}
	return Snapshot{Ready: ready, Components: components}
}

func (snapshot Snapshot) BlockingReasons() []string {
	reasons := make([]string, 0)
	for _, name := range RequiredComponents {
		component := snapshot.Components[name]
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

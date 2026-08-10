package readiness

import (
	"context"
	"sort"
)

var RequiredComponents = []string{"maintenance", "process", "database", "gateway", "publicData", "execution"}

var reportedComponents = []string{"maintenance", "process", "database", "gateway", "publicData", "accounts", "execution"}

type Component struct {
	Ready             bool              `json:"ready"`
	Reason            string            `json:"reason,omitempty"`
	Details           map[string]string `json:"details,omitempty"`
	RetryAfterSeconds int               `json:"retryAfterSeconds,omitempty"`
}

type Result struct {
	Ready      bool                 `json:"ready"`
	Components map[string]Component `json:"components"`
}

func (r Result) ComponentNames() []string {
	names := make([]string, 0, len(r.Components))
	for name := range r.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type Checker interface {
	Check(context.Context) Result
}

type ComponentCheck func(context.Context) Component

func WithComponents(base Checker, checks map[string]ComponentCheck) Checker {
	copied := make(map[string]ComponentCheck, len(checks))
	for key, value := range checks {
		copied[key] = value
	}
	return componentChecker{base: base, checks: copied}
}

type staticChecker map[string]Component

func StaticChecks(components map[string]Component) Checker {
	copied := make(map[string]Component, len(components))
	for key, value := range components {
		copied[key] = value
	}
	return staticChecker(copied)
}

func (s staticChecker) Check(context.Context) Result {
	components := make(map[string]Component, len(reportedComponents))
	ready := true
	for _, name := range reportedComponents {
		component, ok := s[name]
		if !ok {
			component = Component{Ready: false, Reason: "not-configured"}
		}
		components[name] = component
		if requiredComponent(name) && !component.Ready {
			ready = false
		}
	}
	return Result{Ready: ready, Components: components}
}

type componentChecker struct {
	base   Checker
	checks map[string]ComponentCheck
}

func (c componentChecker) Check(ctx context.Context) Result {
	var result Result
	if c.base != nil {
		result = c.base.Check(ctx)
	} else {
		result = StaticChecks(nil).Check(ctx)
	}
	if result.Components == nil {
		result.Components = make(map[string]Component, len(RequiredComponents))
	}
	for name, check := range c.checks {
		component := Component{Ready: false, Reason: "not-configured"}
		if check != nil {
			component = check(ctx)
		}
		result.Components[name] = component
	}
	result.Ready = true
	for _, name := range RequiredComponents {
		component, ok := result.Components[name]
		if !ok {
			component = Component{Ready: false, Reason: "not-configured"}
			result.Components[name] = component
		}
		if !component.Ready {
			result.Ready = false
		}
	}
	return result
}

func requiredComponent(name string) bool {
	for _, required := range RequiredComponents {
		if name == required {
			return true
		}
	}
	return false
}

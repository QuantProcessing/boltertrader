package system

import "testing"

func TestReadinessSeparatesProcessStatusFromDependencies(t *testing.T) {
	service := NewReadiness()
	service.Update("process", Component{Ready: true, Reason: "running"})
	service.Update("database", Component{Ready: false, Reason: "database_unavailable", RetryAfterSeconds: 2})

	status := service.Status()
	if !status.Components["process"].Ready {
		t.Fatalf("Status() = %#v", status)
	}
	ready := service.Readiness()
	if ready.Ready || ready.Components["database"].Reason != "database_unavailable" {
		t.Fatalf("Readiness() = %#v", ready)
	}
}

func TestReadinessRequiresFrozenComponents(t *testing.T) {
	service := NewReadiness()
	for _, name := range RequiredComponents {
		service.Update(name, Component{Ready: true, Reason: "ready"})
	}
	if snapshot := service.Readiness(); !snapshot.Ready {
		t.Fatalf("Readiness() = %#v", snapshot)
	}
}

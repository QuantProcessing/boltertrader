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

func TestReadinessEvaluatorIsRecomputedForEverySnapshot(t *testing.T) {
	service := NewReadiness()
	ready := true
	service.SetEvaluator("fundingWorker", func() Component {
		return Component{Ready: ready, Reason: "worker_not_ready"}
	})
	if component := service.Status().Components["fundingWorker"]; !component.Ready {
		t.Fatalf("initial evaluated component=%+v", component)
	}
	ready = false
	if component := service.Status().Components["fundingWorker"]; component.Ready {
		t.Fatalf("latched evaluated component=%+v", component)
	}
}

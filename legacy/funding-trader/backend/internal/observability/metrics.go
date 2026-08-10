package observability

import "context"

import "github.com/QuantProcessing/funding-trader/backend/internal/readmodel"

// Snapshot contains principal-scoped operational gauges. Field names are kept
// separate from storage state names so the public metric contract remains
// stable if persistence details change.
type Snapshot struct {
	PartiallyHedged            int64
	Compensating               int64
	Uncertain                  int64
	ManualInterventionRequired int64
	ResnapshotRequired         int64
}

type Reader interface {
	Snapshot(context.Context, readmodel.Scope) (Snapshot, error)
}

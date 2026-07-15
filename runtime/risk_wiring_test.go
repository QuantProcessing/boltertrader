package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	runtimeexec "github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
)

type optionalCapabilitySetterRisk struct {
	setCalls atomic.Int32
}

func (r *optionalCapabilitySetterRisk) CheckSubmission(context.Context, model.OrderRequest, *model.Instrument) (func(), error) {
	return func() {}, nil
}

func (r *optionalCapabilitySetterRisk) SetRuntimeCapabilities(*contract.Capabilities, *contract.Capabilities) {
	r.setCalls.Add(1)
}

func TestWithRiskDoesNotDiscoverOptionalCapabilitySetter(t *testing.T) {
	node := runtime.NewNode(runtime.Clients{Execution: runtimetest.NewFakeExec()}, nil, "risk-wiring")
	risk := &optionalCapabilitySetterRisk{}

	runtime.WithRisk(risk, nil)(node)

	if got := risk.setCalls.Load(); got != 0 {
		t.Fatalf("optional capability setter calls=%d, want 0; concrete risk configuration belongs to the caller", got)
	}
}

func checkSubmissionRisk(e runtimeexec.SubmissionRiskChecker, req model.OrderRequest, inst *model.Instrument) error {
	release, err := e.CheckSubmission(context.Background(), req, inst)
	if release != nil {
		release()
	}
	return err
}

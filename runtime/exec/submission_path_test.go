package exec_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	bt_exec "github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/latency"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
)

type orderedSubmissionExec struct {
	*runtimetest.FakeExec
	calls         *[]string
	validationErr error
}

func (c *orderedSubmissionExec) ValidateSubmit(model.OrderRequest) error {
	*c.calls = append(*c.calls, "validate")
	return c.validationErr
}

func (c *orderedSubmissionExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	*c.calls = append(*c.calls, "submit")
	return c.FakeExec.Submit(ctx, req)
}

type orderedSubmissionRisk struct {
	calls           *[]string
	checks          atomic.Int32
	releases        atomic.Int32
	wantContext     context.Context
	wantRequest     model.OrderRequest
	wantInstrument  *model.Instrument
	observedContext context.Context
	observedRequest model.OrderRequest
	observedInst    *model.Instrument
}

func (r *orderedSubmissionRisk) CheckSubmission(ctx context.Context, req model.OrderRequest, inst *model.Instrument) (func(), error) {
	*r.calls = append(*r.calls, "risk")
	r.checks.Add(1)
	r.observedContext = ctx
	r.observedRequest = req
	r.observedInst = inst
	return func() { r.releases.Add(1) }, nil
}

type optionalInstrumentSetterRisk struct {
	setCalls atomic.Int32
}

func (r *optionalInstrumentSetterRisk) CheckSubmission(context.Context, model.OrderRequest, *model.Instrument) (func(), error) {
	return func() {}, nil
}

func (r *optionalInstrumentSetterRisk) SetInstrumentProvider(model.InstrumentProvider) {
	r.setCalls.Add(1)
}

func TestWithRiskDoesNotDiscoverOptionalInstrumentSetter(t *testing.T) {
	engine, _, _ := testEngine(runtimetest.NewFakeExec())
	risk := &optionalInstrumentSetterRisk{}

	engine.WithRisk(risk, singleInstrumentProvider{})

	if got := risk.setCalls.Load(); got != 0 {
		t.Fatalf("optional instrument setter calls=%d, want 0; concrete risk configuration belongs to the caller", got)
	}
}

type singleInstrumentProvider struct{ instrument *model.Instrument }

func (p singleInstrumentProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	if p.instrument == nil || p.instrument.ID != id {
		return nil, false
	}
	return p.instrument, true
}

func (p singleInstrumentProvider) All() []*model.Instrument {
	if p.instrument == nil {
		return nil
	}
	return []*model.Instrument{p.instrument}
}

func TestSubmitWithRiskCallsCheckSubmissionDirectlyOnce(t *testing.T) {
	calls := make([]string, 0, 3)
	client := &orderedSubmissionExec{FakeExec: runtimetest.NewFakeExec(), calls: &calls}
	req := testReq("generic-submit-path")
	req.AccountID = "test"
	inst := &model.Instrument{ID: req.InstrumentID}
	ctx := context.WithValue(context.Background(), struct{}{}, "submission-context")
	risk := &orderedSubmissionRisk{calls: &calls, wantContext: ctx, wantRequest: req, wantInstrument: inst}
	engine, _, _ := testEngine(client)
	engine.WithRisk(risk, singleInstrumentProvider{instrument: inst})

	if _, err := engine.Submit(ctx, req); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if want := []string{"validate", "risk", "submit"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order=%v, want %v", calls, want)
	}
	if got := risk.releases.Load(); got != 1 {
		t.Fatalf("risk reservation releases=%d, want 1", got)
	}
	if got := risk.checks.Load(); got != 1 {
		t.Fatalf("risk checks=%d, want 1", got)
	}
	if risk.observedContext != risk.wantContext || risk.observedRequest != risk.wantRequest || risk.observedInst != risk.wantInstrument {
		t.Fatalf("risk observed ctx/req/inst=(%v,%+v,%p), want (%v,%+v,%p)", risk.observedContext, risk.observedRequest, risk.observedInst, risk.wantContext, risk.wantRequest, risk.wantInstrument)
	}
}

func TestSubmitWithoutRiskSkipsRiskAndSubmitsOnce(t *testing.T) {
	calls := make([]string, 0, 2)
	client := &orderedSubmissionExec{FakeExec: runtimetest.NewFakeExec(), calls: &calls}
	engine, _, _ := testEngine(client)

	if _, err := engine.Submit(context.Background(), testReq("no-risk-submit")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if want := []string{"validate", "submit"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order=%v, want %v", calls, want)
	}
}

func TestSubmitValidationRejectsBeforeConfiguredRisk(t *testing.T) {
	fail := errors.New("local validation failed")
	calls := make([]string, 0, 1)
	client := &orderedSubmissionExec{
		FakeExec:      runtimetest.NewFakeExec(),
		calls:         &calls,
		validationErr: fail,
	}
	risk := &orderedSubmissionRisk{calls: &calls}
	engine, _, journal := testEngine(client)
	engine.WithRisk(risk, nil)

	if _, err := engine.Submit(context.Background(), testReq("validation-first")); !errors.Is(err, fail) {
		t.Fatalf("submit err=%v, want %v", err, fail)
	}
	if want := []string{"validate"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order=%v, want %v", calls, want)
	}
	if got := len(journal.Records()); got != 0 {
		t.Fatalf("journal records=%d, want 0", got)
	}
}

type submissionProbeExec struct {
	*runtimetest.FakeExec
	trace         *[]string
	validationErr error
	capabilities  *contract.Capabilities
	validateCalls atomic.Int32
	submitCalls   atomic.Int32
	lastValidated model.OrderRequest
	lastSubmitted model.OrderRequest
}

func (c *submissionProbeExec) Capabilities() contract.Capabilities {
	if c.trace != nil {
		*c.trace = append(*c.trace, "capabilities")
	}
	if c.capabilities != nil {
		return *c.capabilities
	}
	return c.FakeExec.Capabilities()
}

func (c *submissionProbeExec) ValidateSubmit(req model.OrderRequest) error {
	c.validateCalls.Add(1)
	c.lastValidated = req
	if c.trace != nil {
		*c.trace = append(*c.trace, "validate")
	}
	return c.validationErr
}

func (c *submissionProbeExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	c.submitCalls.Add(1)
	c.lastSubmitted = req
	if c.trace != nil {
		*c.trace = append(*c.trace, "submit")
	}
	return c.FakeExec.Submit(ctx, req)
}

type submissionProbeRisk struct {
	trace    *[]string
	err      error
	onCheck  func()
	checks   atomic.Int32
	releases atomic.Int32
}

func (r *submissionProbeRisk) CheckSubmission(context.Context, model.OrderRequest, *model.Instrument) (func(), error) {
	r.checks.Add(1)
	if r.trace != nil {
		*r.trace = append(*r.trace, "risk")
	}
	if r.onCheck != nil {
		r.onCheck()
	}
	return func() { r.releases.Add(1) }, r.err
}

type submissionProbeGate struct {
	trace *[]string
	err   error
	calls atomic.Int32
}

func (g *submissionProbeGate) CanSubmit(model.OrderRequest) error {
	g.calls.Add(1)
	if g.trace != nil {
		*g.trace = append(*g.trace, "gate")
	}
	return g.err
}

func (*submissionProbeGate) CanCancel() error { return nil }
func (*submissionProbeGate) CanModify() error { return nil }

func TestSubmitPreValidationAllowedOperationsAreFinite(t *testing.T) {
	validationErr := errors.New("stop at validation")
	trace := make([]string, 0, 3)
	client := &submissionProbeExec{FakeExec: runtimetest.NewFakeExec(), trace: &trace, validationErr: validationErr}
	risk := &submissionProbeRisk{trace: &trace}
	gate := &submissionProbeGate{trace: &trace}
	recorder := latency.NewRecorder(8)
	engine, cached, store := testEngine(client)
	engine.WithCommandGate(gate).WithRisk(risk, nil).WithLatencyRecorder(recorder)

	req := testReq("")
	if _, err := engine.Submit(context.Background(), req); !errors.Is(err, validationErr) {
		t.Fatalf("Submit err=%v, want %v", err, validationErr)
	}
	if want := []string{"gate", "capabilities", "validate"}; !reflect.DeepEqual(trace, want) {
		t.Fatalf("pre-validation trace=%v, want %v", trace, want)
	}
	if client.lastValidated.ClientID == "" || client.lastValidated.AccountID != "test" {
		t.Fatalf("validation request missing runtime defaults: %+v", client.lastValidated)
	}
	if client.validateCalls.Load() != 1 || client.submitCalls.Load() != 0 || risk.checks.Load() != 0 || risk.releases.Load() != 0 {
		t.Fatalf("calls validate=%d submit=%d risk=%d releases=%d", client.validateCalls.Load(), client.submitCalls.Load(), risk.checks.Load(), risk.releases.Load())
	}
	if len(store.Records()) != 0 || engine.InFlightCount() != 0 {
		t.Fatalf("validation failure produced durable state: records=%d inflight=%d", len(store.Records()), engine.InFlightCount())
	}
	if _, ok := cached.OrderByClientIDForAccount("test", client.lastValidated.ClientID); ok {
		t.Fatal("validation failure produced a cached order")
	}
	if got := len(client.Events()); got != 0 {
		t.Fatalf("validation failure emitted %d execution events", got)
	}
	if got := recorder.Snapshot().CommandsTotal; got != 1 {
		t.Fatalf("latency commands=%d, want 1", got)
	}
}

func TestSubmitDuplicateIDShortCircuitsBeforeValidation(t *testing.T) {
	client := &submissionProbeExec{FakeExec: runtimetest.NewFakeExec()}
	engine, _, _ := testEngine(client)
	req := testReq("duplicate-before-validation")
	if _, err := engine.Submit(context.Background(), req); err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	if _, err := engine.Submit(context.Background(), req); err == nil {
		t.Fatal("duplicate Submit succeeded")
	}
	if client.validateCalls.Load() != 1 || client.submitCalls.Load() != 1 {
		t.Fatalf("calls validate=%d submit=%d, want 1/1", client.validateCalls.Load(), client.submitCalls.Load())
	}
}

func TestSubmitCommandGateShortCircuitsBeforeValidation(t *testing.T) {
	client := &submissionProbeExec{FakeExec: runtimetest.NewFakeExec()}
	risk := &submissionProbeRisk{}
	gateErr := errors.New("command gate denied")
	gate := &submissionProbeGate{err: gateErr}
	engine, _, store := testEngine(client)
	engine.WithCommandGate(gate).WithRisk(risk, nil)

	if _, err := engine.Submit(context.Background(), testReq("gate-denied")); !errors.Is(err, gateErr) {
		t.Fatalf("Submit err=%v, want %v", err, gateErr)
	}
	if client.validateCalls.Load() != 0 || client.submitCalls.Load() != 0 || risk.checks.Load() != 0 || len(store.Records()) != 0 {
		t.Fatalf("gate denial calls validate=%d submit=%d risk=%d records=%d", client.validateCalls.Load(), client.submitCalls.Load(), risk.checks.Load(), len(store.Records()))
	}
}

func TestSubmitCapabilityCheckShortCircuitsBeforeValidation(t *testing.T) {
	caps := runtimetest.NewFakeExec().Capabilities()
	caps.Trading.Submit = false
	client := &submissionProbeExec{FakeExec: runtimetest.NewFakeExec(), capabilities: &caps}
	risk := &submissionProbeRisk{}
	engine, _, store := testEngine(client)
	engine.WithRisk(risk, nil)

	if _, err := engine.Submit(context.Background(), testReq("unsupported-submit")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("Submit err=%v, want ErrNotSupported", err)
	}
	if client.validateCalls.Load() != 0 || client.submitCalls.Load() != 0 || risk.checks.Load() != 0 || len(store.Records()) != 0 {
		t.Fatalf("capability denial calls validate=%d submit=%d risk=%d records=%d", client.validateCalls.Load(), client.submitCalls.Load(), risk.checks.Load(), len(store.Records()))
	}
}

func TestConfiguredRiskDenialStopsBeforeDurableAndVenueEffects(t *testing.T) {
	client := &submissionProbeExec{FakeExec: runtimetest.NewFakeExec()}
	riskErr := errors.New("local risk denied")
	risk := &submissionProbeRisk{err: riskErr}
	engine, cached, store := testEngine(client)
	engine.WithRisk(risk, nil)
	req := testReq("risk-denied")

	if _, err := engine.Submit(context.Background(), req); !errors.Is(err, riskErr) {
		t.Fatalf("Submit err=%v, want %v", err, riskErr)
	}
	if client.validateCalls.Load() != 1 || client.submitCalls.Load() != 0 || risk.checks.Load() != 1 || risk.releases.Load() != 1 {
		t.Fatalf("calls validate=%d submit=%d risk=%d releases=%d", client.validateCalls.Load(), client.submitCalls.Load(), risk.checks.Load(), risk.releases.Load())
	}
	if len(store.Records()) != 0 || engine.InFlightCount() != 0 {
		t.Fatalf("risk denial produced durable state: records=%d inflight=%d", len(store.Records()), engine.InFlightCount())
	}
	if _, ok := cached.Order(req.ClientID); ok {
		t.Fatal("risk denial produced a cached order")
	}
}

type conflictOnIntentJournal struct {
	journal.Store
	cache *cache.Cache
}

func (j *conflictOnIntentJournal) AppendCommandIntent(ctx context.Context, intent journal.CommandIntent) error {
	if err := j.Store.AppendCommandIntent(ctx, intent); err != nil {
		return err
	}
	return j.cache.UpsertOrderChecked(model.Order{
		Request: model.OrderRequest{
			AccountID: intent.AccountID, InstrumentID: intent.InstrumentID, ClientID: intent.ClientID,
			Side: intent.Side, Type: intent.OrderType, TIF: intent.TIF, Quantity: intent.Quantity, Price: intent.Price,
		},
		VenueOrderID: "external-" + intent.ClientID,
		Status:       enums.StatusNew,
	})
}

func TestConfiguredRiskReleaseForEveryOutcome(t *testing.T) {
	tests := []struct {
		name      string
		wantErr   bool
		configure func(*submissionProbeExec, *submissionProbeRisk, *bt_exec.Engine, *cache.Cache, *journal.MemoryJournal, model.OrderRequest) context.Context
	}{
		{name: "accepted"},
		{
			name:    "definitive rejection",
			wantErr: true,
			configure: func(client *submissionProbeExec, _ *submissionProbeRisk, _ *bt_exec.Engine, _ *cache.Cache, _ *journal.MemoryJournal, _ model.OrderRequest) context.Context {
				client.SetSubmitResult(nil, errors.Join(contract.ErrVenueRejected, errors.New("venue rejected")))
				return context.Background()
			},
		},
		{
			name:    "ambiguous timeout",
			wantErr: true,
			configure: func(client *submissionProbeExec, _ *submissionProbeRisk, _ *bt_exec.Engine, _ *cache.Cache, _ *journal.MemoryJournal, _ model.OrderRequest) context.Context {
				client.SetSubmitResult(nil, context.DeadlineExceeded)
				return context.Background()
			},
		},
		{
			name:    "canceled before venue handoff",
			wantErr: true,
			configure: func(_ *submissionProbeExec, risk *submissionProbeRisk, _ *bt_exec.Engine, _ *cache.Cache, _ *journal.MemoryJournal, _ model.OrderRequest) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				risk.onCheck = cancel
				return ctx
			},
		},
		{
			name:    "journal failure",
			wantErr: true,
			configure: func(_ *submissionProbeExec, _ *submissionProbeRisk, engine *bt_exec.Engine, _ *cache.Cache, store *journal.MemoryJournal, _ model.OrderRequest) context.Context {
				engine.WithJournal(&failingJournal{Store: store, failIntent: errors.New("intent failed")})
				return context.Background()
			},
		},
		{
			name:    "cache conflict",
			wantErr: true,
			configure: func(_ *submissionProbeExec, _ *submissionProbeRisk, engine *bt_exec.Engine, cached *cache.Cache, store *journal.MemoryJournal, _ model.OrderRequest) context.Context {
				engine.WithJournal(&conflictOnIntentJournal{Store: store, cache: cached})
				return context.Background()
			},
		},
		{
			name:    "adapter identity error",
			wantErr: true,
			configure: func(client *submissionProbeExec, _ *submissionProbeRisk, _ *bt_exec.Engine, _ *cache.Cache, _ *journal.MemoryJournal, req model.OrderRequest) context.Context {
				foreign := req
				foreign.ClientID = "foreign-client-id"
				client.SetSubmitResult(&model.Order{Request: foreign, VenueOrderID: "foreign-order", Status: enums.StatusNew}, nil)
				return context.Background()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &submissionProbeExec{FakeExec: runtimetest.NewFakeExec()}
			risk := &submissionProbeRisk{}
			engine, cached, store := testEngine(client)
			engine.WithRisk(risk, nil)
			req := testReq("release-" + tt.name)
			ctx := context.Background()
			if tt.configure != nil {
				ctx = tt.configure(client, risk, engine, cached, store, req)
			}

			_, err := engine.Submit(ctx, req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Submit err=%v wantErr=%v", err, tt.wantErr)
			}
			if got := risk.checks.Load(); got != 1 {
				t.Fatalf("risk checks=%d, want 1", got)
			}
			if got := risk.releases.Load(); got != 1 {
				t.Fatalf("risk releases=%d, want 1", got)
			}
		})
	}
}

package runtimeaccept

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestWaitForPrivateExecutionEvidenceRequiresOrderAndFillStreamEvents(t *testing.T) {
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	events := make(chan contract.ExecEnvelope, 2)
	events <- contract.NewExecEnvelopeWithMeta(contract.OrderEvent{Order: model.Order{
		Request: model.OrderRequest{AccountID: "TEST-001", InstrumentID: id},
	}}, contract.EventMeta{Source: contract.SourceAdapterStream, Flags: contract.EventFlagFromStream})
	events <- contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: model.Fill{
		AccountID: "TEST-001", InstrumentID: id,
	}}, contract.EventMeta{Source: contract.SourceAdapterStream, Flags: contract.EventFlagFromStream})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	report, err := WaitForPrivateExecutionEvidence(ctx, events, id, "TEST-001")
	if err != nil {
		t.Fatalf("WaitForPrivateExecutionEvidence: %v", err)
	}
	if report.OrderEvents != 1 || report.FillEvents != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestWaitForPrivateExecutionEvidenceRejectsRESTMetadata(t *testing.T) {
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	events := make(chan contract.ExecEnvelope, 1)
	events <- contract.NewExecEnvelopeWithMeta(contract.OrderEvent{Order: model.Order{
		Request: model.OrderRequest{AccountID: "TEST-001", InstrumentID: id},
	}}, contract.EventMeta{Source: contract.SourceAdapterREST, Flags: contract.EventFlagFromSnapshot})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := WaitForPrivateExecutionEvidence(ctx, events, id, "TEST-001"); err == nil {
		t.Fatal("REST event unexpectedly satisfied private stream evidence")
	}
}

func TestWaitForPrivateFillEvidenceAcceptsFillWithoutOrderEvent(t *testing.T) {
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	events := make(chan contract.ExecEnvelope, 1)
	events <- contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: model.Fill{
		AccountID: "TEST-001", InstrumentID: id,
	}}, contract.EventMeta{Source: contract.SourceAdapterStream, Flags: contract.EventFlagFromStream})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	report, err := WaitForPrivateFillEvidence(ctx, events, id, "TEST-001")
	if err != nil {
		t.Fatalf("WaitForPrivateFillEvidence: %v", err)
	}
	if report.OrderEvents != 0 || report.FillEvents != 1 {
		t.Fatalf("report=%+v", report)
	}
}

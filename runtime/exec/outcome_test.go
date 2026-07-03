package exec_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
)

func TestOutcomeClassifier(t *testing.T) {
	cases := []struct {
		name string
		got  exec.Outcome
		want exec.OutcomeClass
	}{
		{"risk before boundary", exec.ClassifyCommandResult(false, errors.New("risk rejected")), exec.OutcomeLocalDenied},
		{"unsupported before boundary", exec.ClassifyCommandResult(false, contract.ErrNotSupported), exec.OutcomeUnsupported},
		{"venue structured reject", exec.ClassifyCommandResult(true, exec.DefinitiveReject("bad price")), exec.OutcomeDefinitiveVenueRejected},
		{"contract venue reject after boundary", exec.ClassifyCommandResult(true, contract.ErrVenueRejected), exec.OutcomeDefinitiveVenueRejected},
		{"timeout after possible send", exec.ClassifyCommandResult(true, context.DeadlineExceeded), exec.OutcomeAmbiguous},
		{"disconnect after possible send", exec.ClassifyCommandResult(true, io.ErrUnexpectedEOF), exec.OutcomeAmbiguous},
		{"batch error without per-order result", exec.ClassifyCommandResult(true, errors.New("batch failed")), exec.OutcomeAmbiguous},
		{"acknowledged submit", exec.ClassifySubmitResult(true, &model.Order{Status: enums.StatusNew}, nil), exec.OutcomeConfirmedAccepted},
		{"rejected submit payload", exec.ClassifySubmitResult(true, &model.Order{Status: enums.StatusRejected}, nil), exec.OutcomeDefinitiveVenueRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Class != tc.want {
				t.Fatalf("class=%s, want %s", tc.got.Class, tc.want)
			}
		})
	}
}

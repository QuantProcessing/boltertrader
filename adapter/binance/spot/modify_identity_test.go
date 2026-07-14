package spot

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
)

func TestBinanceSpotModifyIsDisabledBeforeREST(t *testing.T) {
	inst := testSpotInstrument()
	requests := 0
	rest := testREST(func(r *http.Request) (string, int) {
		requests++
		return `{}`, http.StatusInternalServerError
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())

	order, err := exec.Modify(context.Background(), inst.ID, "555", d("3001.00"), d("0.0200"))
	if order != nil || !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("Modify order=%+v err=%v, want explicit unsupported result", order, err)
	}
	if requests != 0 {
		t.Fatalf("REST requests=%d, unsupported modify must stop before transport", requests)
	}
	if exec.Capabilities().Trading.Modify {
		t.Fatal("Modify capability=true, want disabled until runtime supports order incarnations")
	}
}

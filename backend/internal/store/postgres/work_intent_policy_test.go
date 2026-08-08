package postgres

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
)

func TestFundingOpenIntentMustMatchEveryFrozenExecutionField(t *testing.T) {
	frozen := json.RawMessage(`{"previewId":"preview-1","opportunityId":"opportunity-1","longAccountId":"long","shortAccountId":"short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"50","maxSlippageBps":"10","holdThroughSettlement":true}`)
	if err := validateIntentPolicy("test", frozen, work.IntentRequest{IntentKind: "funding.open", Payload: frozen}); err != nil {
		t.Fatalf("matching intent error=%v", err)
	}
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "slippage drift", payload: `{"previewId":"preview-1","opportunityId":"opportunity-1","longAccountId":"long","shortAccountId":"short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"50","maxSlippageBps":"11","holdThroughSettlement":true}`},
		{name: "settlement policy drift", payload: `{"previewId":"preview-1","opportunityId":"opportunity-1","longAccountId":"long","shortAccountId":"short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"50","maxSlippageBps":"10","holdThroughSettlement":false}`},
		{name: "missing field", payload: `{"previewId":"preview-1","opportunityId":"opportunity-1","longAccountId":"long","shortAccountId":"short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"50","maxSlippageBps":"10"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateIntentPolicy("test", frozen, work.IntentRequest{IntentKind: "funding.open", Payload: json.RawMessage(test.payload)})
			if !errors.Is(err, work.ErrWorkRevisionConflict) {
				t.Fatalf("validateIntentPolicy() error=%v", err)
			}
		})
	}
}

package bitget

import (
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/shopspring/decimal"
)

func TestExecutionClientValidateSubmitCatchesLocalFailures(t *testing.T) {
	provider := newInstrumentProvider()
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}
	provider.LoadSnapshot([]*model.Instrument{{ID: id, VenueSymbol: "BTCUSDT"}})
	client := newExecutionClient(nil, provider, nil)
	validator, ok := any(client).(interface {
		ValidateSubmit(model.OrderRequest) error
	})
	if !ok {
		t.Fatal("execution client does not implement local submit validation")
	}

	t.Run("unknown instrument", func(t *testing.T) {
		req := validBitgetValidationRequest(id)
		req.InstrumentID.Symbol = "UNKNOWN"
		if err := validator.ValidateSubmit(req); !errors.Is(err, errs.ErrSymbolNotFound) {
			t.Fatalf("ValidateSubmit err=%v, want ErrSymbolNotFound", err)
		}
	})
	t.Run("conversion", func(t *testing.T) {
		req := validBitgetValidationRequest(id)
		req.Side = enums.SideUnknown
		if err := validator.ValidateSubmit(req); !errors.Is(err, errs.ErrNotSupported) {
			t.Fatalf("ValidateSubmit err=%v, want ErrNotSupported", err)
		}
	})
}

func validBitgetValidationRequest(id model.InstrumentID) model.OrderRequest {
	return model.OrderRequest{
		InstrumentID: id,
		ClientID:     "validate",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.NewFromInt(1),
		Price:        decimal.NewFromInt(100),
	}
}

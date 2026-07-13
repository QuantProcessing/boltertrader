package spot

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/aster/spot"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest      *sdkspot.Client
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.AccountEnvelope]
	streaming bool
}

func newAccountClient(rest *sdkspot.Client, clk clock.Clock, accountID string) *accountClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	if accountID == "" {
		accountID = AccountIDDefault
	}
	return &accountClient{rest: rest, clk: clk, accountID: accountID, stream: wsstream.New[contract.AccountEnvelope](256)}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:     VenueName,
		Products:  []contract.ProductCapability{{Kind: enums.KindSpot, Account: true}},
		Reports:   contract.ReportCapabilities{AccountBalanceSnapshots: true, AccountStateSnapshots: true},
		Streaming: contract.StreamCapabilities{Account: c.streaming, AccountState: false},
	}
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	if c.rest == nil {
		return nil, fmt.Errorf("aster spot: rest client not configured: %w", errs.ErrNotSupported)
	}
	account, err := c.rest.GetAccount(ctx)
	if err != nil {
		return nil, mapAsterError(err)
	}
	if err := validateAccountResponseDecimals(account); err != nil {
		return nil, err
	}
	balances := balancesFromResponse(account, c.accountID, c.clk.Now())
	for _, balance := range balances {
		if err := balance.ValidateCash(); err != nil {
			return nil, err
		}
	}
	return balances, nil
}

func (c *accountClient) Positions(context.Context) ([]model.Position, error) {
	return []model.Position{}, nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	if c.rest == nil {
		return model.AccountState{}, fmt.Errorf("aster spot: rest client not configured: %w", errs.ErrNotSupported)
	}
	account, err := c.rest.GetAccount(ctx)
	if err != nil {
		return model.AccountState{}, mapAsterError(err)
	}
	if err := validateAccountResponseDecimals(account); err != nil {
		return model.AccountState{}, err
	}
	state := accountStateFromResponse(account, c.accountID, c.clk.Now())
	if err := state.Validate(); err != nil {
		return model.AccountState{}, err
	}
	return state, nil
}

func (c *accountClient) SetLeverage(context.Context, model.InstrumentID, decimal.Decimal) error {
	return fmt.Errorf("aster spot: cash accounts do not support leverage: %w", errs.ErrNotSupported)
}

func (c *accountClient) SetMarginMode(context.Context, model.InstrumentID, string) error {
	return fmt.Errorf("aster spot: cash accounts do not support margin mode: %w", errs.ErrNotSupported)
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }
func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}
func (c *accountClient) Close() error { c.stream.Close(); return nil }

func accountStateFromResponse(account *sdkspot.AccountResponse, accountID string, fallback time.Time) model.AccountState {
	ts := fallback
	if account != nil && account.UpdateTime > 0 {
		ts = timeFromMillis(account.UpdateTime)
	}
	return model.AccountState{
		AccountID: accountID,
		Venue:     VenueName,
		Type:      model.AccountCash,
		Balances:  balancesFromResponse(account, accountID, fallback),
		Reported:  true,
		EventID:   model.AccountStateEventID(VenueName, accountID, ts),
		TsEvent:   ts,
		TsInit:    fallback,
	}
}

func balancesFromResponse(account *sdkspot.AccountResponse, accountID string, fallback time.Time) []model.AccountBalance {
	if account == nil {
		return nil
	}
	ts := fallback
	if account.UpdateTime > 0 {
		ts = timeFromMillis(account.UpdateTime)
	}
	out := make([]model.AccountBalance, 0, len(account.Balances))
	for _, bal := range account.Balances {
		if bal.Asset == "" {
			continue
		}
		free := dec(bal.Free)
		locked := dec(bal.Locked)
		out = append(out, model.AccountBalance{
			AccountID: accountID,
			Currency:  bal.Asset,
			Total:     free.Add(locked),
			Free:      free,
			Available: free,
			Locked:    locked,
			UpdatedAt: ts,
		})
	}
	return out
}

func validateAccountResponseDecimals(account *sdkspot.AccountResponse) error {
	if account == nil {
		return fmt.Errorf("aster spot: account response is required")
	}
	for _, bal := range account.Balances {
		if bal.Asset == "" {
			return fmt.Errorf("aster spot: account balance asset is required")
		}
		for field, raw := range map[string]string{"free": bal.Free, "locked": bal.Locked} {
			value, err := parseRequiredSDKDecimal(field, raw)
			if err != nil {
				return fmt.Errorf("aster spot: account balance %s: %w", bal.Asset, err)
			}
			if value.IsNegative() {
				return fmt.Errorf("aster spot: account balance %s has negative %s", bal.Asset, field)
			}
		}
	}
	return nil
}

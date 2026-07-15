package perp

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/QuantProcessing/boltertrader/adapter/internal/streamgap"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
)

const (
	VenueName        = "ASTER"
	AccountIDDefault = "ASTER-001"
	privateStreamID  = "aster:perp:private"
)

type Config struct {
	Profile    astercommon.Profile
	Security   *astercommon.SecurityContext
	Client     *sdkperp.Client
	MarketWS   any
	AccountWS  any
	AccountID  string
	HTTPClient *http.Client
	Clock      clock.Clock
}

func DefaultConfig(environment astercommon.Environment, security *astercommon.SecurityContext) (Config, error) {
	profile, err := astercommon.NewProfile(environment, astercommon.ProductPerp)
	if err != nil {
		return Config{}, err
	}
	return Config{Profile: profile, Security: security, AccountID: AccountIDDefault}, nil
}

type Adapter struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient

	provider   *instrumentProvider
	rest       *sdkperp.Client
	exec       *executionClient
	acct       *accountClient
	wsAcct     perpAccountWebsocket
	startMu    sync.Mutex
	registered bool
}

type perpAccountWebsocket interface {
	SubscribeAccountUpdate(func(*sdkperp.AccountUpdateEvent))
	SubscribeOrderUpdate(func(*sdkperp.OrderUpdateEvent))
	Connect() error
	Close()
}

func New(ctx context.Context, cfg Config) (*Adapter, error) {
	if cfg.Profile.Product() != astercommon.ProductPerp {
		return nil, fmt.Errorf("aster perp: profile product is %q", cfg.Profile.Product())
	}
	if err := cfg.Profile.Validate(); err != nil {
		return nil, err
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewRealClock()
	}
	rest := cfg.Client
	if rest == nil {
		var err error
		rest, err = sdkperp.NewClient(cfg.Profile, cfg.Security)
		if err != nil {
			return nil, err
		}
	}
	if rest.Profile() != cfg.Profile {
		return nil, fmt.Errorf("aster perp: injected client profile does not match adapter profile")
	}
	if cfg.HTTPClient != nil {
		rest.WithHTTPClient(cfg.HTTPClient)
	}
	accountID := cfg.AccountID
	if accountID == "" {
		accountID = AccountIDDefault
	}
	provider := newInstrumentProvider()
	if err := provider.Load(ctx, rest, cfg.Profile); err != nil {
		return nil, fmt.Errorf("aster perp: load instruments: %w", err)
	}
	marketWS := cfg.MarketWS
	if marketWS == nil {
		ws, err := sdkperp.NewWsMarketClient(ctx, cfg.Profile)
		if err != nil {
			return nil, fmt.Errorf("aster perp: create market websocket: %w", err)
		}
		marketWS = ws
	} else if _, ok := marketWS.(perpMarketWebsocket); !ok {
		return nil, fmt.Errorf("aster perp: configured market websocket has unsupported type %T", marketWS)
	}
	accountWS := cfg.AccountWS
	if accountWS == nil && cfg.Security != nil {
		ws, err := sdkperp.NewWsAccountClient(ctx, cfg.Profile, cfg.Security)
		if err != nil {
			return nil, fmt.Errorf("aster perp: create account websocket: %w", err)
		}
		accountWS = ws
	} else if accountWS != nil {
		if _, ok := accountWS.(perpAccountWebsocket); !ok {
			return nil, fmt.Errorf("aster perp: configured account websocket has unsupported type %T", accountWS)
		}
	}
	wsAcct, _ := accountWS.(perpAccountWebsocket)
	market := newMarketDataClient(rest, marketWS, provider, clk)
	exec := newExecutionClient(rest, provider, clk, accountID)
	acct := newAccountClient(rest, provider, clk, accountID)
	exec.streaming = wsAcct != nil
	acct.streaming = wsAcct != nil
	return &Adapter{Market: market, Execution: exec, Account: acct, provider: provider, rest: rest, exec: exec, acct: acct, wsAcct: wsAcct}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.wsAcct == nil {
		return fmt.Errorf("aster perp: account websocket not configured")
	}
	a.startMu.Lock()
	if !a.registered {
		if hooks, ok := a.wsAcct.(interface {
			SetReconnectHooks(func(error), func())
		}); ok {
			reporter := streamgap.New(VenueName, a.exec.accountID, privateStreamID, a.exec.stream.Emit)
			hooks.SetReconnectHooks(func(err error) {
				reason := "private stream disconnected"
				if err != nil {
					reason = err.Error()
				}
				reporter.Started(reason)
			}, func() {
				reporter.Recovered("private stream subscriptions restored")
			})
		}
		resolve := a.provider.resolveKnownVenueSymbol
		a.wsAcct.SubscribeOrderUpdate(func(ev *sdkperp.OrderUpdateEvent) {
			envelopes, err := execEnvelopesFromOrderUpdate(ev, resolve, a.exec.accountID)
			if err != nil {
				return
			}
			for _, envelope := range envelopes {
				a.exec.emitEnvelope(envelope)
			}
		})
		a.wsAcct.SubscribeAccountUpdate(func(ev *sdkperp.AccountUpdateEvent) {
			events, err := accountEventsFromUpdate(ev, resolve, a.acct.accountID)
			if err != nil {
				return
			}
			for _, event := range events {
				a.acct.emit(event)
			}
		})
		a.registered = true
	}
	a.startMu.Unlock()
	return a.wsAcct.Connect()
}

func (a *Adapter) Close() error {
	if a.wsAcct != nil {
		a.wsAcct.Close()
	}
	_ = a.Execution.Close()
	_ = a.Account.Close()
	_ = a.Market.Close()
	return nil
}

package account

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

type spotHandle struct{ client exchange.SpotClient }

func (handle spotHandle) refresh(ctx context.Context) ([]exchange.Instrument, error) {
	return handle.client.RefreshInstruments(ctx)
}
func (handle spotHandle) close() error                      { return handle.client.Close() }
func (handle spotHandle) spot() (exchange.SpotClient, bool) { return handle.client, true }
func (handle spotHandle) perp() (exchange.PerpClient, bool) { return nil, false }
func (handle spotHandle) exactLookupReady() bool {
	_, ok := handle.client.(exchange.ExactOrderLookup)
	return ok
}

type perpHandle struct{ client exchange.PerpClient }

func (handle perpHandle) refresh(ctx context.Context) ([]exchange.Instrument, error) {
	return handle.client.RefreshInstruments(ctx)
}
func (handle perpHandle) close() error                      { return handle.client.Close() }
func (handle perpHandle) spot() (exchange.SpotClient, bool) { return nil, false }
func (handle perpHandle) perp() (exchange.PerpClient, bool) { return handle.client, true }
func (handle perpHandle) exactLookupReady() bool {
	_, ok := handle.client.(exchange.ExactOrderLookup)
	return ok
}

type factoryRuntimeGroupBuilder struct {
	httpClient *http.Client
	catalog    exchange.InstrumentCatalog
}

type factoryConnectionRoute struct{ handle clientHandle }

func (route factoryConnectionRoute) Probe(ctx context.Context) error {
	if ctx == nil || route.handle == nil {
		return fmt.Errorf("credential balance verification failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if client, ok := route.handle.spot(); ok {
		if _, err := client.Balances(ctx); err != nil {
			return fmt.Errorf("credential balance verification failed: %w", err)
		}
		return nil
	}
	if client, ok := route.handle.perp(); ok {
		if _, err := client.Balances(ctx); err != nil {
			return fmt.Errorf("credential balance verification failed: %w", err)
		}
		return nil
	}
	return fmt.Errorf("credential balance verification failed")
}

func (route factoryConnectionRoute) Close() error { return route.handle.close() }

func (route factoryConnectionRoute) SpotClient() (exchange.SpotClient, bool) {
	return route.handle.spot()
}

func (route factoryConnectionRoute) PerpClient() (exchange.PerpClient, bool) {
	return route.handle.perp()
}

func (route factoryConnectionRoute) OrderClient() (exchange.OrderREST, bool) {
	if client, ok := route.handle.spot(); ok {
		return client, true
	}
	if client, ok := route.handle.perp(); ok {
		return client, true
	}
	return nil, false
}

func (route factoryConnectionRoute) ExactLookupReady() bool {
	return route.handle.exactLookupReady()
}

func NewFactoryRuntimeGroupBuilder(httpClient *http.Client, catalog exchange.InstrumentCatalog) RuntimeGroupBuilder {
	return factoryRuntimeGroupBuilder{httpClient: httpClient, catalog: catalog}
}

func (builder factoryRuntimeGroupBuilder) BuildRoute(_ context.Context, snapshot ConnectionSpec, route RouteDefinition, decoded *credential.Decoded) (ConnectionRoute, error) {
	environment, err := factoryEnvironment(snapshot.NativeEnvironment)
	if err != nil || decoded == nil || decoded.Venue != snapshot.Venue || builder.httpClient == nil || builder.catalog == nil {
		return nil, fmt.Errorf("construct runtime route failed")
	}
	options := []factory.Option{factory.WithEnvironment(environment), factory.WithHTTPClient(ratelimit.WithPrincipal(builder.httpClient, string(snapshot.AccountID))), factory.WithInstrumentCatalog(builder.catalog)}
	identity := snapshot.Venue + ":" + route.Product + ":" + route.SettlementAsset
	var handle clientHandle
	switch credentials := decoded.Credentials.(type) {
	case *credential.APIKeySecret:
		switch identity {
		case "binance:spot:":
			handle, err = newSpotFactoryHandle(factory.BinanceSpotConfig(credentials.APIKey, credentials.APISecret, options...))
		case "binance:perp:USDT":
			handle, err = newPerpFactoryHandle(factory.BinanceUSDPerpConfig(credentials.APIKey, credentials.APISecret, options...))
		case "bybit:spot:":
			handle, err = newSpotFactoryHandle(factory.BybitSpotConfig(credentials.APIKey, credentials.APISecret, options...))
		case "bybit:perp:USDT":
			handle, err = newPerpFactoryHandle(factory.BybitUSDTPerpConfig(credentials.APIKey, credentials.APISecret, options...))
		case "bybit:perp:USDC":
			handle, err = newPerpFactoryHandle(factory.BybitUSDCPerpConfig(credentials.APIKey, credentials.APISecret, options...))
		case "gate:spot:":
			handle, err = newSpotFactoryHandle(factory.GateSpotConfig(credentials.APIKey, credentials.APISecret, options...))
		case "gate:perp:USDT":
			handle, err = newPerpFactoryHandle(factory.GateUSDTPerpConfig(credentials.APIKey, credentials.APISecret, options...))
		}
	case *credential.APIKeySecretPassphrase:
		switch identity {
		case "okx:spot:":
			handle, err = newSpotFactoryHandle(factory.OKXSpotConfig(credentials.APIKey, credentials.APISecret, credentials.Passphrase, options...))
		case "okx:perp:USDT":
			handle, err = newPerpFactoryHandle(factory.OKXUSDTPerpConfig(credentials.APIKey, credentials.APISecret, credentials.Passphrase, options...))
		case "bitget:spot:":
			handle, err = newSpotFactoryHandle(factory.BitgetSpotConfig(credentials.APIKey, credentials.APISecret, credentials.Passphrase, options...))
		case "bitget:perp:USDT":
			handle, err = newPerpFactoryHandle(factory.BitgetUSDTPerpConfig(credentials.APIKey, credentials.APISecret, credentials.Passphrase, options...))
		case "bitget:perp:USDC":
			handle, err = newPerpFactoryHandle(factory.BitgetUSDCPerpConfig(credentials.APIKey, credentials.APISecret, credentials.Passphrase, options...))
		}
	case *credential.LighterSigner:
		if identity == "lighter:spot:" {
			handle, err = newSpotFactoryHandle(factory.LighterSpotConfig(credentials.PrivateKey, credentials.AccountIndex, credentials.KeyIndex, options...))
		} else if identity == "lighter:perp:USDC" {
			handle, err = newPerpFactoryHandle(factory.LighterPerpConfig(credentials.PrivateKey, credentials.AccountIndex, credentials.KeyIndex, options...))
		}
	case *credential.EVMPrivateKey:
		if credentials.AccountAddress != "" {
			options = append(options, factory.WithAccountAddress(credentials.AccountAddress))
		}
		if identity == "hyperliquid:spot:" {
			handle, err = newSpotFactoryHandle(factory.HyperliquidSpotConfig(credentials.PrivateKey, options...))
		} else if identity == "hyperliquid:perp:USDC" {
			handle, err = newPerpFactoryHandle(factory.HyperliquidPerpConfig(credentials.PrivateKey, options...))
		}
	case *credential.AsterAPIWallet:
		if identity == "aster:spot:" {
			handle, err = newSpotFactoryHandle(factory.AsterSpotConfig(credentials.UserAddress, credentials.APIWalletPrivateKey, credentials.ExpectedSigner, options...))
		} else if identity == "aster:perp:USDT" {
			handle, err = newPerpFactoryHandle(factory.AsterUSDTPerpConfig(credentials.UserAddress, credentials.APIWalletPrivateKey, credentials.ExpectedSigner, options...))
		}
	case *credential.NadoSigner:
		if identity == "nado:spot:USDT0" {
			handle, err = newSpotFactoryHandle(factory.NadoSpotConfig(credentials.PrivateKey, credentials.Subaccount, options...))
		} else if identity == "nado:perp:USDT0" {
			handle, err = newPerpFactoryHandle(factory.NadoUSDT0PerpConfig(credentials.PrivateKey, credentials.Subaccount, options...))
		}
	}
	if err != nil || handle == nil {
		return nil, fmt.Errorf("construct runtime route failed")
	}
	return factoryConnectionRoute{handle: handle}, nil
}

func newSpotFactoryHandle(configuration factory.Config[exchange.SpotClient]) (clientHandle, error) {
	client, err := factory.New(configuration)
	if err != nil {
		return nil, err
	}
	return spotHandle{client: client}, nil
}

func newPerpFactoryHandle(configuration factory.Config[exchange.PerpClient]) (clientHandle, error) {
	client, err := factory.New(configuration)
	if err != nil {
		return nil, err
	}
	return perpHandle{client: client}, nil
}

func buildFactoryClient(ctx context.Context, account config.AccountConfig, options Options) (clientHandle, error) {
	environment, err := factoryEnvironment(account.Environment)
	if err != nil {
		return nil, err
	}
	factoryOptions := []factory.Option{
		factory.WithEnvironment(environment),
		factory.WithHTTPClient(options.HTTPClient),
		factory.WithInstrumentCatalog(options.ExchangeCatalog),
	}
	resolve := func(ref secret.Ref) (string, error) {
		value, err := options.Secrets.Resolve(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("resolve configured credential failed")
		}
		return value, nil
	}
	buildAPI := func(credentials config.APIKeySecretCredentials, spot bool, configSpot func(string, string, ...factory.Option) factory.Config[exchange.SpotClient], configPerp func(string, string, ...factory.Option) factory.Config[exchange.PerpClient]) (clientHandle, error) {
		apiKey, err := resolve(credentials.APIKeyRef)
		if err != nil {
			return nil, err
		}
		apiSecret, err := resolve(credentials.APISecretRef)
		if err != nil {
			return nil, err
		}
		if spot {
			client, err := factory.New(configSpot(apiKey, apiSecret, factoryOptions...))
			if err != nil {
				return nil, fmt.Errorf("construct configured client failed")
			}
			return spotHandle{client: client}, nil
		}
		client, err := factory.New(configPerp(apiKey, apiSecret, factoryOptions...))
		if err != nil {
			return nil, fmt.Errorf("construct configured client failed")
		}
		return perpHandle{client: client}, nil
	}

	switch credentials := account.Credentials.(type) {
	case config.APIKeySecretCredentials:
		switch account.Venue + ":" + account.Product + ":" + account.Settlement {
		case "binance:spot:":
			return buildAPI(credentials, true, factory.BinanceSpotConfig, nil)
		case "binance:perp:USDT":
			return buildAPI(credentials, false, nil, factory.BinanceUSDPerpConfig)
		case "bybit:spot:":
			return buildAPI(credentials, true, factory.BybitSpotConfig, nil)
		case "bybit:perp:USDT":
			return buildAPI(credentials, false, nil, factory.BybitUSDTPerpConfig)
		case "bybit:perp:USDC":
			return buildAPI(credentials, false, nil, factory.BybitUSDCPerpConfig)
		case "gate:spot:":
			return buildAPI(credentials, true, factory.GateSpotConfig, nil)
		case "gate:perp:USDT":
			return buildAPI(credentials, false, nil, factory.GateUSDTPerpConfig)
		}
	case config.APIKeySecretPassphraseCredentials:
		apiKey, err := resolve(credentials.APIKeyRef)
		if err != nil {
			return nil, err
		}
		apiSecret, err := resolve(credentials.APISecretRef)
		if err != nil {
			return nil, err
		}
		passphrase, err := resolve(credentials.PassphraseRef)
		if err != nil {
			return nil, err
		}
		var spot exchange.SpotClient
		var perp exchange.PerpClient
		switch account.Venue + ":" + account.Product + ":" + account.Settlement {
		case "okx:spot:":
			spot, err = factory.New(factory.OKXSpotConfig(apiKey, apiSecret, passphrase, factoryOptions...))
		case "okx:perp:USDT":
			perp, err = factory.New(factory.OKXUSDTPerpConfig(apiKey, apiSecret, passphrase, factoryOptions...))
		case "bitget:spot:":
			spot, err = factory.New(factory.BitgetSpotConfig(apiKey, apiSecret, passphrase, factoryOptions...))
		case "bitget:perp:USDT":
			perp, err = factory.New(factory.BitgetUSDTPerpConfig(apiKey, apiSecret, passphrase, factoryOptions...))
		case "bitget:perp:USDC":
			perp, err = factory.New(factory.BitgetUSDCPerpConfig(apiKey, apiSecret, passphrase, factoryOptions...))
		}
		if err != nil {
			return nil, fmt.Errorf("construct configured client failed")
		}
		if spot != nil {
			return spotHandle{client: spot}, nil
		}
		if perp != nil {
			return perpHandle{client: perp}, nil
		}
	case config.LighterCredentials:
		privateKey, err := resolve(credentials.PrivateKeyRef)
		if err != nil {
			return nil, err
		}
		if account.Product == "spot" {
			client, err := factory.New(factory.LighterSpotConfig(privateKey, credentials.AccountIndex, uint8(credentials.KeyIndex), factoryOptions...))
			if err == nil {
				return spotHandle{client: client}, nil
			}
		} else if account.Product == "perp" && account.Settlement == "USDC" {
			client, err := factory.New(factory.LighterPerpConfig(privateKey, credentials.AccountIndex, uint8(credentials.KeyIndex), factoryOptions...))
			if err == nil {
				return perpHandle{client: client}, nil
			}
		}
		return nil, fmt.Errorf("construct configured client failed")
	case config.PrivateKeyCredentials:
		privateKey, err := resolve(credentials.PrivateKeyRef)
		if err != nil {
			return nil, err
		}
		if credentials.AccountAddress != "" {
			factoryOptions = append(factoryOptions, factory.WithAccountAddress(credentials.AccountAddress))
		}
		if account.Product == "spot" {
			client, err := factory.New(factory.HyperliquidSpotConfig(privateKey, factoryOptions...))
			if err == nil {
				return spotHandle{client: client}, nil
			}
		} else if account.Product == "perp" && account.Settlement == "USDC" {
			client, err := factory.New(factory.HyperliquidPerpConfig(privateKey, factoryOptions...))
			if err == nil {
				return perpHandle{client: client}, nil
			}
		}
		return nil, fmt.Errorf("construct configured client failed")
	case config.AsterCredentials:
		privateKey, err := resolve(credentials.APIWalletPrivateKeyRef)
		if err != nil {
			return nil, err
		}
		if account.Product == "spot" {
			client, err := factory.New(factory.AsterSpotConfig(credentials.UserAddress, privateKey, credentials.ExpectedSigner, factoryOptions...))
			if err == nil {
				return spotHandle{client: client}, nil
			}
		} else if account.Product == "perp" && account.Settlement == "USDT" {
			client, err := factory.New(factory.AsterUSDTPerpConfig(credentials.UserAddress, privateKey, credentials.ExpectedSigner, factoryOptions...))
			if err == nil {
				return perpHandle{client: client}, nil
			}
		}
		return nil, fmt.Errorf("construct configured client failed")
	case config.NadoCredentials:
		privateKey, err := resolve(credentials.PrivateKeyRef)
		if err != nil {
			return nil, err
		}
		if account.Product == "spot" && account.Settlement == "USDT0" {
			client, err := factory.New(factory.NadoSpotConfig(privateKey, credentials.Subaccount, factoryOptions...))
			if err == nil {
				return spotHandle{client: client}, nil
			}
		} else if account.Product == "perp" && account.Settlement == "USDT0" {
			client, err := factory.New(factory.NadoUSDT0PerpConfig(privateKey, credentials.Subaccount, factoryOptions...))
			if err == nil {
				return perpHandle{client: client}, nil
			}
		}
		return nil, fmt.Errorf("construct configured client failed")
	}
	return nil, fmt.Errorf("construct configured client failed")
}

// factoryEnvironment converts a stored venue_environment into the factory's
// typed one, and refuses anything that is not live.
//
// It used to have three arms. The remaining refusal is the point: an account row
// written before demo and testnet were removed still carries one of those
// strings, and this is where it stops rather than reaching a venue client that
// would happily talk to the real exchange with it.
func factoryEnvironment(value string) (factory.Environment, error) {
	if value != "live" {
		return "", fmt.Errorf("construct configured client failed")
	}
	return factory.EnvironmentLive, nil
}

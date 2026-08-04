package account

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/gateway/config"
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

func factoryEnvironment(value string) (factory.Environment, error) {
	switch value {
	case "live":
		return factory.EnvironmentLive, nil
	case "demo":
		return factory.EnvironmentDemo, nil
	case "testnet":
		return factory.EnvironmentTestnet, nil
	default:
		return "", fmt.Errorf("construct configured client failed")
	}
}

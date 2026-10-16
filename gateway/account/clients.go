package account

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
)

// routeClient is the authenticated client of one market of an account.
type routeClient struct {
	market venue.Market
	spot   exchange.SpotClient
	perp   exchange.PerpClient
}

func (client routeClient) orders() exchange.OrderREST {
	if client.perp != nil {
		return client.perp
	}
	return client.spot
}

func (client routeClient) socket() any {
	if client.perp != nil {
		return client.perp.WebSocket()
	}
	return client.spot.WebSocket()
}

func (client routeClient) close() {
	if client.perp != nil {
		_ = client.perp.Close()
	}
	if client.spot != nil {
		_ = client.spot.Close()
	}
}

// buildClients constructs one authenticated client per served market of the
// account's venue. Construction is local; no network I/O happens here.
func buildClients(accountID string, decoded *credential.Decoded, httpClient *http.Client) ([]routeClient, error) {
	// Each account gets its own endpoint budget key so one busy account cannot
	// starve another's writes; the venue-wide egress budget stays shared.
	hash := sha256.Sum256([]byte(accountID))
	scoped := ratelimit.WithPrincipal(httpClient, hex.EncodeToString(hash[:8]))
	options := []factory.Option{factory.WithEnvironment(factory.EnvironmentLive), factory.WithHTTPClient(scoped)}

	var clients []routeClient
	for _, market := range venue.MarketsOf(exchange.Venue(decoded.Venue)) {
		client := routeClient{market: market}
		var err error
		switch credentials := decoded.Credentials.(type) {
		case *credential.APIKeySecret:
			key, secret := credentials.APIKey, credentials.APISecret
			switch market.String() {
			case "binance:spot":
				client.spot, err = factory.New(factory.BinanceSpotConfig(key, secret, options...))
			case "binance:perp:USDT":
				client.perp, err = factory.New(factory.BinanceUSDPerpConfig(key, secret, options...))
			case "bybit:spot":
				client.spot, err = factory.New(factory.BybitSpotConfig(key, secret, options...))
			case "bybit:perp:USDT":
				client.perp, err = factory.New(factory.BybitUSDTPerpConfig(key, secret, options...))
			case "bybit:perp:USDC":
				client.perp, err = factory.New(factory.BybitUSDCPerpConfig(key, secret, options...))
			case "gate:spot":
				client.spot, err = factory.New(factory.GateSpotConfig(key, secret, options...))
			case "gate:perp:USDT":
				client.perp, err = factory.New(factory.GateUSDTPerpConfig(key, secret, options...))
			}
		case *credential.APIKeySecretPassphrase:
			key, secret, passphrase := credentials.APIKey, credentials.APISecret, credentials.Passphrase
			switch market.String() {
			case "okx:spot":
				client.spot, err = factory.New(factory.OKXSpotConfig(key, secret, passphrase, options...))
			case "okx:perp:USDT":
				client.perp, err = factory.New(factory.OKXUSDTPerpConfig(key, secret, passphrase, options...))
			case "bitget:spot":
				client.spot, err = factory.New(factory.BitgetSpotConfig(key, secret, passphrase, options...))
			case "bitget:perp:USDT":
				client.perp, err = factory.New(factory.BitgetUSDTPerpConfig(key, secret, passphrase, options...))
			case "bitget:perp:USDC":
				client.perp, err = factory.New(factory.BitgetUSDCPerpConfig(key, secret, passphrase, options...))
			}
		case *credential.LighterSigner:
			switch market.String() {
			case "lighter:spot":
				client.spot, err = factory.New(factory.LighterSpotConfig(credentials.PrivateKey, credentials.AccountIndex, credentials.KeyIndex, options...))
			case "lighter:perp:USDC":
				client.perp, err = factory.New(factory.LighterPerpConfig(credentials.PrivateKey, credentials.AccountIndex, credentials.KeyIndex, options...))
			}
		case *credential.EVMPrivateKey:
			hlOptions := options
			if credentials.AccountAddress != "" {
				hlOptions = append(append([]factory.Option(nil), options...), factory.WithAccountAddress(credentials.AccountAddress))
			}
			switch market.String() {
			case "hyperliquid:spot":
				client.spot, err = factory.New(factory.HyperliquidSpotConfig(credentials.PrivateKey, hlOptions...))
			case "hyperliquid:perp:USDC":
				client.perp, err = factory.New(factory.HyperliquidPerpConfig(credentials.PrivateKey, hlOptions...))
			}
		}
		if err != nil {
			for _, built := range clients {
				built.close()
			}
			return nil, fmt.Errorf("%s: %w", market, err)
		}
		if client.spot == nil && client.perp == nil {
			continue
		}
		clients = append(clients, client)
	}
	if len(clients) == 0 {
		return nil, fmt.Errorf("venue %s is not supported", decoded.Venue)
	}
	return clients, nil
}

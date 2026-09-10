package factory

import (
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

// Only implementation bindings live here. Venue/product/settlement facts come
// from routeprofile; executable SDK dispatch stays in the typed constructors.
var configProfiles = map[string]string{
	"binance-spot":     "binance.spot.v1",
	"binance-usd-perp": "binance.perp.usdt.v1",
	"okx-spot":         "okx.spot.v1",
	"okx-usdt-perp":    "okx.perp.usdt.v1",
	"lighter-spot":     "lighter.spot.v1",
	"lighter-perp":     "lighter.perp.usdc.v1",
	"hyperliquid-spot": "hyperliquid.spot.v1",
	"hyperliquid-perp": "hyperliquid.perp.usdc.v1",
	"bybit-spot":       "bybit.spot.v1",
	"bybit-usdt-perp":  "bybit.perp.usdt.v1",
	"bybit-usdc-perp":  "bybit.perp.usdc.v1",
	"bitget-spot":      "bitget.spot.v1",
	"bitget-usdt-perp": "bitget.perp.usdt.v1",
	"bitget-usdc-perp": "bitget.perp.usdc.v1",
	"gate-spot":        "gate.spot.v1",
	"gate-usdt-perp":   "gate.perp.usdt.v1",
	"aster-spot":       "aster.spot.v1",
	"aster-usdt-perp":  "aster.perp.usdt.v1",
	"nado-spot":        "nado.spot.usdt0.v1",
	"nado-usdt0-perp":  "nado.perp.usdt0.v1",
}

func catalogIdentityForConfig(name string) (exchange.Venue, exchange.Product, string, bool) {
	id, ok := configProfiles[name]
	if !ok {
		return "", "", "", false
	}
	profile, err := routeprofile.DefaultRegistry().Resolve(strings.SplitN(id, ".", 2)[0], id)
	if err != nil {
		return "", "", "", false
	}
	return exchange.Venue(profile.Venue), exchange.Product(profile.Product), profile.SettlementAsset, true
}

func publicConnectorConfigName(venue exchange.Venue, product exchange.Product, settlement string) (string, bool) {
	profile, err := routeprofile.DefaultRegistry().ResolveLegacy(string(venue), string(product), settlement)
	if err != nil {
		return "", false
	}
	for name, id := range configProfiles {
		if id == profile.ID {
			return name, true
		}
	}
	return "", false
}

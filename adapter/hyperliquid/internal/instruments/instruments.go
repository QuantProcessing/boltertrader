package instruments

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

const VenueName = "HYPERLIQUID"

const (
	spotAssetIDOffset = 10000
	hip3AssetIDOffset = 100000
	hip3DexIDStride   = 10000
	defaultSettle     = "USDC"
)

var ErrHIP3CollateralNotResolved = errors.New("hyperliquid hip3 collateral not resolved")

func BuildSpotInstruments(meta *sdkspot.SpotMeta) ([]*model.Instrument, error) {
	if meta == nil {
		return nil, fmt.Errorf("hyperliquid spot instruments: missing spot meta")
	}
	tokens := spotTokensByIndex(meta)
	insts := make([]*model.Instrument, 0, len(meta.Universe))
	for _, universe := range meta.Universe {
		if universe.Name == "" || len(universe.Tokens) < 2 {
			continue
		}
		baseToken, ok := tokens[universe.Tokens[0]]
		if !ok {
			return nil, fmt.Errorf("hyperliquid spot instruments: base token index %d not found for %s", universe.Tokens[0], universe.Name)
		}
		quoteToken, ok := tokens[universe.Tokens[1]]
		if !ok {
			return nil, fmt.Errorf("hyperliquid spot instruments: quote token index %d not found for %s", universe.Tokens[1], universe.Name)
		}
		assetID := spotAssetIDOffset + universe.Index
		insts = append(insts, &model.Instrument{
			ID: model.InstrumentID{
				Venue:  VenueName,
				Symbol: neutralSymbol(universe.Name),
				Kind:   enums.KindSpot,
			},
			Base:           baseToken.Name,
			Quote:          quoteToken.Name,
			Settle:         quoteToken.Name,
			VenueSymbol:    universe.Name,
			AssetIndex:     intPtr(assetID),
			SizeStep:       decimalStep(baseToken.SzDecimals),
			PositionMode:   model.NetOnly,
			MinQty:         decimal.Zero,
			MinNotional:    decimal.Zero,
			PriceTick:      decimal.Zero,
			PricePrecision: 0,
		})
	}
	return insts, nil
}

func BuildStandardPerpInstruments(meta *sdkperp.PrepMeta) ([]*model.Instrument, error) {
	if meta == nil {
		return nil, fmt.Errorf("hyperliquid perp instruments: missing meta")
	}
	insts := make([]*model.Instrument, 0, len(meta.Universe))
	for idx, universe := range meta.Universe {
		if universe.Name == "" {
			continue
		}
		insts = append(insts, &model.Instrument{
			ID: model.InstrumentID{
				Venue:  VenueName,
				Symbol: perpNeutralSymbol(universe.Name, defaultSettle),
				Kind:   enums.KindPerp,
			},
			Base:           universe.Name,
			Quote:          defaultSettle,
			Settle:         defaultSettle,
			VenueSymbol:    universe.Name,
			AssetIndex:     intPtr(idx),
			SizeStep:       decimalStep(universe.SzDecimals),
			PositionMode:   model.NetOnly,
			MinQty:         decimal.Zero,
			MinNotional:    decimal.Zero,
			PriceTick:      decimal.Zero,
			PricePrecision: 0,
		})
	}
	return insts, nil
}

func BuildHIP3PerpInstruments(dex sdkperp.PerpDex, meta *sdkperp.PrepMeta, spotMeta *sdkspot.SpotMeta) ([]*model.Instrument, error) {
	if dex.Name == "" {
		return nil, fmt.Errorf("hyperliquid hip3 instruments: missing dex name")
	}
	if meta == nil {
		return nil, fmt.Errorf("hyperliquid hip3 instruments: missing meta for dex %s", dex.Name)
	}
	collateral, err := resolveSpotTokenName(spotMeta, meta.CollateralToken)
	if err != nil {
		return nil, err
	}
	insts := make([]*model.Instrument, 0, len(meta.Universe))
	for idx, universe := range meta.Universe {
		if universe.Name == "" {
			continue
		}
		venueSymbol := hip3VenueSymbol(dex.Name, universe.Name)
		assetID := hip3AssetIDOffset + dex.Index*hip3DexIDStride + idx
		insts = append(insts, &model.Instrument{
			ID: model.InstrumentID{
				Venue:  VenueName,
				Symbol: hip3NeutralSymbol(venueSymbol, collateral),
				Kind:   enums.KindPerp,
			},
			Base:           venueSymbol,
			Quote:          collateral,
			Settle:         collateral,
			VenueSymbol:    venueSymbol,
			AssetIndex:     intPtr(assetID),
			SizeStep:       decimalStep(universe.SzDecimals),
			PositionMode:   model.NetOnly,
			MinQty:         decimal.Zero,
			MinNotional:    decimal.Zero,
			PriceTick:      decimal.Zero,
			PricePrecision: 0,
		})
	}
	return insts, nil
}

func hip3VenueSymbol(dexName string, coin string) string {
	prefix := dexName + ":"
	if len(coin) > len(prefix) && strings.EqualFold(coin[:len(prefix)], prefix) {
		return coin
	}
	return prefix + coin
}

func hip3NeutralSymbol(rawSymbol string, settle string) string {
	return sanitizeHIP3Symbol(rawSymbol) + "-" + settle
}

func sanitizeHIP3Symbol(value string) string {
	if !strings.ContainsAny(value, "*?") {
		return value
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if r == '*' || r == '?' {
			b.WriteByte('x')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func resolveSpotTokenName(meta *sdkspot.SpotMeta, tokenIndex int) (string, error) {
	if meta == nil {
		return "", fmt.Errorf("%w: missing spot meta", ErrHIP3CollateralNotResolved)
	}
	token, ok := spotTokensByIndex(meta)[tokenIndex]
	if !ok || token.Name == "" {
		return "", fmt.Errorf("%w: token index %d", ErrHIP3CollateralNotResolved, tokenIndex)
	}
	return token.Name, nil
}

type spotToken struct {
	Name       string
	SzDecimals int
}

func spotTokensByIndex(meta *sdkspot.SpotMeta) map[int]spotToken {
	tokens := make(map[int]spotToken, len(meta.Tokens))
	for _, token := range meta.Tokens {
		tokens[token.Index] = spotToken{Name: token.Name, SzDecimals: token.SzDecimals}
	}
	return tokens
}

func decimalStep(decimals int) decimal.Decimal {
	if decimals <= 0 {
		return decimal.NewFromInt(1)
	}
	return decimal.New(1, -int32(decimals))
}

func perpNeutralSymbol(base string, settle string) string {
	return neutralSymbol(base + "-" + settle)
}

func neutralSymbol(raw string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func intPtr(v int) *int {
	return &v
}

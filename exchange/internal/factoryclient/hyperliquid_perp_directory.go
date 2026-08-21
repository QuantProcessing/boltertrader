package factoryclient

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquidbase "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"golang.org/x/sync/errgroup"
)

const (
	hyperliquidBuilderAssetBase   = 100000
	hyperliquidBuilderAssetStride = 10000

	// Loading every official HIP-3 dex at once bursts the shared /info endpoint
	// hard enough that a single 429/transport reset can erase the entire perp
	// catalog. Catalog and funding bootstrap also run beside one another during
	// Gateway startup, so keep each caller deliberately small and retry the
	// individual public read instead of retrying the whole directory fan-out.
	hyperliquidDirectoryConcurrency = 2
	hyperliquidDirectoryAttempts    = 3
)

type hyperliquidPerpDexMetadata struct {
	index      int
	name       string
	prep       *hyperliquidperp.PrepMeta
	full       *hyperliquidperp.MetaAndAssetCtxsFull
	collateral string
}

type hyperliquidClassifications struct {
	categories  map[string]string
	annotations map[string]hyperliquidperp.PerpAnnotation
}

// hyperliquidPerpDirectory reads every official perp dex. allPerpMetas gives
// the stable dex-index ordering used by asset IDs; perpDexs supplies the names
// required to address HIP-3 metadata; spotMeta resolves each dex's official
// collateral token instead of assuming every builder settles in USDC.
func hyperliquidPerpDirectory(ctx context.Context, base *hyperliquidbase.Client, includeClassifications, includeAssetContexts bool) ([]hyperliquidPerpDexMetadata, hyperliquidClassifications, error) {
	client := hyperliquidperp.NewClient(base)
	allMetas, err := client.GetAllPerpMetas(ctx)
	if err != nil {
		return nil, hyperliquidClassifications{}, err
	}
	dexs, err := client.GetPerpDexs(ctx)
	if err != nil {
		return nil, hyperliquidClassifications{}, err
	}
	spotMeta, err := hyperliquidspot.NewClient(base).GetSpotMeta(ctx)
	if err != nil {
		return nil, hyperliquidClassifications{}, err
	}
	collaterals, err := hyperliquidCollateralSymbols(spotMeta)
	if err != nil {
		return nil, hyperliquidClassifications{}, err
	}

	dexNames := make(map[int]string, len(dexs))
	for _, dex := range dexs {
		if dex.Index <= 0 || strings.TrimSpace(dex.Name) == "" {
			continue
		}
		dexNames[dex.Index] = dex.Name
	}
	directory := make([]hyperliquidPerpDexMetadata, 0, len(allMetas))
	for index := range allMetas {
		prep := &allMetas[index]
		if len(prep.Universe) == 0 {
			continue
		}
		name := ""
		if index > 0 {
			var ok bool
			name, ok = dexNames[index]
			if !ok {
				return nil, hyperliquidClassifications{}, fmt.Errorf("missing perp dex name for index %d", index)
			}
		}
		collateral, ok := collaterals[prep.CollateralToken]
		if !ok || collateral == "" {
			return nil, hyperliquidClassifications{}, fmt.Errorf("unknown Hyperliquid collateral token %d", prep.CollateralToken)
		}
		directory = append(directory, hyperliquidPerpDexMetadata{index: index, name: name, prep: prep, collateral: collateral})
	}
	if len(directory) == 0 {
		return nil, hyperliquidClassifications{}, fmt.Errorf("Hyperliquid returned no perp metadata")
	}

	if includeAssetContexts {
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(hyperliquidDirectoryConcurrency)
		for index := range directory {
			row := &directory[index]
			group.Go(func() error {
				full, err := hyperliquidMetaAndAssetCtxs(groupCtx, client, row.name)
				if err != nil {
					return err
				}
				row.full = full
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return nil, hyperliquidClassifications{}, err
		}
	}

	// The two classification endpoints are independent enrichments. Neither is
	// allowed to make the tradable catalog disappear during a metadata outage.
	classifications := hyperliquidClassifications{}
	if includeClassifications {
		classifications.categories, _ = client.GetPerpCategories(ctx)
		classifications.annotations, _ = client.GetPerpConciseAnnotations(ctx)
	}
	return directory, classifications, nil
}

func hyperliquidMetaAndAssetCtxs(ctx context.Context, client *hyperliquidperp.Client, dex string) (*hyperliquidperp.MetaAndAssetCtxsFull, error) {
	var lastErr error
	for attempt := 0; attempt < hyperliquidDirectoryAttempts; attempt++ {
		full, err := client.GetMetaAndAssetCtxsForDex(ctx, dex)
		if err == nil {
			return full, nil
		}
		lastErr = err
		if attempt+1 == hyperliquidDirectoryAttempts {
			break
		}
		delay := time.Duration(attempt+1) * 100 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func hyperliquidCollateralSymbols(meta *hyperliquidspot.SpotMeta) (map[int]string, error) {
	if meta == nil {
		return nil, fmt.Errorf("missing Hyperliquid spot metadata")
	}
	result := make(map[int]string, len(meta.Tokens))
	for _, token := range meta.Tokens {
		name := strings.TrimSpace(token.Name)
		if token.Index < 0 || name == "" {
			return nil, fmt.Errorf("invalid Hyperliquid collateral token metadata")
		}
		if _, exists := result[token.Index]; exists {
			return nil, fmt.Errorf("duplicate Hyperliquid collateral token %d", token.Index)
		}
		result[token.Index] = name
	}
	return result, nil
}

func hyperliquidAssetID(dexIndex, marketIndex int) int {
	if dexIndex == 0 {
		return marketIndex
	}
	return hyperliquidBuilderAssetBase + dexIndex*hyperliquidBuilderAssetStride + marketIndex
}

func hyperliquidUnderlying(coin string) string {
	if _, suffix, ok := strings.Cut(coin, ":"); ok && suffix != "" {
		return suffix
	}
	return coin
}

// hyperliquidValidPerpCoin distinguishes official HIP-3 namespaces from the
// colon-bearing non-portable rows the main dex has always rejected. A builder
// market is accepted only when its prefix exactly matches the dex requested;
// stripping or accepting a foreign prefix would make two deployers collide.
func hyperliquidValidPerpCoin(coin, dex string) bool {
	if coin == "" || strings.TrimSpace(coin) != coin || strings.HasPrefix(coin, "#") || strings.HasPrefix(coin, "+") {
		return false
	}
	if dex == "" {
		return !strings.Contains(coin, ":")
	}
	prefix, suffix, ok := strings.Cut(coin, ":")
	return ok && prefix == dex && suffix != "" && strings.TrimSpace(suffix) == suffix && !strings.Contains(suffix, ":")
}

func applyHyperliquidClassification(instrument *exchange.Instrument, coin string, classification hyperliquidClassifications) {
	if instrument == nil {
		return
	}
	category, source := classification.categories[coin], "hyperliquid.perpCategories"
	if strings.TrimSpace(category) == "" {
		category = classification.annotations[coin].Category
		source = "hyperliquid.perpConciseAnnotations"
	}
	category = strings.TrimSpace(category)
	if category == "" {
		return
	}
	instrument.Metadata.ClassificationSource = source
	instrument.Metadata.RawCategories = exchange.NewInstrumentCategories([]string{category})
	instrument.Metadata.AssetClass = hyperliquidAssetClass(category)
}

func hyperliquidAssetClass(raw string) exchange.AssetClass {
	category := strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(strings.TrimSpace(raw)))
	switch category {
	case "crypto", "cryptocurrency", "cryptocurrencies":
		return exchange.AssetClassCrypto
	case "stock", "stocks", "equity", "equities", "etf", "etfs":
		return exchange.AssetClassEquity
	case "commodity", "commodities":
		return exchange.AssetClassCommodity
	case "index", "indices":
		return exchange.AssetClassIndex
	case "fx", "forex":
		return exchange.AssetClassFX
	case "rate", "rates", "bond", "bonds":
		return exchange.AssetClassRates
	case "preipo", "pre_ipo":
		return exchange.AssetClassPreIPO
	case "rwa", "real_world_asset", "real_world_assets":
		return exchange.AssetClassRWAOther
	default:
		return exchange.AssetClassUnknown
	}
}

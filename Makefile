.PHONY: test test-race test-core test-adapter test-sdk test-capabilities test-p6-offline test-live-read test-demo-acceptance test-binance-demo test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-spot-data test-binance-demo-spot test-binance-demo-runtime-spot test-binance-demo-acceptance test-okx-demo test-okx-demo-spot test-okx-demo-runtime-spot test-okx-demo-perp test-okx-demo-runtime-perp test-okx-demo-acceptance test-hyperliquid-testnet test-hyperliquid-testnet-spot-read test-hyperliquid-testnet-spot test-hyperliquid-testnet-runtime-spot test-hyperliquid-testnet-perp-read test-hyperliquid-testnet-perp test-hyperliquid-testnet-runtime-perp test-hyperliquid-testnet-hip3 test-hyperliquid-testnet-runtime-hip3 test-hyperliquid-testnet-acceptance test-lighter-testnet test-lighter-testnet-read test-lighter-testnet-spot test-lighter-testnet-runtime-spot test-lighter-testnet-perp test-lighter-testnet-runtime-perp test-lighter-testnet-acceptance

test:
	go test ./...

test-race:
	go test -race ./runtime/...

test-core:
	go test ./core/... ./runtime/... ./strategy/...

test-adapter:
	go test ./adapter/...

test-sdk:
	go test ./sdk/...

test-capabilities:
	go test ./adapter -count=1
	go test ./adapter/... -run Capabilit -count=1

test-p6-offline: test-core test-adapter test-sdk test-capabilities

test-live-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go test ./sdk/... ./adapter/...

test-demo-acceptance: test-binance-demo-acceptance test-okx-demo-acceptance

test-binance-demo: test-binance-demo-acceptance

test-binance-demo-perp:
	go test -run TestBinanceDemoExecAcceptance ./adapter/binance/perp/ -count=1 -timeout=3m

test-binance-demo-runtime-perp:
	go test -run TestBinanceDemoRuntimeAcceptance ./adapter/binance/perp/ -count=1 -timeout=3m

test-binance-demo-spot-data:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go test -run TestBinanceSpotDemoDataAcceptance ./adapter/binance/spot/ -count=1 -timeout=2m

test-binance-demo-spot:
	go test -run TestBinanceSpotDemoExecAcceptance ./adapter/binance/spot/ -count=1 -timeout=3m

test-binance-demo-runtime-spot:
	go test -run TestBinanceSpotDemoRuntimeAcceptance ./adapter/binance/spot/ -count=1 -timeout=3m

test-binance-demo-acceptance: test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-spot-data test-binance-demo-spot test-binance-demo-runtime-spot

test-okx-demo: test-okx-demo-acceptance

test-okx-demo-spot:
	go test -run TestOKXSpotDemoExecAcceptance ./adapter/okx/spot/ -count=1 -timeout=3m

test-okx-demo-runtime-spot:
	go test -run TestOKXSpotDemoRuntimeAcceptance ./adapter/okx/spot/ -count=1 -timeout=3m

test-okx-demo-perp:
	go test -run TestOKXPerpDemoExecAcceptance ./adapter/okx/perp/ -count=1 -timeout=3m

test-okx-demo-runtime-perp:
	go test -run TestOKXPerpDemoRuntimeAcceptance ./adapter/okx/perp/ -count=1 -timeout=3m

test-okx-demo-acceptance: test-okx-demo-spot test-okx-demo-runtime-spot test-okx-demo-perp test-okx-demo-runtime-perp

test-hyperliquid-testnet: test-hyperliquid-testnet-acceptance

test-hyperliquid-testnet-spot-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestHyperliquidSpotTestnetReadAcceptance ./adapter/hyperliquid/spot/ -count=1 -timeout=3m

test-hyperliquid-testnet-spot:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestHyperliquidSpotTestnetWriteAcceptance ./adapter/hyperliquid/spot/ -count=1 -timeout=3m

test-hyperliquid-testnet-runtime-spot:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestHyperliquidSpotTestnetRuntimeAcceptance ./adapter/hyperliquid/spot/ -count=1 -timeout=4m

test-hyperliquid-testnet-perp-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestHyperliquidPerpTestnetReadAcceptance ./adapter/hyperliquid/perp/ -count=1 -timeout=3m

test-hyperliquid-testnet-perp:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestHyperliquidPerpTestnetWriteAcceptance ./adapter/hyperliquid/perp/ -count=1 -timeout=3m

test-hyperliquid-testnet-runtime-perp:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestHyperliquidPerpTestnetRuntimeAcceptance ./adapter/hyperliquid/perp/ -count=1 -timeout=4m

test-hyperliquid-testnet-hip3:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestHyperliquidPerpTestnetHIP3ReadAcceptance ./adapter/hyperliquid/perp/ -count=1 -timeout=3m

test-hyperliquid-testnet-runtime-hip3:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestHyperliquidPerpTestnetHIP3RuntimeAcceptance ./adapter/hyperliquid/perp/ -count=1 -timeout=4m

test-hyperliquid-testnet-acceptance: test-hyperliquid-testnet-spot-read test-hyperliquid-testnet-perp-read test-hyperliquid-testnet-hip3 test-hyperliquid-testnet-spot test-hyperliquid-testnet-runtime-spot test-hyperliquid-testnet-perp test-hyperliquid-testnet-runtime-perp test-hyperliquid-testnet-runtime-hip3

test-lighter-testnet: test-lighter-testnet-acceptance

test-lighter-testnet-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestLighterTestnetReadAcceptance ./adapter/lighter/ -count=1 -timeout=3m

test-lighter-testnet-spot:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestLighterTestnetSpotWriteAcceptance ./adapter/lighter/ -count=1 -timeout=3m

test-lighter-testnet-runtime-spot:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestLighterTestnetSpotRuntimeAcceptance ./adapter/lighter/ -count=1 -timeout=4m

test-lighter-testnet-perp:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestLighterTestnetPerpWriteAcceptance ./adapter/lighter/ -count=1 -timeout=3m

test-lighter-testnet-runtime-perp:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run TestLighterTestnetPerpRuntimeAcceptance ./adapter/lighter/ -count=1 -timeout=4m

test-lighter-testnet-acceptance: test-lighter-testnet-read test-lighter-testnet-spot test-lighter-testnet-runtime-spot test-lighter-testnet-perp test-lighter-testnet-runtime-perp

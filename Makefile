.PHONY: test test-race test-core test-adapter test-sdk test-capabilities test-p6-offline test-live-read test-demo-acceptance test-binance-demo test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-spot-data test-binance-demo-spot test-binance-demo-runtime-spot test-binance-demo-acceptance test-okx-demo test-okx-demo-spot test-okx-demo-runtime-spot test-okx-demo-perp test-okx-demo-runtime-perp test-okx-demo-acceptance test-hyperliquid-testnet test-hyperliquid-testnet-spot-read test-hyperliquid-testnet-spot test-hyperliquid-testnet-runtime-spot test-hyperliquid-testnet-perp-read test-hyperliquid-testnet-perp test-hyperliquid-testnet-runtime-perp test-hyperliquid-testnet-hip3 test-hyperliquid-testnet-runtime-hip3 test-hyperliquid-testnet-acceptance test-lighter-testnet test-lighter-testnet-read test-lighter-testnet-spot test-lighter-testnet-runtime-spot test-lighter-testnet-perp test-lighter-testnet-runtime-perp test-lighter-testnet-acceptance
.PHONY: test-bybit-demo test-bybit-demo-spot test-bybit-demo-runtime-spot test-bybit-demo-usdt-perp test-bybit-demo-runtime-usdt-perp test-bybit-demo-usdc-perp test-bybit-demo-runtime-usdc-perp test-bybit-demo-acceptance test-bybit-spot-acceptance test-bybit-usdt-perp-acceptance test-bybit-usdc-perp-acceptance test-bybit-acceptance
.PHONY: test-bitget-demo test-bitget-demo-spot test-bitget-demo-runtime-spot test-bitget-demo-usdt-perp test-bitget-demo-runtime-usdt-perp test-bitget-demo-usdc-perp test-bitget-demo-runtime-usdc-perp test-bitget-demo-acceptance test-bitget-testnet test-bitget-testnet-spot test-bitget-testnet-runtime-spot test-bitget-testnet-usdt-perp test-bitget-testnet-runtime-usdt-perp test-bitget-testnet-usdc-perp test-bitget-testnet-runtime-usdc-perp test-bitget-testnet-acceptance test-bitget-spot-acceptance test-bitget-usdt-perp-acceptance test-bitget-usdc-perp-acceptance test-bitget-acceptance test-bybit-bitget-acceptance
.PHONY: test-gate-testnet test-gate-testnet-read test-gate-testnet-spot test-gate-testnet-runtime-spot test-gate-testnet-usdt-perp test-gate-testnet-runtime-usdt-perp test-gate-testnet-usdc-perp-deferred test-gate-testnet-acceptance test-gate-spot-acceptance test-gate-usdt-perp-acceptance test-gate-acceptance

test:
	go test -short ./...

test-race:
	go test -race ./runtime/...

test-core:
	go test ./core/... ./runtime/... ./strategy/...

test-adapter:
	go test -short ./adapter/...

test-sdk:
	go test -short ./sdk/...

test-capabilities:
	go test -short ./adapter -count=1
	go test -short ./adapter/... -run Capabilit -count=1

test-p6-offline: test-core test-adapter test-sdk test-capabilities

test-live-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go test ./sdk/... ./adapter/...

test-demo-acceptance: test-binance-demo-acceptance test-okx-demo-acceptance test-bybit-acceptance test-bitget-acceptance

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

test-bybit-demo: test-bybit-demo-acceptance

test-bybit-demo-spot:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoSpotAcceptance$$' ./adapter/bybit/ -count=1 -timeout=3m

test-bybit-demo-runtime-spot:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoSpotRuntimeAcceptance$$' ./adapter/bybit/ -count=1 -timeout=3m

test-bybit-demo-usdt-perp:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDTPerpAcceptance$$' ./adapter/bybit/ -count=1 -timeout=3m

test-bybit-demo-runtime-usdt-perp:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDTPerpRuntimeAcceptance$$' ./adapter/bybit/ -count=1 -timeout=3m

test-bybit-demo-usdc-perp:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDCPerpAcceptance$$' ./adapter/bybit/ -count=1 -timeout=3m

test-bybit-demo-runtime-usdc-perp:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDCPerpRuntimeAcceptance$$' ./adapter/bybit/ -count=1 -timeout=3m

test-bybit-demo-acceptance: test-bybit-demo-spot test-bybit-demo-runtime-spot test-bybit-demo-usdt-perp test-bybit-demo-runtime-usdt-perp test-bybit-demo-usdc-perp test-bybit-demo-runtime-usdc-perp

test-bybit-spot-acceptance: test-bybit-demo-spot test-bybit-demo-runtime-spot

test-bybit-usdt-perp-acceptance: test-bybit-demo-usdt-perp test-bybit-demo-runtime-usdt-perp

test-bybit-usdc-perp-acceptance: test-bybit-demo-usdc-perp test-bybit-demo-runtime-usdc-perp

test-bybit-acceptance: test-bybit-spot-acceptance test-bybit-usdt-perp-acceptance test-bybit-usdc-perp-acceptance

test-bitget-demo: test-bitget-demo-acceptance

test-bitget-demo-spot:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoSpotAcceptance$$' ./adapter/bitget/ -count=1 -timeout=3m

test-bitget-demo-runtime-spot:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoSpotRuntimeAcceptance$$' ./adapter/bitget/ -count=1 -timeout=3m

test-bitget-demo-usdt-perp:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDTPerpAcceptance$$' ./adapter/bitget/ -count=1 -timeout=3m

test-bitget-demo-runtime-usdt-perp:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDTPerpRuntimeAcceptance$$' ./adapter/bitget/ -count=1 -timeout=3m

test-bitget-demo-usdc-perp:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDCPerpAcceptance$$' ./adapter/bitget/ -count=1 -timeout=3m

test-bitget-demo-runtime-usdc-perp:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDCPerpRuntimeAcceptance$$' ./adapter/bitget/ -count=1 -timeout=3m

test-bitget-demo-acceptance: test-bitget-demo-spot test-bitget-demo-runtime-spot test-bitget-demo-usdt-perp test-bitget-demo-runtime-usdt-perp test-bitget-demo-usdc-perp test-bitget-demo-runtime-usdc-perp

test-bitget-testnet: test-bitget-demo

test-bitget-testnet-spot: test-bitget-demo-spot

test-bitget-testnet-runtime-spot: test-bitget-demo-runtime-spot

test-bitget-testnet-usdt-perp: test-bitget-demo-usdt-perp

test-bitget-testnet-runtime-usdt-perp: test-bitget-demo-runtime-usdt-perp

test-bitget-testnet-usdc-perp: test-bitget-demo-usdc-perp

test-bitget-testnet-runtime-usdc-perp: test-bitget-demo-runtime-usdc-perp

test-bitget-testnet-acceptance: test-bitget-demo-acceptance

test-bitget-spot-acceptance: test-bitget-demo-spot test-bitget-demo-runtime-spot

test-bitget-usdt-perp-acceptance: test-bitget-demo-usdt-perp test-bitget-demo-runtime-usdt-perp

test-bitget-usdc-perp-acceptance: test-bitget-demo-usdc-perp test-bitget-demo-runtime-usdc-perp

test-bitget-acceptance: test-bitget-spot-acceptance test-bitget-usdt-perp-acceptance test-bitget-usdc-perp-acceptance

test-bybit-bitget-acceptance: test-bybit-acceptance test-bitget-acceptance

test-gate-testnet: test-gate-testnet-acceptance

test-gate-testnet-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetReadAcceptance$$' ./adapter/gate/ -count=1 -timeout=3m

test-gate-testnet-spot:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetSpotAcceptance$$' ./adapter/gate/ -count=1 -timeout=3m

test-gate-testnet-runtime-spot:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetSpotRuntimeAcceptance$$' ./adapter/gate/ -count=1 -timeout=3m

test-gate-testnet-usdt-perp:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetUSDTPerpAcceptance$$' ./adapter/gate/ -count=1 -timeout=3m

test-gate-testnet-runtime-usdt-perp:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetUSDTPerpRuntimeAcceptance$$' ./adapter/gate/ -count=1 -timeout=3m

test-gate-testnet-usdc-perp-deferred:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetUSDCPerpDeferredCapability$$' ./adapter/gate/ -count=1 -timeout=2m

test-gate-testnet-acceptance: test-gate-testnet-read test-gate-testnet-spot test-gate-testnet-runtime-spot test-gate-testnet-usdt-perp test-gate-testnet-runtime-usdt-perp test-gate-testnet-usdc-perp-deferred

test-gate-spot-acceptance: test-gate-testnet-spot test-gate-testnet-runtime-spot

test-gate-usdt-perp-acceptance: test-gate-testnet-usdt-perp test-gate-testnet-runtime-usdt-perp

test-gate-acceptance: test-gate-testnet-acceptance

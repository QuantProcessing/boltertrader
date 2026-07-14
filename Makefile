.PHONY: test test-race test-core test-adapter test-sdk test-capabilities test-p6-offline test-live-read test-demo-acceptance test-binance-demo test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-spot-data test-binance-demo-spot test-binance-demo-runtime-spot test-binance-demo-acceptance test-okx-demo test-okx-demo-spot test-okx-demo-runtime-spot test-okx-demo-perp test-okx-demo-runtime-perp test-okx-demo-acceptance test-hyperliquid-testnet test-hyperliquid-testnet-spot-read test-hyperliquid-testnet-spot test-hyperliquid-testnet-runtime-spot test-hyperliquid-testnet-perp-read test-hyperliquid-testnet-perp test-hyperliquid-testnet-runtime-perp test-hyperliquid-testnet-hip3 test-hyperliquid-testnet-hip3-write test-hyperliquid-testnet-runtime-hip3 test-hyperliquid-testnet-acceptance test-lighter-testnet test-lighter-testnet-read test-lighter-testnet-spot test-lighter-testnet-runtime-spot test-lighter-testnet-perp test-lighter-testnet-runtime-perp test-lighter-testnet-acceptance
.PHONY: test-bybit-demo test-bybit-demo-spot test-bybit-demo-runtime-spot test-bybit-demo-usdt-perp test-bybit-demo-runtime-usdt-perp test-bybit-demo-usdc-perp test-bybit-demo-runtime-usdc-perp test-bybit-demo-acceptance test-bybit-spot-acceptance test-bybit-usdt-perp-acceptance test-bybit-usdc-perp-acceptance test-bybit-acceptance
.PHONY: test-bitget-demo test-bitget-demo-spot test-bitget-demo-runtime-spot test-bitget-demo-usdt-perp test-bitget-demo-runtime-usdt-perp test-bitget-demo-usdc-perp test-bitget-demo-runtime-usdc-perp test-bitget-demo-acceptance test-bitget-testnet test-bitget-testnet-spot test-bitget-testnet-runtime-spot test-bitget-testnet-usdt-perp test-bitget-testnet-runtime-usdt-perp test-bitget-testnet-usdc-perp test-bitget-testnet-runtime-usdc-perp test-bitget-testnet-acceptance test-bitget-spot-acceptance test-bitget-usdt-perp-acceptance test-bitget-usdc-perp-acceptance test-bitget-acceptance test-bybit-bitget-acceptance
.PHONY: test-gate-testnet test-gate-testnet-read test-gate-testnet-spot test-gate-testnet-runtime-spot test-gate-testnet-usdt-perp test-gate-testnet-runtime-usdt-perp test-gate-testnet-usdc-perp-deferred test-gate-testnet-acceptance test-gate-spot-acceptance test-gate-usdt-perp-acceptance test-gate-acceptance
.PHONY: test-reference-data-offline test-reference-data-read test-binance-demo-reference-data-read test-okx-demo-reference-data-read test-bybit-demo-reference-data-read test-bitget-demo-reference-data-read test-gate-testnet-reference-data-read test-hyperliquid-testnet-reference-data-read test-lighter-testnet-reference-data-read test-aster-testnet-reference-data-read test-nado-testnet-reference-data-read
.PHONY: test-aster-testnet test-aster-testnet-read test-aster-testnet-spot-read test-aster-testnet-perp-read test-aster-testnet-spot test-aster-testnet-runtime-spot test-aster-testnet-perp test-aster-testnet-runtime-perp test-aster-testnet-acceptance
.PHONY: test-nado-testnet test-nado-testnet-read test-nado-testnet-spot-read test-nado-testnet-perp-read test-nado-testnet-spot test-nado-testnet-runtime-spot test-nado-testnet-perp test-nado-testnet-runtime-perp test-nado-testnet-acceptance test-aster-nado-testnet-acceptance

.NOTPARALLEL:

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

test-p6-offline: test-core test-adapter test-sdk test-capabilities test-reference-data-offline

test-reference-data-offline:
	go test -short ./core/model ./core/contract ./runtime/cache ./runtime ./runtime/runtimetest ./adapter/internal/runtimeaccept ./adapter/binance/perp ./adapter/okx/perp ./adapter/bybit ./adapter/bitget ./adapter/gate ./adapter/hyperliquid/perp ./adapter/lighter ./adapter/aster/perp ./adapter/nado ./sdk/nado -run 'Reference|OpenInterest|Capabilit' -count=1

test-live-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go test ./sdk/... ./adapter/...

test-reference-data-read: test-binance-demo-reference-data-read test-okx-demo-reference-data-read test-bybit-demo-reference-data-read test-bitget-demo-reference-data-read test-gate-testnet-reference-data-read test-hyperliquid-testnet-reference-data-read test-lighter-testnet-reference-data-read test-aster-testnet-reference-data-read test-nado-testnet-reference-data-read

test-binance-demo-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceDemoReferenceDataReadAcceptance$$' ./adapter/binance/perp/ -count=1 -timeout=3m

test-okx-demo-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXPerpDemoReferenceDataReadAcceptance$$' ./adapter/okx/perp/ -count=1 -timeout=3m

test-bybit-demo-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoReferenceDataReadAcceptance$$' ./adapter/bybit/ -count=1 -timeout=4m

test-bitget-demo-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoReferenceDataReadAcceptance$$' ./adapter/bitget/ -count=1 -timeout=4m

test-gate-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetReferenceDataReadAcceptance$$' ./adapter/gate/ -count=1 -timeout=3m

test-hyperliquid-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetReferenceDataReadAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=4m

test-lighter-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetReferenceDataReadAcceptance$$' ./adapter/lighter/ -count=1 -timeout=3m

test-aster-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterPerpTestnetReferenceDataReadAcceptance$$' ./adapter/aster/perp/ -count=1 -timeout=3m

test-nado-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoTestnetReferenceDataReadAcceptance$$' ./adapter/nado/ -count=1 -timeout=3m

test-aster-testnet: test-aster-testnet-acceptance

test-aster-testnet-read: test-aster-testnet-spot-read test-aster-testnet-perp-read test-aster-testnet-reference-data-read

test-aster-testnet-spot-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterSpotTestnetReadAcceptance$$' ./adapter/aster/spot/ -count=1 -timeout=3m

test-aster-testnet-perp-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterPerpTestnetReadAcceptance$$' ./adapter/aster/perp/ -count=1 -timeout=3m

test-aster-testnet-spot:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterSpotTestnetAdapterAcceptance$$' ./adapter/aster/spot/ -count=1 -timeout=5m

test-aster-testnet-runtime-spot:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterSpotTestnetRuntimeAcceptance$$' ./adapter/aster/spot/ -count=1 -timeout=6m

test-aster-testnet-perp:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterPerpTestnetAdapterAcceptance$$' ./adapter/aster/perp/ -count=1 -timeout=5m

test-aster-testnet-runtime-perp:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterPerpTestnetRuntimeAcceptance$$' ./adapter/aster/perp/ -count=1 -timeout=6m

test-aster-testnet-acceptance: test-aster-testnet-read test-aster-testnet-spot test-aster-testnet-runtime-spot test-aster-testnet-perp test-aster-testnet-runtime-perp

test-nado-testnet: test-nado-testnet-acceptance

test-nado-testnet-read: test-nado-testnet-spot-read test-nado-testnet-perp-read test-nado-testnet-reference-data-read

test-nado-testnet-spot-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoSpotTestnetReadAcceptance$$' ./adapter/nado/ -count=1 -timeout=3m

test-nado-testnet-perp-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoPerpTestnetReadAcceptance$$' ./adapter/nado/ -count=1 -timeout=3m

test-nado-testnet-spot:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoSpotTestnetAdapterAcceptance$$' ./adapter/nado/ -count=1 -timeout=5m

test-nado-testnet-runtime-spot:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoSpotTestnetRuntimeAcceptance$$' ./adapter/nado/ -count=1 -timeout=6m

test-nado-testnet-perp:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoPerpTestnetAdapterAcceptance$$' ./adapter/nado/ -count=1 -timeout=5m

test-nado-testnet-runtime-perp:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoPerpTestnetRuntimeAcceptance$$' ./adapter/nado/ -count=1 -timeout=6m

test-nado-testnet-acceptance: test-nado-testnet-read test-nado-testnet-spot test-nado-testnet-runtime-spot test-nado-testnet-perp test-nado-testnet-runtime-perp

test-aster-nado-testnet-acceptance: test-aster-testnet-acceptance test-nado-testnet-acceptance

test-demo-acceptance: test-binance-demo-acceptance test-okx-demo-acceptance test-bybit-acceptance test-bitget-acceptance

test-binance-demo: test-binance-demo-acceptance

test-binance-demo-perp:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceDemoExecAcceptance$$' ./adapter/binance/perp/ -count=1 -timeout=5m

test-binance-demo-runtime-perp:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceDemoRuntimeAcceptance$$' ./adapter/binance/perp/ -count=1 -timeout=6m

test-binance-demo-spot-data:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceSpotDemoDataAcceptance$$' ./adapter/binance/spot/ -count=1 -timeout=2m

test-binance-demo-spot:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceSpotDemoExecAcceptance$$' ./adapter/binance/spot/ -count=1 -timeout=5m

test-binance-demo-runtime-spot:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceSpotDemoRuntimeAcceptance$$' ./adapter/binance/spot/ -count=1 -timeout=6m

test-binance-demo-acceptance: test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-spot-data test-binance-demo-spot test-binance-demo-runtime-spot

test-okx-demo: test-okx-demo-acceptance

test-okx-demo-spot:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXSpotDemoExecAcceptance$$' ./adapter/okx/spot/ -count=1 -timeout=5m

test-okx-demo-runtime-spot:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXSpotDemoRuntimeAcceptance$$' ./adapter/okx/spot/ -count=1 -timeout=6m

test-okx-demo-perp:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXPerpDemoExecAcceptance$$' ./adapter/okx/perp/ -count=1 -timeout=5m

test-okx-demo-runtime-perp:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXPerpDemoRuntimeAcceptance$$' ./adapter/okx/perp/ -count=1 -timeout=6m

test-okx-demo-acceptance: test-okx-demo-spot test-okx-demo-runtime-spot test-okx-demo-perp test-okx-demo-runtime-perp

test-hyperliquid-testnet: test-hyperliquid-testnet-acceptance

test-hyperliquid-testnet-spot-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidSpotTestnetReadAcceptance$$' ./adapter/hyperliquid/spot/ -count=1 -timeout=3m

test-hyperliquid-testnet-spot:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidSpotTestnetWriteAcceptance$$' ./adapter/hyperliquid/spot/ -count=1 -timeout=5m

test-hyperliquid-testnet-runtime-spot:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidSpotTestnetRuntimeAcceptance$$' ./adapter/hyperliquid/spot/ -count=1 -timeout=6m

test-hyperliquid-testnet-perp-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetReadAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=3m

test-hyperliquid-testnet-perp:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetWriteAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=5m

test-hyperliquid-testnet-runtime-perp:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetRuntimeAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=6m

test-hyperliquid-testnet-hip3:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetHIP3ReadAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=3m

test-hyperliquid-testnet-hip3-write:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetHIP3WriteAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=5m

test-hyperliquid-testnet-runtime-hip3:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetHIP3RuntimeAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=6m

test-hyperliquid-testnet-acceptance: test-hyperliquid-testnet-spot-read test-hyperliquid-testnet-perp-read test-hyperliquid-testnet-hip3 test-hyperliquid-testnet-spot test-hyperliquid-testnet-runtime-spot test-hyperliquid-testnet-perp test-hyperliquid-testnet-runtime-perp test-hyperliquid-testnet-hip3-write test-hyperliquid-testnet-runtime-hip3

test-lighter-testnet: test-lighter-testnet-acceptance

test-lighter-testnet-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetReadAcceptance$$' ./adapter/lighter/ -count=1 -timeout=3m

test-lighter-testnet-spot:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetSpotWriteAcceptance$$' ./adapter/lighter/ -count=1 -timeout=5m

test-lighter-testnet-runtime-spot:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetSpotRuntimeAcceptance$$' ./adapter/lighter/ -count=1 -timeout=6m

test-lighter-testnet-perp:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetPerpWriteAcceptance$$' ./adapter/lighter/ -count=1 -timeout=5m

test-lighter-testnet-runtime-perp:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetPerpRuntimeAcceptance$$' ./adapter/lighter/ -count=1 -timeout=6m

test-lighter-testnet-acceptance: test-lighter-testnet-read test-lighter-testnet-spot test-lighter-testnet-runtime-spot test-lighter-testnet-perp test-lighter-testnet-runtime-perp

test-bybit-demo: test-bybit-demo-acceptance

test-bybit-demo-spot:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoSpotAcceptance$$' ./adapter/bybit/ -count=1 -timeout=5m

test-bybit-demo-runtime-spot:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoSpotRuntimeAcceptance$$' ./adapter/bybit/ -count=1 -timeout=6m

test-bybit-demo-usdt-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDTPerpAcceptance$$' ./adapter/bybit/ -count=1 -timeout=5m

test-bybit-demo-runtime-usdt-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDTPerpRuntimeAcceptance$$' ./adapter/bybit/ -count=1 -timeout=6m

test-bybit-demo-usdc-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDCPerpAcceptance$$' ./adapter/bybit/ -count=1 -timeout=5m

test-bybit-demo-runtime-usdc-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDCPerpRuntimeAcceptance$$' ./adapter/bybit/ -count=1 -timeout=6m

test-bybit-demo-acceptance: test-bybit-demo-spot test-bybit-demo-runtime-spot test-bybit-demo-usdt-perp test-bybit-demo-runtime-usdt-perp test-bybit-demo-usdc-perp test-bybit-demo-runtime-usdc-perp

test-bybit-spot-acceptance: test-bybit-demo-spot test-bybit-demo-runtime-spot

test-bybit-usdt-perp-acceptance: test-bybit-demo-usdt-perp test-bybit-demo-runtime-usdt-perp

test-bybit-usdc-perp-acceptance: test-bybit-demo-usdc-perp test-bybit-demo-runtime-usdc-perp

test-bybit-acceptance: test-bybit-spot-acceptance test-bybit-usdt-perp-acceptance test-bybit-usdc-perp-acceptance

test-bitget-demo: test-bitget-demo-acceptance

test-bitget-demo-spot:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoSpotAcceptance$$' ./adapter/bitget/ -count=1 -timeout=5m

test-bitget-demo-runtime-spot:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoSpotRuntimeAcceptance$$' ./adapter/bitget/ -count=1 -timeout=6m

test-bitget-demo-usdt-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDTPerpAcceptance$$' ./adapter/bitget/ -count=1 -timeout=5m

test-bitget-demo-runtime-usdt-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDTPerpRuntimeAcceptance$$' ./adapter/bitget/ -count=1 -timeout=6m

test-bitget-demo-usdc-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDCPerpAcceptance$$' ./adapter/bitget/ -count=1 -timeout=5m

test-bitget-demo-runtime-usdc-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDCPerpRuntimeAcceptance$$' ./adapter/bitget/ -count=1 -timeout=6m

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
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetSpotAcceptance$$' ./adapter/gate/ -count=1 -timeout=5m

test-gate-testnet-runtime-spot:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetSpotRuntimeAcceptance$$' ./adapter/gate/ -count=1 -timeout=6m

test-gate-testnet-usdt-perp:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetUSDTPerpAcceptance$$' ./adapter/gate/ -count=1 -timeout=5m

test-gate-testnet-runtime-usdt-perp:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetUSDTPerpRuntimeAcceptance$$' ./adapter/gate/ -count=1 -timeout=6m

test-gate-testnet-usdc-perp-deferred:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetUSDCPerpDeferredCapability$$' ./adapter/gate/ -count=1 -timeout=2m

test-gate-testnet-acceptance: test-gate-testnet-read test-gate-testnet-spot test-gate-testnet-runtime-spot test-gate-testnet-usdt-perp test-gate-testnet-runtime-usdt-perp test-gate-testnet-usdc-perp-deferred

test-gate-spot-acceptance: test-gate-testnet-spot test-gate-testnet-runtime-spot

test-gate-usdt-perp-acceptance: test-gate-testnet-usdt-perp test-gate-testnet-runtime-usdt-perp

test-gate-acceptance: test-gate-testnet-acceptance

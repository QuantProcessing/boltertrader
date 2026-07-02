.PHONY: test test-race test-core test-adapter test-sdk test-live-read test-binance-demo test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-spot-data test-binance-demo-spot test-binance-demo-acceptance test-okx-demo test-okx-demo-spot test-okx-demo-runtime-spot test-okx-demo-perp test-okx-demo-runtime-perp test-okx-demo-acceptance

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

test-live-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go test ./sdk/... ./adapter/...

test-binance-demo: test-binance-demo-acceptance

test-binance-demo-perp:
	go test -run TestBinanceDemoExecAcceptance ./adapter/binance/perp/ -count=1 -timeout=3m

test-binance-demo-runtime-perp:
	go test -run TestBinanceDemoRuntimeAcceptance ./adapter/binance/perp/ -count=1 -timeout=3m

test-binance-demo-spot-data:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go test -run TestBinanceSpotDemoDataAcceptance ./adapter/binance/spot/ -count=1 -timeout=2m

test-binance-demo-spot:
	go test -run TestBinanceSpotDemoExecAcceptance ./adapter/binance/spot/ -count=1 -timeout=3m

test-binance-demo-acceptance: test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-spot-data test-binance-demo-spot

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

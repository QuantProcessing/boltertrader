.PHONY: test test-race test-core test-adapter test-sdk test-live-read test-binance-demo test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-acceptance

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
	go test -run TestBinanceDemoExecE2E ./adapter/binance/perp/ -count=1 -timeout=3m

test-binance-demo-runtime-perp:
	go test -run TestBinanceDemoRuntimeE2E ./adapter/binance/perp/ -count=1 -timeout=3m

test-binance-demo-acceptance: test-binance-demo-perp test-binance-demo-runtime-perp

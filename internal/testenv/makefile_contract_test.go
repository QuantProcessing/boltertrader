package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRepositoryGitignoreProtectsEnvironmentCredentials(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read repository .gitignore: %v", err)
	}

	positions := map[string]int{
		".env":          -1,
		".env.*":        -1,
		"!.env.example": -1,
	}
	for lineNumber, line := range strings.Split(string(data), "\n") {
		rule := strings.TrimSpace(line)
		if _, tracked := positions[rule]; tracked {
			positions[rule] = lineNumber
		}
	}
	for rule, position := range positions {
		if position < 0 {
			t.Errorf("repository .gitignore is missing %q", rule)
		}
	}
	if positions["!.env.example"] < positions[".env.*"] {
		t.Error("!.env.example must follow .env.* so the shareable template remains unignored")
	}
}

func TestDemoAcceptanceRecipesRejectSkippedTests(t *testing.T) {
	makefile := readRepoMakefile(t)
	for target, want := range map[string]string{
		"test-binance-demo-perp":         "go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceDemoExecAcceptance$$'",
		"test-binance-demo-runtime-perp": "go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceDemoRuntimeAcceptance$$'",
		"test-binance-demo-spot-data":    "go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceSpotDemoDataAcceptance$$'",
		"test-binance-demo-spot":         "go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceSpotDemoExecAcceptance$$'",
		"test-binance-demo-runtime-spot": "go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceSpotDemoRuntimeAcceptance$$'",
		"test-okx-demo-spot":             "go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXSpotDemoExecAcceptance$$'",
		"test-okx-demo-runtime-spot":     "go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXSpotDemoRuntimeAcceptance$$'",
		"test-okx-demo-perp":             "go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXPerpDemoExecAcceptance$$'",
		"test-okx-demo-runtime-perp":     "go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXPerpDemoRuntimeAcceptance$$'",
	} {
		t.Run(target, func(t *testing.T) {
			block := makeTargetBlock(t, makefile, target)
			if !strings.Contains(block, want) {
				t.Fatalf("%s recipe must use noskipgotest with an exact verbose test selector\nblock:\n%s", target, block)
			}
		})
	}
}

func TestExchangeOfflineTargetIsPhony(t *testing.T) {
	makefile := readRepoMakefile(t)
	phony := make(map[string]bool)
	for _, line := range strings.Split(makefile, "\n") {
		if !strings.HasPrefix(line, ".PHONY:") {
			continue
		}
		for _, target := range strings.Fields(strings.TrimPrefix(line, ".PHONY:")) {
			phony[target] = true
		}
	}
	if !phony["test-exchange-offline"] {
		t.Error("Makefile .PHONY declarations are missing test-exchange-offline")
	}
}

func TestExchangeOfflineRecipeRunsShortSuite(t *testing.T) {
	makefile := readRepoMakefile(t)
	const want = "test-exchange-offline:\n\tgo test -short ./exchange/... -count=1"
	if got := makeTargetBlock(t, makefile, "test-exchange-offline"); got != want {
		t.Fatalf("test-exchange-offline recipe mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestExchangeQualityRecipesRunRaceAndRedactionGates(t *testing.T) {
	makefile := readRepoMakefile(t)
	for target, want := range map[string]string{
		"test-exchange-race":      "test-exchange-race:\n\tgo test -race ./exchange/... -count=1",
		"test-exchange-redaction": "test-exchange-redaction:\n\tgo test -short ./exchange/... -run 'Test(ConfigFormattingRedactsCredentials|ConstructedClientFormattingRedactsCredentials|ExpandedConstructedClientFormattingRedactsCredentials|AllVenueQueryNormalizersRedactUnderlyingErrors|AllVenueMutationNormalizersRedactVenueMessages|NormalizedErrorKindsAndMetadata)$$' -count=1",
		"test-exchange-quality":   "test-exchange-quality: test-exchange-offline test-exchange-redaction test-exchange-race",
	} {
		t.Run(target, func(t *testing.T) {
			if got := makeTargetBlock(t, makefile, target); got != want {
				t.Fatalf("%s recipe mismatch\ngot:\n%s\nwant:\n%s", target, got, want)
			}
		})
	}
}

func TestExchangeAcceptanceRecipesUseExactProductRowSelectors(t *testing.T) {
	makefile := readRepoMakefile(t)
	for target, contract := range map[string]struct {
		env      string
		selector string
		timeout  string
	}{
		"test-exchange-binance-demo-spot":        {"BOLTER_ENABLE_BINANCE_DEMO_WRITES=1", "-run '^TestExchangeBinanceSpotDemoAcceptance$$'", "-timeout=6m"},
		"test-exchange-binance-demo-perp":        {"BOLTER_ENABLE_BINANCE_DEMO_WRITES=1", "-run '^TestExchangeBinancePerpDemoAcceptance$$'", "-timeout=6m"},
		"test-exchange-okx-demo-spot":            {"BOLTER_ENABLE_OKX_DEMO_WRITES=1", "-run '^TestExchangeOKXSpotDemoAcceptance$$'", "-timeout=6m"},
		"test-exchange-okx-demo-perp":            {"BOLTER_ENABLE_OKX_DEMO_WRITES=1", "-run '^TestExchangeOKXPerpDemoAcceptance$$'", "-timeout=6m"},
		"test-exchange-lighter-testnet-spot":     {"BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1", "-run '^TestExchangeLighterSpotTestnetAcceptance$$'", "-timeout=6m"},
		"test-exchange-lighter-testnet-perp":     {"BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1", "-run '^TestExchangeLighterPerpTestnetAcceptance$$'", "-timeout=6m"},
		"test-exchange-hyperliquid-testnet-spot": {"BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1", "-run '^TestExchangeHyperliquidSpotTestnetAcceptance$$'", "-timeout=6m"},
		"test-exchange-hyperliquid-testnet-perp": {"BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1", "-run '^TestExchangeHyperliquidPerpTestnetAcceptance$$'", "-timeout=6m"},
		"test-exchange-bybit-demo-spot":          {"BOLTER_ENABLE_BYBIT_DEMO_WRITES=1", "-run '^TestExchangeBybitSpotDemoAcceptance$$'", "-timeout=15m"},
		"test-exchange-bybit-demo-usdt-perp":     {"BOLTER_ENABLE_BYBIT_DEMO_WRITES=1", "-run '^TestExchangeBybitUSDTPerpDemoAcceptance$$'", "-timeout=15m"},
		"test-exchange-bybit-demo-usdc-perp":     {"BOLTER_ENABLE_BYBIT_DEMO_WRITES=1", "-run '^TestExchangeBybitUSDCPerpDemoAcceptance$$'", "-timeout=15m"},
		"test-exchange-bitget-demo-spot":         {"BOLTER_ENABLE_BITGET_DEMO_WRITES=1", "-run '^TestExchangeBitgetSpotDemoAcceptance$$'", "-timeout=15m"},
		"test-exchange-bitget-demo-usdt-perp":    {"BOLTER_ENABLE_BITGET_DEMO_WRITES=1", "-run '^TestExchangeBitgetUSDTPerpDemoAcceptance$$'", "-timeout=15m"},
		"test-exchange-bitget-demo-usdc-perp":    {"BOLTER_ENABLE_BITGET_DEMO_WRITES=1", "-run '^TestExchangeBitgetUSDCPerpDemoAcceptance$$'", "-timeout=15m"},
		"test-exchange-gate-testnet-spot":        {"BOLTER_ENABLE_GATE_TESTNET_WRITES=1", "-run '^TestExchangeGateSpotTestnetAcceptance$$'", "-timeout=15m"},
		"test-exchange-gate-testnet-usdt-perp":   {"BOLTER_ENABLE_GATE_TESTNET_WRITES=1", "-run '^TestExchangeGateUSDTPerpTestnetAcceptance$$'", "-timeout=15m"},
		"test-exchange-aster-testnet-spot":       {"BOLTER_ENABLE_ASTER_TESTNET_WRITES=1", "-run '^TestExchangeAsterSpotTestnetAcceptance$$'", "-timeout=15m"},
		"test-exchange-aster-testnet-usdt-perp":  {"BOLTER_ENABLE_ASTER_TESTNET_WRITES=1", "-run '^TestExchangeAsterUSDTPerpTestnetAcceptance$$'", "-timeout=15m"},
		"test-exchange-nado-testnet-spot":        {"BOLTER_ENABLE_NADO_TESTNET_WRITES=1", "-run '^TestExchangeNadoSpotTestnetAcceptance$$'", "-timeout=15m"},
		"test-exchange-nado-testnet-usdt0-perp":  {"BOLTER_ENABLE_NADO_TESTNET_WRITES=1", "-run '^TestExchangeNadoUSDT0PerpTestnetAcceptance$$'", "-timeout=15m"},
	} {
		t.Run(target, func(t *testing.T) {
			block := makeTargetBlock(t, makefile, target)
			for _, want := range []string{
				contract.env,
				"go run ./internal/testenv/cmd/noskipgotest -- -v",
				contract.selector,
				"./exchange/...",
				"-count=1",
				contract.timeout,
			} {
				if !strings.Contains(block, want) {
					t.Fatalf("%s recipe missing %q\nblock:\n%s", target, want, block)
				}
			}
		})
	}
}

func TestGatewayFirstPromotionRecipesUseSerialNoSkipProductRows(t *testing.T) {
	makefile := readRepoMakefile(t)
	type contract struct {
		env      string
		selector string
		timeout  string
	}
	rows := []struct {
		target   string
		contract contract
	}{
		{"test-gateway-binance-spot-demo", contract{"BOLTER_ENABLE_BINANCE_DEMO_WRITES=1", "^TestExchangeBinanceSpotDemoAcceptance$$", "-timeout=6m"}},
		{"test-gateway-binance-usd-perp-demo", contract{"BOLTER_ENABLE_BINANCE_DEMO_WRITES=1", "^TestExchangeBinancePerpDemoAcceptance$$", "-timeout=6m"}},
		{"test-gateway-okx-spot-demo", contract{"BOLTER_ENABLE_OKX_DEMO_WRITES=1", "^TestExchangeOKXSpotDemoAcceptance$$", "-timeout=6m"}},
		{"test-gateway-okx-usdt-perp-demo", contract{"BOLTER_ENABLE_OKX_DEMO_WRITES=1", "^TestExchangeOKXPerpDemoAcceptance$$", "-timeout=6m"}},
		{"test-gateway-lighter-spot-testnet", contract{"BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1", "^TestExchangeLighterSpotTestnetAcceptance$$", "-timeout=6m"}},
		{"test-gateway-lighter-perp-testnet", contract{"BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1", "^TestExchangeLighterPerpTestnetAcceptance$$", "-timeout=6m"}},
		{"test-gateway-hyperliquid-spot-testnet", contract{"BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1", "^TestExchangeHyperliquidSpotTestnetAcceptance$$", "-timeout=6m"}},
		{"test-gateway-hyperliquid-perp-testnet", contract{"BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1", "^TestExchangeHyperliquidPerpTestnetAcceptance$$", "-timeout=6m"}},
		{"test-gateway-bybit-spot-demo", contract{"BOLTER_ENABLE_BYBIT_DEMO_WRITES=1", "^TestExchangeBybitSpotDemoAcceptance$$", "-timeout=15m"}},
		{"test-gateway-bybit-usdt-perp-demo", contract{"BOLTER_ENABLE_BYBIT_DEMO_WRITES=1", "^TestExchangeBybitUSDTPerpDemoAcceptance$$", "-timeout=15m"}},
		{"test-gateway-bybit-usdc-perp-demo", contract{"BOLTER_ENABLE_BYBIT_DEMO_WRITES=1", "^TestExchangeBybitUSDCPerpDemoAcceptance$$", "-timeout=15m"}},
		{"test-gateway-bitget-spot-demo", contract{"BOLTER_ENABLE_BITGET_DEMO_WRITES=1", "^TestExchangeBitgetSpotDemoAcceptance$$", "-timeout=15m"}},
		{"test-gateway-bitget-usdt-perp-demo", contract{"BOLTER_ENABLE_BITGET_DEMO_WRITES=1", "^TestExchangeBitgetUSDTPerpDemoAcceptance$$", "-timeout=15m"}},
		{"test-gateway-bitget-usdc-perp-demo", contract{"BOLTER_ENABLE_BITGET_DEMO_WRITES=1", "^TestExchangeBitgetUSDCPerpDemoAcceptance$$", "-timeout=15m"}},
		{"test-gateway-gate-spot-testnet", contract{"BOLTER_ENABLE_GATE_TESTNET_WRITES=1", "^TestExchangeGateSpotTestnetAcceptance$$", "-timeout=15m"}},
		{"test-gateway-gate-usdt-perp-testnet", contract{"BOLTER_ENABLE_GATE_TESTNET_WRITES=1", "^TestExchangeGateUSDTPerpTestnetAcceptance$$", "-timeout=15m"}},
		{"test-gateway-aster-spot-testnet", contract{"BOLTER_ENABLE_ASTER_TESTNET_WRITES=1", "^TestExchangeAsterSpotTestnetAcceptance$$", "-timeout=15m"}},
		{"test-gateway-aster-usdt-perp-testnet", contract{"BOLTER_ENABLE_ASTER_TESTNET_WRITES=1", "^TestExchangeAsterUSDTPerpTestnetAcceptance$$", "-timeout=15m"}},
		{"test-gateway-nado-spot-testnet", contract{"BOLTER_ENABLE_NADO_TESTNET_WRITES=1", "^TestExchangeNadoSpotTestnetAcceptance$$", "-timeout=15m"}},
		{"test-gateway-nado-usdt0-perp-testnet", contract{"BOLTER_ENABLE_NADO_TESTNET_WRITES=1", "^TestExchangeNadoUSDT0PerpTestnetAcceptance$$", "-timeout=15m"}},
	}

	phony := make(map[string]bool)
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, ".PHONY:") {
			for _, target := range strings.Fields(strings.TrimPrefix(line, ".PHONY:")) {
				phony[target] = true
			}
		}
	}
	for _, row := range rows {
		t.Run(row.target, func(t *testing.T) {
			if !phony[row.target] {
				t.Fatalf("%s is not phony", row.target)
			}
			block := makeTargetBlock(t, makefile, row.target)
			for _, want := range []string{
				row.contract.env,
				"go run ./internal/testenv/cmd/noskipgotest -- -v",
				"-run '" + row.contract.selector + "'",
				"./exchange/...",
				"-count=1",
				row.contract.timeout,
			} {
				if !strings.Contains(block, want) {
					t.Fatalf("%s recipe missing %q\nblock:\n%s", row.target, want, block)
				}
			}
		})
	}
	if !phony["test-gateway-demo-testnet"] {
		t.Fatal("test-gateway-demo-testnet is not phony")
	}
	wantAggregate := "test-gateway-demo-testnet:"
	for _, row := range rows {
		wantAggregate += " " + row.target
	}
	if got := makeTargetBlock(t, makefile, "test-gateway-demo-testnet"); got != wantAggregate {
		t.Fatalf("gateway promotion aggregate mismatch\ngot:\n%s\nwant:\n%s", got, wantAggregate)
	}
}

func TestExchangeAcceptanceAggregateRunsAllProductRowsSerially(t *testing.T) {
	makefile := readRepoMakefile(t)
	block := makeTargetBlock(t, makefile, "test-exchange-acceptance")
	wantLine := "test-exchange-acceptance: test-exchange-binance-demo-acceptance test-exchange-okx-demo-acceptance test-exchange-lighter-testnet-acceptance test-exchange-hyperliquid-testnet-acceptance test-exchange-bybit-demo-acceptance test-exchange-bitget-demo-acceptance test-exchange-gate-testnet-acceptance test-exchange-aster-testnet-acceptance test-exchange-nado-testnet-acceptance"
	if block != wantLine {
		t.Fatalf("test-exchange-acceptance aggregate mismatch\ngot:\n%s\nwant:\n%s", block, wantLine)
	}
}

func TestExchangeAcceptanceVenueAggregatesAndExternalAlias(t *testing.T) {
	makefile := readRepoMakefile(t)
	for target, want := range map[string]string{
		"test-exchange-binance-demo-acceptance":        "test-exchange-binance-demo-acceptance: test-exchange-binance-demo-spot test-exchange-binance-demo-perp",
		"test-exchange-okx-demo-acceptance":            "test-exchange-okx-demo-acceptance: test-exchange-okx-demo-spot test-exchange-okx-demo-perp",
		"test-exchange-lighter-testnet-acceptance":     "test-exchange-lighter-testnet-acceptance: test-exchange-lighter-testnet-spot test-exchange-lighter-testnet-perp",
		"test-exchange-hyperliquid-testnet-acceptance": "test-exchange-hyperliquid-testnet-acceptance: test-exchange-hyperliquid-testnet-spot test-exchange-hyperliquid-testnet-perp",
		"test-exchange-bybit-demo-acceptance":          "test-exchange-bybit-demo-acceptance: test-exchange-bybit-demo-spot test-exchange-bybit-demo-usdt-perp test-exchange-bybit-demo-usdc-perp",
		"test-exchange-bitget-demo-acceptance":         "test-exchange-bitget-demo-acceptance: test-exchange-bitget-demo-spot test-exchange-bitget-demo-usdt-perp test-exchange-bitget-demo-usdc-perp",
		"test-exchange-gate-testnet-acceptance":        "test-exchange-gate-testnet-acceptance: test-exchange-gate-testnet-spot test-exchange-gate-testnet-usdt-perp",
		"test-exchange-aster-testnet-acceptance":       "test-exchange-aster-testnet-acceptance: test-exchange-aster-testnet-spot test-exchange-aster-testnet-usdt-perp",
		"test-exchange-nado-testnet-acceptance":        "test-exchange-nado-testnet-acceptance: test-exchange-nado-testnet-spot test-exchange-nado-testnet-usdt0-perp",
		"test-exchange-external-acceptance":            "test-exchange-external-acceptance: test-exchange-acceptance",
	} {
		t.Run(target, func(t *testing.T) {
			if got := makeTargetBlock(t, makefile, target); got != want {
				t.Fatalf("%s aggregate mismatch\ngot:\n%s\nwant:\n%s", target, got, want)
			}
		})
	}
}

func TestLighterAcceptanceRecipesUseExactSelectors(t *testing.T) {
	makefile := readRepoMakefile(t)
	for target, selector := range map[string]string{
		"test-lighter-testnet-read":         "-run '^TestLighterTestnetReadAcceptance$$'",
		"test-lighter-testnet-spot":         "-run '^TestLighterTestnetSpotWriteAcceptance$$'",
		"test-lighter-testnet-runtime-spot": "-run '^TestLighterTestnetSpotRuntimeAcceptance$$'",
		"test-lighter-testnet-perp":         "-run '^TestLighterTestnetPerpWriteAcceptance$$'",
		"test-lighter-testnet-runtime-perp": "-run '^TestLighterTestnetPerpRuntimeAcceptance$$'",
	} {
		t.Run(target, func(t *testing.T) {
			block := makeTargetBlock(t, makefile, target)
			if !strings.Contains(block, selector) {
				t.Fatalf("%s recipe must use exact selector %q\nblock:\n%s", target, selector, block)
			}
		})
	}
}

func TestDemoWriteRecipesSetExplicitVenueWriteGates(t *testing.T) {
	makefile := readRepoMakefile(t)
	for target, want := range map[string]string{
		"test-binance-demo-perp":             "BOLTER_ENABLE_BINANCE_DEMO_WRITES=1",
		"test-binance-demo-runtime-perp":     "BOLTER_ENABLE_BINANCE_DEMO_WRITES=1",
		"test-binance-demo-spot":             "BOLTER_ENABLE_BINANCE_DEMO_WRITES=1",
		"test-binance-demo-runtime-spot":     "BOLTER_ENABLE_BINANCE_DEMO_WRITES=1",
		"test-okx-demo-spot":                 "BOLTER_ENABLE_OKX_DEMO_WRITES=1",
		"test-okx-demo-runtime-spot":         "BOLTER_ENABLE_OKX_DEMO_WRITES=1",
		"test-okx-demo-perp":                 "BOLTER_ENABLE_OKX_DEMO_WRITES=1",
		"test-okx-demo-runtime-perp":         "BOLTER_ENABLE_OKX_DEMO_WRITES=1",
		"test-bybit-demo-spot":               "BOLTER_ENABLE_BYBIT_DEMO_WRITES=1",
		"test-bybit-demo-runtime-spot":       "BOLTER_ENABLE_BYBIT_DEMO_WRITES=1",
		"test-bybit-demo-usdt-perp":          "BOLTER_ENABLE_BYBIT_DEMO_WRITES=1",
		"test-bybit-demo-runtime-usdt-perp":  "BOLTER_ENABLE_BYBIT_DEMO_WRITES=1",
		"test-bybit-demo-usdc-perp":          "BOLTER_ENABLE_BYBIT_DEMO_WRITES=1",
		"test-bybit-demo-runtime-usdc-perp":  "BOLTER_ENABLE_BYBIT_DEMO_WRITES=1",
		"test-bitget-demo-spot":              "BOLTER_ENABLE_BITGET_DEMO_WRITES=1",
		"test-bitget-demo-runtime-spot":      "BOLTER_ENABLE_BITGET_DEMO_WRITES=1",
		"test-bitget-demo-usdt-perp":         "BOLTER_ENABLE_BITGET_DEMO_WRITES=1",
		"test-bitget-demo-runtime-usdt-perp": "BOLTER_ENABLE_BITGET_DEMO_WRITES=1",
		"test-bitget-demo-usdc-perp":         "BOLTER_ENABLE_BITGET_DEMO_WRITES=1",
		"test-bitget-demo-runtime-usdc-perp": "BOLTER_ENABLE_BITGET_DEMO_WRITES=1",
	} {
		t.Run(target, func(t *testing.T) {
			block := makeTargetBlock(t, makefile, target)
			if !strings.Contains(block, want) {
				t.Fatalf("%s recipe must set %s command-locally\nblock:\n%s", target, want, block)
			}
		})
	}

	spotData := makeTargetBlock(t, makefile, "test-binance-demo-spot-data")
	if strings.Contains(spotData, "BOLTER_ENABLE_BINANCE_DEMO_WRITES=1") {
		t.Fatalf("read-only Binance Spot data recipe must not enable writes\nblock:\n%s", spotData)
	}
}

func TestMakefileSerializesParallelTopLevelTargets(t *testing.T) {
	makefile := readRepoMakefile(t)
	if !regexp.MustCompile(`(?m)^\.NOTPARALLEL:\s*$`).MatchString(makefile) {
		t.Fatal("Makefile must use a global .NOTPARALLEL directive so direct make -j leaf invocations cannot overlap credentialed writes")
	}

	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	temp := t.TempDir()
	probe := filepath.Join(temp, "serial-probe.mk")
	lock := filepath.Join(temp, "active")
	overlap := filepath.Join(temp, "overlap")
	probeMakefile := `.PHONY: codex-serial-probe-a codex-serial-probe-b
codex-serial-probe-a codex-serial-probe-b:
	@if mkdir "$(CODEX_SERIAL_LOCK)" 2>/dev/null; then sleep 0.2; rmdir "$(CODEX_SERIAL_LOCK)"; else touch "$(CODEX_SERIAL_OVERLAP)"; exit 1; fi
`
	if err := os.WriteFile(probe, []byte(probeMakefile), 0o600); err != nil {
		t.Fatalf("write Make serialization probe: %v", err)
	}
	cmd := exec.Command(
		"make", "--no-print-directory",
		"-f", filepath.Join(root, "Makefile"),
		"-f", probe,
		"-j2",
		"codex-serial-probe-a", "codex-serial-probe-b",
		"CODEX_SERIAL_LOCK="+lock,
		"CODEX_SERIAL_OVERLAP="+overlap,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("parallel Make probe overlapped top-level targets: %v\n%s", err, output)
	}
	if _, err := os.Stat(overlap); err == nil {
		t.Fatal("parallel Make probe observed overlapping top-level targets")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat Make overlap marker: %v", err)
	}
}

func TestCredentialedWriteRecipesReserveCleanupTimeout(t *testing.T) {
	makefile := readRepoMakefile(t)
	minimums := map[string]time.Duration{
		"test-binance-demo-perp":                 5 * time.Minute,
		"test-binance-demo-runtime-perp":         6 * time.Minute,
		"test-binance-demo-spot":                 5 * time.Minute,
		"test-binance-demo-runtime-spot":         6 * time.Minute,
		"test-exchange-binance-demo-spot":        6 * time.Minute,
		"test-exchange-binance-demo-perp":        6 * time.Minute,
		"test-exchange-okx-demo-spot":            6 * time.Minute,
		"test-exchange-okx-demo-perp":            6 * time.Minute,
		"test-exchange-lighter-testnet-spot":     6 * time.Minute,
		"test-exchange-lighter-testnet-perp":     6 * time.Minute,
		"test-exchange-hyperliquid-testnet-spot": 6 * time.Minute,
		"test-exchange-hyperliquid-testnet-perp": 6 * time.Minute,
		"test-gateway-binance-spot-demo":         6 * time.Minute,
		"test-gateway-binance-usd-perp-demo":     6 * time.Minute,
		"test-gateway-okx-spot-demo":             6 * time.Minute,
		"test-gateway-okx-usdt-perp-demo":        6 * time.Minute,
		"test-gateway-lighter-spot-testnet":      6 * time.Minute,
		"test-gateway-lighter-perp-testnet":      6 * time.Minute,
		"test-gateway-hyperliquid-spot-testnet":  6 * time.Minute,
		"test-gateway-hyperliquid-perp-testnet":  6 * time.Minute,
		"test-gateway-bybit-spot-demo":           15 * time.Minute,
		"test-gateway-bybit-usdt-perp-demo":      15 * time.Minute,
		"test-gateway-bybit-usdc-perp-demo":      15 * time.Minute,
		"test-gateway-bitget-spot-demo":          15 * time.Minute,
		"test-gateway-bitget-usdt-perp-demo":     15 * time.Minute,
		"test-gateway-bitget-usdc-perp-demo":     15 * time.Minute,
		"test-gateway-gate-spot-testnet":         15 * time.Minute,
		"test-gateway-gate-usdt-perp-testnet":    15 * time.Minute,
		"test-gateway-aster-spot-testnet":        15 * time.Minute,
		"test-gateway-aster-usdt-perp-testnet":   15 * time.Minute,
		"test-gateway-nado-spot-testnet":         15 * time.Minute,
		"test-gateway-nado-usdt0-perp-testnet":   15 * time.Minute,
		"test-exchange-bybit-demo-spot":          15 * time.Minute,
		"test-exchange-bybit-demo-usdt-perp":     15 * time.Minute,
		"test-exchange-bybit-demo-usdc-perp":     15 * time.Minute,
		"test-exchange-bitget-demo-spot":         15 * time.Minute,
		"test-exchange-bitget-demo-usdt-perp":    15 * time.Minute,
		"test-exchange-bitget-demo-usdc-perp":    15 * time.Minute,
		"test-exchange-gate-testnet-spot":        15 * time.Minute,
		"test-exchange-gate-testnet-usdt-perp":   15 * time.Minute,
		"test-exchange-aster-testnet-spot":       15 * time.Minute,
		"test-exchange-aster-testnet-usdt-perp":  15 * time.Minute,
		"test-exchange-nado-testnet-spot":        15 * time.Minute,
		"test-exchange-nado-testnet-usdt0-perp":  15 * time.Minute,
		"test-okx-demo-spot":                     5 * time.Minute,
		"test-okx-demo-runtime-spot":             6 * time.Minute,
		"test-okx-demo-perp":                     5 * time.Minute,
		"test-okx-demo-runtime-perp":             6 * time.Minute,
		"test-bybit-demo-spot":                   5 * time.Minute,
		"test-bybit-demo-runtime-spot":           6 * time.Minute,
		"test-bybit-demo-usdt-perp":              5 * time.Minute,
		"test-bybit-demo-runtime-usdt-perp":      6 * time.Minute,
		"test-bybit-demo-usdc-perp":              5 * time.Minute,
		"test-bybit-demo-runtime-usdc-perp":      6 * time.Minute,
		"test-bitget-demo-spot":                  5 * time.Minute,
		"test-bitget-demo-runtime-spot":          6 * time.Minute,
		"test-bitget-demo-usdt-perp":             5 * time.Minute,
		"test-bitget-demo-runtime-usdt-perp":     6 * time.Minute,
		"test-bitget-demo-usdc-perp":             5 * time.Minute,
		"test-bitget-demo-runtime-usdc-perp":     6 * time.Minute,
		"test-gate-testnet-spot":                 5 * time.Minute,
		"test-gate-testnet-runtime-spot":         6 * time.Minute,
		"test-gate-testnet-usdt-perp":            5 * time.Minute,
		"test-gate-testnet-runtime-usdt-perp":    6 * time.Minute,
		"test-hyperliquid-testnet-spot":          5 * time.Minute,
		"test-hyperliquid-testnet-runtime-spot":  6 * time.Minute,
		"test-hyperliquid-testnet-perp":          5 * time.Minute,
		"test-hyperliquid-testnet-runtime-perp":  6 * time.Minute,
		"test-hyperliquid-testnet-hip3-write":    5 * time.Minute,
		"test-hyperliquid-testnet-runtime-hip3":  6 * time.Minute,
		"test-lighter-testnet-spot":              5 * time.Minute,
		"test-lighter-testnet-runtime-spot":      6 * time.Minute,
		"test-lighter-testnet-perp":              5 * time.Minute,
		"test-lighter-testnet-runtime-perp":      6 * time.Minute,
		"test-aster-testnet-spot":                5 * time.Minute,
		"test-aster-testnet-runtime-spot":        6 * time.Minute,
		"test-aster-testnet-perp":                5 * time.Minute,
		"test-aster-testnet-runtime-perp":        6 * time.Minute,
		"test-nado-testnet-spot":                 5 * time.Minute,
		"test-nado-testnet-runtime-spot":         6 * time.Minute,
		"test-nado-testnet-perp":                 5 * time.Minute,
		"test-nado-testnet-runtime-perp":         6 * time.Minute,
	}
	timeoutPattern := regexp.MustCompile(`(?:^|\s)-timeout=([^\s]+)`)
	discovered := make(map[string]struct{})
	currentTarget := ""
	for _, line := range strings.Split(makefile, "\n") {
		if !strings.HasPrefix(line, "\t") {
			if name, _, ok := strings.Cut(line, ":"); ok {
				currentTarget = strings.TrimSpace(name)
			}
			continue
		}
		if strings.Contains(line, "BOLTER_ENABLE_") && strings.Contains(line, "_WRITES=1") {
			discovered[currentTarget] = struct{}{}
			if _, ok := minimums[currentTarget]; !ok {
				t.Errorf("write-enabled Makefile target %s is missing from the cleanup-timeout contract", currentTarget)
			}
		}
	}

	for target, minimum := range minimums {
		t.Run(target, func(t *testing.T) {
			if _, ok := discovered[target]; !ok {
				t.Fatalf("cleanup-timeout contract target %s is not a write-enabled Makefile recipe", target)
			}
			block := makeTargetBlock(t, makefile, target)
			match := timeoutPattern.FindStringSubmatch(block)
			if match == nil {
				t.Fatalf("%s recipe must set an explicit go test timeout\nblock:\n%s", target, block)
			}
			timeout, err := time.ParseDuration(match[1])
			if err != nil {
				t.Fatalf("%s recipe has invalid timeout %q: %v", target, match[1], err)
			}
			if timeout < minimum {
				t.Fatalf("%s timeout=%s, want at least %s so deferred cleanup can finish", target, timeout, minimum)
			}
		})
	}
}

func readRepoMakefile(t *testing.T) string {
	t.Helper()
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	data, err := os.ReadFile(root + "/Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	return string(data)
}

func makeTargetBlock(t *testing.T, makefile, target string) string {
	t.Helper()
	prefix := target + ":"
	start := strings.Index(makefile, prefix)
	if start < 0 {
		t.Fatalf("Makefile target %s not found", target)
	}
	rest := makefile[start:]
	if end := strings.Index(rest, "\n\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}

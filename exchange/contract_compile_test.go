package exchange

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFactoryProductMethodSetsCompile(t *testing.T) {
	root := repositoryRoot(t)

	positive := `package compilecheck

import (
	"context"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
)

func check() {
	client, _ := factory.New(factory.BinanceUSDPerpConfig(
		"key",
		"secret",
		factory.WithEnvironment(factory.EnvironmentLive),
	))
	var typed exchange.PerpClient = client
	_, _ = typed.Positions(context.Background(), exchange.PositionsRequest{})

	var spot exchange.SpotClient
	var perp exchange.PerpClient
	spot, _ = factory.New(factory.BybitSpotConfig("key", "secret", factory.WithEnvironment(factory.EnvironmentLive)))
	perp, _ = factory.New(factory.BybitUSDTPerpConfig("key", "secret", factory.WithEnvironment(factory.EnvironmentLive)))
	perp, _ = factory.New(factory.BybitUSDCPerpConfig("key", "secret", factory.WithEnvironment(factory.EnvironmentLive)))
	spot, _ = factory.New(factory.BitgetSpotConfig("key", "secret", "passphrase", factory.WithEnvironment(factory.EnvironmentLive)))
	perp, _ = factory.New(factory.BitgetUSDTPerpConfig("key", "secret", "passphrase", factory.WithEnvironment(factory.EnvironmentLive)))
	perp, _ = factory.New(factory.BitgetUSDCPerpConfig("key", "secret", "passphrase", factory.WithEnvironment(factory.EnvironmentLive)))
	spot, _ = factory.New(factory.GateSpotConfig("key", "secret", factory.WithEnvironment(factory.EnvironmentLive)))
	perp, _ = factory.New(factory.GateUSDTPerpConfig("key", "secret", factory.WithEnvironment(factory.EnvironmentLive)))
	spot, _ = factory.New(factory.AsterSpotConfig(
		"0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf",
		"0000000000000000000000000000000000000000000000000000000000000001",
		"0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf",
		factory.WithEnvironment(factory.EnvironmentLive),
	))
	perp, _ = factory.New(factory.AsterUSDTPerpConfig(
		"0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf",
		"0000000000000000000000000000000000000000000000000000000000000001",
		"0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf",
		factory.WithEnvironment(factory.EnvironmentLive),
	))
	spot, _ = factory.New(factory.NadoSpotConfig(
		"0000000000000000000000000000000000000000000000000000000000000001",
		"default",
		factory.WithEnvironment(factory.EnvironmentLive),
	))
	perp, _ = factory.New(factory.NadoUSDT0PerpConfig(
		"0000000000000000000000000000000000000000000000000000000000000001",
		"default",
		factory.WithEnvironment(factory.EnvironmentLive),
	))
	_ = spot
	_ = perp
}
`
	runCompileFixture(t, root, "positive_perp", positive, true, "")

	negative := `package compilecheck

import (
	"context"

	"github.com/QuantProcessing/boltertrader/exchange/factory"
)

func check() {
	client, _ := factory.New(factory.BinanceSpotConfig(
		"key",
		"secret",
		factory.WithEnvironment(factory.EnvironmentLive),
	))
	_, _ = client.Positions(context.Background(), struct{}{})
}
`
	runCompileFixture(
		t,
		root,
		"negative_spot",
		negative,
		false,
		"client.Positions undefined",
	)
}

func runCompileFixture(
	t *testing.T,
	root string,
	name string,
	source string,
	wantSuccess bool,
	wantDiagnostic string,
) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	module := "module example.com/" + name + "\n\ngo 1.26\n\n" +
		"require github.com/QuantProcessing/boltertrader v0.0.0\n\n" +
		"replace github.com/QuantProcessing/boltertrader => " + root + "\n"
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "compile_test.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("go", "test", "-mod=mod", "./...")
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if wantSuccess {
		if err != nil {
			t.Fatalf("%s must compile: %v\n%s", name, err, output)
		}
		return
	}
	if err == nil {
		t.Fatalf("%s unexpectedly compiled", name)
	}
	if !strings.Contains(string(output), wantDiagnostic) {
		t.Fatalf("%s diagnostic missing %q:\n%s", name, wantDiagnostic, output)
	}
}

//go:build darwin

package secretstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

type recordingRunner struct {
	command string
	args    []string
}

func (runner *recordingRunner) Run(_ context.Context, command string, args ...string) ([]byte, error) {
	runner.command = command
	runner.args = append([]string(nil), args...)
	return []byte(strings.Repeat("a", 64) + "\n"), nil
}

func TestKeychainSourceUsesInjectedSecurityRunner(t *testing.T) {
	runner := &recordingRunner{}
	key, err := (secretstore.KeychainSource{Service: "service", Account: "account", Runner: runner}).Load(context.Background())
	if err != nil || len(key) != 32 {
		t.Fatalf("key length=%d err=%v", len(key), err)
	}
	clear(key)
	if runner.command != "security" || strings.Join(runner.args, " ") != "find-generic-password -w -s service -a account" {
		t.Fatalf("command=%q args=%q", runner.command, runner.args)
	}
}

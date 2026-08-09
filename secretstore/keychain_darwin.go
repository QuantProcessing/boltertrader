//go:build darwin

package secretstore

import (
	"context"
	"encoding/hex"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type KeychainSource struct {
	Service string
	Account string
	Runner  CommandRunner
}

func (source KeychainSource) Load(ctx context.Context) ([]byte, error) {
	if source.Service == "" || source.Account == "" || source.Runner == nil {
		return nil, ErrInvalidKeySource
	}
	output, err := source.Runner.Run(ctx, "security", "find-generic-password", "-w", "-s", source.Service, "-a", source.Account)
	if err != nil {
		clear(output)
		return nil, ErrInvalidKeySource
	}
	defer clear(output)
	for len(output) > 0 && output[len(output)-1] == '\n' {
		output = output[:len(output)-1]
	}
	if len(output) != 64 {
		return nil, ErrInvalidKeySource
	}
	key := make([]byte, 32)
	if _, err := hex.Decode(key, output); err != nil {
		clear(key)
		return nil, ErrInvalidKeySource
	}
	return key, nil
}

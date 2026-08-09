//go:build !darwin

package secretstore

import "context"

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type KeychainSource struct {
	Service string
	Account string
	Runner  CommandRunner
}

func (KeychainSource) Load(context.Context) ([]byte, error) {
	return nil, ErrKeySourceUnsupported
}

package nado

import "errors"

var (
	ErrUnknownInstrument = errors.New("nado: unknown instrument")
	ErrAccountMismatch   = errors.New("nado: account mismatch")
)

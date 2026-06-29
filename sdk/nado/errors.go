package nado

import "errors"

var (
	ErrInvalidSymbol       = errors.New("nado: invalid symbol")
	ErrInvalidSide         = errors.New("nado: invalid side")
	ErrInvalidOrderType    = errors.New("nado: invalid order type")
	ErrNotAuthenticated    = errors.New("nado: not authenticated")
	ErrTimeout             = errors.New("nado: timeout")
	ErrCredentialsRequired = errors.New("nado: credentials required")
)

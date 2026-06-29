package sdk

import "github.com/QuantProcessing/exchanges/internal/errs"

var (
	ErrAuthFailed     = errs.ErrAuthFailed
	ErrOrderNotFound  = errs.ErrOrderNotFound
	ErrSymbolNotFound = errs.ErrSymbolNotFound
	ErrRateLimited    = errs.ErrRateLimited
)

type ExchangeError = errs.ExchangeError

func NewExchangeError(exchange, code, message string, sentinel error) *ExchangeError {
	return errs.NewExchangeError(exchange, code, message, sentinel)
}

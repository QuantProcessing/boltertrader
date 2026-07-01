package errs

import (
	"errors"
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/contract"
)

var (
	ErrInsufficientBalance = errors.New("insufficient balance")
	ErrRateLimited         = errors.New("rate limited")
	ErrInvalidPrecision    = errors.New("invalid precision")
	ErrOrderNotFound       = errors.New("order not found")
	ErrSymbolNotFound      = errors.New("symbol not found")
	ErrMinNotional         = errors.New("below minimum notional")
	ErrMinQuantity         = errors.New("below minimum quantity")
	ErrAuthFailed          = errors.New("authentication failed")
	ErrNetworkTimeout      = errors.New("network timeout")
	ErrNotSupported        = contract.ErrNotSupported
)

type ExchangeError struct {
	Exchange string
	Code     string
	Message  string
	Err      error
}

func (e *ExchangeError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("[%s] %s: %s", e.Exchange, e.Code, e.Message)
	}
	return fmt.Sprintf("[%s] %s", e.Exchange, e.Message)
}

func (e *ExchangeError) Unwrap() error {
	return e.Err
}

func NewExchangeError(exchange, code, message string, sentinel error) *ExchangeError {
	return &ExchangeError{
		Exchange: exchange,
		Code:     code,
		Message:  message,
		Err:      sentinel,
	}
}

package okx

import (
	"fmt"
)

// APIError represents an error returned by the OKX API.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"msg"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("okx api error: code=%s, msg=%s", e.Code, e.Message)
}

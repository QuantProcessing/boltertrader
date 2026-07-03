package okx

import (
	"fmt"
)

// APIError represents an error returned by the OKX API.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"msg"`
	Details string `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("okx api error: code=%s, msg=%s, details=%s", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("okx api error: code=%s, msg=%s", e.Code, e.Message)
}

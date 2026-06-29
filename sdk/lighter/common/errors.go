package common

import (
	"errors"
	"fmt"
)

type APIError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

func (e APIError) Error() string {
	if e.IsValid() {
		return fmt.Sprintf("<APIError> code=%d, msg=%s", e.Code, e.Message)
	}
	return fmt.Sprintf("<APIError> rsp=%s", e.Message)
}

func (e APIError) IsValid() bool {
	return e.Code != 0 || e.Message != ""
}

func IsAPIError(e error) bool {
	var APIError *APIError
	ok := errors.As(e, &APIError)
	return ok
}

package standx

import "fmt"

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("standx error: code=%d message=%s", e.Code, e.Message)
}

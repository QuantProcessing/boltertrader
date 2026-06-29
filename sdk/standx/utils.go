package standx

import (
	"fmt"

	"github.com/google/uuid"
)

func GenSessionID() string {
	// session-uuid
	return fmt.Sprintf("session-%v", uuid.New().String())
}

func GenRequestID() string {
	// req-uuid
	return fmt.Sprintf("%v", uuid.New().String())
}

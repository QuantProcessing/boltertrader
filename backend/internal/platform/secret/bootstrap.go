package secret

import (
	"crypto/subtle"
	"fmt"
	"os"
	"strings"
)

func LoadToken(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("token path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file is empty")
	}
	return token, nil
}

func ConstantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

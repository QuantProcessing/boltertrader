package common

import (
	"fmt"
	"net/url"
	"strings"
)

type VenueError struct {
	statusCode int
	method     string
	path       string
	code       int
	message    string
}

func NewVenueError(statusCode int, method, path string, code int, message string) error {
	return &VenueError{
		statusCode: statusCode,
		method:     method,
		path:       sanitizePath(path),
		code:       code,
		message:    sanitizeAuthMaterial(message),
	}
}

func (e *VenueError) Error() string {
	return fmt.Sprintf("aster sdk: %s %s returned HTTP %d, code %d: %s", e.method, e.path, e.statusCode, e.code, e.message)
}

func (e *VenueError) StatusCode() int { return e.statusCode }

func (e *VenueError) Code() int { return e.code }

func (e *VenueError) Message() string { return e.message }

type TransportError struct {
	method string
	path   string
	cause  error
}

func NewTransportError(method, path string, cause error) error {
	return &TransportError{method: method, path: sanitizePath(path), cause: cause}
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("aster sdk: %s %s transport failed", e.method, e.path)
}

func (e *TransportError) Unwrap() error { return e.cause }

func RedactURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<redacted-url>"
	}
	query := parsed.Query()
	for key := range query {
		if _, sensitive := reservedAuthFields[strings.ToLower(key)]; sensitive {
			parsed.RawQuery = url.Values{"query": {"<redacted>"}}.Encode()
			parsed.Fragment = ""
			return parsed.String()
		}
	}
	return parsed.String()
}

func SanitizeVenueMessage(message string) string {
	return sanitizeAuthMaterial(message)
}

func sanitizePath(path string) string {
	parsed, err := url.Parse(path)
	if err != nil {
		return "<redacted-path>"
	}
	safe := parsed.EscapedPath()
	if safe == "" {
		return "<redacted-path>"
	}
	return safe
}

func sanitizeAuthMaterial(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "request weight"):
		return "request weight limit exceeded"
	case strings.Contains(lower, "too many requests"):
		return "too many requests"
	case strings.Contains(lower, "banned until"):
		return "request banned until rate limit reset"
	default:
		return "<redacted>"
	}
}

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
		path:       path,
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
	if urlError, ok := cause.(*url.Error); ok && urlError.Err != nil {
		cause = urlError.Err
	}
	return &TransportError{method: method, path: path, cause: cause}
}

func (e *TransportError) Error() string {
	if e.cause == nil {
		return fmt.Sprintf("aster sdk: %s %s transport failed", e.method, e.path)
	}
	return fmt.Sprintf("aster sdk: %s %s transport failed: %s", e.method, e.path, sanitizeAuthMaterial(e.cause.Error()))
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

func sanitizeAuthMaterial(message string) string {
	lower := strings.ToLower(message)
	for key := range reservedAuthFields {
		if strings.Contains(lower, key+"=") {
			return "<redacted>"
		}
	}
	return message
}

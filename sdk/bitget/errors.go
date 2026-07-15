package sdk

import (
	"errors"
	"fmt"
	"strings"
)

// ResponseError is a decoded Bitget application-level failure. HTTP status,
// transport, context, and JSON errors remain distinct and are never promoted
// to this type.
type ResponseError struct {
	Operation string
	Code      string
	Message   string
}

type commandResponseEnvelope[T any] struct {
	Code *string `json:"code"`
	Msg  string  `json:"msg"`
	Data *T      `json:"data"`
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("bitget sdk: %s failed: %s %s", e.Operation, e.Code, e.Message)
}

func newResponseError(operation, code, message string) error {
	return &ResponseError{Operation: operation, Code: code, Message: message}
}

func commandResult[T any](operation string, response commandResponseEnvelope[T]) (*T, error) {
	if response.Code == nil {
		return nil, fmt.Errorf("bitget sdk: %s returned a partial response without code", operation)
	}
	if *response.Code != "00000" {
		return nil, newResponseError(operation, *response.Code, response.Msg)
	}
	if response.Data == nil {
		return nil, fmt.Errorf("bitget sdk: %s returned a partial response without data", operation)
	}
	return response.Data, nil
}

func validateOrderActionResult(operation, orderID, clientOID, expectedOrderID, expectedClientOID string) error {
	if orderID == "" {
		return fmt.Errorf("bitget sdk: %s returned a partial response without order id", operation)
	}
	if expectedOrderID != "" && orderID != expectedOrderID {
		return fmt.Errorf("bitget sdk: %s returned mismatched order id %q for %q", operation, orderID, expectedOrderID)
	}
	if expectedClientOID != "" && clientOID != "" && clientOID != expectedClientOID {
		return fmt.Errorf("bitget sdk: %s returned mismatched client order id %q for %q", operation, clientOID, expectedClientOID)
	}
	return nil
}

// IsDefinitiveCommandRejection excludes known timeout, throttling, backend,
// and internal-service response classes. All classification uses structured
// response codes; message text is diagnostic only.
func IsDefinitiveCommandRejection(err error) bool {
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr == nil {
		return false
	}
	code := strings.TrimSpace(responseErr.Code)
	if code == "" || code == "00000" {
		return false
	}
	switch code {
	case "25000", "25001", "25003", "25004", "25102", "25103", "25106",
		"25209", "25239", "25567", "25572", "25653",
		"40010", "40015", "40200", "40725", "40808", "429", "42900",
		"45001", "50000":
		return false
	}
	// This allow-list is the intersection of Bitget's documented UTA/common
	// taxonomy and command rejection semantics. Unknown future codes remain
	// ambiguous until the SDK taxonomy is deliberately extended.
	switch code {
	case "25002", "25005", "25006", "25007", "25008", "25009", "25010",
		"25011", "25012", "25100", "25101", "25104", "25105", "25107",
		"25108", "25200", "25201", "25202", "25203", "25204", "25205",
		"25206", "25207", "25208", "25210", "25211", "25212", "25213",
		"25214", "25215", "25216", "25217", "25218", "25219", "25221",
		"25222", "25223", "25224", "25225", "25226", "25227", "25228",
		"25229", "25230", "25231", "25232", "25233", "25234", "25235",
		"25236", "25237", "25238", "25240", "25241", "25242", "25243",
		"25244", "25245", "25568", "25569", "25570", "25571", "25573",
		"25574", "25620", "25654", "25655",
		"40000", "40001", "40002", "40003", "40005", "40006", "40007",
		"40008", "40009", "40011", "40012", "40013", "40014", "40016",
		"40017", "40018", "40019", "40020", "40034", "40042", "40100",
		"40101", "40102", "40103", "40104", "40105", "40109", "40110",
		"40111", "40199", "40301", "40303", "40304", "40305", "40306",
		"40309", "40402", "40404", "40407", "40408", "40409", "40704",
		"40705", "40706", "40707", "40710", "40714", "40715", "40716",
		"40717", "40718", "40719", "40720", "40721", "40722", "40723",
		"40724", "40730", "40732", "40734", "40744", "40746",
		"40931", "41101", "41117", "41118", "42072",
		"43001", "43002", "43003", "43004", "43005", "43006", "43007",
		"43008", "43009", "43010", "43012", "43022", "43027", "43028",
		"43112", "43118", "95005", "95006", "95007", "95008":
		return true
	default:
		return false
	}
}

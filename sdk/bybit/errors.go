package sdk

import (
	"errors"
	"fmt"
)

// ResponseError is a decoded Bybit V5 application-level failure. It is
// distinct from HTTP, transport, deadline, and JSON decoding failures.
type ResponseError struct {
	Operation string
	Code      int
	Message   string
}

type commandResponseEnvelope[T any] struct {
	RetCode *int   `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  *T     `json:"result"`
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("bybit sdk: %s failed: %d %s", e.Operation, e.Code, e.Message)
}

func newResponseError(operation string, code int, message string) error {
	return &ResponseError{Operation: operation, Code: code, Message: message}
}

func commandResult[T any](operation string, response commandResponseEnvelope[T]) (*T, error) {
	if response.RetCode == nil {
		return nil, fmt.Errorf("bybit sdk: %s returned a partial response without retCode", operation)
	}
	if *response.RetCode != 0 {
		return nil, newResponseError(operation, *response.RetCode, response.RetMsg)
	}
	if response.Result == nil {
		return nil, fmt.Errorf("bybit sdk: %s returned a partial response without result", operation)
	}
	return response.Result, nil
}

func validateOrderActionResult(operation string, result *OrderActionResponse, expectedOrderID, expectedOrderLinkID string) error {
	if result == nil || result.OrderID == "" {
		return fmt.Errorf("bybit sdk: %s returned a partial response without order id", operation)
	}
	if expectedOrderID != "" && result.OrderID != expectedOrderID {
		return fmt.Errorf("bybit sdk: %s returned mismatched order id %q for %q", operation, result.OrderID, expectedOrderID)
	}
	if expectedOrderLinkID != "" && result.OrderLinkID != "" && result.OrderLinkID != expectedOrderLinkID {
		return fmt.Errorf("bybit sdk: %s returned mismatched order link id %q for %q", operation, result.OrderLinkID, expectedOrderLinkID)
	}
	return nil
}

// IsDefinitiveCommandRejection is intentionally conservative. It recognizes
// only decoded command envelopes and excludes Bybit's known timeout,
// throttling, backend, and service-restart classes.
func IsDefinitiveCommandRejection(err error) bool {
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr == nil || responseErr.Code == 0 {
		return false
	}
	switch responseErr.Code {
	case -1, 429, 10000, 10006, 10016, 10018, 10019, 10429,
		30035, 40004,
		110079, 110114,
		170001, 170005, 170007, 170019, 170032, 170035, 170146,
		170147, 170191, 170212, 170222, 170234, 170310,
		3400214:
		return false
	}
	// Only codes explicitly documented by Bybit as authentication,
	// validation, account/risk, or order-state rejection are definitive.
	// Unknown future codes fail closed instead of inheriting meaning merely
	// because they are non-zero.
	switch responseErr.Code {
	case -2015, 33004,
		10001, 10002, 10003, 10004, 10005, 10007, 10008, 10009, 10010,
		10014, 10017, 10024, 10027, 10028, 10029, 100028,
		30133, 30134, 30135, 30136, 30208, 30209, 30228,
		110001, 110003, 110004, 110005, 110006, 110007, 110008, 110009,
		110010, 110011, 110012, 110013, 110014, 110015, 110017, 110018,
		110019, 110020, 110021, 110022, 110023, 110024, 110025, 110026,
		110027, 110028, 110029, 110031, 110032, 110033, 110034, 110035,
		110036, 110037, 110038, 110039, 110040, 110041, 110042, 110043,
		110044, 110045, 110046, 110047, 110048, 110049, 110050, 110051,
		110052, 110053, 110054, 110055, 110056, 110057, 110058, 110059,
		110060, 110061, 110062, 110063, 110064, 110065, 110066, 110067,
		110068, 110069, 110070, 110071, 110072, 110073, 110074, 110076,
		110077, 110078, 110080, 110082, 110083, 110085, 110086, 110087,
		110088, 110089, 110090, 110092, 110093, 110094, 110095, 110096,
		110097, 110098, 110099, 110100, 110101, 110102, 110103, 110104,
		110105, 110106, 110107, 110108, 110109, 110110, 110111, 110112,
		110113, 110115, 110116, 110117, 110118, 110119, 110120, 110121,
		110123, 110124, 110125, 110132, 110135, 110136, 110137,
		170010, 170011, 170031, 170033, 170034, 170036, 170037,
		170105, 170115, 170116, 170117, 170121, 170124, 170130, 170131,
		170132, 170133, 170134, 170136, 170137, 170139, 170140, 170141,
		170142, 170143, 170144, 170145, 170148, 170149, 170150, 170151,
		170157, 170159, 170190, 170192, 170193, 170194, 170195, 170196,
		170197, 170198, 170199, 170200, 170201, 170202, 170203, 170204,
		170206, 170207, 170209, 170210, 170213, 170215, 170216, 170217,
		170218, 170219, 170220, 170221, 170223, 170224, 170226, 170227,
		170228, 170229, 170230, 170241, 170311, 170312, 170313, 170341,
		170344, 170346, 170348, 170355, 170358, 170359, 170360, 170371,
		170372, 170381, 170382, 170709, 170810:
		return true
	default:
		return false
	}
}

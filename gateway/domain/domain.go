package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const maxUint48 = uint64(1<<48 - 1)

var (
	decimalUint = regexp.MustCompile(`^[1-9][0-9]*$`)
	uuidV7      = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-7[0-9A-Fa-f]{3}-[89ABab][0-9A-Fa-f]{3}-[0-9A-Fa-f]{12}$`)
)

type CommandID string

type PrincipalID string

type AccountID string

type InstrumentID string

type IdempotencyKey string

type GatewayOrderID string

type EventID string

type BootID string

type ResourceVersion int64

type ClientOrderID uint64

func ParseClientOrderID(value string) (ClientOrderID, error) {
	if !decimalUint.MatchString(value) {
		return 0, fmt.Errorf("client order id must be a positive decimal uint48")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || parsed > maxUint48 {
		return 0, fmt.Errorf("client order id must be a positive decimal uint48")
	}
	return ClientOrderID(parsed), nil
}

func (id ClientOrderID) String() string {
	if id == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(id), 10)
}

func (id PrincipalID) Validate() error    { return validateSafeToken("principal id", string(id)) }
func (id AccountID) Validate() error      { return validateSafeToken("account id", string(id)) }
func (id IdempotencyKey) Validate() error { return validateSafeToken("idempotency key", string(id)) }

func (id InstrumentID) Validate() error {
	value := string(id)
	if err := validateSafeToken("instrument id", value); err != nil {
		return err
	}
	parts := strings.Split(value, ":")
	if len(parts) != 4 && len(parts) != 5 {
		return fmt.Errorf("instrument id must be venue:environment:product[:settlement]:symbol")
	}
	if len(parts) == 5 && (parts[3] == "" || strings.Contains(parts[3], "-")) {
		return fmt.Errorf("instrument id settlement must be normalized asset")
	}
	symbol := parts[len(parts)-1]
	if !strings.Contains(symbol, "-") {
		return fmt.Errorf("instrument id must use canonical symbol")
	}
	return nil
}

func NewInstrumentID(venue, environment, product, settlement, symbol string) (InstrumentID, error) {
	parts := []string{venue, environment, product}
	if settlement != "" {
		parts = append(parts, settlement)
	}
	parts = append(parts, symbol)
	id := InstrumentID(strings.Join(parts, ":"))
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func (id InstrumentID) String() string { return string(id) }

// Product and Settlement read back the two fields NewInstrumentID wrote, so a
// reader holding only an instrument id can say which of an account's routes it
// belongs to. That question is not academic: one account may run several routes
// at once, and bybit settles perpetuals in both USDT and USDC under a single
// set of credentials. Product alone does not tell those two apart.
//
// Settlement returns "" for the four-part form, which omits it by design — a
// venue whose product has no settlement asset to name.
func (id InstrumentID) Product() string {
	parts := strings.Split(string(id), ":")
	if len(parts) != 4 && len(parts) != 5 {
		return ""
	}
	return parts[2]
}

func (id InstrumentID) Settlement() string {
	parts := strings.Split(string(id), ":")
	if len(parts) != 5 {
		return ""
	}
	return parts[3]
}

func (id CommandID) Validate() error      { return validateUUIDv7("command id", string(id)) }
func (id GatewayOrderID) Validate() error { return validateUUIDv7("gateway order id", string(id)) }
func (id EventID) Validate() error        { return validateUUIDv7("event id", string(id)) }
func (id BootID) Validate() error         { return validateUUIDv7("boot id", string(id)) }

func (version ResourceVersion) Validate() error {
	if version < 0 {
		return fmt.Errorf("resource version must be nonnegative")
	}
	return nil
}

func validateSafeToken(name, value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is required and must be trimmed", name)
	}
	if strings.ContainsAny(value, "\n\r\t") {
		return fmt.Errorf("%s contains unsafe whitespace", name)
	}
	return nil
}

func validateUUIDv7(name, value string) error {
	if !uuidV7.MatchString(value) {
		return fmt.Errorf("%s must be uuidv7", name)
	}
	return nil
}

func CanonicalPayloadHash(raw json.RawMessage) (string, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("payload must contain one JSON value")
		}
		return "", fmt.Errorf("payload must contain one JSON value")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

type OrderState string

const (
	StatePrepared        OrderState = "prepared"
	StateDispatching     OrderState = "dispatching"
	StateAccepted        OrderState = "accepted"
	StateOpen            OrderState = "open"
	StatePartiallyFilled OrderState = "partially_filled"
	StateFilled          OrderState = "filled"
	StateCanceled        OrderState = "canceled"
	StateRejected        OrderState = "rejected"
	StateUncertain       OrderState = "uncertain"
)

var allowedTransitions = map[OrderState]map[OrderState]bool{
	StatePrepared: {
		StateDispatching: true,
		StateRejected:    true,
	},
	StateDispatching: {
		StateAccepted:  true,
		StateUncertain: true,
		StateRejected:  true,
	},
	StateAccepted: {
		StateOpen:            true,
		StatePartiallyFilled: true,
		StateFilled:          true,
		StateCanceled:        true,
		StateRejected:        true,
		StateUncertain:       true,
	},
	StateOpen: {
		StatePartiallyFilled: true,
		StateFilled:          true,
		StateCanceled:        true,
		StateUncertain:       true,
	},
	StatePartiallyFilled: {
		StateFilled:    true,
		StateCanceled:  true,
		StateUncertain: true,
	},
	StateUncertain: {
		StateAccepted:        true,
		StateOpen:            true,
		StatePartiallyFilled: true,
		StateFilled:          true,
		StateCanceled:        true,
		StateRejected:        true,
	},
}

func ValidateOrderTransition(from, to OrderState) error {
	if from == "" || to == "" {
		return fmt.Errorf("order state transition is incomplete")
	}
	if allowedTransitions[from][to] {
		return nil
	}
	return fmt.Errorf("illegal order state transition %s -> %s", from, to)
}

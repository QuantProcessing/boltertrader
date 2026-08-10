package readmodel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidCursor = errors.New("read-model cursor is invalid")

type Resource string

const (
	Opportunities   Resource = "opportunities"
	Alerts          Resource = "funding-rate-alerts"
	Executions      Resource = "executions"
	Positions       Resource = "positions"
	Activities      Resource = "activities"
	AuditEvents     Resource = "audit-events"
	FundingPayments Resource = "ledger-funding-payments"
)

type CursorPosition struct {
	Resource        Resource  `json:"resource"`
	PrincipalID     string    `json:"principalId,omitempty"`
	Mode            string    `json:"mode"`
	ContractVersion string    `json:"contractVersion"`
	Fingerprint     string    `json:"fingerprint"`
	SortValue       string    `json:"sortValue"`
	ID              string    `json:"id"`
	OccurredAt      time.Time `json:"occurredAt,omitempty"`
}

type CursorExpectation struct {
	Resource        Resource
	PrincipalID     string
	Mode            string
	ContractVersion string
	Fingerprint     string
}

type CursorCodec struct {
	key []byte
}

func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) < sha256.Size {
		return nil, fmt.Errorf("cursor key must be at least %d bytes", sha256.Size)
	}
	copied := make([]byte, len(key))
	copy(copied, key)
	return &CursorCodec{key: copied}, nil
}

func (codec *CursorCodec) Encode(position CursorPosition) (string, error) {
	if position.ContractVersion == "" {
		position.ContractVersion = ContractVersion
	}
	if codec == nil || len(codec.key) < sha256.Size || !validPosition(position) {
		return "", ErrInvalidCursor
	}
	position.OccurredAt = position.OccurredAt.UTC()
	payload, err := json.Marshal(struct {
		Version  int            `json:"version"`
		Position CursorPosition `json:"position"`
	}{Version: 2, Position: position})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	signature := codec.sign(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(signature), nil
}

func (codec *CursorCodec) Decode(raw string, expected CursorExpectation) (CursorPosition, error) {
	if codec == nil || len(codec.key) < sha256.Size {
		return CursorPosition{}, ErrInvalidCursor
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return CursorPosition{}, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return CursorPosition{}, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, codec.sign(payload)) {
		return CursorPosition{}, ErrInvalidCursor
	}
	var envelope struct {
		Version  int            `json:"version"`
		Position CursorPosition `json:"position"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil ||
		envelope.Version != 2 ||
		!validPosition(envelope.Position) ||
		envelope.Position.Resource != expected.Resource ||
		envelope.Position.PrincipalID != expected.PrincipalID ||
		envelope.Position.Mode != expected.Mode ||
		envelope.Position.ContractVersion != expected.ContractVersion ||
		envelope.Position.Fingerprint != expected.Fingerprint {
		return CursorPosition{}, ErrInvalidCursor
	}
	envelope.Position.OccurredAt = envelope.Position.OccurredAt.UTC()
	return envelope.Position, nil
}

func (codec *CursorCodec) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func validPosition(position CursorPosition) bool {
	switch position.Resource {
	case Opportunities, Alerts, Executions, Positions, Activities, AuditEvents, FundingPayments:
	default:
		return false
	}
	return strings.TrimSpace(position.PrincipalID) != "" &&
		(position.Mode == "test" || position.Mode == "live") &&
		position.ContractVersion == ContractVersion &&
		strings.TrimSpace(position.Fingerprint) != "" &&
		strings.TrimSpace(position.SortValue) != "" &&
		strings.TrimSpace(position.ID) != ""
}

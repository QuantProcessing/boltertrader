package secretstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type Mode string

const (
	ModeTest Mode = "test"
	ModeLive Mode = "live"
)

var (
	ErrInvalidScope       = errors.New("invalid secret store scope")
	ErrInvalidOperationID = errors.New("invalid secret store operation id")
	ErrInvalidReference   = errors.New("invalid secret store reference")
)

type Scope struct {
	Mode       Mode
	Namespace  string
	KeyVersion string
}

func (scope Scope) Validate() error {
	if scope.KeyVersion != "v1" {
		return ErrInvalidScope
	}
	switch scope.Mode {
	case ModeTest:
		if scope.Namespace != "boltertrader/test" {
			return ErrInvalidScope
		}
	case ModeLive:
		if scope.Namespace != "boltertrader/live" {
			return ErrInvalidScope
		}
	default:
		return ErrInvalidScope
	}
	return nil
}

type OperationID string

func ParseOperationID(raw string) (OperationID, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.String() != raw {
		return "", ErrInvalidOperationID
	}
	return OperationID(raw), nil
}

func (operation OperationID) String() string { return string(operation) }

type OpaqueRef string

func ParseOpaqueRef(raw string) (OpaqueRef, error) {
	if len(raw) != 43 {
		return "", ErrInvalidReference
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return "", ErrInvalidReference
	}
	return OpaqueRef(raw), nil
}

func (reference OpaqueRef) String() string {
	if reference == "" {
		return "secretstore.OpaqueRef{redacted, invalid}"
	}
	return "secretstore.OpaqueRef{value:redacted}"
}

func (reference OpaqueRef) GoString() string { return reference.String() }

func (reference OpaqueRef) MarshalJSON() ([]byte, error) { return json.Marshal(reference.String()) }

// RawForTrustedBoundary returns the canonical reference for provider storage or
// internal transport. Ordinary formatting must never use this accessor.
func (reference OpaqueRef) RawForTrustedBoundary() string { return string(reference) }

type CredentialWriter interface {
	Check(context.Context, Scope) error
	Put(context.Context, Scope, OperationID, []byte) (OpaqueRef, error)
	LookupOperation(context.Context, Scope, OperationID) (OpaqueRef, error)
	Delete(context.Context, Scope, OpaqueRef) error
}

type CredentialResolver interface {
	Check(context.Context, Scope) error
	Resolve(context.Context, Scope, OpaqueRef) ([]byte, error)
}

func (scope Scope) String() string {
	return fmt.Sprintf("secretstore.Scope{Mode:%q Namespace:%q KeyVersion:%q}", scope.Mode, scope.Namespace, scope.KeyVersion)
}

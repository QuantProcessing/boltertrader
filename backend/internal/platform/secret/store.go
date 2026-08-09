package secret

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

type Mode = secretstore.Mode

const (
	ModeTest      = secretstore.ModeTest
	ModeLive      = secretstore.ModeLive
	TestNamespace = "boltertrader/test"
	LiveNamespace = "boltertrader/live"
)

type Code string

const (
	CodeKeyMissing            Code = "SECRET_STORE_KEY_MISSING"
	CodeKeyMismatch           Code = "SECRET_STORE_KEY_MISMATCH"
	CodeKeyVersionUnsupported Code = "SECRET_STORE_KEY_VERSION_UNSUPPORTED"
	CodeUnavailable           Code = "SECRET_STORE_UNAVAILABLE"
)

var (
	ErrKeyMissing            = errors.New("secret store key missing")
	ErrKeyMismatch           = errors.New("secret store key mismatch")
	ErrKeyVersionUnsupported = errors.New("secret store key version unsupported")
)

type Error struct{ code Code }

func (err *Error) Error() string { return string(err.code) }
func (err *Error) Code() Code    { return err.code }

func IsCode(err error, code Code) bool {
	var storeErr *Error
	return errors.As(err, &storeErr) && storeErr.code == code
}

type Config struct {
	Mode       Mode
	Namespace  string
	KeyVersion string
}

type Scope = secretstore.Scope
type Provider = secretstore.CredentialWriter

type Store struct {
	provider secretstore.CredentialWriter
	scope    secretstore.Scope
}

func NewStore(config Config, provider secretstore.CredentialWriter) (*Store, error) {
	if provider == nil {
		return nil, coded(CodeUnavailable)
	}
	scope := secretstore.Scope{Mode: config.Mode, Namespace: config.Namespace, KeyVersion: config.KeyVersion}
	if err := scope.Validate(); err != nil {
		if config.KeyVersion != "v1" {
			return nil, coded(CodeKeyVersionUnsupported)
		}
		return nil, coded(CodeKeyMismatch)
	}
	return &Store{provider: provider, scope: scope}, nil
}

func (store *Store) Readiness(ctx context.Context) error {
	return mapProviderError(store.provider.Check(ctx, store.scope))
}

func (store *Store) Put(ctx context.Context, operationID secretstore.OperationID, value []byte) (Reference, error) {
	if operationID == "" || len(value) == 0 {
		return Reference{}, coded(CodeUnavailable)
	}
	providerValue := append([]byte(nil), value...)
	defer clear(providerValue)
	raw, err := store.provider.Put(ctx, store.scope, operationID, providerValue)
	if err != nil || raw == "" {
		return Reference{}, mapProviderError(err)
	}
	return newReference(raw, store.scope), nil
}

func (store *Store) LookupOperation(ctx context.Context, operationID secretstore.OperationID) (Reference, error) {
	raw, err := store.provider.LookupOperation(ctx, store.scope, operationID)
	if err != nil || raw == "" {
		return Reference{}, mapProviderError(err)
	}
	return newReference(raw, store.scope), nil
}

func (store *Store) Delete(ctx context.Context, rawReference string) error {
	ref, err := secretstore.ParseOpaqueRef(rawReference)
	if err != nil {
		return coded(CodeUnavailable)
	}
	return mapProviderError(store.provider.Delete(ctx, store.scope, ref))
}

type Reference struct {
	raw        secretstore.OpaqueRef
	namespace  string
	keyVersion string
}

func newReference(raw secretstore.OpaqueRef, scope secretstore.Scope) Reference {
	return Reference{raw: raw, namespace: scope.Namespace, keyVersion: scope.KeyVersion}
}

func (ref Reference) IsZero() bool           { return ref.raw == "" }
func (ref Reference) Namespace() string      { return ref.namespace }
func (ref Reference) KeyVersion() string     { return ref.keyVersion }
func (ref Reference) RawForProvider() string { return ref.raw.RawForTrustedBoundary() }
func (ref Reference) GoString() string       { return ref.String() }
func (ref Reference) String() string {
	if ref.IsZero() {
		return "secret.Reference{invalid, redacted}"
	}
	return fmt.Sprintf("secret.Reference{namespace:%s, keyVersion:%s, value:redacted}", ref.namespace, ref.keyVersion)
}

func mapProviderError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrKeyMissing):
		return coded(CodeKeyMissing)
	case errors.Is(err, ErrKeyMismatch), errors.Is(err, secretstore.ErrInvalidScope):
		return coded(CodeKeyMismatch)
	case errors.Is(err, ErrKeyVersionUnsupported):
		return coded(CodeKeyVersionUnsupported)
	default:
		return coded(CodeUnavailable)
	}
}

func coded(code Code) error { return &Error{code: code} }

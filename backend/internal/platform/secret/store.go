package secret

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type Mode string

const (
	ModeTest Mode = "test"
	ModeLive Mode = "live"
)

const (
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

type Error struct {
	code Code
}

func (err *Error) Error() string { return string(err.code) }

func (err *Error) Code() Code { return err.code }

func IsCode(err error, code Code) bool {
	var storeErr *Error
	return errors.As(err, &storeErr) && storeErr.code == code
}

type Config struct {
	Mode       Mode
	Namespace  string
	KeyVersion string
}

type Scope struct {
	Mode       Mode
	Namespace  string
	KeyVersion string
}

type Provider interface {
	Check(context.Context, Scope) error
	Put(context.Context, Scope, []byte) (string, error)
}

type Store struct {
	provider Provider
	scope    Scope
}

func NewStore(config Config, provider Provider) (*Store, error) {
	if provider == nil {
		return nil, coded(CodeUnavailable)
	}
	wantNamespace := ""
	switch config.Mode {
	case ModeTest:
		wantNamespace = TestNamespace
	case ModeLive:
		wantNamespace = LiveNamespace
	default:
		return nil, coded(CodeKeyMismatch)
	}
	if config.Namespace != wantNamespace {
		return nil, coded(CodeKeyMismatch)
	}
	if config.KeyVersion != "v1" {
		return nil, coded(CodeKeyVersionUnsupported)
	}
	return &Store{
		provider: provider,
		scope: Scope{
			Mode:       config.Mode,
			Namespace:  config.Namespace,
			KeyVersion: config.KeyVersion,
		},
	}, nil
}

func (store *Store) Readiness(ctx context.Context) error {
	if err := store.provider.Check(ctx, store.scope); err != nil {
		return mapProviderError(err)
	}
	return nil
}

func (store *Store) Put(ctx context.Context, value []byte) (Reference, error) {
	if len(value) == 0 {
		return Reference{}, coded(CodeUnavailable)
	}
	providerValue := append([]byte(nil), value...)
	defer clear(providerValue)
	raw, err := store.provider.Put(ctx, store.scope, providerValue)
	if err != nil || strings.TrimSpace(raw) == "" {
		return Reference{}, mapProviderError(err)
	}
	return Reference{
		raw:        raw,
		namespace:  store.scope.Namespace,
		keyVersion: store.scope.KeyVersion,
	}, nil
}

type Reference struct {
	raw        string
	namespace  string
	keyVersion string
}

func (ref Reference) IsZero() bool { return ref.raw == "" }

func (ref Reference) Namespace() string { return ref.namespace }

func (ref Reference) KeyVersion() string { return ref.keyVersion }

func (ref Reference) RawForProvider() string { return ref.raw }

func (ref Reference) String() string {
	if ref.IsZero() {
		return "secret.Reference{invalid, redacted}"
	}
	return fmt.Sprintf("secret.Reference{namespace:%s, keyVersion:%s, value:redacted}", ref.namespace, ref.keyVersion)
}

func (ref Reference) GoString() string { return ref.String() }

func mapProviderError(err error) error {
	switch {
	case errors.Is(err, ErrKeyMissing):
		return coded(CodeKeyMissing)
	case errors.Is(err, ErrKeyMismatch):
		return coded(CodeKeyMismatch)
	case errors.Is(err, ErrKeyVersionUnsupported):
		return coded(CodeKeyVersionUnsupported)
	default:
		return coded(CodeUnavailable)
	}
}

func coded(code Code) error { return &Error{code: code} }

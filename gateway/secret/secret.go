package secret

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Ref struct {
	raw    string
	scheme string
	name   string
}

func ParseRef(value string) (Ref, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return Ref{}, fmt.Errorf("invalid secret reference")
	}
	switch parsed.Scheme {
	case "env":
		if parsed.Path != "" {
			return Ref{}, fmt.Errorf("invalid secret reference")
		}
	case "vault", "kms":
	default:
		return Ref{}, fmt.Errorf("unsupported secret reference scheme")
	}
	name := parsed.Host
	if parsed.Path != "" {
		name += parsed.Path
	}
	return Ref{raw: value, scheme: parsed.Scheme, name: name}, nil
}

func (ref Ref) IsZero() bool {
	return ref.raw == ""
}

func (ref Ref) Scheme() string {
	return ref.scheme
}

func (ref Ref) Fingerprint() string {
	if ref.raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ref.raw))
	return ref.scheme + ":" + hex.EncodeToString(sum[:])
}

func (ref Ref) RawForProvider() string {
	return ref.raw
}

func (ref Ref) String() string {
	if ref.scheme == "" {
		return "secret.Ref{redacted, invalid}"
	}
	return "secret.Ref{scheme:" + ref.scheme + ", value:redacted}"
}

func (ref Ref) GoString() string {
	return ref.String()
}

func (ref Ref) MarshalJSON() ([]byte, error) {
	return json.Marshal(ref.String())
}

func (ref *Ref) UnmarshalYAML(unmarshal func(any) error) error {
	var value string
	if err := unmarshal(&value); err != nil {
		return err
	}
	parsed, err := ParseRef(value)
	if err != nil {
		return err
	}
	*ref = parsed
	return nil
}

type Provider interface {
	Resolve(context.Context, Ref) (string, error)
}

type EnvProvider struct {
	allow map[string]struct{}
}

func NewEnvProvider(allowed []string) EnvProvider {
	provider := EnvProvider{allow: make(map[string]struct{}, len(allowed))}
	for _, value := range allowed {
		ref, err := ParseRef(value)
		if err == nil && ref.scheme == "env" {
			provider.allow[ref.raw] = struct{}{}
		}
	}
	return provider
}

func (provider EnvProvider) Resolve(ctx context.Context, ref Ref) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("secret resolution canceled")
	}
	if ref.scheme != "env" {
		return "", fmt.Errorf("secret provider cannot resolve reference")
	}
	if _, ok := provider.allow[ref.raw]; !ok {
		return "", fmt.Errorf("secret reference is not allowlisted")
	}
	value, ok := os.LookupEnv(ref.name)
	if !ok {
		return "", fmt.Errorf("secret reference is unset")
	}
	return value, nil
}

type StaticProvider map[string]string

func (provider StaticProvider) Resolve(ctx context.Context, ref Ref) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("secret resolution canceled")
	}
	value, ok := provider[ref.raw]
	if !ok {
		return "", fmt.Errorf("secret reference is unset")
	}
	return value, nil
}

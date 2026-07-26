package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

type BearerMiddleware struct {
	tokenHash [sha256.Size]byte
	principal domain.PrincipalID
	identity  *domain.IdentityGenerator
}

type principalContextKey struct{}

func NewBearerMiddleware(ctx context.Context, provider secret.Provider, ref secret.Ref) (BearerMiddleware, error) {
	if provider == nil {
		return BearerMiddleware{}, fmt.Errorf("auth provider is required")
	}
	token, err := provider.Resolve(ctx, ref)
	if err != nil {
		return BearerMiddleware{}, fmt.Errorf("resolve auth token")
	}
	if token == "" {
		return BearerMiddleware{}, fmt.Errorf("auth token is invalid")
	}
	hash := sha256.Sum256([]byte(token))
	return BearerMiddleware{
		tokenHash: hash,
		principal: domain.PrincipalID("token-" + hex.EncodeToString(hash[:8])),
		identity:  domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	}, nil
}

func (middleware BearerMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !middleware.authorized(r.Header.Get("Authorization")) {
			writeUnauthorized(w, middleware.correlationID())
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, middleware.principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func Principal(ctx context.Context) (domain.PrincipalID, bool) {
	if ctx == nil {
		return "", false
	}
	principal, ok := ctx.Value(principalContextKey{}).(domain.PrincipalID)
	return principal, ok && principal.Validate() == nil
}

// PrincipalID returns the stable non-secret identity derived from the bearer
// token. It is safe to use for durable ownership and private event filtering.
func (middleware BearerMiddleware) PrincipalID() domain.PrincipalID {
	return middleware.principal
}

func (middleware BearerMiddleware) authorized(header string) bool {
	scheme, token, ok := splitBearerAuthorization(header)
	if !ok || !equalASCIIFold(scheme, "Bearer") || token == "" {
		return false
	}
	candidate := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(candidate[:], middleware.tokenHash[:]) == 1
}

func splitBearerAuthorization(header string) (string, string, bool) {
	for i := 0; i < len(header); i++ {
		if header[i] == ' ' {
			if i == 0 || i == len(header)-1 {
				return "", "", false
			}
			token := header[i+1:]
			for j := 0; j < len(token); j++ {
				if token[j] == ' ' || token[j] == '\t' || token[j] == '\r' || token[j] == '\n' {
					return "", "", false
				}
			}
			return header[:i], token, true
		}
		if header[i] == '\t' || header[i] == '\r' || header[i] == '\n' {
			return "", "", false
		}
	}
	return "", "", false
}

func equalASCIIFold(got string, want string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := 0; i < len(got); i++ {
		c := got[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		w := want[i]
		if 'A' <= w && w <= 'Z' {
			w += 'a' - 'A'
		}
		if c != w {
			return false
		}
	}
	return true
}

func (middleware BearerMiddleware) String() string {
	return "auth.BearerMiddleware{token:redacted}"
}

func (middleware BearerMiddleware) GoString() string { return middleware.String() }

func (middleware BearerMiddleware) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"token": "redacted"})
}

func (middleware BearerMiddleware) correlationID() string {
	if middleware.identity == nil {
		return ""
	}
	id, err := middleware.identity.EventID()
	if err != nil {
		return ""
	}
	return string(id)
}

func writeUnauthorized(w http.ResponseWriter, correlationID string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":           "unauthorized",
			"message":        "unauthorized",
			"correlation_id": correlationID,
			"retryable":      false,
		},
	})
}

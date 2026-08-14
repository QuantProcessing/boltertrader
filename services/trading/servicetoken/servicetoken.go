// Package servicetoken authenticates callers that reach the trading service
// over the wire rather than through an in-process mount.
//
// The trading service does not authenticate users. It never has: the auth edge
// resolves a session and tells trading who is asking. That arrangement survives
// the split by moving from a function call to two HTTP headers, and this
// package is the receiving end — it proves the *caller* is the auth edge (a
// shared service token) and then trusts the identity that caller asserts.
//
// The whole design rests on the token being unreachable from a browser. A
// caller that cannot present it cannot assert an identity, which is why the
// identity headers are stripped from every request before the token is even
// checked: a request that arrives carrying one is either a misconfigured proxy
// or someone probing, and in both cases the header must not survive to be read
// as authority.
package servicetoken

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
)

const (
	// PrincipalHeader and ModeHeader carry the identity the trusted caller has
	// already resolved. They are meaningful only alongside a valid service
	// token; on their own they are inert.
	PrincipalHeader = "X-Bolter-Principal"
	ModeHeader      = "X-Bolter-Principal-Mode"
)

// ErrUnauthenticated is what the injected identity functions report when a
// request reaches a handler without a verified principal. Reaching a handler at
// all means Wrap accepted the token, so in practice this means the trusted
// caller sent a token but no identity.
var ErrUnauthenticated = errors.New("trading caller identity is absent")

// Authenticator verifies the service token and carries the asserted identity
// from the middleware to the handlers.
type Authenticator struct {
	tokenHash [sha256.Size]byte
	mode      string
}

type identity struct {
	principal string
	mode      string
}

type identityKey struct{}

// New builds the authenticator. The token is required to be at least 32 bytes
// because a shorter one is not a service token — it is a password someone typed,
// and this credential is never typed.
//
// mode is the mode this process runs in. An identity asserting a different one
// is rejected rather than honoured: a live principal arriving at a test process
// means the deployment is crossed, and executing its orders under either mode
// would be wrong.
func New(token []byte, mode string) (*Authenticator, error) {
	if len(token) < 32 {
		return nil, fmt.Errorf("trading service token is invalid")
	}
	if mode != "test" && mode != "live" {
		return nil, fmt.Errorf("trading service mode is invalid")
	}
	return &Authenticator{tokenHash: sha256.Sum256(token), mode: mode}, nil
}

// Wrap rejects any request that does not carry the service token, and strips
// the identity headers before deciding anything so that no handler downstream
// can read an identity this package did not put there.
func (authenticator *Authenticator) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asserted := identity{
			principal: strings.TrimSpace(r.Header.Get(PrincipalHeader)),
			mode:      strings.TrimSpace(r.Header.Get(ModeHeader)),
		}
		r.Header.Del(PrincipalHeader)
		r.Header.Del(ModeHeader)

		if !authenticator.authorized(r.Header.Get("Authorization")) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "request rejected", nil, false, httpx.RequestID(r))
			return
		}
		if asserted.principal == "" || asserted.mode != authenticator.mode {
			// A valid token with no usable identity is a caller bug, not an
			// attack: the trusted caller is expected to resolve identity before
			// forwarding. Handlers still answer 401 for their own reasons, so
			// this stays out of the way and lets them decide.
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), asserted)))
	})
}

// Principal is the identity function the trading assembly injects. It reads
// only what Wrap established, never a header.
func (authenticator *Authenticator) Principal(r *http.Request) (string, string, error) {
	asserted, ok := fromContext(r.Context())
	if !ok {
		return "", "", ErrUnauthenticated
	}
	return asserted.principal, asserted.mode, nil
}

// Authenticate answers the audit surface, which needs only the principal.
func (authenticator *Authenticator) Authenticate(r *http.Request) (string, error) {
	principal, _, err := authenticator.Principal(r)
	return principal, err
}

// AuthorizeMutation permits any authenticated caller to write. There is no
// second check here on purpose: the trusted caller already decided whether this
// principal may mutate — for the auth edge that is a CSRF token bound to a
// browser session — and re-deciding it here without the browser's context would
// mean inventing a weaker rule and calling it a stronger one.
func (authenticator *Authenticator) AuthorizeMutation(r *http.Request) error {
	_, _, err := authenticator.Principal(r)
	return err
}

func (authenticator *Authenticator) authorized(header string) bool {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return false
	}
	candidate := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(candidate[:], authenticator.tokenHash[:]) == 1
}

// String, GoString and MarshalJSON keep the token hash out of any log line or
// error that happens to render the authenticator.
func (authenticator *Authenticator) String() string {
	return "servicetoken.Authenticator{token:redacted}"
}

func (authenticator *Authenticator) GoString() string { return authenticator.String() }

func (authenticator *Authenticator) MarshalJSON() ([]byte, error) {
	return []byte(`{"token":"redacted"}`), nil
}

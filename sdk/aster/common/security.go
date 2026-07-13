package common

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

const (
	domainName              = "AsterSignTransaction"
	domainVersion           = "1"
	domainVerifyingContract = "0x0000000000000000000000000000000000000000"
)

var reservedAuthFields = map[string]struct{}{
	"user": {}, "signer": {}, "nonce": {}, "timestamp": {}, "signature": {},
}

type CredentialConfig struct {
	User           string
	PrivateKey     string
	ExpectedSigner string
}

type SecurityOption func(*securityOptions) error

type securityOptions struct {
	clock Clock
}

func WithClock(clock Clock) SecurityOption {
	return func(options *securityOptions) error {
		if clock == nil {
			return fmt.Errorf("aster security: clock is required")
		}
		options.clock = clock
		return nil
	}
}

type SecurityContext struct {
	user       ethcommon.Address
	signer     ethcommon.Address
	privateKey *ecdsa.PrivateKey
	clock      Clock
	nonces     *NonceCoordinator
}

func NewSecurityContext(config CredentialConfig, options ...SecurityOption) (*SecurityContext, error) {
	if !ethcommon.IsHexAddress(config.User) {
		return nil, fmt.Errorf("aster security: invalid user address")
	}
	user := ethcommon.HexToAddress(config.User)
	if user == (ethcommon.Address{}) {
		return nil, fmt.Errorf("aster security: zero user address is not allowed")
	}

	privateKeyHex := strings.TrimPrefix(strings.TrimSpace(config.PrivateKey), "0x")
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("aster security: invalid API-wallet private key")
	}
	signer := crypto.PubkeyToAddress(privateKey.PublicKey)
	if config.ExpectedSigner != "" {
		if !ethcommon.IsHexAddress(config.ExpectedSigner) {
			return nil, fmt.Errorf("aster security: invalid expected signer address")
		}
		if signer != ethcommon.HexToAddress(config.ExpectedSigner) {
			return nil, fmt.Errorf("aster security: derived signer does not match expected signer")
		}
	}

	settings := securityOptions{clock: systemClock{}}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&settings); err != nil {
			return nil, err
		}
	}
	return &SecurityContext{
		user:       user,
		signer:     signer,
		privateKey: privateKey,
		clock:      settings.clock,
		nonces:     NewNonceCoordinator(settings.clock),
	}, nil
}

func (s *SecurityContext) User() string { return s.user.Hex() }

func (s *SecurityContext) Signer() string { return s.signer.Hex() }

type SignedParams struct {
	values url.Values
	digest string
}

func (s SignedParams) Values() url.Values { return cloneValues(s.values) }

func (s SignedParams) Digest() string { return s.digest }

func (s SignedParams) Encode() string { return s.values.Encode() }

func (s SignedParams) String() string { return "aster signed params{query=<redacted>}" }

func (s SignedParams) GoString() string { return s.String() }

func (s *SecurityContext) Sign(profile Profile, params url.Values) (SignedParams, error) {
	if s == nil || s.privateKey == nil {
		return SignedParams{}, fmt.Errorf("aster security: credentials are required")
	}
	if err := profile.Validate(); err != nil {
		return SignedParams{}, err
	}
	values := cloneValues(params)
	for key := range values {
		if _, reserved := reservedAuthFields[strings.ToLower(key)]; reserved {
			return SignedParams{}, fmt.Errorf("aster security: caller supplied reserved auth field %q", key)
		}
	}

	now := s.clock.Now()
	if now.IsZero() || now.UnixMicro() <= 0 {
		return SignedParams{}, fmt.Errorf("aster security: clock returned an invalid time")
	}
	nonce := s.nonces.nextAt(now)
	values.Set("user", s.user.Hex())
	values.Set("signer", s.signer.Hex())
	values.Set("nonce", strconv.FormatInt(nonce, 10))
	values.Set("timestamp", strconv.FormatInt(now.UnixMilli(), 10))

	canonical := values.Encode()
	digest, _, err := apitypes.TypedDataAndHash(apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"Message": {{Name: "msg", Type: "string"}},
		},
		PrimaryType: "Message",
		Domain: apitypes.TypedDataDomain{
			Name:              domainName,
			Version:           domainVersion,
			ChainId:           math.NewHexOrDecimal256(profile.chainID),
			VerifyingContract: domainVerifyingContract,
		},
		Message: apitypes.TypedDataMessage{"msg": canonical},
	})
	if err != nil {
		return SignedParams{}, fmt.Errorf("aster security: hash typed data: %w", err)
	}
	signature, err := crypto.Sign(digest, s.privateKey)
	if err != nil {
		return SignedParams{}, fmt.Errorf("aster security: sign typed data: %w", err)
	}
	if signature[64] < 27 {
		signature[64] += 27
	}
	values.Set("signature", "0x"+hex.EncodeToString(signature))
	return SignedParams{
		values: values,
		digest: "0x" + hex.EncodeToString(digest),
	}, nil
}

func (s *SecurityContext) String() string {
	if s == nil {
		return "aster security{<nil>}"
	}
	return fmt.Sprintf("aster security{user=%s signer=%s privateKey=<redacted>}", redactAddress(s.user), redactAddress(s.signer))
}

func (s *SecurityContext) GoString() string { return s.String() }

func redactAddress(address ethcommon.Address) string {
	value := address.Hex()
	return value[:8] + "..." + value[len(value)-4:]
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, source := range values {
		cloned[key] = append([]string(nil), source...)
	}
	return cloned
}

var _ Clock = ClockFunc(nil)

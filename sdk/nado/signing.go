package nado

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// Domain separator constants
const (
	EIP712DomainName    = "Nado"
	EIP712DomainVersion = "0.0.1"
)

// TypedData structure definitions for EIP-712

var OrderTypes = []apitypes.Type{
	{Name: "sender", Type: "bytes32"},
	{Name: "priceX18", Type: "int128"},
	{Name: "amount", Type: "int128"},
	{Name: "expiration", Type: "uint64"},
	{Name: "nonce", Type: "uint64"},
	{Name: "appendix", Type: "uint128"},
}

var CancelOrdersTypes = []apitypes.Type{
	{Name: "sender", Type: "bytes32"},
	{Name: "productIds", Type: "uint32[]"},
	{Name: "digests", Type: "bytes32[]"},
	{Name: "nonce", Type: "uint64"},
}

var CancelProductOrdersTypes = []apitypes.Type{
	{Name: "sender", Type: "bytes32"},
	{Name: "productIds", Type: "uint32[]"},
	{Name: "nonce", Type: "uint64"},
}

var StreamAuthenticationTypes = []apitypes.Type{
	{Name: "sender", Type: "bytes32"},
	{Name: "expiration", Type: "uint64"},
}

// Signer handles EIP-712 signing
type Signer struct {
	privateKey *ecdsa.PrivateKey
	chainId    *big.Int
}

func NewSigner(privateKeyHex string, chainID int64) (*Signer, error) {
	if chainID <= 0 {
		return nil, fmt.Errorf("nado signer: a positive chain id is required")
	}
	privateKeyHex = strings.TrimPrefix(privateKeyHex, "0x")
	pk, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	return &Signer{
		privateKey: pk,
		chainId:    big.NewInt(chainID),
	}, nil
}

func (s *Signer) ChainID() int64 {
	if s == nil || s.chainId == nil {
		return 0
	}
	return s.chainId.Int64()
}

func (s *Signer) GetAddress() common.Address {
	return crypto.PubkeyToAddress(s.privateKey.PublicKey)
}

// BuildSender constructs the bytes32 sender field (Address + SubAccount)
func BuildSender(address common.Address, subAccountName string) string {
	// Address is 20 bytes
	// SubAccount is 12 bytes
	// Result is 32 bytes hex string

	// Convert subAccountName to bytes, pad to 12 bytes
	subAccountBytes := make([]byte, 12)

	// Copy the subaccount name bytes. If shorter than 12, the rest remains 0.
	// If longer, it will be truncated (copy handles this safely by copying min length).
	copy(subAccountBytes, []byte(subAccountName))

	// Concatenate
	senderBytes := append(address.Bytes(), subAccountBytes...)
	return "0x" + hex.EncodeToString(senderBytes)
}

// SignOrder signs an order and returns signature and digest
func (s *Signer) SignOrder(order TxOrder, verifyingContract string) (string, string, error) {
	domain, err := s.domain(verifyingContract)
	if err != nil {
		return "", "", err
	}
	sender32, err := decodeSender(order.Sender)
	if err != nil {
		return "", "", err
	}
	priceX18, err := parseInteger("priceX18", order.PriceX18, true, 128)
	if err != nil {
		return "", "", err
	}
	amount, err := parseInteger("amount", order.Amount, true, 128)
	if err != nil {
		return "", "", err
	}
	expiration, err := parseInteger("expiration", order.Expiration, false, 64)
	if err != nil {
		return "", "", err
	}
	nonce, err := parseInteger("nonce", order.Nonce, false, 64)
	if err != nil {
		return "", "", err
	}
	appendix, err := parseInteger("appendix", order.Appendix, false, 128)
	if err != nil {
		return "", "", err
	}

	message := map[string]interface{}{
		"sender":     sender32,
		"priceX18":   (*math.HexOrDecimal256)(priceX18),
		"amount":     (*math.HexOrDecimal256)(amount),
		"expiration": (*math.HexOrDecimal256)(expiration),
		"nonce":      (*math.HexOrDecimal256)(nonce),
		"appendix":   (*math.HexOrDecimal256)(appendix),
	}

	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"Order": OrderTypes,
		},
		PrimaryType: "Order",
		Domain:      domain,
		Message:     message,
	}

	sig, digest, err := s.signTypedData(typedData)
	return sig, digest, err
}

// SignCancelProductOrders signs a batch cancel request
func (s *Signer) SignCancelProductOrders(tx TxCancelProductOrders, verifyingContract string) (string, error) {
	domain, err := s.domain(verifyingContract)
	if err != nil {
		return "", err
	}
	nonce, err := parseInteger("nonce", tx.Nonce, false, 64)
	if err != nil {
		return "", err
	}
	sender32, err := decodeSender(tx.Sender)
	if err != nil {
		return "", err
	}

	productIds := make([]*math.HexOrDecimal256, len(tx.ProductIds))
	for i, pid := range tx.ProductIds {
		if pid < 0 || pid > int64(^uint32(0)) {
			return "", fmt.Errorf("nado signer: product id %d is outside uint32", pid)
		}
		productIds[i] = math.NewHexOrDecimal256(int64(pid))
	}

	message := map[string]interface{}{
		"sender":     sender32,
		"productIds": productIds,
		"nonce":      (*math.HexOrDecimal256)(nonce),
	}

	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"CancellationProducts": CancelProductOrdersTypes,
		},
		PrimaryType: "CancellationProducts",
		Domain:      domain,
		Message:     message,
	}

	sig, _, err := s.signTypedData(typedData)
	return sig, err
}

// SignStreamAuthentication signs a stream auth request
func (s *Signer) SignStreamAuthentication(tx TxStreamAuth, verifyingContract string) (string, error) {
	domain, err := s.domain(verifyingContract)
	if err != nil {
		return "", err
	}
	expiration, err := parseInteger("expiration", tx.Expiration, false, 64)
	if err != nil {
		return "", err
	}
	sender32, err := decodeSender(tx.Sender)
	if err != nil {
		return "", err
	}

	message := map[string]interface{}{
		"sender":     sender32,
		"expiration": (*math.HexOrDecimal256)(expiration),
	}

	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"StreamAuthentication": StreamAuthenticationTypes,
		},
		PrimaryType: "StreamAuthentication",
		Domain:      domain,
		Message:     message,
	}

	sig, _, err := s.signTypedData(typedData)
	return sig, err
}

func (s *Signer) signTypedData(typedData apitypes.TypedData) (string, string, error) {
	if s == nil || s.privateKey == nil || s.chainId == nil {
		return "", "", fmt.Errorf("nado signer: signer is not initialized")
	}
	hash, _, err := apitypes.TypedDataAndHash(typedData)
	if err != nil {
		return "", "", fmt.Errorf("hash typed data: %w", err)
	}

	signature, err := crypto.Sign(hash, s.privateKey)
	if err != nil {
		return "", "", fmt.Errorf("sign hash: %w", err)
	}

	// Add 27 to recovery ID (v) to match legacy eth signature format if needed,
	// but standard Ethereum signature is enough usually. EIP-712 usually expects standard 65 byte sig.
	if signature[64] < 27 {
		signature[64] += 27
	}

	return "0x" + hex.EncodeToString(signature), "0x" + hex.EncodeToString(hash), nil
}

// SignCancelOrders signs a batch cancel request by digests
func (s *Signer) SignCancelOrders(tx TxCancelOrders, verifyingContract string) (string, error) {
	domain, err := s.domain(verifyingContract)
	if err != nil {
		return "", err
	}
	nonce, err := parseInteger("nonce", tx.Nonce, false, 64)
	if err != nil {
		return "", err
	}
	sender32, err := decodeSender(tx.Sender)
	if err != nil {
		return "", err
	}

	productIds := make([]*math.HexOrDecimal256, len(tx.ProductIds))
	for i, pid := range tx.ProductIds {
		if pid < 0 || pid > int64(^uint32(0)) {
			return "", fmt.Errorf("nado signer: product id %d is outside uint32", pid)
		}
		productIds[i] = math.NewHexOrDecimal256(int64(pid))
	}

	digests := make([][32]byte, len(tx.Digests))
	for i, d := range tx.Digests {
		dBytes, err := hex.DecodeString(strings.TrimPrefix(d, "0x"))
		if err != nil {
			return "", fmt.Errorf("invalid digest hex at index %d: %w", i, err)
		}
		if len(dBytes) != 32 {
			return "", fmt.Errorf("digest at index %d must be 32 bytes", i)
		}
		copy(digests[i][:], dBytes)
	}

	message := map[string]interface{}{
		"sender":     sender32,
		"productIds": productIds,
		"digests":    digests,
		"nonce":      (*math.HexOrDecimal256)(nonce),
	}

	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"Cancellation": CancelOrdersTypes,
		},
		PrimaryType: "Cancellation",
		Domain:      domain,
		Message:     message,
	}

	sig, _, err := s.signTypedData(typedData)
	return sig, err
}

func (s *Signer) domain(verifyingContract string) (apitypes.TypedDataDomain, error) {
	if s == nil || s.chainId == nil || s.chainId.Sign() <= 0 {
		return apitypes.TypedDataDomain{}, fmt.Errorf("nado signer: chain id is required")
	}
	if !common.IsHexAddress(verifyingContract) || common.HexToAddress(verifyingContract) == (common.Address{}) {
		return apitypes.TypedDataDomain{}, fmt.Errorf("nado signer: invalid verifying contract")
	}
	return apitypes.TypedDataDomain{
		Name:              EIP712DomainName,
		Version:           EIP712DomainVersion,
		ChainId:           (*math.HexOrDecimal256)(new(big.Int).Set(s.chainId)),
		VerifyingContract: common.HexToAddress(verifyingContract).Hex(),
	}, nil
}

func decodeSender(value string) ([32]byte, error) {
	var sender [32]byte
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil {
		return sender, fmt.Errorf("nado signer: invalid sender hex: %w", err)
	}
	if len(decoded) != len(sender) {
		return sender, fmt.Errorf("nado signer: sender must be 32 bytes")
	}
	copy(sender[:], decoded)
	return sender, nil
}

func parseInteger(name, value string, signed bool, bits uint) (*big.Int, error) {
	parsed, ok := new(big.Int).SetString(value, 10)
	if !ok || (!signed && parsed.Sign() < 0) {
		return nil, fmt.Errorf("nado signer: invalid %s", name)
	}
	if signed {
		limit := new(big.Int).Lsh(big.NewInt(1), bits-1)
		minimum := new(big.Int).Neg(limit)
		maximum := new(big.Int).Sub(new(big.Int).Set(limit), big.NewInt(1))
		if parsed.Cmp(minimum) < 0 || parsed.Cmp(maximum) > 0 {
			return nil, fmt.Errorf("nado signer: %s is outside int%d", name, bits)
		}
	} else if parsed.BitLen() > int(bits) {
		return nil, fmt.Errorf("nado signer: %s is outside uint%d", name, bits)
	}
	return parsed, nil
}

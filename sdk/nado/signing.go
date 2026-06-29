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
	ChainID             = 57073
	EndpointAddress     = "0x05ec92d78ed421f3d3ada77ffde167106565974e"
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

func NewSigner(privateKeyHex string) (*Signer, error) {
	privateKeyHex = strings.TrimPrefix(privateKeyHex, "0x")
	pk, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	return &Signer{
		privateKey: pk,
		chainId:    big.NewInt(ChainID),
	}, nil
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
	domain := apitypes.TypedDataDomain{
		Name:              EIP712DomainName,
		Version:           EIP712DomainVersion,
		ChainId:           math.NewHexOrDecimal256(s.chainId.Int64()),
		VerifyingContract: verifyingContract,
	}

	// Parse values
	amount, _ := new(big.Int).SetString(order.Amount, 10)
	priceX18, _ := new(big.Int).SetString(order.PriceX18, 10)
	nonce, _ := new(big.Int).SetString(order.Nonce, 10)
	expiration, _ := new(big.Int).SetString(order.Expiration, 10)
	appendix, _ := new(big.Int).SetString(order.Appendix, 10)

	// Convert sender hex string to [32]byte
	varsenderBytes, err := hex.DecodeString(strings.TrimPrefix(order.Sender, "0x"))
	if err != nil {
		return "", "", fmt.Errorf("invalid sender hex: %w", err)
	}
	if len(varsenderBytes) != 32 {
		return "", "", fmt.Errorf("sender must be 32 bytes")
	}
	var sender32 [32]byte
	copy(sender32[:], varsenderBytes)

	message := map[string]interface{}{
		"sender":   sender32,
		"priceX18": math.NewHexOrDecimal256(priceX18.Int64()), // Note: Ensure int128 fits in int64 for this wrapper or use big.Int if needed. SDK uses wrapper typically for JSON.
		// Wait, math.NewHexOrDecimal256 takes int64. If values exceed int64, this helper is risky.
		// Detailed check: int128 can exceed int64.
		// Let's check how go-ethereum apitypes handles big ints. content uses math.HexOrDecimal256.
		// Actually, standard apitypes.TypedData message values can be string, float, or big.Int directly for numbers.
		// But in the original code it was using math.NewHexOrDecimal256.
		// Let's stick to passing *big.Int directly if possible or check what apitypes expects.
		// Docs for apitypes.TypedDataAndHash say: "Numeric types are parsed as big.Int".
		// So we can pass *big.Int directly in the map.
		"amount":     amount, // Pass *big.Int directly to be safe
		"expiration": math.NewHexOrDecimal256(expiration.Int64()),
		"nonce":      math.NewHexOrDecimal256(nonce.Int64()),
		"appendix":   appendix, // Pass *big.Int directly
	}

	// Quick fix: The original code used math.NewHexOrDecimal256 which only accepts int64.
	// However, priceX18 (1e18 scale) will definitely overflow int64 for high prices.
	// I should use *big.Int or math.HexOrDecimal256 created from big.Int.
	// But math.HexOrDecimal256 is an alias for big.Int with custom json marshaling.
	// Let's just use *math.HexOrDecimal256 casting from *big.Int.
	// Actually, looking at go-ethereum source, HexOrDecimal256 is `type HexOrDecimal256 big.Int`.

	message = map[string]interface{}{
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
	domain := apitypes.TypedDataDomain{
		Name:              EIP712DomainName,
		Version:           EIP712DomainVersion,
		ChainId:           math.NewHexOrDecimal256(s.chainId.Int64()),
		VerifyingContract: verifyingContract,
	}

	nonce, _ := new(big.Int).SetString(tx.Nonce, 10)

	varsenderBytes, err := hex.DecodeString(strings.TrimPrefix(tx.Sender, "0x"))
	if err != nil {
		return "", fmt.Errorf("invalid sender hex: %w", err)
	}
	var sender32 [32]byte
	copy(sender32[:], varsenderBytes)

	productIds := make([]*math.HexOrDecimal256, len(tx.ProductIds))
	for i, pid := range tx.ProductIds {
		productIds[i] = math.NewHexOrDecimal256(int64(pid))
	}

	message := map[string]interface{}{
		"sender":     sender32,
		"productIds": productIds,
		"nonce":      math.NewHexOrDecimal256(nonce.Int64()),
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
	domain := apitypes.TypedDataDomain{
		Name:              EIP712DomainName,
		Version:           EIP712DomainVersion,
		ChainId:           math.NewHexOrDecimal256(s.chainId.Int64()),
		VerifyingContract: verifyingContract,
	}

	expiration, _ := new(big.Int).SetString(tx.Expiration, 10)

	varsenderBytes, err := hex.DecodeString(strings.TrimPrefix(tx.Sender, "0x"))
	if err != nil {
		return "", fmt.Errorf("invalid sender hex: %w", err)
	}
	var sender32 [32]byte
	copy(sender32[:], varsenderBytes)

	message := map[string]interface{}{
		"sender":     sender32,
		"expiration": math.NewHexOrDecimal256(expiration.Int64()),
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
	domain := apitypes.TypedDataDomain{
		Name:              EIP712DomainName,
		Version:           EIP712DomainVersion,
		ChainId:           math.NewHexOrDecimal256(s.chainId.Int64()),
		VerifyingContract: verifyingContract,
	}

	nonce, _ := new(big.Int).SetString(tx.Nonce, 10)

	varsenderBytes, err := hex.DecodeString(strings.TrimPrefix(tx.Sender, "0x"))
	if err != nil {
		return "", fmt.Errorf("invalid sender hex: %w", err)
	}
	var sender32 [32]byte
	copy(sender32[:], varsenderBytes)

	productIds := make([]*math.HexOrDecimal256, len(tx.ProductIds))
	for i, pid := range tx.ProductIds {
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
		"nonce":      math.NewHexOrDecimal256(nonce.Int64()),
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

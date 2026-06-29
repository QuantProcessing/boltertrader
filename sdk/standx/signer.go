package standx

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/mr-tron/base58"
)

type Signer struct {
	evmPrivateKey  *ecdsa.PrivateKey
	evmAddress     string
	ed25519PrivKey ed25519.PrivateKey
	ed25519PubKey  ed25519.PublicKey
	requestID      string // Base58 encoded Ed25519 Public Key
}

func NewSigner(evmPrivateKeyHex string) (*Signer, error) {
	// 1. Load EVM Private Key
	evmPrivateKeyHex = strings.TrimPrefix(evmPrivateKeyHex, "0x")
	evmPrivKey, err := crypto.HexToECDSA(evmPrivateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid EVM private key: %w", err)
	}

	// Derive Address
	publicKey := evmPrivKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("error casting public key to ECDSA")
	}
	address := crypto.PubkeyToAddress(*publicKeyECDSA).Hex()

	// 2. Generate Ephemeral Ed25519 Key Pair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 key: %w", err)
	}

	// 3. Generate RequestID (Base58 encoded Public Key)
	reqID := base58.Encode(pub)

	return &Signer{
		evmPrivateKey:  evmPrivKey,
		evmAddress:     address,
		ed25519PrivKey: priv,
		ed25519PubKey:  pub,
		requestID:      reqID,
	}, nil
}

func (s *Signer) GetEVMAddress() string {
	return s.evmAddress
}

// GetRequestID returns the Base58 encoded Ed25519 public key
func (s *Signer) GetRequestID() string {
	return s.requestID
}

// SignEVMPersonal signs a message with the EVM private key using Ethereum Personal Sign format.
func (s *Signer) SignEVMPersonal(msg string) (string, error) {
	data := []byte(msg)
	// Keccak256("\x19Ethereum Signed Message:\n" + len(msg) + msg)
	hash := crypto.Keccak256Hash(
		[]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(data), data)),
	)

	signature, err := crypto.Sign(hash.Bytes(), s.evmPrivateKey)
	if err != nil {
		return "", err
	}

	// Adjust V (last byte) to be 27 or 28 (Ethereum standard for legacy signing)
	// crypto.Sign returns 0 or 1.
	if signature[64] < 27 {
		signature[64] += 27
	}

	// Return as 0x hex string
	return "0x" + fmt.Sprintf("%x", signature), nil
}

// SignRequest generates headers for authenticated requests (REST & WS)
// Header format:
// x-request-sign-version: v1
// x-request-id: <requestId (Base58)>
// x-request-timestamp: <timestamp>
// x-request-signature: Base64(Ed25519Sign(message))
// Message format: "v1,<requestId>,<timestamp>,<payload>"
func (s *Signer) SignRequest(payload string, timestamp int64, requestIDOverride string) map[string]string {
	version := "v1"
	tsStr := strconv.FormatInt(timestamp, 10)

	id := s.requestID
	if requestIDOverride != "" {
		id = requestIDOverride
	}

	// Message Construction: "v1,requestId,timestamp,payload"
	message := fmt.Sprintf("%s,%s,%s,%s", version, id, tsStr, payload)

	// Ed25519 Sign
	sig := ed25519.Sign(s.ed25519PrivKey, []byte(message))
	sigBase64 := base64.StdEncoding.EncodeToString(sig)

	return map[string]string{
		"x-request-sign-version": version,
		"x-request-id":           id,
		"x-request-timestamp":    tsStr,
		"x-request-signature":    sigBase64,
	}
}

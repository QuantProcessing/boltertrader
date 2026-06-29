package common

import (
	"fmt"
	"hash"
	"time"

	curve "github.com/elliottech/poseidon_crypto/curve/ecgfp5"
	g "github.com/elliottech/poseidon_crypto/field/goldilocks"
	gFp5 "github.com/elliottech/poseidon_crypto/field/goldilocks_quintic_extension"
	p2 "github.com/elliottech/poseidon_crypto/hash/poseidon2_goldilocks"
	schnorr "github.com/elliottech/poseidon_crypto/signature/schnorr"
	ethCommon "github.com/ethereum/go-ethereum/common"
)

type Signer interface {
	Sign(message []byte, hFunc hash.Hash) ([]byte, error)
	CreateAuthToken(accountIndex int64, keyIndex uint8, deadline time.Time) (string, error)
}

type KeyManager interface {
	Signer
	PubKey() gFp5.Element
	PubKeyBytes() [40]byte
	PrvKeyBytes() []byte
}

type keyManager struct {
	key curve.ECgFp5Scalar
}

func NewKeyManager(b []byte) (KeyManager, error) {
	if len(b) != 40 {
		return nil, fmt.Errorf("invalid private key length")
	}
	return &keyManager{key: curve.ScalarElementFromLittleEndianBytes(b)}, nil
}

func (key *keyManager) Sign(hashedMessage []byte, hFunc hash.Hash) ([]byte, error) {
	hashedMessageAsQuinticExtension, err := gFp5.FromCanonicalLittleEndianBytes(hashedMessage)
	if err != nil {
		return nil, fmt.Errorf("failed to parse message while signing. message: %v err: %w", hashedMessage, err)
	}
	return schnorr.SchnorrSignHashedMessage(hashedMessageAsQuinticExtension, key.key).ToBytes(), nil
}

func (key *keyManager) CreateAuthToken(accountIndex int64, keyIndex uint8, deadline time.Time) (string, error) {
	message := fmt.Sprintf("%v:%v:%v", deadline.Unix(), accountIndex, keyIndex)

	msgInField, err := g.ArrayFromCanonicalLittleEndianBytes([]byte(message))
	if err != nil {
		return "", fmt.Errorf("failed to convert bytes to field element. message: %s, error: %w", message, err)
	}

	msgHash := p2.HashToQuinticExtension(msgInField).ToLittleEndianBytes()
	signatureBytes, err := key.Sign(msgHash, p2.NewPoseidon2())
	if err != nil {
		return "", err
	}
	signature := ethCommon.Bytes2Hex(signatureBytes)
	return fmt.Sprintf("%v:%v", message, signature), nil
}

func (key *keyManager) PubKey() gFp5.Element {
	return schnorr.SchnorrPkFromSk(key.key)
}

func (key *keyManager) PubKeyBytes() (res [40]byte) {
	bytes := key.PubKey().ToLittleEndianBytes()
	copy(res[:], bytes[:])
	return
}

func (key *keyManager) PrvKeyBytes() []byte {
	return key.key.ToLittleEndianBytes()
}

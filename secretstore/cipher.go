package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

var (
	ErrIntegrity   = errors.New("SECRET_STORE_INTEGRITY_FAILURE")
	ErrUnavailable = errors.New("SECRET_STORE_UNAVAILABLE")
)

// Cipher encrypts account records without a separate secret object or operation journal.
type Cipher struct{ aead cipher.AEAD }

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, ErrInvalidKeySource
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalidKeySource
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidKeySource
	}
	return &Cipher{aead: aead}, nil
}

func (box *Cipher) Seal(accountID, venue string, material []byte) ([]byte, error) {
	if box == nil || box.aead == nil || accountID == "" || venue == "" || len(material) == 0 {
		return nil, ErrUnavailable
	}
	nonce := make([]byte, box.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrUnavailable
	}
	return box.aead.Seal(nonce, nonce, material, []byte("bolter-account-v1\x00"+accountID+"\x00"+venue)), nil
}

func (box *Cipher) Open(accountID, venue string, sealed []byte) ([]byte, error) {
	if box == nil || box.aead == nil || len(sealed) < box.aead.NonceSize()+box.aead.Overhead() {
		return nil, ErrIntegrity
	}
	n := box.aead.NonceSize()
	plain, err := box.aead.Open(nil, sealed[:n], sealed[n:], []byte("bolter-account-v1\x00"+accountID+"\x00"+venue))
	if err != nil {
		return nil, ErrIntegrity
	}
	return plain, nil
}

func (*Cipher) String() string               { return "secretstore.Cipher{key:redacted}" }
func (box *Cipher) GoString() string         { return box.String() }
func (*Cipher) MarshalJSON() ([]byte, error) { return []byte(`{"key":"redacted"}`), nil }

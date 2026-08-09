package secretstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrOperationConflict = errors.New("SECRET_STORE_OPERATION_CONFLICT")
	ErrSecretNotFound    = errors.New("SECRET_STORE_NOT_FOUND")
	ErrIntegrity         = errors.New("SECRET_STORE_INTEGRITY_FAILURE")
	ErrModeKeyReuse      = errors.New("SECRET_STORE_MODE_KEY_REUSE")
	ErrUnavailable       = errors.New("SECRET_STORE_UNAVAILABLE")
)

type FilesystemConfig struct {
	StateDir  string
	Mode      Mode
	KeySource KeySource
	Now       func() time.Time
	Random    io.Reader
}

type FilesystemProvider struct {
	stateDir string
	mode     Mode
	key      []byte
	aead     cipher.AEAD
	now      func() time.Time
	random   io.Reader
	mu       sync.Mutex
}

type filesystemWriter struct{ provider *FilesystemProvider }
type filesystemResolver struct{ provider *FilesystemProvider }

type envelopeV1 struct {
	Version        int    `json:"version"`
	Nonce          string `json:"nonce"`
	Ciphertext     string `json:"ciphertext"`
	MaterialDigest string `json:"materialDigest"`
	CreatedAt      string `json:"createdAt"`
}

type providerMetadata struct {
	Version int    `json:"version"`
	Check   string `json:"check"`
}

func NewFilesystemProvider(config FilesystemConfig) (*FilesystemProvider, error) {
	if !filepath.IsAbs(config.StateDir) || config.KeySource == nil || (config.Mode != ModeTest && config.Mode != ModeLive) {
		return nil, ErrUnavailable
	}
	key, err := config.KeySource.Load(context.Background())
	if err != nil || len(key) != 32 {
		clear(key)
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidKeySource
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		clear(key)
		return nil, ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		clear(key)
		return nil, ErrUnavailable
	}
	provider := &FilesystemProvider{stateDir: filepath.Clean(config.StateDir), mode: config.Mode, key: key, aead: aead, now: config.Now, random: config.Random}
	if provider.now == nil {
		provider.now = time.Now
	}
	if provider.random == nil {
		provider.random = rand.Reader
	}
	if err := provider.bootstrap(); err != nil {
		provider.Close()
		return nil, err
	}
	return provider, nil
}

func (provider *FilesystemProvider) StateDir() string { return provider.stateDir }

func (provider *FilesystemProvider) Writer() CredentialWriter {
	return filesystemWriter{provider: provider}
}

func (provider *FilesystemProvider) Resolver() CredentialResolver {
	return filesystemResolver{provider: provider}
}

func (provider *FilesystemProvider) Close() { clear(provider.key) }

func (writer filesystemWriter) Check(ctx context.Context, scope Scope) error {
	return writer.provider.check(ctx, scope)
}

func (writer filesystemWriter) Put(ctx context.Context, scope Scope, operation OperationID, material []byte) (OpaqueRef, error) {
	return writer.provider.put(ctx, scope, operation, material)
}

func (writer filesystemWriter) LookupOperation(ctx context.Context, scope Scope, operation OperationID) (OpaqueRef, error) {
	if err := writer.provider.check(ctx, scope); err != nil || !validOperationID(operation) {
		if err != nil {
			return "", err
		}
		return "", ErrInvalidOperationID
	}
	ref := deriveReference(writer.provider.key, scope, operation)
	if _, err := os.Stat(writer.provider.objectPath(ref)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrSecretNotFound
		}
		return "", ErrUnavailable
	}
	return ref, nil
}

func (writer filesystemWriter) Delete(ctx context.Context, scope Scope, ref OpaqueRef) error {
	if err := writer.provider.checkReference(ctx, scope, ref); err != nil {
		return err
	}
	if err := os.Remove(writer.provider.objectPath(ref)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	return syncDirectory(filepath.Dir(writer.provider.objectPath(ref)))
}

func (resolver filesystemResolver) Check(ctx context.Context, scope Scope) error {
	return resolver.provider.check(ctx, scope)
}

func (resolver filesystemResolver) Resolve(ctx context.Context, scope Scope, ref OpaqueRef) ([]byte, error) {
	return resolver.provider.resolve(ctx, scope, ref)
}

func (provider *FilesystemProvider) check(ctx context.Context, scope Scope) error {
	if err := ctx.Err(); err != nil {
		return ErrUnavailable
	}
	if err := scope.Validate(); err != nil || scope.Mode != provider.mode {
		return ErrInvalidScope
	}
	return provider.verifyMetadata()
}

func (provider *FilesystemProvider) checkReference(ctx context.Context, scope Scope, ref OpaqueRef) error {
	if err := provider.check(ctx, scope); err != nil {
		return err
	}
	parsed, err := ParseOpaqueRef(ref.RawForTrustedBoundary())
	if err != nil || parsed != ref {
		return ErrInvalidReference
	}
	return nil
}

func (provider *FilesystemProvider) put(ctx context.Context, scope Scope, operation OperationID, material []byte) (OpaqueRef, error) {
	if err := provider.check(ctx, scope); err != nil {
		return "", err
	}
	if !validOperationID(operation) {
		return "", ErrInvalidOperationID
	}
	if len(material) == 0 {
		return "", ErrUnavailable
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	ref := deriveReference(provider.key, scope, operation)
	digest := materialDigest(provider.key, material)
	if existing, err := provider.readEnvelope(ref); err == nil {
		if !hmac.Equal([]byte(existing.MaterialDigest), []byte(digest)) {
			return "", ErrOperationConflict
		}
		if _, err := provider.open(scope, ref, existing); err != nil {
			return "", err
		}
		return ref, nil
	} else if !errors.Is(err, ErrSecretNotFound) {
		return "", err
	}
	nonce := make([]byte, provider.aead.NonceSize())
	if _, err := io.ReadFull(provider.random, nonce); err != nil {
		return "", ErrUnavailable
	}
	defer clear(nonce)
	ciphertext := provider.aead.Seal(nil, nonce, material, envelopeAAD(scope, ref, digest))
	defer clear(ciphertext)
	envelope := envelopeV1{Version: 1, Nonce: base64.RawURLEncoding.EncodeToString(nonce), Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext), MaterialDigest: digest, CreatedAt: provider.now().UTC().Format(time.RFC3339Nano)}
	if err := writeJSONAtomic(provider.objectPath(ref), envelope, 0o600); err != nil {
		return "", err
	}
	return ref, nil
}

func (provider *FilesystemProvider) resolve(ctx context.Context, scope Scope, ref OpaqueRef) ([]byte, error) {
	if err := provider.checkReference(ctx, scope, ref); err != nil {
		return nil, err
	}
	envelope, err := provider.readEnvelope(ref)
	if err != nil {
		return nil, err
	}
	return provider.open(scope, ref, envelope)
}

func (provider *FilesystemProvider) open(scope Scope, ref OpaqueRef, envelope envelopeV1) ([]byte, error) {
	if envelope.Version != 1 {
		return nil, ErrIntegrity
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != provider.aead.NonceSize() {
		clear(nonce)
		return nil, ErrIntegrity
	}
	defer clear(nonce)
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		clear(ciphertext)
		return nil, ErrIntegrity
	}
	defer clear(ciphertext)
	plaintext, err := provider.aead.Open(nil, nonce, ciphertext, envelopeAAD(scope, ref, envelope.MaterialDigest))
	if err != nil || !hmac.Equal([]byte(materialDigest(provider.key, plaintext)), []byte(envelope.MaterialDigest)) {
		clear(plaintext)
		return nil, ErrIntegrity
	}
	return plaintext, nil
}

func (provider *FilesystemProvider) readEnvelope(ref OpaqueRef) (envelopeV1, error) {
	data, err := os.ReadFile(provider.objectPath(ref))
	if errors.Is(err, os.ErrNotExist) {
		return envelopeV1{}, ErrSecretNotFound
	}
	if err != nil {
		return envelopeV1{}, ErrUnavailable
	}
	defer clear(data)
	var envelope envelopeV1
	if err := json.Unmarshal(data, &envelope); err != nil {
		return envelopeV1{}, ErrIntegrity
	}
	return envelope, nil
}

func (provider *FilesystemProvider) objectPath(ref OpaqueRef) string {
	sum := sha256.Sum256([]byte(ref.RawForTrustedBoundary()))
	return filepath.Join(provider.stateDir, string(provider.mode), "objects", hex.EncodeToString(sum[:])+".json")
}

func (provider *FilesystemProvider) bootstrap() error {
	if err := ensureDirectory(provider.stateDir); err != nil {
		return err
	}
	modeDir := filepath.Join(provider.stateDir, string(provider.mode))
	if err := ensureDirectory(modeDir); err != nil {
		return err
	}
	if err := ensureDirectory(filepath.Join(modeDir, "objects")); err != nil {
		return err
	}
	check := keyedValue(provider.key, "boltertrader-secret-store-key-check-v1")
	other := ModeTest
	if provider.mode == ModeTest {
		other = ModeLive
	}
	if data, err := os.ReadFile(filepath.Join(provider.stateDir, string(other), "metadata.json")); err == nil {
		var metadata providerMetadata
		if json.Unmarshal(data, &metadata) != nil || metadata.Version != 1 || metadata.Check == "" {
			clear(data)
			return ErrIntegrity
		}
		if hmac.Equal([]byte(metadata.Check), []byte(check)) {
			clear(data)
			return ErrModeKeyReuse
		}
		clear(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	path := filepath.Join(modeDir, "metadata.json")
	if data, err := os.ReadFile(path); err == nil {
		defer clear(data)
		var metadata providerMetadata
		if json.Unmarshal(data, &metadata) != nil || metadata.Version != 1 || !hmac.Equal([]byte(metadata.Check), []byte(check)) {
			return ErrIntegrity
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	return writeJSONAtomic(path, providerMetadata{Version: 1, Check: check}, 0o600)
}

func (provider *FilesystemProvider) verifyMetadata() error {
	data, err := os.ReadFile(filepath.Join(provider.stateDir, string(provider.mode), "metadata.json"))
	if err != nil {
		return ErrUnavailable
	}
	defer clear(data)
	var metadata providerMetadata
	want := keyedValue(provider.key, "boltertrader-secret-store-key-check-v1")
	if json.Unmarshal(data, &metadata) != nil || metadata.Version != 1 || !hmac.Equal([]byte(metadata.Check), []byte(want)) {
		return ErrIntegrity
	}
	return nil
}

func validOperationID(operation OperationID) bool {
	parsed, err := ParseOperationID(string(operation))
	return err == nil && parsed == operation
}

func deriveReference(key []byte, scope Scope, operation OperationID) OpaqueRef {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("boltertrader-secret-ref-v1\x00" + string(scope.Mode) + "\x00" + scope.Namespace + "\x00" + scope.KeyVersion + "\x00" + string(operation)))
	return OpaqueRef(base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

func materialDigest(key, material []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("boltertrader-secret-material-v1\x00"))
	_, _ = mac.Write(material)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func keyedValue(key []byte, label string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(label))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func envelopeAAD(scope Scope, ref OpaqueRef, digest string) []byte {
	return []byte("boltertrader-secret-envelope-v1\x00" + string(scope.Mode) + "\x00" + scope.Namespace + "\x00" + scope.KeyVersion + "\x00" + ref.RawForTrustedBoundary() + "\x00" + digest)
}

func ensureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return ErrUnavailable
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return ErrUnavailable
	}
	return nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrUnavailable
	}
	defer clear(data)
	temp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return ErrUnavailable
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return ErrUnavailable
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return ErrUnavailable
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return ErrUnavailable
	}
	if err := temp.Close(); err != nil {
		return ErrUnavailable
	}
	if err := os.Rename(tempPath, path); err != nil {
		return ErrUnavailable
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return ErrUnavailable
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return ErrUnavailable
	}
	return nil
}

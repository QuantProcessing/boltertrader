package secretstore

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

var (
	ErrInvalidKeySource     = errors.New("SECRET_STORE_KEY_SOURCE_INVALID")
	ErrKeyPermissions       = errors.New("SECRET_STORE_KEY_PERMISSIONS_INVALID")
	ErrKeySourceUnsupported = errors.New("SECRET_STORE_KEY_SOURCE_UNSUPPORTED")
)

type KeySource interface {
	Load(context.Context) ([]byte, error)
}

type StaticKeySource struct{ Key []byte }

func (source StaticKeySource) Load(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil || len(source.Key) != 32 {
		return nil, ErrInvalidKeySource
	}
	return append([]byte(nil), source.Key...), nil
}

type FileKeySource struct{ Path string }

func (source FileKeySource) Load(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil || !filepath.IsAbs(source.Path) {
		return nil, ErrInvalidKeySource
	}
	info, err := os.Lstat(source.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidKeySource
	}
	if info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
		return nil, ErrKeyPermissions
	}
	data, err := os.ReadFile(source.Path)
	if err != nil {
		return nil, ErrInvalidKeySource
	}
	defer clear(data)
	raw := strings.TrimSuffix(string(data), "\n")
	if len(raw) != 64 || strings.ContainsAny(raw, "\r\n") {
		return nil, ErrInvalidKeySource
	}
	key := make([]byte, 32)
	if _, err := hex.Decode(key, []byte(raw)); err != nil {
		clear(key)
		return nil, ErrInvalidKeySource
	}
	return key, nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

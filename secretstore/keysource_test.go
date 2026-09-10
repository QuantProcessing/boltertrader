package secretstore_test

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileKeySourceRequiresSecureCanonicalKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	encoded := hex.EncodeToString(make([]byte, 32)) + "\n"
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := (secretstore.FileKeySource{Path: path}).Load(context.Background())
	if err != nil || len(key) != 32 {
		t.Fatalf("key length=%d err=%v", len(key), err)
	}
	clear(key)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (secretstore.FileKeySource{Path: path}).Load(context.Background()); !errors.Is(err, secretstore.ErrKeyPermissions) {
		t.Fatalf("permissions err=%v", err)
	}
	relative := secretstore.FileKeySource{Path: "master.key"}
	if _, err := relative.Load(context.Background()); !errors.Is(err, secretstore.ErrInvalidKeySource) {
		t.Fatalf("relative err=%v", err)
	}
	for name, prepare := range map[string]func(string) error{
		"directory":   func(path string) error { return os.Mkdir(path, 0o700) },
		"invalid hex": func(path string) error { return os.WriteFile(path, []byte(strings.Repeat("z", 64)), 0o600) },
		"empty":       func(path string) error { return os.WriteFile(path, nil, 0o600) },
		"symlink":     func(path string) error { return os.Symlink(filepath.Join(dir, "master.key"), path) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
			if err := prepare(candidate); err != nil {
				t.Fatal(err)
			}
			if _, err := (secretstore.FileKeySource{Path: candidate}).Load(context.Background()); err == nil {
				t.Fatal("invalid key source accepted")
			}
		})
	}
}

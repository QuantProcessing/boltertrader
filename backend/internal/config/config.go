package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const FundingWorkerTokenFileEnv = "BT_FUNDING_WORKER_TOKEN_FILE"

type Config struct {
	Mode                     string
	BrowserListenAddr        string
	WorkListenAddr           string
	BootstrapUsername        string
	BootstrapPasswordHash    string
	FundingWorkerTokenFile   string
	FundingWorkerTokenDigest [sha256.Size]byte
}

func Load() (Config, error) {
	if strings.TrimSpace(os.Getenv("BT_PLATFORM_WORK_TOKEN")) != "" || strings.TrimSpace(os.Getenv("BT_FUNDING_WORKER_TOKEN")) != "" {
		return Config{}, fmt.Errorf("inline worker tokens are forbidden; use %s", FundingWorkerTokenFileEnv)
	}
	cfg := Config{
		Mode:                   strings.TrimSpace(os.Getenv("BT_MODE")),
		BrowserListenAddr:      getenv("BT_PLATFORM_BROWSER_LISTEN_ADDR", "127.0.0.1:18091"),
		WorkListenAddr:         getenv("BT_PLATFORM_WORK_LISTEN_ADDR", "127.0.0.1:18092"),
		BootstrapUsername:      strings.TrimSpace(os.Getenv("BT_BOOTSTRAP_USERNAME")),
		BootstrapPasswordHash:  strings.TrimSpace(os.Getenv("BT_BOOTSTRAP_PASSWORD_HASH")),
		FundingWorkerTokenFile: strings.TrimSpace(os.Getenv(FundingWorkerTokenFileEnv)),
	}
	if cfg.FundingWorkerTokenFile == "" {
		return Config{}, fmt.Errorf("%s is required", FundingWorkerTokenFileEnv)
	}
	token, err := readProtectedToken(cfg.FundingWorkerTokenFile)
	if err != nil {
		return Config{}, err
	}
	cfg.FundingWorkerTokenDigest = sha256.Sum256(token)
	for index := range token {
		token[index] = 0
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) Validate() error {
	if cfg.Mode != "test" && cfg.Mode != "live" {
		return fmt.Errorf("BT_MODE must be test or live")
	}
	if !isLoopback(cfg.BrowserListenAddr) {
		return fmt.Errorf("BT_PLATFORM_BROWSER_LISTEN_ADDR must use a loopback host")
	}
	if !isLoopback(cfg.WorkListenAddr) {
		return fmt.Errorf("BT_PLATFORM_WORK_LISTEN_ADDR must use a loopback host")
	}
	if cfg.BrowserListenAddr == cfg.WorkListenAddr {
		return fmt.Errorf("browser and work listeners must use distinct addresses")
	}
	if strings.TrimSpace(cfg.BootstrapUsername) == "" {
		return fmt.Errorf("BT_BOOTSTRAP_USERNAME is required")
	}
	if _, err := bcrypt.Cost([]byte(cfg.BootstrapPasswordHash)); err != nil {
		return fmt.Errorf("BT_BOOTSTRAP_PASSWORD_HASH must be a bcrypt hash")
	}
	if strings.TrimSpace(cfg.FundingWorkerTokenFile) == "" {
		return fmt.Errorf("%s is required", FundingWorkerTokenFileEnv)
	}
	if allZero(cfg.FundingWorkerTokenDigest[:]) {
		return fmt.Errorf("funding worker token digest is required")
	}
	return nil
}

func readProtectedToken(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", FundingWorkerTokenFileEnv, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must name a regular non-symlink file", FundingWorkerTokenFileEnv)
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%s must have permissions 0600", FundingWorkerTokenFileEnv)
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", FundingWorkerTokenFileEnv, err)
	}
	token = bytes.TrimSpace(token)
	if len(token) < 32 {
		return nil, fmt.Errorf("%s must contain at least 32 opaque bytes", FundingWorkerTokenFileEnv)
	}
	for _, value := range token {
		if value < 0x21 || value > 0x7e {
			return nil, fmt.Errorf("%s must contain an opaque ASCII token without whitespace", FundingWorkerTokenFileEnv)
		}
	}
	return token, nil
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func allZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}

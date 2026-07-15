package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

var (
	ErrIdentityRequired  = errors.New("hyperliquid account identity required")
	ErrAccountIDMismatch = errors.New("hyperliquid account id mismatch")
)

const DefaultAccountID = "HYPERLIQUID-001"

type Source struct {
	ExplicitAccountID string
	AccountAddress    string
	VaultAddress      string
	SignerAddress     string
}

type Identity struct {
	AccountID    string
	QueryAddress string
}

func ResolveIdentity(src Source) (Identity, error) {
	queryAddress := firstNonEmpty(src.AccountAddress, src.VaultAddress, src.SignerAddress)
	accountID := strings.TrimSpace(src.ExplicitAccountID)
	if accountID == "" {
		if queryAddress == "" {
			return Identity{}, ErrIdentityRequired
		}
		accountID = DefaultAccountID
	}
	return Identity{AccountID: accountID, QueryAddress: queryAddress}, nil
}

// AccountIDForAddress is retained for compatibility. Hyperliquid addresses are
// account selectors; the runtime account id is the venue default unless
// explicitly overridden.
func AccountIDForAddress(address string) string {
	return DefaultAccountID
}

func ResolveAPIAccountAddress(ctx context.Context, client *sdk.Client, configuredAddress string) (string, error) {
	if client == nil {
		return "", nil
	}
	configuredAddress = strings.TrimSpace(configuredAddress)
	if configuredAddress != "" && !IsHexAddress(configuredAddress) {
		return "", fmt.Errorf("hyperliquid account identity: configured account address must be 0x address, got %q", configuredAddress)
	}
	if client.PrivateKey == nil || client.AccountAddr == "" {
		if configuredAddress != "" {
			client.WithAccount(configuredAddress)
			return client.AccountAddr, nil
		}
		return "", ErrIdentityRequired
	}
	signerAddress := strings.TrimSpace(client.AccountAddr)
	if !IsHexAddress(signerAddress) {
		return "", fmt.Errorf("hyperliquid account identity: signer address must be 0x address, got %q", signerAddress)
	}
	role, err := client.GetUserRole(ctx, signerAddress)
	if err != nil {
		return "", fmt.Errorf("hyperliquid account identity: resolve userRole for %s: %w", signerAddress, err)
	}
	switch role.Role {
	case sdk.UserRoleAgent:
		owner := strings.TrimSpace(role.Data.User)
		if !IsHexAddress(owner) {
			return "", fmt.Errorf("hyperliquid account identity: agent owner from userRole must be 0x address, got %q", role.Data.User)
		}
		if configuredAddress != "" && !sameHexAddress(configuredAddress, owner) {
			return "", fmt.Errorf("hyperliquid account identity: configured account %q does not match userRole owner %q", configuredAddress, owner)
		}
		client.WithAccount(owner)
		return client.AccountAddr, nil
	case sdk.UserRoleUser:
		if configuredAddress != "" && !sameHexAddress(configuredAddress, signerAddress) {
			return "", fmt.Errorf("hyperliquid account identity: configured account %q does not match signer %q", configuredAddress, signerAddress)
		}
		return client.AccountAddr, nil
	case sdk.UserRoleVault, sdk.UserRoleSubAccount:
		return "", fmt.Errorf("hyperliquid account identity: userRole %q is not supported by this account model", role.Role)
	default:
		return "", fmt.Errorf("hyperliquid account identity: unsupported userRole %q", role.Role)
	}
}

func IsHexAddress(address string) bool {
	address = strings.TrimSpace(address)
	if len(address) != 42 || !strings.HasPrefix(address, "0x") {
		return false
	}
	for _, r := range address[2:] {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}

func sameHexAddress(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func ResolveScopedAccountID(explicit, canonical string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	canonical = strings.TrimSpace(canonical)
	if canonical == "" {
		return explicit, nil
	}
	if explicit == "" {
		return canonical, nil
	}
	if explicit != canonical {
		return "", fmt.Errorf("%w: got %q want %q", ErrAccountIDMismatch, explicit, canonical)
	}
	return explicit, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

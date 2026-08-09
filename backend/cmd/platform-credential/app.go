package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const maxCredentialBundle = 32 << 10

type accountMetadata struct {
	Venue         string         `json:"venue"`
	DisplayName   string         `json:"displayName"`
	ProductPolicy []string       `json:"productPolicy"`
	Routes        []accountRoute `json:"routes"`
}

type accountRoute struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
	RequestedState  string `json:"requestedState"`
}

type provisionRequest struct {
	Mode             string
	IdempotencyKey   string
	Metadata         accountMetadata
	CredentialBundle []byte
}

type provisionResult struct {
	AccountID   string `json:"accountId"`
	OperationID string `json:"operationId"`
	State       string `json:"state"`
}

var provisionAccount = provisionProductionAccount

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name := strings.ToLower(strings.SplitN(arg, "=", 2)[0])
		if strings.Contains(name, "credential") || strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "private-key") || strings.Contains(name, "api-key") {
			return fmt.Errorf("CREDENTIAL_ARGUMENT_REJECTED")
		}
	}
	for _, name := range []string{"BT_CREDENTIAL_BUNDLE", "BT_API_SECRET", "BT_PRIVATE_KEY", "TRADING_SECRET"} {
		if _, present := os.LookupEnv(name); present {
			return fmt.Errorf("CREDENTIAL_ENVIRONMENT_REJECTED")
		}
	}
	flags := flag.NewFlagSet("platform-credential", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "test", "process mode")
	accountFile := flags.String("account-file", "", "protected account metadata JSON")
	idempotencyKey := flags.String("idempotency-key", "", "idempotency key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *accountFile == "" || strings.TrimSpace(*idempotencyKey) == "" {
		return fmt.Errorf("--account-file and --idempotency-key are required")
	}
	if *mode != "test" {
		return fmt.Errorf("LIVE_PROVISIONING_DISABLED")
	}
	metadata, err := readMetadata(*accountFile)
	if err != nil {
		return err
	}
	bundle, err := readCredentialBundle(stdin)
	if err != nil {
		return err
	}
	defer clear(bundle)
	result, err := provisionAccount(context.Background(), provisionRequest{
		Mode: *mode, IdempotencyKey: *idempotencyKey, Metadata: metadata, CredentialBundle: bundle,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func readMetadata(path string) (accountMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return accountMetadata{}, fmt.Errorf("ACCOUNT_METADATA_UNAVAILABLE")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > 1<<20 {
		return accountMetadata{}, fmt.Errorf("ACCOUNT_METADATA_UNAVAILABLE")
	}
	decoder := json.NewDecoder(io.LimitReader(file, (1<<20)+1))
	decoder.DisallowUnknownFields()
	var metadata accountMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return accountMetadata{}, fmt.Errorf("INVALID_ACCOUNT_METADATA")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return accountMetadata{}, fmt.Errorf("INVALID_ACCOUNT_METADATA")
	}
	if strings.TrimSpace(metadata.Venue) == "" || strings.TrimSpace(metadata.DisplayName) == "" || len(metadata.Routes) == 0 {
		return accountMetadata{}, fmt.Errorf("INVALID_ACCOUNT_METADATA")
	}
	return metadata, nil
}

func readCredentialBundle(reader io.Reader) ([]byte, error) {
	if file, ok := reader.(*os.File); ok && isTerminal(file) {
		return readTTYCredentialBundle(file, func(enabled bool) error { return setTerminalEcho(file, enabled) })
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxCredentialBundle+1))
	if err != nil || len(data) == 0 || len(data) > maxCredentialBundle {
		clear(data)
		return nil, fmt.Errorf("CREDENTIAL_STDIN_UNAVAILABLE")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || !json.Valid(data) {
		clear(data)
		return nil, fmt.Errorf("INVALID_CREDENTIAL_BUNDLE")
	}
	return data, nil
}

func readTTYCredentialBundle(reader io.Reader, setEcho func(bool) error) ([]byte, error) {
	if err := setEcho(false); err != nil {
		return nil, fmt.Errorf("CREDENTIAL_STDIN_UNAVAILABLE")
	}
	restored := false
	defer func() {
		if !restored {
			_ = setEcho(true)
		}
	}()
	data, err := bufio.NewReader(io.LimitReader(reader, maxCredentialBundle+1)).ReadBytes('\n')
	data = bytes.TrimSpace(data)
	if len(data) == 0 || len(data) > maxCredentialBundle || !json.Valid(data) {
		clear(data)
		return nil, fmt.Errorf("INVALID_CREDENTIAL_BUNDLE")
	}
	if err != nil && err != io.EOF {
		clear(data)
		return nil, fmt.Errorf("CREDENTIAL_STDIN_UNAVAILABLE")
	}
	if err := setEcho(true); err != nil {
		clear(data)
		return nil, fmt.Errorf("CREDENTIAL_STDIN_UNAVAILABLE")
	}
	restored = true
	return data, nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func setTerminalEcho(file *os.File, enabled bool) error {
	argument := "-echo"
	if enabled {
		argument = "echo"
	}
	command := exec.Command("stty", argument)
	command.Stdin = file
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

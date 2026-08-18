package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

const (
	defaultGatewayPath  = "/usr/local/bin/exchange-gateway"
	defaultSecretRoot   = "/run/secrets"
	defaultProbeTimeout = 2 * time.Second
	maxProbeTimeout     = 10 * time.Second
)

var safeEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type dockerSupportDeps struct {
	secretRoot string
	readFile   func(string) ([]byte, error)
	exec       func(string, []string, []string) error
	environ    func() []string
	httpClient *http.Client
}

func main() {
	if err := runDockerSupport(context.Background(), os.Args[1:], dockerSupportDeps{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runDockerSupport(ctx context.Context, args []string, deps dockerSupportDeps) error {
	if len(args) == 0 {
		return fmt.Errorf("run or health subcommand is required")
	}
	switch args[0] {
	case "run":
		return runGateway(args[1:], deps)
	case "health":
		return probeGateway(ctx, args[1:], deps)
	default:
		return fmt.Errorf("unsupported subcommand")
	}
}

func runGateway(args []string, deps dockerSupportDeps) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "Gateway config path")
	gatewayPath := flags.String("gateway", defaultGatewayPath, "Gateway executable")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse run arguments")
	}
	if *configPath == "" {
		return fmt.Errorf("run requires -config")
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("run does not accept positional arguments")
	}
	file, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	values, err := loadSecretEnvironment(file.SecretRefs(), deps)
	if err != nil {
		return err
	}
	environ := os.Environ
	if deps.environ != nil {
		environ = deps.environ
	}
	execProcess := syscall.Exec
	if deps.exec != nil {
		execProcess = deps.exec
	}
	childEnv := mergeEnvironment(environ(), values)
	if err := execProcess(*gatewayPath, []string{*gatewayPath, "-config", *configPath}, childEnv); err != nil {
		return fmt.Errorf("exec Gateway failed: %w", err)
	}
	return nil
}

func probeGateway(ctx context.Context, args []string, deps dockerSupportDeps) error {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "Gateway config path")
	kind := flags.String("kind", "ready", "Probe kind: live or ready")
	timeout := flags.Duration("timeout", defaultProbeTimeout, "Probe timeout")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse health arguments")
	}
	if *configPath == "" {
		return fmt.Errorf("health requires -config")
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("health does not accept positional arguments")
	}
	if *kind != "live" && *kind != "ready" {
		return fmt.Errorf("health kind must be live or ready")
	}
	if *timeout <= 0 || *timeout > maxProbeTimeout {
		return fmt.Errorf("health timeout must be positive and at most %s", maxProbeTimeout)
	}
	file, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	token, err := readSecret(file.Server.AuthTokenRef, deps)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(file.Server.Listen)
	if err != nil {
		return fmt.Errorf("Gateway listen address is invalid")
	}
	address := net.ParseIP(host)
	if address == nil {
		return fmt.Errorf("Gateway listen address is invalid")
	}
	if !address.IsLoopback() {
		host = "127.0.0.1"
	}

	probeCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		probeCtx,
		http.MethodGet,
		"http://"+net.JoinHostPort(host, port)+"/v1/health/"+*kind,
		nil,
	)
	if err != nil {
		return fmt.Errorf("construct health request")
	}
	// Readiness sits behind the same auth as every other /v1 route, so an
	// unauthenticated probe gets a flat 401 from a Gateway that is perfectly
	// healthy — and Compose reads that as unhealthy forever, holding back every
	// service that waits on it. The token is read from the same config the
	// Gateway itself parses, through the same loader, so this is the value it
	// compares against rather than a guess.
	//
	// This request used to carry a mode attestation as well, because the Gateway
	// wrapped every route in one and answered 409 without it. There is one mode
	// now, and the header is gone from both ends.
	request.Header.Set("Authorization", "Bearer "+token)
	client := deps.httpClient
	if client == nil {
		client = &http.Client{}
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("Gateway health probe timed out")
		}
		return fmt.Errorf("Gateway health probe failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Gateway health probe returned status %d", response.StatusCode)
	}
	return nil
}

func loadConfig(path string) (config.File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config.File{}, fmt.Errorf("read Gateway config failed")
	}
	file, err := config.LoadYAML(data)
	if err != nil {
		return config.File{}, fmt.Errorf("load Gateway config failed: %w", err)
	}
	return file, nil
}

func loadSecretEnvironment(refs []secret.Ref, deps dockerSupportDeps) (map[string]string, error) {
	values := make(map[string]string, len(refs))
	for _, ref := range refs {
		name, err := environmentName(ref)
		if err != nil {
			return nil, err
		}
		if _, exists := values[name]; exists {
			continue
		}
		value, err := readSecret(ref, deps)
		if err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, nil
}

func readSecret(ref secret.Ref, deps dockerSupportDeps) (string, error) {
	name, err := environmentName(ref)
	if err != nil {
		return "", err
	}
	root := deps.secretRoot
	if root == "" {
		root = defaultSecretRoot
	}
	readFile := deps.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	value, err := readFile(filepath.Join(root, name))
	if err != nil {
		return "", fmt.Errorf("required Docker secret file is missing")
	}
	normalized := strings.TrimSuffix(string(value), "\n")
	normalized = strings.TrimSuffix(normalized, "\r")
	if normalized == "" {
		return "", fmt.Errorf("required Docker secret file is empty")
	}
	return normalized, nil
}

func environmentName(ref secret.Ref) (string, error) {
	if ref.Scheme() != "env" {
		return "", fmt.Errorf("Docker support requires env secret references")
	}
	name := strings.TrimPrefix(ref.RawForProvider(), "env://")
	if !safeEnvironmentName.MatchString(name) {
		return "", fmt.Errorf("Docker secret reference has an unsafe environment name")
	}
	return name, nil
}

func mergeEnvironment(base []string, values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	overridden := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if _, replace := values[name]; replace {
			if _, already := overridden[name]; !already {
				result = append(result, name+"="+values[name])
				overridden[name] = struct{}{}
			}
			continue
		}
		if found {
			result = append(result, entry)
		}
	}
	for _, name := range names {
		if _, already := overridden[name]; already {
			continue
		}
		result = append(result, name+"="+values[name])
	}
	return result
}

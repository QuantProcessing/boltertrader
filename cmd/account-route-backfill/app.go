package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/QuantProcessing/boltertrader/routeprofile"
)

type mappingArtifact struct {
	SchemaVersion int       `json:"schemaVersion"`
	Mappings      []mapping `json:"mappings"`
}

type mapping struct {
	Venue             string `json:"venue"`
	NativeEnvironment string `json:"nativeEnvironment"`
	LegacyProduct     string `json:"legacyProduct"`
	SettlementAsset   string `json:"settlementAsset"`
	RouteProfileID    string `json:"routeProfileId"`
	RouteID           string `json:"routeId"`
}

type backfillRequest struct {
	Mode           string
	TargetService  string
	TargetDatabase string
	Artifact       mappingArtifact
}

type backfillResult struct {
	Accounts   int      `json:"accounts"`
	Routes     int      `json:"routes"`
	AccountIDs []string `json:"accountIds"`
}

var executeBackfill = func(context.Context, backfillRequest) (backfillResult, error) {
	return backfillResult{}, fmt.Errorf("BACKFILL_DATABASE_HANDOFF_REQUIRED")
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("account-route-backfill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mappingPath := flags.String("mapping", "", "checksum-verified route mapping artifact")
	mode := flags.String("mode", "test", "process mode")
	targetService := flags.String("target-service", "", "non-secret PostgreSQL service name")
	targetDatabase := flags.String("target-database", "", "non-secret PostgreSQL database name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*mappingPath) == "" {
		return fmt.Errorf("--mapping is required and positional arguments are not accepted")
	}
	if *mode != "test" {
		return fmt.Errorf("LIVE_PROVISIONING_DISABLED")
	}
	if err := validateIdentifier("target service", *targetService, 64, true); err != nil {
		return err
	}
	if err := validateIdentifier("target database", *targetDatabase, 63, false); err != nil {
		return err
	}
	data, err := os.ReadFile(*mappingPath)
	if err != nil {
		return fmt.Errorf("MAPPING_ARTIFACT_UNAVAILABLE")
	}
	artifact, err := decodeMapping(data)
	if err != nil {
		return err
	}
	result, err := executeBackfill(context.Background(), backfillRequest{
		Mode: *mode, TargetService: *targetService, TargetDatabase: *targetDatabase, Artifact: artifact,
	})
	if err != nil {
		return err
	}
	sort.Strings(result.AccountIDs)
	return json.NewEncoder(stdout).Encode(result)
}

func decodeMapping(data []byte) (mappingArtifact, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var artifact mappingArtifact
	if err := decoder.Decode(&artifact); err != nil {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	if err := requireEOF(decoder); err != nil {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	if artifact.SchemaVersion != 1 || len(artifact.Mappings) == 0 || len(artifact.Mappings) > 256 {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	registry := routeprofile.DefaultRegistry()
	seenLegacy := make(map[string]struct{}, len(artifact.Mappings))
	seenRoute := make(map[string]struct{}, len(artifact.Mappings))
	for _, item := range artifact.Mappings {
		mode := routeprofile.ModeTest
		if item.NativeEnvironment == string(routeprofile.EnvironmentLive) {
			mode = routeprofile.ModeLive
		}
		profile, err := registry.Resolve(mode, item.Venue, item.RouteProfileID)
		if err != nil || string(profile.NativeEnvironment) != item.NativeEnvironment || profile.SettlementAsset != item.SettlementAsset {
			return mappingArtifact{}, fmt.Errorf("ROUTE_PROFILE_CONFLICT")
		}
		if !safeToken(item.LegacyProduct) || !safeToken(item.RouteID) || item.SettlementAsset != strings.ToUpper(item.SettlementAsset) {
			return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
		}
		legacyKey := strings.Join([]string{item.Venue, item.NativeEnvironment, item.LegacyProduct}, "\x00")
		if _, duplicate := seenLegacy[legacyKey]; duplicate {
			return mappingArtifact{}, fmt.Errorf("AMBIGUOUS_LEGACY_PROFILE")
		}
		seenLegacy[legacyKey] = struct{}{}
		routeKey := strings.Join([]string{item.Venue, item.NativeEnvironment, item.RouteID}, "\x00")
		if _, duplicate := seenRoute[routeKey]; duplicate {
			return mappingArtifact{}, fmt.Errorf("AMBIGUOUS_ROUTE_ID")
		}
		seenRoute[routeKey] = struct{}{}
	}
	slices.SortFunc(artifact.Mappings, func(left, right mapping) int {
		return strings.Compare(left.Venue+"\x00"+left.NativeEnvironment+"\x00"+left.LegacyProduct, right.Venue+"\x00"+right.NativeEnvironment+"\x00"+right.LegacyProduct)
	})
	return artifact, nil
}

func validateIdentifier(label, value string, max int, allowDash bool) error {
	if value == "" || len(value) > max {
		return fmt.Errorf("%s is required and must be non-secret", label)
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || (allowDash && char == '-') {
			continue
		}
		return fmt.Errorf("%s is required and must be non-secret", label)
	}
	return nil
}

func safeToken(value string) bool {
	if value == "" || len(value) > 64 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate field")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid delimiter")
	}
	_, err = decoder.Token()
	return err
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

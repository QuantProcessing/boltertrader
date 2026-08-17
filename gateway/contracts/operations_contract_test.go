package contracts

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

type acceptanceCrosswalk struct {
	Version  int               `json:"version"`
	Criteria []acceptanceEntry `json:"criteria"`
}

type acceptanceEntry struct {
	ID       string   `json:"id"`
	Tests    []string `json:"tests"`
	Evidence []string `json:"evidence"`
}

func TestAcceptanceCrosswalkHasExactlyTwentySevenExecutableEntries(t *testing.T) {
	data, err := os.ReadFile("testdata/acceptance_crosswalk.json")
	if err != nil {
		t.Fatalf("read acceptance crosswalk: %v", err)
	}
	var crosswalk acceptanceCrosswalk
	if err := json.Unmarshal(data, &crosswalk); err != nil {
		t.Fatalf("decode acceptance crosswalk: %v", err)
	}
	if crosswalk.Version != 1 || len(crosswalk.Criteria) != 27 {
		t.Fatalf("crosswalk version/criteria = %d/%d, want 1/27", crosswalk.Version, len(crosswalk.Criteria))
	}
	seen := make(map[string]struct{}, 27)
	for index, entry := range crosswalk.Criteria {
		want := "GW-AC" + twoDigits(index+1)
		if entry.ID != want {
			t.Errorf("criterion[%d] = %q, want %q", index, entry.ID, want)
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			t.Errorf("duplicate criterion %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		if len(entry.Tests) == 0 || len(entry.Evidence) == 0 {
			t.Errorf("%s has no executable tests/evidence", entry.ID)
		}
		for _, value := range append(append([]string{}, entry.Tests...), entry.Evidence...) {
			if strings.TrimSpace(value) != value || value == "" || strings.ContainsAny(value, "\r\n") {
				t.Errorf("%s has invalid crosswalk value %q", entry.ID, value)
			}
		}
	}
}

func TestAcceptanceCrosswalkFreezesRouteAwareV5Contracts(t *testing.T) {
	data, err := os.ReadFile("testdata/acceptance_crosswalk.json")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, testName := range []string{"TestOpenAPIRouteAwarePrivateWriteFences", "TestInternalAuthorityOpenAPIIsCompleteStrictAndSeparatelyAuthenticated", "TestV5AccountAndGatewayMutationsAreRouteAware"} {
		if !strings.Contains(text, testName) {
			t.Errorf("acceptance crosswalk missing %s", testName)
		}
	}
}

func TestGatewayOperationsDocsAndMakeTargetsAreComplete(t *testing.T) {
	for _, path := range []string{
		"../../docs/guides/exchange-gateway.md",
		"../../docs/zh-CN/guides/exchange-gateway.md",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		text := string(data)
		for _, required := range []string{
			"PostgreSQL", "/v1/orders", "/v1/ws", "/metrics",
			"graceful", "migration", "reconciliation", "no Redis", "no gRPC",
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing %q", path, required)
			}
		}
		for _, forbidden := range []string{
			"provides a withdrawal endpoint",
			"provides shared margin",
			"provides smart order routing",
		} {
			if strings.Contains(strings.ToLower(text), forbidden) {
				t.Errorf("%s claims forbidden capability %q", path, forbidden)
			}
		}
	}

	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	text := string(makefile)
	// Only credential-free targets remain. The twenty per-venue promotion targets
	// and their aggregate drove writes at a venue's demo/testnet environment, which
	// no longer exists; a gateway target that reached production would spend real
	// funds, so `make` no longer offers one.
	requiredTargets := []string{
		"test-gateway-contract", "test-gateway-offline", "test-gateway-race",
		"test-gateway-postgres", "test-gateway-restart", "test-gateway-security",
		"test-gateway",
	}
	for _, target := range requiredTargets {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `:`).MatchString(text) {
			t.Errorf("Makefile missing target %s", target)
		}
	}
}

func twoDigits(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}

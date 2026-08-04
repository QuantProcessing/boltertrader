package productplatformcontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFrozenContractValidates(t *testing.T) {
	contract := loadContract(t)
	if contract.ContractVersion != Version {
		t.Fatalf("contract version=%q want=%q", contract.ContractVersion, Version)
	}
}

func TestContractMatchesFrozenMarkdownExpectations(t *testing.T) {
	contract := loadContract(t)
	data, err := os.ReadFile(filepath.Join("testdata", "frozen-markdown-expectations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ContractVersion     string          `json:"contractVersion"`
		Revision            int             `json:"revision"`
		ListenerRouteCounts map[string]int  `json:"listenerRouteCounts"`
		StrictMutationCount int             `json:"strictMutationCount"`
		NestedPayloadCount  int             `json:"nestedPayloadCount"`
		StrictMutationsHash string          `json:"strictMutationsSHA256"`
		NestedPayloadsHash  string          `json:"strictNestedPayloadsSHA256"`
		SchemaIdentifiers   []string        `json:"schemaIdentifiers"`
		WorkerRetryability  map[string]bool `json:"workerRetryability"`
		WorkerHTTPStatus    map[string]int  `json:"workerHTTPStatus"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ContractVersion != contract.ContractVersion || fixture.Revision != contract.Revision {
		t.Fatalf("fixture identity=%s/r%d contract=%s/r%d", fixture.ContractVersion, fixture.Revision, contract.ContractVersion, contract.Revision)
	}
	gotRouteCounts := make(map[string]int, len(contract.Listeners))
	for _, listener := range contract.Listeners {
		gotRouteCounts[listener.ID] = len(listener.Routes)
	}
	if !reflect.DeepEqual(gotRouteCounts, fixture.ListenerRouteCounts) ||
		len(contract.StrictMutations) != fixture.StrictMutationCount ||
		len(contract.StrictNestedPayload) != fixture.NestedPayloadCount ||
		fingerprint(t, contract.StrictMutations) != fixture.StrictMutationsHash ||
		fingerprint(t, contract.StrictNestedPayload) != fixture.NestedPayloadsHash ||
		!reflect.DeepEqual(contract.SchemaIdentifiers, fixture.SchemaIdentifiers) ||
		!reflect.DeepEqual(contract.StableWorkerErrors, fixture.WorkerRetryability) ||
		!reflect.DeepEqual(contract.StableWorkerStatus, fixture.WorkerHTTPStatus) {
		t.Fatal("machine-readable contract drifted from frozen Markdown expectations")
	}
}

func TestEveryStrictMutationRejectsUnknownFields(t *testing.T) {
	contract := loadContract(t)
	for route := range contract.StrictMutations {
		err := contract.ValidateMutation(route, []byte(`{"unexpectedSensitiveField":true}`))
		if !errors.Is(err, ErrInvalidContract) || !strings.Contains(err.Error(), `rejects unknown field "unexpectedSensitiveField"`) {
			t.Errorf("route %q error=%v", route, err)
		}
	}
}

func TestEveryStrictNestedPayloadRejectsUnknownFields(t *testing.T) {
	contract := loadContract(t)
	for name := range contract.StrictNestedPayload {
		err := contract.ValidateNested(name, []byte(`{"unexpectedNestedField":true}`))
		if !errors.Is(err, ErrInvalidContract) || !strings.Contains(err.Error(), `rejects unknown field "unexpectedNestedField"`) {
			t.Errorf("payload %q error=%v", name, err)
		}
	}
}

func TestStrictMutationFixtures(t *testing.T) {
	contract := loadContract(t)
	tests := []struct {
		name    string
		route   string
		fixture string
		wantErr bool
	}{
		{name: "login accepted", route: "POST /api/v1/auth/login", fixture: "valid-login.json"},
		{name: "login extra field rejected", route: "POST /api/v1/auth/login", fixture: "invalid-login-extra.json", wantErr: true},
		{name: "login duplicate field rejected", route: "POST /api/v1/auth/login", fixture: "invalid-login-duplicate.json", wantErr: true},
		{name: "internal desired state accepts secret reference", route: "POST /internal/v1/accounts/desired", fixture: "valid-gateway-desired.json"},
		{name: "internal desired state rejects secret material", route: "POST /internal/v1/accounts/desired", fixture: "invalid-gateway-secret-material.json", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			err = contract.ValidateMutation(test.route, body)
			if test.wantErr && !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("error=%v want ErrInvalidContract", err)
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContractRejectsCrossModeReuse(t *testing.T) {
	contract := loadContract(t)
	contract.Runtime.ModeIsolation.CrossModeReuseAllowed = true
	if err := contract.Validate(); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("error=%v want ErrInvalidContract", err)
	}
}

func TestContractRejectsFencingReorder(t *testing.T) {
	contract := loadContract(t)
	contract.Fencing.IntentValidationOrder[1], contract.Fencing.IntentValidationOrder[6] =
		contract.Fencing.IntentValidationOrder[6], contract.Fencing.IntentValidationOrder[1]
	if err := contract.Validate(); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("error=%v want ErrInvalidContract", err)
	}
}

func TestStrictNestedFixtures(t *testing.T) {
	contract := loadContract(t)
	for _, test := range []struct {
		name    string
		fixture string
		wantErr bool
	}{
		{name: "work.open", fixture: "valid-work-open.json"},
		{name: "work.open", fixture: "invalid-work-open-raw-symbol.json", wantErr: true},
	} {
		body, err := os.ReadFile(filepath.Join("testdata", test.fixture))
		if err != nil {
			t.Fatal(err)
		}
		err = contract.ValidateNested(test.name, body)
		if test.wantErr && !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("fixture %s error=%v want ErrInvalidContract", test.fixture, err)
		}
		if !test.wantErr && err != nil {
			t.Fatalf("fixture %s: %v", test.fixture, err)
		}
	}
}

func TestDecodeRejectsUnknownContractField(t *testing.T) {
	_, err := Decode([]byte(`{"schemaVersion":1,"unknown":true}`))
	if !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("error=%v want ErrInvalidContract", err)
	}
}

func TestDecodeRejectsUnknownNestedContractFields(t *testing.T) {
	path := filepath.Join("..", "..", "api", "product-platform", "v1", "contract.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		anchor string
		extra  string
	}{
		{name: "worker", anchor: `"heartbeatIntervalSeconds"`, extra: `"acceptsGatewayToken":true,`},
		{name: "listener", anchor: `"acceptedPrincipals"`, extra: `"csrfBypass":true,`},
		{name: "mode isolation", anchor: `"crossModeReuseAllowed"`, extra: `"reuseAuditScope":true,`},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := bytes.Replace(data, []byte(test.anchor), []byte(test.extra+test.anchor), 1)
			if _, err := Decode(mutated); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("error=%v want ErrInvalidContract", err)
			}
		})
	}
}

func fingerprint(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func loadContract(t *testing.T) Contract {
	t.Helper()
	path := filepath.Join("..", "..", "api", "product-platform", "v1", "contract.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

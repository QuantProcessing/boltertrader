package fixtureaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type fixtureManifest struct {
	Version    int            `json:"version"`
	ObservedAt string         `json:"observed_at"`
	Entries    []fixtureEntry `json:"entries"`
}

type fixtureEntry struct {
	ID           string   `json:"id"`
	Venue        string   `json:"venue"`
	Product      string   `json:"product"`
	Kind         string   `json:"kind"`
	Path         string   `json:"path"`
	SourceKind   string   `json:"source_kind"`
	Source       string   `json:"source"`
	Sanitization []string `json:"sanitization"`
	Negative     bool     `json:"negative"`
}

func TestAsterNadoFixtureInventory(t *testing.T) {
	root := repositoryRoot(t)
	manifestPath := filepath.Join(root, "internal", "fixtureaudit", "testdata", "aster_nado_manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}

	var manifest fixtureManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	if manifest.Version != 1 {
		t.Fatalf("manifest version = %d, want 1", manifest.Version)
	}
	if manifest.ObservedAt == "" {
		t.Fatal("manifest observed_at is required")
	}

	required := map[string][]string{
		"aster/spot":   {"instrument", "account", "order", "fill", "market_stream", "private_stream", "error"},
		"aster/perp":   {"instrument", "account", "position", "order", "fill", "market_stream", "private_stream", "reference", "open_interest", "error"},
		"nado/unified": {"instrument", "account", "order", "fill", "market_stream", "private_stream", "reference", "capacity", "simulation", "error"},
	}

	covered := make(map[string]map[string]bool)
	negative := make(map[string]bool)
	seenIDs := make(map[string]bool)
	listedPaths := make(map[string]bool)
	for _, entry := range manifest.Entries {
		validateFixtureEntry(t, root, entry)
		if entry.Venue == "nado" && strings.Contains(strings.ToLower(entry.ID+" "+entry.Path+" "+entry.Source), "validate_order") {
			t.Errorf("unsupported Nado validate_order fixture remains: %s", entry.ID)
		}
		if seenIDs[entry.ID] {
			t.Errorf("duplicate fixture id %q", entry.ID)
		}
		seenIDs[entry.ID] = true
		listedPaths[entry.Path] = true

		key := entry.Venue + "/" + entry.Product
		if covered[key] == nil {
			covered[key] = make(map[string]bool)
		}
		covered[key][entry.Kind] = true
		negative[key] = negative[key] || entry.Negative
	}

	for key, kinds := range required {
		var missing []string
		for _, kind := range kinds {
			if !covered[key][kind] {
				missing = append(missing, kind)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s fixture coverage missing: %s", key, strings.Join(missing, ", "))
		}
		if !negative[key] {
			t.Errorf("%s has no negative-path fixture", key)
		}
	}
	assertAllFixturesInventoried(t, root, listedPaths)
}

func TestNadoAccountFixtureDoesNotInventCurrencyAvailability(t *testing.T) {
	root := repositoryRoot(t)
	var envelope struct {
		Data struct {
			Healths      []map[string]any `json:"healths"`
			SpotBalances []struct {
				ProductID int            `json:"product_id"`
				Balance   map[string]any `json:"balance"`
			} `json:"spot_balances"`
			PerpBalances []struct {
				Balance map[string]any `json:"balance"`
			} `json:"perp_balances"`
		} `json:"data"`
	}
	decodeFixture(t, filepath.Join(root, "sdk", "nado", "testdata", "subaccount_info.json"), &envelope)

	if len(envelope.Data.Healths) != 3 {
		t.Fatalf("health count = %d, want initial/maintenance/unweighted", len(envelope.Data.Healths))
	}
	if len(envelope.Data.SpotBalances) == 0 || len(envelope.Data.PerpBalances) == 0 {
		t.Fatal("account fixture must contain spot and perp balances")
	}
	for _, spot := range envelope.Data.SpotBalances {
		if len(spot.Balance) != 1 || spot.Balance["amount"] == nil {
			t.Fatalf("spot product %d balance must contain only raw amount: %#v", spot.ProductID, spot.Balance)
		}
		for _, forbidden := range []string{"free", "available", "available_balance", "locked"} {
			if _, ok := spot.Balance[forbidden]; ok {
				t.Fatalf("spot product %d invents %s: %#v", spot.ProductID, forbidden, spot.Balance)
			}
		}
	}
	if envelope.Data.PerpBalances[0].Balance["v_quote_balance"] == nil {
		t.Fatal("perp fixture must preserve v_quote_balance")
	}
	for _, forbidden := range []string{"free", "available", "available_balance", "locked"} {
		if _, ok := envelope.Data.PerpBalances[0].Balance[forbidden]; ok {
			t.Fatalf("perp balance invents %s: %#v", forbidden, envelope.Data.PerpBalances[0].Balance)
		}
	}
}

func TestNadoProductZeroFixtureResolvesToUSDT0(t *testing.T) {
	root := repositoryRoot(t)
	var envelope struct {
		Data struct {
			Symbols map[string]struct {
				ProductID int    `json:"product_id"`
				Symbol    string `json:"symbol"`
			} `json:"symbols"`
		} `json:"data"`
	}
	decodeFixture(t, filepath.Join(root, "sdk", "nado", "testdata", "symbols.json"), &envelope)
	settlement, ok := envelope.Data.Symbols["USDT0"]
	if !ok || settlement.ProductID != 0 || settlement.Symbol != "USDT0" {
		t.Fatalf("product 0 settlement mapping = %#v, present=%t", settlement, ok)
	}
}

func TestAsterOpenInterestFixtureIsProbeBacked(t *testing.T) {
	root := repositoryRoot(t)
	var manifest fixtureManifest
	decodeFixture(t, filepath.Join(root, "internal", "fixtureaudit", "testdata", "aster_nado_manifest.json"), &manifest)
	for _, entry := range manifest.Entries {
		if entry.ID != "aster-perp-open-interest" {
			continue
		}
		if entry.SourceKind != "probe" || !strings.Contains(entry.Source, "/fapi/v3/openInterest") {
			t.Fatalf("Aster OI provenance = %q %q, want probe-backed V3 route", entry.SourceKind, entry.Source)
		}
		return
	}
	t.Fatal("Aster OI fixture is missing from inventory")
}

func TestSensitiveFixtureFieldsMustBeRedacted(t *testing.T) {
	if err := rejectSensitiveFields(map[string]any{"signature": "0xraw"}); err == nil {
		t.Fatal("raw signature was accepted")
	}
	if err := rejectSensitiveFields(map[string]any{"signature": "<redacted>"}); err != nil {
		t.Fatalf("redacted signature rejected: %v", err)
	}
}

func validateFixtureEntry(t *testing.T, root string, entry fixtureEntry) {
	t.Helper()
	if entry.ID == "" || entry.Venue == "" || entry.Product == "" || entry.Kind == "" {
		t.Errorf("fixture identity fields are required: %+v", entry)
	}
	if entry.SourceKind != "official" && entry.SourceKind != "probe" {
		t.Errorf("fixture %q source_kind = %q, want official or probe", entry.ID, entry.SourceKind)
	}
	if entry.SourceKind == "official" && !strings.HasPrefix(entry.Source, "https://") {
		t.Errorf("fixture %q official source must be an https URL", entry.ID)
	}
	if entry.SourceKind == "probe" && !strings.HasPrefix(entry.Source, "probe:") {
		t.Errorf("fixture %q probe source must start with probe:", entry.ID)
	}
	if len(entry.Sanitization) == 0 {
		t.Errorf("fixture %q must describe sanitization", entry.ID)
	}

	cleanPath := filepath.Clean(entry.Path)
	if cleanPath != entry.Path || strings.HasPrefix(cleanPath, "..") {
		t.Fatalf("fixture %q has unsafe path %q", entry.ID, entry.Path)
	}
	if !strings.HasPrefix(cleanPath, "sdk/aster/") && !strings.HasPrefix(cleanPath, "sdk/nado/") {
		t.Errorf("fixture %q must be owned by sdk/aster or sdk/nado", entry.ID)
	}

	raw, err := os.ReadFile(filepath.Join(root, cleanPath))
	if err != nil {
		t.Errorf("read fixture %q: %v", entry.ID, err)
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Errorf("decode fixture %q: %v", entry.ID, err)
		return
	}
	if err := rejectSensitiveFields(payload); err != nil {
		t.Errorf("fixture %q: %v", entry.ID, err)
	}
	for _, forbidden := range []string{
		"BINANCE_", "ASTER_API", "NADO_PRIVATE", "BEGIN PRIVATE KEY", "X-MBX-APIKEY",
	} {
		if strings.Contains(strings.ToUpper(string(raw)), forbidden) {
			t.Errorf("fixture %q contains forbidden credential marker %q", entry.ID, forbidden)
		}
	}
}

func rejectSensitiveFields(value any) error {
	sensitive := map[string]bool{
		"apikey": true, "api_key": true, "apisecret": true, "api_secret": true,
		"secret": true, "secretkey": true, "secret_key": true,
		"privatekey": true, "private_key": true, "authorization": true,
		"signature": true,
	}
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if sensitive[strings.ToLower(key)] {
				if redacted, ok := child.(string); !ok || redacted != "<redacted>" {
					return fmt.Errorf("contains unredacted sensitive field %q", key)
				}
				continue
			}
			if err := rejectSensitiveFields(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := rejectSensitiveFields(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func decodeFixture(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func assertAllFixturesInventoried(t *testing.T, root string, listed map[string]bool) {
	t.Helper()
	fixtureRoots := []string{
		"sdk/aster/spot/testdata/v3",
		"sdk/aster/perp/testdata/v3",
		"sdk/nado/testdata",
	}
	for _, fixtureRoot := range fixtureRoots {
		err := filepath.WalkDir(filepath.Join(root, fixtureRoot), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !listed[rel] {
				t.Errorf("fixture is not inventoried: %s", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", fixtureRoot, err)
		}
	}
}

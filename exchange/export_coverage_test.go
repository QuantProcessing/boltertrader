package exchange

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type publicSurfaceManifest struct {
	SchemaVersion     int                        `json:"schema_version"`
	Symbols           []publicSurfaceSymbol      `json:"symbols"`
	ProductRows       []exchangeProductRow       `json:"product_rows"`
	RESTMethods       []exchangeMethodSupport    `json:"rest_methods"`
	WebSocketMethods  []exchangeMethodSupport    `json:"websocket_methods"`
	Operations        []exchangeOperation        `json:"operations"`
	ParameterCases    []exchangeParameterCase    `json:"parameter_cases"`
	Deferred          []string                   `json:"deferred"`
	Acknowledgements  []string                   `json:"acknowledgements"`
	AcceptanceTargets []exchangeAcceptanceTarget `json:"acceptance_targets"`
}

type publicSurfaceSymbol struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Coverage string   `json:"coverage"`
	Tests    []string `json:"tests"`
}

type exchangeProductRow struct {
	Code             string                   `json:"code"`
	Venue            string                   `json:"venue"`
	Product          string                   `json:"product"`
	FactoryConfig    string                   `json:"factory_config"`
	RESTMethods      []string                 `json:"rest_methods"`
	WebSocketMethods []string                 `json:"websocket_methods"`
	Fixtures         []string                 `json:"fixtures"`
	AcceptanceTarget string                   `json:"acceptance_target"`
	Acceptance       exchangeAcceptanceStatus `json:"acceptance"`
}

type exchangeMethodSupport struct {
	Method string `json:"method"`
	Spot   bool   `json:"spot"`
	Perp   bool   `json:"perp"`
}

type exchangeAcceptanceStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type exchangeAcceptanceTarget struct {
	Target       string `json:"target"`
	Env          string `json:"env"`
	Package      string `json:"package"`
	TestSelector string `json:"test_selector"`
}

type exchangeOperation struct {
	ID                 string   `json:"id"`
	Method             string   `json:"method"`
	Transport          string   `json:"transport"`
	Spot               bool     `json:"spot"`
	Perp               bool     `json:"perp"`
	Effect             string   `json:"effect"`
	Credentials        string   `json:"credentials"`
	FundingRequirement string   `json:"funding_requirement"`
	ExpectedAck        string   `json:"expected_ack"`
	ExpectedEvent      string   `json:"expected_event"`
	Cleanup            string   `json:"cleanup"`
	ExternalCells      []string `json:"external_cells"`
	Tests              []string `json:"tests"`
}

type exchangeParameterCase struct {
	ID                 string   `json:"id"`
	OperationID        string   `json:"operation_id"`
	Spot               bool     `json:"spot"`
	Perp               bool     `json:"perp"`
	Credentials        string   `json:"credentials"`
	FundingRequirement string   `json:"funding_requirement"`
	ExpectedAck        string   `json:"expected_ack"`
	Cleanup            string   `json:"cleanup"`
	ExternalCells      []string `json:"external_cells"`
	Tests              []string `json:"tests"`
}

func TestPublicSurfaceManifestCoversExportedSymbols(t *testing.T) {
	root := repositoryRoot(t)
	manifest := loadPublicSurfaceManifest(t, root)
	if manifest.SchemaVersion != 2 {
		t.Fatalf("public surface manifest schema_version = %d, want 2", manifest.SchemaVersion)
	}

	actual := discoverExportedSurface(t, root)
	declared := make(map[string]string, len(manifest.Symbols))
	tests := discoverExchangeTests(t, root)
	for _, symbol := range manifest.Symbols {
		if strings.TrimSpace(symbol.ID) == "" {
			t.Fatal("public surface manifest contains an empty symbol id")
		}
		if _, duplicate := declared[symbol.ID]; duplicate {
			t.Fatalf("public surface manifest contains duplicate symbol %s", symbol.ID)
		}
		if len(symbol.Tests) == 0 {
			t.Fatalf("public surface symbol %s has no test classification", symbol.ID)
		}
		switch symbol.Coverage {
		case "compile", "unit", "contract", "fixture":
		default:
			t.Fatalf("public surface symbol %s has invalid coverage class %q", symbol.ID, symbol.Coverage)
		}
		for _, testName := range symbol.Tests {
			if !tests[testName] {
				t.Errorf("public surface symbol %s references missing test %s", symbol.ID, testName)
			}
		}
		declared[symbol.ID] = symbol.Kind
	}

	if len(actual) < 116 {
		t.Fatalf(
			"public surface unexpectedly shrank: got %d declarations, want at least 116",
			len(actual),
		)
	}
	if len(actual) != len(declared) {
		t.Fatalf(
			"public surface declaration count = %d actual / %d declared\nmissing: %v\nstale: %v",
			len(actual),
			len(declared),
			mapKeysAbsent(actual, declared),
			mapKeysAbsent(declared, actual),
		)
	}
	if !reflect.DeepEqual(declared, actual) {
		t.Fatalf(
			"public surface manifest does not match exported declarations\nmissing: %v\nstale: %v",
			mapKeysAbsent(actual, declared),
			mapKeysAbsent(declared, actual),
		)
	}
}

func loadPublicSurfaceManifest(t *testing.T, root string) publicSurfaceManifest {
	t.Helper()
	path := filepath.Join(root, "exchange", "testdata", "public_surface_manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read public surface manifest %s: %v", path, err)
	}
	var manifest publicSurfaceManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("decode public surface manifest %s: %v", path, err)
	}
	return manifest
}

func discoverExportedSurface(t *testing.T, root string) map[string]string {
	t.Helper()
	surface := make(map[string]string)
	for _, packagePath := range []string{"exchange", "exchange/factory"} {
		directory := filepath.Join(root, filepath.FromSlash(packagePath))
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.AllErrors)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, declaration := range file.Decls {
				switch declaration := declaration.(type) {
				case *ast.GenDecl:
					for _, spec := range declaration.Specs {
						switch spec := spec.(type) {
						case *ast.TypeSpec:
							kind := "type"
							interfaceType, isInterface := spec.Type.(*ast.InterfaceType)
							if isInterface {
								kind = "interface"
							}
							if ast.IsExported(spec.Name.Name) {
								addSurfaceSymbol(t, surface, packagePath+"."+spec.Name.Name, kind)
							}
							if isInterface {
								for _, field := range interfaceType.Methods.List {
									for _, name := range field.Names {
										if ast.IsExported(name.Name) {
											addSurfaceSymbol(t, surface, packagePath+"."+spec.Name.Name+"."+name.Name, "interface_method")
										}
									}
								}
							}
						case *ast.ValueSpec:
							for _, name := range spec.Names {
								if ast.IsExported(name.Name) {
									addSurfaceSymbol(t, surface, packagePath+"."+name.Name, declaration.Tok.String())
								}
							}
						}
					}
				case *ast.FuncDecl:
					if !ast.IsExported(declaration.Name.Name) {
						continue
					}
					id := packagePath + "." + declaration.Name.Name
					kind := "func"
					if declaration.Recv != nil && len(declaration.Recv.List) == 1 {
						id = packagePath + "." + receiverTypeName(t, declaration.Recv.List[0].Type) + "." + declaration.Name.Name
						kind = "method"
					}
					addSurfaceSymbol(t, surface, id, kind)
				}
			}
		}
	}
	return surface
}

func addSurfaceSymbol(t *testing.T, surface map[string]string, id, kind string) {
	t.Helper()
	if prior, duplicate := surface[id]; duplicate {
		t.Fatalf("exported surface contains duplicate %s (%s and %s)", id, prior, kind)
	}
	surface[id] = kind
}

func receiverTypeName(t *testing.T, expression ast.Expr) string {
	t.Helper()
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.StarExpr:
		return receiverTypeName(t, expression.X)
	case *ast.IndexExpr:
		return receiverTypeName(t, expression.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t, expression.X)
	default:
		t.Fatalf("unsupported receiver expression %T", expression)
		return ""
	}
}

func discoverExchangeTests(t *testing.T, root string) map[string]bool {
	t.Helper()
	tests := make(map[string]bool)
	err := filepath.WalkDir(filepath.Join(root, "exchange"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && strings.HasPrefix(function.Name.Name, "Test") {
				tests[function.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tests
}

func mapKeysAbsent[V any, W any](left map[string]V, right map[string]W) []string {
	missing := make([]string, 0)
	for key := range left {
		if _, ok := right[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

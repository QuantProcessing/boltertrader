package exchange

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicPackagesHaveNoForbiddenLayerImportsOrRawEscapeSymbols(t *testing.T) {
	root := repositoryRoot(t)
	for _, relative := range []string{"exchange", "exchange/factory"} {
		directory := filepath.Join(root, filepath.FromSlash(relative))
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
				strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.AllErrors)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, imported := range file.Imports {
				value := strings.Trim(imported.Path.Value, `"`)
				for _, forbidden := range []string{
					"/sdk/", "/adapter/", "/runtime/",
				} {
					if strings.Contains(value, forbidden) {
						t.Errorf("%s imports forbidden path %q", path, value)
					}
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				identifier, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				switch strings.ToLower(identifier.Name) {
				case "rawparams", "nativeparams", "sdkclient", "nativeclient":
					t.Errorf("%s contains forbidden public escape symbol %q", path, identifier.Name)
				}
				return true
			})
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(workingDirectory)
}

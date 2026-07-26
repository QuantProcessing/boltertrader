package contracts

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayProductionImportBoundary(t *testing.T) {
	forbidden := []string{
		"github.com/QuantProcessing/boltertrader/runtime",
		"github.com/QuantProcessing/boltertrader/adapter",
		"github.com/QuantProcessing/boltertrader/strategy",
		"github.com/QuantProcessing/boltertrader/sdk",
	}
	allowedRepo := map[string]bool{
		"github.com/QuantProcessing/boltertrader/exchange":         true,
		"github.com/QuantProcessing/boltertrader/exchange/factory": true,
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			for _, bad := range forbidden {
				if importPath == bad || strings.HasPrefix(importPath, bad+"/") {
					t.Fatalf("%s imports forbidden package %s", path, importPath)
				}
			}
			if strings.HasPrefix(importPath, "github.com/QuantProcessing/boltertrader/gateway/") {
				continue
			}
			if strings.HasPrefix(importPath, "github.com/QuantProcessing/boltertrader/") && !allowedRepo[importPath] {
				t.Fatalf("%s imports unexpected repository package %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

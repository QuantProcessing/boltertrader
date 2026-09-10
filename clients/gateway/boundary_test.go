package gateway

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientAndWireHaveNoServiceOrSDKDependencies(t *testing.T) {
	const module = "github.com/QuantProcessing/boltertrader/"
	for _, directory := range []string{".", "../../api/exchange-gateway/v1"} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range file.Imports {
				value := strings.Trim(imp.Path.Value, "\"")
				if strings.HasPrefix(value, module) && value != module+"api/exchange-gateway/v1" {
					t.Errorf("%s imports implementation %s", path, value)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

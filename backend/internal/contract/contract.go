package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	productplatformcontract "github.com/QuantProcessing/boltertrader/internal/productplatformcontract"
)

type Contract = productplatformcontract.Contract

func Load() (productplatformcontract.Contract, error) {
	path, err := Path()
	if err != nil {
		return productplatformcontract.Contract{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return productplatformcontract.Contract{}, fmt.Errorf("read frozen contract: %w", err)
	}
	return productplatformcontract.Decode(data)
}

func Path() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("contract source path unavailable")
	}
	start := filepath.Dir(file)
	for dir := start; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "..", "..", "..", "api", "product-platform", "v1", "contract.json")
		if _, err := os.Stat(candidate); err == nil {
			return filepath.Clean(candidate), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", fmt.Errorf("frozen contract not found from %s", start)
}

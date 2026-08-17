package exchange

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type documentedProductRow struct {
	Venue         string
	Product       string
	FactoryConfig string
}

type operationMatrix map[string]map[string]string

func TestDocsMatchExchangeManifest(t *testing.T) {
	root := repositoryRoot(t)
	surface := loadPublicSurfaceManifest(t, root)
	wantRows := productRowsForDocs(t, surface.ProductRows)
	documentPaths := []struct {
		path   string
		header string
	}{
		{
			path:   filepath.Join(root, "docs", "reference", "exchange-rest-v1-operation-matrix.md"),
			header: "| Row | Venue | Product | Factory config |",
		},
		{
			path:   filepath.Join(root, "docs", "zh-CN", "reference", "exchange-rest-v1-operation-matrix.md"),
			header: "| Row | Venue | Product | Factory config |",
		},
		{
			path:   filepath.Join(root, "docs", "reference", "exchange-ws-v1-operation-matrix.md"),
			header: "| Row | Venue | Product | Factory config | WebSocket facet |",
		},
		{
			path:   filepath.Join(root, "docs", "zh-CN", "reference", "exchange-ws-v1-operation-matrix.md"),
			header: "| Row | Venue | Product | Factory config | WebSocket facet |",
		},
	}
	for _, documentPath := range documentPaths {
		path := documentPath.path
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read exchange matrix %s: %v", path, err)
		}
		gotRows := parseDocumentedProductRows(t, path, string(data), documentPath.header)
		if !reflect.DeepEqual(gotRows, wantRows) {
			t.Errorf("%s product rows = %#v, want manifest rows %#v", path, gotRows, wantRows)
		}
	}

	wantAcknowledgements := append([]string(nil), surface.Acknowledgements...)
	sort.Strings(wantAcknowledgements)
	publicAcknowledgements := []string{
		acknowledgementName(AckAcceptedPending),
		acknowledgementName(AckResting),
		acknowledgementName(AckPartiallyFilled),
		acknowledgementName(AckImmediatelyFilled),
		acknowledgementName(AckCanceled),
		acknowledgementName(AckRejected),
		acknowledgementName(AckAmbiguous),
	}
	sort.Strings(publicAcknowledgements)
	if !reflect.DeepEqual(wantAcknowledgements, publicAcknowledgements) {
		t.Fatalf(
			"manifest acknowledgement inventory = %v, want complete public contract %v",
			wantAcknowledgements,
			publicAcknowledgements,
		)
	}
	for _, path := range []string{
		filepath.Join(root, "docs", "reference", "exchange-rest-v1-operation-matrix.md"),
		filepath.Join(root, "docs", "zh-CN", "reference", "exchange-rest-v1-operation-matrix.md"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read REST operation matrix %s: %v", path, err)
		}
		got := parseDocumentedAcknowledgements(t, path, string(data))
		if !reflect.DeepEqual(got, wantAcknowledgements) {
			t.Errorf("%s acknowledgement inventory = %v, want manifest %v", path, got, wantAcknowledgements)
		}
	}
}

func TestOperationMatricesMatchExchangeManifest(t *testing.T) {
	root := repositoryRoot(t)
	surface := loadPublicSurfaceManifest(t, root)
	productCodes := []string{
		"BNS", "BNP", "OXS", "OXP", "LIS", "LIP", "HLS", "HLP",
		"BYS", "BYU", "BYC", "BGS", "BGU", "BGC", "GTS", "GTU",
		"ATS", "ATP", "NDS", "NDP",
	}

	tests := []struct {
		name      string
		header    string
		baseCells int
		paths     []string
		methods   func(exchangeProductRow) []string
	}{
		{
			name:      "REST",
			header:    "| Operation | Interface | BNS | BNP | OXS | OXP | LIS | LIP | HLS | HLP | BYS | BYU | BYC | BGS | BGU | BGC | GTS | GTU | ATS | ATP | NDS | NDP |",
			baseCells: 2,
			paths: []string{
				filepath.Join(root, "docs", "reference", "exchange-rest-v1-operation-matrix.md"),
				filepath.Join(root, "docs", "zh-CN", "reference", "exchange-rest-v1-operation-matrix.md"),
			},
			methods: func(row exchangeProductRow) []string { return row.RESTMethods },
		},
		{
			name:      "WebSocket",
			header:    "| Operation | Event type | Scope | BNS | BNP | OXS | OXP | LIS | LIP | HLS | HLP | BYS | BYU | BYC | BGS | BGU | BGC | GTS | GTU | ATS | ATP | NDS | NDP |",
			baseCells: 3,
			paths: []string{
				filepath.Join(root, "docs", "reference", "exchange-ws-v1-operation-matrix.md"),
				filepath.Join(root, "docs", "zh-CN", "reference", "exchange-ws-v1-operation-matrix.md"),
			},
			methods: func(row exchangeProductRow) []string { return row.WebSocketMethods },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := operationMatrixFromManifest(t, surface.ProductRows, productCodes, test.methods)
			got := make([]operationMatrix, 0, len(test.paths))
			for _, path := range test.paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read operation matrix %s: %v", path, err)
				}
				matrix := parseOperationMatrix(t, path, string(data), test.header, test.baseCells, productCodes)
				if !reflect.DeepEqual(matrix, want) {
					t.Errorf("%s operation matrix = %#v, want %#v", path, matrix, want)
				}
				got = append(got, matrix)
			}
			if !reflect.DeepEqual(got[0], got[1]) {
				t.Errorf("English and zh-CN %s operation matrices differ:\nEnglish: %#v\nzh-CN: %#v", test.name, got[0], got[1])
			}
		})
	}
}

func operationMatrixFromManifest(
	t *testing.T,
	rows []exchangeProductRow,
	productCodes []string,
	methods func(exchangeProductRow) []string,
) operationMatrix {
	t.Helper()
	byCode := productRowsByCode(t, rows)
	matrix := operationMatrix{}
	for _, code := range productCodes {
		row, ok := byCode[code]
		if !ok {
			t.Fatalf("manifest missing product row %s", code)
		}
		for _, method := range methods(row) {
			if matrix[method] == nil {
				matrix[method] = map[string]string{}
			}
			matrix[method][code] = "A"
		}
	}
	for method := range matrix {
		for _, code := range productCodes {
			if matrix[method][code] == "" {
				matrix[method][code] = "N/A"
			}
		}
	}
	return matrix
}

func parseOperationMatrix(
	t *testing.T,
	path string,
	document string,
	header string,
	baseCells int,
	productCodes []string,
) operationMatrix {
	t.Helper()
	rows := parseMarkdownTable(t, path, document, header)
	matrix := make(operationMatrix, len(rows))
	for _, row := range rows {
		if len(row) != baseCells+len(productCodes) {
			t.Fatalf("%s operation row has %d cells, want %d: %v", path, len(row), baseCells+len(productCodes), row)
		}
		method := markdownIdentifier(row[0])
		if _, duplicate := matrix[method]; duplicate {
			t.Fatalf("%s has duplicate operation row %s", path, method)
		}
		matrix[method] = make(map[string]string, len(productCodes))
		for index, code := range productCodes {
			value := markdownIdentifier(row[baseCells+index])
			if value != "A" && value != "N/A" {
				t.Fatalf("%s operation %s row %s has invalid support value %q", path, method, code, value)
			}
			matrix[method][code] = value
		}
	}
	return matrix
}

func parseDocumentedProductRows(
	t *testing.T,
	path string,
	document string,
	header string,
) map[string]documentedProductRow {
	t.Helper()
	rows := parseMarkdownTable(t, path, document, header)
	manifest := make(map[string]documentedProductRow, len(rows))
	for _, row := range rows {
		if len(row) < 4 {
			t.Fatalf("%s product row has %d cells, want at least 4: %v", path, len(row), row)
		}
		code := markdownIdentifier(row[0])
		if _, duplicate := manifest[code]; duplicate {
			t.Fatalf("%s has duplicate product row %s", path, code)
		}
		manifest[code] = documentedProductRow{
			Venue:         markdownIdentifier(row[1]),
			Product:       markdownIdentifier(row[2]),
			FactoryConfig: markdownIdentifier(row[3]),
		}
	}
	return manifest
}

func parseDocumentedAcknowledgements(t *testing.T, path, document string) []string {
	t.Helper()
	rows := parseMarkdownTable(t, path, document, "| State | Meaning |")
	states := make([]string, 0, len(rows))
	for _, row := range rows {
		if len(row) != 2 {
			t.Fatalf("%s acknowledgement row has %d cells, want 2: %v", path, len(row), row)
		}
		state := markdownIdentifier(row[0])
		state = strings.TrimPrefix(state, "Ack")
		if state == "" {
			t.Fatalf("%s has empty acknowledgement state: %v", path, row)
		}
		states = append(states, state)
	}
	sort.Strings(states)
	return states
}

func parseMarkdownTable(t *testing.T, path, document, header string) [][]string {
	t.Helper()
	lines := strings.Split(document, "\n")
	start := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == header {
			start = index + 2
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s missing machine-readable table header %q", path, header)
	}

	var rows [][]string
	for _, line := range lines[start:] {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			break
		}
		parts := strings.Split(strings.Trim(line, "|"), "|")
		row := make([]string, 0, len(parts))
		for _, part := range parts {
			row = append(row, strings.TrimSpace(part))
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatalf("%s table %q has no rows", path, header)
	}
	return rows
}

func markdownIdentifier(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "`")
	return strings.TrimSpace(value)
}

func productRowsForDocs(t *testing.T, rows []exchangeProductRow) map[string]documentedProductRow {
	t.Helper()
	manifest := make(map[string]documentedProductRow, len(rows))
	for _, row := range rows {
		if _, duplicate := manifest[row.Code]; duplicate {
			t.Fatalf("duplicate public surface product row %s", row.Code)
		}
		manifest[row.Code] = documentedProductRow{
			Venue:         row.Venue,
			Product:       row.Product,
			FactoryConfig: row.FactoryConfig,
		}
	}
	return manifest
}

func acknowledgementName(state OrderAckState) string {
	parts := strings.Split(string(state), "_")
	for index, part := range parts {
		if part == "" {
			continue
		}
		parts[index] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

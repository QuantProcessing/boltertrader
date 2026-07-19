package exchange

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type quickstartRow struct {
	Venue         string
	Product       string
	FactoryConfig string
}

type operationMatrix map[string]map[string]string

func TestDocsMatchExchangeManifest(t *testing.T) {
	root := repositoryRoot(t)
	paths := []string{
		filepath.Join(root, "docs", "guides", "exchange-rest-quickstart.md"),
		filepath.Join(root, "docs", "zh-CN", "guides", "exchange-rest-quickstart.md"),
	}

	type quickstartManifest struct {
		Rows             map[string]quickstartRow
		RESTMethods      map[string][2]string
		WebSocketMethods map[string][2]string
		Acknowledgements []string
	}

	surface := loadPublicSurfaceManifest(t, root)
	manifests := make([]quickstartManifest, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read quickstart %s: %v", path, err)
		}
		document := string(data)
		manifests = append(manifests, quickstartManifest{
			Rows:             parseQuickstartRows(t, path, document),
			RESTMethods:      parseQuickstartMethods(t, path, document, "| Method | Spot | Perp |"),
			WebSocketMethods: parseQuickstartMethods(t, path, document, "| Method | SpotWebSocket | PerpWebSocket | Scope |"),
			Acknowledgements: parseQuickstartFirstColumn(
				t,
				path,
				document,
				"| Acknowledgement | Meaning |",
			),
		})
	}

	wantRows := productRowsForDocs(t, surface.ProductRows)
	wantRESTMethods := methodSupportForDocs(surface.RESTMethods)
	wantWebSocketMethods := methodSupportForDocs(surface.WebSocketMethods)
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

	for index, manifest := range manifests {
		path := paths[index]
		for code, want := range wantRows {
			got, ok := manifest.Rows[code]
			if !ok {
				t.Errorf("%s missing product row %s", path, code)
				continue
			}
			if got != want {
				t.Errorf("%s row %s = %#v, want %#v", path, code, got, want)
			}
		}
		if len(manifest.Rows) != len(wantRows) {
			t.Errorf("%s has %d product rows, want %d: %#v", path, len(manifest.Rows), len(wantRows), manifest.Rows)
		}
		if !reflect.DeepEqual(manifest.RESTMethods, wantRESTMethods) {
			t.Errorf("%s REST method manifest = %#v, want %#v", path, manifest.RESTMethods, wantRESTMethods)
		}
		if !reflect.DeepEqual(manifest.WebSocketMethods, wantWebSocketMethods) {
			t.Errorf("%s WebSocket method manifest = %#v, want %#v", path, manifest.WebSocketMethods, wantWebSocketMethods)
		}
		if !reflect.DeepEqual(manifest.Acknowledgements, wantAcknowledgements) {
			t.Errorf("%s acknowledgement manifest = %v, want %v", path, manifest.Acknowledgements, wantAcknowledgements)
		}
	}

	if !reflect.DeepEqual(manifests[0], manifests[1]) {
		t.Errorf("English and zh-CN quickstart manifests differ:\nEnglish: %#v\nzh-CN: %#v", manifests[0], manifests[1])
	}
}

func TestOperationMatricesMatchExchangeManifest(t *testing.T) {
	root := repositoryRoot(t)
	surface := loadPublicSurfaceManifest(t, root)
	productCodes := []string{"BNS", "BNP", "OXS", "OXP", "LIS", "LIP", "HLS", "HLP"}

	tests := []struct {
		name      string
		header    string
		baseCells int
		paths     []string
		methods   func(exchangeProductRow) []string
	}{
		{
			name:      "REST",
			header:    "| Operation | Interface | BNS | BNP | OXS | OXP | LIS | LIP | HLS | HLP |",
			baseCells: 2,
			paths: []string{
				filepath.Join(root, "docs", "reference", "exchange-rest-v1-operation-matrix.md"),
				filepath.Join(root, "docs", "zh-CN", "reference", "exchange-rest-v1-operation-matrix.md"),
			},
			methods: func(row exchangeProductRow) []string { return row.RESTMethods },
		},
		{
			name:      "WebSocket",
			header:    "| Operation | Event type | Scope | BNS | BNP | OXS | OXP | LIS | LIP | HLS | HLP |",
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

func parseQuickstartRows(t *testing.T, path, document string) map[string]quickstartRow {
	t.Helper()
	rows := parseMarkdownTable(t, path, document, "| Row | Venue | Product | Factory config |")
	manifest := make(map[string]quickstartRow, len(rows))
	for _, row := range rows {
		if len(row) != 4 {
			t.Fatalf("%s product row has %d cells, want 4: %v", path, len(row), row)
		}
		code := markdownIdentifier(row[0])
		if _, duplicate := manifest[code]; duplicate {
			t.Fatalf("%s has duplicate product row %s", path, code)
		}
		manifest[code] = quickstartRow{
			Venue:         markdownIdentifier(row[1]),
			Product:       markdownIdentifier(row[2]),
			FactoryConfig: markdownIdentifier(row[3]),
		}
	}
	return manifest
}

func parseQuickstartMethods(t *testing.T, path, document, header string) map[string][2]string {
	t.Helper()
	rows := parseMarkdownTable(t, path, document, header)
	manifest := make(map[string][2]string, len(rows))
	for _, row := range rows {
		if len(row) < 3 {
			t.Fatalf("%s method row has %d cells, want at least 3: %v", path, len(row), row)
		}
		method := markdownIdentifier(row[0])
		if _, duplicate := manifest[method]; duplicate {
			t.Fatalf("%s has duplicate method row %s", path, method)
		}
		manifest[method] = [2]string{
			strings.ToLower(markdownIdentifier(row[1])),
			strings.ToLower(markdownIdentifier(row[2])),
		}
	}
	return manifest
}

func parseQuickstartFirstColumn(t *testing.T, path, document, header string) []string {
	t.Helper()
	rows := parseMarkdownTable(t, path, document, header)
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		if len(row) != 2 {
			t.Fatalf("%s table %q row has %d cells, want 2: %v", path, header, len(row), row)
		}
		values = append(values, markdownIdentifier(row[0]))
	}
	sort.Strings(values)
	return values
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

func productRowsForDocs(t *testing.T, rows []exchangeProductRow) map[string]quickstartRow {
	t.Helper()
	manifest := make(map[string]quickstartRow, len(rows))
	for _, row := range rows {
		if _, duplicate := manifest[row.Code]; duplicate {
			t.Fatalf("duplicate public surface product row %s", row.Code)
		}
		manifest[row.Code] = quickstartRow{
			Venue:         row.Venue,
			Product:       row.Product,
			FactoryConfig: row.FactoryConfig,
		}
	}
	return manifest
}

func methodSupportForDocs(rows []exchangeMethodSupport) map[string][2]string {
	manifest := make(map[string][2]string, len(rows))
	for _, row := range rows {
		manifest[row.Method] = [2]string{yesNo(row.Spot), yesNo(row.Perp)}
	}
	return manifest
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
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

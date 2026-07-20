package exchange

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
)

const exchangeDocCompileMarker = "<!-- exchange-doc-example:compile -->"

var (
	markdownLinkPattern         = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	explicitAnchorPattern       = regexp.MustCompile(`(?i)<a\s+(?:name|id)=["']([^"']+)["'][^>]*>`)
	inlineCodePattern           = regexp.MustCompile("`([^`\\n]+)`")
	credentialAssignmentPattern = regexp.MustCompile(
		"(?i)\\b[A-Z][A-Z0-9_]*(?:API_?KEY|SECRET(?:_KEY)?|TOKEN|PASSPHRASE|PRIVATE_KEY)\\s*=\\s*([^\\s`]+)",
	)
	rawLogPattern = regexp.MustCompile(
		`(?m)^20[0-9]{2}-[0-9]{2}-[0-9]{2}T[^\n]*\[(?:INFO|DEBUG|ERROR|WARN)\]`,
	)
	privateKeyPattern = regexp.MustCompile(`BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY`)
)

type exchangeDocPair struct {
	english string
	chinese string
}

type markedGoExample struct {
	path string
	line int
	code string
}

func TestExchangeDocsNavigationAndLinks(t *testing.T) {
	root := repositoryRoot(t)
	requiredLinks := map[string][]string{
		"README.md": {
			"docs/exchange/README.md",
		},
		"README.zh-CN.md": {
			"docs/zh-CN/exchange/README.md",
		},
		"docs/README.md": {
			"exchange/README.md",
		},
		"docs/zh-CN/README.md": {
			"exchange/README.md",
		},
		"docs/exchange/README.md": {
			"../concepts/exchange.md",
			"../guides/exchange-rest-quickstart.md",
			"../reference/exchange-rest-v1-operation-matrix.md",
			"../reference/exchange-ws-v1-operation-matrix.md",
			"../reference/exchange-venues-and-account-modes.md",
			"../reference/testing.md",
			"../reference/configuration.md",
		},
		"docs/zh-CN/exchange/README.md": {
			"../concepts/exchange.md",
			"../guides/exchange-rest-quickstart.md",
			"../reference/exchange-rest-v1-operation-matrix.md",
			"../reference/exchange-ws-v1-operation-matrix.md",
			"../reference/exchange-venues-and-account-modes.md",
			"../reference/testing.md",
			"../reference/configuration.md",
		},
	}

	for relativePath, targets := range requiredLinks {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		document := readExchangeDoc(t, path)
		for _, target := range targets {
			if !strings.Contains(document, "]("+target+")") {
				t.Errorf("%s missing required link target %s", relativePath, target)
			}
		}
	}

	for _, relativePath := range exchangeDocSystemPaths() {
		validateExchangeDocLinks(t, root, filepath.Join(root, filepath.FromSlash(relativePath)))
	}
}

func TestExchangeDocsRejectVenueRowReordering(t *testing.T) {
	expected := []string{"Binance", "OKX", "Lighter"}
	reordered := []string{"OKX", "Binance", "Lighter"}
	if err := validateExchangeVenueOrder(expected, reordered); err == nil {
		t.Fatal("venue order validation accepted reordered rows")
	}
}

func TestExchangeDocsEnforceMirrorLinkLocality(t *testing.T) {
	root := filepath.Clean("/repository")
	chineseSource := filepath.Join(root, "docs", "zh-CN", "concepts", "exchange.md")
	englishSource := filepath.Join(root, "docs", "concepts", "exchange.md")

	tests := []struct {
		name    string
		source  string
		target  string
		wantErr bool
	}{
		{
			name:   "Chinese same-tree link",
			source: chineseSource,
			target: filepath.Join(root, "docs", "zh-CN", "reference", "testing.md"),
		},
		{
			name:   "Chinese counterpart link",
			source: chineseSource,
			target: englishSource,
		},
		{
			name:   "Chinese code link",
			source: chineseSource,
			target: filepath.Join(root, "exchange", "contract.go"),
		},
		{
			name:    "Chinese cross-tree content link",
			source:  chineseSource,
			target:  filepath.Join(root, "docs", "reference", "testing.md"),
			wantErr: true,
		},
		{
			name:   "English same-tree link",
			source: englishSource,
			target: filepath.Join(root, "docs", "reference", "testing.md"),
		},
		{
			name:   "English counterpart link",
			source: englishSource,
			target: chineseSource,
		},
		{
			name:    "English cross-tree content link",
			source:  englishSource,
			target:  filepath.Join(root, "docs", "zh-CN", "reference", "testing.md"),
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateExchangeMirrorLinkLocality(root, test.source, test.target)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateExchangeMirrorLinkLocality() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestExchangeDocsMirrorStructure(t *testing.T) {
	root := repositoryRoot(t)
	for _, pair := range exchangeDocPairs() {
		englishPath := filepath.Join(root, filepath.FromSlash(pair.english))
		chinesePath := filepath.Join(root, filepath.FromSlash(pair.chinese))
		english := readExchangeDoc(t, englishPath)
		chinese := readExchangeDoc(t, chinesePath)

		if got, want := markdownHeadingLevels(english), markdownHeadingLevels(chinese); !reflect.DeepEqual(got, want) {
			t.Errorf("%s and %s heading levels differ:\nEnglish: %v\nChinese: %v", pair.english, pair.chinese, got, want)
		}
		if got, want := markdownTableShapes(english), markdownTableShapes(chinese); !reflect.DeepEqual(got, want) {
			t.Errorf("%s and %s table shapes differ:\nEnglish: %v\nChinese: %v", pair.english, pair.chinese, got, want)
		}
		if got, want := markdownFenceLanguages(english), markdownFenceLanguages(chinese); !reflect.DeepEqual(got, want) {
			t.Errorf("%s and %s fenced-code languages differ:\nEnglish: %v\nChinese: %v", pair.english, pair.chinese, got, want)
		}
		if got, want := markdownInlineCodeTokens(english), markdownInlineCodeTokens(chinese); !reflect.DeepEqual(got, want) {
			t.Errorf("%s and %s inline technical tokens differ:\nEnglish: %v\nChinese: %v", pair.english, pair.chinese, got, want)
		}
	}

	for _, relativePath := range []string{
		"docs/concepts/exchange.md",
		"docs/zh-CN/concepts/exchange.md",
	} {
		document := readExchangeDoc(t, filepath.Join(root, filepath.FromSlash(relativePath)))
		for _, token := range []string{
			"`factory.New`",
			"`factory.Config[C]`",
			"`exchange.SpotClient`",
			"`exchange.PerpClient`",
			"`context.Context`",
			"`decimal.Decimal`",
			"`exchange.PlaceOrderRequest`",
			"`exchange.OrderAcknowledgement`",
			"`exchange.Subscription[T]`",
			"`exchange.ErrUnsupported`",
		} {
			if !strings.Contains(document, token) {
				t.Errorf("%s missing required concept token %s", relativePath, token)
			}
		}
	}
}

func TestExchangeVenueDifferencesMatchManifest(t *testing.T) {
	root := repositoryRoot(t)
	surface := loadPublicSurfaceManifest(t, root)
	expected := make(map[string][]string)
	var expectedOrder []string
	for _, row := range surface.ProductRows {
		if _, seen := expected[row.Venue]; !seen {
			expectedOrder = append(expectedOrder, row.Venue)
		}
		expected[row.Venue] = append(expected[row.Venue], row.Code)
	}
	if len(expected) != 9 {
		t.Fatalf("public surface manifest has %d venues, want 9", len(expected))
	}
	for venue := range expected {
		sort.Strings(expected[venue])
	}
	requiredVenueTokens := map[string][]string{
		"Binance":     {"`BNS`", "`BNP`", "`BOTH`", "`SetLeverage`"},
		"OKX":         {"`OXS`", "`OXP`", "`cross`", "`net`", "`SetLeverage`"},
		"Lighter":     {"`LIS`", "`LIP`", "`accountIndex`", "`keyIndex`", "`SetLeverage`"},
		"Hyperliquid": {"`HLS`", "`HLP`", "`WithAccountAddress`", "`cross`", "`isolated`"},
		"Bybit":       {"`BYS`", "`BYU`", "`BYC`", "`UNIFIED`", "`positionIdx=0`"},
		"Bitget":      {"`BGS`", "`BGU`", "`BGC`", "`UTA`", "`one_way_mode`", "`hedge_mode`", "`crossed`"},
		"Gate":        {"`GTS`", "`GTU`", "`classic`", "`multi_currency`"},
		"Aster":       {"`ATS`", "`ATP`", "`BOTH`", "`SetLeverage`"},
		"Nado":        {"`NDS`", "`NDP`", "`Effective=0`", "`0x`", "`WatchMarkPrice`", "`exchange.ErrUnsupported`"},
	}

	const header = "| Venue | Rows | Account system | Logical account identity | Position mode | Margin mode | Leverage | Order caveats |"
	for _, relativePath := range []string{
		"docs/reference/exchange-venues-and-account-modes.md",
		"docs/zh-CN/reference/exchange-venues-and-account-modes.md",
	} {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		document := readExchangeDoc(t, path)
		rows := parseMarkdownTable(t, path, document, header)
		actual := make(map[string][]string, len(rows))
		actualOrder := make([]string, 0, len(rows))
		for _, row := range rows {
			if len(row) != 8 {
				t.Fatalf("%s venue row has %d cells, want 8: %v", relativePath, len(row), row)
			}
			venue := markdownIdentifier(row[0])
			if _, duplicate := actual[venue]; duplicate {
				t.Fatalf("%s has duplicate venue row %s", relativePath, venue)
			}
			actualOrder = append(actualOrder, venue)
			actual[venue] = exchangeRowCodes(row[1])
			joined := strings.Join(row, " | ")
			for _, token := range requiredVenueTokens[venue] {
				if !strings.Contains(joined, token) {
					t.Errorf("%s venue %s missing required grounded token %s", relativePath, venue, token)
				}
			}
			for index, value := range row[2:] {
				value = strings.TrimSpace(markdownIdentifier(value))
				if value == "" || strings.EqualFold(value, "TBD") {
					t.Errorf("%s venue %s dimension %d is not documented", relativePath, venue, index+1)
				}
			}
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Errorf("%s venue rows = %#v, want manifest venues %#v", relativePath, actual, expected)
		}
		if err := validateExchangeVenueOrder(expectedOrder, actualOrder); err != nil {
			t.Errorf("%s: %v", relativePath, err)
		}
		if strings.Contains(document, "| Operation | Interface | BNS |") ||
			strings.Contains(document, "| Operation | Event type | Scope | BNS |") {
			t.Errorf("%s duplicates a complete operation matrix", relativePath)
		}
	}

	for _, relativePath := range []string{
		"docs/guides/exchange-rest-quickstart.md",
		"docs/zh-CN/guides/exchange-rest-quickstart.md",
	} {
		document := readExchangeDoc(t, filepath.Join(root, filepath.FromSlash(relativePath)))
		for _, forbiddenHeader := range []string{
			"| Row | Venue | Product | Factory config |",
			"| Method | Spot | Perp |",
			"| Method | SpotWebSocket | PerpWebSocket | Scope |",
			"| Row | Acceptance status | Reason |",
		} {
			if strings.Contains(document, forbiddenHeader) {
				t.Errorf("%s still duplicates canonical inventory %q", relativePath, forbiddenHeader)
			}
		}
	}
}

func TestExchangeDocsExamplesCompile(t *testing.T) {
	root := repositoryRoot(t)
	var examples []markedGoExample
	for _, relativePath := range exchangeDocSystemPaths() {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		examples = append(examples, extractMarkedGoExamples(t, path, readExchangeDoc(t, path))...)
	}
	if len(examples) < 2 {
		t.Fatalf("found %d marked complete Go examples, want at least one English/Chinese mirror pair", len(examples))
	}

	for index, example := range examples {
		example := example
		t.Run(fmt.Sprintf("%03d_%s_line_%d", index+1, filepath.Base(example.path), example.line), func(t *testing.T) {
			t.Parallel()
			if !strings.HasPrefix(strings.TrimSpace(example.code), "package ") {
				t.Fatalf("%s:%d marked Go example must be a complete package", example.path, example.line)
			}
			compileExchangeDocExample(t, root, example)
		})
	}
}

func TestExchangeDocsPublicSafety(t *testing.T) {
	root := repositoryRoot(t)
	var markdownFiles []string
	for _, relativePath := range []string{"README.md", "README.zh-CN.md"} {
		markdownFiles = append(markdownFiles, filepath.Join(root, relativePath))
	}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			markdownFiles = append(markdownFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range markdownFiles {
		document := readExchangeDoc(t, path)
		for _, forbidden := range []string{".omx/", "/Users/", "/private/"} {
			if strings.Contains(document, forbidden) {
				t.Errorf("%s contains private path marker %q", path, forbidden)
			}
		}
		if rawLogPattern.MatchString(document) {
			t.Errorf("%s contains raw timestamped log output", path)
		}
		if privateKeyPattern.MatchString(document) {
			t.Errorf("%s contains a private-key block marker", path)
		}
		for _, match := range credentialAssignmentPattern.FindAllStringSubmatch(document, -1) {
			if !safeDocumentationCredentialValue(match[1]) {
				t.Errorf("%s contains credential-shaped literal %q", path, match[0])
			}
		}
	}

	terminologyPaths := append([]string(nil), exchangeDocSystemPaths()...)
	terminologyPaths = append(terminologyPaths,
		"docs/reference/exchange-rest-v1-operation-matrix.md",
		"docs/zh-CN/reference/exchange-rest-v1-operation-matrix.md",
		"docs/reference/exchange-ws-v1-operation-matrix.md",
		"docs/zh-CN/reference/exchange-ws-v1-operation-matrix.md",
	)
	for _, relativePath := range terminologyPaths {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		if strings.Contains(strings.ToLower(readExchangeDoc(t, path)), "facade") {
			t.Errorf("%s contains stale facade terminology", relativePath)
		}
	}
}

func exchangeDocPairs() []exchangeDocPair {
	return []exchangeDocPair{
		{
			english: "docs/exchange/README.md",
			chinese: "docs/zh-CN/exchange/README.md",
		},
		{
			english: "docs/concepts/exchange.md",
			chinese: "docs/zh-CN/concepts/exchange.md",
		},
		{
			english: "docs/reference/exchange-venues-and-account-modes.md",
			chinese: "docs/zh-CN/reference/exchange-venues-and-account-modes.md",
		},
		{
			english: "docs/guides/exchange-rest-quickstart.md",
			chinese: "docs/zh-CN/guides/exchange-rest-quickstart.md",
		},
		{
			english: "docs/reference/exchange-rest-v1-operation-matrix.md",
			chinese: "docs/zh-CN/reference/exchange-rest-v1-operation-matrix.md",
		},
		{
			english: "docs/reference/exchange-ws-v1-operation-matrix.md",
			chinese: "docs/zh-CN/reference/exchange-ws-v1-operation-matrix.md",
		},
	}
}

func exchangeDocSystemPaths() []string {
	var paths []string
	for _, pair := range exchangeDocPairs() {
		paths = append(paths, pair.english, pair.chinese)
	}
	return paths
}

func readExchangeDoc(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read documentation %s: %v", path, err)
	}
	return string(data)
}

func validateExchangeDocLinks(t *testing.T, root, sourcePath string) {
	t.Helper()
	document := readExchangeDoc(t, sourcePath)
	for _, match := range markdownLinkPattern.FindAllStringSubmatch(document, -1) {
		rawTarget := strings.TrimSpace(match[1])
		parsed, err := url.Parse(rawTarget)
		if err != nil {
			t.Errorf("%s has invalid link %q: %v", sourcePath, rawTarget, err)
			continue
		}
		if parsed.IsAbs() || parsed.Host != "" || parsed.Scheme == "mailto" {
			continue
		}
		targetPath := sourcePath
		if parsed.Path != "" {
			unescapedPath, err := url.PathUnescape(parsed.Path)
			if err != nil {
				t.Errorf("%s has invalid escaped link path %q: %v", sourcePath, rawTarget, err)
				continue
			}
			targetPath = filepath.Clean(filepath.Join(filepath.Dir(sourcePath), filepath.FromSlash(unescapedPath)))
		}
		relative, err := filepath.Rel(root, targetPath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Errorf("%s link %q resolves outside repository", sourcePath, rawTarget)
			continue
		}
		if err := validateExchangeMirrorLinkLocality(root, sourcePath, targetPath); err != nil {
			t.Errorf("%s link %q: %v", sourcePath, rawTarget, err)
			continue
		}
		info, err := os.Stat(targetPath)
		if err != nil {
			t.Errorf("%s link %q resolves to missing target %s: %v", sourcePath, rawTarget, targetPath, err)
			continue
		}
		if info.IsDir() {
			targetPath = filepath.Join(targetPath, "README.md")
		}
		if parsed.Fragment == "" {
			continue
		}
		fragment, err := url.PathUnescape(parsed.Fragment)
		if err != nil {
			t.Errorf("%s has invalid fragment %q: %v", sourcePath, parsed.Fragment, err)
			continue
		}
		if !markdownAnchors(readExchangeDoc(t, targetPath))[fragment] {
			t.Errorf("%s link %q has no page-local anchor %q in %s", sourcePath, rawTarget, fragment, targetPath)
		}
	}
}

func validateExchangeVenueOrder(expected, actual []string) error {
	if reflect.DeepEqual(actual, expected) {
		return nil
	}
	return fmt.Errorf("venue row order = %v, want %v", actual, expected)
}

func validateExchangeMirrorLinkLocality(root, sourcePath, targetPath string) error {
	docsRoot := filepath.Join(root, "docs")
	chineseRoot := filepath.Join(docsRoot, "zh-CN")
	if !pathWithin(docsRoot, targetPath) {
		return nil
	}

	sourceIsChinese := pathWithin(chineseRoot, sourcePath)
	targetIsChinese := pathWithin(chineseRoot, targetPath)
	if sourceIsChinese == targetIsChinese {
		return nil
	}

	var counterpart string
	if sourceIsChinese {
		relative, err := filepath.Rel(chineseRoot, sourcePath)
		if err != nil {
			return fmt.Errorf("resolve Chinese counterpart: %w", err)
		}
		counterpart = filepath.Join(docsRoot, relative)
	} else {
		relative, err := filepath.Rel(docsRoot, sourcePath)
		if err != nil {
			return fmt.Errorf("resolve English counterpart: %w", err)
		}
		counterpart = filepath.Join(chineseRoot, relative)
	}
	if filepath.Clean(targetPath) == filepath.Clean(counterpart) {
		return nil
	}
	return fmt.Errorf("cross-language documentation target %s is not counterpart %s", targetPath, counterpart)
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func markdownAnchors(document string) map[string]bool {
	anchors := make(map[string]bool)
	duplicates := make(map[string]int)
	inFence := false
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, match := range explicitAnchorPattern.FindAllStringSubmatch(line, -1) {
			anchors[match[1]] = true
		}
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		base := githubStyleSlug(heading)
		if base == "" {
			continue
		}
		slug := base
		if duplicates[base] > 0 {
			slug = fmt.Sprintf("%s-%d", base, duplicates[base])
		}
		duplicates[base]++
		anchors[slug] = true
	}
	return anchors
}

func githubStyleSlug(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	lastWasSpace := false
	for _, current := range value {
		switch {
		case unicode.IsLetter(current), unicode.IsNumber(current), current == '-', current == '_':
			if lastWasSpace && builder.Len() > 0 && !strings.HasSuffix(builder.String(), "-") {
				builder.WriteRune('-')
			}
			lastWasSpace = false
			builder.WriteRune(current)
		case unicode.IsSpace(current):
			lastWasSpace = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func markdownHeadingLevels(document string) []int {
	var levels []int
	inFence := false
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level < len(trimmed) && trimmed[level] == ' ' {
			levels = append(levels, level)
		}
	}
	return levels
}

func markdownTableShapes(document string) [][]int {
	var tables [][]int
	inFence := false
	inTable := false
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			cellCount := len(strings.Split(strings.Trim(trimmed, "|"), "|"))
			if !inTable {
				tables = append(tables, nil)
				inTable = true
			}
			tables[len(tables)-1] = append(tables[len(tables)-1], cellCount)
			continue
		}
		inTable = false
	}
	return tables
}

func markdownFenceLanguages(document string) []string {
	var languages []string
	inFence := false
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "```") {
			continue
		}
		if !inFence {
			languages = append(languages, strings.TrimSpace(strings.TrimPrefix(trimmed, "```")))
		}
		inFence = !inFence
	}
	return languages
}

func markdownInlineCodeTokens(document string) []string {
	var tokens []string
	inFence := false
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, match := range inlineCodePattern.FindAllStringSubmatch(line, -1) {
			tokens = append(tokens, match[1])
		}
	}
	sort.Strings(tokens)
	return tokens
}

func exchangeRowCodes(value string) []string {
	var codes []string
	for _, field := range strings.FieldsFunc(value, func(current rune) bool {
		return current == ',' || unicode.IsSpace(current) || current == '`'
	}) {
		if len(field) == 3 {
			codes = append(codes, field)
		}
	}
	sort.Strings(codes)
	return codes
}

func extractMarkedGoExamples(t *testing.T, path, document string) []markedGoExample {
	t.Helper()
	lines := strings.Split(document, "\n")
	var examples []markedGoExample
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index-1]) != exchangeDocCompileMarker ||
			strings.TrimSpace(lines[index]) != "```go" {
			continue
		}
		end := index + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "```" {
			end++
		}
		if end == len(lines) {
			t.Fatalf("%s:%d marked Go example has no closing fence", path, index+1)
		}
		examples = append(examples, markedGoExample{
			path: path,
			line: index + 1,
			code: strings.Join(lines[index+1:end], "\n") + "\n",
		})
		index = end
	}
	return examples
}

func compileExchangeDocExample(t *testing.T, root string, example markedGoExample) {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte(example.code), 0o600); err != nil {
		t.Fatal(err)
	}
	goMod := fmt.Sprintf(
		"module example.invalid/exchange-doc/%d\n\ngo 1.26\n\nrequire github.com/QuantProcessing/boltertrader v0.0.0\n\nreplace github.com/QuantProcessing/boltertrader => %s\n",
		example.line,
		filepath.ToSlash(root),
	)
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "./...")
	command.Dir = directory
	command.Env = replaceProcessEnvironment(os.Environ(), map[string]string{
		"GOPROXY": "off",
		"GOSUMDB": "off",
		"GOWORK":  "off",
		"GOFLAGS": "-mod=mod",
	})
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s:%d example compilation timed out: %v", example.path, example.line, ctx.Err())
	}
	if err != nil {
		t.Fatalf("%s:%d example does not compile: %v\n%s", example.path, example.line, err, output)
	}
}

func replaceProcessEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replaced := replacements[name]; replaced {
				continue
			}
		}
		result = append(result, entry)
	}
	for name, value := range replacements {
		result = append(result, name+"="+value)
	}
	return result
}

func safeDocumentationCredentialValue(value string) bool {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	lower := strings.ToLower(value)
	return strings.HasPrefix(value, "<") ||
		strings.HasPrefix(value, "${") ||
		strings.HasPrefix(value, "$") ||
		lower == "redacted" ||
		lower == "placeholder" ||
		lower == "example" ||
		lower == "dummy"
}

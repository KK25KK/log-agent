package runbookmock

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestRuntimePackageHasNoNetworkConfigOrRealAdapterDependency(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]struct{}{
		"context":                  {},
		"errors":                   {},
		"sync":                     {},
		"time":                     {},
		"logagent/internal/domain": {},
		"logagent/internal/ports":  {},
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := allowed[path]; !ok {
				t.Fatalf("runtime runbook Mock imports disallowed dependency %q", path)
			}
		}
	}
}

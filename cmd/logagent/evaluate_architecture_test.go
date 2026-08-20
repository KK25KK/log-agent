package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEvaluationPathDoesNotImportRealProvidersOrNetwork(t *testing.T) {
	t.Parallel()
	files := []string{"evaluate.go"}
	for _, root := range []string{
		filepath.Join("..", "..", "internal", "evaluation"),
		filepath.Join("..", "..", "internal", "adapters", "evalmock"),
		filepath.Join("..", "..", "internal", "adapters", "replayfs"),
		filepath.Join("..", "..", "internal", "observability"),
	} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() && filepath.Ext(path) == ".go" {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if forbiddenEvaluationImport(path) {
				t.Fatalf("%s imports forbidden real-provider or network package %q", file, path)
			}
		}
	}
}

func forbiddenEvaluationImport(path string) bool {
	if path == "net" || strings.HasPrefix(path, "net/") || path == "os/signal" || path == "logagent/internal/config" {
		return true
	}
	return path == "logagent/internal/adapters/feishu" || path == "logagent/internal/adapters/aliyunsls"
}
